//go:build darwin

package luahost

import (
	"errors"

	lua "github.com/yuin/gopher-lua"
)

// registerApple binds the `apple` table documented in
// docs/architecture/lua-host-api.md's "Apple platform verbs" section — the
// darwin half of the registerApple(h, L) call every NewState makes
// (register.go). apple_stub.go supplies the !darwin no-op.
func (h *Host) registerApple(L *lua.LState) {
	tbl := L.NewTable()

	set := func(name string, fn lua.LGFunction) { tbl.RawSetString(name, L.NewFunction(fn)) }

	set("sim_platforms", func(L *lua.LState) int {
		out := L.NewTable()
		for i, k := range simPlatformKeys() {
			out.RawSetInt(i+1, lua.LString(k))
		}
		L.Push(out)
		return 1
	})
	set("device_platforms", func(L *lua.LState) int {
		out := L.NewTable()
		for i, k := range devicePlatformKeys() {
			out.RawSetInt(i+1, lua.LString(k))
		}
		L.Push(out)
		return 1
	})

	set("log_predicate", func(L *lua.LState) int {
		process := L.CheckString(1)
		subsystem := L.CheckString(2)
		level := L.OptString(3, "app")
		pred, err := LogPredicate(process, subsystem, level)
		if err != nil {
			L.RaiseError("apple.log_predicate: %v", err)
			return 0
		}
		L.Push(lua.LString(pred))
		return 1
	})

	set("xcode_is_signed_out", func(L *lua.LState) int {
		L.Push(lua.LBool(XcodeIsSignedOut(L.CheckString(1))))
		return 1
	})

	set("app_store_connect_key_rejected", func(L *lua.LState) int {
		L.Push(lua.LBool(AppStoreConnectKeyRejected(L.CheckString(1))))
		return 1
	})

	set("diagnose_build_failure", func(L *lua.LState) int {
		for _, line := range DiagnoseBuildFailure(L.CheckString(1)) {
			h.Warn(line)
		}
		return 0
	})

	set("app_store_connect_auth_flags", func(L *lua.LState) int {
		out := L.NewTable()
		for i, f := range AppStoreConnectAuthFlags() {
			out.RawSetInt(i+1, lua.LString(f))
		}
		L.Push(out)
		return 1
	})

	set("find_app", func(L *lua.LState) int {
		path, err := FindApp(L.CheckString(1))
		if err != nil {
			L.RaiseError("apple.find_app: %v", err)
			return 0
		}
		if path == "" {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(path))
		return 1
	})

	set("clean_derived_data", func(L *lua.LState) int {
		if err := CleanDerivedData(L.CheckString(1)); err != nil {
			L.RaiseError("apple.clean_derived_data: %v", err)
		}
		return 0
	})

	set("resolve_signing_identity", func(L *lua.LState) int {
		selector := L.OptString(1, "")
		identity, warnLines := ResolveSigningIdentity(selector)
		for _, w := range warnLines {
			h.Warn(w)
		}
		if identity == "" {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(identity))
		return 1
	})

	set("sign", func(L *lua.LState) int {
		path := L.CheckString(1)
		opts := SignOptions{Deep: true}
		if t, ok := L.Get(2).(*lua.LTable); ok {
			if s, ok := t.RawGetString("identity").(lua.LString); ok {
				opts.Identity = string(s)
			}
			if s, ok := t.RawGetString("identifier").(lua.LString); ok {
				opts.Identifier = string(s)
			}
			if b, ok := t.RawGetString("deep").(lua.LBool); ok {
				opts.Deep, opts.DeepSet = bool(b), true
			}
		}
		res, err := Sign(path, opts)
		if err != nil {
			h.Err(err.Error())
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(res.Identity))
		return 1
	})

	set("dev_macos_postprocess", func(L *lua.LState) int {
		built := L.CheckString(1)
		productName := L.CheckString(2)
		displayName := L.CheckString(3)
		signIdentity := L.OptString(4, "-")
		dest, err := DevMacOSPostprocess(built, productName, displayName, signIdentity)
		var pw *postprocessWarning
		if errors.As(err, &pw) {
			for _, line := range pw.lines {
				h.Warn(line)
			}
			err = nil
		}
		if err != nil {
			L.RaiseError("apple.dev_macos_postprocess: %v", err)
			return 0
		}
		L.Push(lua.LString(dest))
		return 1
	})

	set("target_build_dir", xcodebuildOptsFunc(func(opts XcodebuildOptions) (lua.LValue, error) {
		dir, err := TargetBuildDir(opts)
		if err != nil || dir == "" {
			return lua.LNil, err
		}
		return lua.LString(dir), nil
	}))

	set("xcodebuild", func(L *lua.LState) int {
		opts := xcodebuildOptionsFromTable(L.CheckTable(1))
		res, err := h.XcodebuildFiltered(opts)
		if err != nil {
			L.RaiseError("apple.xcodebuild: %v", err)
			return 0
		}
		for _, w := range res.Warnings {
			h.Warn(w)
		}
		L.Push(lua.LBool(res.Ok))
		L.Push(lua.LString(res.Output))
		return 2
	})

	set("find_simulator", func(L *lua.LState) int {
		id, err := FindSimulator(L.CheckString(1))
		if err != nil {
			L.RaiseError("apple.find_simulator: %v", err)
			return 0
		}
		if id == "" {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(id))
		return 1
	})

	set("resolve_simulator", func(L *lua.LState) int {
		id, name, infoLine, err := ResolveSimulator(L.CheckString(1))
		if err != nil {
			h.Err(err.Error())
			L.Push(lua.LNil)
			return 1
		}
		h.Info(infoLine)
		L.Push(lua.LString(id))
		L.Push(lua.LString(name))
		return 2
	})

	set("boot_simulator", func(L *lua.LState) int {
		BootSimulator(L.CheckString(1))
		return 0
	})

	set("ensure_booted_simulator", func(L *lua.LState) int {
		id, name, infoLine, err := EnsureBootedSimulator(L.CheckString(1))
		if err != nil {
			h.Err(err.Error())
			L.Push(lua.LNil)
			return 1
		}
		h.Info(infoLine)
		L.Push(lua.LString(id))
		L.Push(lua.LString(name))
		return 2
	})

	set("install_and_launch_sim", func(L *lua.LState) int {
		deviceID := L.CheckString(1)
		appPath := L.CheckString(2)
		bundleID := L.CheckString(3)
		var launchArgs []string
		if t, ok := L.Get(4).(*lua.LTable); ok {
			launchArgs = tableToStringSlice(t)
		}
		InstallAndLaunchSim(deviceID, appPath, bundleID, launchArgs)
		return 0
	})

	set("terminate_sim_app", func(L *lua.LState) int {
		TerminateSimApp(L.CheckString(1), L.CheckString(2))
		return 0
	})

	set("is_sim_app_running", func(L *lua.LState) int {
		L.Push(lua.LBool(IsSimAppRunning(L.CheckString(1), L.CheckString(2))))
		return 1
	})

	set("app_bundle_identifier", func(L *lua.LState) int {
		id := AppBundleIdentifier(L.CheckString(1))
		if id == "" {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(id))
		return 1
	})

	set("find_connected_device", func(L *lua.LState) int {
		platform := L.CheckString(1)
		deviceType := L.OptString(2, "")
		id, name, udid, warnLines, err := h.FindConnectedDevice(platform, deviceType)
		for _, w := range warnLines {
			h.Warn(w)
		}
		if err != nil {
			h.Err(err.Error())
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(id))
		L.Push(lua.LString(name))
		L.Push(lua.LString(udid))
		return 3
	})

	set("install_on_device", func(L *lua.LState) int {
		err := InstallOnDevice(L.CheckString(1), L.CheckString(2))
		L.Push(lua.LBool(err == nil))
		return 1
	})

	set("launch_on_device", func(L *lua.LState) int {
		deviceID := L.CheckString(1)
		bundleID := L.CheckString(2)
		var env map[string]string
		if t, ok := L.Get(3).(*lua.LTable); ok {
			env = map[string]string{}
			t.ForEach(func(k, v lua.LValue) { env[k.String()] = lua.LVAsString(v) })
		}
		err := LaunchOnDevice(deviceID, bundleID, env)
		L.Push(lua.LBool(err == nil))
		return 1
	})

	set("terminate_device_app", func(L *lua.LState) int {
		h.TerminateDeviceApp(L.CheckString(1), L.CheckString(2))
		return 0
	})

	set("is_device_app_running", func(L *lua.LState) int {
		L.Push(lua.LBool(h.IsDeviceAppRunning(L.CheckString(1), L.CheckString(2))))
		return 1
	})

	L.SetGlobal("apple", tbl)
}

