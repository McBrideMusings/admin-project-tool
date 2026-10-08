// locale_test.go covers `locale = "window"`'s dashboard-producer-only
// contract (restored by admin-wwrg, withdrawing admin-m6m8's producer-independent
// one): RunAction (every step, plain CLI or already-launched) never consults
// `locale` and never reaches the project's [terminal] launcher (ADR-0020);
// RunCommand's top-level dispatch consults it through launchCommand for its
// required dashboard CommandProducer — the CLI's shape now calls
// RunCommandInline instead of reaching RunCommand at all, and runs inline
// exactly like RunAction does. It replaces lifetime_test.go — the Lifetime
// kind-catalog attribute admin-ysxr.2 deleted.
package kinds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// localeManifest is the shared fixture for the acceptance tests: one
// detached action whose `run` would leave a real, checkable side effect
// (ran.txt) if it executed inline — so "did the launcher intercept it" and
// "did it run inline" are both directly observable. The "dev" command lets a
// producer-path test dispatch through RunCommand; launchCommand keys the
// live entry on that command name.
func localeManifest(terminal map[string]any) *manifest.Manifest {
	return &manifest.Manifest{
		Terminal: terminal,
		Custom: map[string]manifest.Command{
			"dev": {Steps: []string{"dev-server"}},
		},
		Actions: map[string]manifest.Action{
			"dev-server": {
				"kind":   "shell",
				"run":    "touch ran.txt",
				"locale": "window",
			},
		},
	}
}

// TestRunActionCLILocaleRunsInlineNeverReachesLauncher proves RunAction
// itself — every step's own dispatch, whichever surface the enclosing
// command landed on — runs a detached action inline and never reaches the
// launcher, even with a spawnable [terminal] declared. The launcher decision
// is made once, in launchCommand, before any step runs.
func TestRunActionCLILocaleRunsInlineNeverReachesLauncher(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	dir := t.TempDir()
	t.Chdir(dir)

	before := len(livework.Default.List())

	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	logPath := filepath.Join(dir, "terminal-calls.log")
	m := localeManifest(withFakeTerminal(t, "fake-handle-1", logPath))

	stderr := captureStderr(t, func() {
		if err := RunAction(m, r, "dev-server", nil); err != nil {
			t.Fatalf("RunAction = %v, want nil", err)
		}
	})
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	assertFileExists(t, dir, "ran.txt")

	if logged, err := os.ReadFile(logPath); err == nil && len(logged) > 0 {
		t.Errorf("terminal log = %q, want empty — a CLI dispatch must never reach the launcher", string(logged))
	}
	if got := len(livework.Default.List()); got != before {
		t.Errorf("live-work registry grew from %d to %d — a CLI dispatch must never register a launcher entry", before, got)
	}
}

// TestRunCommandDashboardProducerLocaleDispatchesThroughLauncher is the
// producer-path twin: the identical detached action, dispatched through
// RunCommand with a non-nil CommandProducer, still reaches the launcher and
// never falls back to the producer's own default surface.
func TestRunCommandDashboardProducerLocaleDispatchesThroughLauncher(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	dir := t.TempDir()
	t.Chdir(dir)

	before := len(livework.Default.List())
	launched := false

	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	m := localeManifest(map[string]any{"spawn": "echo fake-handle-1", "attach": "true", "kill": "true"})

	if err := RunCommand(m, r, "dev", nil, dashboardProducer(&launched)); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	if launched {
		t.Error("a dashboard producer must not fall back to its own default surface when a launcher is declared")
	}
	assertFileMissing(t, dir, "ran.txt")

	entries := livework.Default.List()
	found := false
	for _, e := range entries {
		if e.Name == "dev" && e.Handle == "fake-handle-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("live-work registry = %+v, want an entry for dev with handle fake-handle-1", entries)
	}
	if got := len(entries); got != before+1 {
		t.Errorf("live-work registry grew from %d to %d, want exactly one new entry", before, got)
	}
}

// TestRunCommandInlineNeverReachesLauncher proves RunCommandInline — the
// `admin cmd` picker's own entry point — never reaches the launcher for a
// locale="window" command, even with a spawnable [terminal] declared, the same
// guarantee TestRunActionCLILocaleRunsInlineNeverReachesLauncher proves for
// RunAction. Unlike RunCommand, this must hold with no dependence on
// inReentry() or a CommandProducer at all.
func TestRunCommandInlineNeverReachesLauncher(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	dir := t.TempDir()
	t.Chdir(dir)

	before := len(livework.Default.List())

	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	logPath := filepath.Join(dir, "terminal-calls.log")
	m := localeManifest(withFakeTerminal(t, "fake-handle-1", logPath))

	if err := RunCommandInline(m, r, "dev", nil); err != nil {
		t.Fatalf("RunCommandInline = %v, want nil", err)
	}
	assertFileExists(t, dir, "ran.txt")

	if logged, err := os.ReadFile(logPath); err == nil && len(logged) > 0 {
		t.Errorf("terminal log = %q, want empty — RunCommandInline must never reach the launcher", string(logged))
	}
	if got := len(livework.Default.List()); got != before {
		t.Errorf("live-work registry grew from %d to %d — RunCommandInline must never register a launcher entry", before, got)
	}
}

