package workspacecommands

// DigitalOcean's set-up on its API (#6073): the Agent Loops ssh key, the New Machine form from
// the account, and what a make says it did, against a fake DigitalOcean. No real account is
// reached.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// doAccount is a fake account's keys, regions and sizes, laid over fakeDigitalOcean.
type doAccount struct {
	keys        map[string]bool // fingerprints on the account
	keyStatus   int             // the status every key request gets, when not 0
	addStatus   int             // the status a key's add gets, when not 0
	dropletNo   string          // a refusal of POST /v2/droplets, when not ""
	regionsFail bool
}

func fakeDigitalOceanAccount(t *testing.T, a *doAccount) (*fakeAPI, string) {
	t.Helper()
	f, text := fakeDigitalOcean(t)
	droplets := f.answer
	f.answer = func(w http.ResponseWriter, r *http.Request, body string) bool {
		path := r.URL.Path
		switch {
		case strings.HasPrefix(path, "/v2/account/keys"):
			if a.keyStatus != 0 {
				w.WriteHeader(a.keyStatus)
				fmt.Fprint(w, `{"id":"forbidden","message":"You are not authorized to perform this operation."}`)
				return true
			}
			if r.Method == http.MethodGet {
				fingerprint := strings.TrimPrefix(path, "/v2/account/keys/")
				f.mu.Lock()
				on := a.keys[fingerprint]
				f.mu.Unlock()
				if !on {
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprint(w, `{"id":"not_found","message":"The resource you were accessing could not be found."}`)
					return true
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ssh_key": map[string]any{"id": 1, "fingerprint": fingerprint,
					"name": "Agent Loops"}})
				return true
			}
			if a.addStatus != 0 {
				w.WriteHeader(a.addStatus)
				fmt.Fprint(w, `{"id":"server_error","message":"Something went wrong."}`)
				return true
			}
			var added map[string]string
			_ = json.Unmarshal([]byte(body), &added)
			key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(added["public_key"]))
			if err != nil || added["name"] != "Agent Loops" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				fmt.Fprint(w, `{"id":"unprocessable_entity","message":"Key invalid"}`)
				return true
			}
			f.mu.Lock()
			a.keys[ssh.FingerprintLegacyMD5(key)] = true
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"ssh_key":{"id":2}}`)
			return true
		case path == "/v2/regions":
			if a.regionsFail {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"id":"server_error","message":"Something went wrong."}`)
				return true
			}
			fmt.Fprint(w, `{"regions":[
				{"slug":"nyc3","name":"New York 3","available":true},
				{"slug":"sfo3","name":"San Francisco 3","available":true},
				{"slug":"ams3","name":"Amsterdam 3","available":true},
				{"slug":"nyc2","name":"New York 2","available":false}]}`)
			return true
		case path == "/v2/sizes":
			fmt.Fprint(w, `{"sizes":[
				{"slug":"s-2vcpu-4gb","memory":4096,"vcpus":2,"disk":80,"price_monthly":24,"price_hourly":0.03571,"regions":["nyc3","sfo3","ams3"],"available":true},
				{"slug":"s-1vcpu-1gb","memory":1024,"vcpus":1,"disk":25,"price_monthly":6,"price_hourly":0.00893,"regions":["nyc3","sfo3"],"available":true},
				{"slug":"s-1vcpu-512mb-10gb","memory":512,"vcpus":1,"disk":10,"price_monthly":4,"price_hourly":0.00595,"regions":["nyc2"],"available":true},
				{"slug":"g-2vcpu-8gb","memory":8192,"vcpus":2,"disk":25,"price_monthly":63,"price_hourly":0.09375,"regions":["nyc3"],"available":true},
				{"slug":"s-8vcpu-16gb","memory":16384,"vcpus":8,"disk":320,"price_monthly":96,"price_hourly":0.14286,"regions":["sfo3"],"available":false}]}`)
			return true
		case r.Method == http.MethodPost && path == "/v2/droplets" && a.dropletNo != "":
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprintf(w, `{"id":"unprocessable_entity","message":%q}`, a.dropletNo)
			return true
		}
		return droplets(w, r, body)
	}
	return f, text
}

