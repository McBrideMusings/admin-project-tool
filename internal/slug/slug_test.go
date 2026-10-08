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
