package workspacecommands

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Contract is the provider contract's version (FR-001).
const Contract = 1

// ListLimit is what one list gives, at most.
const ListLimit = 200

// Timing is how long a command may take, and how often a start or stop asks whether it is
// done: first after PollEvery, then twice as long each time, up to PollMost (#1854).
type Timing struct {
	Quick     time.Duration
	Long      time.Duration
	PollEvery time.Duration
	PollMost  time.Duration
	// Sleep is how a start or stop waits between its lists; nil is time.Sleep. A test
	// records the waits asked for, not the time between lists (#5654).
	Sleep func(time.Duration)
}

func (t Timing) sleep(d time.Duration) {
	if t.Sleep != nil {
		t.Sleep(d)
		return
	}
	time.Sleep(d)
}

// StandardTiming is under the control plane's own 30 seconds and 10 minutes, so its failure
// is ours, with our reason, rather than its timeout. A start asks at 2, 6, 14 and 30 seconds,
// then every 20: a cloud's list is a real API call (#1854).
var StandardTiming = Timing{Quick: 25 * time.Second, Long: 595 * time.Second, PollEvery: 2 * time.Second, PollMost: 20 * time.Second}

// Failure is a failure in words for the person who pressed Start. Again is a refusal worth
// asking again while a start or stop waits (a 429, a 5xx, a provider that can't be reached),
// and After how long the provider asked to wait first (Retry-After) (#1854).
type Failure struct {
	Reason string
	Again  bool
	After  time.Duration
	// Status is the HTTP status of a provider's refusal, or 0 (#6073).
	Status int
}

func (f Failure) Error() string { return f.Reason }

// Workspace is one workspace as a list found it.
type Workspace struct {
	ID    string
	Name  string
	State State
	// Values are what its templates are filled in with: id, name, and a listing's columns
	// or fields.
	Values   map[string]string
	Commands Commands
}

// SSH is how to reach it, or nil when its line has a value nothing filled in.
func (w Workspace) SSH() *string {
	if w.Commands.SSH == nil {
		return nil
	}
	line := Fill(*w.Commands.SSH, w.Values, false)
	if strings.Contains(line, "${") {
		return nil
	}
	return &line
}

// Addresses are its public addresses, IPv4 then IPv6, as its listing gave them (#7466):
// the ip and ip6 values.
func (w Workspace) Addresses() []any {
	var out []any
	for _, key := range []string{"ip", "ip6"} {
		if v := strings.TrimSpace(w.Values[key]); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// JSON is the workspace as the contract gives it.
func (w Workspace) JSON() map[string]any {
	o := map[string]any{"id": w.ID, "name": w.Name, "state": string(w.State), "tunnel": w.Commands.Tunnel}
	if ssh := w.SSH(); ssh != nil {
		o["ssh"] = *ssh
	}
	if a := w.Addresses(); len(a) > 0 {
		o["addresses"] = a
	}
	if w.Commands.HostKeyRenews {
		o["hostKeyRenews"] = true
	}
	if w.Commands.Cost != nil {
		o["cost"] = Fill(*w.Commands.Cost, w.Values, false)
	}
	// The provider's own idle timeout, as a codespace's list says it (#4803).
	if n, err := strconv.Atoi(w.Values["idleTimeoutMinutes"]); err == nil && n > 0 {
		o["idleTimeoutMinutes"] = n
	}
	return o
}

// Provider is the provider's state: only what a start or stop left, for workspaces nothing
// else can ask. It keeps nothing on disk. A workspace with no way to ask its state (no
// status command, no listing) is as its last start or stop left it while the server runs,
// and unknown before.
type Provider struct {
	Config Config
	Timing Timing
	Run    Runner
	// Log is where a start, stop, create or delete is said; the standard logger when nil.
	Log func(string)
	// Fetch does a request (#1119); Fetch when nil. Env is where its secrets are;
	// os.LookupEnv when nil.
	Fetch Fetcher
	Env   Environment
	// Folder is where it keeps what it makes for itself, such as the Agent Loops ssh key of
	// a DigitalOcean provider (#6073): the folder it was started in. None keeps nothing.
	Folder string
	// google keeps Google Cloud's access tokens until they end.
	google googleTokens

	mu   sync.Mutex
	left map[string]State
}

func (p *Provider) log(line string) {
	if p.Log != nil {
		p.Log(line)
	} else {
		log.Print(line)
	}
}

func (p *Provider) leftState(id string) (State, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.left[id]
	return s, ok
}

func (p *Provider) setLeft(id string, s State, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.left == nil {
		p.left = map[string]State{}
	}
	if ok {
		p.left[id] = s
	} else {
		delete(p.left, id)
	}
}

// List is every workspace.
func (p *Provider) List() ([]Workspace, error) {
	if p.Config.Listing != nil {
		return p.listed(*p.Config.Listing)
	}
	entries := p.Config.Workspaces
	if len(entries) > ListLimit {
		entries = entries[:ListLimit]
	}
	found := make([]Workspace, len(entries))
	var wg sync.WaitGroup
	for i, entry := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			found[i] = p.written(entry)
		}()
	}
	wg.Wait()
	return found, nil
}