// atOffset sets this machine's hours from UTC for a test.
func atOffset(t *testing.T, hours float64) {
	t.Helper()
	was := localOffset
	localOffset = func() float64 { return hours }
	t.Cleanup(func() { localOffset = was })
}

func madeDroplet(t *testing.T, f *fakeAPI) map[string]any {
	t.Helper()
	var made map[string]any
	for _, r := range f.requests() {
		if strings.HasPrefix(r, "POST /v2/droplets {") {
			_ = json.Unmarshal([]byte(strings.TrimPrefix(r, "POST /v2/droplets ")), &made)
		}
	}
	return made
}

func TestDigitalOceanFormComesFromTheAccountNearestRegionFirst(t *testing.T) {
	atOffset(t, -8)
	_, text := fakeDigitalOceanAccount(t, &doAccount{keys: map[string]bool{}})
	p, logged := apiProvider(t, text, DigitalOceanSecret, token)
	form := p.Config.Making.Form(p)
	region := at(form, "properties.region").(map[string]any)
	if fmt.Sprint(region["enum"]) != "[sfo3 nyc3 ams3]" || region["default"] != "sfo3" ||
		at(region, "x-titles.sfo3") != "San Francisco 3" {
		t.Errorf("region %v", region)
	}
	size := at(form, "properties.size").(map[string]any)
	// Basic sizes only, available in a region the account may use, cheapest first.
	if fmt.Sprint(size["enum"]) != "[s-1vcpu-1gb s-2vcpu-4gb]" || size["default"] != "s-2vcpu-4gb" ||
		size["x-recommended"] != "s-2vcpu-4gb" {
		t.Errorf("size %v", size)
	}
	if at(size, "x-titles.s-2vcpu-4gb") != "2 vCPUs · 4 GB · 80 GB disk · $24/month" ||
		at(size, "x-titles.s-1vcpu-1gb") != "1 vCPU · 1 GB · 25 GB disk · $6/month" {
		t.Errorf("titles %v", size["x-titles"])
	}
	// The hourly price first: the clients read the first $ as the hourly one.
	if at(size, "x-costs.s-2vcpu-4gb") != "$0.03571/hour ($24/month)" {
		t.Errorf("costs %v", size["x-costs"])
	}
	if fmt.Sprint(at(size, "x-sizes.s-1vcpu-1gb.regions")) != "[nyc3 sfo3]" ||
		fmt.Sprint(at(size, "x-sizes.s-2vcpu-4gb.priceMonthly")) != "24" {
		t.Errorf("details %v", size["x-sizes"])
	}
	if at(form, "properties.image.default") != "ubuntu-24-04-x64" {
		t.Errorf("image %v", at(form, "properties.image"))
	}
	noToken(t, *logged...)
}

func TestDigitalOceanNearestRegionIsByThisMachinesClock(t *testing.T) {
	for _, c := range []struct {
		hours float64
		first string
	}{{-5, "nyc3"}, {1, "ams3"}, {0, "ams3"}, {-7, "sfo3"}} {
		atOffset(t, c.hours)
		_, text := fakeDigitalOceanAccount(t, &doAccount{keys: map[string]bool{}})
		p, _ := apiProvider(t, text, DigitalOceanSecret, token)
		if got := at(p.Config.Making.Form(p), "properties.region.default"); got != c.first {
			t.Errorf("at %v hours: %v, want %s", c.hours, got, c.first)
		}
	}
}

func TestDigitalOceanFormIsThePlainOneWhenTheAccountCantBeRead(t *testing.T) {
	_, text := fakeDigitalOceanAccount(t, &doAccount{keys: map[string]bool{}, regionsFail: true})
	p, logged := apiProvider(t, text, DigitalOceanSecret, token)
	form := p.Config.Making.Form(p)
	if at(form, "properties.size.default") != "s-2vcpu-4gb" || at(form, "properties.region.default") != "nyc3" ||
		at(form, "properties.region.enum") != nil {
		t.Errorf("%v", form)
	}
	if !strings.Contains(strings.Join(*logged, "\n"), "the form is the plain one") {
		t.Errorf("%q", *logged)
	}
}

