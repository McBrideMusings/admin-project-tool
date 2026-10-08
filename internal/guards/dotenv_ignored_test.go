package guards

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// dotenvRepo makes a git repo holding a .env, with the developer's global
// and system git config and ignore file shut out — on a machine whose global ignore
// lists .env, every case would otherwise read as ignored.
func dotenvRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "none"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	mustWrite(t, filepath.Join(dir, ".env"), "SECRET=x\n")
	return dir
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestDotenvIgnoredCheck(t *testing.T) {
	t.Run("no .env passes", func(t *testing.T) {
		if ok, msg := dotenvIgnoredCheck(t.TempDir()); !ok {
			t.Errorf("failed with %q", msg)
		}
	})
	t.Run("not a repository passes", func(t *testing.T) {
		dir := t.TempDir()
		mustWrite(t, filepath.Join(dir, ".env"), "SECRET=x\n")
		if ok, msg := dotenvIgnoredCheck(dir); !ok {
			t.Errorf("failed with %q", msg)
		}
	})
	t.Run("unignored .env fails", func(t *testing.T) {
		if ok, _ := dotenvIgnoredCheck(dotenvRepo(t)); ok {
			t.Error("passed; .env is not ignored")
		}
	})
	t.Run("ignored .env passes", func(t *testing.T) {
		dir := dotenvRepo(t)
		mustWrite(t, filepath.Join(dir, ".gitignore"), ".env\n")
		if ok, msg := dotenvIgnoredCheck(dir); !ok {
			t.Errorf("failed with %q", msg)
		}
	})
	t.Run("tracked .env fails even when a rule matches", func(t *testing.T) {
		dir := dotenvRepo(t)
		git(t, dir, "add", ".env")
		git(t, dir, "commit", "-qm", "leak")
		mustWrite(t, filepath.Join(dir, ".gitignore"), ".env\n")
		if ok, _ := dotenvIgnoredCheck(dir); ok {
			t.Error("passed; .env is tracked")
		}
	})
	t.Run("git-crypt .env passes", func(t *testing.T) {
		dir := dotenvRepo(t)
		mustWrite(t, filepath.Join(dir, ".gitattributes"), ".env filter=git-crypt diff=git-crypt\n")
		if ok, msg := dotenvIgnoredCheck(dir); !ok {
			t.Errorf("failed with %q", msg)
		}
	})
}
