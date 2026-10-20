package luahost

import (
	"github.com/McBrideMusings/admin-project-tool/internal/dockerrun"
	lua "github.com/yuin/gopher-lua"
)

// registerDockerRun binds internal/dockerrun's cfg<->argv renderers and
// drift/mask helpers into the `dockerrun` global table. The docker
// archetype's run-args, deploy, and diff actions all call through this
// table rather than each carrying its own copy of the field-table renderer
// — issue #388's lesson (the cfg->argv and inspect->argv directions drifted
// apart once already because each was hand-written separately) applies
// just as much to two Lua bodies implementing the same direction.
func (h *Host) registerDockerRun(L *lua.LState) {
	tbl := L.NewTable()

	// dockerrun.build_args_from_cfg(cfg[, env_vars[, env_file_vars]]) -> argv
	// env_vars/env_file_vars: an optional {NAME=VALUE} table. Omitting
	// env_vars means "read the admin process's own environment".
	tbl.RawSetString("build_args_from_cfg", L.NewFunction(func(L *lua.LState) int {
		cfg, _ := fromLuaValue(L.CheckTable(1)).(map[string]any)
		envVars := optionalStringMap(L, 2)
		envFileVars := optionalStringMap(L, 3)
		args := dockerrun.BuildArgsFromCfg(cfg, envVars, envFileVars, h.Warn)
		L.Push(stringSliceToLua(L, args))
		return 1
	}))

	// dockerrun.build_args_from_inspect(inspect) -> argv
	tbl.RawSetString("build_args_from_inspect", L.NewFunction(func(L *lua.LState) int {
		inspect, _ := fromLuaValue(L.CheckTable(1)).(map[string]any)
		args := dockerrun.BuildArgsFromInspect(inspect)
		L.Push(stringSliceToLua(L, args))
		return 1
	}))

	// dockerrun.compute_drift(target_argv, live_argv) -> in_target_only, in_live_only
	tbl.RawSetString("compute_drift", L.NewFunction(func(L *lua.LState) int {
		target := tableToStringSlice(L.CheckTable(1))
		live := tableToStringSlice(L.CheckTable(2))
		inTargetOnly, inLiveOnly := dockerrun.ComputeDrift(target, live)
		L.Push(stringSliceToLua(L, inTargetOnly))
		L.Push(stringSliceToLua(L, inLiveOnly))
		return 2
	}))

	// dockerrun.deploy_gate(live_only, snap_only, snap_live_only, have_snap) -> refuse, hand_only, hand_removed
	tbl.RawSetString("deploy_gate", L.NewFunction(func(L *lua.LState) int {
		liveOnly := tableToStringSlice(L.CheckTable(1))
		snapOnly := tableToStringSlice(L.CheckTable(2))
		snapLiveOnly := tableToStringSlice(L.CheckTable(3))
		haveSnap := lua.LVAsBool(L.Get(4))
		refuse, handOnly, handRemoved := dockerrun.DeployGate(liveOnly, snapOnly, snapLiveOnly, haveSnap)
		L.Push(lua.LBool(refuse))
		L.Push(stringSliceToLua(L, handOnly))
		L.Push(stringSliceToLua(L, handRemoved))
		return 3
	}))

	// dockerrun.format_drift_report(applying, hand_only, hand_removed[, refusing]) -> string
	tbl.RawSetString("format_drift_report", L.NewFunction(func(L *lua.LState) int {
		applying := tableToStringSlice(L.CheckTable(1))
		handOnly := tableToStringSlice(L.CheckTable(2))
		handRemoved := tableToStringSlice(L.CheckTable(3))
		refusing := true
		if b, ok := L.Get(4).(lua.LBool); ok {
			refusing = bool(b)
		}
		L.Push(lua.LString(dockerrun.FormatDriftReport(applying, handOnly, handRemoved, refusing)))
		return 1
	}))

	// dockerrun.live_argv_for_drift(inspect, image_env, image_labels, image_workdir, image_user, cfg) -> argv
	tbl.RawSetString("live_argv_for_drift", L.NewFunction(func(L *lua.LState) int {
		inspect, _ := fromLuaValue(L.CheckTable(1)).(map[string]any)
		var imageEnv []string
		if t, ok := L.Get(2).(*lua.LTable); ok {
			imageEnv = tableToStringSlice(t)
		}
		imageLabels := optionalStringMap(L, 3)
		imageWorkdir := L.OptString(4, "")
		imageUser := L.OptString(5, "")
		var cfg map[string]any
		if t, ok := L.Get(6).(*lua.LTable); ok {
			cfg, _ = fromLuaValue(t).(map[string]any)
		}
		args := dockerrun.LiveArgvForDrift(inspect, imageEnv, imageLabels, imageWorkdir, imageUser, cfg)
		L.Push(stringSliceToLua(L, args))
		return 1
	}))

	// dockerrun.mask_argv_for_display(argv) -> argv
	tbl.RawSetString("mask_argv_for_display", L.NewFunction(func(L *lua.LState) int {
		argv := tableToStringSlice(L.CheckTable(1))
		L.Push(stringSliceToLua(L, dockerrun.MaskArgvForDisplay(argv)))
		return 1
	}))

	// dockerrun.mask_env_assignment("NAME=VALUE") -> masked string
	tbl.RawSetString("mask_env_assignment", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(dockerrun.MaskEnvAssignment(L.CheckString(1))))
		return 1
	}))

	// dockerrun.is_secret_env_name(name) -> bool
	tbl.RawSetString("is_secret_env_name", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LBool(dockerrun.IsSecretEnvName(L.CheckString(1))))
		return 1
	}))

	// dockerrun.container_name(run_cfg, image) -> string
	// [docker_run].container_name when declared, else derived from image.
	tbl.RawSetString("container_name", L.NewFunction(func(L *lua.LState) int {
		cfg, _ := fromLuaValue(L.CheckTable(1)).(map[string]any)
		L.Push(lua.LString(dockerrun.ContainerName(cfg, L.CheckString(2))))
		return 1
	}))

	// dockerrun.restore_image_from_inspect(inspect, image) -> string
	tbl.RawSetString("restore_image_from_inspect", L.NewFunction(func(L *lua.LState) int {
		inspect, _ := fromLuaValue(L.CheckTable(1)).(map[string]any)
		L.Push(lua.LString(dockerrun.RestoreImageFromInspect(inspect, L.CheckString(2))))
		return 1
	}))

	// dockerrun.provision_commands(cfg[, env_vars]) -> {shell_cmd, ...}
	tbl.RawSetString("provision_commands", L.NewFunction(func(L *lua.LState) int {
		cfg, _ := fromLuaValue(L.CheckTable(1)).(map[string]any)
		envVars := optionalStringMap(L, 2)
		cmds := dockerrun.ProvisionCommands(cfg, envVars, h.Warn)
		L.Push(stringSliceToLua(L, cmds))
		return 1
	}))

	// dockerrun.health_check_shell(cfg[, env_vars]) -> string or nil
	tbl.RawSetString("health_check_shell", L.NewFunction(func(L *lua.LState) int {
		cfg, _ := fromLuaValue(L.CheckTable(1)).(map[string]any)
		envVars := optionalStringMap(L, 2)
		shell := dockerrun.HealthCheckShell(cfg, envVars, h.Warn)
		if shell == "" {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(shell))
		return 1
	}))

	// dockerrun.preflight_argv(cfg, target_argv, image) -> argv or nil, err
	tbl.RawSetString("preflight_argv", L.NewFunction(func(L *lua.LState) int {
		cfg, _ := fromLuaValue(L.CheckTable(1)).(map[string]any)
		argv := tableToStringSlice(L.CheckTable(2))
		parts, err := dockerrun.PreflightArgv(cfg, argv, L.CheckString(3))
		if err != nil {
			L.Push(lua.LNil)
			L.Push(lua.LString(err.Error()))
			return 2
		}
		if parts == nil {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(stringSliceToLua(L, parts))
		return 1
	}))

	// dockerrun.rollback_tag(image) -> string
	tbl.RawSetString("rollback_tag", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(dockerrun.RollbackTag(L.CheckString(1))))
		return 1
	}))

	// dockerrun.health_probe_shell(cfg[, env_vars]) -> string or nil
	tbl.RawSetString("health_probe_shell", L.NewFunction(func(L *lua.LState) int {
		cfg, _ := fromLuaValue(L.CheckTable(1)).(map[string]any)
		shell := dockerrun.HealthProbeShell(cfg, optionalStringMap(L, 2), h.Warn)
		if shell == "" {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(shell))
		return 1
	}))

	// dockerrun.mount_env_deps(cfg) -> {name, ...}
	tbl.RawSetString("mount_env_deps", L.NewFunction(func(L *lua.LState) int {
		cfg, _ := fromLuaValue(L.CheckTable(1)).(map[string]any)
		L.Push(stringSliceToLua(L, dockerrun.MountEnvDeps(cfg)))
		return 1
	}))

	// dockerrun.build_deploy_sync_plan(cfg, env_vars, is_local, dry_run) ->
	// plan (table with src/dest/target/is_remote/host/rsync_cmd/filter_args/
	// chown/post_sync_cmd) or nil, errors (array of strings)
	tbl.RawSetString("build_deploy_sync_plan", L.NewFunction(func(L *lua.LState) int {
		cfg, _ := fromLuaValue(L.CheckTable(1)).(map[string]any)
		envVars := optionalStringMap(L, 2)
		isLocal := lua.LVAsBool(L.Get(3))
		dryRun := lua.LVAsBool(L.Get(4))
		plan, errs := dockerrun.BuildDeploySyncPlan(cfg, envVars, isLocal, dryRun)
		if plan == nil {
			L.Push(lua.LNil)
			L.Push(stringSliceToLua(L, errs))
			return 2
		}
		out := L.NewTable()
		out.RawSetString("src", lua.LString(plan.Src))
		out.RawSetString("dest", lua.LString(plan.Dest))
		out.RawSetString("target", lua.LString(plan.Target))
		out.RawSetString("is_remote", lua.LBool(plan.IsRemote))
		out.RawSetString("host", lua.LString(plan.Host))
		out.RawSetString("rsync_cmd", lua.LString(plan.RsyncCmd))
		out.RawSetString("filter_args", lua.LString(plan.FilterArgs))
		out.RawSetString("chown", lua.LString(plan.Chown))
		out.RawSetString("post_sync_cmd", lua.LString(plan.PostSyncCmd))
		L.Push(out)
		L.Push(L.NewTable())
		return 2
	}))

	// dockerrun.build_command(build_cfg, opts) -> command or nil, err
	// opts: image, target, on_host, context, host, is_local. The one place a
	// `docker build` line is assembled (build and deploy both call it).
	tbl.RawSetString("build_command", L.NewFunction(func(L *lua.LState) int {
		cfg, _ := fromLuaValue(L.CheckTable(1)).(map[string]any)
		opts := L.CheckTable(2)
		str := func(k string) string { return lua.LVAsString(opts.RawGetString(k)) }
		cmd, err := dockerrun.BuildImageCommand(dockerrun.BuildParams{
			BuildCfg:    cfg,
			Target:      str("target"),
			Image:       str("image"),
			OnHost:      lua.LVAsBool(opts.RawGetString("on_host")),
			Context:     str("context"),
			Host:        str("host"),
			IsLocal:     lua.LVAsBool(opts.RawGetString("is_local")),
			ProjectRoot: h.ProjectRoot,
		})
		if err != nil {
			L.Push(lua.LNil)
			L.Push(lua.LString(err.Error()))
			return 2
		}
		L.Push(lua.LString(cmd))
		return 1
	}))

	// dockerrun.parse_env_text(text) -> {NAME = value, ...}
	tbl.RawSetString("parse_env_text", L.NewFunction(func(L *lua.LState) int {
		parsed := dockerrun.ParseEnvText(L.CheckString(1))
		out := L.NewTable()
		for k, v := range parsed {
			out.RawSetString(k, lua.LString(v))
		}
		L.Push(out)
		return 1
	}))

	L.SetGlobal("dockerrun", tbl)
}

// optionalStringMap reads argument idx as a Lua {NAME=VALUE} table into a Go
// map[string]string, or returns nil (meaning "not supplied") when the
// argument is absent/nil — the shape dockerrun's BuildArgsFromCfg and
// siblings use to mean "fall back to process env" vs. "use exactly this map".
func optionalStringMap(L *lua.LState, idx int) map[string]string {
	t, ok := L.Get(idx).(*lua.LTable)
	if !ok {
		return nil
	}
	return stringTableToMap(t)
}

func stringSliceToLua(L *lua.LState, s []string) *lua.LTable {
	tbl := L.NewTable()
	for i, v := range s {
		tbl.RawSetInt(i+1, lua.LString(v))
	}
	return tbl
}
