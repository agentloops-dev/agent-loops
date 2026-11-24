package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"
	"golang.org/x/text/width"
)

// The terminal's colours are the design tokens (design/tokens.json, #4602), mapped to ANSI
// (gate 2 of #6443): 24-bit colour where the terminal says it has it (COLORTERM), else the
// nearest of the 256-colour cube. Colour only on a terminal, and never with NO_COLOR
// (no-color.org). The terminal's background picks the light or the dark value.

type paletteToken struct{ name, light, dark string }

// role is what a piece of text is, and the token that draws it.
type role string

const (
	roleBrand     role = "accent"         // "Agent Loops", the agent's name, the spinner
	roleYou       role = "secondary"      // "you"
	roleQuiet     role = "tertiary"       // tool calls, hints, counts, times
	roleAttention role = "tint.attention" // a card: needs you
	roleWarning   role = "tint.warning"   // a notice
	roleFailure   role = "tint.failure"   // a failed tool call, an error
	roleDone      role = "tint.vouched"   // a card answered, a turn done
)

// style writes text in a role's colour, or as it is when colour is off.
type style struct {
	on   bool
	full bool // 24-bit colour; else the 256-colour cube
	dark bool
}

// plain is no colour at all.
var plain = style{}

// detectStyle is the style for this process's stdout: tty says stdout is a terminal.
func detectStyle(tty bool, env func(string) string) style {
	if !tty || env("NO_COLOR") != "" || env("TERM") == "dumb" {
		return plain
	}
	ct := strings.ToLower(env("COLORTERM"))
	return style{on: true, full: ct == "truecolor" || ct == "24bit", dark: darkBackground(env)}
}

func styleOf(w io.Writer) style {
	f, ok := w.(*os.File)
	return detectStyle(ok && term.IsTerminal(int(f.Fd())), osEnv)
}

// darkBackground reads LOOPS_THEME (light or dark), else COLORFGBG ("15;0": the background
// is the last number, 0 to 6 and 8 dark), else takes a dark background, as most terminals
// that agents run in have. The live session asks the terminal itself (askBackground).
func darkBackground(env func(string) string) bool {
	switch strings.ToLower(env("LOOPS_THEME")) {
	case "light":
		return false
	case "dark":
		return true
	}
	if fgbg := env("COLORFGBG"); fgbg != "" {
		parts := strings.Split(fgbg, ";")
		if bg, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			return bg <= 6 || bg == 8
		}
	}
	return true
}

func tokenHex(name string, dark bool) string {
	for _, t := range paletteTokens {
		if t.name == name {
			if dark {
				return t.dark
			}
			return t.light
		}
	}
	return ""
}

func rgb(hex string) (r, g, b int, ok bool) {
	if len(hex) != 7 || hex[0] != '#' {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff), true
}

// cube is the nearest colour of the 256-colour palette's 6×6×6 cube or its grey ramp.
func cube(r, g, b int) int {
	steps := [6]int{0, 95, 135, 175, 215, 255}
	near := func(v int) int {
		best := 0
		for i, s := range steps {
			if abs(v-s) < abs(v-steps[best]) {
				best = i
			}
		}
		return best
	}
	ri, gi, bi := near(r), near(g), near(b)
	cr, cg, cb := steps[ri], steps[gi], steps[bi]
	grey := (r + g + b) / 3
	gi2 := min(max((grey-8+5)/10, 0), 23)
	gv := 8 + gi2*10
	if dist(r, g, b, gv, gv, gv) < dist(r, g, b, cr, cg, cb) {
		return 232 + gi2
	}
	return 16 + 36*ri + 6*gi + bi
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func dist(r, g, b, r2, g2, b2 int) int {
	return (r-r2)*(r-r2) + (g-g2)*(g-g2) + (b-b2)*(b-b2)
}

// sgr is the escape that starts a role's colour.
func (s style) sgr(ro role) string {
	r, g, b, ok := rgb(tokenHex(string(ro), s.dark))
	if !ok {
		return ""
	}
	if s.full {
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
	}
	return fmt.Sprintf("\x1b[38;5;%dm", cube(r, g, b))
}

// paint is text in a role's colour.
func (s style) paint(ro role, text string) string {
	if !s.on || text == "" {
		return text
	}
	return s.sgr(ro) + text + "\x1b[39m"
}

// bold is text in bold.
func (s style) bold(text string) string {
	if !s.on || text == "" {
		return text
	}
	return "\x1b[1m" + text + "\x1b[22m"
}

// osEnv is os.Getenv, for detectStyle.
func osEnv(k string) string { return os.Getenv(k) }

// cellWidth is how many cells a rune takes: wide CJK and emoji take two, combining marks
// and controls none. A regional indicator is one cell; a pair makes a two-cell flag.
func cellWidth(r rune) int {
	if r < 0x20 || (r >= 0x7f && r < 0xa0) || r == 0x200d || r == 0xfe0f || unicode.Is(unicode.Mn, r) {
		return 0
	}
	if r >= 0x1f1e6 && r <= 0x1f1ff {
		return 1
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	if r >= 0x1f000 && r <= 0x1faff {
		return 2
	}
	return 1
}

// visibleWidth is how many cells text takes, without its escapes.
func visibleWidth(text string) int {
	n := 0
	for i := 0; i < len(text); {
		if text[i] == 0x1b {
			j := i + 1
			if j < len(text) && text[j] == '[' {
				j++
				for j < len(text) && (text[j] < 0x40 || text[j] > 0x7e) {
					j++
				}
			}
			i = j + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		i += size
		n += cellWidth(r)
	}
	return n
}
