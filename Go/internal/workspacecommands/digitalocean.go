package workspacecommands

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// DigitalOcean's set-up on its API (#6073, epic #6070): the steps a person did by hand on
// DigitalOcean's site, done by the machine that carries the provider.
//
//   - An ssh key for Agent Loops: one key pair on this machine, in its Folder, whose public
//     half is added to each DigitalOcean account once (found again by its fingerprint). Each
//     droplet New Machine makes gets it, so DigitalOcean mails no root password, and this
//     machine can reach the droplet with `ssh -i <Folder>/agent-loops-ssh/id_ed25519`.
//   - The New Machine form from the account: the regions it may use, the one nearest this
//     machine first, and the Basic sizes with DigitalOcean's own prices, a recommended one
//     chosen. With the account out of reach, the form is the plain one.
//   - What a make did, in words, for the control plane's set-up step 1.

const (
	// agentLoopsKeyName is the ssh key's name on the account.
	agentLoopsKeyName = "Agent Loops"
	// agentLoopsKeyFolder is the key pair's folder in the provider's Folder.
	agentLoopsKeyFolder = "agent-loops-ssh"
	// recommendedSize is the size New Machine starts with: 2 vCPUs and 4 GB, enough for two
	// or three agents at once.
	recommendedSize = "s-2vcpu-4gb"
	// digitalOceanImage is the default image: one with systemd, which the first boot needs.
	digitalOceanImage = "ubuntu-24-04-x64"
)

// keyMaking is held while the key pair is read or made, so two makes make one key.
var keyMaking sync.Mutex

