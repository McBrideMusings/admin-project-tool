package kinds

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
)

// The DEV-build chip: a red marker every dev build paints into the corner of
// every window it opens, so three simultaneously-running builds of the same app
// can be told apart at a glance.
//
// WHY THE TOOL OWNS IT
//
// apple-notepad hand-rolled this and commit 347d9c4 deleted it — the view file
// and both call sites — as a side effect of unrelated chrome work. Nobody
// noticed for two days, and on 2026-09-02 it cost real data: unisolated
// automation drove the SHARED dev app and overwrote two of the user's actual
// notes with test fixtures, with nothing on screen saying which app was which.
// A marker a project owns is a marker a refactor deletes, which is why this one
// belongs to the archetype.
//
// HOW IT SURVIVES A REFACTOR — two halves, each a removal rather than a guard:
//
//  1. The FILE. Both sources are rewritten from the embedded templates on every
//     apple verb (runAppleVerb calls materializeDevChip before dispatching), so
//     deleting or editing them is undone by the next build. They carry a
//     DO-NOT-EDIT header and show up as a git diff when they drift.
//  2. The CALL SITE. There isn't one. AdminDevChipBootstrap.m runs from an
//     Objective-C +load before main and reaches AdminDevChip by name, so no line
//     of app code references the chip and no refactor has a line to delete.
//
// WHAT THE PROJECT STILL OWNS, and why this is not "hand-copy a Swift file":
// which source directories the chip lands in. That is per-project structure —
// one app target's sources, or two, or a name the tool cannot guess — so it is
// declared in admin.toml, not assumed by the tool (ADR-0019). The tool supplies
// every byte of content; the manifest supplies only the destinations.
//
//	[apple]
//	dev_chip_dirs = ["Scratchpad", "ScratchpadIOS"]
//
// A directory already inside a target's sources (xcodegen globs a folder;
// Xcode's own file-system-synchronized groups do the same) needs nothing
// further. A hand-managed pbxproj needs the two files added to the target once.
//
// PRODUCTION DRAWS NOTHING. The gate is the runtime bundle identifier, checked
// inside the app — not a build setting and not this code — so a production build
// compiles both files and renders no pixels. That is deliberate: a marker that
// exists only in dev builds is one whose absence you cannot verify.
//
// REJECTED, and why:
//   - A build-setting plus conditional compilation (-D DEV_BUILD) leaves every
//     project writing its own chip. That is the status quo that failed.
//   - Writing the file only for dev builds, and deleting it after, the way the
//     dev-icon banner swap works. Source has to be in the target's build phase
//     to compile, so a transient file is either not compiled or churns the
//     pbxproj on every build.
//   - Shipping it inside a Swift package the project depends on. It would still
//     need a call site to reach the window, which is exactly what got deleted.

//go:embed resources/AdminDevChip.swift
var devChipSwift string

//go:embed resources/AdminDevChipBootstrap.m
var devChipBootstrap string

// devChipFiles maps each generated file's name to its content.
var devChipFiles = map[string]string{
	"AdminDevChip.swift":      devChipSwift,
	"AdminDevChipBootstrap.m": devChipBootstrap,
}

// materializeDevChip writes the chip sources into every directory
// apple.dev_chip_dirs names, rewriting any that has drifted from the template.
//
// A manifest that declares no directories gets nothing — the tool never assumes
// a project wants UI it did not ask for. A directory that does not exist is an
// error rather than a silent skip: the manifest named a destination, so a typo
// there means the chip is quietly absent from the build, which is the failure
// this whole file exists to prevent.
func materializeDevChip(cfg map[string]any, h *luahost.Host) error {
	dirs := cfgStrings(cfg, "dev_chip_dirs")
	if len(dirs) == 0 {
		return nil
	}
	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return fmt.Errorf(
				"apple.dev_chip_dirs names %q, which is not a directory — "+
					"it must be a source directory compiled into an app target, "+
					"relative to the project root", dir)
		}
		for name, want := range devChipFiles {
			path := filepath.Join(dir, name)
			if got, err := os.ReadFile(path); err == nil && string(got) == want {
				continue
			}
			if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
				return fmt.Errorf("writing dev-build chip to %s: %w", path, err)
			}
			h.Info("DEV chip: wrote " + path)
		}
	}
	return nil
}
