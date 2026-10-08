package slug

import "testing"

func TestFromRoot(t *testing.T) {
	cases := []struct{ root, home, want string }{
		{"/Users/p/Projects/muxarr", "/Users/p", "Projects-muxarr"},
		{"/Users/p/.worktrees/muxarr", "/Users/p", ".worktrees-muxarr"},
		{"/srv/checkouts/muxarr", "/Users/p", "srv-checkouts-muxarr"},
		{"/Users/p/Projects/muxarr/", "/Users/p/", "Projects-muxarr"},
		{"/Users/pierce2/x", "/Users/p", "Users-pierce2-x"},
		{"/Users/p", "/Users/p", "Users-p"},
		{"/", "/Users/p", "root"},
	}
	for _, c := range cases {
		if got := FromRoot(c.root, c.home); got != c.want {
			t.Errorf("FromRoot(%q, %q) = %q, want %q", c.root, c.home, got, c.want)
		}
	}
}

func TestSpaceLabels(t *testing.T) {
	cases := []struct{ got, want string }{
		{IssueLabel(292, "Fix the login timeout!"), "Issue-292-fix-the-login-timeout"},
		{IssueLabel(7, "???"), "Issue-7"},
		{BeadLabel("social-chess-l37.2", "Add password reset"), "Bead-l37.2-add-password-reset"},
		{BeadLabel("bd4f2", ""), "Bead-bd4f2"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
