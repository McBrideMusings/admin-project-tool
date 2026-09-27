package luahost

import (
	"path/filepath"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/metro"
	lua "github.com/yuin/gopher-lua"
)

// registerMetro binds internal/metro (admin-3rgp.14's self-contained
// Metro-dev-server package) into the `metro` global table — the wiring
// that package's own doc comment says it's waiting on ("ready for
// whichever ticket wires a React Native dev loop to call it"). The
// react-native archetype (admin-3rgp.37) is that ticket: its dev/metro
// action bodies call through this table.
//
// metro.log_file/metro.pid_file default under h.LogDir ("<project>/tmp") —
// a project only overrides them by passing an explicit path.
func (h *Host) registerMetro(L *lua.LState) {
	tbl := L.NewTable()

	// metro.ensure(app_dir[, {port=, log_file=, pid_file=}]) -> started(bool)
	// Raises a Lua error on failure (Metro exited immediately, or never
	// answered /status in time) — must not continue toward a build that
	// would only show a red screen, using Lua's error-raising idiom rather
	// than a second (ok, err) return a caller could accidentally ignore.
	tbl.RawSetString("ensure", L.NewFunction(func(L *lua.LState) int {
		appDir := L.CheckString(1)
		port := metro.DefaultPort
		logFile := filepath.Join(h.LogDir, "metro.log")
		pidFile := filepath.Join(h.LogDir, "metro.pid")
		if opts, ok := L.Get(2).(*lua.LTable); ok {
			if p, ok := opts.RawGetString("port").(lua.LNumber); ok {
				port = int(p)
			}
			if lf, ok := opts.RawGetString("log_file").(lua.LString); ok {
				logFile = string(lf)
			}
			if pf, ok := opts.RawGetString("pid_file").(lua.LString); ok {
				pidFile = string(pf)
			}
		}
		started, err := metro.Ensure(appDir, port, logFile, pidFile, 60*time.Second)
		if err != nil {
			L.RaiseError("metro.ensure: %v", err)
			return 0
		}
		if started {
			h.Info("Metro log → " + logFile)
		}
		L.Push(lua.LBool(started))
		return 1
	}))

	// metro.stop([{port=, pid_file=}]) -> stopped(bool)
	tbl.RawSetString("stop", L.NewFunction(func(L *lua.LState) int {
		port := metro.DefaultPort
		pidFile := filepath.Join(h.LogDir, "metro.pid")
		if opts, ok := L.Get(1).(*lua.LTable); ok {
			if p, ok := opts.RawGetString("port").(lua.LNumber); ok {
				port = int(p)
			}
			if pf, ok := opts.RawGetString("pid_file").(lua.LString); ok {
				pidFile = string(pf)
			}
		}
		L.Push(lua.LBool(metro.Stop(pidFile, port)))
		return 1
	}))

	// metro.running([port]) -> bool
	tbl.RawSetString("running", L.NewFunction(func(L *lua.LState) int {
		port := L.OptInt(1, metro.DefaultPort)
		L.Push(lua.LBool(metro.Running(port)))
		return 1
	}))

	L.SetGlobal("metro", tbl)
}