// agentLoopsKey is the key pair in folder, made there the first time (0600 in a 0700
// folder). It returns the public key.
func agentLoopsKey(folder string) (ssh.PublicKey, error) {
	keyMaking.Lock()
	defer keyMaking.Unlock()
	dir := filepath.Join(folder, agentLoopsKeyFolder)
	private := filepath.Join(dir, "id_ed25519")
	if data, err := os.ReadFile(private); err == nil {
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			return nil, fmt.Errorf("its ssh key in %s can't be read: %w", private, err)
		}
		return signer.PublicKey(), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	public, secret, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(secret, agentLoopsKeyName)
	if err != nil {
		return nil, err
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		return nil, err
	}
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " " + agentLoopsKeyName + "\n"
	if err := os.WriteFile(private+".pub", []byte(authorized), 0o600); err != nil {
		return nil, err
	}
	// The private half last: a key pair is there only when both halves are.
	if err := os.WriteFile(private, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// digitalOcean is the preset's set-up on the API: its address and headers, and the
// account's choices as last read, to say what a make made.
type digitalOcean struct {
	base    string
	headers map[string]string

	mu      sync.Mutex
	regions map[string]string // slug: name
	sizes   map[string]doSize
}

// doSize is one of DigitalOcean's sizes, as /v2/sizes gives it.
type doSize struct {
	Slug         string   `json:"slug"`
	Memory       int      `json:"memory"`
	VCPUs        int      `json:"vcpus"`
	Disk         int      `json:"disk"`
	PriceMonthly float64  `json:"price_monthly"`
	PriceHourly  float64  `json:"price_hourly"`
	Regions      []string `json:"regions"`
	Available    bool     `json:"available"`
	Description  string   `json:"description"`
}

// doRegion is one of DigitalOcean's regions, as /v2/regions gives it.
type doRegion struct {
	Slug      string   `json:"slug"`
	Name      string   `json:"name"`
	Sizes     []string `json:"sizes"`
	Available bool     `json:"available"`
}

// title is a size as a person reads it: 2 vCPUs · 4 GB · 80 GB disk · $24/month.
func (s doSize) title() string {
	cpus := "1 vCPU"
	if s.VCPUs != 1 {
		cpus = fmt.Sprintf("%d vCPUs", s.VCPUs)
	}
	memory := fmt.Sprintf("%d MB", s.Memory)
	if s.Memory >= 1024 {
		memory = trimmedNumber(float64(s.Memory)/1024) + " GB"
	}
	return fmt.Sprintf("%s · %s · %d GB disk · %s/month", cpus, memory, s.Disk, dollarsOf(s.PriceMonthly))
}

// cost is a size's price in the x-costs words the clients read: the hourly price first.
func (s doSize) cost() string {
	return fmt.Sprintf("$%s/hour (%s/month)", trimmedNumber(s.PriceHourly), dollarsOf(s.PriceMonthly))
}

func dollarsOf(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("$%d", int64(v))
	}
	return fmt.Sprintf("$%.2f", v)
}

// trimmedNumber is v with no trailing zeros: 0.5, 4, 0.03571.
func trimmedNumber(v float64) string {
	text := fmt.Sprintf("%.5f", v)
	text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	if text == "" {
		return "0"
	}
	return text
}

// regionOffsets are each region's hours from UTC (standard time), for the one nearest this
// machine: a guess from its clock, which needs no location and asks nothing.
var regionOffsets = map[string]float64{"nyc": -5, "tor": -5, "atl": -5, "ric": -5, "sfo": -8, "lon": 0,
	"ams": 1, "fra": 1, "blr": 5.5, "sgp": 8, "syd": 10}

// localOffset is this machine's hours from UTC, as standard time: January's offset, so the
// order does not change with summer time. A test sets it.
var localOffset = func() float64 {
	_, seconds := time.Date(time.Now().Year(), time.January, 1, 12, 0, 0, 0, time.Local).Zone()
	return float64(seconds) / 3600
}

// nearestFirst sorts regions by how near their offset is to this machine's, then by slug.
func nearestFirst(regions []doRegion) {
	here := localOffset()
	distance := func(slug string) float64 {
		offset, ok := regionOffsets[strings.TrimRight(slug, "0123456789")]
		if !ok {
			return 24
		}
		d := math.Abs(offset - here)
		return math.Min(d, 24-d)
	}
	sort.SliceStable(regions, func(i, j int) bool {
		a, b := distance(regions[i].Slug), distance(regions[j].Slug)
		if a != b {
			return a < b
		}
		return regions[i].Slug < regions[j].Slug
	})
}

// get is one GET on the API, its answer read into out.
func (d *digitalOcean) get(p *Provider, path, doing string, out any) error {
	answer, err := p.call(Request{Method: http.MethodGet, URL: d.base + path, Headers: d.headers}, map[string]string{},
		p.Timing.Quick, doing)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(answer, out); err != nil {
		return Failure{Reason: "DigitalOcean's answer to " + path + " isn't JSON."}
	}
	return nil
}

// choices is the New Machine form from the account (workspace_options): the regions it may
// use, nearest first, and the Basic sizes available in at least one of them, cheapest first,
// each with its title and DigitalOcean's price. Beside enum, x-titles names each choice,
// x-costs prices a size, and x-sizes says what each size is and where it may be made, for
// a form that shows the sizes of the region chosen (#6577).
func (d *digitalOcean) choices(p *Provider) (map[string]any, error) {
	var regions struct {
		Regions []doRegion `json:"regions"`
	}
	if err := d.get(p, "/v2/regions?per_page=200", "reading DigitalOcean's regions", &regions); err != nil {
		return nil, err
	}
	var sizes struct {
		Sizes []doSize `json:"sizes"`
	}
	if err := d.get(p, "/v2/sizes?per_page=200", "reading DigitalOcean's sizes", &sizes); err != nil {
		return nil, err
	}
	var open []doRegion
	usable := map[string]bool{}
	for _, r := range regions.Regions {
		if r.Available && r.Slug != "" {
			open = append(open, r)
			usable[r.Slug] = true
		}
	}
	if len(open) == 0 {
		return nil, Failure{Reason: "DigitalOcean lists no region this account may make droplets in."}
	}
	nearestFirst(open)
	var basic []doSize
	for _, s := range sizes.Sizes {
		if !s.Available || !strings.HasPrefix(s.Slug, "s-") {
			continue
		}
		var where []string
		for _, r := range s.Regions {
			if usable[r] {
				where = append(where, r)
			}
		}
		if len(where) == 0 {
			continue
		}
		s.Regions = where
		basic = append(basic, s)
	}
	if len(basic) == 0 {
		return nil, Failure{Reason: "DigitalOcean lists no Basic size this account may make."}
	}
	sort.SliceStable(basic, func(i, j int) bool {
		if basic[i].PriceMonthly != basic[j].PriceMonthly {
			return basic[i].PriceMonthly < basic[j].PriceMonthly
		}
		return basic[i].Slug < basic[j].Slug
	})
	regionSlugs, regionTitles := []any{}, map[string]any{}
	names := map[string]string{}
	for _, r := range open {
		regionSlugs = append(regionSlugs, r.Slug)
		regionTitles[r.Slug] = r.Name
		names[r.Slug] = r.Name
	}
	sizeSlugs, sizeTitles, costs, details := []any{}, map[string]any{}, map[string]any{}, map[string]any{}
	known := map[string]doSize{}
	for _, s := range basic {
		sizeSlugs = append(sizeSlugs, s.Slug)
		sizeTitles[s.Slug] = s.title()
		costs[s.Slug] = s.cost()
		where := make([]any, len(s.Regions))
		for i, r := range s.Regions {
			where[i] = r
		}
		details[s.Slug] = map[string]any{"vcpus": s.VCPUs, "memoryMB": s.Memory, "diskGB": s.Disk,
			"priceMonthly": s.PriceMonthly, "priceHourly": s.PriceHourly, "regions": where}
		known[s.Slug] = s
	}
	recommended := recommendedOf(basic, open[0].Slug)
	d.mu.Lock()
	d.regions, d.sizes = names, known
	d.mu.Unlock()
	return map[string]any{"type": "object", "properties": map[string]any{
		"region": map[string]any{"type": "string", "title": "Region", "enum": regionSlugs, "default": open[0].Slug,
			"x-titles": regionTitles, "description": "Where DigitalOcean makes the droplet: the nearest first."},
		"size": map[string]any{"type": "string", "title": "Size", "enum": sizeSlugs, "default": recommended,
			"x-titles": sizeTitles, "x-costs": costs, "x-sizes": details, "x-recommended": recommended,
			"description": "DigitalOcean charges for a droplet while it exists, also when it is off. " +
				sizeTitles[recommended].(string) + " is enough for 2–3 agents at once."},
		"image": map[string]any{"type": "string", "title": "Image", "default": digitalOceanImage,
			"description": "An image with systemd, such as ubuntu-24-04-x64 or debian-12-x64."},
	}}, nil
}

// recommendedOf is the size New Machine starts with: recommendedSize where the region has
// it, else the cheapest with 4 GB or more there, else the cheapest there, else the first.
func recommendedOf(sizes []doSize, region string) string {
	in := func(s doSize) bool {
		for _, r := range s.Regions {
			if r == region {
				return true
			}
		}
		return false
	}
	for _, s := range sizes {
		if s.Slug == recommendedSize && in(s) {
			return s.Slug
		}
	}
	for _, s := range sizes {
		if s.Memory >= 4096 && in(s) {
			return s.Slug
		}
	}
	for _, s := range sizes {
		if in(s) {
			return s.Slug
		}
	}
	return sizes[0].Slug
}

// prepare is a make's first step: the Agent Loops ssh key, on this machine and on the
// account, put in the droplet's ssh_keys. A token that may not read or add keys makes the
// droplet with none, as before #6073; any other refusal ends the make with no droplet.
func (d *digitalOcean) prepare(p *Provider, request *Request, _ map[string]string) (*Request, []string, error) {
	if p.Folder == "" {
		return request, nil, nil
	}
	key, err := agentLoopsKey(p.Folder)
	if err != nil {
		return nil, nil, Failure{Reason: "Agent Loops could not make its ssh key: " + err.Error() + " No droplet was made."}
	}
	fingerprint := ssh.FingerprintLegacyMD5(key)
	said := ""
	_, err = p.call(Request{Method: http.MethodGet, URL: d.base + "/v2/account/keys/" + url.PathEscape(fingerprint),
		Headers: d.headers}, map[string]string{}, p.Timing.Quick, "reading the account's ssh keys")
	var refused Failure
	switch {
	case err == nil:
		said = "The Agent Loops ssh key is on your DigitalOcean account."
	case errors.As(err, &refused) && refused.Status == http.StatusNotFound:
		public := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		_, err = p.call(Request{Method: http.MethodPost, URL: d.base + "/v2/account/keys", Headers: d.headers,
			Body: map[string]any{"name": agentLoopsKeyName, "public_key": public}}, map[string]string{},
			p.Timing.Quick, "adding the Agent Loops ssh key")
		switch {
		case err == nil:
			said = "Made an ssh key for Agent Loops and added it to your DigitalOcean account."
		case errors.As(err, &refused) && refused.Status == http.StatusUnprocessableEntity:
			// Another make added it in between: it is there.
			said = "The Agent Loops ssh key is on your DigitalOcean account."
		case errors.As(err, &refused) && (refused.Status == http.StatusUnauthorized || refused.Status == http.StatusForbidden):
			return request, []string{noKeyWords}, nil
		default:
			return nil, nil, Failure{Reason: err.Error() + " No droplet was made."}
		}
	case errors.As(err, &refused) && (refused.Status == http.StatusUnauthorized || refused.Status == http.StatusForbidden):
		return request, []string{noKeyWords}, nil
	default:
		return nil, nil, Failure{Reason: err.Error() + " No droplet was made."}
	}
	copied := *request
	body := map[string]any{}
	if given, ok := request.Body.(map[string]any); ok {
		for k, v := range given {
			body[k] = v
		}
	}
	keys, _ := body["ssh_keys"].([]any)
	body["ssh_keys"] = append(append([]any{}, keys...), fingerprint)
	copied.Body = body
	return &copied, []string{said}, nil
}

// noKeyWords say a make with no Agent Loops key: the token's scopes leave keys out.
const noKeyWords = "Made no ssh key for Agent Loops: the token may not read or add ssh keys (ssh_key:read, ssh_key:create)."

// made says what a make made, with the names and price of the account's choices as last read.
func (d *digitalOcean) made(values map[string]string) string {
	d.mu.Lock()
	region, size := d.regions[values["region"]], d.sizes[values["size"]]
	d.mu.Unlock()
	if region == "" {
		region = values["region"]
	}
	what := values["size"]
	if size.Slug != "" {
		what = size.title()
	}
	return fmt.Sprintf("Made droplet %s in %s: %s.", values["name"], region, what)
}
