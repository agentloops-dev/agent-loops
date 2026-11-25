package providers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/agentloops-dev/agent-loops/Go/internal/controlplane/records"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// start_session on another machine (#2887): machines/startSession goes to that machine's
// own agents/start, found by its name, saying in the control plane's words which agent on
// which machine asked; a stopped workspace keeps it; another person's machine is not there.

func sessionOn(host string) map[string]any {
	return map[string]any{"host": host, "start": map[string]any{"runtimeID": "claude", "cwd": "file:///home/me/app", "prompt": "Check it",
		"attachments": []any{}, "startOptions": map[string]any{"values": map[string]any{}, "extraArguments": []any{}},
		"additionalDirectories": []any{}, "mcpServers": []any{}, "labels": []any{},
		"startedFrom": map[string]any{"machine": "a lie", "agent": "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE", "who": "a lie"}}}
}

func TestAnAgentStartsASessionOnAnotherMachineByItsName(t *testing.T) {
	h := newHarness(t)
	made := records.NewUUID()
	h.router.mu.Lock()
	h.router.answer = func(host, method string, params json.RawMessage) *Reply { return &Reply{Result: rawOf(made)} }
	h.router.mu.Unlock()
	h.router.setOnline(devbox, true)

	listed, err := h.agentCall(MethodWorkspacesAgent, map[string]any{})
	if hosts := listed.(AgentMachines).Hosts; err != nil || len(hosts) != 2 || hosts[0].Name != "devbox" || hosts[0].State != "online" {
		t.Fatalf("%v %+v", err, listed)
	}

	out, err := h.agentCall(MethodSessionOnMachine, sessionOn("DEVBOX"))
	if err != nil || string(out.(wire.UUID)) != made {
		t.Fatalf("%v %+v", err, out)
	}
	sent := h.router.deliveries()
	if len(sent) != 1 || sent[0].host != devbox || sent[0].method != MethodAgentsStart {
		t.Fatalf("%+v", sent)
	}
	var start wire.StartRequest
	if err := json.Unmarshal(sent[0].params, &start); err != nil {
		t.Fatal(err)
	}
	if f := start.StartedFrom; f == nil || f.Machine != "other" || f.Who != "Claude (fix-login)" || f.Agent != "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE" ||
		start.Cwd != "file:///home/me/app" {
		t.Fatalf("%+v", start)
	}

	// The host says no, in its own words.
	h.router.mu.Lock()
	h.router.answer = func(string, string, json.RawMessage) *Reply {
		return &Reply{Error: &Failure{Code: -32004, Message: "/home/me/app is not there any more."}}
	}
	h.router.mu.Unlock()
	if _, err := h.agentCall(MethodSessionOnMachine, sessionOn("devbox")); failureOf(err) == nil || failureOf(err).Message != "/home/me/app is not there any more." {
		t.Fatalf("%v", err)
	}

	// Offline, with no workspace to start: not sent.
	h.router.setOnline(devbox, false)
	if _, err := h.agentCall(MethodSessionOnMachine, sessionOn("devbox")); failureOf(err) == nil || failureOf(err).Code != CodeHostOffline {
		t.Fatalf("%v", err)
	}
	if _, err := h.agentCall(MethodSessionOnMachine, sessionOn("nowhere")); failureOf(err) == nil || failureOf(err).Code != CodeNoSuchHost {
		t.Fatalf("%v", err)
	}
}

func TestASessionOnAStoppedWorkspaceIsKept(t *testing.T) {
	h := newHarness(t)
	h.link(h.add(), devbox, "agents-devbox")
	h.keeper()
	_, err := h.agentCall(MethodSessionOnMachine, sessionOn("devbox"))
	if s := keptSummary(t, failureOf(err)); s.Kind != "start" {
		t.Fatalf("%+v", s)
	}
}

