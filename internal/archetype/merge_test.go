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
tracker            = ["beads", "github"]
uses_pull_requests = false
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	// The project overrides tracker but leaves uses_pull_requests to the
	// archetype. A declared list REPLACES the archetype's wholesale — it
	// does not append — so the merged Trackers is exactly ["github"], not
	// ["beads", "github", "github"] or any union of the two.
	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["beads-tracker"]
tracker      = ["github"]
`)
	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !merged.HasTracker || len(merged.Trackers) != 1 || merged.Trackers[0] != "github" {
		t.Errorf("Trackers = %v, HasTracker = %v, want [\"github\"], true (manifest replaces archetype)", merged.Trackers, merged.HasTracker)
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
over = "row"
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
over = "row"
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
	if got := merged.Actions["review-waiting"]["over"]; got != "row" {
		t.Fatalf("Actions[review-waiting][over] = %v, want %q", got, "row")
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

// TestManifestOverridePreservesArchetypePreflight is admin-tlhw.3's
// regression: mergeCommandOverrides field-merges a project override onto an
// archetype-provided command, and Preflight/HasPreflight was left out of
// that field list — so an archetype declaring `preflight = false` on a
// command (docker's own [commands.logs], the shape this ticket's own fix
// is about) lost that declaration the moment a project overrode any other
// field on the same command, silently reverting to "preflight applies."
func TestManifestOverridePreservesArchetypePreflight(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "docker", `
[commands.logs]
desc      = "archetype logs"
steps     = ["docker-logs"]
preflight = false
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["docker"]

[commands.logs]
desc = "project logs"
`)

	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := merged.Overrides["logs"]
	if !got.HasPreflight || got.Preflight {
		t.Errorf("Overrides[logs].Preflight = %v (has=%v), want false (has=true) — "+
			"the archetype's preflight=false must survive a desc-only override", got.Preflight, got.HasPreflight)
	}
}

// TestManifestOverridePreflightWinsOverArchetype is the other direction: a
// project explicitly declaring `preflight = false` on a command the
// archetype also provides must win, exactly like every other field.
func TestManifestOverridePreflightWinsOverArchetype(t *testing.T) {
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
preflight = false
`)

	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := merged.Overrides["build"]
	if !got.HasPreflight || got.Preflight {
		t.Errorf("Overrides[build].Preflight = %v (has=%v), want false (has=true) — project override must win", got.Preflight, got.HasPreflight)
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

// TestRowKeysMergePerKey covers the acceptance criterion directly: an
// archetype declaring two bindings for [keys.bead] plus a project declaring
// one different binding for the same row kind must yield all three, not the
// project's single entry replacing the archetype's whole table.
func TestRowKeysMergePerKey(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "bead-keys", `
[keys.bead]
enter = "implement"
o     = "open-bead"
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["bead-keys"]

[keys.bead]
i = "inspect"
`)

	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	bead, ok := merged.Keys["bead"].(map[string]any)
	if !ok {
		t.Fatalf("Keys[bead] = %#v, want map[string]any", merged.Keys["bead"])
	}
	want := map[string]any{"enter": "implement", "o": "open-bead", "i": "inspect"}
	if len(bead) != len(want) {
		t.Fatalf("Keys[bead] = %#v, want %#v", bead, want)
	}
	for k, v := range want {
		if bead[k] != v {
			t.Errorf("Keys[bead][%q] = %#v, want %#v", k, bead[k], v)
		}
	}
}

// TestRowKeysProjectWinsOnSharedKey covers the second acceptance criterion: a
// project binding the same key an archetype already bound must win on that
// key while leaving the archetype's other bindings for the row kind intact.
func TestRowKeysProjectWinsOnSharedKey(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "bead-keys", `
[keys.bead]
enter = "implement"
o     = "open-bead"
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["bead-keys"]

[keys.bead]
enter = "dispatch"
`)

	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	bead, ok := merged.Keys["bead"].(map[string]any)
	if !ok {
		t.Fatalf("Keys[bead] = %#v, want map[string]any", merged.Keys["bead"])
	}
	if bead["enter"] != "dispatch" {
		t.Errorf(`Keys[bead]["enter"] = %#v, want "dispatch" (project wins on a shared key)`, bead["enter"])
	}
	if bead["o"] != "open-bead" {
		t.Errorf(`Keys[bead]["o"] = %#v, want "open-bead" (archetype's other binding must survive)`, bead["o"])
	}
}

