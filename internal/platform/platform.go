// Package platform is the single source of truth for which OS/architecture
// pairs admin is a supported target on. See ADR-0012
// (docs/adr/0012-supported-platforms-darwin-and-linux-not-windows.md) for the
// decision and why.
//
// darwin is the real target: admin exists to drive Apple builds (xcodebuild,
// simctl, devicectl, codesign) and every archetype in daily use assumes it.
// linux currently builds cleanly and is kept working deliberately, but it is
// a "keep it working," not a platform anything was designed around.
//
// Windows is NOT a supported target. internal/data (unix.Flock),
// internal/ports (unix.SetsockoptInt, syscall.Kill), internal/electron and
// internal/metro all use POSIX-only syscalls with no build tag, so
// `GOOS=windows go build ./...` fails today and is expected to keep failing.
// That is a deliberate decision (admin-3rgp.41), not an oversight waiting on
// a `_windows.go` counterpart.
package platform

// Pair is one GOOS/GOARCH combination admin is expected to build and run on.
type Pair struct {
	GOOS   string
	GOARCH string
}

// Supported lists every GOOS/GOARCH pair TestSupportedPlatformsBuild
// cross-compiles on every test run. This slice is the check, not just the
// documentation of one: adding a platform here means it must actually build,
// and removing one is the only correct way to stop claiming it.
var Supported = []Pair{
	{GOOS: "darwin", GOARCH: "arm64"},
	{GOOS: "darwin", GOARCH: "amd64"},
	{GOOS: "linux", GOARCH: "amd64"},
	{GOOS: "linux", GOARCH: "arm64"},
}
