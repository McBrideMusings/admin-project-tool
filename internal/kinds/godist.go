package kinds

import (
	"fmt"

	"github.com/McBrideMusings/admin-project-tool/internal/godist"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// makeGoDist is kind="go-dist"'s factory: cross-compile a Go binary for the
// os/arch matrix in `[go_dist]`. Like [launchd], that table was parsed into
// Manifest.Settings["go_dist"] and exposed to config bodies but read by no factory, so a
// project declaring it got nothing.
//
// `run` picks the operation: "build" (default) writes
// <out_dir>/<binary>-<os>-<arch> plus VERSION; "stage" copies an
// already-built matrix into a destination the action names in `dest` or the
// caller passes as the first CLI argument (an Electron server-dist, an iOS
// folder reference).
func makeGoDist(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	verb := orDefault(func() string { s, _ := actionString(action, "run"); return s }(), "build")
	if verb != "build" && verb != "stage" {
		return nil, fmt.Errorf(`action %q: kind="go-dist" run=%q is not a verb (build | stage)`, name, verb)
	}
	destDefault, _ := actionString(action, "dest")
	raw := archetypeConfigTable(m, "go_dist")

	return func(args []string) error {
		if len(raw) == 0 {
			return fmt.Errorf("action %q: [go_dist] table missing from admin.toml", name)
		}
		h := newHost(m)
		cfg, err := goDistConfig(raw)
		if err != nil {
			return fmt.Errorf("action %q: [go_dist]: %w", name, err)
		}

		if verb == "stage" {
			dest := destDefault
			if len(args) > 0 && args[0] != "" {
				dest = args[0]
			}
			if dest == "" {
				return fmt.Errorf("action %q: stage needs a destination — set 'dest' or pass one", name)
			}
			if _, err := godist.Stage(cfg, manifest.ResolveComputed(dest), h); err != nil {
				return fmt.Errorf("action %q: %w", name, err)
			}
			return nil
		}

		if _, err := godist.Build(cfg, h); err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		return nil
	}, nil
}

func goDistConfig(raw map[string]any) (godist.Config, error) {
	rv := &envResolver{}
	cfg := godist.Config{
		SourceDir:    rv.s(cfgString(raw, "source_dir", "")),
		OutDir:       rv.s(cfgString(raw, "out_dir", "")),
		Binary:       rv.s(cfgString(raw, "binary", "")),
		Targets:      rv.all(cfgStrings(raw, "targets")),
		Stamp:        cfgBool(raw, "stamp", true),
		VersionFlag:  rv.s(cfgString(raw, "version_flag", "")),
		ExtraLdflags: rv.s(cfgString(raw, "extra_ldflags", "")),
		Trimpath:     cfgBool(raw, "trimpath", true),
		CgoEnabled:   cfgString(raw, "cgo_enabled", ""),
	}
	return cfg, rv.firstErr
}