// xcodebuildOptionsFromTable reads the {project=, scheme=, config=,
// destination=, action=, extra_settings=, allow_provisioning=} shape shared
// by apple.target_build_dir and apple.xcodebuild.
func xcodebuildOptionsFromTable(t *lua.LTable) XcodebuildOptions {
	str := func(key string) string {
		if s, ok := t.RawGetString(key).(lua.LString); ok {
			return string(s)
		}
		return ""
	}
	opts := XcodebuildOptions{
		Project:       str("project"),
		Scheme:        str("scheme"),
		Config:        str("config"),
		Destination:   str("destination"),
		Action:        str("action"),
		ExtraSettings: str("extra_settings"),
	}
	if b, ok := t.RawGetString("allow_provisioning").(lua.LBool); ok {
		opts.AllowProvisioning, opts.AllowProvisioningSet = bool(b), true
	}
	return opts
}

// xcodebuildOptsFunc adapts a (XcodebuildOptions) -> (lua.LValue, error)
// function into a lua.LGFunction taking the shared options-table argument,
// so target_build_dir doesn't repeat xcodebuild's argument-table parsing.
func xcodebuildOptsFunc(fn func(XcodebuildOptions) (lua.LValue, error)) lua.LGFunction {
	return func(L *lua.LState) int {
		opts := xcodebuildOptionsFromTable(L.CheckTable(1))
		v, err := fn(opts)
		if err != nil {
			L.RaiseError("%v", err)
			return 0
		}
		L.Push(v)
		return 1
	}
}
