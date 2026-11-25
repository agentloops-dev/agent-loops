// agentsctl is a client of a control plane, as the window is one, for driving hosts in a
// walk without the screen (#1125's spike, kept for #1136's proofs). Not shipped.
//
//	agentsctl pair [-kind iPhone] '<client code>'     keeps ~/.agents-go/ctl.json
//	agentsctl unpair                                  forgets the pairing, here and there
//	agentsctl hosts                                   control/status's hosts
//	agentsctl call [-host H] METHOD ['{json}']        one request; prints the result
//	agentsctl watch [-host H]                         prints notifications
//	agentsctl lend -host H [-from mac]                lends -from's Claude sign-in to H (Lending.swift by hand)
//	agentsctl turn -host H -runtime claude -cwd PATH [-allow] 'prompt'
//	                                                  starts an agent, follows it to its end, prints it
//	agentsctl device -host H -token T                 a device's stand-in (#1507): gives its push
//	                                                  token, says it may be notified, stays
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controllink"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// file is one pairing, with its private key. It is not in ~/.agents, on purpose (#6808, as
// #6800): it is the identity of one client, and a person syncs ~/.agents. It has no age, so
// that a pairing that works is never lost: unpair deletes it.
func file() string {
	if f := os.Getenv("AGENTSCTL"); f != "" {
		return f
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agents-go", "ctl.json")
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(1)
}

func membership() (*controllink.ClientMembership, error) {
	b, err := os.ReadFile(file())
	if err != nil {
		return nil, fmt.Errorf("not paired: agentsctl pair '<client code>' (%w)", err)
	}
	var m controllink.ClientMembership
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("not paired: %s is not a membership (%w)", file(), err)
	}
	return &m, nil
}

func session(ctx context.Context) *controllink.Session {
	m, err := membership()
	if err != nil {
		die("%v", err)
	}
	s, err := controllink.DialClient(ctx, m)
	if err != nil {
		die("dial: %v", err)
	}
	return s
}

func main() {
	if len(os.Args) < 2 {
		die("usage: agentsctl pair|unpair|hosts|call|watch|lend|turn|device …")
	}
	ctx := context.Background()
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	host := fs.String("host", "", "a host id; empty for the control plane")
	from := fs.String("from", "mac", "the host that lends")
	rt := fs.String("runtime", "claude", "the runtime")
	cwd := fs.String("cwd", "", "the agent's folder on the host")
	allow := fs.Bool("allow", false, "answer every permission card with its first allow option, and every form with its first choices")
	wait := fs.Int("wait", 300, "seconds to follow a turn")
	kind := fs.String("kind", "mac", "pair as this kind of client: mac, iPhone or iPad")
	token := fs.String("token", "", "a push token, for device")
	_ = fs.Parse(args) // ExitOnError: a bad flag exits before Parse returns
	rest := fs.Args()
	switch cmd {
	case "pair":
		pair(ctx, rest, *kind)
	case "unpair":
		if err := unpair(ctx, os.Stdout); err != nil {
			die("unpair: %v", err)
		}
	case "hosts":
		s := session(ctx)
		r, err := s.Call(ctx, "", "machines/list", nil)
		out(r, err)
	case "call":
		if len(rest) < 1 {
			die("agentsctl call [-host H] METHOD [json]")
		}
		var params any
		if len(rest) > 1 {
			params = json.RawMessage(rest[1])
		}
		s := session(ctx)
		r, err := s.Call(ctx, *host, rest[0], params)
		out(r, err)
	case "watch":
		s := session(ctx)
		// A client hears a host only once it has a channel there: open one.
		if *host != "" {
			if _, err := s.Call(ctx, *host, "daemon/ping", nil); err != nil {
				fmt.Fprintf(os.Stderr, "daemon/ping: %v\n", err)
			}
		}
		for n := range s.Notes {
			if *host == "" || n.Host == *host {
				fmt.Printf("%s %s %s\n", n.Host, n.Method, n.Params)
			}
		}
	case "device":
		if *host == "" || *token == "" {
			die("agentsctl device -host H -token T")
		}
		s := session(ctx)
		out(s.Call(ctx, "", "clients/setPushToken", map[string]string{"token": *token, "environment": "development"}))
		// Presence goes to a host; it says this device may be notified and is not in use.
		out(s.Call(ctx, *host, "presence/report", map[string]any{"active": false, "mayNotify": true}))
		fmt.Println("a device, with its push token; until stopped")
		for range s.Notes {
		}
	case "lend":
		s := session(ctx)
		lend(ctx, s, *host, *from, *rt)
	case "turn":
		if len(rest) != 1 || *host == "" || *cwd == "" {
			die("agentsctl turn -host H -runtime R -cwd PATH 'prompt'")
		}
		s := session(ctx)
		turn(ctx, s, *host, *rt, *cwd, rest[0], *allow, time.Duration(*wait)*time.Second, *from)
	default:
		die("unknown command %s", cmd)
	}
}

