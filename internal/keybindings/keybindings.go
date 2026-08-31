// Package keybindings is the Go port of admin_lib/keybindings.py: generic
// interactive keybindings for kind="interactive-shell" and
// kind="bridge-listen". It runs a long-lived subprocess like
// internal/reload.RunWithReload does, but instead of restarting the wrapped
// process on a keypress, it side-fires a user-declared binding while the
// wrapped process keeps running.
//
// Three binding shapes per key (exactly one of Run/Action/Signal set —
// internal/validate.ParseKeys enforces that before this package ever sees a
// binding):
//
//	{run = "<shell snippet>"}                — fire and forget in a subshell
//	{action = "<action-name>"}                — invoke a registered action
//	{signal = "TERM", pid_file = "/path"}     — read a PID, send a signal
//
// This package also owns starting/stopping the log bridge
// (internal/logbridge) for the duration of the wrapped process and wiring
// its output through the shared pump (internal/streams.OutputPump) and
// footer (internal/frame) — mirroring admin_lib/keybindings.py's own import
// of log_bridge/log_server, and the CLAUDE.md "Log bridge" section's
// requirement that a browser line and a server line reach the terminal
// indistinguishably.
package keybindings

import (
	"io"
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/logbridge"
	"github.com/McBrideMusings/admin-project-tool/internal/validate"
)

// Options configures Run. Every callback field is optional; a nil one is
// simply not called.
type Options struct {
	// Label names the action for the keybinding banner and the log-bridge
	// info line ("[docker-dev] ...").
	Label string

	// Formatter, when set, is applied to each complete line of the wrapped
	// process's own output before it reaches Stdout — internal/streams.
	// OutputPump's own contract. It is ALSO what decides whether log-bridge
	// lines get formatted at all (see installBridgeSink in the platform
	// file): a nil Formatter here means bridge lines are written raw,
	// matching admin_lib/keybindings.py::_install_bridge_sink's own check.
	Formatter func(lineWithNewline string) string

	// Stdout is where the wrapped process's output (through Formatter/the
	// pump) and log-bridge lines are written. Defaults to os.Stdout when
	// nil.
	Stdout io.Writer

	// LogFn, when set, receives the wrapped process's raw output for
	// appending to the active log file.
	LogFn func(text string)

	// LogPath is the log file log-bridge entries are appended to — normally
	// the same file LogFn appends the wrapped process's own output to, so
	// `admin logs` replays one interleaved stream.
	LogPath string

	// LogBridge configures the log-bridge listener. Nil (or an unConfig)
	// disables it entirely — no listener starts, matching a project with no
	// [log_bridge] table declared.
	LogBridge *logbridge.Config

	// RunAction dispatches a key binding's `action = "name"` target. The
	// caller (internal/kinds) supplies this rather than this package
	// resolving actions itself, so keybindings stays free of a
	// *manifest.Manifest/*kinds.Registry dependency — the same reason
	// internal/kinds.RunAction takes callbacks rather than this package
	// reaching back into internal/kinds (which would cycle: kinds already
	// imports this package).
	RunAction func(name string) error

	Info func(msg string)
	Warn func(msg string)
	Err  func(msg string)
}

// formatBanner renders the keybinding banner run_with_keybindings prints
// once at startup — the Go port of admin_lib/keybindings.py::_format_banner.
// Sorted by keychar so the banner (and any test asserting on it) is
// deterministic — Python iterates a dict in insertion order, which TOML
// table decoding does not guarantee to preserve in Go's map[string]any, so
// this is a deliberate, harmless divergence rather than a port of insertion
// order that doesn't exist on this side.
func formatBanner(keys map[string]validate.KeyBinding, label string) string {
	chars := make([]string, 0, len(keys))
	for ch := range keys {
		chars = append(chars, ch)
	}
	sort.Strings(chars)

	parts := make([]string, 0, len(chars)+1)
	for _, ch := range chars {
		b := keys[ch]
		lbl := b.Label
		if lbl == "" {
			lbl = b.Action
		}
		if lbl == "" {
			lbl = b.Signal
		}
		if lbl == "" {
			lbl = "run"
		}
		parts = append(parts, "["+ch+"] "+lbl)
	}
	parts = append(parts, "[Ctrl-C] exit")
	prefix := ""
	if label != "" {
		prefix = "[" + label + "] "
	}
	return prefix + strings.Join(parts, " · ")
}
