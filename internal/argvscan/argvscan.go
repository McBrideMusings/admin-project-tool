// Package argvscan holds the source scan behind each command package's
// TestArgvCoverage guard: it finds every exported function that reaches a
// command-running seam and diffs that set against the package's hand-kept
// coveredCommands map, so a new command can't ship without its argv pinned.
//
// The guard exists because #18 shipped a `gh pr list --state merged` with no
// `--repo`: gh resolved the repo from the working directory, so in a fork a
// live branch rendered struck through as merged, one keypress from deletion.
// The scan is the part with real logic in it — a bug in the fixed-point walk
// fails open, passing while covering nothing — so it lives in one place.
package argvscan

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Config describes one package's scan.
type Config struct {
	// Tool names the command the package wraps ("git", "gh", "bd"). It only
	// appears in failure messages.
	Tool string
	// Files are the source files to scan, relative to the package directory the
	// test runs in. Empty means every non-test .go file there — git's commands
	// span four files, and a command reached from any of them is the same
	// command.
	Files []string
	// Seams are the callee names that mean "this function runs a command",
	// e.g. "runner" and "mutator". A trailing "(" is optional and trimmed
	// before matching, so existing callers passing "runner(" still work.
	Seams []string
	// Covered is the package's coveredCommands map: every exported function it
	// claims has an argv case pinned.
	Covered map[string]bool
}

// fnInfo is what scan collects about one declared function.
type fnInfo struct {
	hitsSeam bool            // calls one of cfg.Seams directly
	calls    map[string]bool // callee names referenced anywhere in the decl
}

// Check fails t when the set of exported functions reaching a seam differs from
// cfg.Covered in either direction.
func Check(t testing.TB, cfg Config) {
	t.Helper()

	found := exported(t, cfg)
	if len(found) == 0 {
		t.Fatalf("found no exported functions reaching %s in %s — the scan is broken, not the package",
			strings.Join(cfg.Seams, "/"), cfg.sources())
	}
	for fn := range found {
		if !cfg.Covered[fn] {
			t.Errorf("%s runs a %s command but has no argv case in TestCommandArgv", fn, cfg.Tool)
		}
	}
	for fn := range cfg.Covered {
		if !found[fn] {
			t.Errorf("coveredCommands lists %s, which no longer runs a %s command in %s", fn, cfg.Tool, cfg.sources())
		}
	}
}

// exported returns every exported function in cfg's source that reaches one of
// cfg.Seams, directly or through an unexported helper.
func exported(t testing.TB, cfg Config) map[string]bool {
	t.Helper()

	fns := scan(t, cfg)

	// A function runs a command if it hits a seam itself or calls something that
	// does, so a command reached only through an unexported helper still counts.
	// gh's IssuePRStatuses reaches runner( only through issuePRStatusBatch.
	commands := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for fn, info := range fns {
			if commands[fn] {
				continue
			}
			runs := info.hitsSeam
			if !runs {
				for callee := range commands {
					if info.calls[callee] {
						runs = true
						break
					}
				}
			}
			if runs {
				commands[fn] = true
				changed = true
			}
		}
	}

	found := map[string]bool{}
	for fn := range commands {
		if fn[0] >= 'A' && fn[0] <= 'Z' { // unexported helpers count through their exported caller
			found[fn] = true
		}
	}
	return found
}

// scan parses cfg's source files and returns each declared function's
// seam/callee info keyed by name, merged across files.
func scan(t testing.TB, cfg Config) map[string]fnInfo {
	t.Helper()

	files := cfg.Files
	if len(files) == 0 {
		entries, err := os.ReadDir(".")
		if err != nil {
			t.Fatalf("read package dir: %v", err)
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			files = append(files, name)
		}
	}

	seams := map[string]bool{}
	for _, seam := range cfg.Seams {
		seams[strings.TrimSuffix(seam, "(")] = true
	}

	fset := token.NewFileSet()
	out := map[string]fnInfo{}
	for _, name := range files {
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			info, ok := out[fd.Name.Name]
			if !ok {
				info = fnInfo{calls: map[string]bool{}}
			}
			ast.Inspect(fd, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee := calleeName(call.Fun)
				if callee == "" {
					return true
				}
				if seams[callee] {
					info.hitsSeam = true
				}
				info.calls[callee] = true
				return true
			})
			out[fd.Name.Name] = info
		}
	}
	return out
}

// calleeName returns the identifier a call expression's function resolves
// to: the bare name for a plain call, the selector's field for a method or
// package-qualified call, and "" for anything else (e.g. a call through a
// parenthesized function literal).
func calleeName(expr ast.Expr) string {
	switch fn := expr.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	default:
		return ""
	}
}

// sources names the scanned source in a failure message.
func (c Config) sources() string {
	if len(c.Files) == 0 {
		return "this package"
	}
	return strings.Join(c.Files, ", ")
}
