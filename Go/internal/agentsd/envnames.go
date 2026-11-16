package agentsd

import "strings"

// envReservedEverywhere are the names neither file may set: Agents' own, the system's, and
// what points a runtime at its model, account or sandbox. A person's PATH or API key would
// break the runtime or change which account it bills (#5256). A name ending in _ is a prefix.
var envReservedEverywhere = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "PWD",
	"AGENTS_", "CLAUDE_", "CODEX_", "GEMINI_", "ANTHROPIC_", "OPENAI_", "AGY_", "GROK_CONFIG_PATH",
	"GH_TOKEN", "NO_BROWSER", "TERM", "TERM_PROGRAM"}

// envReservedInRepo are the names a repo's file may not set as well: a cloned repo is not
// trusted, and each of these runs code or changes what git does.
var envReservedInRepo = []string{"NODE_OPTIONS", "LD_", "DYLD_", "BASH_ENV", "ENV", "ZDOTDIR", "PYTHONSTARTUP",
	"PYTHONPATH", "PERL5OPT", "PERL5LIB", "RUBYOPT", "GIT_"}

func envNameIn(name string, list []string) bool {
	for _, n := range list {
		if name == n || strings.HasSuffix(n, "_") && strings.HasPrefix(name, n) {
			return true
		}
	}
	return false
}

// envRefusal is why a file of the layer may not give the name, or "".
func envRefusal(name string, repo bool) string {
	if envNameIn(name, envReservedEverywhere) || envNameIn(name, runtimeVariables) {
		return name + " is set by Agents or the system, so a file of variables cannot set it."
	}
	if repo && envNameIn(name, envReservedInRepo) {
		return name + " can make programs run other code, so a repo's file cannot set it. Put it in your own ~/.agents/env."
	}
	return ""
}
