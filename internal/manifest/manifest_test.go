package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, Filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return dir
}

func TestParseFullManifest(t *testing.T) {
	dir := writeManifest(t, `
project_name = "widget-app"
python_min   = "3.12"
archetypes   = ["react-native", "docker"]
modules      = ["core", "docker"]
order        = ["build", "", "deploy", "---", "test"]
exclude_modules = ["extra"]
hide_commands   = ["secret"]
hide_urls       = ["internal"]

[urls]
local = "http://localhost:8787"
lan   = "http://${LAN_IP}:8787"

[commands.build]
desc     = "Build the project"
steps    = ["compile"]
group    = 1
priority = 10

[commands.custom.release]
desc  = "Full release"
steps = ["build", "deploy"]

[actions.compile]
kind = "shell"
run  = "make build"

[actions.deploy]
kind = "multi-target"

[actions.deploy.prod]
run = "make deploy-prod"

[guards.no-tag]
run  = "test -z \"$(git tag --points-at HEAD)\""
fail = "HEAD is already tagged"

[logs.api]
default_env = "dev"
dev  = "/var/log/api-dev.log"
prod = { path = "/var/log/api.log", host = "prod.example.com", user = "deploy" }

[logs.web]
kind = "docker"
container = "web"
highlight = ["ERROR", "WARN"]

[apple]
scheme = "MyApp"

[deploy]
src  = "./dist"
dest = "/srv/app"
`)

	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if m.ProjectName != "widget-app" {
		t.Errorf("ProjectName = %q, want widget-app", m.ProjectName)
	}
	if m.PythonMin != "3.12" {
		t.Errorf("PythonMin = %q, want 3.12", m.PythonMin)
	}
	wantArch := []string{"react-native", "docker"}
	if !equalSlices(m.Archetypes, wantArch) {
		t.Errorf("Archetypes = %v, want %v", m.Archetypes, wantArch)
	}
	wantOrder := []string{"build", Spacer, "deploy", Spacer, "test"}
	if !equalSlices(m.Order, wantOrder) {
		t.Errorf("Order = %v, want %v", m.Order, wantOrder)
	}
	if m.Urls["local"] != "http://localhost:8787" {
		t.Errorf("Urls[local] = %q", m.Urls["local"])
	}
	if m.Urls["lan"] != "http://${LAN_IP}:8787" {
		t.Errorf("Urls[lan] = %q, want raw ${LAN_IP} preserved", m.Urls["lan"])
	}

	build, ok := m.Overrides["build"]
	if !ok {
		t.Fatal("Overrides[build] missing")
	}
	if build.Desc != "Build the project" || !equalSlices(build.Steps, []string{"compile"}) {
		t.Errorf("Overrides[build] = %+v", build)
	}
	if build.Group != 1 || build.Priority != 10 {
		t.Errorf("Overrides[build] group/priority = %d/%d, want 1/10", build.Group, build.Priority)
	}

	release, ok := m.Custom["release"]
	if !ok {
		t.Fatal("Custom[release] missing")
	}
	if !equalSlices(release.Steps, []string{"build", "deploy"}) {
		t.Errorf("Custom[release].Steps = %v", release.Steps)
	}

	compile, ok := m.Actions["compile"]
	if !ok {
		t.Fatal("Actions[compile] missing")
	}
	if compile["run"] != "make build" || compile["kind"] != "shell" {
		t.Errorf("Actions[compile] = %v", compile)
	}

	deploy, ok := m.Actions["deploy"]
	if !ok {
		t.Fatal("Actions[deploy] missing")
	}
	prodTarget, ok := deploy["prod"].(map[string]any)
	if !ok || prodTarget["run"] != "make deploy-prod" {
		t.Errorf("Actions[deploy].prod = %v", deploy["prod"])
	}

	guard, ok := m.Guards["no-tag"]
	if !ok {
		t.Fatal("Guards[no-tag] missing")
	}
	if guard.Fail != "HEAD is already tagged" {
		t.Errorf("Guards[no-tag].Fail = %q", guard.Fail)
	}

	api, ok := m.Logs["api"]
	if !ok {
		t.Fatal("Logs[api] missing")
	}
	if api.Kind != "file" || !api.HasDefault || api.DefaultEnv != "dev" {
		t.Errorf("Logs[api] = %+v", api)
	}
	if api.Envs["dev"].Path != "/var/log/api-dev.log" {
		t.Errorf("Logs[api].Envs[dev] = %+v", api.Envs["dev"])
	}
	prodEnv := api.Envs["prod"]
	if prodEnv.Path != "/var/log/api.log" || prodEnv.Host != "prod.example.com" || prodEnv.User != "deploy" {
		t.Errorf("Logs[api].Envs[prod] = %+v", prodEnv)
	}

	web, ok := m.Logs["web"]
	if !ok {
		t.Fatal("Logs[web] missing")
	}
	if web.Kind != "docker" || web.Fields["container"] != "web" {
		t.Errorf("Logs[web] = %+v", web)
	}
	if !equalSlices(web.Highlight, []string{"ERROR", "WARN"}) {
		t.Errorf("Logs[web].Highlight = %v", web.Highlight)
	}

	if m.Apple["scheme"] != "MyApp" {
		t.Errorf("Apple[scheme] = %v", m.Apple["scheme"])
	}
	if m.Deploy["src"] != "./dist" || m.Deploy["dest"] != "/srv/app" {
		t.Errorf("Deploy = %v", m.Deploy)
	}
	if !equalSlices(m.ExcludeModules, []string{"extra"}) {
		t.Errorf("ExcludeModules = %v", m.ExcludeModules)
	}
}

