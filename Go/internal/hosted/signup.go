package hosted

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/signin"
	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/store"
)

// Who may sign up (#2152, spec 1003 FR-001–FR-012). A person with an account signs in as
// before, never limited. A new one is checked in order: blocked (by sign-in or by email),
// an account for their email already, the mode, allowed (the allowlist, an invite, or open
// with a verified email), the GitHub account's age, the cap on accounts. One who may not
// have an account yet goes on the waitlist. Making an account or a waitlist entry counts
// against the rates; showing a place already kept does not.
//
// Open sign-up (#3388, decided in #3387) needs an email the provider verified: Google's
// email_verified, Apple's email (Apple gives only verified ones, a private relay address
// too), GitHub's verified primary. One email has one account, whichever provider signs up
// with it. The service keeps only the email's keyed hash, its mark (direction.md principle
// 7). Cloudflare Turnstile, when its keys are set, is on the sign-up page (turnstile.go).

// The modes of Config.SignUp.
const (
	SignUpInvite = "invite"
	SignUpOpen   = "open"
	SignUpClosed = "closed"
)

// The defaults of Config's limits (spec 1003, Clarifications).
const (
	defaultMaxAccounts       = 100
	defaultMinGitHubAge      = 30 * 24 * time.Hour
	defaultSignUpsPerAddress = 3
	defaultSignUpsPerHour    = 30
	defaultMaxCopies         = 50
	defaultCertsPerHour      = 10
	defaultCertsPerWeek      = 40
	// InviteLife is how long an invite link can be used.
	InviteLife = 14 * 24 * time.Hour
	// waitlistMost bounds the waitlist; the rates fill it slowly.
	waitlistMost = 10000
)

// Why a person is on the waitlist, as the page says it.
const (
	whyNoInvite   = "no-invite"
	whyInviteUsed = "invite-used"
	whyAge        = "age"
	whyCap        = "cap"
)

func allowKey(login string) string {
	return allowFolder + strings.ToLower(login) + ".json"
}
func inviteKey(code string) string { return invitesFolder + code + ".json" }
func waitingKey(user SignIn) string { return waitingFolder + user.who() + ".json" }
func blockedKey(user SignIn) string { return blockedFolder + user.who() + ".json" }
func blockedEmailKey(mark string) string {
	return blockedEmailFolder + mark + ".json"
}
func waitlistKey(place string) string { return waitlistFolder + place + ".json" }

// Their bounds are in recordbounds.go.
const (
	// store-bounds: kind=document; count=1_000; size=4KiB a record; age=90d; applied=on write (refused), and the hourly sweep deletes one past its expiry
	invitesFolder = hostedPrefix + "v1/invites/"
	// store-bounds: kind=key-value; count=10_000; size=4KiB a record; age=90d; applied=on write (refused) and hourly sweep
	allowFolder = hostedPrefix + "v1/allow/"
	// store-bounds: kind=key-value; count=10_000; size=4KiB a record; age=until-deleted; applied=on write (refused), and no age: a block that aged out would let the person back in
	blockedFolder = hostedPrefix + "v1/blocked/"
	// store-bounds: kind=key-value; count=10_000; size=4KiB a record; age=until-deleted; applied=on write (refused), and no age: a block that aged out would let the person back in
	blockedEmailFolder = hostedPrefix + "v1/blocked/email/"
	// store-bounds: kind=document; count=10_000; size=4KiB a record; age=90d; applied=on write (refused) and hourly sweep
	waitingFolder = hostedPrefix + "v1/waiting/"
	// store-bounds: kind=document; count=10_000; size=4KiB a record; age=90d; applied=on write (refused) and hourly sweep
	waitlistFolder = hostedPrefix + "v1/waitlist/"
)

// Invite is an invite link's record.
type Invite struct {
	Expires time.Time `json:"expires"`
	Used    *Used     `json:"used,omitempty"`
}

// Used is who used an invite, and when.
type Used struct {
	By *SignIn `json:"by,omitempty"`
	// The GitHub user who used it before #3386.
	GitHub int64     `json:"github,omitempty"`
	At     time.Time `json:"at"`
}

// Waiting is a sign-in on the waitlist.
type Waiting struct {
	SignIn SignIn `json:"signIn"`
	At     time.Time `json:"at"`
	Why    string    `json:"why"`
	// Its key in the waitlist folder, whose order is the waitlist's.
	Place string `json:"place"`
}

// waitlistLink is a place's record: whose it is. One from before #3386 says only GitHub.
type waitlistLink struct {
	SignIn *SignIn `json:"signIn,omitempty"`
	GitHub int64   `json:"github,omitempty"`
}

