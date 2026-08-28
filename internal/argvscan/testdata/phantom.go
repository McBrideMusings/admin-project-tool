// Package testdata is a fixture for argvscan's regression test. It is never
// built by the go tool — it lives under testdata/ — and exists only to be
// parsed directly by the test with go/parser.
package testdata

// notASeam mentions "runner(" inside a comment, which must not count as a
// call to the runner seam:
//
//	runner(
func notASeam() string {
	// This string literal contains what looks like a function declaration,
	// which must not register as a Phantom function or a call to runner:
	return "\nfunc Phantom(\n\treturn runner(\n"
}
