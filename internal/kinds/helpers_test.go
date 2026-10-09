package kinds

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// TestShellQuoteRoundTrips runs shellQuote's output through sh, bash and zsh
// and checks every word comes back as typed:
// "#12" must not comment out the rest of the line, "=ls" must not expand to
// a path under zsh.
func TestShellQuoteRoundTrips(t *testing.T) {
	words := []string{"see", "#12", "a#b", "=ls", "x=1", "it's", "", "~", "*", "^x", "héllo", "a b", "$HOME", "-n", "/tmp/a.b"}
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = shellQuote(w)
	}
	line := `for a in ` + strings.Join(quoted, " ") + `; do printf '%s\n' "<$a>"; done`
	want := make([]string, len(words))
	for i, w := range words {
		want[i] = "<" + w + ">"
	}
	for _, sh := range []string{"sh", "bash", "zsh"} {
		if _, err := exec.LookPath(sh); err != nil {
			t.Logf("%s not installed, skipping it", sh)
			continue
		}
		out, err := exec.Command(sh, "-c", line).Output()
		if err != nil {
			t.Fatalf("%s -c %q: %v", sh, line, err)
		}
		if got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s got %q, want %q", sh, got, want)
		}
	}
}

// A word made only of safe bytes stays bare, so generated command lines keep
// reading like what a person would type.
func TestShellQuoteLeavesPlainWordsBare(t *testing.T) {
	for _, w := range []string{"claude", "build/out", "--target=web", "a,b:c@d%e+f"} {
		if got := shellQuote(w); got != w {
			t.Errorf("shellQuote(%q) = %q, want it bare", w, got)
		}
	}
}
