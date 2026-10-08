package kinds

import (
	"fmt"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// makeApple is kind="apple"'s factory. `run` names one of the Apple verb
// functions runAppleVerb dispatches on (build_mac, dev_ios, deploy_tv,
// clean_ios, ...); the top-level `[apple]` manifest table (m.Settings["apple"]) is the
// config every verb reads. The actual verb bodies live in
// apple_darwin.go (real implementation, darwin-only — xcodebuild/simctl/
// devicectl don't exist anywhere else) and apple_other.go (a clear
// "macOS only" error everywhere else), following the same build-tag split
// internal/luahost's Apple surface already uses.
func makeApple(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	verb, ok := actionString(action, "run")
	if !ok || verb == "" {
		return nil, fmt.Errorf(`action %q: kind="apple" requires 'run'`, name)
	}
	cfg := archetypeConfigTable(m, "apple")
	return func(args []string) error {
		if len(cfg) == 0 {
			return fmt.Errorf("action %q: [apple] table missing from admin.toml", name)
		}
		return runAppleVerb(verb, name, cfg, args, m)
	}, nil
}

// appleFlags is the small set of long flags an "apple" kind action's
// leftover CLI args may carry: --log LEVEL, --clean, --install, and --args.
// Every field has a documented default when the flag is absent.
type appleFlags struct {
	logLevel   string
	clean      bool
	install    bool
	launchArgs []string
}

// parseAppleFlags reads --log LEVEL, --clean, --install, and --args
// (everything after --args is forwarded verbatim as launch arguments) out
// of a kind="apple" action's args. Unrecognised tokens are ignored rather
// than rejected — this mirrors the leniency every other kind factory in
// this package already has toward its own args, and keeps a future flag
// addition from being a breaking change for an existing manifest.
func parseAppleFlags(args []string) appleFlags {
	f := appleFlags{logLevel: "app"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--log":
			if i+1 < len(args) {
				i++
				f.logLevel = args[i]
			}
		case "--clean":
			f.clean = true
		case "--install":
			f.install = true
		case "--args":
			f.launchArgs = append(f.launchArgs, args[i+1:]...)
			i = len(args)
		}
	}
	return f
}

// appleLaunchArgs is what a dev loop launches the app with: --args when the
// command line gave any, which the invoking shell already expanded, else the
// [apple] table's launch_args with each ${VAR} expanded (envResolver) — they
// become argv, so no shell will expand them later, and an unset reference is
// an error rather than a literal "${WORKTREE_SLUG}" reaching the app.
func appleLaunchArgs(cfg map[string]any, f appleFlags) ([]string, error) {
	if len(f.launchArgs) > 0 {
		return f.launchArgs, nil
	}
	var r envResolver
	out := r.all(cfgStrings(cfg, "launch_args"))
	if r.firstErr != nil {
		return nil, fmt.Errorf("apple.launch_args: %w", r.firstErr)
	}
	return out, nil
}
