package workspacecommands

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Root set-up (#7154): the lines that run root.sh, and the presets that run them.

func TestRootBootIsNothingWithNoScript(t *testing.T) {
	if got := RootBoot(nil); got != "" {
		t.Errorf("%q", got)
	}
}

func TestRootBootRunsTheScriptAsWrittenWithItsLimitAndWritesTheResultLast(t *testing.T) {
	script := []byte("apt-get install -y gh\n")
	lines := RootBoot(script)
	for _, want := range []string{
		"install -d -m 755 /var/lib/agents-setup\n",
		"printf %s '" + base64.StdEncoding.EncodeToString(script) + "' | base64 -d > /var/lib/agents-setup/root.sh\n",
		"timeout -k 10 600 bash -e /var/lib/agents-setup/root.sh >/var/lib/agents-setup/root.log 2>&1 </dev/null",
		"agents_root_done",
		"mv /var/lib/agents-setup/root.exit.new /var/lib/agents-setup/root.exit ) &",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("no %q in\n%s", want, lines)
		}
	}
	// The restart comes before the result, so the installer sees it once the host has the group.
	if strings.Index(lines, "agents_root_done") > strings.Index(lines, "root.exit.new") {
		t.Errorf("the result is written before the restart:\n%s", lines)
	}
}

func TestTheLargestRootScriptFitsInRootBoot(t *testing.T) {
	if got := len(RootBoot([]byte(strings.Repeat("x", RootScriptMost)))); got > RootBootMost {
		t.Errorf("%d characters, over %d", got, RootBootMost)
	}
}

func TestDigitalOceanAndGoogleRunRootBootBesideTheInstall(t *testing.T) {
	for name, first := range map[string]string{"digitalocean": digitalOceanFirstBoot, "gcp": gcpStartup} {
		root := strings.Index(first, "${rootBoot}")
		install := strings.Index(first, `su - agents -c`)
		user := strings.Index(first, "useradd")
		wait := strings.LastIndex(first, "\nwait\n")
		if user < 0 || root < user || install < root || wait < install {
			t.Errorf("%s: the user, then rootBoot, then the install, then a wait:\n%s", name, first)
		}
		// The restart waits for the installer's Root set-up step to begin.
		if !strings.Contains(first, "agents_root_done()") || !strings.Contains(first, ".agents-server/root/"+RootReadyFile) ||
			!strings.Contains(first, `systemctl restart "user@$uid.service"`) {
			t.Errorf("%s: no restart of the user's manager after root.sh:\n%s", name, first)
		}
	}
}

func TestRootBootRunsIsWhetherTheCreateHasIt(t *testing.T) {
	for _, preset := range []struct {
		name string
		o    map[string]any
		runs bool
	}{
		{"digitalocean-api", map[string]any{}, true},
		{"gcp-compute-api", map[string]any{"project": "p", "zone": "us-west1-b"}, true},
		{"docker", map[string]any{"image": "agents-devbox:latest"}, true},
		{"docker", map[string]any{}, false},
		{"codespaces", map[string]any{}, false},
	} {
		c, err := expandPreset(preset.name, preset.o)
		if err != nil {
			t.Fatal(preset.name, err)
		}
		if got := c.Making.RootBootRuns(); got != preset.runs {
			t.Errorf("%s: %v", preset.name, got)
		}
	}
}

func TestADropletsUserDataHasRootBootAndNoChoiceSetsIt(t *testing.T) {
	atOffset(t, -8)
	f, text := fakeDigitalOceanAccount(t, &doAccount{keys: map[string]bool{}})
	p, _ := apiProvider(t, text, DigitalOceanSecret, token)
	lines := RootBoot([]byte("apt-get install -y gh\n"))
	if _, _, err := p.CreateRooted("devbox-4000001", map[string]any{"rootBoot": "echo chosen", "firstBoot": "echo chosen"},
		"curl … | sh", lines); err != nil {
		t.Fatal(err)
	}
	data, _ := madeDroplet(t, f)["user_data"].(string)
	if !strings.Contains(data, lines) || !strings.Contains(data, "curl … | sh") || strings.Contains(data, "chosen") {
		t.Errorf("%s", data)
	}
}