func TestParsePartialAllowsMissingProjectName(t *testing.T) {
	data := map[string]any{
		"name": "docker",
		"commands": map[string]any{
			"build": map[string]any{
				"desc":  "Build the image",
				"steps": []any{"docker-build"},
			},
		},
	}

	m, err := ParsePartial(data, "docker/archetype.toml")
	if err != nil {
		t.Fatalf("ParsePartial: %v", err)
	}
	if m.ProjectName != "" {
		t.Errorf("ProjectName = %q, want empty for a partial manifest", m.ProjectName)
	}
	if len(m.Unknown) != 0 {
		t.Errorf("Unknown = %v, want none — 'name' must be a known top-level key", m.Unknown)
	}
	if _, ok := m.Overrides["build"]; !ok {
		t.Errorf("Overrides = %v, want 'build' parsed", m.Overrides)
	}

	// Parse still rejects the same data for lacking project_name.
	if _, err := Parse(data, "docker/archetype.toml"); err == nil {
		t.Fatal("Parse: want error for missing project_name, got nil")
	} else if !strings.Contains(err.Error(), "'project_name' is required") {
		t.Errorf("Parse error = %q, want to mention project_name", err.Error())
	}
}

func TestParsePreservesEnvPlaceholders(t *testing.T) {
	dir := writeManifest(t, `
project_name = "envtest"

[urls]
lan = "http://${LAN_IP}:9000"

[actions.build]
kind = "multi-target"
run  = "echo ${VERSION:-dev}"

[actions.build.mac]
run = "echo ${MAC_TARGET}"

[logs.app]
dev = "${LOG_DIR}/app.log"
`)

	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if m.Urls["lan"] != "http://${LAN_IP}:9000" {
		t.Errorf("Urls[lan] = %q, want ${LAN_IP} preserved", m.Urls["lan"])
	}

	build := m.Actions["build"]
	if build["run"] != "echo ${VERSION:-dev}" {
		t.Errorf("Actions[build].run = %v, want ${VERSION:-dev} preserved", build["run"])
	}
	mac, ok := build["mac"].(map[string]any)
	if !ok || mac["run"] != "echo ${MAC_TARGET}" {
		t.Errorf("Actions[build].mac.run = %v, want ${MAC_TARGET} preserved", build["mac"])
	}

	app := m.Logs["app"]
	if app.Envs["dev"].Path != "${LOG_DIR}/app.log" {
		t.Errorf("Logs[app].Envs[dev].Path = %q, want ${LOG_DIR}/app.log preserved", app.Envs["dev"].Path)
	}

	// Confirm the scanner actually recognizes the placeholders stored verbatim.
	refs := IterEnvRefs(build["run"].(string))
	if len(refs) != 1 || refs[0].Name != "VERSION" || refs[0].Default != "dev" {
		t.Errorf("IterEnvRefs(build.run) = %+v", refs)
	}
}

