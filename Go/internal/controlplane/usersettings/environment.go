package usersettings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/agentloops-dev/agent-loops/Go/internal/envfile"
	"github.com/agentloops-dev/agent-loops/Go/wire"
)

// The environment part (#5259): ~/.agents/env, whole, as text, written to each of the person's
// machines. It is kept in clear, as the accounts are, so a value that looks like a secret is
// refused, with words that name the variable and never its value.

// checkEnvironment is the part, refused when it is not a UserEnvironment, its file would not be
// read (too big, too many variables), or a value looks like a secret.
func checkEnvironment(value json.RawMessage) error {
	var part wire.UserEnvironment
	d := json.NewDecoder(bytes.NewReader(value))
	d.DisallowUnknownFields()
	if err := d.Decode(&part); err != nil {
		return fmt.Errorf("%w: The environment is not a UserEnvironment: %v", ErrInvalid, err)
	}
	f := envfile.Parse([]byte(part.Text))
	if f.Problem != "" {
		return fmt.Errorf("%w: %s", ErrInvalid, f.Problem)
	}
	for _, v := range f.Variables {
		if len(v.Value) > envfile.ValueMost {
			return fmt.Errorf("%w: The value of %s is over 4 KB.", ErrInvalid, v.Name)
		}
		if why := envfile.SecretWords(v.Name, v.Value); why != "" {
			return fmt.Errorf("%w: %s Your variables are kept in clear: keep the value in Settings ▸ You ▸ Secrets instead.", ErrInvalid, why)
		}
	}
	return nil
}

// EnvironmentChanges is the names whose value or description differ between two environment
// parts, sorted: for the audit, which keeps each name and never a value.
func EnvironmentChanges(before, after json.RawMessage) []string {
	read := func(raw json.RawMessage) map[string]envfile.Variable {
		out := map[string]envfile.Variable{}
		var part wire.UserEnvironment
		if len(raw) == 0 || json.Unmarshal(raw, &part) != nil {
			return out
		}
		for _, v := range envfile.Parse([]byte(part.Text)).Variables {
			v.Line = 0
			out[v.Name] = v
		}
		return out
	}
	a, b := read(before), read(after)
	var names []string
	for n, v := range a {
		if w, ok := b[n]; !ok || w != v {
			names = append(names, n)
		}
	}
	for n := range b {
		if _, ok := a[n]; !ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}
