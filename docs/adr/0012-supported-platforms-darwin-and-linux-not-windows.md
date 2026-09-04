# ADR-0012: Supported platforms are darwin and linux; Windows is not a target

`admin` supports darwin and linux. Windows is not a supported target — no CI matrix entry, no doc claiming eventual support, no comment promising a future `_windows.go` counterpart for a package that doesn't have one. `internal/platform.Supported` names the GOOS/GOARCH pairs, and `TestSupportedPlatformsBuild` cross-compiles every one of them on every test run.

darwin is the real target — `admin` drives Apple builds (xcodebuild, simctl, devicectl, codesign) and every archetype in daily use assumes it; linux builds cleanly and is kept working, though nothing in the manifest/archetype/Lua model is Apple-specific.

`GOOS=windows go build ./...` is expected to fail on packages calling POSIX-only syscalls with no build tag (`internal/data`, `internal/ports`, `internal/electron`, `internal/metro`); that is not a bug to triage. `internal/reload`, `internal/keybindings`, `internal/logbridge` and `internal/frame` carry real `_unix.go`/`_windows.go` splits so `go build ./...` doesn't fail on a missing symbol from platform-agnostic code — their Windows stubs return an explicit "not supported" error, not a working implementation.
