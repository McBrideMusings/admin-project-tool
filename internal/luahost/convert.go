package luahost

import (
	"fmt"
	"sort"

	lua "github.com/yuin/gopher-lua"
)

// toLuaValue converts a Go value decoded from TOML or JSON (nil, bool,
// string, int64/float64, []any, map[string]any) into the equivalent Lua
// value. This is how an archetype config table (_APPLE_CONFIG and siblings)
// "reaches a body as a real Lua table, not a globals() lookup" — the
// acceptance criterion this function exists for.
//
// map[string]any keys are sorted before insertion so a table's #-length and
// ipairs order are deterministic across runs, even though Lua tables with
// string keys don't have a meaningful order of their own — this only
// matters for any caller that also iterates with pairs() and expects
// repeatable output (tests, golden-file docs).
func toLuaValue(L *lua.LState, v any) lua.LValue {
	switch val := v.(type) {
	case nil:
		return lua.LNil
	case bool:
		return lua.LBool(val)
	case string:
		return lua.LString(val)
	case int:
		return lua.LNumber(val)
	case int64:
		return lua.LNumber(val)
	case float64:
		return lua.LNumber(val)
	case []any:
		tbl := L.NewTable()
		for i, item := range val {
			tbl.RawSetInt(i+1, toLuaValue(L, item))
		}
		return tbl
	case []string:
		tbl := L.NewTable()
		for i, item := range val {
			tbl.RawSetInt(i+1, lua.LString(item))
		}
		return tbl
	case map[string]any:
		tbl := L.NewTable()
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			tbl.RawSetString(k, toLuaValue(L, val[k]))
		}
		return tbl
	default:
		// Anything else (a TOML datetime, an unrecognized numeric type) is
		// stringified rather than dropped silently — a body seeing "<a Go
		// %v>" is a debuggable surprise; a missing key is a silent one.
		return lua.LString(fmt.Sprintf("%v", val))
	}
}

// fromLuaValue converts a Lua value back into the same nil/bool/string/
// float64/[]any/map[string]any shape toLuaValue accepts, for host functions
// that hand a Lua table back to Go (e.g. the json capability's encode, or
// pick_target's targets argument).
func fromLuaValue(lv lua.LValue) any {
	switch v := lv.(type) {
	case *lua.LNilType:
		return nil
	case lua.LBool:
		return bool(v)
	case lua.LString:
		return string(v)
	case lua.LNumber:
		return float64(v)
	case *lua.LTable:
		// A table with a contiguous 1..N integer-keyed run and no string
		// keys is treated as an array; anything else is a map. This mirrors
		// how JSON (and TOML) distinguish the two shapes.
		maxN := v.Len()
		isArray := maxN > 0
		v.ForEach(func(k, _ lua.LValue) {
			if _, ok := k.(lua.LNumber); !ok {
				isArray = false
			}
		})
		if isArray {
			out := make([]any, 0, maxN)
			for i := 1; i <= maxN; i++ {
				out = append(out, fromLuaValue(v.RawGetInt(i)))
			}
			return out
		}
		out := map[string]any{}
		v.ForEach(func(k, val lua.LValue) {
			out[k.String()] = fromLuaValue(val)
		})
		return out
	default:
		return lv.String()
	}
}

// tableToStringSlice reads a Lua array-of-strings argument (used by
// set_log_context's variadic parts and pick_target's key lists) into a Go
// []string, skipping non-string entries rather than erroring — a stray
// number or nil in a hand-written manifest table shouldn't crash the whole
// action.
func tableToStringSlice(tbl *lua.LTable) []string {
	out := make([]string, 0, tbl.Len())
	for i := 1; i <= tbl.Len(); i++ {
		if s, ok := tbl.RawGetInt(i).(lua.LString); ok {
			out = append(out, string(s))
		}
	}
	return out
}
