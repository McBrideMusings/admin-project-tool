package kinds

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/McBrideMusings/admin-project-tool/internal/clip"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// makeClip is kind="clip"'s factory: record a looping demo of the project's
// app and encode it as an animated WebP (internal/clip). The [clip] table
// holds the recorder, the launch command, the period and the choreography;
// `table` on the action names a different table, for a project with more
// than one clip.
//
//	[actions.clip]
//	kind = "clip"
//
// `--no-publish` skips [clip].publish for one run.
func makeClip(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	table, _ := actionString(action, "table")
	table = orDefault(table, "clip")

	return func(args []string) error {
		raw := archetypeConfigTable(m, table)
		if len(raw) == 0 {
			return fmt.Errorf("action %q: [%s] table missing from admin.toml", name, table)
		}
		rv := &envResolver{}
		c, err := clip.Parse(raw, rv.s)
		if err == nil {
			err = rv.firstErr
		}
		if err != nil {
			return fmt.Errorf("action %q: [%s]: %w", name, table, err)
		}
		root := projectRoot(m)
		c.Root = root
		if c.Out == "" {
			project := filepath.Base(root)
			if m != nil && m.ProjectName != "" {
				project = m.ProjectName
			}
			c.Out = filepath.Join(os.TempDir(), "admin-clip", project, table)
		} else {
			c.Out = resolveUnder(root, c.Out)
		}

		opts := clip.Options{Echo: os.Stderr}
		for _, a := range args {
			switch a {
			case "--no-publish":
				opts.NoPublish = true
			default:
				return fmt.Errorf("action %q: unknown argument %q (--no-publish)", name, a)
			}
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		r, err := clip.Run(ctx, c, opts)
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		fmt.Println(r.WebP)
		return nil
	}, nil
}