func TestDigitalOceanMakeAddsTheAgentLoopsKeyOnceAndSaysEachStep(t *testing.T) {
	account := &doAccount{keys: map[string]bool{}}
	f, text := fakeDigitalOceanAccount(t, account)
	p, logged := apiProvider(t, text, DigitalOceanSecret, token)
	p.Folder = t.TempDir()
	// The form was read, so the make's words have the names and the price.
	p.Config.Making.Form(p)
	_, said, err := p.Create("devbox-4000001", map[string]any{"region": "sfo3"}, "curl … | sh")
	if err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(p.Folder, "agent-loops-ssh", "id_ed25519")
	data, err := os.ReadFile(private)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(private); info.Mode().Perm() != 0o600 {
		t.Errorf("the key is %v", info.Mode())
	}
	if info, _ := os.Stat(filepath.Dir(private)); info.Mode().Perm() != 0o700 {
		t.Errorf("its folder is %v", info.Mode())
	}
	fingerprint := ssh.FingerprintLegacyMD5(signer.PublicKey())
	if !account.keys[fingerprint] {
		t.Errorf("the account has %v, not %s", account.keys, fingerprint)
	}
	if made := madeDroplet(t, f); fmt.Sprint(made["ssh_keys"]) != "["+fingerprint+"]" || made["size"] != "s-2vcpu-4gb" {
		t.Errorf("%v", made)
	}
	if strings.Join(said, "\n") != "Made an ssh key for Agent Loops and added it to your DigitalOcean account.\n"+
		"Made droplet devbox-4000001 in San Francisco 3: 2 vCPUs · 4 GB · 80 GB disk · $24/month." {
		t.Errorf("%q", said)
	}

	// The second make uses the same key, and adds it to nothing.
	f.mu.Lock()
	f.said = nil
	delete(f.states, "4000001")
	f.mu.Unlock()
	_, said, err = p.Create("devbox-4000001", map[string]any{}, "curl … | sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range f.requests() {
		if strings.HasPrefix(r, "POST /v2/account/keys") {
			t.Errorf("added again: %s", r)
		}
	}
	if said[0] != "The Agent Loops ssh key is on your DigitalOcean account." {
		t.Errorf("%q", said)
	}
	if again, _ := os.ReadFile(private); string(again) != string(data) {
		t.Error("a second key was made")
	}
	noToken(t, *logged...)
}

// A make with no options makes the region and size the account's form shows as its defaults
// (#6686), not the plain form's nyc3.
func TestDigitalOceanMakeWithNoOptionsTakesTheAccountFormsDefaults(t *testing.T) {
	atOffset(t, -8)
	f, text := fakeDigitalOceanAccount(t, &doAccount{keys: map[string]bool{}})
	p, _ := apiProvider(t, text, DigitalOceanSecret, token)
	form := p.Config.Making.Form(p)
	region, size := at(form, "properties.region.default"), at(form, "properties.size.default")
	if region != "sfo3" || size != "s-2vcpu-4gb" {
		t.Fatalf("the form's defaults: %v %v", region, size)
	}
	_, said, err := p.Create("devbox-4000001", nil, "curl … | sh")
	if err != nil {
		t.Fatal(err)
	}
	if made := madeDroplet(t, f); made["region"] != region || made["size"] != size {
		t.Errorf("made %v %v, the form shows %v %v", made["region"], made["size"], region, size)
	}
	if got := said[len(said)-1]; got != "Made droplet devbox-4000001 in San Francisco 3: 2 vCPUs · 4 GB · 80 GB disk · $24/month." {
		t.Errorf("%q", got)
	}
}