func TestAnAgentCannotStartASessionOnAnotherPersonsMachine(t *testing.T) {
	h := newHarness(t)
	h.providers.hooks.OwnerOf = func(host string) string {
		if host == devbox {
			return "kim"
		}
		return "lee"
	}
	h.router.setOnline(devbox, true)
	if _, err := h.agentCall(MethodSessionOnMachine, sessionOn("devbox")); failureOf(err) == nil || failureOf(err).Code != CodeNoSuchHost {
		t.Fatalf("%v", err)
	}
	listed, _ := h.agentCall(MethodWorkspacesAgent, map[string]any{})
	if hosts := listed.(AgentMachines).Hosts; len(hosts) != 1 || hosts[0].Name != "other" {
		t.Fatalf("%+v", hosts)
	}
	if len(h.router.deliveries()) != 0 {
		t.Fatal("sent")
	}
}

// A session started on another machine (#2927): the starter's calls go to that machine's
// agents/fromStarter, saying in the control plane's words which machine and agent ask;
// its end goes to the starter's machine as agents/endedElsewhere, naming the machine it
// ran on.
func TestTheStarterReachesItsSessionThroughTheControlPlane(t *testing.T) {
	h := newHarness(t)
	h.router.mu.Lock()
	h.router.answer = func(host, method string, params json.RawMessage) *Reply {
		if method == MethodAgentsFromStarter {
			return &Reply{Result: rawOf(wire.SessionCallReply{Said: "working"})}
		}
		return &Reply{Result: rawOf(map[string]any{})}
	}
	h.router.mu.Unlock()
	call := map[string]any{"host": "devbox", "call": map[string]any{"session": "11111111-2222-3333-4444-555555555555", "op": "status",
		"from": map[string]any{"machine": "a lie", "agent": "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE", "who": "a lie"}}}
	if _, err := h.agentCall(MethodSessionCall, call); failureOf(err) == nil || failureOf(err).Code != CodeHostOffline {
		t.Fatalf("%v", err)
	}
	h.router.setOnline(devbox, true)
	out, err := h.agentCall(MethodSessionCall, call)
	if err != nil || out.(wire.SessionCallReply).Said != "working" {
		t.Fatalf("%v %+v", err, out)
	}
	sent := h.router.deliveries()
	var asked wire.SessionCall
	if len(sent) != 1 || sent[0].host != devbox || sent[0].method != MethodAgentsFromStarter || json.Unmarshal(sent[0].params, &asked) != nil ||
		asked.From.Machine != "other" || asked.From.Who != "Claude (fix-login)" || asked.From.Agent != "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE" || asked.Op != "status" {
		t.Fatalf("%+v %+v", sent, asked)
	}

	// other's session ended; devbox started it.
	ended := map[string]any{"machine": "DEVBOX", "session": "11111111-2222-3333-4444-555555555555", "starter": "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE",
		"how": map[string]any{"finished": map[string]any{}}}
	if _, err := h.agentCall(MethodSessionEnded, ended); err != nil {
		t.Fatal(err)
	}
	sent = h.router.deliveries()
	var told wire.SessionEnded
	if len(sent) != 2 || sent[1].host != devbox || sent[1].method != MethodAgentsEndedThere || json.Unmarshal(sent[1].params, &told) != nil ||
		told.Machine != "other" || told.How.Finished == nil {
		t.Fatalf("%+v %+v", sent, told)
	}
	ended["machine"] = "nowhere"
	if _, err := h.agentCall(MethodSessionEnded, ended); failureOf(err) == nil || failureOf(err).Code != CodeNoSuchHost {
		t.Fatalf("%v", err)
	}

	// Another person's machine is not there.
	h.providers.hooks.OwnerOf = func(host string) string {
		if host == devbox {
			return "kim"
		}
		return "lee"
	}
	ended["machine"] = "devbox"
	if _, err := h.agentCall(MethodSessionCall, call); failureOf(err) == nil || failureOf(err).Code != CodeNoSuchHost {
		t.Fatalf("%v", err)
	}
	if _, err := h.agentCall(MethodSessionEnded, ended); failureOf(err) == nil || failureOf(err).Code != CodeNoSuchHost {
		t.Fatalf("%v", err)
	}
	if len(h.router.deliveries()) != 2 {
		t.Fatal("sent")
	}
}

