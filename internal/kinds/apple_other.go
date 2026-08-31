//go:build !darwin

package kinds

import "fmt"

// runAppleVerb on any non-darwin platform: xcodebuild, simctl, devicectl and
// codesign don't exist there, so every kind="apple" verb fails loudly and
// immediately rather than pretending to have done something. Matches
// internal/luahost's apple_stub.go — the same "this whole surface is
// darwin-only" boundary, drawn in the same place for the same reason.
func runAppleVerb(verb, action string, cfg map[string]any, args []string) error {
	return fmt.Errorf("action %q: kind=\"apple\" verb %q needs Xcode, simctl and devicectl — only runs on macOS", action, verb)
}