// TestRowKeysLaterArchetypeWinsOnSharedKey covers the archetype-vs-archetype
// direction: two archetypes each declaring [keys.bead], the later one
// winning on a shared key while each side's unique key survives, and a row
// kind only one side declares passing through untouched.
func TestRowKeysLaterArchetypeWinsOnSharedKey(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "a", `
[keys.bead]
enter = "from-a"
x     = "only-a"

[keys.pr]
m = "merge"
`, false)
	writeArchetypeDir(t, root, "b", `
[keys.bead]
enter = "from-b"
y     = "only-b"
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["a", "b"]
`)

	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	bead, ok := merged.Keys["bead"].(map[string]any)
	if !ok {
		t.Fatalf("Keys[bead] = %#v, want map[string]any", merged.Keys["bead"])
	}
	if bead["enter"] != "from-b" {
		t.Errorf(`Keys[bead]["enter"] = %#v, want "from-b" (later archetype wins on a shared key)`, bead["enter"])
	}
	if bead["x"] != "only-a" {
		t.Errorf(`Keys[bead]["x"] = %#v, want "only-a" (earlier archetype's unique key must survive)`, bead["x"])
	}
	if bead["y"] != "only-b" {
		t.Errorf(`Keys[bead]["y"] = %#v, want "only-b"`, bead["y"])
	}

	pr, ok := merged.Keys["pr"].(map[string]any)
	if !ok {
		t.Fatalf("Keys[pr] = %#v, want map[string]any", merged.Keys["pr"])
	}
	if pr["m"] != "merge" {
		t.Errorf(`Keys[pr]["m"] = %#v, want "merge" (a row kind only one side declares passes through untouched)`, pr["m"])
	}
}

// TestConfigTablesMergeProjectWinsOverArchetype is the "merged through
// archetypes like every other manifest key" acceptance criterion for
// [worktrees]/[beads]/[workflow]: an archetype may declare
// defaults, but the project's own admin.toml wins on any key it also
// declares, and a key only the archetype declares survives untouched.
func TestConfigTablesMergeProjectWinsOverArchetype(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "base", `
[worktrees]
root = "/archetype/worktrees"

[beads]
columns = ["id", "title"]

[workflow]
ceiling = 2
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["base"]

[workflow]
ceiling = 5
`)

	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if merged.Worktrees["root"] != "/archetype/worktrees" {
		t.Errorf("Worktrees[root] = %v, want the archetype's value to survive untouched", merged.Worktrees["root"])
	}
	if merged.Workflow["ceiling"] != int64(5) {
		t.Errorf("Workflow[ceiling] = %v (%T), want the project's value to win", merged.Workflow["ceiling"], merged.Workflow["ceiling"])
	}
	cols, ok := merged.Beads["columns"].([]any)
	if !ok || len(cols) != 2 {
		t.Errorf("Beads[columns] = %#v, want the archetype's two columns", merged.Beads["columns"])
	}
}

// [worktrees.hooks] merges by hook name (ADR-0024): a project declaring one
// hook keeps the archetype's others, and its own declaration of a shared hook
// wins.
func TestWorktreeHooksMergeByHookName(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "base", `
[worktrees.hooks]
post_create = "archetype-create"
pre_remove  = "archetype-remove"
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["base"]

[worktrees.hooks]
pre_remove = "project-remove"
`)

	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	hooks, _ := merged.Worktrees["hooks"].(map[string]any)
	if hooks["post_create"] != "archetype-create" {
		t.Errorf("hooks[post_create] = %v, want the archetype's value to survive", hooks["post_create"])
	}
	if hooks["pre_remove"] != "project-remove" {
		t.Errorf("hooks[pre_remove] = %v, want the project's value to win", hooks["pre_remove"])
	}
}