// written is a workspace written out, with its state from its status command if it has one.
func (p *Provider) written(entry Entry) Workspace {
	values := map[string]string{"id": entry.ID, "name": entry.Name}
	state, ok := p.leftState(entry.ID)
	if !ok {
		state = Unknown
	}
	if entry.Commands.Status != "" {
		outcome := p.Run(Fill(entry.Commands.Status, values, true), p.Timing.Quick)
		if outcome.Status == 0 {
			state = p.Config.StateFor(outcome.Text())
		} else {
			state = Unknown
		}
	}
	return Workspace{ID: entry.ID, Name: entry.Name, State: state, Values: values, Commands: entry.Commands}
}

func (p *Provider) listed(listing Listing) ([]Workspace, error) {
	outcome, read, err := p.runListing(listing)
	if err != nil {
		return nil, err
	}
	var rows []map[string]string
	if listing.Shape.Fields == nil {
		rows = columnRows(outcome.Text(), listing.Shape.Columns)
	} else {
		rows, err = jsonRows(listing, outcome.Output, read)
		if err != nil {
			return nil, err
		}
	}
	if len(rows) > ListLimit {
		rows = rows[:ListLimit]
	}
	var out []Workspace
	for _, row := range rows {
		if w, ok := p.listedWorkspace(listing, row); ok {
			out = append(out, w)
		}
	}
	return out, nil
}

// runListing runs a listing's request or command, and gives its outcome and the
// function that reads the output into a value.
func (p *Provider) runListing(listing Listing) (Outcome, func([]byte) (any, error), error) {
	if listing.Request != nil {
		answer, err := p.call(*listing.Request, map[string]string{}, p.Timing.Quick, "listing workspaces")
		if err != nil {
			return Outcome{}, nil, err
		}
		return Outcome{Output: answer}, listing.Request.value, nil
	}
	outcome := p.Run(listing.Command, p.Timing.Quick)
	if outcome.Status != 0 || outcome.TimedOut {
		return Outcome{}, nil, Failure{Reason: reason(outcome, listing.Command, "listing workspaces", p.Timing.Quick)}
	}
	return outcome, decode, nil
}