// start_session with send (#2929): each step of machines/sendFolder goes to that machine's
// own folders/receive* method, and its answer comes back; an offline machine gets none.
func TestAnAgentSendsAFolderToAnotherMachine(t *testing.T) {
	h := newHarness(t)
	h.router.mu.Lock()
	h.router.answer = func(host, method string, params json.RawMessage) *Reply {
		switch method {
		case MethodReceiveState:
			return &Reply{Result: rawOf(wire.ReceiveState{Folder: "file:///home/me/app/", Exists: true, Repository: true, Heads: []string{"abc"}})}
		case MethodReceive:
			return &Reply{Result: rawOf(wire.FolderSummary{Name: "app"})}
		case MethodSendFacts:
			return &Reply{Result: rawOf(wire.SendFacts{Branches: []string{"main"}, Current: new("main")})}
		case MethodSendPack:
			return &Reply{Result: rawOf(wire.SendPack{Commits: []wire.SentBranch{{Name: "main", Commit: "0123456789012345678901234567890123456789"}}})}
		case MethodSendRead:
			return &Reply{Result: rawOf(wire.SendRead{Data: wire.Data("hello"), End: true})}
		case MethodReceiveSeed:
			return &Reply{Result: rawOf(wire.ReceiveSeed{Finished: true, Heads: []string{"abc"}})}
		}
		return &Reply{Result: rawOf(map[string]any{})}
	}
	h.router.mu.Unlock()
	h.router.setOnline(devbox, true)

	out, err := h.agentCall(MethodSendFolder, map[string]any{"host": "devbox", "state": map[string]any{"folder": "/home/me/app"}})
	if s := out.(wire.MachineFolderSent).State; err != nil || s == nil || len(s.Heads) != 1 {
		t.Fatalf("%v %+v", err, out)
	}
	if _, err := h.agentCall(MethodSendFolder, map[string]any{"host": "devbox",
		"upload": map[string]any{"upload": records.NewUUID(), "offset": 0, "data": "aGVsbG8="}}); err != nil {
		t.Fatal(err)
	}
	out, err = h.agentCall(MethodSendFolder, map[string]any{"host": "devbox", "receive": map[string]any{"folder": "/home/me/app",
		"branches": []any{map[string]any{"name": "main", "commit": "0123456789012345678901234567890123456789"}}, "checkout": "main"}})
	if f := out.(wire.MachineFolderSent).Folder; err != nil || f == nil || f.Name != "app" {
		t.Fatalf("%v %+v", err, out)
	}
	sent := h.router.deliveries()
	if len(sent) != 3 || sent[0].method != MethodReceiveState || sent[1].method != MethodReceiveUpload || sent[2].method != MethodReceive || sent[2].host != devbox {
		t.Fatalf("%+v", sent)
	}
	var upload wire.ReceiveUploadRequest
	if json.Unmarshal(sent[1].params, &upload) != nil || string(upload.Data) != "hello" {
		t.Fatalf("%s", sent[1].params)
	}

	// bring_back's steps (#2947): facts, pack and read, each with its answer.
	out, err = h.agentCall(MethodSendFolder, map[string]any{"host": "devbox", "facts": map[string]any{"folder": "file:///home/me/app/"}})
	if f := out.(wire.MachineFolderSent).Facts; err != nil || f == nil || f.Current == nil || *f.Current != "main" {
		t.Fatalf("%v %+v", err, out)
	}
	out, err = h.agentCall(MethodSendFolder, map[string]any{"host": "devbox", "pack": map[string]any{"folder": "file:///home/me/app/",
		"branches": []any{"main"}, "leavingOut": []any{}, "includeChanges": false}})
	if p := out.(wire.MachineFolderSent).Pack; err != nil || p == nil || len(p.Commits) != 1 {
		t.Fatalf("%v %+v", err, out)
	}
	out, err = h.agentCall(MethodSendFolder, map[string]any{"host": "devbox", "read": map[string]any{"id": records.NewUUID(), "offset": 0}})
	if r := out.(wire.MachineFolderSent).Read; err != nil || r == nil || string(r.Data) != "hello" || !r.End {
		t.Fatalf("%v %+v", err, out)
	}
	sent = h.router.deliveries()
	if len(sent) != 6 || sent[3].method != MethodSendFacts || sent[4].method != MethodSendPack || sent[5].method != MethodSendRead {
		t.Fatalf("%+v", sent)
	}

	// The seed from the folder's origin (#7119).
	seed := map[string]any{"seed": records.NewUUID(), "folder": "/home/me/app", "origin": "https://host.test/a/b"}
	out, err = h.agentCall(MethodSendFolder, map[string]any{"host": "devbox", "seed": seed})
	if s := out.(wire.MachineFolderSent).Seed; err != nil || s == nil || !s.Finished || len(s.Heads) != 1 {
		t.Fatalf("%v %+v", err, out)
	}
	if sent = h.router.deliveries(); len(sent) != 7 || sent[6].method != MethodReceiveSeed {
		t.Fatalf("%+v", sent)
	}
	if _, err := h.agentCall(MethodSendFolder, map[string]any{"host": "devbox", "seed": seed,
		"state": map[string]any{"folder": "/home/me/app"}}); failureOf(err) == nil || failureOf(err).Code != CodeInvalidParams {
		t.Fatalf("two steps: %v", err)
	}

	if _, err := h.agentCall(MethodSendFolder, map[string]any{"host": "devbox"}); failureOf(err) == nil || failureOf(err).Code != CodeInvalidParams {
		t.Fatalf("%v", err)
	}
	h.router.setOnline(devbox, false)
	if _, err := h.agentCall(MethodSendFolder, map[string]any{"host": "devbox", "state": map[string]any{"folder": "/a"}}); failureOf(err) == nil || failureOf(err).Code != CodeHostOffline {
		t.Fatalf("%v", err)
	}
	if len(h.router.deliveries()) != 7 {
		t.Fatal("sent to an offline machine")
	}
}