// TestRunActionNoTerminalRunsInline proves the same action with no
// [terminal] declared runs inline — the unaffected case, since a CLI
// dispatch never consulted the launcher in the first place.
func TestRunActionNoTerminalRunsInline(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	dir := t.TempDir()
	t.Chdir(dir)

	before := len(livework.Default.List())

	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	m := localeManifest(nil)

	if err := RunAction(m, r, "dev-server", nil); err != nil {
		t.Fatalf("RunAction = %v, want nil", err)
	}
	assertFileExists(t, dir, "ran.txt")

	after := livework.Default.List()
	if len(after) != before {
		t.Errorf("live-work registry grew from %d to %d entries — a plain-CLI dispatch must never "+
			"register a launcher entry", before, len(after))
	}
}

// TestRunActionLocaleAbsentRunsInline proves an action with no `locale` key
// at all is unaffected by a declared [terminal] — irrelevant to a CLI
// dispatch either way, since `locale` is never consulted on this path.
func TestRunActionLocaleAbsentRunsInline(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	m := &manifest.Manifest{
		Terminal: map[string]any{"spawn": "echo should-not-be-called"},
		Actions: map[string]manifest.Action{
			"build": {"kind": "shell", "run": "touch ran.txt"},
		},
	}
	if err := RunAction(m, r, "build", nil); err != nil {
		t.Fatalf("RunAction = %v, want nil", err)
	}
	assertFileExists(t, dir, "ran.txt")
}

// localeOf maps the present/value pairs the fixtures in hosted_test.go
// were written with onto the two words a manifest can write.
func localeOf(window bool) string {
	if window {
		return "window"
	}
	return "hosted"
}

// -- ValidateLocale ---------------------------------------------------------

func TestValidateLocaleAcceptsWindowAndHosted(t *testing.T) {
	actions := map[string]manifest.Action{
		"dev": {"kind": "shell", "run": "true", "locale": "window"},
		"ci": {
			"kind": "multi-target",
			"cf":   map[string]any{"run": "true", "locale": "hosted"},
		},
	}
	if err := ValidateLocale(actions); err != nil {
		t.Errorf("ValidateLocale() = %v, want nil", err)
	}
}

func TestValidateLocaleRejectsBadValuesNamingActionAndTarget(t *testing.T) {
	cases := []struct {
		name    string
		actions map[string]manifest.Action
		want    []string
	}{
		{"non-string on an action",
			map[string]manifest.Action{"dev": {"kind": "shell", "run": "true", "locale": true}},
			[]string{`action "dev"`, `must be "window" or "hosted"`}},
		{"inline is not a value",
			map[string]manifest.Action{"dev": {"kind": "shell", "run": "true", "locale": "inline"}},
			[]string{`action "dev"`, "a plain shell always runs inline"}},
		{"unknown word on a target",
			map[string]manifest.Action{"dev": {"kind": "multi-target", "cf": map[string]any{"run": "true", "locale": "pane"}}},
			[]string{`action "dev"`, `target "cf"`, `"pane" is unknown`}},
		{"detach is not a key on an action",
			map[string]manifest.Action{"dev": {"kind": "shell", "run": "true", "detach": true}},
			[]string{`action "dev"`, "detach is not a key"}},
		{"detach is not a key on a target",
			map[string]manifest.Action{"dev": {"kind": "multi-target", "cf": map[string]any{"run": "true", "detach": true}}},
			[]string{`target "cf"`, "detach is not a key"}},
		{"a workflow still rejects an unknown word",
			map[string]manifest.Action{"impl": {"kind": "workflow", "locale": "pane"}},
			[]string{`action "impl"`, `"pane" is unknown`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateLocale(c.actions)
			if err == nil {
				t.Fatal("ValidateLocale() = nil, want an error")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error = %v, want it to contain %q", err, w)
				}
			}
		})
	}
}

