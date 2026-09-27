package kinds

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
)

// Watcher is the interface a kind="shell" action opts into with
// `watch = "<kind>"`; after the action's command succeeds, the named
// Watcher scans its captured output for remote job IDs and blocks until
// each one finishes.
type Watcher interface {
	// Extract pulls job/run IDs out of the action's captured stdout+stderr,
	// in first-seen order with duplicates removed.
	Extract(output string) []string
	// Watch blocks on each id in turn, logging through h.
	Watch(h *luahost.Host, ids []string)
}

// watchRegistry is a name -> Watcher map, seeded at init with the built-in
// kinds.
var watchRegistry = map[string]Watcher{}

// registerWatcher registers w under kind. Kept as a real function (rather
// than inlining the map write) so a future project-specific watcher has
// the same one-call registration seam via a package-level call before
// dispatch.
func registerWatcher(kind string, w Watcher) {
	watchRegistry[kind] = w
}

// runWatcher looks up kind in the registry, extracts ids from output, and
// watches each one. An unknown
// kind warns and returns rather than failing the action — the action's own
// command already succeeded by the time this runs, and a bad `watch` value
// in the manifest should not turn a successful run into a failed one.
func runWatcher(h *luahost.Host, kind, output string) {
	w, ok := watchRegistry[kind]
	if !ok {
		h.Warn(fmt.Sprintf("no watcher registered for kind '%s'", kind))
		return
	}
	ids := w.Extract(output)
	if len(ids) == 0 {
		return
	}
	w.Watch(h, ids)
}

// runURLRe matches against a GitHub Actions run URL such as
// https://github.com/<org>/<repo>/actions/runs/1234567890.
var runURLRe = regexp.MustCompile(`actions/runs/(\d+)`)

// githubActionWatcher extracts run IDs from actions/runs/<id> URLs, then
// blocks on `gh run watch <id>` for each.
type githubActionWatcher struct{}

// Extract pulls run IDs out of output, deduplicating while preserving
// first-seen order.
func (githubActionWatcher) Extract(output string) []string {
	matches := runURLRe.FindAllStringSubmatch(output, -1)
	seen := make(map[string]bool, len(matches))
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		id := m[1]
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

// Watch runs `gh run watch <id>` per id, in order, inheriting this
// process's stdout/stderr with no redirection.
func (githubActionWatcher) Watch(h *luahost.Host, ids []string) {
	for _, id := range ids {
		h.Info("tracking GitHub Action run " + id)
		cmd := exec.Command("gh", "run", "watch", id)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			h.Warn(fmt.Sprintf("gh run watch %s exited with error: %v", id, err))
		}
	}
}

func init() {
	registerWatcher("github-action", githubActionWatcher{})
}