// pair pairs as a client with a client code and keeps the membership in file().
func pair(ctx context.Context, rest []string, kind string) {
	if len(rest) != 1 {
		die("agentsctl pair '<client code>'")
	}
	if len(rest[0]) > 2<<10 {
		die("the code is too long")
	}
	code, err := controllink.ParseCode(rest[0])
	if err != nil {
		die("%v", err)
	}
	m, err := controllink.PairClientAs(ctx, code, "agentsctl", kind)
	if err != nil {
		die("pair: %v", err)
	}
	b, err := kept(m)
	if err != nil {
		_ = controllink.ForgetSelf(ctx, m) // best effort: its Devices list shows one that stays
		die("pair: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(file()), 0o700); err != nil {
		die("pair: %v", err)
	}
	// store-bounds: kind=secret; count=1; size=4KiB; age=until-deleted; applied=on pair, which replaces it, and unpair deletes it
	if err := os.WriteFile(file(), b, 0o600); err != nil { //nolint:gosec // G703: agentsctl's own membership file
		die("pair: %v", err)
	}
	fmt.Println("paired as", m.Client)
}

// mostPairing is the largest pairing kept; the control plane gives the client's id.
const mostPairing = 4 << 10

// kept is the pairing as the file has it, refused when it is too large.
func kept(m *controllink.ClientMembership) ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err == nil && len(b) > mostPairing {
		err = fmt.Errorf("the pairing is %d bytes, at most %d; it is not kept", len(b), mostPairing)
	}
	return b, err
}

// unpair deletes the pairing. The control plane forgets this client too; when it does not
// answer, the pairing goes here all the same, and its Devices list still shows it.
func unpair(ctx context.Context, say io.Writer) error {
	m, err := membership()
	if err != nil {
		return err
	}
	there := controllink.ForgetSelf(ctx, m)
	if err := os.Remove(file()); err != nil {
		return err
	}
	if there != nil {
		fmt.Fprintf(say, "unpaired here; %s did not forget %s: forget it in Settings ▸ Devices (%v)\n", m.URL, m.Client, there)
		return nil
	}
	fmt.Fprintf(say, "unpaired; %s forgets %s\n", m.URL, m.Client)
	return nil
}