// The agent tools scoped to the project (#3277): an agent's machine reaches a session of
// its project on another machine only when it has a folder of that project, and is told the
// project's folders and sessions on its person's machines alone.
func TestAnAgentReachesItsProjectOnOtherMachines(t *testing.T) {
	h := newHarness(t)
	h.router.mu.Lock()
	h.router.answer = func(host, method string, params json.RawMessage) *Reply {
		return &Reply{Result: rawOf(wire.SessionCallReply{Said: "read"})}
	}
	h.router.mu.Unlock()
	h.router.setOnline(devbox, true)
	folders := map[string][]string{devbox: {"/home/me/app"}, other: {"/Users/me/app"}}
	h.providers.hooks.ProjectFolders = func(project string) map[string][]string {
		if project != "p-app" {
			return map[string][]string{}
		}
		return folders
	}
	var asked []wire.ProjectSessionsRequest
	h.providers.hooks.ProjectSessions = func(request wire.ProjectSessionsRequest, caller Caller) (wire.ProjectSessions, error) {
		asked = append(asked, request)
		return wire.ProjectSessions{Sessions: []wire.ProjectSession{}, Unreached: []wire.ProjectMachine{}}, nil
	}
	call := func(project string) map[string]any {
		return map[string]any{"host": "devbox", "call": map[string]any{"session": "11111111-2222-3333-4444-555555555555", "op": "read",
			"project": project, "from": map[string]any{"machine": "", "agent": "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE", "who": ""}}}
	}
	if out, err := h.agentCall(MethodSessionCall, call("p-app")); err != nil || out.(wire.SessionCallReply).Said != "read" {
		t.Fatalf("%v %+v", err, out)
	}
	var sent wire.SessionCall
	if d := h.router.deliveries(); len(d) != 1 || json.Unmarshal(d[0].params, &sent) != nil || deref(sent.Project) != "p-app" {
		t.Fatalf("%+v", d)
	}
	// Not a project this machine has a folder of.
	if _, err := h.agentCall(MethodSessionCall, call("p-other")); failureOf(err) == nil || failureOf(err).Code != CodeNotPermitted {
		t.Fatalf("%v", err)
	}
	if _, err := h.agentCall(MethodProjectSessions, map[string]any{"project": "p-other"}); failureOf(err) == nil || failureOf(err).Code != CodeNotPermitted {
		t.Fatalf("%v", err)
	}
	if _, err := h.agentCall(MethodProjectSessions, map[string]any{"project": "p-app"}); err != nil || len(asked) != 1 || asked[0].Project != "p-app" {
		t.Fatalf("%v %+v", err, asked)
	}

	out, err := h.agentCall(MethodProjectFolders, map[string]any{"project": "p-app"})
	if m := out.(wire.ProjectFolders).Machines; err != nil || len(m) != 2 || m[0].MachineName != "devbox" || m[0].Folders[0] != "/home/me/app" {
		t.Fatalf("%v %+v", err, out)
	}
	// Another person's machine is not there.
	h.providers.hooks.OwnerOf = func(host string) string {
		if host == devbox {
			return "kim"
		}
		return "lee"
	}
	out, _ = h.agentCall(MethodProjectFolders, map[string]any{"project": "p-app"})
	if m := out.(wire.ProjectFolders).Machines; len(m) != 1 || m[0].MachineName != "other" {
		t.Fatalf("%+v", m)
	}
}

