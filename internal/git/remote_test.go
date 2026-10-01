package git

import "testing"

func TestParseRemoteHost(t *testing.T) {
	cases := []struct {
		raw  string
		host string
		ssh  bool
	}{
		{"Tower:/mnt/user/storage/git/wow-private.git", "Tower", true},
		{"git@git.example.com:me/repo.git", "git.example.com", true},
		{"ssh://git@10.0.0.5:2222/srv/repo.git", "10.0.0.5", true},
		{"https://git.example.com/me/repo.git", "git.example.com", false},
		{"/srv/git/repo.git", "", false},
		{"./relative/repo.git", "", false},
		{"file:///srv/git/repo.git", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		host, ssh := ParseRemoteHost(c.raw)
		if host != c.host || ssh != c.ssh {
			t.Errorf("ParseRemoteHost(%q) = %q, %v; want %q, %v", c.raw, host, ssh, c.host, c.ssh)
		}
	}
}

func TestParseSSHHostname(t *testing.T) {
	out := "user root\nhostname 100.64.0.10\nport 22\n"
	if got := parseSSHHostname(out, "Tower"); got != "100.64.0.10" {
		t.Errorf("alias: got %q", got)
	}
	if got := parseSSHHostname("hostname tower\n", "Tower"); got != "" {
		t.Errorf("same name: got %q, want empty", got)
	}
	if got := parseSSHHostname("user root\n", "Tower"); got != "" {
		t.Errorf("no hostname line: got %q, want empty", got)
	}
}
