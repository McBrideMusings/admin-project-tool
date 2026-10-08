package guards

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// dotenvIgnoredCheck fails when root/.env exists and git would commit it —
// the file holds the secrets manifest.LoadDotenv feeds to ${VAR}, so a .env
// git does not ignore is one `git add .` from publishing them. A file git
// tracks fails too: `git check-ignore` reports a tracked path as not ignored
// whatever .gitignore says.
//
// Two cases pass: a .env whose `filter` attribute is git-crypt (committed
// encrypted on purpose — git stores ciphertext), and any case git cannot
// answer (not a repository, git missing). Like deps-fresh this runs before
// every command, so a git error degrades to a pass rather than blocking.
func dotenvIgnoredCheck(root string) (bool, string) {
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return true, ""
		}
		root = wd
	}
	if _, err := os.Stat(filepath.Join(root, ".env")); err != nil {
		return true, ""
	}
	// Exit 0 means ignored, 1 means not ignored, anything else means git
	// could not answer.
	err := exec.Command("git", "-C", root, "check-ignore", "-q", ".env").Run()
	var exit *exec.ExitError
	if err == nil || !errors.As(err, &exit) || exit.ExitCode() != 1 {
		return true, ""
	}
	out, err := exec.Command("git", "-C", root, "check-attr", "filter", "--", ".env").Output()
	if err == nil && strings.TrimSpace(string(out)) == ".env: filter: git-crypt" {
		return true, ""
	}
	return false, "dotenv-ignored: " + filepath.Join(root, ".env") +
		" holds this project's secrets, but git does not ignore it (it is tracked, or no ignore rule matches it) — " +
		"add `.env` to .gitignore and `git rm --cached .env` if it is tracked, or encrypt it with git-crypt."
}
