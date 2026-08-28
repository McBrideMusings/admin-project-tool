// Package archetypeparity proves the two archetype loaders agree.
//
// admin-3rgp.4 added a directory-format twin (archetype.toml [+ scripts/])
// for each of the fourteen archetypes/<name>.py Python modules, without
// deleting the Python originals (admin-3rgp.30 does that later). This
// package is the checked-in equivalence check the ticket asks for: for each
// of the fourteen names, resolve archetypes/<name>.py through the Python
// loader (admin_lib.archetypes.resolve, via tools/archetype_parity.py) and
// archetypes/<name>/archetype.toml through the Go loader
// (internal/archetype.Resolve), then diff the projected command/action/url
// sets.
//
// Deliberately NOT inside internal/archetype: the ticket that produced this
// package forbids modifying that package, and this only ever reads it.
//
// Go's archetype.Resolve computes no `modules` set and applies none of
// Python's resolve-time validation (steps→action existence, guard
// `requires`, url refs, @-ref targets, multi-target target flattening) — see
// internal/archetype/merge.go and archetype.go. The comparison here is
// therefore scoped to exactly what both sides produce: commands, actions,
// and urls. None of the fourteen archetypes use a multi-target action, so
// every action's `targets` is asserted empty on both sides rather than
// independently computed.
package archetypeparity

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// archetypeNames is every archetype admin-3rgp.4 converted, matching the
// fourteen archetypes/<name>.py modules this package diffs against.
var archetypeNames = []string{
	"simple",
	"docs",
	"firefox-extension",
	"rust",
	"rust-tauri",
	"go-cli",
	"hugo",
	"raylib-game",
	"cloudflare-workers",
	"unraid-plugin",
	"apple",
	"forge-engine",
	"react-native",
	"docker",
}

// repoRoot resolves the admin-project-tool checkout root from this test
// file's own path, so the test works regardless of the caller's cwd.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: could not resolve this file's path")
	}
	// internal/archetypeparity/parity_test.go -> repo root is two levels up.
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
}

// requirePython3WithAdminLib skips the calling test when python3 or
// admin_lib is unavailable, so `go test ./...` stays green off-machine (a
// laptop or CI box with no Python toolchain).
func requirePython3WithAdminLib(t *testing.T, root string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH — skipping Python/Go loader parity check")
	}
	cmd := exec.Command("python3", "-c", "import admin_lib.manifest, admin_lib.archetypes")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("admin_lib not importable (%v): %s", err, out)
	}
}

// pythonResolved is tools/archetype_parity.py's JSON shape, decoded generic
// so numeric/null handling matches encoding/json's defaults on both sides.
type pythonResolved = map[string]any

// runPythonParity shells out to tools/archetype_parity.py <name> and decodes
// its JSON.
func runPythonParity(t *testing.T, root, name string) pythonResolved {
	t.Helper()
	cmd := exec.Command("python3", filepath.Join(root, "tools", "archetype_parity.py"), name)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("tools/archetype_parity.py %s: %v\nstderr: %s", name, err, ee.Stderr)
		}
		t.Fatalf("tools/archetype_parity.py %s: %v", name, err)
	}
	var decoded pythonResolved
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode archetype_parity.py output for %s: %v\noutput: %s", name, err, out)
	}
	return decoded
}

// normaliseScriptRun substitutes an action's `run` for the contents of the
// file it names under the archetype's scripts/ directory, when one exists.
// The Go archetype.toml files repoint the three payload-carrying archetypes
// (apple, forge-engine, react-native) at scripts/<action>.py instead of
// inlining the Python body, so a byte comparison against the Python loader's
// resolved `run` (which IS the inline body) must read that file first. Every
// other archetype's `run` is untouched — normaliseScriptRun no-ops when
// `run` does not name an existing file under scripts/.
func normaliseScriptRun(t *testing.T, root, archName string, arch *archetype.Archetype, actions map[string]any) {
	t.Helper()
	if arch.Scripts == "" {
		return
	}
	for name, raw := range actions {
		act, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		run, ok := act["run"].(string)
		if !ok || run == "" {
			continue
		}
		scriptPath := filepath.Join(root, "archetypes", filepath.FromSlash(arch.Scripts), filepath.Base(run))
		// Only substitute when `run` actually names a file directly under
		// scripts/ (e.g. "scripts/build.py") — not a bare verb like "build_ios"
		// (the apple kind's own actions, which never point at scripts/).
		expectedRel := filepath.ToSlash(filepath.Join("scripts", filepath.Base(run)))
		if filepath.ToSlash(run) != expectedRel {
			continue
		}
		content, err := os.ReadFile(scriptPath)
		if err != nil {
			t.Fatalf("%s: action %q names script %q but it could not be read: %v", archName, name, run, err)
		}
		act["run"] = string(content)
	}
}

