// Package detect ports admin_lib/detectors.py: file-layout heuristics that
// scan a project root and propose which archetypes apply. Unlike archetypes
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
	Archetypes  []string
	URLs        map[string]string
	ProjectName string
	// Tables holds every other key a detector returns, passed through as a
	// manifest table verbatim — e.g. Tables["apple"] = map[string]any{"project_path": "ios/App.xcodeproj"}.
	Tables map[string]any
}

// Detector inspects root and returns a Match, or nil if the stack doesn't
// match. A detector never errors: any I/O or parse failure while probing is
// itself a non-match, exactly like the Python originals swallowing
// OSError/ValueError.
type Detector func(root string) *Match

// Order is the run order from admin_lib.detectors.DETECTOR_ORDER, preserved
// exactly — it is not alphabetical and later entries win merge conflicts.
// "simple" is last and is handled as a fallback by Run rather than run in
// the loop, matching the Python loader.
//
// forge-engine has a detector (detectForgeEngine, registered in Registry)
// and a matching archetype directory, but is deliberately absent from Order:
// admin_lib.detectors.DETECTOR_ORDER never lists it either, so it never
// runs. tests/test_archetype_detector_pairing.py::test_detector_order_entries_have_backing_files
// documents that the detector<->order relationship is one-directional — every
// name in Order must have a detector file, but a detector file need not be
// in Order.
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
	// apple only matches an .xcodeproj/.xcworkspace at the project root, while
	// an RN app keeps its workspace down in <app_dir>/ios/ — but a repo with a
	// native Mac app at the root and an RN client in a subdir matches both.
	"react-native",
	// Orthogonal to every archetype above — a docs site rides along with
	// whatever the project actually is, so it contributes no competing
	// command names.
	"docs",
	"simple",
}

// Registry holds every detector by name, including forge-engine, which is
// present here but absent from Order (see the comment above Order).
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
	"forge-engine":       detectForgeEngine, // not in Order — see comment above Order
	"raylib-game":        detectRaylibGame,  // not in Order — no Python entry either; kept for parity with detectors/raylib-game.py
}

// Result is the merged outcome of a detection run: the union of every
// matched detector's contribution, plus provenance in Detector.
type Result struct {
	Archetypes  []string
	URLs        map[string]string
	ProjectName string
	Tables      map[string]any
	// Detector is "+".join(matched detector names) in Order, or "fallback"
	// when nothing in Order matched and Archetypes was substituted with
	// ["simple"].
	Detector string
}

// Propose runs every detector in Order (skipping "simple") against root and
// returns the raw, pre-fallback union: the archetypes actually matched, the
// tables/urls/project_name they contributed, and the list of detector names
// that matched, in Order. A project matching nothing returns a zero-length
// Archetypes slice and a zero-length matched slice — never an error.
func Propose(root string) (archetypes []string, urls map[string]string, projectName string, tables map[string]any, matched []string) {
	archetypes = []string{}
	urls = map[string]string{}
	tables = map[string]any{}
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
		for k, v := range m.URLs {
			urls[k] = v
		}
		if m.ProjectName != "" {
			projectName = m.ProjectName
		}
		for k, v := range m.Tables {
			tables[k] = v
		}
	}
	return archetypes, urls, projectName, tables, matched
}

// Run performs a full detection pass, applying the "simple" fallback when
// nothing else matched — the Go equivalent of admin_lib.detectors.detect().
// It never returns an error: a project matching no detector still yields a
// valid Result (archetypes ["simple"], Detector "fallback"), satisfying
// "empty proposal, not an error" while Propose exposes the true empty set
// beforehand.
func Run(root string) Result {
	archetypes, urls, projectName, tables, matched := Propose(root)

	if len(archetypes) == 0 {
		archetypes = []string{"simple"}
		matched = []string{"fallback"}
	}

	out := Result{
		Archetypes: archetypes,
		Tables:     tables,
		Detector:   joinPlus(matched),
	}
	if len(urls) > 0 {
		out.URLs = urls
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
