package policy

import "testing"

func TestStrictestPicksTheRiskiestDecision(t *testing.T) {
	d := func(a Action, rule string) Decision { return Decision{Action: a, Rule: rule} }
	for name, tc := range map[string]struct {
		in   []Decision
		want int
	}{
		"no decisions":             {nil, -1},
		"one":                      {[]Decision{d(Allow, "a")}, 0},
		"all allow takes first":    {[]Decision{d(Allow, "a"), d(Allow, "b"), d(Allow, "c")}, 0},
		"approval beats allow":     {[]Decision{d(Allow, "a"), d(Approval, "b"), d(Allow, "c")}, 1},
		"deny beats approval":      {[]Decision{d(Approval, "a"), d(Deny, "b"), d(Allow, "c")}, 1},
		"hide beats deny":          {[]Decision{d(Deny, "a"), d(Hide, "b"), d(Approval, "c")}, 1},
		"hide last still wins":     {[]Decision{d(Allow, "a"), d(Approval, "b"), d(Deny, "c"), d(Hide, "d")}, 3},
		"tie takes the first":      {[]Decision{d(Allow, "a"), d(Approval, "b"), d(Approval, "c")}, 1},
		"tie of hide takes first":  {[]Decision{d(Hide, "a"), d(Hide, "b")}, 0},
		"a write after reads wins": {[]Decision{d(Allow, "r1"), d(Allow, "r2"), d(Approval, "w")}, 2},
	} {
		if got := Strictest(tc.in); got != tc.want {
			t.Errorf("%s: Strictest = %d, want %d", name, got, tc.want)
		}
	}
}
