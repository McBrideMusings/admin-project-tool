package kinds

import (
	"fmt"
	"os"

	"github.com/McBrideMusings/admin-project-tool/internal/electron"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/reload"
)

// makeElectron is kind="electron"'s factory. internal/electron is a complete
// port of admin_lib/electron.py — StageResources, BuildElectron,
// InstallElectron, DeployElectron, DevElectron — and its own package doc says
// it is "the real, callable, ready-to-wire port" waiting for exactly this
// file. Until it existed the package had zero callers, so an Electron project
// could neither build, deploy, nor dev its shell under the Go tool.
//
// The shape deliberately copies kind="apple" (apple.go): `run` names a verb,
// the top-level `[electron]` table is the config every verb reads. A project
// wiring three sub-targets writes three one-line actions, the same way the
// react-native archetype writes dev-ios/dev-device.
//
// Verbs: build (artifact only, nothing installed), deploy (build then
// atomically install into [electron].install_dir, default /Applications),
// dev (run unpackaged from source with main-process reload), stage (the
// resource-staging half of build, on its own).
func makeElectron(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	verb, ok := actionString(action, "run")
	if !ok || verb == "" {
		return nil, fmt.Errorf(`action %q: kind="electron" requires 'run' (build | deploy | dev | stage)`, name)
	}
	switch verb {
	case "build", "deploy", "dev", "stage":
	default:
		return nil, fmt.Errorf(
			`action %q: kind="electron" run=%q is not a verb (build | deploy | dev | stage)`, name, verb)
	}
	raw := archetypeConfigTable(m, "electron")

	return func(args []string) error {
		if len(raw) == 0 {
			return fmt.Errorf("action %q: [electron] table missing from admin.toml", name)
		}
		h := newHost(m)
		cfg, err := electronConfig(raw)
		if err != nil {
			return fmt.Errorf("action %q: [electron]: %w", name, err)
		}
		root, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}

		switch verb {
		case "stage":
			if err := electron.StageResources(cfg, root); err != nil {
				return fmt.Errorf("action %q: %w", name, err)
			}
			h.Ok("staged electron resources")
			return nil

		case "build":
			built, err := electron.BuildElectron(cfg, root)
			if err != nil {
				return fmt.Errorf("action %q: %w", name, err)
			}
			h.Ok("built " + built)
			return nil

		case "deploy":
			if err := electron.DeployElectron(cfg, root, h); err != nil {
				return fmt.Errorf("action %q: %w", name, err)
			}
			h.Ok("installed " + cfg.AppName + ".app")
			return nil

		default: // dev
			// DevElectron takes runWithReload injected rather than importing
			// internal/reload itself (its doc comment: "the real kind factory
			// that eventually wires this wires it to reload.RunWithReload").
			// This is that factory.
			rc, err := electron.DevElectron(cfg, root,
				electron.DevOptions{Info: h.Info},
				func(cmd, target string) int {
					return reload.RunWithReload(cmd, target, reload.Options{
						Dir:    h.ProjectRoot,
						Stdout: h.Stdout,
						LogFn:  h.AppendLog,
						Info:   h.Info,
						Warn:   h.Warn,
					})
				})
			if err != nil {
				return fmt.Errorf("action %q: %w", name, err)
			}
			if rc != 0 {
				return fmt.Errorf("action %q: electron exited %d", name, rc)
			}
			return nil
		}
	}, nil
}

// electronConfig maps the `[electron]` manifest table onto electron.Config.
// dev_env is the table that routinely carries a reference — term's
// CLIENT_URL = "${CLIENT_URL:-http://localhost:5173/}" — and DevElectron
// os.Setenv's those values straight into the child, so they have to be
// resolved here rather than left for a shell that never runs.
func electronConfig(raw map[string]any) (electron.Config, error) {
	rv := &envResolver{}
	devEnv := map[string]string{}
	for k, v := range cfgStringMap(cfgMap(raw, "dev_env")) {
		devEnv[k] = rv.s(v)
	}
	cfg := electron.Config{
		DesktopDir:        rv.s(cfgString(raw, "desktop_dir", "")),
		AppName:           rv.s(cfgString(raw, "app_name", "")),
		InstallDir:        rv.s(cfgString(raw, "install_dir", "")),
		OutDir:            rv.s(cfgString(raw, "out_dir", "")),
		WebSourceDir:      rv.s(cfgString(raw, "web_source_dir", "")),
		WebDistDirname:    rv.s(cfgString(raw, "web_dist_dirname", "")),
		ServerDistDir:     rv.s(cfgString(raw, "server_dist_dir", "")),
		ServerDistDirname: rv.s(cfgString(raw, "server_dist_dirname", "")),
		WatchFiles:        rv.all(cfgStrings(raw, "watch_files")),
		DevEnv:            devEnv,
		DevDistDirVar:     rv.s(cfgString(raw, "dev_dist_dir_var", "")),
	}
	return cfg, rv.firstErr
}