func TestValidateLocaleAcceptsWindowAndHostedOnAWorkflow(t *testing.T) {
	for _, loc := range []string{"window", "hosted"} {
		actions := map[string]manifest.Action{"impl": {"kind": "workflow", "locale": loc}}
		if err := ValidateLocale(actions); err != nil {
			t.Errorf("locale %q: ValidateLocale() = %v, want nil", loc, err)
		}
	}
}

// TestMultiTargetPerTargetLocaleRunsInlineFromCLI proves the multi-target
// factory's Runner no longer has a launcher branch of its own at all: a
// picked target's own `locale = "window"` override is never consulted from a
// plain-CLI dispatch (RunAction), whatever the parent action's default is.
func TestMultiTargetPerTargetLocaleRunsInlineFromCLI(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	dir := t.TempDir()
	t.Chdir(dir)

	before := len(livework.Default.List())

	r := NewRegistry()
	r.MustRegister("multi-target", makeMultiTarget)
	r.MustRegister("shell", makeShell)

	logPath := filepath.Join(dir, "terminal-calls.log")
	m := &manifest.Manifest{
		Terminal: withFakeTerminal(t, "fake-handle-2", logPath),
		Actions: map[string]manifest.Action{
			"dev": {
				"kind":   "multi-target",
				"locale": "hosted",
				"cf":     map[string]any{"run": "touch cf-ran.txt", "locale": "window"},
				"local":  map[string]any{"run": "touch local-ran.txt"},
			},
		},
	}

	if err := RunAction(m, r, "dev", []string{"cf"}); err != nil {
		t.Fatalf("RunAction(cf) = %v, want nil", err)
	}
	assertFileExists(t, dir, "cf-ran.txt")

	if logged, err := os.ReadFile(logPath); err == nil && len(logged) > 0 {
		t.Errorf("terminal log = %q, want empty — a CLI dispatch must never reach the launcher", string(logged))
	}
	if got := len(livework.Default.List()); got != before {
		t.Errorf("live-work registry grew from %d to %d — a CLI dispatch must never register a launcher entry", before, got)
	}
}

// TestMultiTargetPerTargetLocaleDashboardProducerDispatchesThroughLauncher is
// the producer-path twin: the identical per-target override, dispatched
// through RunCommand with a non-nil CommandProducer, still reaches the
// launcher via launchCommand's own dispatchLocale resolution.
func TestMultiTargetPerTargetLocaleDashboardProducerDispatchesThroughLauncher(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	dir := t.TempDir()
	t.Chdir(dir)

	before := len(livework.Default.List())
	launched := false

	r := NewRegistry()
	r.MustRegister("multi-target", makeMultiTarget)
	r.MustRegister("shell", makeShell)

	m := &manifest.Manifest{
		Terminal: map[string]any{"spawn": "echo fake-handle-2", "attach": "true", "kill": "true"},
		Custom: map[string]manifest.Command{
			"dev": {Steps: []string{"pick"}},
		},
		Actions: map[string]manifest.Action{
			"pick": {
				"kind":   "multi-target",
				"locale": "hosted",
				"cf":     map[string]any{"run": "touch cf-ran.txt", "locale": "window"},
				"local":  map[string]any{"run": "touch local-ran.txt"},
			},
		},
	}

	if err := RunCommand(m, r, "dev", []string{"cf"}, dashboardProducer(&launched)); err != nil {
		t.Fatalf("RunCommand(cf) = %v, want nil", err)
	}
	if launched {
		t.Error("a dashboard producer must not fall back to its own default surface when a launcher is declared")
	}
	assertFileMissing(t, dir, "cf-ran.txt")

	entries := livework.Default.List()
	found := false
	for _, e := range entries {
		if e.Name == "dev:cf" && e.Handle == "fake-handle-2" {
			found = true
		}
	}
	if !found {
		t.Errorf("live-work registry = %+v, want an entry named dev:cf with handle fake-handle-2 (a genuine multi-target menu keys its entry on the command and the picked target)", entries)
	}
	if got := len(entries); got != before+1 {
		t.Errorf("live-work registry has %d entries, want exactly %d", got, before+1)
	}
}

