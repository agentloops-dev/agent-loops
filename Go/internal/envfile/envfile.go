package envfile

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/agentloops-dev/agent-loops/Go/internal/agentsd/redact"
)

// The files of variables (#5259): ~/.agents/env, the person's, and <folder>/.agents/env, the
// repo's, committed with it. Each is NAME=value, one per line. A # comment line just above a
// variable is its description; blank lines are allowed; `export ` and one pair of quotes
// around a value are accepted and taken off.
//
// Our own parser, not a library: joho/godotenv, the one most used in Go, has had no release
// in the last 12 months, and the format is the few rules above. godotenv's expansion of
// ${OTHER}, escapes in double quotes and values over several lines are not wanted here: a
// value is the text after = as it is written.

const (
	// FileMost is the largest file read; a larger one is not read at all, and the page says why.
	FileMost = 64 << 10
	// VariablesMost is the most variables a file may have.
	VariablesMost = 500
	// ValueMost is the longest value, in bytes.
	ValueMost = 4 << 10
)

// Name is a variable's name, as a shell takes it.
var Name = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Variable is one variable of a file, where it was found.
type Variable struct {
	Name, Value, Description string
	Line                     int // from 1
}

// File is a file read: its lines as they are, and the variables found in them.
type File struct {
	Lines     []string
	Variables []Variable
	Problem   string // why the file is not used: it is too big or has too many variables
}

// Parse reads a file of variables. A line it cannot read (no =, a bad name) is kept as
// it is and gives nothing. A later line with the same name wins, as in a shell.
func Parse(b []byte) File {
	if len(b) > FileMost {
		return File{Problem: fmt.Sprintf("The file is over %d KB, so it is not read.", FileMost>>10)}
	}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	var p File
	if text != "" {
		p.Lines = strings.Split(text, "\n")
	}
	comment := ""
	for i, raw := range p.Lines {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			comment = ""
			continue
		case strings.HasPrefix(line, "#"):
			comment = strings.TrimSpace(strings.TrimPrefix(line, "#"))
			continue
		}
		name, value, ok := splitLine(line)
		description := comment
		comment = ""
		if !ok {
			continue
		}
		p.Variables = append(p.Variables, Variable{Name: name, Value: value, Description: description, Line: i + 1})
	}
	if len(p.Variables) > VariablesMost {
		return File{Problem: fmt.Sprintf("The file has over %d variables, so it is not read.", VariablesMost)}
	}
	// The last of a name wins: take out the earlier ones.
	seen := map[string]bool{}
	var kept []Variable
	for i := len(p.Variables) - 1; i >= 0; i-- {
		if v := p.Variables[i]; !seen[v.Name] {
			seen[v.Name] = true
			kept = append([]Variable{v}, kept...)
		}
	}
	p.Variables = kept
	return p
}

// splitLine is NAME=value from one trimmed line, with `export ` and the quotes taken off.
func splitLine(line string) (string, string, bool) {
	line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	name, value, ok := strings.Cut(line, "=")
	name = strings.TrimSpace(name)
	if !ok || !Name.MatchString(name) {
		return "", "", false
	}
	value = strings.TrimSpace(value)
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		value = value[1 : len(value)-1]
	}
	return name, value, true
}

// lineFor is NAME=value as a file keeps it: quoted when the value has a space, a # or a quote
// at its ends, so the parser reads the same value back.
func lineFor(name, value string) string {
	if value != strings.TrimSpace(value) || strings.ContainsAny(value, "#\"'") || strings.Contains(value, " ") {
		if !strings.Contains(value, "'") {
			return name + "='" + value + "'"
		}
		return name + `="` + value + `"`
	}
	return name + "=" + value
}

// Edit sets (value non-nil) or removes (nil) one variable, keeping every other line. A
// new variable goes at the end, after a blank line, with its description above it.
// description nil keeps the one there; "" removes it. The caller refuses a file with a problem.
func Edit(b []byte, name string, value, description *string) []byte {
	p := Parse(b)
	lines := p.Lines
	// Every line that sets the name, and the comment just above each.
	var at []int
	for i, raw := range lines {
		if n, _, ok := splitLine(strings.TrimSpace(raw)); ok && n == name {
			at = append(at, i)
		}
	}
	commentAbove := func(i int) bool {
		return i > 0 && strings.HasPrefix(strings.TrimSpace(lines[i-1]), "#")
	}
	var out []string
	drop := map[int]bool{}
	replace := -1
	if len(at) > 0 {
		replace = at[len(at)-1]
	}
	blank := func(i int) bool { return i < 0 || i >= len(lines) || strings.TrimSpace(lines[i]) == "" }
	for _, i := range at {
		if i != replace || value == nil {
			drop[i] = true
			first := i
			if commentAbove(i) {
				drop[i-1] = true
				first = i - 1
			}
			// A block between two blank lines leaves one, not two.
			if blank(first-1) && i+1 < len(lines) && blank(i+1) {
				drop[i+1] = true
			}
		}
	}
	for i, raw := range lines {
		if drop[i] {
			continue
		}
		if i == replace && value != nil {
			hasComment := commentAbove(i)
			if description != nil {
				if hasComment {
					out = out[:len(out)-1]
				}
				if d := oneLine(*description); d != "" {
					out = append(out, "# "+d)
				}
			}
			out = append(out, lineFor(name, *value))
			continue
		}
		out = append(out, raw)
	}
	if replace < 0 && value != nil {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		if description != nil {
			if d := oneLine(*description); d != "" {
				out = append(out, "# "+d)
			}
		}
		out = append(out, lineFor(name, *value))
	}
	// No blank lines left at the end by a removal.
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return nil
	}
	var w bytes.Buffer
	w.WriteString(strings.Join(out, "\n"))
	w.WriteByte('\n')
	return w.Bytes()
}

func oneLine(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(s))
}

// SecretLookingName is a header or variable whose value is a secret by its name.
var SecretLookingName = regexp.MustCompile(`(?i)(token|secret|key|password|passwd|auth|bearer|cookie|credential)`)

// SecretWords is why a variable looks like a secret, or "": a name that says it is one
// (SecretLookingName) with a value, or a value that has a token's shape (redact's shapes).
func SecretWords(name, value string) string {
	if value == "" {
		return ""
	}
	if _, hit := redact.Bytes([]byte(value)); hit {
		return "The value of " + name + " looks like a token."
	}
	if SecretLookingName.MatchString(name) {
		return name + " looks like the name of a secret."
	}
	return ""
}