func TestADropletWithNoRootScriptHasNoRootLines(t *testing.T) {
	atOffset(t, -8)
	f, text := fakeDigitalOceanAccount(t, &doAccount{keys: map[string]bool{}})
	p, _ := apiProvider(t, text, DigitalOceanSecret, token)
	if _, _, err := p.Create("devbox-4000001", nil, "curl … | sh"); err != nil {
		t.Fatal(err)
	}
	data, _ := madeDroplet(t, f)["user_data"].(string)
	if strings.Contains(data, "${rootBoot}") || strings.Contains(data, RootSetupFolder) {
		t.Errorf("%s", data)
	}
}

func TestTheServersCreateTakesRootBootAndSaysItRuns(t *testing.T) {
	atOffset(t, -8)
	f, text := fakeDigitalOceanAccount(t, &doAccount{keys: map[string]bool{}})
	p, _ := apiProvider(t, text, DigitalOceanSecret, token)
	s := NewServer(p.Config, quick, p.Run, "test")
	s.Provider.Env, s.Provider.Log, s.Provider.Folder = p.Env, p.Log, t.TempDir()
	lines := RootBoot([]byte("true\n"))
	answer, err := s.Handle("tools/call", map[string]any{"name": "workspace_create",
		"arguments": map[string]any{"name": "devbox-4000001", "firstBoot": "curl … | sh", "rootBoot": lines}})
	if err != nil {
		t.Fatal(err)
	}
	if at(answer, "structuredContent.rootBootRuns") != true || at(answer, "structuredContent.firstBootRuns") != true {
		t.Errorf("%v", answer)
	}
	if data, _ := madeDroplet(t, f)["user_data"].(string); !strings.Contains(data, lines) {
		t.Errorf("%s", data)
	}
	tools, _ := s.Handle("tools/list", nil)
	raw, _ := json.Marshal(tools)
	if !strings.Contains(string(raw), `"rootBoot"`) {
		t.Errorf("workspace_create does not declare rootBoot: %s", raw)
	}
}

// Docker and custom commands (#7159).

// rootBootSeen is a recorder that keeps what the file in each command's ${rootBoot} held while the
// create ran, and that file's name.
type rootBootSeen struct {
	file, text string
	mode       os.FileMode
}

func (s *rootBootSeen) look(line, prefix string) {
	rest, ok := strings.CutPrefix(line, prefix)
	if !ok {
		return
	}
	name, _, _ := strings.Cut(strings.TrimPrefix(rest, "'"), "'")
	if data, err := os.ReadFile(name); err == nil {
		s.file, s.text = name, string(data)
		if info, err := os.Stat(name); err == nil {
			s.mode = info.Mode().Perm()
		}
	}
}

func TestDockerCopiesRootBootInAndRunsItAsRootDetachedBeforeTheInstall(t *testing.T) {
	m := &made{}
	seen := &rootBootSeen{}
	r := &recorder{answer: func(line string) (int, string) {
		switch {
		case strings.HasPrefix(line, "docker ps"):
			return 0, m.list("\t")
		case strings.HasPrefix(line, "docker run"):
			m.add("build-2")
			seen.look(line[strings.Index(line, "docker cp "):], "docker cp ")
		}
		return 0, ""
	}}
	s := server(t, `{"preset":"docker","image":"agents-devbox:latest"}`, quick, r.run)
	lines := RootBoot([]byte("apt-get install -y gh\n"))
	raw, _ := json.Marshal(map[string]any{"name": "build-2", "firstBoot": "curl -fsSL https://x/install.sh | sh", "rootBoot": lines})
	answer := call(t, s, "workspace_create", string(raw))
	if at(answer, "structuredContent.rootBootRuns") != true {
		t.Errorf("%v", answer)
	}
	all := strings.Join(r.all(), "\n")
	// The lines are in a file of this user's alone, never on the command line, and it is gone after.
	if seen.text != lines+"\nwait\n" || seen.mode != 0o600 || strings.Contains(all, base64.StdEncoding.EncodeToString([]byte("apt-get install -y gh\n"))) {
		t.Errorf("file %q (%v): %q\n%s", seen.file, seen.mode, seen.text, all)
	}
	if _, err := os.Stat(seen.file); !os.IsNotExist(err) {
		t.Errorf("%s is still there: %v", seen.file, err)
	}
	cp := strings.Index(all, "docker cp '"+seen.file+"' 'build-2':/tmp/agents-root-boot.sh")
	root := strings.Index(all, "docker exec -d -u root 'build-2' sh /tmp/agents-root-boot.sh")
	install := strings.Index(all, "docker exec 'build-2' sh -c 'curl")
	if cp < 0 || root < cp || install < root {
		t.Errorf("not copy, then root detached, then the install:\n%s", all)
	}
}