// TestWindowEntryAttachRunsLauncherAttachWithHandle is admin-ysxr.3's
// acceptance criterion: pressing attach on a launcher-dispatched row runs
// that launcher's attach command with ADMIN_HANDLE set to the handle spawn
// printed. The fake launcher's attach writes ADMIN_HANDLE to a file rather
// than actually attaching to anything, so the observable proof is that
// file's content. Dispatched through a dashboard CommandProducer here, but a
// nil (CLI) producer reaches the identical launcher entry.
func TestWindowEntryAttachRunsLauncherAttachWithHandle(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	dir := t.TempDir()
	t.Chdir(dir)

	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	m := localeManifest(map[string]any{
		"spawn":  "echo fake-handle-1",
		"attach": `echo "$ADMIN_HANDLE" > attached.txt`,
		"kill":   `echo "$ADMIN_HANDLE" > killed.txt`,
	})

	if err := RunCommand(m, r, "dev", nil, dashboardProducer(new(bool))); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}

	if err := livework.Default.Attach("dev"); err != nil {
		t.Fatalf("Attach(dev) = %v, want nil", err)
	}
	assertFileExists(t, dir, "attached.txt")
	body, err := os.ReadFile(filepath.Join(dir, "attached.txt"))
	if err != nil {
		t.Fatalf("reading attached.txt: %v", err)
	}
	if got := strings.TrimSpace(string(body)); got != "fake-handle-1" {
		t.Errorf("attached.txt = %q, want the spawned handle %q", got, "fake-handle-1")
	}
}

// TestWindowEntryKillRunsLauncherKillWithHandle proves killing a
// launcher-dispatched row runs the launcher's kill command, also with
// ADMIN_HANDLE set to the spawned handle. Reached through the producer path,
// for the same reason TestWindowEntryAttachRunsLauncherAttachWithHandle is.
func TestWindowEntryKillRunsLauncherKillWithHandle(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	dir := t.TempDir()
	t.Chdir(dir)

	r := NewRegistry()
	r.MustRegister("shell", makeShell)

	m := localeManifest(map[string]any{
		"spawn":  "echo fake-handle-1",
		"attach": `echo "$ADMIN_HANDLE" > attached.txt`,
		"kill":   `echo "$ADMIN_HANDLE" > killed.txt`,
	})

	if err := RunCommand(m, r, "dev", nil, dashboardProducer(new(bool))); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}

	if err := livework.Default.Kill("dev"); err != nil {
		t.Fatalf("Kill(dev) = %v, want nil", err)
	}
	assertFileExists(t, dir, "killed.txt")
	body, err := os.ReadFile(filepath.Join(dir, "killed.txt"))
	if err != nil {
		t.Fatalf("reading killed.txt: %v", err)
	}
	if got := strings.TrimSpace(string(body)); got != "fake-handle-1" {
		t.Errorf("killed.txt = %q, want the spawned handle %q", got, "fake-handle-1")
	}
}

// TestLauncherShellOutputStaysOffTheTerminal proves a launcher command's
// output never reaches this process's stdout or stderr — the dashboard's
// screen when it runs a kill — and that a failure carries it in the error.
func TestLauncherShellOutputStaysOffTheTerminal(t *testing.T) {
	sink, err := os.Create(filepath.Join(t.TempDir(), "terminal"))
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = sink, sink
	err = runLauncherShell(`echo out; echo '{"error":"workspace w1D0 not found"}' >&2; exit 1`, "w1D0:p1")
	os.Stdout, os.Stderr = stdout, stderr

	if err == nil || !strings.Contains(err.Error(), "workspace w1D0 not found") {
		t.Errorf("runLauncherShell error = %v, want one carrying the command's stderr", err)
	}
	if info, _ := sink.Stat(); info.Size() != 0 {
		body, _ := os.ReadFile(sink.Name())
		t.Errorf("launcher output reached the terminal: %q", body)
	}
}

// -- [terminal].locale as the base of the chain ----------------------------

// TestDispatchLocaleChain covers the resolutions the chain names: the
// terminal default applies when nothing more specific speaks, an action's own
// locale beats it, and a target's own locale beats the action's and the
// terminal's. A workflow action always resolves to hosted, whatever else is
// declared, because the action itself never runs in a window.
func TestDispatchLocaleChain(t *testing.T) {
	multi := func(parent map[string]any, target map[string]any) manifest.Action {
		a := manifest.Action{"kind": "multi-target", "playtest": target}
		for k, v := range parent {
			a[k] = v
		}
		return a
	}
	cases := []struct {
		name     string
		action   manifest.Action
		terminal string
		want     string
	}{
		{"nothing declared anywhere", manifest.Action{"kind": "shell"}, "", ""},
		{"terminal window, action silent", manifest.Action{"kind": "shell"}, "window", "window"},
		{"terminal hosted, action silent", manifest.Action{"kind": "shell"}, "hosted", "hosted"},
		{"action hosted beats terminal window", manifest.Action{"kind": "shell", "locale": "hosted"}, "window", "hosted"},
		{"terminal hosted, action window", manifest.Action{"kind": "shell", "locale": "window"}, "hosted", "window"},
		{"multi-target parent inherits terminal window", multi(nil, map[string]any{"run": "true"}), "window", "window"},
		{"target hosted beats inherited terminal window", multi(nil, map[string]any{"run": "true", "locale": "hosted"}), "window", "hosted"},
		{"target hosted beats parent window", multi(map[string]any{"locale": "window"}, map[string]any{"run": "true", "locale": "hosted"}), "window", "hosted"},
		{"target window beats terminal hosted", multi(nil, map[string]any{"run": "true", "locale": "window"}), "hosted", "window"},
		{"workflow is hosted even under a window default", manifest.Action{"kind": "workflow", "locale": "window"}, "window", "hosted"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, resolvable := dispatchLocale(c.action, []string{"playtest"}, c.terminal)
			if !resolvable || got != c.want {
				t.Errorf("dispatchLocale = (%q, %v), want (%q, true)", got, resolvable, c.want)
			}
		})
	}
}