// columnRows reads a list command's lines into rows, a value for each column.
func columnRows(text string, columns []string) []map[string]string {
	var rows []map[string]string
	for _, line := range strings.FieldsFunc(text, isNewline) {
		fields := splitColumns(line, len(columns)-1)
		if len(fields) == 0 {
			continue
		}
		row := map[string]string{}
		for i := 0; i < len(columns) && i < len(fields); i++ {
			if fields[i] != "" {
				row[columns[i]] = strings.TrimFunc(fields[i], isBlank)
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// jsonRows reads the array at the listing's items path into rows, a value for each of
// its json fields.
func jsonRows(listing Listing, output []byte, read func([]byte) (any, error)) ([]map[string]string, error) {
	value, err := read(output)
	found := PathValue(listing.Shape.Items, value)
	array, ok := found.([]any)
	// An API leaves its list out when it is empty (Compute Engine's items).
	if _, isObject := value.(map[string]any); err == nil && found == nil && isObject && listing.Request != nil {
		array, ok = []any{}, true
	}
	if err != nil || !ok {
		return nil, notAnArray(listing)
	}
	var rows []map[string]string
	for _, item := range array {
		row := map[string]string{}
		for key, path := range listing.Shape.Fields {
			if text, ok := PathText(path, item); ok && text != "" {
				row[key] = text
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// notAnArray is the failure when a listing's output has no array at its items path.
func notAnArray(listing Listing) Failure {
	at := listing.Shape.Items
	if at == "" {
		at = "its top"
	}
	what := "The list command's output"
	if listing.Request != nil {
		what = "The list request's answer"
	}
	format := "JSON"
	if listing.Request != nil && listing.Request.Answer == "xml" {
		format = "XML"
	}
	return Failure{Reason: what + " isn't a " + format + " array at " + at + "."}
}

// listedWorkspace is the workspace a row names, with the listing's defaults under the
// row's values; false when the row has no id.
func (p *Provider) listedWorkspace(listing Listing, row map[string]string) (Workspace, bool) {
	id, ok := row["id"]
	if !ok {
		return Workspace{}, false
	}
	values := map[string]string{}
	for k, v := range listing.Defaults {
		values[k] = v
	}
	for k, v := range row {
		values[k] = v
	}
	name, ok := row["name"]
	if !ok {
		name = id
	}
	values["name"] = name
	state := Unknown
	if said, ok := row["state"]; ok {
		state = p.Config.StateFor(said)
	} else if left, ok := p.leftState(id); ok {
		state = left
	}
	return Workspace{ID: id, Name: name, State: state, Values: values, Commands: listing.Commands}, true
}

func isNewline(r rune) bool {
	return r == '\n' || r == '\r' || r == '\v' || r == '\f' || r == 0x85 || r == 0x2028 || r == 0x2029
}

// splitColumns is Swift's split(maxSplits:whereSeparator: isWhitespace): runs of whitespace
// part the fields, and once maxSplits are made the rest of the line, from just after the
// last separator, is the last.
func splitColumns(line string, maxSplits int) []string {
	var out []string
	start := 0
	for i, r := range line {
		if !unicode.IsSpace(r) {
			continue
		}
		appended := false
		if i > start {
			out = append(out, line[start:i])
			appended = true
		}
		start = i + len(string(r))
		if appended && len(out) == maxSplits {
			break
		}
	}
	if start < len(line) {
		out = append(out, line[start:])
	}
	return out
}

func (p *Provider) find(id string) (Workspace, error) {
	if p.Config.Listing == nil {
		for _, entry := range p.Config.Workspaces {
			if entry.ID == id {
				return p.written(entry), nil
			}
		}
	}
	all, err := p.List()
	if err != nil {
		return Workspace{}, err
	}
	for _, w := range all {
		if w.ID == id {
			return w, nil
		}
	}
	return Workspace{}, Failure{Reason: "There is no workspace " + id + "."}
}

// canAsk is whether anything but our own memory can say how the workspace stands.
func (p *Provider) canAsk(w Workspace) bool {
	return p.Config.Listing != nil || w.Commands.Status != ""
}

// Start starts the workspace and answers once it is running.
func (p *Provider) Start(id string) (Workspace, error) { return p.act(true, id) }

// Stop stops the workspace and answers once it is stopped.
func (p *Provider) Stop(id string) (Workspace, error) { return p.act(false, id) }

func (p *Provider) act(start bool, id string) (Workspace, error) {
	began := time.Now()
	action, doing, target := "stop", "stopping", Stopped
	if start {
		action, doing, target = "start", "starting", Running
	}
	workspace, err := p.find(id)
	if err != nil {
		return Workspace{}, err
	}
	if workspace.State == target {
		return workspace, nil
	}
	template, request := workspace.Commands.Stop, workspace.Commands.StopRequest
	if start {
		template, request = workspace.Commands.Start, workspace.Commands.StartRequest
	}
	if err := p.do(template, request, workspace.Values, action+" "+workspace.Name, doing+" "+workspace.Name); err != nil {
		return Workspace{}, err
	}
	if !p.canAsk(workspace) {
		p.setLeft(id, target, true)
		workspace.State = target
		return workspace, nil
	}
	// Asked less often as it goes on, and a refusal worth asking again is asked again, after
	// what the provider asked for, until Long is up (#1854).
	wait, most := p.Timing.PollEvery, max(p.Timing.PollMost, p.Timing.PollEvery)
	var refused error
	for {
		found, err := p.find(id)
		var failure Failure
		switch {
		case err == nil && found.State == target:
			return found, nil
		case err == nil:
			workspace, refused = found, nil
		case errors.As(err, &failure) && failure.Again:
			refused = err
			wait = max(wait, failure.After)
		default:
			return Workspace{}, err
		}
		left := p.Timing.Long - time.Since(began)
		if left <= 0 {
			break
		}
		p.Timing.sleep(min(wait, left))
		wait = min(wait*2, most)
	}
	if refused != nil {
		return Workspace{}, refused
	}
	return Workspace{}, Failure{Reason: fmt.Sprintf("%s was still %s after %s.", workspace.Name, workspace.State, words(p.Timing.Long))}
}

// Create makes a workspace (US5) with the choices given, and answers once it is listed,
// with what it did, in words, one line a step (#6073).
func (p *Provider) Create(name string, options map[string]any, firstBoot string) (Workspace, []string, error) {
	return p.CreateRooted(name, options, firstBoot, "")
}

// CreateRooted is Create with rootBoot too (#7154): the lines that run the person's root.sh,
// filled in where the create has ${rootBoot}, and nothing where it has none.
func (p *Provider) CreateRooted(name string, options map[string]any, firstBoot, rootBoot string) (Workspace, []string, error) {
	making := p.Config.Making
	if !making.CanCreate() {
		return Workspace{}, nil, Failure{Reason: "It can't make workspaces."}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Workspace{}, nil, Failure{Reason: "A new machine needs a name."}
	}
	all, err := p.List()
	if err != nil {
		return Workspace{}, nil, err
	}
	for _, w := range all {
		if w.Name == name || w.ID == name {
			return Workspace{}, nil, Failure{Reason: "There is already a workspace called " + name + "."}
		}
	}
	values := map[string]string{"name": name, "firstBoot": firstBoot, "rootBoot": rootBoot}
	removeRootBoot, err := putRootBootFile(making, values)
	if err != nil {
		return Workspace{}, nil, err
	}
	defer removeRootBoot()
	// A choice left out takes the form's default, as the form showed it: the account's
	// choices where the preset reads them (#6686), else the plain form.
	chosen := map[string]any{}
	properties, _ := making.Options["properties"].(map[string]any)
	form := properties
	for key := range properties {
		if _, given := options[key]; !given && making.Choices != nil {
			form, _ = making.Form(p)["properties"].(map[string]any)
			break
		}
	}
	for key, property := range form {
		if d, ok := property.(map[string]any)["default"]; ok {
			chosen[key] = d
		}
	}
	for key, value := range options {
		chosen[key] = value
	}
	putChoices(values, chosen)
	// A repository as owner/name (#1809) is also its two parts, for an address such as
	// /repos/${repository.owner}/${repository.name}/codespaces.
	if owner, name, ok := strings.Cut(values["repository"], "/"); ok && owner != "" && name != "" && !strings.Contains(name, "/") {
		values["repository.owner"], values["repository.name"] = owner, name
	} else if making.CreateRequest != nil && strings.Contains(making.CreateRequest.URL, "${repository.") {
		return Workspace{}, nil, Failure{Reason: "Give the repository as owner/name."}
	}
	request, err := withIntegers(making.CreateRequest, properties, values)
	if err != nil {
		return Workspace{}, nil, err
	}
	said, err := p.make(request, values, name)
	if err != nil {
		return Workspace{}, nil, err
	}
	all, err = p.List()
	if err != nil {
		return Workspace{}, nil, err
	}
	for _, w := range all {
		if w.Name == name || w.ID == name {
			return w, said, nil
		}
	}
	return Workspace{}, nil, Failure{Reason: name + " was made, but the list doesn't show it yet."}
}

// make is a make's steps (#6073): Prepare, the create itself, and what it made, in words. A
// create the provider refused says that nothing was made, when the preset says how.
func (p *Provider) make(request *Request, values map[string]string, name string) ([]string, error) {
	making := p.Config.Making
	var said []string
	if making.Prepare != nil && request != nil {
		var steps []string
		var err error
		if request, steps, err = making.Prepare(p, request, values); err != nil {
			return nil, err
		}
		said = append(said, steps...)
	}
	// The first-boot command carries a host code: logged as the template, never filled in.
	if err := p.do(making.Create, request, values, "create "+name, "making "+name); err != nil {
		var refused Failure
		if making.NotMade != "" && errors.As(err, &refused) && refused.Status != 0 {
			refused.Reason += " " + making.NotMade
			return nil, refused
		}
		return nil, err
	}
	if making.Made != nil {
		said = append(said, making.Made(values))
	}
	return said, nil
}

// withIntegers is the request with each body value that is only ${key}, for an option of
// type integer, as a JSON number (#4636): an API such as GitHub's refuses "240" for 240. A
// form can give the number as a string, as the Mac's picker gives an enum's choice.
func withIntegers(request *Request, properties map[string]any, values map[string]string) (*Request, error) {
	if request == nil {
		return nil, nil
	}
	numbers := map[string]json.Number{}
	for key, property := range properties {
		if kind, _ := property.(map[string]any)["type"].(string); kind != "integer" {
			continue
		}
		value, ok := values[key]
		if !ok {
			continue
		}
		if _, err := strconv.Atoi(value); err != nil {
			title, _ := property.(map[string]any)["title"].(string)
			if title == "" {
				title = key
			}
			return nil, Failure{Reason: title + " must be a whole number."}
		}
		numbers["${"+key+"}"] = json.Number(value)
	}
	if len(numbers) == 0 {
		return request, nil
	}
	var typed func(any) any
	typed = func(v any) any {
		switch t := v.(type) {
		case string:
			if n, ok := numbers[t]; ok {
				return n
			}
		case []any:
			out := make([]any, len(t))
			for i, e := range t {
				out[i] = typed(e)
			}
			return out
		case map[string]any:
			out := make(map[string]any, len(t))
			for k, e := range t {
				out[k] = typed(e)
			}
			return out
		}
		return v
	}
	copied := *request
	copied.Body = typed(request.Body)
	return &copied, nil
}

// Delete deletes the workspace and everything on it.
func (p *Provider) Delete(id string) error {
	making := p.Config.Making
	if !making.CanDelete() {
		return Failure{Reason: "It can't delete workspaces."}
	}
	workspace, err := p.find(id)
	if err != nil {
		return err
	}
	if err := p.do(making.Delete, making.DeleteRequest, workspace.Values, "delete "+workspace.Name, "deleting "+workspace.Name); err != nil {
		return err
	}
	p.setLeft(id, "", false)
	return nil
}

// do runs the command, or does the request in its place, with the values filled in. The log
// says the template, never what was filled in: a first-boot command carries a host code.
func (p *Provider) do(template string, request *Request, values map[string]string, what, doing string) error {
	if request != nil {
		p.log(fmt.Sprintf("workspace-commands: %s: %s", what, request.Said()))
		_, err := p.call(*request, values, p.Timing.Long, doing)
		return err
	}
	p.log(fmt.Sprintf("workspace-commands: %s: %s", what, template))
	outcome := p.Run(Fill(template, values, true), p.Timing.Long)
	if outcome.Status != 0 || outcome.TimedOut {
		return Failure{Reason: reason(outcome, template, doing, p.Timing.Long)}
	}
	return nil
}

// reason is why a command failed: its own last lines, or that its CLI isn't there.
func reason(outcome Outcome, line, doing string, deadline time.Duration) string {
	if outcome.TimedOut {
		return strings.ToUpper(doing[:1]) + doing[1:] + " took longer than " + words(deadline) + "."
	}
	if outcome.Failure != "" {
		return outcome.Failure
	}
	if outcome.Status == 127 {
		cli := line
		if f := strings.Fields(line); len(f) > 0 {
			cli = f[0]
		}
		return cli + " isn't installed where the control plane runs."
	}
	said := outcome.ErrorText()
	if strings.TrimSpace(said) == "" {
		said = outcome.Text()
	}
	lines := strings.FieldsFunc(said, isNewline)
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	text := strings.TrimSpace(strings.Join(lines, "\n"))
	if text == "" {
		first, _, _ := strings.Cut(line, " ")
		return fmt.Sprintf("%s exited with %d.", first, outcome.Status)
	}
	if len(text) > 1024 {
		text = strings.ToValidUTF8(text[len(text)-1024:], "�")
	}
	return text
}

func words(d time.Duration) string {
	seconds := int(d / time.Second)
	if seconds >= 60 {
		return fmt.Sprintf("%d minutes", (seconds+30)/60)
	}
	return fmt.Sprintf("%d seconds", seconds)
}

// putChoices puts each choice in values as text, for the templates. The make's own values (its
// name and what its first boot runs) are never a choice.
func putChoices(values map[string]string, chosen map[string]any) {
	for key, value := range chosen {
		if key == "name" || key == "firstBoot" || key == "rootBoot" {
			// The make's own: no choice is them.
			continue
		}
		switch v := value.(type) {
		case string:
			values[key] = v
		case json.Number:
			values[key] = v.String()
		case float64:
			values[key] = fmt.Sprint(v)
		case int:
			values[key] = strconv.Itoa(v)
		case bool:
			values[key] = fmt.Sprint(v)
		}
	}
}
