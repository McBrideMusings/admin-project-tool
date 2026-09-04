package kinds

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
)

// Watcher is the Go port of admin_lib/watch.py::Watcher — a kind="shell"
// action opts in with `watch = "<kind>"`, and after the action's command
// succeeds, the named Watcher scans its captured output for remote job IDs
// and blocks until each one finishes.
type Watcher interface {
	// Extract pulls job/run IDs out of the action's captured stdout+stderr,
	// in first-seen order with duplicates removed.
	Extract(output string) []string
	// Watch blocks on each id in turn, logging through h.
	Watch(h *luahost.Host, ids []string)
}

// watchRegistry is the Go port of admin_lib/watch.py's module-level
// _REGISTRY: a name -> Watcher map, seeded at init with the built-in kinds.
var watchRegistry = map[string]Watcher{}

// registerWatcher is the Go port of admin_lib/watch.py::register_watcher.
// Kept as a real function (rather than inlining the map write) so a future
// project-specific watcher has the same one-call registration seam Python
// has via a package-level call before dispatch.
func registerWatcher(kind string, w Watcher) {
	watchRegistry[kind] = w
}

// runWatcher is the Go port of admin_lib/watch.py::run_watcher: look up kind
// in the registry, extract ids from output, and watch each one. An unknown
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

// runURLRe is the Go port of admin_lib/watch.py::_GithubActionWatcher's
// _RUN_URL_RE — matched against a GitHub Actions run URL such as
// https://github.com/<org>/<repo>/actions/runs/1234567890.
var runURLRe = regexp.MustCompile(`actions/runs/(\d+)`)

// githubActionWatcher is the Go port of
// admin_lib/watch.py::_GithubActionWatcher: extract run IDs from
// actions/runs/<id> URLs, then block on `gh run watch <id>` for each.
type githubActionWatcher struct{}

// Extract is the Go port of _GithubActionWatcher.extract, including Python's
// dict.fromkeys dedup-preserving-order behavior.
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

// Watch is the Go port of _GithubActionWatcher.watch: `gh run watch <id>`
// per id, in order, inheriting this process's stdout/stderr the same way
// Python's subprocess.call does with no stdout/stderr redirection.
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