func TestParseMalformedNamesOffendingKey(t *testing.T) {
	cases := []struct {
		name       string
		toml       string
		wantSubstr string
	}{
		{
			name:       "archetypes not a list of strings",
			toml:       "project_name = \"x\"\narchetypes = \"x\"\n",
			wantSubstr: "'archetypes' must be a list of strings",
		},
		{
			name:       "command entry not a table",
			toml:       "project_name = \"x\"\n[commands]\nbuild = \"x\"\n",
			wantSubstr: "commands.build must be a table",
		},
		{
			name:       "action entry not a table",
			toml:       "project_name = \"x\"\n[actions]\nfoo = 3\n",
			wantSubstr: "actions.foo must be a table",
		},
		{
			name:       "log missing path",
			toml:       "project_name = \"x\"\n[logs.api]\ndev = { host = \"h\" }\n",
			wantSubstr: "logs.api.dev must have a 'path' string",
		},
		{
			name:       "unknown default_env",
			toml:       "project_name = \"x\"\n[logs.api]\ndefault_env = \"missing\"\ndev = \"/var/log/x.log\"\n",
			wantSubstr: "logs.api.default_env 'missing' not in envs",
		},
		{
			name:       "deploy unknown key",
			toml:       "project_name = \"x\"\n[deploy]\nsrc = \"a\"\ndest = \"b\"\nbogus = \"c\"\n",
			wantSubstr: "'[deploy]' has unknown key(s): bogus",
		},
		{
			name:       "deploy missing src",
			toml:       "project_name = \"x\"\n[deploy]\ndest = \"b\"\n",
			wantSubstr: "'[deploy].src' is required",
		},
		{
			name:       "deploy exclude not a list",
			toml:       "project_name = \"x\"\n[deploy]\nsrc = \"a\"\ndest = \"b\"\nexclude = \"a\"\n",
			wantSubstr: "'[deploy].exclude' must be a list",
		},
		{
			name:       "deploy delete not a bool",
			toml:       "project_name = \"x\"\n[deploy]\nsrc = \"a\"\ndest = \"b\"\ndelete = \"yes\"\n",
			wantSubstr: "'[deploy].delete' must be true or false",
		},
		{
			name:       "docs is not a validation site — [docs] must at least be a table",
			toml:       "project_name = \"x\"\ndocs = \"not-a-table\"\n",
			wantSubstr: "'[docs]' must be a table",
		},
		{
			name:       "invalid python_min",
			toml:       "project_name = \"x\"\npython_min = \"3\"\n",
			wantSubstr: "'python_min' must be MAJOR.MINOR",
		},
		{
			name:       "missing project_name",
			toml:       "archetypes = []\n",
			wantSubstr: "'project_name' is required",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := writeManifest(t, c.toml)
			_, err := Load(dir)
			if err == nil {
				t.Fatalf("Load(%s) succeeded, want error containing %q", c.name, c.wantSubstr)
			}
			if !contains(err.Error(), c.wantSubstr) {
				t.Errorf("Load(%s) error = %q, want substring %q", c.name, err.Error(), c.wantSubstr)
			}
		})
	}
}

func TestParseMalformedTOMLDoesNotPanic(t *testing.T) {
	dir := writeManifest(t, "project_name = \"x\n[unterminated\n")
	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load(malformed TOML) succeeded, want error")
	}
	if !contains(err.Error(), "invalid TOML") {
		t.Errorf("Load(malformed TOML) error = %q, want it to mention invalid TOML", err.Error())
	}
}

