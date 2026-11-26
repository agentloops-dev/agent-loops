package workspacecommands

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
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
		{"docker", map[string]any{}, false},
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
