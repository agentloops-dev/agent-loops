package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// What a key does at the prompt (wireframe 1): ⏎ sends, ⌥⏎ (or ^J) adds a line, ^C stops
// the turn, ^D leaves. A paste (bracketed paste) keeps its lines.
type keyKind int

const (
	keyText keyKind = iota
	keyEnter
	keyNewline
	keyBackspace
	keyClear // ^U: the whole prompt
	keyInterrupt
	keyEOF
	keyBackground // the terminal's answer to askBackground: dark says how it is
)

type key struct {
	kind keyKind
	text string
	dark bool
}

// keyReader turns the bytes of a terminal in raw mode into keys. Escapes it does not use
// (arrows, function keys) are dropped whole.
type keyReader struct {
	pasting bool
	pending []byte
}

// feed is the keys in b, with what it holds of an escape not ended yet.
func (k *keyReader) feed(b []byte) []key {
	buf := append(k.pending, b...)
	k.pending = nil
	var out []key
	text := func(s string) {
		if n := len(out); n > 0 && out[n-1].kind == keyText {
			out[n-1].text += s
			return
		}
		out = append(out, key{kind: keyText, text: s})
	}
	for i := 0; i < len(buf); {
		c := buf[i]
		if c == 0x1b {
			n, ks, ok := k.escape(buf[i:])
			if !ok {
				k.pending = append([]byte(nil), buf[i:]...)
				return out
			}
			out = append(out, ks...)
			i += n
			continue
		}
		switch {
		case k.pasting && (c == '\r' || c == '\n'):
			text("\n")
		case c == '\r':
			out = append(out, key{kind: keyEnter})
		case c == '\n':
			out = append(out, key{kind: keyNewline})
		case c == 0x7f || c == 0x08:
			out = append(out, key{kind: keyBackspace})
		case c == 0x15:
			out = append(out, key{kind: keyClear})
		case c == 0x03:
			out = append(out, key{kind: keyInterrupt})
		case c == 0x04:
			out = append(out, key{kind: keyEOF})
		case c == '\t':
			text("    ")
		case c < 0x20:
		default:
			if !utf8.FullRune(buf[i:]) {
				k.pending = append([]byte(nil), buf[i:]...)
				return out
			}
			r, size := utf8.DecodeRune(buf[i:])
			text(string(r))
			i += size
			continue
		}
		i++
	}
	return out
}

// escape reads one escape at the front of b: how many bytes it took and its keys, or not
// ok when b ends inside it.
func (k *keyReader) escape(b []byte) (int, []key, bool) {
	if len(b) < 2 {
		return 0, nil, false
	}
	switch b[1] {
	case '\r', '\n':
		return 2, []key{{kind: keyNewline}}, true // ⌥⏎ as Esc+
	case '[':
		for j := 2; j < len(b); j++ {
			if b[j] >= 0x40 && b[j] <= 0x7e {
				switch string(b[2 : j+1]) {
				case "200~":
					k.pasting = true
				case "201~":
					k.pasting = false
				case "13;3u", "13;2u": // ⌥⏎ and ⇧⏎ where the terminal reports them (CSI u)
					return j + 1, []key{{kind: keyNewline}}, true
				}
				return j + 1, nil, true
			}
		}
		return 0, nil, false
	case ']':
		for j := 2; j < len(b); j++ {
			end := 0
			if b[j] == 0x07 {
				end = j + 1
			} else if b[j] == 0x1b && j+1 < len(b) && b[j+1] == '\\' {
				end = j + 2
			}
			if end > 0 {
				if dark, ok := backgroundReply(string(b[2:j])); ok {
					return end, []key{{kind: keyBackground, dark: dark}}, true
				}
				return end, nil, true
			}
		}
		return 0, nil, false
	}
	return 2, nil, true
}

// askBackground asks the terminal for its background colour (OSC 11); the answer comes as
// a keyBackground.
const askBackground = "\x1b]11;?\x07"

// backgroundReply reads "11;rgb:RRRR/GGGG/BBBB": dark when its light is under half.
func backgroundReply(s string) (dark, ok bool) {
	rest, found := strings.CutPrefix(s, "11;rgb:")
	if !found {
		return false, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return false, false
	}
	var v [3]float64
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 16, 32)
		if err != nil || len(p) == 0 {
			return false, false
		}
		v[i] = float64(n) / float64(uint64(1)<<(4*len(p))-1)
	}
	return 0.2126*v[0]+0.7152*v[1]+0.0722*v[2] < 0.5, true
}

// screen is the terminal: the lines that scroll up, and under them the part that is drawn
// again each time something changes: the agent's line not ended yet, the status (working,
// needs you) and the prompt. In raw mode a line ends with \r\n.
type screen struct {
	w       io.Writer
	raw     bool
	width   func() int
	drawn   int  // rows the live part took when it was last drawn, under the cursor's row
	showing bool // the live part is on the screen
	partial string
	status  string
	prompt  bool
	input   string
}

func (s *screen) eol() string {
	if s.raw {
		return "\r\n"
	}
	return "\n"
}

// rows is how many rows text takes at the terminal's width.
func (s *screen) rows(text string) int {
	w := 0
	if s.width != nil {
		w = s.width()
	}
	n := visibleWidth(text)
	if w <= 0 || n <= w {
		return 1
	}
	return (n + w - 1) / w
}

// clear takes the live part off the screen; the cursor goes back to where it started.
func (s *screen) clear() {
	if !s.showing {
		return
	}
	var b strings.Builder
	b.WriteString("\r")
	if s.drawn > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", s.drawn)
	}
	b.WriteString("\x1b[J")
	_, _ = io.WriteString(s.w, b.String())
	s.showing, s.drawn = false, 0
}

// live is the live part's lines.
func (s *screen) live() []string {
	var out []string
	if s.partial != "" {
		out = append(out, s.partial)
	}
	if s.status != "" {
		out = append(out, s.status)
	}
	if s.prompt {
		lines := strings.Split(s.input, "\n")
		out = append(out, "› "+lines[0])
		for _, l := range lines[1:] {
			out = append(out, "  "+l)
		}
	}
	return out
}

// draw puts the live part back, the cursor at its end.
func (s *screen) draw() {
	lines := s.live()
	if len(lines) == 0 || !s.raw {
		return
	}
	rows := 0
	for i, l := range lines {
		rows += s.rows(l)
		if i == len(lines)-1 {
			rows--
		}
	}
	_, _ = io.WriteString(s.w, strings.Join(lines, s.eol()))
	s.showing, s.drawn = true, rows
}

// print puts lines in the part that scrolls, above the live part.
func (s *screen) print(lines ...string) {
	if len(lines) == 0 {
		return
	}
	s.clear()
	_, _ = io.WriteString(s.w, strings.Join(lines, s.eol())+s.eol())
	s.draw()
}

// update draws the live part again after a change to it.
func (s *screen) update() {
	s.clear()
	s.draw()
}

// end takes the live part off for good; a partial line goes in the lines.
func (s *screen) end() {
	s.clear()
	if s.partial != "" {
		_, _ = io.WriteString(s.w, s.partial+s.eol())
		s.partial = ""
	}
	s.prompt, s.status = false, ""
}
