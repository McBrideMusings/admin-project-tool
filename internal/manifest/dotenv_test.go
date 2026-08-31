package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func writeDotenv(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadDotenvAppliesWellFormedLines(t *testing.T) {
	dir := writeDotenv(t, "# comment\n\nUNRAID_HOST=100.114.249.118\n  SPACED = value \nexport SKIPPED=nope\nno-equals\n1BAD=x\n")
	for _, k := range []string{"UNRAID_HOST", "SPACED", "SKIPPED", "1BAD"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	LoadDotenv(dir)

	if got := os.Getenv("UNRAID_HOST"); got != "100.114.249.118" {
		t.Errorf("UNRAID_HOST = %q, want 100.114.249.118", got)
	}
	if got := os.Getenv("SPACED"); got != "value" {
		t.Errorf("SPACED = %q, want value (key and value trimmed)", got)
	}
	if _, ok := os.LookupEnv("SKIPPED"); ok {
		t.Error("`export KEY=v` should be dropped — the key is not an identifier")
	}
	if _, ok := os.LookupEnv("1BAD"); ok {
		t.Error("a key starting with a digit should be dropped")
	}
}

func TestLoadDotenvDoesNotOverrideProcessEnv(t *testing.T) {
	dir := writeDotenv(t, "UNRAID_HOST=from-file\n")
	t.Setenv("UNRAID_HOST", "from-shell")
	LoadDotenv(dir)
	if got := os.Getenv("UNRAID_HOST"); got != "from-shell" {
		t.Errorf("UNRAID_HOST = %q, want from-shell — the process env wins", got)
	}
}

func TestLoadDotenvSkipsGitCryptAndMissingFile(t *testing.T) {
	dir := writeDotenv(t, "\x00GITCRYPT\x00\xff\xfeUNRAID_HOST=leaked\n")
	t.Setenv("UNRAID_HOST", "")
	os.Unsetenv("UNRAID_HOST")
	LoadDotenv(dir)
	if _, ok := os.LookupEnv("UNRAID_HOST"); ok {
		t.Error("a git-crypt-locked .env must be skipped entirely")
	}
	LoadDotenv(t.TempDir()) // no .env at all: must not panic
}

// The end-to-end shape the Go binary shipped broken: a shell `run` keeps its
// ${VAR} for the shell to expand, so the value has to reach the environment.
func TestDotenvFeedsResolveComputedPassthrough(t *testing.T) {
	dir := writeDotenv(t, "UNRAID_HOST=100.114.249.118\n")
	t.Setenv("UNRAID_HOST", "")
	os.Unsetenv("UNRAID_HOST")
	LoadDotenv(dir)

	run := "ssh ${UNRAID_USER:-root}@${UNRAID_HOST} true"
	if got := ResolveComputed(run); got != run {
		t.Errorf("ResolveComputed rewrote the shell body: %q", got)
	}
	got, err := ResolveEnv(run)
	if err != nil {
		t.Fatalf("ResolveEnv: %v", err)
	}
	if want := "ssh root@100.114.249.118 true"; got != want {
		t.Errorf("ResolveEnv = %q, want %q", got, want)
	}
}
