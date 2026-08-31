//go:build !darwin

package luahost

import lua "github.com/yuin/gopher-lua"

// registerApple is a no-op on any platform other than darwin: xcodebuild,
// simctl, devicectl and codesign don't exist there, so a Lua body written
// against the `apple` table has no meaningful non-Mac behaviour to give it.
// A non-Apple archetype's Lua body never references `apple` at all, so
// leaving the global unset (rather than a table of functions that all
// error) is the honest shape — the same "undefined global" a body would get
// for typoing any other host function name.
func (h *Host) registerApple(L *lua.LState) {}
