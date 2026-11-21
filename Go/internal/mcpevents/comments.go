package mcpevents

import "strings"

// AgentMarker starts a comment an agent posted as the person (#3860), where agents post as the
// person's own account: pr.commented leaves it out, so a comment it raises is a person's.
const AgentMarker = "<!-- agent -->"

// ByAgent says a comment's body starts with AgentMarker, after any leading space.
func ByAgent(body string) bool {
	return strings.HasPrefix(strings.TrimSpace(body), AgentMarker)
}