// projectGoResolved runs a project archetypes=[name] manifest through
// internal/archetype.Resolve and internal/manifest, then projects the merged
// manifest into the same shape tools/archetype_parity.py prints.
func projectGoResolved(t *testing.T, root, name string) (pythonResolved, *archetype.Archetype) {
	t.Helper()

	data := map[string]any{
		"project_name": "parity",
		"archetypes":   []any{name},
	}
	project, err := manifest.Parse(data, "<parity-test>")
	if err != nil {
		t.Fatalf("manifest.Parse: %v", err)
	}

	sp := archetype.SearchPath{{Label: "archetypes", FS: os.DirFS(filepath.Join(root, "archetypes"))}}
	merged, _, err := archetype.Resolve(project, sp)
	if err != nil {
		t.Fatalf("archetype.Resolve(%s): %v", name, err)
	}
	arch, err := archetype.Load(sp, name)
	if err != nil {
		t.Fatalf("archetype.Load(%s): %v", name, err)
	}

	urls := map[string]any{}
	for k, v := range merged.Urls {
		urls[k] = v
	}

	commands := map[string]any{}
	for cname, cmd := range merged.Overrides {
		var env any
		if cmd.Env != nil {
			e := map[string]any{}
			for k, v := range cmd.Env {
				e[k] = v
			}
			env = e
		}
		var logging any
		if cmd.HasLogFile || cmd.HasLog || cmd.HasRetain {
			l := map[string]any{}
			if cmd.HasLogFile {
				l["log_file"] = cmd.LogFile
			}
			if cmd.HasRetain {
				// json.Unmarshal on the Python side decodes every number as
				// float64, so match that here rather than leaving an int.
				l["log_retain"] = float64(cmd.LogRetain)
			}
			if cmd.HasLog {
				l["log"] = cmd.Log
			}
			logging = l
		}
		steps := []any{}
		for _, s := range cmd.Steps {
			steps = append(steps, s)
		}
		commands[cname] = map[string]any{
			"desc":     cmd.Desc,
			"steps":    steps,
			"group":    float64(cmd.Group),
			"priority": float64(cmd.Priority),
			"env":      env,
			"logging":  logging,
		}
	}

	actions := map[string]any{}
	for aname, raw := range merged.Actions {
		act := map[string]any(raw)
		kind, _ := act["kind"].(string)
		run, _ := act["run"].(string)
		desc, ok := act["desc"].(string)
		if !ok {
			desc = aname
		}
		actions[aname] = map[string]any{
			"kind":    kind,
			"run":     run,
			"desc":    desc,
			"targets": map[string]any{},
		}
	}
	normaliseScriptRun(t, root, name, arch, actions)

	return pythonResolved{
		"urls":     urls,
		"commands": commands,
		"actions":  actions,
	}, arch
}

// roundTripJSON re-encodes/decodes v through encoding/json so both sides of
// a comparison share exactly the same type representation (numbers as
// float64, absent maps as nil, etc.) regardless of how each was built.
func roundTripJSON(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	return out
}

func TestGoAndPythonLoadersAgree(t *testing.T) {
	root := repoRoot(t)
	requirePython3WithAdminLib(t, root)

	for _, name := range archetypeNames {
		name := name
		t.Run(name, func(t *testing.T) {
			pyResolved := runPythonParity(t, root, name)
			goResolved, _ := projectGoResolved(t, root, name)

			pyNorm := roundTripJSON(t, pyResolved)
			goNorm := roundTripJSON(t, goResolved)

			if !reflect.DeepEqual(pyNorm, goNorm) {
				pyJSON, _ := json.MarshalIndent(pyNorm, "", "  ")
				goJSON, _ := json.MarshalIndent(goNorm, "", "  ")
				t.Errorf("archetype %q: Go and Python loaders disagree\n--- python (admin_lib.archetypes.resolve) ---\n%s\n--- go (internal/archetype.Resolve) ---\n%s", name, pyJSON, goJSON)
			}
		})
	}
}

// pythonActionBodyWrapper mirrors admin_lib/interp.py::_make_python exactly:
// the action's `run` becomes the body of a synthetic function, indented by
// four spaces under `def __py_action(args):`. Several extracted scripts
// (react-native's dev.py, forge-engine's build.py) contain a bare top-level
// `return`, which is a SyntaxError as a standalone module — this wrapper is
// what "compiles" means for a kind="python" body, matching how the
// interpreter actually loads one.
const pythonActionBodyWrapperScript = `
import sys, textwrap

path = sys.argv[1]
with open(path) as f:
    src = f.read()
code = "def __py_action(args):\n" + textwrap.indent(src.strip(), "    ") + "\n"
compile(code, "<python-action:" + path + ">", "exec")
`

// TestExtractedScriptsCompileAsPythonActionBodies is the "scripts run
// standalone under python3" acceptance criterion, discharged the way a
// kind="python" body actually loads: compiled as an indented function body
// under interp._make_python's own wrapper, not run as a bare script. A
// plain `python3 <script>` is not equivalent — see the module doc.
func TestExtractedScriptsCompileAsPythonActionBodies(t *testing.T) {
	root := repoRoot(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}

	scriptDirs := map[string][]string{
		"apple":        {"build.py", "dev.py", "deploy.py", "test.py", "clean.py", "icons.py", "reload.py", "appid.py"},
		"forge-engine": {"build.py", "dev.py", "test.py", "clean.py"},
		"react-native": {"dev.py", "metro.py"},
	}

	wrapperPath := filepath.Join(t.TempDir(), "wrapper.py")
	if err := os.WriteFile(wrapperPath, []byte(pythonActionBodyWrapperScript), 0o644); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}

	names := make([]string, 0, len(scriptDirs))
	for name := range scriptDirs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, archName := range names {
		for _, script := range scriptDirs[archName] {
			archName, script := archName, script
			t.Run(archName+"/"+script, func(t *testing.T) {
				scriptPath := filepath.Join(root, "archetypes", archName, "scripts", script)
				if _, err := os.Stat(scriptPath); err != nil {
					t.Fatalf("script missing: %v", err)
				}
				out, err := exec.Command("python3", wrapperPath, scriptPath).CombinedOutput()
				if err != nil {
					t.Errorf("scripts/%s does not compile as a python-action body: %v\n%s", script, err, out)
				}
			})
		}
	}
}