// TestRunCommandDashboardProducerTerminalLocaleDefault runs the whole
// dispatch: [terminal].locale = "window" with an action that declares no
// `locale` reaches the launcher, and the same manifest with the action's own
// locale = "hosted" runs on the producer's default surface.
func TestRunCommandDashboardProducerTerminalLocaleDefault(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")
	terminal := withFakeTerminal(t, "fake-handle-td", logPath)
	terminal["locale"] = "window"

	build := func(action manifest.Action) *manifest.Manifest {
		return &manifest.Manifest{
			Terminal: terminal,
			Custom:   map[string]manifest.Command{"dev": {Steps: []string{"td-step"}}},
			Actions:  map[string]manifest.Action{"td-step": action},
		}
	}

	launched := false
	if err := RunCommand(build(manifest.Action{"kind": "vite-dev"}), registryRecordingInline(new(bool)), "dev", nil, dashboardProducer(&launched)); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	if launched {
		t.Error("[terminal].locale = window with a silent action must reach the launcher, not the producer's default surface")
	}
	logged, _ := os.ReadFile(logPath)
	if !strings.Contains(string(logged), "spawn") {
		t.Errorf("terminal log = %q, want a `spawn` call", string(logged))
	}

	launched = false
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	if err := RunCommand(build(manifest.Action{"kind": "vite-dev", "locale": "hosted"}), registryRecordingInline(new(bool)), "dev", nil, dashboardProducer(&launched)); err != nil {
		t.Fatalf("RunCommand = %v, want nil", err)
	}
	if !launched {
		t.Error("an action's locale = hosted must beat [terminal].locale = window")
	}
	if logged, err := os.ReadFile(logPath); err == nil && strings.Contains(string(logged), "spawn") {
		t.Errorf("terminal log = %q, want no `spawn` call", string(logged))
	}
}

// TestRunCommandDashboardProducerRejectsRetiredDetachKey proves a leftover
// `detach` — on an action, or in [terminal] — stops the dashboard dispatch
// with an error instead of silently running the command hosted: nothing
// launches, nothing runs, and the producer's own surface is never reached.
func TestRunCommandDashboardProducerRejectsRetiredDetachKey(t *testing.T) {
	t.Cleanup(livework.UseRegistry(livework.NewRegistry()))
	dir := t.TempDir()
	t.Chdir(dir)
	logPath := filepath.Join(dir, "terminal-calls.log")

	cases := []struct {
		name     string
		terminal func() map[string]any
		action   manifest.Action
		want     string
	}{
		{"detach on the action", func() map[string]any { return withFakeTerminal(t, "h", logPath) },
			manifest.Action{"kind": "shell", "run": "touch ran.txt", "detach": true}, "detach is not a key"},
		{"detach in [terminal]", func() map[string]any {
			term := withFakeTerminal(t, "h", logPath)
			term["detach"] = true
			return term
		}, manifest.Action{"kind": "shell", "run": "touch ran.txt"}, "unknown key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			launched := false
			r := NewRegistry()
			r.MustRegister("shell", makeShell)
			m := &manifest.Manifest{
				Terminal: c.terminal(),
				Custom:   map[string]manifest.Command{"dev": {Steps: []string{"dev-step"}}},
				Actions:  map[string]manifest.Action{"dev-step": c.action},
			}
			err := RunCommand(m, r, "dev", nil, dashboardProducer(&launched))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("RunCommand = %v, want an error containing %q", err, c.want)
			}
			if launched {
				t.Error("the producer's default surface must not run a command whose manifest was rejected")
			}
			assertFileMissing(t, dir, "ran.txt")
			if logged, err := os.ReadFile(logPath); err == nil && len(logged) > 0 {
				t.Errorf("terminal log = %q, want empty — nothing may spawn", string(logged))
			}
		})
	}
}
