// Package detect holds file-layout heuristics that scan a project root and
// propose which archetypes apply. Unlike archetypes
// (data, merged by internal/archetype), detectors are logic — Go functions,
// one per archetype name, registered in a run order that decides precedence
// when more than one matches.
//
// Detectors only run during bootstrap; once admin.toml exists it is the sole
// source of truth (this package has no opinion about that — a later
// bootstrap port is the caller).
package detect

// Match is one detector's contribution: the archetype(s) it proposes plus
// any partial manifest data it found while looking (a react-native detector
// that located package.json can seed [react_native].app_dir, for instance).
// A detector returns nil when the project doesn't match its stack.
type Match struct {
	Archetypes     []string
	ProjectName    string
	HasProjectName bool // distinguish "not set" from "set to empty string"
	// Tables holds every other key a detector returns, passed through as a
	// manifest table verbatim — e.g. Tables["apple"] = map[string]any{"project_path": "ios/App.xcodeproj"}.
	Tables map[string]map[string]string
}

// Detector inspects root and returns a Match, or nil if the stack doesn't
// match. A detector never errors: any I/O or parse failure while probing is
// itself treated as a non-match.
type Detector func(root string) *Match

// Order is the run order: it is not alphabetical and later entries win
// merge conflicts. "simple" is last and is handled as a fallback by Run
// rather than run in the loop.
var Order = []string{
	"hugo",
	"go-cli",
	"rust",
	"rust-tauri",
	"firefox-extension",
	"unraid-plugin",
	"docker",
	"cloudflare-workers",
	"apple",
	// After apple so that react-native wins the dev command if both match:
	// later archetypes win on command-name conflicts. The two rarely collide —
	// apple skips an Xcode project in an RN app's own native dir
	// (<app_dir>/ios/), which belongs to react-native — but a repo with a
	// native Mac app beside an RN client in a subdir matches both.
	"react-native",
	// The game engines come after every general-purpose stack so their
	// build/dev/test/clean win over a Dockerfile or wrangler.toml that rides
	// along in the same repo. The two never both match: detectRaylibGame
	// declines the forge engine repo itself.
	"raylib-game",
	"forge-engine",
	// Orthogonal to every archetype above — a docs site rides along with
	// whatever the project actually is, so it contributes no competing
	// command names.
	"docs",
	"simple",
}

// Registry holds every detector by name.
var Registry = map[string]Detector{
	"hugo":               detectHugo,
	"go-cli":             detectGoCLI,
	"rust":               detectRust,
	"rust-tauri":         detectRustTauri,
	"firefox-extension":  detectFirefoxExtension,
	"unraid-plugin":      detectUnraidPlugin,
	"docker":             detectDocker,
	"cloudflare-workers": detectCloudflareWorkers,
	"apple":              detectApple,
	"react-native":       detectReactNative,
	"docs":               detectDocs,
	"simple":             detectSimple,
	"raylib-game":        detectRaylibGame,
	"forge-engine":       detectForgeEngine,
}

// Result is the merged outcome of a detection run: the union of every
// matched detector's contribution, plus provenance in Detector.
type Result struct {
	Archetypes  []string
	ProjectName string
	Tables      map[string]map[string]string
	// Detector is "+".join(matched detector names) in Order, or "fallback"
	// when nothing in Order matched and Archetypes was substituted with
	// ["simple"].
	Detector string
}

// Propose runs every detector in Order (skipping "simple") against root and
// returns the raw, pre-fallback union: the archetypes actually matched, the
// tables/project_name they contributed, and the list of detector names
// that matched, in Order. A project matching nothing returns a zero-length
// Archetypes slice and a zero-length matched slice — never an error.
func Propose(root string) (archetypes []string, projectName string, tables map[string]map[string]string, matched []string) {
	archetypes = []string{}
	tables = map[string]map[string]string{}
	matched = []string{}

	for _, name := range Order {
		if name == "simple" {
			continue
		}
		det, ok := Registry[name]
		if !ok {
			continue
		}
		m := det(root)
		if m == nil {
			continue
		}
		matched = append(matched, name)
		for _, a := range m.Archetypes {
			if !contains(archetypes, a) {
				archetypes = append(archetypes, a)
			}
		}
		// project_name merges based on presence, not truthiness. Later
		// detectors can explicitly clear it by setting HasProjectName=true
		// with ProjectName="".
		if m.HasProjectName {
			projectName = m.ProjectName
		}
		for k, v := range m.Tables {
			tables[k] = v
		}
	}
	return archetypes, projectName, tables, matched
}

// Run performs a full detection pass, applying the "simple" fallback when
// nothing else matched. It never returns an error: a project matching no
// detector still yields a valid Result (archetypes ["simple"], Detector
// "fallback"), satisfying "empty proposal, not an error" while Propose
// exposes the true empty set beforehand.
func Run(root string) Result {
	archetypes, projectName, tables, matched := Propose(root)

	if len(archetypes) == 0 {
		archetypes = []string{"simple"}
		matched = []string{"fallback"}
	}

	out := Result{
		Archetypes: archetypes,
		Tables:     tables,
		Detector:   joinPlus(matched),
	}
	if projectName != "" {
		out.ProjectName = projectName
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func joinPlus(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "+"
		}
		out += p
	}
	return out
}