func out(r json.RawMessage, err error) {
	if err != nil {
		die("%v", err)
	}
	var v any
	if err := json.Unmarshal(r, &v); err != nil {
		die("reply is not JSON: %v", err)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

// lend is what the window does on -32036 (Lending.swift): the control plane lets the
// lender lend, the lender grants a stand-in and its CA, the borrower is offered them.
func lend(ctx context.Context, s *controllink.Session, host, from, runtime string) {
	if _, err := s.Call(ctx, "", "machines/lendSignIn", map[string]any{"host": host, "from": from, "runtime": runtime, "allowed": true}); err != nil {
		die("machines/lendSignIn: %v", err)
	}
	g, err := s.Call(ctx, from, "relay/grant", map[string]any{"runtime": runtime})
	if err != nil {
		die("relay/grant on %s: %v", from, err)
	}
	var grant struct {
		CACertificate string `json:"caCertificate"`
		StandIn       string `json:"standIn"`
	}
	if err := json.Unmarshal(g, &grant); err != nil {
		die("relay/grant on %s: %v", from, err)
	}
	if _, err := s.Call(ctx, host, "relay/offer", map[string]any{"runtime": runtime, "socketPath": "tunnel",
		"caCertificate": grant.CACertificate, "standIn": grant.StandIn}); err != nil {
		die("relay/offer on %s: %v", host, err)
	}
	fmt.Printf("%s lends %s's sign-in to %s\n", from, runtime, host)
}

func turn(ctx context.Context, s *controllink.Session, host, rt, cwd, prompt string, allow bool, wait time.Duration, from string) {
	folder := "file://" + strings.TrimSuffix(cwd, "/") + "/"
	if _, err := s.Call(ctx, host, "folders/add", map[string]any{"folder": folder}); err != nil {
		fmt.Fprintf(os.Stderr, "folders/add: %v\n", err)
	}
	start := map[string]any{"runtimeID": rt, "cwd": folder, "prompt": prompt, "attachments": []any{},
		"startOptions": map[string]any{"values": map[string]any{}, "extraArguments": []any{}}}
	r, err := s.Call(ctx, host, "agents/start", start)
	if err != nil && strings.Contains(err.Error(), "-32036") {
		fmt.Println("the host wants a sign-in (-32036); lending it, as the window does")
		lend(ctx, s, host, from, rt)
		r, err = s.Call(ctx, host, "agents/start", start)
	}
	if err != nil {
		die("agents/start: %v", err)
	}
	var id string
	if err := json.Unmarshal(r, &id); err != nil {
		die("agents/start: %v", err)
	}
	fmt.Println("agent", id)
	w := turnWatch{ctx: ctx, s: s, host: host, id: id, allow: allow}
	deadline := time.After(wait)
	for {
		select {
		case n, ok := <-s.Notes:
			if !ok {
				die("the control plane went")
			}
			if n.Host != host {
				continue
			}
			if w.note(n.Method, n.Params) {
				return
			}
		case <-deadline:
			die("\nstill going after %s", wait)
		}
	}
}

// turnWatch prints one agent's notes and answers its cards and forms.
type turnWatch struct {
	ctx   context.Context
	s     *controllink.Session
	host  string
	id    string
	allow bool
}

// note handles one note from the host; it says true when the agent ended.
func (w turnWatch) note(method string, params json.RawMessage) bool {
	switch method {
	case "agent/entry":
		w.entry(params)
	case "agent/permission":
		w.permission(params)
	case "agent/elicitation":
		w.elicitation(params)
	case "agent/changed":
		return w.changed(params)
	}
	return false
}

func (w turnWatch) entry(params json.RawMessage) {
	var e struct {
		AgentID string `json:"agentID"`
		Entry   struct {
			Kind map[string]json.RawMessage `json:"kind"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(params, &e); err != nil {
		fmt.Fprintf(os.Stderr, "agent/entry: %v\n", err)
		return
	}
	if !strings.EqualFold(e.AgentID, w.id) {
		return
	}
	for k, v := range e.Entry.Kind {
		if k == "agentMessage" {
			var m struct{ Text string }
			if err := json.Unmarshal(v, &m); err != nil {
				fmt.Fprintf(os.Stderr, "agentMessage: %v\n", err)
				continue
			}
			fmt.Print(m.Text)
			continue
		}
		t := string(v)
		if len(t) > 160 {
			t = t[:160] + "…"
		}
		fmt.Printf("\n[%s] %s\n", k, t)
	}
}

func (w turnWatch) permission(params json.RawMessage) {
	var p struct {
		AgentID string `json:"agentID"`
		Request *struct {
			ID      string `json:"id"`
			Options []struct {
				OptionID string `json:"optionID"`
				Kind     string `json:"kind"`
			} `json:"options"`
		} `json:"request"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		fmt.Fprintf(os.Stderr, "agent/permission: %v\n", err)
		return
	}
	if p.Request == nil || !strings.EqualFold(p.AgentID, w.id) {
		return
	}
	if !w.allow {
		fmt.Printf("\n[card] waiting for an answer in the window (permission %s)\n", p.Request.ID)
		return
	}
	for _, o := range p.Request.Options {
		if strings.HasPrefix(o.Kind, "allow") {
			fmt.Printf("\n[card] answering %s\n", o.OptionID)
			if _, err := w.s.Call(w.ctx, w.host, "permissions/answer", map[string]any{"permissionID": p.Request.ID, "optionID": o.OptionID}); err != nil {
				fmt.Fprintf(os.Stderr, "permissions/answer: %v\n", err)
			}
			return
		}
	}
}

func (w turnWatch) elicitation(params json.RawMessage) {
	var f struct {
		AgentID string                   `json:"agentID"`
		Request *wire.ElicitationRequest `json:"request"`
	}
	if err := json.Unmarshal(params, &f); err != nil {
		fmt.Fprintf(os.Stderr, "agent/elicitation: %v\n", err)
		return
	}
	if f.Request == nil || !strings.EqualFold(f.AgentID, w.id) {
		return
	}
	b, _ := json.Marshal(f.Request.Mode)
	fmt.Printf("\n[form] %s\n", b)
	if !w.allow || f.Request.Mode.Form == nil {
		fmt.Printf("[form] waiting for an answer in the window (form %s)\n", f.Request.ID)
		return
	}
	content := formAnswer(f.Request.Mode.Form.Value.Properties)
	fmt.Printf("[form] answering %v\n", content)
	if _, err := w.s.Call(w.ctx, w.host, "elicitations/answer", map[string]any{"requestID": f.Request.ID, "action": "accept",
		"content": content}); err != nil {
		fmt.Fprintf(os.Stderr, "elicitations/answer: %v\n", err)
	}
}

func (w turnWatch) changed(params json.RawMessage) bool {
	var a struct {
		ID          string          `json:"id"`
		State       string          `json:"state"`
		EndedReason string          `json:"endedReason"`
		Report      json.RawMessage `json:"report"`
	}
	if err := json.Unmarshal(params, &a); err != nil {
		fmt.Fprintf(os.Stderr, "agent/changed: %v\n", err)
		return false
	}
	if strings.EqualFold(a.ID, w.id) && (a.State == "finished" || a.State == "stopped") {
		fmt.Printf("\n== %s (%s) %s\n", a.State, a.EndedReason, a.Report)
		return true
	}
	return false
}

// formAnswer takes the first choice of each question, and yes to a yes or no.
func formAnswer(props []wire.ElicitationSchemaProperty) map[string]any {
	content := map[string]any{}
	for _, p := range props {
		switch k := p.Kind; {
		case k.String != nil && len(k.String.Choices) > 0:
			content[p.Name] = k.String.Choices[0].Value
		case k.MultiSelect != nil && len(k.MultiSelect.Items) > 0:
			content[p.Name] = []string{k.MultiSelect.Items[0].Value}
		case k.Boolean != nil:
			content[p.Name] = true
		case p.IsRequired:
			content[p.Name] = "yes"
		}
	}
	return content
}