// #3492 (D7): a machine that holds its person's lock signs what the other machine is to
// get. The control plane delivers it as it came, not in its own words, and only as the
// method the call is for.
func TestASignedStartGoesAsTheAskingMachineSignedIt(t *testing.T) {
	h := newHarness(t)
	made := records.NewUUID()
	h.router.mu.Lock()
	h.router.answer = func(host, method string, params json.RawMessage) *Reply { return &Reply{Result: rawOf(made)} }
	h.router.mu.Unlock()
	h.router.setOnline(devbox, true)
	signed := `{"runtimeID":"claude", "cwd":"file:///home/me/app","prompt":"<b>&</b>","startedFrom":{"machine":"other","agent":"AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE","who":"Claude"}}`
	deliver := &wire.LockDelivery{Method: MethodAgentsStart, Params: signed, Lock: wire.LockSignature{At: time.Now().Unix(), Key: []byte{4}, Sig: []byte{1}}}
	call := func(d *wire.LockDelivery) (any, error) {
		return h.providers.HostSaid(other, MethodHostMachines, rawOf(MachineWorkspaceCall{Agent: "Claude (fix-login)", Method: MethodSessionOnMachine,
			Params: rawOf(sessionOn("devbox")), Deliver: d}))
	}
	if out, err := call(deliver); err != nil || string(out.(wire.UUID)) != made {
		t.Fatalf("%v %+v", err, out)
	}
	sent := h.router.deliveries()
	if len(sent) != 1 || sent[0].method != MethodAgentsStart || string(sent[0].params) != signed {
		t.Fatalf("not delivered as signed: %+v", sent)
	}
	// A signed request for another method is not delivered as the start.
	terminal := *deliver
	terminal.Method = "mac/terminal"
	if _, err := call(&terminal); failureOf(err) == nil {
		t.Fatal("a signed terminal went as a start")
	}
	if len(h.router.deliveries()) != 1 {
		t.Fatal("it was delivered")
	}
}

