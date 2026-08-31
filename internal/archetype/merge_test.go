package archetype

import (
	"os"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

func projectManifest(t *testing.T, toml string) *manifest.Manifest {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/"+manifest.Filename, []byte(toml), 0o644); err != nil {
		t.Fatalf("write admin.toml: %v", err)
	}
	m, err := manifest.Load(dir)
	if err != nil {
		t.Fatalf("manifest.Load: %v", err)
	}
	return m
}

func TestLaterArchetypeWinsOnConflict(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "a", `
[commands.build]
desc  = "from a"
steps = ["step-a"]
`, false)
	writeArchetypeDir(t, root, "b", `
[commands.build]
desc  = "from b"
steps = ["step-b"]
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["a", "b"]
`)

	merged, log, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := merged.Overrides["build"].Desc; got != "from b" {
		t.Errorf("Overrides[build].Desc = %q, want %q (b listed after a must win)", got, "from b")
	}

	found := false
	for _, line := range log {
		if strings.Contains(line, "'build'") && strings.Contains(line, "archetype 'a'") && strings.Contains(line, "archetype 'b'") {
			found = true
		}
	}
	if !found {
		t.Errorf("merge log = %v, want a line naming the 'build' collision between archetype 'a' and 'b'", log)
	}
}

// TestTrackerAndUsesPullRequestsMerge is admin-3rgp.21's merge rule for the
// two new scalar manifest keys: an archetype may declare them, but the
// project's own admin.toml always wins when it declares them too (ADR-0009),
// and a key neither side declares stays undeclared (Has* false) all the way
// through — the Config tab's "not declared" case must not turn into a
// silently-defaulted false/empty from the merge itself.
func TestTrackerAndUsesPullRequestsMerge(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "beads-tracker", `
tracker            = "beads"
uses_pull_requests = false
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	// The project overrides tracker but leaves uses_pull_requests to the
	// archetype.
	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["beads-tracker"]
tracker      = "github"
`)
	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !merged.HasTracker || merged.Tracker != "github" {
		t.Errorf("Tracker = %q, HasTracker = %v, want %q, true (manifest beats archetype)", merged.Tracker, merged.HasTracker, "github")
	}
	if !merged.HasUsesPullRequests || merged.UsesPullRequests {
		t.Errorf("UsesPullRequests = %v, HasUsesPullRequests = %v, want false, true (from the archetype, untouched by the manifest)", merged.UsesPullRequests, merged.HasUsesPullRequests)
	}

	// Neither the project nor any archetype declares either key.
	bare := projectManifest(t, `project_name = "widget"`)
	mergedBare, _, err := Resolve(bare, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if mergedBare.HasTracker || mergedBare.HasUsesPullRequests {
		t.Errorf("HasTracker = %v, HasUsesPullRequests = %v, want false, false for a manifest with no archetypes and no declaration", mergedBare.HasTracker, mergedBare.HasUsesPullRequests)
	}
}

// TestWorkflowActionMergesFromArchetypesLikeAnyAction is admin-3rgp.24's
// acceptance criterion "Workflows merge from archetypes the same way
// commands and actions do": a kind="workflow" action contributed by an
// archetype is overridden by the project the same whole-value-replacement
// way any other action is (internal/archetype/merge.go's mergeMap over
// dst.Actions carries no kind-specific branch), and the project's own
// kind="workflow" action from a second archetype layer merges the same way
// too.
func TestWorkflowActionMergesFromArchetypesLikeAnyAction(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "wf", `
[actions.retire-merged]
kind = "workflow"
over = "selection"
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["wf"]

[actions.retire-merged]
kind = "workflow"
over.query = "label:merged"

[actions.review-waiting]
kind = "workflow"
over = "selection"
`)

	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// The project's own [actions.retire-merged] replaces the archetype's
	// whole-value, same as an ordinary action collision.
	over, ok := merged.Actions["retire-merged"]["over"].(map[string]any)
	if !ok {
		t.Fatalf("Actions[retire-merged][over] = %#v, want the project's table override", merged.Actions["retire-merged"]["over"])
	}
	if over["query"] != "label:merged" {
		t.Fatalf("Actions[retire-merged][over][query] = %v, want %q", over["query"], "label:merged")
	}

	// An action the project alone declares (no archetype contributed it)
	// merges through untouched.
	if got := merged.Actions["review-waiting"]["over"]; got != "selection" {
		t.Fatalf("Actions[review-waiting][over] = %v, want %q", got, "selection")
	}
}

func TestManifestOverridesArchetype(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "docker", `
[commands.build]
desc  = "archetype build"
steps = ["docker-build"]
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["docker"]

[commands.build]
desc  = "project build"
steps = ["custom-build"]
`)

	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := merged.Overrides["build"]
	if got.Desc != "project build" {
		t.Errorf("Overrides[build].Desc = %q, want %q (project [commands] beats every archetype)", got.Desc, "project build")
	}
	if len(got.Steps) != 1 || got.Steps[0] != "custom-build" {
		t.Errorf("Overrides[build].Steps = %v, want [custom-build]", got.Steps)
	}
}

