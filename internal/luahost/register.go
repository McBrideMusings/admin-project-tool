package luahost

import (
	lua "github.com/yuin/gopher-lua"
)

// NewState returns a Lua VM with h's host API registered and the
// "config" global set to cfg (nil is fine — a body with no archetype config
// table just sees an empty table). Only Lua's base, table, string and math
// libraries are opened; os and io are deliberately left out — see this
// package's doc comment. The caller owns Close()ing the returned state.
func (h *Host) NewState(cfg map[string]any) *lua.LState {
	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	for _, pair := range []struct {
		n string
		f lua.LGFunction
	}{
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.StringLibName, lua.OpenString},
		{lua.MathLibName, lua.OpenMath},
	} {
		L.Push(L.NewFunction(pair.f))
		L.Push(lua.LString(pair.n))
		L.Call(1, 0)
	}

	h.registerOutput(L)
	h.registerLogContext(L)
	h.registerRunCmd(L)
	h.registerRunGroup(L)
	h.registerTargets(L)
	registerCapabilities(L)
	h.registerApple(L)
	h.registerMetro(L)
	h.registerDockerRun(L)

	if cfg == nil {
		cfg = map[string]any{}
	}
	L.SetGlobal("config", toLuaValue(L, cfg))

	return L
}

// Exec runs source (Lua source text) in a fresh state carrying cfg as the
// config global, then closes the state. This is the interpreter's dispatch
// entrypoint for kind="lua" — "a Lua body dispatched by the interpreter can
// call every host function in the documented surface and read its
// archetype config as a Lua table" (acceptance criterion). Wiring an actual
// "lua" kind into internal/kinds is admin-3rgp.35's job; Exec is what that
// kind's factory calls.
//
// args (variadic so every pre-existing 2-argument call site keeps compiling
// unchanged) becomes the `args` global — a Lua array of the CLI arguments
// the action was invoked with, exactly as the Python original's bare `args`
// name held sys.argv's tail. admin-3rgp.37 is the first caller that needs
// this: a body doing its own target-selection/flag-parsing (`./admin build
// windows`, `./admin dev --clean`) has to see what was typed.
func (h *Host) Exec(source string, cfg map[string]any, args ...string) error {
	L := h.NewState(cfg)
	defer L.Close()
	argsTbl := L.NewTable()
	for i, a := range args {
		argsTbl.RawSetInt(i+1, lua.LString(a))
	}
	L.SetGlobal("args", argsTbl)
	return L.DoString(source)
}

func (h *Host) registerOutput(L *lua.LState) {
	reg1 := func(name string, fn func(string)) {
		L.SetGlobal(name, L.NewFunction(func(L *lua.LState) int {
			fn(L.CheckString(1))
			return 0
		}))
	}
	reg1("info", h.Info)
	reg1("ok", h.Ok)
	reg1("warn", h.Warn)
	reg1("err", h.Err)
	reg1("print", h.Print)
}

func (h *Host) registerLogContext(L *lua.LState) {
	// set_log_context(...parts[, {retain=N}]) — every string argument is a
	// name part; a trailing table argument (if present) supplies retain.
	L.SetGlobal("set_log_context", L.NewFunction(func(L *lua.LState) int {
		n := L.GetTop()
		parts := make([]string, 0, n)
		retain := 0
		for i := 1; i <= n; i++ {
			v := L.Get(i)
			if tbl, ok := v.(*lua.LTable); ok && i == n {
				if r, ok := tbl.RawGetString("retain").(lua.LNumber); ok {
					retain = int(r)
				}
				continue
			}
			parts = append(parts, lua.LVAsString(v))
		}
		h.SetLogContext(parts, retain)
		return 0
	}))

	// set_log_context_path(path[, {retain=N}])
	L.SetGlobal("set_log_context_path", L.NewFunction(func(L *lua.LState) int {
		path := L.CheckString(1)
		retain := 0
		if tbl, ok := L.Get(2).(*lua.LTable); ok {
			if r, ok := tbl.RawGetString("retain").(lua.LNumber); ok {
				retain = int(r)
			}
		}
		h.SetLogContextPath(path, retain)
		return 0
	}))
}