// #7118: a part of an upload that is only in what the asking machine signed. The control
// plane delivers it as it was signed, to that host, and only as one step, of that method.
func TestASignedUploadGoesWithoutASecondCopy(t *testing.T) {
	h := newHarness(t)
	h.router.mu.Lock()
	h.router.answer = func(host, method string, params json.RawMessage) *Reply {
		return &Reply{Result: rawOf(map[string]any{})}
	}
	h.router.mu.Unlock()
	h.router.setOnline(devbox, true)
	part := `{"upload":"` + records.NewUUID() + `", "offset":0,"data":"aGVsbG8="}`
	call := func(params map[string]any, d *wire.LockDelivery) error {
		_, err := h.providers.HostSaid(other, MethodHostMachines, rawOf(MachineWorkspaceCall{Agent: "Claude (fix-login)", Method: MethodSendFolder,
			Params: rawOf(params), Deliver: d}))
		return err
	}
	signed := func(method, params string) *wire.LockDelivery {
		return &wire.LockDelivery{Method: method, Params: params, Lock: wire.LockSignature{At: time.Now().Unix(), Key: []byte{4}, Sig: []byte{1}}}
	}
	step := map[string]any{"host": "devbox", "uploadSigned": true}
	upload := map[string]any{"upload": records.NewUUID(), "offset": 0, "data": "aGVsbG8="}
	for why, c := range map[string]struct {
		params map[string]any
		d      *wire.LockDelivery
	}{
		"not signed":              {step, nil},
		"another method":          {step, signed("mac/terminal", part)},
		"another step of a send":  {step, signed(MethodReceive, part)},
		"not a part":              {step, signed(MethodReceiveUpload, `{"folder":"/home/me/app"}`)},
		"not JSON":                {step, signed(MethodReceiveUpload, `{`)},
		"two steps":               {map[string]any{"host": "devbox", "uploadSigned": true, "upload": upload}, signed(MethodReceiveUpload, part)},
		"two steps, another kind": {map[string]any{"host": "devbox", "uploadSigned": true, "state": map[string]any{"folder": "/a"}}, signed(MethodReceiveUpload, part)},
		"no step":                 {map[string]any{"host": "devbox", "uploadSigned": false}, signed(MethodReceiveUpload, part)},
	} {
		if err := call(c.params, c.d); failureOf(err) == nil || failureOf(err).Code != CodeInvalidParams {
			t.Errorf("%s: %v", why, err)
		}
	}
	if len(h.router.deliveries()) != 0 {
		t.Fatal("it was delivered")
	}
	if err := call(step, signed(MethodReceiveUpload, part)); err != nil {
		t.Fatal(err)
	}
	// A machine that does not know the form, or a machine's own step, goes as before.
	if err := call(map[string]any{"host": "devbox", "upload": upload}, signed(MethodReceiveUpload, part)); err != nil {
		t.Fatal(err)
	}
	if err := call(map[string]any{"host": "devbox", "upload": upload}, nil); err != nil {
		t.Fatal(err)
	}
	sent := h.router.deliveries()
	if len(sent) != 3 || sent[0].host != devbox || sent[0].method != MethodReceiveUpload || string(sent[0].params) != part ||
		string(sent[1].params) != part || sent[2].method != MethodReceiveUpload {
		t.Fatalf("not delivered as signed: %+v", sent)
	}
	h.providers.hooks.OwnerOf = func(host string) string {
		if host == devbox {
			return "kim"
		}
		return "lee"
	}
	if err := call(step, signed(MethodReceiveUpload, part)); failureOf(err) == nil || failureOf(err).Code != CodeNoSuchHost {
		t.Fatalf("another person's machine: %v", err)
	}
}

// The bot's key between the person's machines (#3950): it goes only as the asking machine
// signed it, never in the control plane's own words, and never to another person's machine.
func TestTheBotsKeyGoesOnlyAsItsMachineSignedIt(t *testing.T) {
	h := newHarness(t)
	h.router.mu.Lock()
	h.router.answer = func(host, method string, params json.RawMessage) *Reply {
		return &Reply{Result: rawOf(map[string]any{})}
	}
	h.router.mu.Unlock()
	h.router.setOnline(devbox, true)
	sealed := `{"appId":42,"version":1,"sealed":{"encapsulated":"AQ==","ciphertext":"Ag=="}}`
	params := map[string]any{"host": "devbox", "copy": json.RawMessage(sealed)}
	call := func(d *wire.LockDelivery) (any, error) {
		return h.providers.HostSaid(other, MethodHostMachines, rawOf(MachineWorkspaceCall{Agent: "the agents' bot", Method: MethodBotKey,
			Params: rawOf(params), Deliver: d}))
	}
	if _, err := call(nil); failureOf(err) == nil {
		t.Fatal("an unsigned copy went")
	}
	deliver := &wire.LockDelivery{Method: MethodBotTake, Params: sealed, Lock: wire.LockSignature{At: time.Now().Unix(), Key: []byte{4}, Sig: []byte{1}}}
	terminal := *deliver
	terminal.Method = "mac/terminal"
	if _, err := call(&terminal); failureOf(err) == nil {
		t.Fatal("a signed terminal went as the bot's key")
	}
	if len(h.router.deliveries()) != 0 {
		t.Fatal("it was delivered")
	}
	if _, err := call(deliver); err != nil {
		t.Fatal(err)
	}
	if sent := h.router.deliveries(); len(sent) != 1 || sent[0].method != MethodBotTake || string(sent[0].params) != sealed {
		t.Fatalf("not delivered as signed: %+v", sent)
	}
	h.providers.hooks.OwnerOf = func(host string) string {
		if host == devbox {
			return "kim"
		}
		return "lee"
	}
	if _, err := call(deliver); failureOf(err) == nil || failureOf(err).Code != CodeNoSuchHost {
		t.Fatalf("another person's machine: %v", err)
	}
}