func (l waitlistLink) signIn() SignIn {
	if l.SignIn != nil {
		return *l.SignIn
	}
	return oldGitHubSignIn(l.GitHub)
}

// emailMark is an email's mark: HMAC-SHA256 of the address in lower case, under a key made
// from the service's own key, so the store says nothing of who signed up, and a list of
// addresses cannot be tried against it without the keeper. "" for no email.
func emailMark(key []byte, email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return ""
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(email))
	return hex.EncodeToString(m.Sum(nil))
}

// emailMarkKey is the key of emailMark, from the service's own key.
func emailMarkKey(own []byte) []byte {
	return controllink.HKDF(own, []byte("agents-hosted-email-v1"), nil, 32)
}

// Block is a sign-in, or an email, that may not sign up.
type Block struct {
	Login  string    `json:"login,omitempty"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

// validLogin is a GitHub login: letters, digits and hyphens, at most 39.
func validLogin(login string) bool {
	if login == "" || len(login) > 39 {
		return false
	}
	for _, c := range login {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// validInvite is an invite code: twenty letters and digits.
func validInvite(code string) bool {
	if len(code) != 20 {
		return false
	}
	for i := 0; i < len(code); i++ {
		if c := code[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func newInviteCode() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = idLetters[int(b[i])%len(idLetters)]
	}
	return string(b)
}

// signUpResult is what the gate decided: an account, a place on the waitlist, or a
// refusal in words.
type signUpResult struct {
	account *Account
	made    bool
	place   int
	why     string
	refused string
	// What kind of refusal, for the metrics.
	kind   string
	status int
}

// gate decides what a sign-in with no account gets.
func (s *Service) gate(ctx context.Context, from string, user SignIn, invite string) signUpResult {
	if !user.valid() {
		return unreadable(fmt.Errorf("%s is not a sign-in", user))
	}
	mark := emailMark(s.markKey, user.Email)
	if res, done := s.blockedOrKnown(ctx, user, mark); done {
		return res
	}
	if s.cfg.SignUp == SignUpClosed {
		return signUpResult{refused: "Sign-up is not open yet.", status: http.StatusServiceUnavailable, kind: "closed"}
	}
	gitHub := user.Provider == signin.GitHub
	listed, err := s.allowListed(ctx, user)
	if err != nil {
		return unreadable(err)
	}
	// Open is open to a sign-in with a verified email (#3387); GitHub's age is checked too.
	open := s.cfg.SignUp == SignUpOpen
	allowed := open && mark != "" || listed
	var inv *store.Object
	if !allowed && validInvite(invite) {
		inv, err = s.unusedInvite(ctx, invite)
		if err != nil {
			return unreadable(err)
		}
		allowed = inv != nil
	}
	if !allowed && open && mark == "" && invite == "" {
		return signUpResult{refused: noEmailText(user.Provider), status: http.StatusForbidden, kind: "no_email"}
	}
	why, err := s.whyWait(ctx, user, invite, allowed, gitHub && !listed)
	if err != nil {
		return unreadable(err)
	}
	if why != "" {
		return s.wait(ctx, from, user, why, why == whyCap, false)
	}
	return s.admit(ctx, from, user, mark, invite, inv)
}

// blockedOrKnown gives the result for a sign-in that is blocked, by its own key or its
// email's, or whose email has an account already; done is false for any other.
func (s *Service) blockedOrKnown(ctx context.Context, user SignIn, mark string) (res signUpResult, done bool) {
	st := s.cfg.Store
	blocked := signUpResult{refused: "Sign-up is not open to this " + signin.Title(user.Provider) + " account.", status: http.StatusForbidden, kind: "blocked"}
	if o, err := st.Get(ctx, blockedKey(user)); err != nil {
		return unreadable(err), true
	} else if o != nil {
		return blocked, true
	}
	if mark == "" {
		return signUpResult{}, false
	}
	if o, err := st.Get(ctx, blockedEmailKey(mark)); err != nil {
		return unreadable(err), true
	} else if o != nil {
		return blocked, true
	}
	// One account per email (#3388): another provider's sign-in with the same email
	// gets the account that email has.
	if acc, err := s.accounts.byEmail(ctx, mark); err != nil {
		return unreadable(err), true
	} else if acc != nil {
		s.leaveWaitlist(ctx, user)
		return signUpResult{account: acc}, true
	}
	return signUpResult{}, false
}

// allowListed says whether a GitHub sign-in's login is on the allow list.
func (s *Service) allowListed(ctx context.Context, user SignIn) (bool, error) {
	if user.Provider != signin.GitHub || !validLogin(user.Login) {
		return false, nil
	}
	o, err := s.cfg.Store.Get(ctx, allowKey(user.Login))
	if err != nil {
		return false, err
	}
	return o != nil, nil
}

// unusedInvite gives the invite's record when it is there, unused and not expired, else nil.
func (s *Service) unusedInvite(ctx context.Context, invite string) (*store.Object, error) {
	o, err := s.cfg.Store.Get(ctx, inviteKey(invite))
	if err != nil {
		return nil, err
	}
	var rec Invite
	if o != nil && json.Unmarshal(o.Data, &rec) == nil && rec.Used == nil && s.cfg.Now().Before(rec.Expires) {
		return o, nil
	}
	return nil, nil
}

// whyWait gives why a sign-in goes on the waitlist, or "" when it may sign up now.
// ageChecked says its GitHub account's age counts.
func (s *Service) whyWait(ctx context.Context, user SignIn, invite string, allowed, ageChecked bool) (string, error) {
	switch {
	case !allowed && invite != "":
		return whyInviteUsed, nil
	case !allowed:
		return whyNoInvite, nil
	case ageChecked && s.cfg.Now().Sub(user.Created) < s.cfg.MinGitHubAge:
		return whyAge, nil
	}
	n, err := s.accounts.count(ctx)
	if err != nil {
		return "", err
	}
	if n >= s.cfg.MaxAccounts {
		return whyCap, nil
	}
	room, err := s.roomFor(ctx, plans[PlanFree].Storage)
	if err != nil {
		return "", err
	}
	if !room {
		// The plans' storage would add up to more than the disk (#3121).
		return whyCap, nil
	}
	return "", nil
}

// admit makes the account of a sign-in that may sign up, using its invite (inv) when it
// came by one.
func (s *Service) admit(ctx context.Context, from string, user SignIn, mark, invite string, inv *store.Object) signUpResult {
	if !s.rates.allow(from, s.cfg.Now(), s.cfg.SignUpsPerAddress, s.cfg.SignUpsPerHour) {
		return tooMany()
	}
	if inv != nil {
		var rec Invite
		_ = json.Unmarshal(inv.Data, &rec)
		by := SignIn{Provider: user.Provider, Subject: user.Subject, Login: user.Login}
		rec.Used = &Used{By: &by, At: s.cfg.Now().UTC()}
		data, _ := json.Marshal(rec)
		if err := putRecord(ctx, s.cfg.Store, inviteKey(invite), data, store.Matching(inv.ETag)); err != nil {
			if !store.IsConflict(err) {
				return unreadable(err)
			}
			// Someone else used it first.
			return s.wait(ctx, from, user, whyInviteUsed, false, true)
		}
	}
	acc, made, err := s.accounts.makeMarked(ctx, user, mark)
	if err != nil || acc == nil {
		return unreadable(err)
	}
	s.leaveWaitlist(ctx, user)
	return signUpResult{account: acc, made: made}
}

// wait puts a sign-in on the waitlist, at the front for one who may sign up once there is
// room, or gives their place when they are on it already. counted says the rates have
// counted this sign-up already.
func (s *Service) wait(ctx context.Context, from string, user SignIn, why string, front, counted bool) signUpResult {
	st := s.cfg.Store
	o, err := st.Get(ctx, waitingKey(user))
	if err != nil {
		return unreadable(err)
	}
	var entry Waiting
	if o != nil && json.Unmarshal(o.Data, &entry) == nil {
		return s.placeOf(ctx, entry)
	}
	if !counted && !s.rates.allow(from, s.cfg.Now(), s.cfg.SignUpsPerAddress, s.cfg.SignUpsPerHour) {
		return tooMany()
	}
	keys, err := st.List(ctx, waitlistFolder)
	if err != nil {
		return unreadable(err)
	}
	if len(keys) >= waitlistMost {
		return signUpResult{refused: "The waitlist is full just now. Try again another day.", status: http.StatusServiceUnavailable, kind: "waitlist_full"}
	}
	now := s.cfg.Now().UTC()
	// Front places sort before the others, each in the order they came.
	order := "b"
	if front {
		order = "a"
	}
	kept := SignIn{Provider: user.Provider, Subject: user.Subject, Login: user.Login}
	// The place's key ends with a short hash of the sign-in, so two at once have two.
	sum := sha256.Sum256([]byte(user.String()))
	entry = Waiting{SignIn: kept, At: now, Why: why,
		Place: fmt.Sprintf("%s%020d-%s", order, now.UnixNano(), hex.EncodeToString(sum[:6]))}
	data, _ := json.Marshal(entry)
	if err := putRecord(ctx, st, waitingKey(user), data, store.Absent); err != nil {
		if store.IsConflict(err) {
			// Another tab put them on it at the same time.
			return s.wait(ctx, from, user, why, front, true)
		}
		return unreadable(err)
	}
	link, _ := json.Marshal(waitlistLink{SignIn: &SignIn{Provider: user.Provider, Subject: user.Subject}})
	if err := putRecord(ctx, st, waitlistKey(entry.Place), link, store.Absent); err != nil {
		return unreadable(err)
	}
	s.metrics.signedUp("waitlist")
	s.log(fmt.Sprintf("waitlist: %s (%s)", user, why))
	return s.placeOf(ctx, entry)
}

// placeOf is a waitlist entry's place, from one.
func (s *Service) placeOf(ctx context.Context, entry Waiting) signUpResult {
	keys, err := s.cfg.Store.List(ctx, waitlistFolder)
	if err != nil {
		return unreadable(err)
	}
	place := sort.Search(len(keys), func(i int) bool { return keys[i].Key >= waitlistKey(entry.Place) }) + 1
	return signUpResult{place: place, why: entry.Why}
}

// leaveWaitlist takes a sign-in off the waitlist, once they have an account or are blocked.
func (s *Service) leaveWaitlist(ctx context.Context, user SignIn) {
	leaveWaitlist(ctx, s.cfg.Store, user)
}

func leaveWaitlist(ctx context.Context, st store.Store, user SignIn) {
	o, err := st.Get(ctx, waitingKey(user))
	if err != nil || o == nil {
		return
	}
	var entry Waiting
	if json.Unmarshal(o.Data, &entry) == nil && entry.Place != "" {
		_ = st.Delete(ctx, waitlistKey(entry.Place))
	}
	_ = st.Delete(ctx, waitingKey(user))
}

// whyText is a waitlist reason in words.
func whyText(why string, minAge time.Duration) string {
	switch why {
	case whyInviteUsed:
		return "That invite is used or expired."
	case whyAge:
		return fmt.Sprintf("Your GitHub account is newer than %d days.", int(minAge.Hours()/24))
	case whyCap:
		return "Agent Loops has as many accounts as it can take for now."
	default:
		return "Sign-up is by invitation while Agent Loops is in beta."
	}
}

// noEmailText says why a sign-in with no verified email cannot sign up.
func noEmailText(provider string) string {
	title := signin.Title(provider)
	if provider == signin.GitHub {
		return "Sign-up needs an email address that GitHub has verified, as your primary email. Verify one at github.com/settings/emails, then sign up again."
	}
	return "Sign-up needs an email address that " + title + " has verified, and " + title + " gave none. Sign up with another account."
}

func unreadable(err error) signUpResult {
	_ = err
	return signUpResult{refused: "Your account could not be read or made. Try again in a moment.", status: http.StatusServiceUnavailable, kind: "store"}
}

func tooMany() signUpResult {
	return signUpResult{refused: "Too many sign-ups just now. Try again in an hour.", status: http.StatusTooManyRequests, kind: "rate"}
}

// rates are the sign-ups of the last hour, by address and in all (FR-006). They are in
// memory: the service is one process, and a restart starts them again.
type rates struct {
	mu  sync.Mutex
	by  map[string][]time.Time
	all []time.Time
}

// allow counts a sign-up from an address, if it is under both rates.
func (r *rates) allow(from string, now time.Time, perAddress, perHour int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	since := now.Add(-time.Hour)
	if r.by == nil {
		r.by = map[string][]time.Time{}
	}
	r.all = recent(r.all, since)
	mine := recent(r.by[from], since)
	// Bound the map: drop the addresses with nothing left in the hour.
	if len(r.by) > 256 {
		for k, v := range r.by {
			if len(recent(v, since)) == 0 {
				delete(r.by, k)
			}
		}
	}
	if len(mine) >= perAddress || len(r.all) >= perHour {
		r.by[from] = mine
		return false
	}
	r.by[from] = append(mine, now)
	r.all = append(r.all, now)
	return true
}

// recent is the times after since, in order.
func recent(times []time.Time, since time.Time) []time.Time {
	i := 0
	for i < len(times) && !times[i].After(since) {
		i++
	}
	return times[i:]
}

var errFull = errors.New("the service is full")
