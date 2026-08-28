package argvscan

import "testing"

// Regression lock for #35: text inside a string literal or a comment must
// not register as a function declaration or as a call to a seam. The fixture
// in testdata/phantom.go contains a "\nfunc Phantom(" inside a raw string
// literal and a "runner(" mention inside a comment; the old text-scan
// registered both, the AST walk must register neither.

func TestScanIgnoresStringLiterals(t *testing.T) {
	cfg := Config{
		Tool:  "test",
		Files: []string{"testdata/phantom.go"},
		Seams: []string{"runner("},
	}
	fns := scan(t, cfg)
	if _, ok := fns["Phantom"]; ok {
		t.Errorf("scan registered Phantom, a function name that only appears inside a string literal")
	}
}

func TestScanIgnoresComments(t *testing.T) {
	cfg := Config{
		Tool:  "test",
		Files: []string{"testdata/phantom.go"},
		Seams: []string{"runner("},
	}
	fns := scan(t, cfg)
	info, ok := fns["notASeam"]
	if !ok {
		t.Fatalf("scan did not find notASeam")
	}
	if info.hitsSeam {
		t.Errorf("notASeam registered as hitting the runner seam, but runner( only appears in a comment and a string literal")
	}
}
