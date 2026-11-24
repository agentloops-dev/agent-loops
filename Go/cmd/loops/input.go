package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What a key does at the prompt (wireframe 1): ⏎ sends, ⌥⏎ (or ^J) adds a line, ^C stops
// the turn, ^D leaves. A paste (bracketed paste) keeps its lines.
type keyKind int

const (
	keyText keyKind = iota
	keyTab
	keyEnter
	keyNewline
	keyBackspace
	keyDelete
	keyLeft
	keyRight
	keyWordLeft
	keyWordRight
	keyHome
	keyEnd
	keyPrevLine
	keyNextLine
	keyDeleteWordBefore
	keyDeleteWordAfter
	keyKillToEnd
	keyKillToStart // ^U: the whole prompt, when the cursor is at its end
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
// are dropped whole.
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
		case c == 0x01: // ^A
			out = append(out, key{kind: keyHome})
		case c == 0x02: // ^B
			out = append(out, key{kind: keyLeft})
		case c == 0x04: // ^D; ^D on an empty prompt leaves, in live.key
			out = append(out, key{kind: keyEOF})
		case c == 0x05: // ^E
			out = append(out, key{kind: keyEnd})
		case c == 0x06: // ^F
			out = append(out, key{kind: keyRight})
		case c == 0x0b: // ^K
			out = append(out, key{kind: keyKillToEnd})
		case c == 0x15: // ^U
			out = append(out, key{kind: keyKillToStart})
		case c == 0x17: // ^W
			out = append(out, key{kind: keyDeleteWordBefore})
		case c == 0x03:
			out = append(out, key{kind: keyInterrupt})
		case c == '\t':
			out = append(out, key{kind: keyTab})
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
	one := func(kind keyKind) (int, []key, bool) {
		return 2, []key{{kind: kind}}, true
	}
	switch b[1] {
	case '\r', '\n':
		return one(keyNewline) // ⌥⏎ as Esc+⏎
	case 0x7f: // ⌥⌫ on terminals that send Esc DEL
		return one(keyDeleteWordBefore)
	case 'b', 'B':
		return one(keyWordLeft) // ⌥b and ^←
	case 'f', 'F':
		return one(keyWordRight) // ⌥f and ^→
	case 'd', 'D':
		return one(keyDeleteWordAfter) // ⌥d
	case 'O': // application-mode arrows
		if len(b) < 3 {
			return 0, nil, false
		}
		switch b[2] {
		case 'A':
			return 3, []key{{kind: keyPrevLine}}, true
		case 'B':
			return 3, []key{{kind: keyNextLine}}, true
		case 'C':
			return 3, []key{{kind: keyRight}}, true
		case 'D':
			return 3, []key{{kind: keyLeft}}, true
		}
		return 3, nil, true
	case '[':
		for j := 2; j < len(b); j++ {
			if b[j] >= 0x40 && b[j] <= 0x7e {
				seq := string(b[2 : j+1])
				var ks []key
				switch seq {
				case "200~":
					k.pasting = true
				case "201~":
					k.pasting = false
				case "13;3u", "13;2u": // ⌥⏎ and ⇧⏎ where the terminal reports them (CSI u)
					ks = []key{{kind: keyNewline}}
				case "A":
					ks = []key{{kind: keyPrevLine}}
				case "B":
					ks = []key{{kind: keyNextLine}}
				case "C":
					ks = []key{{kind: keyRight}}
				case "D":
					ks = []key{{kind: keyLeft}}
				case "H", "1~":
					ks = []key{{kind: keyHome}}
				case "F", "4~":
					ks = []key{{kind: keyEnd}}
				case "3~":
					ks = []key{{kind: keyDelete}}
				case "3;3~": // ⌥⌫ on terminals that send a modified Delete
					ks = []key{{kind: keyDeleteWordBefore}}
				case "1;3D", "1;5D": // ⌥← and ^←
					ks = []key{{kind: keyWordLeft}}
				case "1;3C", "1;5C": // ⌥→ and ^→
					ks = []key{{kind: keyWordRight}}
				}
				return j + 1, ks, true
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

// lineEditor is the prompt's text and the cursor in it. The cursor is a byte offset into
// text, always at a rune boundary, so an edit is two slices and a join.
type lineEditor struct {
	text   string
	cursor int
}

func (e *lineEditor) set(s string) {
	e.text, e.cursor = s, len(s)
}

func (e *lineEditor) insert(s string) {
	e.text = e.text[:e.cursor] + s + e.text[e.cursor:]
	e.cursor += len(s)
}

func (e *lineEditor) backspace() {
	if e.cursor == 0 {
		return
	}
	_, size := utf8.DecodeLastRuneInString(e.text[:e.cursor])
	e.cursor -= size
	e.text = e.text[:e.cursor] + e.text[e.cursor+size:]
}

func (e *lineEditor) del() {
	if e.cursor >= len(e.text) {
		return
	}
	_, size := utf8.DecodeRuneInString(e.text[e.cursor:])
	e.text = e.text[:e.cursor] + e.text[e.cursor+size:]
}

func (e *lineEditor) step(dir int) {
	if dir < 0 {
		if e.cursor > 0 {
			_, size := utf8.DecodeLastRuneInString(e.text[:e.cursor])
			e.cursor -= size
		}
		return
	}
	if e.cursor < len(e.text) {
		_, size := utf8.DecodeRuneInString(e.text[e.cursor:])
		e.cursor += size
	}
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// word is the byte offset at the far end of the word the cursor is in, or of the next one
// (backward for dir < 0). A word is a run of letters, digits and underscores.
func (e *lineEditor) word(dir int) int {
	i := e.cursor
	if dir < 0 {
		for i > 0 {
			r, size := utf8.DecodeLastRuneInString(e.text[:i])
			if isWordRune(r) {
				break
			}
			i -= size
		}
		for i > 0 {
			r, size := utf8.DecodeLastRuneInString(e.text[:i])
			if !isWordRune(r) {
				break
			}
			i -= size
		}
		return i
	}
	for i < len(e.text) {
		r, size := utf8.DecodeRuneInString(e.text[i:])
		if isWordRune(r) {
			break
		}
		i += size
	}
	for i < len(e.text) {
		r, size := utf8.DecodeRuneInString(e.text[i:])
		if !isWordRune(r) {
			break
		}
		i += size
	}
	return i
}

func (e *lineEditor) deleteWord(dir int) {
	at := e.word(dir)
	if dir < 0 {
		e.text = e.text[:at] + e.text[e.cursor:]
		e.cursor = at
	} else {
		e.text = e.text[:e.cursor] + e.text[at:]
	}
}

// line is the byte range of the prompt line the cursor is on.
func (e *lineEditor) line() (int, int) {
	start := strings.LastIndexByte(e.text[:e.cursor], '\n') + 1
	if i := strings.IndexByte(e.text[e.cursor:], '\n'); i >= 0 {
		return start, e.cursor + i
	}
	return start, len(e.text)
}

func (e *lineEditor) moveLine(dir int) {
	start, _ := e.line()
	col := visibleWidth(e.text[start:e.cursor])
	lineNo := strings.Count(e.text[:start], "\n")
	if want := lineNo + dir; want < 0 || want >= strings.Count(e.text, "\n")+1 {
		return
	} else {
		from := 0
		for i := 0; i < want; i++ {
			from += strings.IndexByte(e.text[from:], '\n') + 1
		}
		to := strings.IndexByte(e.text[from:], '\n')
		if to < 0 {
			to = len(e.text)
		} else {
			to += from
		}
		at := from
		for at < to && col > 0 {
			r, size := utf8.DecodeRuneInString(e.text[at:])
			w := cellWidth(r)
			if col < w {
				break
			}
			col -= w
			at += size
		}
		e.cursor = at
	}
}

// apply edits the prompt for the keys that do, and says whether k was one of them. The
// other keys (Enter, ^C, the background answer) are left to the caller.
func (e *lineEditor) apply(k key) bool {
	switch k.kind {
	case keyText:
		e.insert(k.text)
	case keyNewline:
		e.insert("\n")
	case keyBackspace:
		e.backspace()
	case keyDelete, keyEOF:
		e.del()
	case keyLeft:
		e.step(-1)
	case keyRight:
		e.step(1)
	case keyWordLeft:
		e.cursor = e.word(-1)
	case keyWordRight:
		e.cursor = e.word(1)
	case keyHome:
		start, _ := e.line()
		e.cursor = start
	case keyEnd:
		_, end := e.line()
		e.cursor = end
	case keyPrevLine:
		e.moveLine(-1)
	case keyNextLine:
		e.moveLine(1)
	case keyDeleteWordBefore:
		e.deleteWord(-1)
	case keyDeleteWordAfter:
		e.deleteWord(1)
	case keyKillToEnd:
		e.text = e.text[:e.cursor]
	case keyKillToStart:
		e.text = e.text[e.cursor:]
		e.cursor = 0
	default:
		return false
	}
	return true
}

// screen is the terminal: the lines that scroll up, and under them the part that is drawn
// again each time something changes: the agent's line not ended yet, the status (working,
// needs you) and the prompt. In raw mode a line ends with \r\n.
type screen struct {
	w               io.Writer
	raw             bool
	width           func() int
	drawn           int  // rows the live part took when it was last drawn, under the cursor's row
	down            int  // rows from the cursor back down to the last of those rows
	showing         bool // the live part is on the screen
	partial         string
	status          string
	settings        string
	prompt          bool
	prefix          string
	input           lineEditor
	completions     []completion
	completionFor   string
	completionShown bool
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
	if s.down > 0 {
		fmt.Fprintf(&b, "\x1b[%dB", s.down)
	}
	if s.drawn > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", s.drawn)
	}
	b.WriteString("\x1b[J")
	_, _ = io.WriteString(s.w, b.String())
	s.showing, s.drawn, s.down = false, 0, 0
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
		prefix := s.promptPrefix()
		continuation := strings.Repeat(" ", visibleWidth(prefix))
		lines := strings.Split(s.input.text, "\n")
		out = append(out, prefix+lines[0])
		for _, l := range lines[1:] {
			out = append(out, continuation+l)
		}
		if s.completionShown {
			for _, c := range s.completions {
				line := "  " + c.label
				if c.detail != "" {
					line += "  " + c.detail
				}
				out = append(out, line)
			}
		}
	}
	if s.settings != "" {
		out = append(out, s.settings)
	}
	return out
}

func (s *screen) promptPrefix() string {
	if s.prefix != "" {
		return s.prefix
	}
	return "› "
}

// cursorAt is the drawn line and cell column of the prompt's cursor. Every prompt line
// has the prompt's prefix.
func (s *screen) cursorAt(lines []string) (int, int) {
	base := 0
	if s.partial != "" {
		base++
	}
	if s.status != "" {
		base++
	}
	before := s.input.text[:s.input.cursor]
	off := before
	if i := strings.LastIndexByte(before, '\n'); i >= 0 {
		off = before[i+1:]
	}
	return base + strings.Count(before, "\n"), visibleWidth(s.promptPrefix()) + visibleWidth(off)
}

// draw puts the live part back, with the cursor at the prompt's editing point.
func (s *screen) draw() {
	lines := s.live()
	if len(lines) == 0 || !s.raw {
		return
	}
	cols := 0
	if s.width != nil {
		cols = s.width()
	}
	spans := make([]int, len(lines))
	total := 0
	for i, l := range lines {
		spans[i] = s.rows(l)
		total += spans[i]
	}
	_, _ = io.WriteString(s.w, strings.Join(lines, s.eol()))
	s.showing, s.drawn, s.down = true, total-1, 0
	if !s.prompt {
		return
	}
	line, col := s.cursorAt(lines)
	if line >= len(lines) {
		return
	}
	row := 0
	for _, n := range spans[:line] {
		row += n
	}
	if cols > 0 {
		row += col / cols
		col %= cols
	} else {
		row = line
	}
	if row > total-1 {
		// The editing point is at the end of a full row: the terminal holds the cursor at
		// that row's last cell until something is written, so start the row it goes to.
		_, _ = io.WriteString(s.w, s.eol())
		s.drawn = row
	} else if up := total - 1 - row; up > 0 {
		// After the lines the cursor is on the last row; the editing point is above it.
		fmt.Fprintf(s.w, "\x1b[%dA", up)
		s.down = up
	}
	_, _ = io.WriteString(s.w, "\r")
	if col > 0 {
		fmt.Fprintf(s.w, "\x1b[%dC", col)
	}
}

// print puts lines in the part that scrolls, above the live part.
func (s *screen) print(lines ...string) {
	if len(lines) == 0 {
		return
	}
	s.frame(func() {
		s.clear()
		_, _ = io.WriteString(s.w, strings.Join(lines, s.eol())+s.eol())
		s.draw()
	})
}

// update draws the live part again after a change to it.
func (s *screen) update() {
	s.frame(func() {
		s.clear()
		s.draw()
	})
}

// frame sends what paint writes in one write, with the cursor hidden while it moves, so
// a redraw (the spinner's, ten times a second) never shows the cursor away from the
// editing point (#6876).
func (s *screen) frame(paint func()) {
	if !s.raw {
		paint()
		return
	}
	w := s.w
	var b strings.Builder
	b.WriteString(hideCursor)
	s.w = &b
	paint()
	s.w = w
	b.WriteString(showCursor)
	_, _ = io.WriteString(w, b.String())
}

const (
	hideCursor = "\x1b[?25l"
	showCursor = "\x1b[?25h"
)

// end takes the live part off for good; a partial line goes in the lines.
func (s *screen) end() {
	s.clear()
	if s.partial != "" {
		_, _ = io.WriteString(s.w, s.partial+s.eol())
		s.partial = ""
	}
	s.prompt, s.status, s.settings = false, "", ""
}