// TestTerminalMergesLikeEveryOtherArchetypeTable is the ADR-0020 acceptance
// criterion for archetype merge: an archetype-supplied [terminal] reaches
// the merged manifest via mergeTable ([keys] is the one exception to this
// pattern), and a project's own key wins on conflict while a key only the
// archetype declares survives untouched — the same shape
// TestConfigTablesMergeProjectWinsOverArchetype proves for [beads]/
// [workflow] above.
func TestTerminalMergesLikeEveryOtherArchetypeTable(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "base", `
[terminal]
spawn  = "archetype-spawn"
attach = "archetype-attach"
kill   = "archetype-kill"
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["base"]

[terminal]
spawn = "project-spawn"
`)

	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if merged.Terminal["spawn"] != "project-spawn" {
		t.Errorf("Terminal[spawn] = %v, want the project's value to win", merged.Terminal["spawn"])
	}
	if merged.Terminal["attach"] != "archetype-attach" {
		t.Errorf("Terminal[attach] = %v, want the archetype's value to survive untouched", merged.Terminal["attach"])
	}
	if merged.Terminal["kill"] != "archetype-kill" {
		t.Errorf("Terminal[kill] = %v, want the archetype's value to survive untouched", merged.Terminal["kill"])
	}
}

// TestPopulateMergeAppendsAndDedupesByPattern is the append-in-list-order,
// de-dup-by-pattern rule this ticket's approach settles for [[populate]]: an
// archetype's entries come first, a project's own entries are appended after,
// and a project entry repeating an archetype's pattern replaces its mode
// rather than producing two rows for the same file.
func TestPopulateMergeAppendsAndDedupesByPattern(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "base", `
[[populate]]
pattern = ".env"
mode    = "copy"

[[populate]]
pattern = ".claude/skills/*"
mode    = "symlink"
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["base"]

[[populate]]
pattern = ".env"
mode    = "symlink"

[[populate]]
pattern = "admin.toml"
mode    = "copy"
`)

	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(merged.Populate) != 3 {
		t.Fatalf("Populate = %+v, want 3 entries (2 archetype + 1 new project, .env de-duped)", merged.Populate)
	}
	byPattern := map[string]string{}
	for _, e := range merged.Populate {
		byPattern[e["pattern"].(string)] = e["mode"].(string)
	}
	if byPattern[".env"] != "symlink" {
		t.Errorf("Populate[.env].mode = %q, want the project's override (symlink) to win", byPattern[".env"])
	}
	if byPattern[".claude/skills/*"] != "symlink" {
		t.Errorf("Populate[.claude/skills/*].mode = %q, want the archetype's entry to survive untouched", byPattern[".claude/skills/*"])
	}
	if byPattern["admin.toml"] != "copy" {
		t.Errorf("Populate[admin.toml].mode = %q, want the project's new entry", byPattern["admin.toml"])
	}
	// .env must appear exactly once in list order — first, since the
	// archetype declared it and the project's later entry replaces it in
	// place rather than appending a duplicate.
	if merged.Populate[0]["pattern"] != ".env" {
		t.Errorf("Populate[0] = %+v, want .env to stay in the archetype's original position", merged.Populate[0])
	}
}