func TestLoadRepoOwnManifest(t *testing.T) {
	// admin-project-tool tracks its own admin.toml (unlike every other repo,
	// where it's gitignored — see CLAUDE.md), so it's a real fixture: walk up
	// from this test file's package directory to the repo root.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	repoRoot := filepath.Join(wd, "..", "..")
	path := filepath.Join(repoRoot, Filename)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no %s at repo root (%s): %v", Filename, path, err)
	}

	m, err := Load(repoRoot)
	if err != nil {
		t.Fatalf("Load(%s): %v", repoRoot, err)
	}
	if m.ProjectName == "" {
		t.Error("ProjectName is empty")
	}
	if len(m.Overrides) == 0 && len(m.Custom) == 0 {
		t.Error("expected at least one [commands.*] entry in the repo's own admin.toml")
	}
	if len(m.Actions) == 0 {
		t.Error("expected at least one [actions.*] entry in the repo's own admin.toml")
	}
}

// TestParseTrackerAndUsesPullRequests covers admin-3rgp.21's two new
// top-level manifest keys: declared, undeclared (Has* stays false so the
// Config tab can tell "false" from "never set"), and rejected wrong types.
// admin-j00d turned `tracker` from a bare string into a list — this test
// exercises the single-member and multi-member list forms, plus every
// rejected shape: a bare string, an empty list, an unknown member, and a
// duplicate member.
func TestParseTrackerAndUsesPullRequests(t *testing.T) {
	dir := writeManifest(t, `
project_name       = "widget-app"
tracker            = ["beads"]
uses_pull_requests = true
`)
	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !m.HasTracker || len(m.Trackers) != 1 || m.Trackers[0] != "beads" {
		t.Errorf("Trackers = %v, HasTracker = %v, want [\"beads\"], true", m.Trackers, m.HasTracker)
	}
	if !m.HasUsesPullRequests || !m.UsesPullRequests {
		t.Errorf("UsesPullRequests = %v, HasUsesPullRequests = %v, want true, true", m.UsesPullRequests, m.HasUsesPullRequests)
	}

	dir3 := writeManifest(t, `
project_name = "widget-app"
tracker      = ["beads", "github"]
`)
	m3, err := Load(dir3)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !m3.HasTracker || len(m3.Trackers) != 2 || m3.Trackers[0] != "beads" || m3.Trackers[1] != "github" {
		t.Errorf("Trackers = %v, want [\"beads\", \"github\"] in that order", m3.Trackers)
	}

	dir2 := writeManifest(t, `project_name = "widget-app"`)
	m2, err := Load(dir2)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m2.HasTracker {
		t.Error("HasTracker = true for a manifest that never declared 'tracker'")
	}
	if m2.HasUsesPullRequests {
		t.Error("HasUsesPullRequests = true for a manifest that never declared 'uses_pull_requests'")
	}

	cases := []struct {
		name       string
		toml       string
		wantSubstr string
	}{
		{"tracker bare string", "project_name = \"x\"\ntracker = \"beads\"\n", "'tracker' must be a list"},
		{"tracker not a string or list", "project_name = \"x\"\ntracker = 3\n", "'tracker' must be a list of strings"},
		{"tracker empty list", "project_name = \"x\"\ntracker = []\n", "'tracker' must not be an empty list"},
		{"tracker unknown member", "project_name = \"x\"\ntracker = [\"jira\"]\n", `unknown value "jira"`},
		{"tracker duplicate member", "project_name = \"x\"\ntracker = [\"beads\", \"beads\"]\n", `lists "beads" more than once`},
		{"uses_pull_requests not a bool", "project_name = \"x\"\nuses_pull_requests = \"yes\"\n", "'uses_pull_requests' must be true or false"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := writeManifest(t, c.toml)
			_, err := Load(dir)
			if err == nil {
				t.Fatalf("Load(%s) succeeded, want error", c.name)
			}
			if !contains(err.Error(), c.wantSubstr) {
				t.Errorf("Load(%s) error = %q, want substring %q", c.name, err.Error(), c.wantSubstr)
			}
		})
	}
}