func (h *Host) registerRunCmd(L *lua.LState) {
	// run_cmd(cmd[, {dir=path, capture_log=bool}]) -> ok, output
	L.SetGlobal("run_cmd", L.NewFunction(func(L *lua.LState) int {
		cmd := L.CheckString(1)
		opts := RunCmdOptions{CaptureLog: true}
		if tbl, ok := L.Get(2).(*lua.LTable); ok {
			if d, ok := tbl.RawGetString("dir").(lua.LString); ok {
				opts.Dir = string(d)
			}
			if c, ok := tbl.RawGetString("capture_log").(lua.LBool); ok {
				opts.CaptureLog = bool(c)
			}
		}
		res, err := h.RunCmd(cmd, opts)
		if err != nil {
			L.RaiseError("run_cmd: %v", err)
			return 0
		}
		L.Push(lua.LBool(res.Ok))
		L.Push(lua.LString(res.Output))
		return 2
	}))
}

// registerRunGroup binds run_group({{tag=, cmd=}, ...}[, {dir=}]) -> exit
// code (0 if every command exited zero) — the Lua entrypoint for
// Host.RunGroup, ADR-0010's named exception ("moves into the host") for the
// one archetype body that supervises several concurrent child processes with
// tagged, interleaved output.
func (h *Host) registerRunGroup(L *lua.LState) {
	L.SetGlobal("run_group", L.NewFunction(func(L *lua.LState) int {
		tbl := L.CheckTable(1)
		n := tbl.Len()
		cmds := make([]GroupCmd, 0, n)
		for i := 1; i <= n; i++ {
			row, ok := tbl.RawGetInt(i).(*lua.LTable)
			if !ok {
				continue
			}
			tag, _ := row.RawGetString("tag").(lua.LString)
			cmd, _ := row.RawGetString("cmd").(lua.LString)
			cmds = append(cmds, GroupCmd{Tag: string(tag), Cmd: string(cmd)})
		}
		dir := ""
		if opts, ok := L.Get(2).(*lua.LTable); ok {
			if d, ok := opts.RawGetString("dir").(lua.LString); ok {
				dir = string(d)
			}
		}
		code, err := h.RunGroup(cmds, dir)
		if err != nil {
			L.RaiseError("run_group: %v", err)
			return 0
		}
		L.Push(lua.LNumber(code))
		return 1
	}))
}

// entriesFromLuaTable reads a Lua array of {key=..., description=...} rows
// (pick_target/only_target's argument shape) into []TargetEntry, preserving
// array order — the reason the argument is an array and not a plain
// {key=description} map, which Lua (like the Go map it would decode to)
// does not order.
func entriesFromLuaTable(tbl *lua.LTable) []TargetEntry {
	n := tbl.Len()
	out := make([]TargetEntry, 0, n)
	for i := 1; i <= n; i++ {
		row, ok := tbl.RawGetInt(i).(*lua.LTable)
		if !ok {
			continue
		}
		key, _ := row.RawGetString("key").(lua.LString)
		desc, _ := row.RawGetString("description").(lua.LString)
		out = append(out, TargetEntry{Key: string(key), Description: string(desc)})
	}
	return out
}

func (h *Host) registerTargets(L *lua.LState) {
	// pick_target(entries[, prompt]) -> key or nil
	L.SetGlobal("pick_target", L.NewFunction(func(L *lua.LState) int {
		entries := entriesFromLuaTable(L.CheckTable(1))
		prompt := L.OptString(2, "Select target")
		key, ok := h.PickTarget(entries, prompt)
		if !ok {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(key))
		return 1
	}))

	// only_target(choices, cmd_name) -> key or nil
	L.SetGlobal("only_target", L.NewFunction(func(L *lua.LState) int {
		entries := entriesFromLuaTable(L.CheckTable(1))
		cmdName := L.CheckString(2)
		key, ok := h.OnlyTarget(entries, cmdName)
		if !ok {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(key))
		return 1
	}))
}

func registerCapabilities(L *lua.LState) {
	jsonTbl := L.NewTable()
	jsonTbl.RawSetString("encode", L.NewFunction(jsonEncode))
	jsonTbl.RawSetString("decode", L.NewFunction(jsonDecode))
	L.SetGlobal("json", jsonTbl)

	globTbl := L.NewTable()
	globTbl.RawSetString("match", L.NewFunction(globMatch))
	L.SetGlobal("glob", globTbl)

	plistTbl := L.NewTable()
	plistTbl.RawSetString("read", L.NewFunction(plistRead))
	L.SetGlobal("plist", plistTbl)

	fsTbl := L.NewTable()
	fsTbl.RawSetString("read", L.NewFunction(fsRead))
	L.SetGlobal("fs", fsTbl)

	L.SetGlobal("env", L.NewFunction(envGet))
}
