package providers

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Redaction is a provider's secrets, taken out of every text that leaves the machine
// that carries it, and the log; each value becomes
// ${NAME}. A value of 6 characters or more is replaced wherever it is; a shorter one only
// as a whole word. Each is also replaced in its URL-encoded and base64 forms.
type Redaction struct{ forms []redactionForm }

type redactionForm struct {
	text      string
	name      string
	wholeWord bool
}

// NewRedaction is a Redaction of secrets.
func NewRedaction(secrets map[string]string) Redaction {
	var forms []redactionForm
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := secrets[name]
		if value == "" {
			continue
		}
		short := utf8.RuneCountInString(value) < 6
		seen := map[string]bool{}
		for _, text := range []string{value, percentEncoded(value), base64.StdEncoding.EncodeToString([]byte(value))} {
			if !seen[text] {
				seen[text] = true
				forms = append(forms, redactionForm{text, name, short})
			}
		}
	}
	// The longest first, so a value that holds another is replaced whole.
	sort.SliceStable(forms, func(i, j int) bool {
		return utf8.RuneCountInString(forms[i].text) > utf8.RuneCountInString(forms[j].text)
	})
	return Redaction{forms}
}

// percentEncoded is addingPercentEncoding(withAllowedCharacters:) with letters, digits and
// -._~ left as they are.
func percentEncoded(value string) string {
	var out strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-._~", r) {
			out.WriteRune(r)
			continue
		}
		for _, b := range []byte(string(r)) {
			fmt.Fprintf(&out, "%%%02X", b)
		}
	}
	return out.String()
}

// Text is the redaction of text.
func (r Redaction) Text(text string) string {
	for _, f := range r.forms {
		if !strings.Contains(text, f.text) {
			continue
		}
		if !f.wholeWord {
			text = strings.ReplaceAll(text, f.text, "${"+f.name+"}")
			continue
		}
		text = replaceWholeWord(text, f.text, "${"+f.name+"}")
	}
	return text
}

func wordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// replaceWholeWord replaces find where no letter, digit or _ of A-Z touches it, left to
// right, as the regular expression (?<![A-Za-z0-9_])find(?![A-Za-z0-9_]) does.
func replaceWholeWord(text, find, with string) string {
	var out strings.Builder
	i := 0
	for i <= len(text) {
		at := strings.Index(text[i:], find)
		if at < 0 {
			break
		}
		at += i
		end := at + len(find)
		if (at == 0 || !wordByte(text[at-1])) && (end == len(text) || !wordByte(text[end])) {
			out.WriteString(text[i:at])
			out.WriteString(with)
			i = end
			continue
		}
		out.WriteString(text[i : at+1])
		i = at + 1
	}
	out.WriteString(text[min(i, len(text)):])
	return out.String()
}

// Value is the redaction of every string in a JSON value, keys and values.
func (r Redaction) Value(v any) any {
	switch t := v.(type) {
	case string:
		return r.Text(t)
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = r.Value(x)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			key := r.Text(k)
			if _, taken := out[key]; !taken {
				out[key] = r.Value(x)
			}
		}
		return out
	}
	return v
}
