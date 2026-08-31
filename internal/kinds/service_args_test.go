package kinds

import "testing"

// TestServiceVerbSplit pins the argument split kind="launchd" does before it
// decides local-vs-remote. The bug this replaces matched a host by SHAPE —
// anything containing "@" or "." — so `admin deploy server unraid`, an
// ordinary tailnet short name, was read as a verb and died with
// "unknown service verb: unraid". A hostname is whatever is left over after
// the verbs and flags are taken, not a word that looks like one.
func TestServiceVerbSplit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		verb, host string
	}{
		{"bare", nil, "", ""},
		{"verb only", []string{"status"}, "status", ""},
		{"bare short hostname", []string{"unraid"}, "", "unraid"},
		{"dotted hostname", []string{"box.tail1234.ts.net"}, "", "box.tail1234.ts.net"},
		{"user at host", []string{"pierce@unraid"}, "", "pierce@unraid"},
		{"verb and host", []string{"install", "unraid"}, "install", "unraid"},
		{"host then uninstall", []string{"unraid", "--uninstall"}, "uninstall", "unraid"},
		{"local uninstall", []string{"--uninstall"}, "uninstall", ""},
		{"unknown flag ignored", []string{"--clean", "unraid"}, "", "unraid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verb, host := splitServiceArgs(tc.args)
			if verb != tc.verb || host != tc.host {
				t.Errorf("split(%v) = (%q, %q), want (%q, %q)", tc.args, verb, host, tc.verb, tc.host)
			}
		})
	}
}

func TestIsServiceVerb(t *testing.T) {
	for _, v := range serviceVerbs {
		if !isServiceVerb(v) {
			t.Errorf("%q should be a verb", v)
		}
	}
	for _, notVerb := range []string{"", "unraid", "deploy", "Install", "installs"} {
		if isServiceVerb(notVerb) {
			t.Errorf("%q should not be a verb", notVerb)
		}
	}
}
