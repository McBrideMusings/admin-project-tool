package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helpFixture is a project shaped like the etv-station deploy that ran for
// real on `admin deploy --help`: a shell step that ignores its arguments
// runs first, then a lua step that would have read --help itself, plus a
// multi-target command for the `deploy files --help` shape. Every step
// leaves a marker file.
const helpFixture = `project_name = "fixture"

[commands.deploy]
desc  = "Snapshot the host, then deploy (image|files|all)"
steps = ["backup", "deploy"]

[commands.ship]
desc  = "Ship one part"
steps = ["ship"]

[commands.build]
desc  = "Build it"
steps = ["build"]

[actions.backup]
kind = "shell"
run  = "touch backup.marker"

[actions.deploy]
kind    = "lua"
desc    = "Deploy image, files or all"
dry_run = true
run     = 'run_cmd("touch deploy.marker")'

[actions.build]
kind = "shell"
run  = "touch build.marker"

[actions.ship]
kind = "multi-target"
desc = "Ship one part"

[actions.ship.files]
desc = "Sync the files"
run  = "touch files.marker"

[actions.ship.image]
action = "deploy"
`

// TestManifestCommandHelpRunsNothing drives `admin <command> --help` through
// the real CLI path — dispatchArgs registers the manifest command, its RunE
// dispatches with the default registry — for a multi-step command, a lua
// step, a plain shell command, and a multi-target subcommand.
//
// MUTATION EVIDENCE: on the code before this fix, `deploy --help` leaves
// backup.marker (the shell step ran) and `build -h` leaves build.marker.
func TestManifestCommandHelpRunsNothing(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"deploy", "--help"}, []string{"Snapshot the host, then deploy", "backup", "Deploy image, files or all"}},
		{[]string{"deploy", "files", "--help"}, []string{"Snapshot the host, then deploy"}},
		{[]string{"build", "-h"}, []string{"Build it", "touch build.marker"}},
		{[]string{"ship", "files", "--help"}, []string{"Ship one part", "files", "Sync the files", "image"}},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(helpFixture), 0o644); err != nil {
				t.Fatal(err)
			}
			name := c.args[0]
			t.Cleanup(func() { removeRootCommand(t, name) })
			dispatchArgs(c.args)

			var runErr error
			out := captureStdout(t, func() {
				for _, cc := range rootCmd.Commands() {
					if cc.Name() == name {
						runErr = cc.RunE(cc, c.args[1:])
						return
					}
				}
				t.Fatalf("dispatchArgs registered no command under %q", name)
			})
			if runErr != nil {
				t.Fatalf("RunE = %v, want nil (exit 0)", runErr)
			}
			for _, marker := range []string{"backup.marker", "deploy.marker", "build.marker", "files.marker"} {
				if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
					t.Errorf("%s exists: --help ran a step", marker)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "tmp")); err == nil {
				t.Errorf("--help created %s", filepath.Join(dir, "tmp"))
			}
			for _, w := range append(c.want, "Usage: admin "+name) {
				if !strings.Contains(out, w) {
					t.Errorf("help output lacks %q:\n%s", w, out)
				}
			}
		})
	}
}

// TestManifestCommandDryRunSkipsShellStep: `admin deploy --dry-run` runs the
// lua step that previews and not the shell step before it.
func TestManifestCommandDryRunSkipsShellStep(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "admin.toml"), []byte(helpFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeRootCommand(t, "deploy") })
	dispatchArgs([]string{"deploy", "--dry-run"})
	var runErr error
	out := captureStdout(t, func() {
		for _, cc := range rootCmd.Commands() {
			if cc.Name() == "deploy" {
				runErr = cc.RunE(cc, []string{"--dry-run"})
			}
		}
	})
	if runErr != nil {
		t.Fatalf("RunE = %v, want nil", runErr)
	}
	if _, err := os.Stat(filepath.Join(dir, "backup.marker")); err == nil {
		t.Error("backup.marker exists: --dry-run ran a step that cannot preview")
	}
	if _, err := os.Stat(filepath.Join(dir, "deploy.marker")); err != nil {
		t.Error("deploy.marker missing: the dry_run = true step did not run")
	}
	if !strings.Contains(out, `dry run: not running action "backup"`) {
		t.Errorf("output lacks the skipped-step line:\n%s", out)
	}
}

// captureStdout runs fn with os.Stdout pointed at a temp file and returns
// what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdout.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = orig; f.Close() }()
	fn()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}
