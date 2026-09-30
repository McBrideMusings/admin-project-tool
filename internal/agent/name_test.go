package agent

import "testing"

func TestSanitizeName(t *testing.T) {
	long := "admin-3rgp-79-fix-login-timeout-a"
	cases := []struct {
		raw, fallback, prefix string
		max                   int
		want                  string
	}{
		{"Pane 1/ab", "x", "w-", 0, "pane-1-ab"},
		{"", "agent", "a-", 32, "agent"},
		{"%%%", "workflow", "w-", 0, "workflow"},
		{"7", "x", "w-", 0, "w-7"},
		{long, "x", "w-", 32, "admin-3rgp-79-fix-login-timeout-"},
		{long, "x", "w-", 0, long},
	}
	for _, c := range cases {
		if got := SanitizeName(c.raw, c.fallback, c.prefix, c.max); got != c.want {
			t.Errorf("SanitizeName(%q, max %d) = %q, want %q", c.raw, c.max, got, c.want)
		}
	}
}