// TestParseConfigTables covers the five config.Config tables becoming
// ordinary admin.toml keys: they parse, survive round-trip as raw maps, and
// [[populate]] decodes as an array of tables.
func TestParseConfigTables(t *testing.T) {
	dir := writeManifest(t, `
project_name = "widget-app"

[identity]
login  = "pierce"
emails = ["pierce@example.com"]

[worktrees]
root = "~/.worktrees"

[beads]
columns = ["id", "title"]

[workflow]
agent   = "claude"
ceiling = 3

[[populate]]
pattern = ".env"
mode    = "copy"

[[populate]]
pattern = ".claude/skills/*"
`)
	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Identity["login"] != "pierce" {
		t.Errorf("Identity[login] = %v, want \"pierce\"", m.Identity["login"])
	}
	if m.Worktrees["root"] != "~/.worktrees" {
		t.Errorf("Worktrees[root] = %v", m.Worktrees["root"])
	}
	if m.Beads["columns"] == nil {
		t.Error("Beads[columns] not parsed")
	}
	if m.Workflow["agent"] != "claude" {
		t.Errorf("Workflow[agent] = %v", m.Workflow["agent"])
	}
	if len(m.Populate) != 2 {
		t.Fatalf("Populate = %+v, want 2 entries", m.Populate)
	}
	if m.Populate[0]["pattern"] != ".env" || m.Populate[0]["mode"] != "copy" {
		t.Errorf("Populate[0] = %+v", m.Populate[0])
	}
	if m.Populate[1]["pattern"] != ".claude/skills/*" {
		t.Errorf("Populate[1] = %+v", m.Populate[1])
	}
	if len(m.Unknown) != 0 {
		t.Errorf("Unknown = %v, want none of the five config tables flagged unknown", m.Unknown)
	}
}

// TestParseConfigTablesRejectUnknownKeys is the acceptance criterion "admin
// check ... rejects an unknown one in the same way it rejects any other": an
// unrecognized key inside [identity]/[worktrees]/[beads]/[workflow] or a
// [[populate]] entry fails the same way an unknown [deploy] key already does.
func TestParseConfigTablesRejectUnknownKeys(t *testing.T) {
	cases := []struct {
		name       string
		toml       string
		wantSubstr string
	}{
		{"identity", "project_name = \"x\"\n[identity]\nnickname = \"p\"\n", "'[identity]' has unknown key(s): nickname"},
		{"worktrees", "project_name = \"x\"\n[worktrees]\npath = \"/tmp\"\n", "'[worktrees]' has unknown key(s): path"},
		{"beads", "project_name = \"x\"\n[beads]\nrows = 5\n", "'[beads]' has unknown key(s): rows"},
		{"workflow", "project_name = \"x\"\n[workflow]\nlimit = 5\n", "'[workflow]' has unknown key(s): limit"},
		{"populate entry", "project_name = \"x\"\n[[populate]]\nglob = \".env\"\n", "'[[populate]]' has unknown key(s): glob"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := writeManifest(t, c.toml)
			_, err := Load(dir)
			if err == nil {
				t.Fatalf("Load(%s) succeeded, want error", c.name)
			}
			if !contains(err.Error(), c.wantSubstr) {
				t.Errorf("Load(%s) error = %q, want substring %q", c.name, err.Error(), c.wantSubstr)
			}
		})
	}
}

// TestParseNoConfigTablesDeclaredIsEmpty is the "a project declaring none of
// them gets the existing code defaults, unchanged" acceptance criterion at
// the parse layer: nothing declared, nothing parsed, and none of the five
// keys is ever flagged unknown.
func TestParseNoConfigTablesDeclaredIsEmpty(t *testing.T) {
	dir := writeManifest(t, `project_name = "widget-app"`)
	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(m.Identity) != 0 || len(m.Worktrees) != 0 || len(m.Beads) != 0 || len(m.Workflow) != 0 || len(m.Populate) != 0 {
		t.Errorf("a manifest declaring none of the five config tables parsed non-empty: "+
			"identity=%v worktrees=%v beads=%v workflow=%v populate=%v",
			m.Identity, m.Worktrees, m.Beads, m.Workflow, m.Populate)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
