// Made by scripts/design.sh gen from design/tokens.json (#4602, #6446). Do not edit:
// change the JSON and run gen again.

package main

// paletteTokens is every colour token: name, light, dark.
var paletteTokens = []paletteToken{
	// The page every column sits on: near-white paper.
	{"ground", "#fcfbf9", "#1a1a19"},
	// The folders column, one small step from the page; a rule sets it apart.
	{"sidebar", "#f8f7f4", "#171716"},
	// Cards, the prompt bar, menus, sheets. Set apart by a rule, not a shadow.
	{"raised", "#ffffff", "#212120"},
	// What is set into the page: code, your own message, a filled field.
	{"well", "#f6f5f2", "#262625"},
	// Hover; a pressed quiet button.
	{"wash", "#f1f0ec", "#2e2e2c"},
	// The line that sets things apart, in place of a shadow (1.5:1): rows, cards, columns.
	{"rule", "#d6d2c9", "#3d3d3a"},
	// The edge of a field, a checkbox, a toggle's track: 3:1 or more, so a control can be found (WCAG 1.4.11).
	{"edge", "#7d786e", "#85817a"},
	// Text and icons: deep charcoal, 17:1 on the ground.
	{"ink", "#1a1918", "#f4f2ed"},
	// A row's second line, a speaker's name, a section heading.
	{"secondary", "#4a4741", "#bdb9b0"},
	// Timestamps, counts, placeholders.
	{"tertiary", "#625e57", "#a29e95"},
	// The picked row, with a 3px accent bar at its edge. Every text colour is 4.5:1 or more on it.
	{"selection", "#ebe9e4", "#363633"},
	// The app icon's cobalt: links, focus, the chosen control, a switch that is on.
	{"accent", "#1a52c0", "#86b4ff"},
	// The chosen thing, a link card.
	{"line.accent", "#a9c2ee", "#3d5687"},
	// Needs you: the yellow's line (#5016).
	{"line.attention", "#e3d196", "#615326"},
	// A warning: the orange's line (#5016).
	{"line.warning", "#ebbf9f", "#6b4a33"},
	// Done, approved.
	{"line.vouched", "#a9d3bb", "#36614d"},
	// Failed.
	{"line.failure", "#eeb0b3", "#6b3a3d"},
	// Needs you: a question, a request, a wait only the person can settle; the only tint drawn in a list. Yellow, never the warning's orange (#5016).
	{"tint.attention", "#7a6200", "#e6c04f"},
	// A warning: something is wrong or nearly so, and a person should know. Not a request (#5016).
	{"tint.warning", "#a3400e", "#f3a672"},
	// Failed.
	{"tint.failure", "#b0222a", "#f4878d"},
	// Approved, done: an ending the app can vouch for.
	{"tint.vouched", "#25684f", "#92ccad"},
	// A label naming an issue, #12.
	{"label.issue", "#0c6278", "#73cbe0"},
	// A label naming a pull request, P#12.
	{"label.pull", "#6c3cae", "#bea3f2"},
	// What is drawn on a filled mark: a state's badge, a swipe's action.
	{"on-fill", "#ffffff", "#ffffff"},
	// Behind a sheet or a viewer, mixed with transparent to the strength it needs.
	{"scrim", "#000000", "#000000"},
	// Open; a check that passed; running.
	{"forge.open", "#1a7f37", "#3fb950"},
	// A closed issue, a merged pull request.
	{"forge.done", "#8250df", "#a371f7"},
	// A pull request closed unmerged; a check that failed; changes asked for.
	{"forge.closed", "#cf222e", "#f85149"},
	// A draft, an issue not planned.
	{"forge.draft", "#6e7781", "#8b949e"},
	// A check still running; an agent that waits on the person.
	{"forge.waiting", "#9a6700", "#d29922"},
	// Under a notice in the forge's amber.
	{"forge.waiting-wash", "#fff8c5", "#2f2a1a"},
	// The edge of a notice in the forge's amber.
	{"forge.waiting-rule", "#d4a72c", "#6b5a1e"},
	// The near-black disc under a connector's letters (GH, GL) in its set-up sheet.
	{"forge.mark", "#212121", "#212121"},
	// An open badge; the merge button.
	{"forge.fill.open", "#1f883d", "#238636"},
	// A merged or closed badge.
	{"forge.fill.done", "#8250df", "#8957e5"},
	// A closed pull request's badge.
	{"forge.fill.closed", "#cf222e", "#da3633"},
	// A draft's or a not planned issue's badge.
	{"forge.fill.draft", "#59636e", "#6e7681"},
	// Under an added line.
	{"diff.added.wash", "#e6ffec", "#1d2e1f"},
	// The bar at an added line's edge.
	{"diff.added.bar", "#4ac26b", "#2ea043"},
	// Under the words that an added line changed.
	{"diff.added.word", "#abf2bc", "#22502a"},
	// Under a removed line.
	{"diff.removed.wash", "#ffebe9", "#3b2220"},
	// The bar at a removed line's edge.
	{"diff.removed.bar", "#ff8182", "#f85149"},
	// Under the words that a removed line changed.
	{"diff.removed.word", "#ffcecb", "#73302c"},
	// A comment, in italic.
	{"code.comment", "#64707a", "#a3adb8"},
	// A keyword, true, false, null.
	{"code.keyword", "#2b4c8c", "#8fb0e8"},
	// A string.
	{"code.string", "#4e6420", "#a9c27a"},
	// A number.
	{"code.number", "#7a3e7a", "#d5a3d5"},
	// A type.
	{"code.type", "#1f6a6a", "#7cc4c0"},
	// A function.
	{"code.function", "#5b4bb5", "#b3a6f0"},
	// A property.
	{"code.property", "#52627a", "#bac5d7"},
	// Punctuation and operators.
	{"code.punctuation", "#5e6470", "#a9b2bd"},
	// The plot behind the lines.
	{"timeline.plot", "#ffffff", "#0d1429"},
	// The axis's marks.
	{"timeline.faint", "#8c8c8c", "#999999"},
	// The trunk every feature leaves from.
	{"timeline.trunk", "#999999", "#e7e8ea"},
	// A release's ring on the trunk.
	{"timeline.release", "#000000", "#ffffff"},
	// The Now line.
	{"timeline.now", "#8c8c8c", "#8c8c8c"},
}