func TestManifestOverrideFallsBackToArchetypeFields(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "docker", `
[commands.build]
desc     = "archetype build"
steps    = ["docker-build"]
group    = 2
priority = 5
log_file = "build.log"

[commands.build.env]
FOO = "bar"
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["docker"]

[commands.build]
desc = "project build"
`)

	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := merged.Overrides["build"]
	if got.Desc != "project build" {
		t.Errorf("Desc = %q, want %q", got.Desc, "project build")
	}
	if len(got.Steps) != 1 || got.Steps[0] != "docker-build" {
		t.Errorf("Steps = %v, want [docker-build] (archetype's steps must survive a desc-only override)", got.Steps)
	}
	if got.Group != 2 {
		t.Errorf("Group = %d, want 2 (fallback to archetype)", got.Group)
	}
	if got.Priority != 5 {
		t.Errorf("Priority = %d, want 5 (fallback to archetype)", got.Priority)
	}
	if got.LogFile != "build.log" || !got.HasLogFile {
		t.Errorf("LogFile = %q (has=%v), want %q (has=true)", got.LogFile, got.HasLogFile, "build.log")
	}
	if got.Env["FOO"] != "bar" {
		t.Errorf("Env[FOO] = %q, want %q (fallback to archetype's env)", got.Env["FOO"], "bar")
	}
}

func TestManifestOverrideWithoutStepsAndNoArchetypeIsError(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "docker", `
[commands.build]
desc  = "archetype build"
steps = ["docker-build"]
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["docker"]

[commands.deploy]
desc = "no archetype provides this"
`)

	_, _, err := Resolve(project, sp)
	if err == nil {
		t.Fatal("Resolve: got nil error, want an error naming 'deploy' and '[actions]'")
	}
	if !strings.Contains(err.Error(), "deploy") {
		t.Errorf("error = %q, want it to name the command 'deploy'", err.Error())
	}
	if !strings.Contains(err.Error(), "[actions]") {
		t.Errorf("error = %q, want it to mention moving run/kind/targets to [actions]", err.Error())
	}
}

func TestNoArchetypesResolvesCleanly(t *testing.T) {
	// No archetype directories exist anywhere on the search path.
	home := t.TempDir()
	repo := t.TempDir()
	sp := DefaultSearchPath(home, repo)

	project := projectManifest(t, `
project_name = "widget"

[commands.build]
desc  = "just the project"
steps = ["compile"]
`)

	merged, log, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v, want a manifest with no archetypes to resolve cleanly", err)
	}
	if len(merged.Overrides) != 1 || merged.Overrides["build"].Desc != "just the project" {
		t.Errorf("Overrides = %v, want just the project's own 'build'", merged.Overrides)
	}
	if len(log) != 0 {
		t.Errorf("merge log = %v, want empty — nothing to collide with", log)
	}
	if merged.ProjectName != "widget" {
		t.Errorf("ProjectName = %q, want %q", merged.ProjectName, "widget")
	}
}

// TestResolveRejectsUnknownGuardReference is ADR-0005's resolve-phase audit,
// exercised through the real Resolve entry point rather than calling
// guards.ValidateRequires directly: a `requires` naming a guard that is
// neither a built-in nor a manifest-declared [guards.<name>] table must
// fail Resolve itself, since a project's admin.toml is invalid without
// ever loading any archetype.
func TestResolveRejectsUnknownGuardReference(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	sp := DefaultSearchPath(home, repo)

	project := projectManifest(t, `
project_name = "widget"

[actions.deploy]
kind     = "shell"
run      = "true"
requires = ["no-such-guard"]
`)

	_, _, err := Resolve(project, sp)
	if err == nil {
		t.Fatal("Resolve: got nil error, want a validation error naming the unknown guard")
	}
	if !strings.Contains(err.Error(), "no-such-guard") {
		t.Errorf("error = %q, want it to name the unknown guard", err.Error())
	}
}

// TestResolveAcceptsUserDefinedGuardReference proves the audit sees guards
// contributed by the project manifest itself (not just built-ins) —
// [guards.<name>] and the requires referencing it live in the same file.
func TestResolveAcceptsUserDefinedGuardReference(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	sp := DefaultSearchPath(home, repo)

	project := projectManifest(t, `
project_name = "widget"

[guards.no-tag]
run  = "true"
fail = "already tagged"

[actions.deploy]
kind     = "shell"
run      = "true"
requires = ["git-clean", "no-tag"]
`)

	if _, _, err := Resolve(project, sp); err != nil {
		t.Fatalf("Resolve: %v, want nil — both requires names resolve", err)
	}
}
