package kinds

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

func newCmdArgsTestRegistry() *Registry {
	r := newDispatchTestRegistry()
	r.MustRegister("shell-passthrough", makeShellPassthrough)
	return r
}

// cmdArgsManifest is a project whose every step leaves a marker file, so a
// test can tell from the directory alone which steps ran.
//
//	deploy  — backup (shell, ignores its args), then push (passthrough that
//	          implements --dry-run and records the args it got)
//	ship    — multi-target: files runs inline, image delegates to push,
//	          raw delegates to backup
func cmdArgsManifest() *manifest.Manifest {
	return &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"deploy": {Desc: "Back up, then push", HasDesc: true, Steps: []string{"backup", "push"}},
			"ship":   {Desc: "Ship one part", HasDesc: true, Steps: []string{"ship"}},
		},
		Actions: map[string]manifest.Action{
			"backup": {"kind": "shell", "run": "touch backup.marker"},
			"push": {"kind": "shell-passthrough", "desc": "Push the build", "dry_run": true,
				"run": `touch push.marker; printf '%s\n' > push.args`},
			"ship": {"kind": "multi-target", "desc": "Ship one part",
				"files": map[string]any{"desc": "Sync the files", "run": "touch files.marker"},
				"image": map[string]any{"action": "push"},
				"raw":   map[string]any{"action": "backup"},
			},
		},
	}
}

