package clip

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Options are per-run choices from the command line.
type Options struct {
	// NoPublish skips [clip].publish.
	NoPublish bool
	// Echo receives the progress lines also written to clip.log.
	Echo io.Writer
}

// Run records, cuts, encodes, checks and publishes one clip. Every artifact,
// clip.log and clip.json (the run's Result, written even on failure) land in
// c.Out.
func Run(ctx context.Context, c Config, o Options) (r Result, err error) {
	if err := os.MkdirAll(c.Out, 0o755); err != nil {
		return r, fmt.Errorf("out dir: %w", err)
	}
	log, err := OpenLog(filepath.Join(c.Out, "clip.log"), o.Echo)
	if err != nil {
		return r, err
	}
	defer log.Close()
	r.Recorder = c.Recorder
	defer func() {
		if err != nil {
			r.Error = err.Error()
			log.Printf("FAILED: %v", err)
		}
		if werr := writeResult(c.Out, r); werr != nil && err == nil {
			err = werr
		}
		log.Printf("state: %s", filepath.Join(c.Out, "clip.json"))
	}()

	log.Printf("start: recorder=%s period=%.2fs warmup=%.2fs repeat=%d out=%s", c.Recorder, c.Period, c.Warmup, c.Repeat, c.Out)
	take, err := lookupRecorder(c.Recorder).Record(ctx, c, c.Out, log)
	if err != nil {
		return r, fmt.Errorf("record: %w", err)
	}
	r.Take = take.Path
	if err := Cut(ctx, c, take, c.Out, log, &r); err != nil {
		return r, fmt.Errorf("cut: %w", err)
	}
	if err := Encode(ctx, c, c.Out, log, &r); err != nil {
		return r, fmt.Errorf("encode: %w", err)
	}
	log.Printf("clip: %s (%d bytes, %.2fs, %dx%d)", r.WebP, r.Bytes, r.Seconds, r.Width, r.Height)

	if c.Publish == "" || o.NoPublish {
		log.Printf("publish: skipped")
		return r, nil
	}
	log.Printf("publish: %s", c.Publish)
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", c.Publish)
	cmd.Dir = c.Root
	cmd.Env = append(os.Environ(), "ADMIN_CLIP_FILE="+r.WebP)
	out, perr := cmd.CombinedOutput()
	log.raw(out)
	if perr != nil {
		return r, fmt.Errorf("publish: %w\n%s", perr, tail(string(out), 8))
	}
	r.Published = true
	return r, nil
}

func writeResult(dir string, r Result) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "clip.json"), append(b, '\n'), 0o644)
}
