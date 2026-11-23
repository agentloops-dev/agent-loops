package agentsd

import "testing"

// Cursor's spent plan (#6817): its refusal is the turn's own words, after blank lines.
func TestCursorsSpentPlanIsSeen(t *testing.T) {
	t.Parallel()

	code := 400
	for _, c := range []struct {
		name    string
		code    *int
		message string
		said    string
		want    limitKind
	}{
		{name: "the words of #6308", said: "\n\nUpgrade your plan to continue", want: limitSpent},
		{name: "no blank lines", said: "Upgrade your plan to continue", want: limitSpent},
		{name: "spaces and a tab", said: " \t\r\nUpgrade your plan to continue\n", want: limitSpent},
		{name: "a refusal's message", code: &code, message: "\nUpgrade your plan to continue", want: limitSpent},
		{name: "quoted later in a reply", said: "I read the error. It said \"Upgrade your plan to continue\", so I stopped.", want: limitNone},
		{name: "quoted after blank lines", said: "\n\nThe docs say: Upgrade your plan to continue", want: limitNone},
		{name: "work done", said: "\n\nDone: the tests pass.", want: limitNone},
	} {
		if got := wordsLimit("cursor", c.code, c.message, c.said); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// The trim is for every runtime: blank lines before another runtime's refusal hide nothing.
func TestBlankLinesBeforeARefusalHideNothing(t *testing.T) {
	t.Parallel()

	for runtime, said := range map[string]string{
		"copilot":     "\n\nYou have exceeded your monthly quota",
		"antigravity": "\n\nUsage Limit Reached",
		"opencode":    "\n\nFree usage limit reached.",
	} {
		if got := wordsLimit(runtime, nil, "", said); got != limitSpent {
			t.Errorf("%s: got %v", runtime, got)
		}
	}
}