func TestACustomCreateGetsRootBootAsAFile(t *testing.T) {
	m := &made{}
	seen := &rootBootSeen{}
	r := &recorder{answer: func(line string) (int, string) {
		if strings.HasPrefix(line, "cli list") {
			return 0, m.list(" ")
		}
		if strings.HasPrefix(line, "cli make") {
			m.add("build-2")
			seen.look(line[strings.Index(line, "--root ")+len("--root "):], "")
		}
		return 0, ""
	}}
	s := server(t, `{"list":"cli list","columns":["id","name","state"],
		"start":"cli start ${id}","stop":"cli stop ${id}",
		"create":"cli make ${name} --user-data ${firstBoot} --root ${rootBoot}"}`, quick, r.run)
	lines := RootBoot([]byte("true\n"))
	raw, _ := json.Marshal(map[string]any{"name": "build-2", "firstBoot": "x", "rootBoot": lines})
	answer := call(t, s, "workspace_create", string(raw))
	if at(answer, "structuredContent.rootBootRuns") != true || seen.text != lines+"\nwait\n" {
		t.Errorf("%v %q %q", answer, seen.text, r.all())
	}
	// With no root.sh, it is empty.
	call(t, s, "workspace_create", `{"name":"build-3","firstBoot":"x"}`)
	if !r.has("cli make 'build-3' --user-data 'x' --root ''") {
		t.Errorf("%q", r.all())
	}
}

func TestRootCommandRunsTheApprovedScriptAgainAndWaitsForIt(t *testing.T) {
	if RootCommand(nil) != "" {
		t.Error("a command for no root.sh")
	}
	script := []byte("echo hi\n")
	command := RootCommand(script)
	b64, ok := strings.CutPrefix(command, "printf %s ")
	b64, ok2 := strings.CutSuffix(b64, " | base64 -d | sudo sh")
	if !ok || !ok2 || strings.ContainsAny(b64, " '\"$;") {
		t.Fatalf("%q", command)
	}
	inner, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	text := string(inner)
	// The old result goes first, so Check reads the new one.
	if !strings.HasPrefix(text, "rm -f /var/lib/agents-setup/root.exit\n") || !strings.Contains(text, RootBoot(script)) ||
		!strings.Contains(text, "\nwait\n") {
		t.Errorf("%s", text)
	}
	if got := len(RootCommand([]byte(strings.Repeat("x", RootScriptMost)))); got > 32*1024 {
		t.Errorf("%d characters", got)
	}
}

// The copied command, run for real with the folder in a temporary one and no sudo: root.sh's
// log and result are where Root set-up reads them.
func TestRootCommandLeavesTheResultWhereRootSetUpReadsIt(t *testing.T) {
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("no timeout here")
	}
	folder := filepath.Join(t.TempDir(), "agents-setup")
	command := RootCommand([]byte("echo first\necho last\nexit 3\n"))
	b64 := strings.TrimSuffix(strings.TrimPrefix(command, "printf %s "), " | base64 -d | sudo sh")
	inner, _ := base64.StdEncoding.DecodeString(b64)
	script := strings.ReplaceAll(string(inner), RootSetupFolder, folder)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	// store-bounds: not a store: a test's old result, which the command takes away
	if err := os.WriteFile(filepath.Join(folder, "root.exit"), []byte("100\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.WaitDelay = 30 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	exit, _ := os.ReadFile(filepath.Join(folder, "root.exit"))
	log, _ := os.ReadFile(filepath.Join(folder, "root.log"))
	if strings.TrimSpace(string(exit)) != "3" || string(log) != "first\nlast\n" || !strings.Contains(string(out), "exit 3") {
		t.Errorf("exit %q, log %q, said %q", exit, log, out)
	}
}
