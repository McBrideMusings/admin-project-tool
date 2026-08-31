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
		h := newHost()
		cfg := electronConfig(raw)
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
			if err := electron.DeployElectron(cfg, root); err != nil {
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
					return reload.RunWithReload(cmd, target, reload.Options{})
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
// Every string goes through manifest.ResolveComputed so `${CLIENT_URL:-...}`
// in dev_env means the same thing it does everywhere else in a manifest —
// dev_env is the one table in here that routinely carries a reference.
func electronConfig(raw map[string]any) electron.Config {
	devEnv := map[string]string{}
	for k, v := range cfgStringMap(cfgMap(raw, "dev_env")) {
		devEnv[k] = manifest.ResolveComputed(v)
	}
	watch := cfgStrings(raw, "watch_files")
	for i, w := range watch {
		watch[i] = manifest.ResolveComputed(w)
	}
	return electron.Config{
		DesktopDir:        manifest.ResolveComputed(cfgString(raw, "desktop_dir", "")),
		AppName:           manifest.ResolveComputed(cfgString(raw, "app_name", "")),
		InstallDir:        manifest.ResolveComputed(cfgString(raw, "install_dir", "")),
		OutDir:            manifest.ResolveComputed(cfgString(raw, "out_dir", "")),
		WebSourceDir:      manifest.ResolveComputed(cfgString(raw, "web_source_dir", "")),
		WebDistDirname:    manifest.ResolveComputed(cfgString(raw, "web_dist_dirname", "")),
		ServerDistDir:     manifest.ResolveComputed(cfgString(raw, "server_dist_dir", "")),
		ServerDistDirname: manifest.ResolveComputed(cfgString(raw, "server_dist_dirname", "")),
		WatchFiles:        watch,
		DevEnv:            devEnv,
		DevDistDirVar:     manifest.ResolveComputed(cfgString(raw, "dev_dist_dir_var", "")),
	}
}
