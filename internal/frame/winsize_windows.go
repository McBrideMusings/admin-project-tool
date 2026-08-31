//go:build windows

package frame

// applyWinsize is a no-op on Windows: there is no TIOCSWINSZ/pty concept
// there, and no caller reaches this path yet anyway (ADR-0010 measured zero
// pty call sites in the whole interpreter). Kept as an explicit stub, the
// same pattern internal/luahost/apple_stub.go uses for darwin-only code, so
// `go build ./...` on Windows never trips over a missing symbol here —
// admin-3rgp.41 already tracks one Windows cross-compile break from an
// unguarded unix-only import, and this file exists so ApplyWinsize is not a
// second one.
func applyWinsize(fd uintptr, rows, cols int) {}