// TestParseCommandArgs pins which words the tool takes: help and dry-run
// anywhere before a bare "--", the "--" itself removed, and nothing after it
// touched.
func TestParseCommandArgs(t *testing.T) {
	cases := []struct {
		in   []string
		want CommandArgs
	}{
		{nil, CommandArgs{Args: []string{}}},
		{[]string{"files"}, CommandArgs{Args: []string{"files"}}},
		{[]string{"--help"}, CommandArgs{Help: true, Args: []string{}}},
		{[]string{"files", "-h"}, CommandArgs{Help: true, Args: []string{"files"}}},
		{[]string{"--dry-run", "files"}, CommandArgs{DryRun: true, Args: []string{"--dry-run", "files"}}},
		{[]string{"x", "--", "--help", "--dry-run", "--"}, CommandArgs{Args: []string{"x", "--help", "--dry-run", "--"}}},
	}
	for _, c := range cases {
		if got := ParseCommandArgs(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseCommandArgs(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

// TestHelpRunsNothing is the etv-station incident: `admin deploy --help`
// ran deploy's first step (a production backup over ssh) because only the
// second step looked for --help. Every spelling and position of the help
// word, on a multi-step command and on a multi-target subcommand, must print
// help and leave no marker and no log behind.
//
// MUTATION EVIDENCE: removing the `if ca.Help` block from runSteps fails
// every case with "backup.marker exists" (or files.marker for ship files).
func TestHelpRunsNothing(t *testing.T) {
	cases := []struct {
		command string
		args    []string
		want    []string
	}{
		{"deploy", []string{"--help"}, []string{"Back up, then push", "backup", "push", "Push the build"}},
		{"deploy", []string{"-h"}, []string{"Back up, then push"}},
		{"deploy", []string{"--dry-run", "--help"}, []string{"Back up, then push"}},
		{"ship", []string{"files", "--help"}, []string{"Ship one part", "files", "Sync the files", "image", "raw"}},
		{"ship", []string{"image", "-h"}, []string{"Ship one part"}},
	}
	for _, c := range cases {
		t.Run(c.command+" "+strings.Join(c.args, " "), func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			logDir := resetProcessLogContext(t)

			var err error
			out := captureStdoutTo(t, t.TempDir(), func() {
				err = RunCommandInline(cmdArgsManifest(), newCmdArgsTestRegistry(), c.command, c.args)
			})
			if err != nil {
				t.Fatalf("RunCommandInline = %v, want nil", err)
			}
			for _, marker := range []string{"backup.marker", "push.marker", "files.marker"} {
				assertFileMissing(t, dir, marker)
			}
			for _, w := range append(c.want, "Usage: admin "+c.command, "Reserved arguments") {
				if !strings.Contains(out, w) {
					t.Errorf("help output lacks %q:\n%s", w, out)
				}
			}
			for _, d := range []string{dir, logDir} {
				if _, err := os.Stat(filepath.Join(d, "tmp")); err == nil {
					t.Errorf("help created a log dir under %s", d)
				}
			}
		})
	}
}

// TestHelpAfterDoubleDashReachesSteps: the one way to hand --help to the
// program a command wraps is to put it after "--", which is removed.
func TestHelpAfterDoubleDashReachesSteps(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	m := &manifest.Manifest{
		Custom:  map[string]manifest.Command{"wrap": {Steps: []string{"wrap"}}},
		Actions: map[string]manifest.Action{"wrap": {"kind": "shell-passthrough", "run": `printf '%s\n' > got.args`}},
	}
	captureStdoutTo(t, t.TempDir(), func() {
		if err := RunCommandInline(m, newCmdArgsTestRegistry(), "wrap", []string{"--", "--help", "-h"}); err != nil {
			t.Fatalf("RunCommandInline = %v, want nil", err)
		}
	})
	if got := readFile(t, dir, "got.args"); got != "--help\n-h\n" {
		t.Fatalf("step got args %q, want %q", got, "--help\n-h\n")
	}
}

// TestDryRunRunsOnlyActionsThatPreview: under --dry-run a step that ignores
// its arguments (the backup) is printed, not run, and a step that declares
// dry_run = true runs and receives --dry-run.
//
// MUTATION EVIDENCE: removing the dryRun check from RunAction fails with
// "backup.marker exists".
func TestDryRunRunsOnlyActionsThatPreview(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	var err error
	out := captureStdoutTo(t, t.TempDir(), func() {
		err = RunCommandInline(cmdArgsManifest(), newCmdArgsTestRegistry(), "deploy", []string{"--dry-run"})
	})
	if err != nil {
		t.Fatalf("RunCommandInline = %v, want nil", err)
	}
	assertFileMissing(t, dir, "backup.marker")
	if got := readFile(t, dir, "push.args"); got != "--dry-run\n" {
		t.Fatalf("push got args %q, want %q", got, "--dry-run\n")
	}
	if !strings.Contains(out, `dry run: not running action "backup"`) || !strings.Contains(out, "would run: touch backup.marker") {
		t.Errorf("dry run did not report the skipped backup:\n%s", out)
	}
	if dryRun.Load() {
		t.Error("dry run state outlived the command")
	}
}

// TestDryRunMultiTarget: an inline `run` target and a delegated target that
// cannot preview are printed, not run; a delegated target that can preview
// runs.
func TestDryRunMultiTarget(t *testing.T) {
	cases := []struct {
		target  string
		ran     string
		skipped string
	}{
		{"files", "", "files.marker"},
		{"raw", "", "backup.marker"},
		{"image", "push.marker", ""},
	}
	for _, c := range cases {
		t.Run(c.target, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			var err error
			out := captureStdoutTo(t, t.TempDir(), func() {
				err = RunCommandInline(cmdArgsManifest(), newCmdArgsTestRegistry(), "ship", []string{c.target, "--dry-run"})
			})
			if err != nil {
				t.Fatalf("RunCommandInline = %v, want nil", err)
			}
			if c.skipped != "" {
				assertFileMissing(t, dir, c.skipped)
				if !strings.Contains(out, "dry run: not running") {
					t.Errorf("dry run did not report the skipped target:\n%s", out)
				}
			}
			if c.ran != "" {
				assertFileExists(t, dir, c.ran)
			}
		})
	}
}

func TestValidateDryRun(t *testing.T) {
	if err := ValidateDryRun(cmdArgsManifest().Actions); err != nil {
		t.Fatalf("ValidateDryRun(valid) = %v, want nil", err)
	}
	err := ValidateDryRun(map[string]manifest.Action{
		"a": {"run": "true", "dry_run": "true"},
		"b": {"kind": "multi-target", "dry_run": true, "x": map[string]any{"run": "true"}},
	})
	if err == nil {
		t.Fatal("ValidateDryRun(invalid) = nil, want errors")
	}
	for _, w := range []string{`action "a": dry_run must be a boolean`, `action "b": dry_run has no effect on a multi-target`} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error lacks %q: %v", w, err)
		}
	}
}

// TestHelpReturnsBeforeEnvRequires: help reads only the manifest, so a
// project whose .env is incomplete still prints it, and exits 0.
func TestHelpReturnsBeforeEnvRequires(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	os.Unsetenv("ADMIN_CMDARGS_TEST_UNSET")
	m := cmdArgsManifest()
	m.EnvRequires = []string{"ADMIN_CMDARGS_TEST_UNSET"}
	var err error
	out := captureStdoutTo(t, t.TempDir(), func() {
		err = RunCommandInline(m, newCmdArgsTestRegistry(), "deploy", []string{"--help"})
	})
	if err != nil {
		t.Fatalf("RunCommandInline = %v, want nil", err)
	}
	if !strings.Contains(out, "Back up, then push") {
		t.Errorf("help not printed:\n%s", out)
	}
	if err := RunCommandInline(m, newCmdArgsTestRegistry(), "deploy", nil); err == nil {
		t.Error("the same command without --help ran despite the unset required variable")
	}
	assertFileMissing(t, dir, "backup.marker")
}