func TestDigitalOceanMakesADropletWithNoKeyWhenTheTokenMayNotAddOne(t *testing.T) {
	f, text := fakeDigitalOceanAccount(t, &doAccount{keys: map[string]bool{}, keyStatus: http.StatusForbidden})
	p, _ := apiProvider(t, text, DigitalOceanSecret, token)
	p.Folder = t.TempDir()
	_, said, err := p.Create("devbox-4000001", map[string]any{"region": "nyc3", "size": "s-2vcpu-4gb", "image": digitalOceanImage}, "curl … | sh")
	if err != nil {
		t.Fatal(err)
	}
	if made := madeDroplet(t, f); made["ssh_keys"] != nil {
		t.Errorf("%v", made)
	}
	// With no form read, the make says the slugs.
	if strings.Join(said, "\n") != noKeyWords+"\nMade droplet devbox-4000001 in nyc3: s-2vcpu-4gb." {
		t.Errorf("%q", said)
	}
}

func TestDigitalOceanMakesNoDropletWhenTheKeyCantBeAdded(t *testing.T) {
	f, text := fakeDigitalOceanAccount(t, &doAccount{keys: map[string]bool{}, addStatus: http.StatusBadRequest})
	p, _ := apiProvider(t, text, DigitalOceanSecret, token)
	p.Folder = t.TempDir()
	_, _, err := p.Create("devbox-4000001", map[string]any{}, "curl … | sh")
	if err == nil || !strings.Contains(err.Error(), "Something went wrong.") || !strings.HasSuffix(err.Error(), "No droplet was made.") {
		t.Errorf("%v", err)
	}
	if made := madeDroplet(t, f); made != nil {
		t.Errorf("made %v", made)
	}
}

func TestDigitalOceanSaysItsRefusalOfADropletAndThatNoneWasMade(t *testing.T) {
	_, text := fakeDigitalOceanAccount(t, &doAccount{keys: map[string]bool{},
		dropletNo: "You do not have a payment method on file."})
	p, logged := apiProvider(t, text, DigitalOceanSecret, token)
	p.Folder = t.TempDir()
	_, said, err := p.Create("devbox-4000001", map[string]any{}, "curl … | sh")
	if err == nil || !strings.Contains(err.Error(), "You do not have a payment method on file.") ||
		!strings.HasSuffix(err.Error(), " No droplet was made.") || said != nil {
		t.Errorf("%v %q", err, said)
	}
	noToken(t, *logged...)
}

func TestDigitalOceanDefaultsToTheAgentsTagAndUser(t *testing.T) {
	f, text := fakeDigitalOcean(t)
	text = strings.Replace(text, `"tag":"agents team",`, "", 1)
	p, _ := apiProvider(t, text, DigitalOceanSecret, token)
	all, err := p.List()
	if err != nil || len(all) != 1 || all[0].SSH() == nil || *all[0].SSH() != "agents@203.0.113.7" {
		t.Fatalf("%v %+v", err, all)
	}
	if !contains(f.requests(), "GET /v2/droplets?per_page=200&tag_name=agents") {
		t.Errorf("%q", f.requests())
	}
}

func TestTheServersMakeSaysWhatItDid(t *testing.T) {
	atOffset(t, -8)
	_, text := fakeDigitalOceanAccount(t, &doAccount{keys: map[string]bool{}})
	p, _ := apiProvider(t, text, DigitalOceanSecret, token)
	s := NewServer(p.Config, quick, p.Run, "test")
	s.Provider.Env, s.Provider.Log, s.Provider.Folder = p.Env, p.Log, t.TempDir()
	answer, err := s.Handle("tools/call", map[string]any{"name": "workspace_create",
		"arguments": map[string]any{"name": "devbox-4000001", "firstBoot": "curl … | sh"}})
	if err != nil {
		t.Fatal(err)
	}
	said := at(answer, "structuredContent.said")
	if fmt.Sprint(said) != "[Made an ssh key for Agent Loops and added it to your DigitalOcean account. Made droplet devbox-4000001 in San Francisco 3: 2 vCPUs · 4 GB · 80 GB disk · $24/month.]" {
		t.Errorf("%v", answer)
	}
	options, err := s.Handle("tools/call", map[string]any{"name": "workspace_options"})
	if err != nil || at(options, "structuredContent.schema.properties.size.x-recommended") != "s-2vcpu-4gb" {
		t.Errorf("%v %v", err, options)
	}
}
