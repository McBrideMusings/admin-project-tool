package manifest

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// envKeyRe is the shape a `.env` key must have to be applied: a shell
// identifier. A line like `export FOO=bar` fails it (the key parses as
// "export FOO") and is dropped.
var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// gitCryptMagic prefixes a git-crypt-encrypted file. Nothing parseable
// follows it when the repo is locked.
var gitCryptMagic = []byte("\x00GITCRYPT\x00")

// LoadDotenv reads <root>/.env into the process environment — the reason a
// manifest can write `ssh ${UNRAID_USER:-root}@${UNRAID_HOST}` at all.
//
// Without it a shell action's `run` string reaches /bin/sh with ${UNRAID_HOST}
// still unexpanded (ResolveComputed deliberately leaves non-computed refs for
// the shell), the shell finds no such variable, and the command runs against
// an empty string: `ssh root@` → "Could not resolve hostname". Nothing errors,
// which is why the Go binary shipped without this and looked like it worked.
//
// Existing process environment always wins, so an exported value overrides
// the file and calling this twice is a no-op.
// A locked or binary .env never fails boot: the git-crypt header is detected
// and the file skipped, undecodable bytes are dropped, and only well-formed
// KEY=value lines are applied. Errors are swallowed for the same reason —
// a missing or unreadable .env is the normal case, not a failure.
func LoadDotenv(root string) {
	raw, err := os.ReadFile(filepath.Join(root, ".env"))
	if err != nil || bytes.HasPrefix(raw, gitCryptMagic) {
		return
	}
	for line := range strings.SplitSeq(strings.ToValidUTF8(string(raw), ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if !envKeyRe.MatchString(key) {
			continue
		}
		if _, present := os.LookupEnv(key); present {
			continue
		}
		os.Setenv(key, strings.TrimSpace(val))
	}
}
