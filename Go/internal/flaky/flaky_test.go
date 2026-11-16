package flaky

import "testing"

func TestQuarantined(t *testing.T) {
	for _, c := range []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"a Mac with a clean environment runs it", map[string]string{}, false},
		{"CI skips it", map[string]string{"CI": "true"}, true},
		{"the quarantine skips it", map[string]string{"AGENTS_QUARANTINE": "1"}, true},
		{"the quarantine off runs it", map[string]string{"AGENTS_QUARANTINE": "0"}, false},
		{"the flaky run runs it in CI", map[string]string{"CI": "true", "AGENTS_RUN_FLAKY": "1"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Quarantined(func(k string) string { return c.env[k] }); got != c.want {
				t.Fatalf("Quarantined(%v) = %v, want %v", c.env, got, c.want)
			}
		})
	}
}
