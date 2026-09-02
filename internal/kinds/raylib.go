package kinds

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// raylibModes is the Go port of admin_lib/interp.py's _RAYLIB_MODES.
var raylibModes = map[string]bool{"build": true, "run": true, "smoke": true, "clean": true}

// makeRaylib is kind="raylib"'s factory — the Go port of
// admin_lib/interp.py::_make_raylib. `run` names the mode
// (build|run|smoke|clean); the seven raylib games this replaced shared a
// CMake configure/build/run/smoke/clean skeleton and differed only in
// binary name, cmake flags, job count, the smoke env var, and which dirs
// clean removes — all of which are [raylib] config, read once here.
func makeRaylib(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	mode, _ := actionString(action, "run")
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "build"
	}
	if !raylibModes[mode] {
		return nil, fmt.Errorf(
			"action %q: kind=\"raylib\" run must be one of [build run smoke clean], got %q",
			name, mode,
		)
	}
	cfg := archetypeConfigTable(m, "raylib")

	return func(args []string) error {
		h := newHost(m)
		buildDir := cfgString(cfg, "build_dir", "build")
		if buildDir == "" {
			buildDir = "build"
		}
		binary := cfgString(cfg, "binary", "")
		flags := cfgStrings(cfg, "cmake_flags")
		smokeEnv := cfgString(cfg, "smoke_env", "RF_SMOKE")
		if smokeEnv == "" {
			smokeEnv = "RF_SMOKE"
		}
		cleanDirs := cfgStrings(cfg, "clean_dirs")
		if len(cleanDirs) == 0 {
			cleanDirs = []string{buildDir}
		}

		if (mode == "run" || mode == "smoke") && binary == "" {
			msg := name + ": [raylib].binary is not set — nothing to run"
			h.Err(msg)
			return fmt.Errorf("action %q: %s", name, msg)
		}
		exe := "./" + strings.Trim(buildDir, "/") + "/" + binary

		if mode == "clean" {
			// Done in Go, not `rm -rf`: clean_dirs entries may be globs, and
			// shell-quoting a glob stops it expanding while not quoting it
			// breaks paths with spaces — matches Python's own choice to do
			// this with glob.glob + shutil.rmtree instead of a shell command.
			for _, pattern := range cleanDirs {
				matches, _ := filepath.Glob(pattern)
				sort.Strings(matches)
				for _, path := range matches {
					h.Info("removing " + path)
					info, statErr := os.Lstat(path)
					if statErr != nil {
						continue
					}
					if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
						if err := os.RemoveAll(path); err != nil {
							msg := "could not remove " + path + ": " + err.Error()
							h.Err(msg)
							return fmt.Errorf("action %q: %s", name, msg)
						}
					} else if err := os.Remove(path); err != nil {
						msg := "could not remove " + path + ": " + err.Error()
						h.Err(msg)
						return fmt.Errorf("action %q: %s", name, msg)
					}
				}
			}
			h.Ok(name + " complete")
			return nil
		}

		var run string
		switch mode {
		case "build":
			configure := []string{"cmake", "-S", ".", "-B", shellQuote(buildDir)}
			for _, f := range flags {
				configure = append(configure, shellQuote(f))
			}
			build := []string{"cmake", "--build", shellQuote(buildDir)}
			switch j := cfg["jobs"].(type) {
			case string:
				if j == "auto" {
					build = append(build, "-j")
				}
			case int64:
				if j != 0 {
					build = append(build, fmt.Sprintf("-j%d", j))
				}
			case int:
				if j != 0 {
					build = append(build, fmt.Sprintf("-j%d", j))
				}
			}
			run = strings.Join(configure, " ") + " && " + strings.Join(build, " ")
		case "run":
			run = shellQuote(exe)
		default: // smoke
			run = smokeEnv + "=1 " + shellQuote(exe)
		}

		res, err := h.RunCmd(run, luahost.RunCmdOptions{CaptureLog: true})
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		if !res.Ok {
			h.Err(run + " failed")
			return fmt.Errorf("action %q: %s failed", name, run)
		}
		h.Ok(name + " complete")
		return nil
	}, nil
}
