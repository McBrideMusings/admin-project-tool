package platform

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestSupportedPlatformsBuild cross-compiles the whole module for every pair
// in Supported. If this test fails, either Supported is wrong or a change
// introduced a platform-specific dependency that needs a build-tag split
// (see internal/reload, internal/frame's winsize_unix.go/winsize_windows.go,
// or internal/luahost's apple_stub.go for the pattern) — Supported is not
// the place to quietly narrow away a real regression.
//
// This test never cross-compiles for windows: ADR-0012 records that as a
// deliberate decision, not a gap this suite is meant to catch.
func TestSupportedPlatformsBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-compile smoke test skipped in -short mode")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}

	for _, p := range Supported {
		p := p
		t.Run(p.GOOS+"_"+p.GOARCH, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command("go", "build", "./...")
			cmd.Dir = "../.." // repo root: internal/platform -> ../..
			cmd.Env = append(os.Environ(),
				"GOOS="+p.GOOS,
				"GOARCH="+p.GOARCH,
				"CGO_ENABLED=0",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("GOOS=%s GOARCH=%s go build ./... failed:\n%s", p.GOOS, p.GOARCH, out)
			}
		})
	}
}

// TestSupportedExcludesWindows pins down the decision itself: Windows must
// never quietly reappear in Supported. If a real Windows target is ever
// added, this test — and ADR-0012 — need to change deliberately, not drift.
func TestSupportedExcludesWindows(t *testing.T) {
	for _, p := range Supported {
		if strings.EqualFold(p.GOOS, "windows") {
			t.Fatalf("Supported lists windows (%+v); ADR-0012 decided Windows is not a supported target", p)
		}
	}
}