// TestResolveMergesBaseImplicitly is admin-mb5q round 2's central claim: a
// project that declares NO archetypes at all still resolves every populate
// entry BaseName contributes, because Resolve merges it in before anything
// else, unasked.
func TestResolveMergesBaseImplicitly(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, BaseName, `
[[populate]]
pattern = ".env"
mode    = "copy"

[[populate]]
pattern = "admin.toml"
mode    = "copy"
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `project_name = "widget"`)

	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(merged.Populate) != 2 {
		t.Fatalf("Populate = %+v, want both of base's entries with no archetypes declared", merged.Populate)
	}
}

// TestResolveMissingBaseIsNotAnError covers LoadBase's other branch: a search
// path with no "base" slot at all (the common case before archetypes/ is
// ever deployed to ~/.admin/archetypes) must resolve cleanly, exactly as it
// did before base existed.
func TestResolveMissingBaseIsNotAnError(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	sp := DefaultSearchPath(home, repo)

	project := projectManifest(t, `project_name = "widget"`)

	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v, want a missing base archetype to be a silent no-op", err)
	}
	if len(merged.Populate) != 0 {
		t.Errorf("Populate = %+v, want none — no base and no project entries", merged.Populate)
	}
}

// TestResolveProjectPopulateWinsOverBase covers acceptance 11: a project's
// own [[populate]] entry repeating a pattern base already declares replaces
// base's mode rather than producing two rows for the same file.
func TestResolveProjectPopulateWinsOverBase(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, BaseName, `
[[populate]]
pattern = ".env"
mode    = "copy"
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"

[[populate]]
pattern = ".env"
mode    = "symlink"
`)

	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(merged.Populate) != 1 {
		t.Fatalf("Populate = %+v, want exactly one .env entry", merged.Populate)
	}
	if merged.Populate[0]["mode"] != "symlink" {
		t.Errorf("Populate[0].mode = %v, want the project's own entry to win over base's", merged.Populate[0]["mode"])
	}
}

// TestResolveDeclaredArchetypeOverridesBase covers acceptance 12: an
// archetype the project actually declares can still override a value base
// contributes, since base merges first and everything after it wins on
// collision.
func TestResolveDeclaredArchetypeOverridesBase(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, BaseName, `
[[populate]]
pattern = ".env"
mode    = "copy"
`, false)
	writeArchetypeDir(t, root, "custom", `
[[populate]]
pattern = ".env"
mode    = "symlink"
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["custom"]
`)

	merged, _, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(merged.Populate) != 1 || merged.Populate[0]["mode"] != "symlink" {
		t.Errorf("Populate = %+v, want the declared archetype's entry to win over base's", merged.Populate)
	}
}

// TestResolveBaseMergesBeforeDeclaredArchetypes proves the merge order
// directly on a non-list field: base merges first, so a declared archetype
// naming the same command wins on collision exactly like any other
// archetype-vs-archetype collision, and the merge log names base by its
// label ("archetype 'base'").
func TestResolveBaseMergesBeforeDeclaredArchetypes(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, BaseName, `
[commands.build]
desc  = "from base"
steps = ["step-base"]
`, false)
	writeArchetypeDir(t, root, "custom", `
[commands.build]
desc  = "from custom"
steps = ["step-custom"]
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}

	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["custom"]
`)

	merged, log, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := merged.Overrides["build"].Desc; got != "from custom" {
		t.Errorf("Overrides[build].Desc = %q, want %q (declared archetype must win over base)", got, "from custom")
	}
	found := false
	for _, line := range log {
		if strings.Contains(line, "archetype 'base'") && strings.Contains(line, "archetype 'custom'") {
			found = true
		}
	}
	if !found {
		t.Errorf("merge log = %v, want a line naming base overridden by custom", log)
	}
}

// A project's [status.prod] replaces an archetype's [status.prod] whole: the
// archetype's headers and every do not leak into the project's entry, and the
// collision is logged like any other named merge.
func TestProjectStatusReplacesArchetypeStatusWhole(t *testing.T) {
	root := t.TempDir()
	writeArchetypeDir(t, root, "a", `
[status.prod]
kind    = "json"
url     = "http://archetype/api"
headers = { X-From = "archetype" }
every   = "30s"
pull    = { n = "length(@)" }
show    = "{n}"

[status.dev]
kind = "http"
url  = "http://localhost:1"
`, false)
	sp := SearchPath{{Label: root, FS: os.DirFS(root)}}
	project := projectManifest(t, `
project_name = "widget"
archetypes   = ["a"]

[status.prod]
kind = "http"
url  = "http://project"
`)

	merged, log, err := Resolve(project, sp)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	prod := merged.Status["prod"]
	if prod.Kind != "http" || prod.URL != "http://project" || len(prod.Headers) != 0 || prod.Every != 0 {
		t.Errorf("Status[prod] = %+v, want the project's entry alone", prod)
	}
	if merged.Status["dev"].URL != "http://localhost:1" {
		t.Errorf("Status[dev] = %+v, want the archetype's entry kept", merged.Status["dev"])
	}
	found := false
	for _, line := range log {
		if strings.Contains(line, "status 'prod'") {
			found = true
		}
	}
	if !found {
		t.Errorf("merge log = %v, want a line naming the 'prod' status collision", log)
	}
}
