package agentsd

import "testing"

func TestParseProcStat(t *testing.T) {
	for _, tc := range []struct {
		stat string
		want procInfo
		ok   bool
	}{
		{"42 (sleep) S 7 42 42 0 -1", procInfo{parent: 7, name: "sleep"}, true},
		{"43 (a b) (c)) R 1 43 43", procInfo{parent: 1, name: "a b) (c)"}, true},
		{"44 (x) S", procInfo{}, false},
		{"garbage", procInfo{}, false},
	} {
		got, ok := parseProcStat(tc.stat)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseProcStat(%q) = %+v, %v; want %+v, %v", tc.stat, got, ok, tc.want, tc.ok)
		}
	}
}