// #7120: the control plane says how many parts may be in flight only for a machine that takes
// them in any order, and holds no more than that many to one machine.
func TestASendHasSeveralPartsInFlight(t *testing.T) {
	h := newHarness(t)
	began, letGo := make(chan struct{}, 2*SendPartsAtOnce), make(chan struct{})
	h.router.mu.Lock()
	h.router.answer = func(_, method string, _ json.RawMessage) *Reply {
		if method == MethodReceiveUpload {
			began <- struct{}{}
			<-letGo
		}
		return &Reply{Result: rawOf(wire.SendRead{Data: wire.Data("hello"), End: true})}
	}
	h.router.mu.Unlock()
	h.router.setOnline(devbox, true)
	read := map[string]any{"host": "devbox", "read": map[string]any{"id": records.NewUUID(), "offset": 0}}

	out, err := h.agentCall(MethodSendFolder, read)
	if n := out.(wire.MachineFolderSent).PartsAtOnce; err != nil || n == nil || *n != SendPartsAtOnce {
		t.Fatalf("%v %+v", err, out)
	}
	h.router.mu.Lock()
	h.router.lacks = map[string]bool{devbox + " " + wire.CapFoldersSendParts1: true}
	h.router.mu.Unlock()
	out, err = h.agentCall(MethodSendFolder, read)
	if n := out.(wire.MachineFolderSent).PartsAtOnce; err != nil || n != nil {
		t.Fatalf("an older machine: %v %+v", err, out)
	}

	ended := make(chan error, SendPartsAtOnce+1)
	for range SendPartsAtOnce + 1 {
		go func() {
			_, err := h.agentCall(MethodSendFolder, map[string]any{"host": "devbox",
				"upload": map[string]any{"upload": records.NewUUID(), "offset": 0, "data": "aGVsbG8="}})
			ended <- err
		}()
	}
	for range SendPartsAtOnce {
		<-began
	}
	select {
	case <-began:
		t.Fatal("more parts than places")
	case <-time.After(100 * time.Millisecond):
	}
	close(letGo)
	for range SendPartsAtOnce + 1 {
		if err := <-ended; err != nil {
			t.Fatal(err)
		}
	}
}

func TestAPartOfASend(t *testing.T) {
	for call, want := range map[string]bool{
		`{"method":"machines/sendFolder","params":{"host":"a","upload":{"offset":0}}}`:    true,
		`{"method":"machines/sendFolder","params":{"host":"a","uploadSigned":true}}`:      true,
		`{"method":"machines/sendFolder","params":{"host":"a","read":{"offset":0}}}`:      true,
		`{"method":"machines/sendFolder","params":{"host":"a","receive":{"folder":"x"}}}`: false,
		`{"method":"machines/forAgent","params":{"read":{}}}`:                             false,
		`[]`: false,
	} {
		if IsPart(MethodHostMachines, json.RawMessage(call)) != want {
			t.Error(call)
		}
	}
	if IsPart("machine/status", json.RawMessage(`{"method":"machines/sendFolder","params":{"read":{}}}`)) {
		t.Error("another method")
	}
}
