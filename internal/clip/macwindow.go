package clip

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// macWindowRecorder records one macOS window with `screencapture -v -x -o
// -l<id>`: the window's own pixels at its backing scale, without its shadow,
// whatever covers it. [clip].launch starts the app with ADMIN_CLIP=1 and
// ADMIN_CLIP_START set; the recorder finds the first window a new process
// owned by [clip.macos].owner opens, waits for its bounds to settle, starts
// recording, then touches ADMIN_CLIP_START (the demo's t0) and runs
// [clip].steps: `sh <command>` and `wait <seconds>`.
type macWindowRecorder struct{}

// window is one entry of the window server's list.
type window struct {
	ID     int     `json:"id"`
	PID    int     `json:"pid"`
	Owner  string  `json:"owner"`
	Name   string  `json:"name"`
	Bounds winRect `json:"b"`
}

type winRect struct {
	X, Y, Width, Height float64
}

// windowListJS prints every layer-0 window as JSON through the JXA ObjC
// bridge, so the binary needs no cgo.
const windowListJS = `ObjC.import('CoreGraphics');
function run() {
  var raw = $.CGWindowListCopyWindowInfo($.kCGWindowListOptionAll | $.kCGWindowListExcludeDesktopElements, $.kCGNullWindowID);
  var list = ObjC.deepUnwrap(ObjC.castRefToObject(raw)) || [];
  return JSON.stringify(list.filter(function (w) { return w.kCGWindowLayer === 0; }).map(function (w) {
    return {id: w.kCGWindowNumber, pid: w.kCGWindowOwnerPID, owner: w.kCGWindowOwnerName || '', name: w.kCGWindowName || '', b: w.kCGWindowBounds};
  }));
}`

func listWindows(ctx context.Context) ([]window, error) {
	out, err := exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e", windowListJS).Output()
	if err != nil {
		return nil, fmt.Errorf("list windows: %w", err)
	}
	var ws []window
	if err := json.Unmarshal(out, &ws); err != nil {
		return nil, fmt.Errorf("list windows: %w", err)
	}
	return ws, nil
}

func (macWindowRecorder) Record(ctx context.Context, c Config, dir string, log *Log) (Take, error) {
	m := c.MacOS
	startFile := filepath.Join(dir, "start")
	if err := os.Remove(startFile); err != nil && !os.IsNotExist(err) {
		return Take{}, err
	}
	before, err := listWindows(ctx)
	if err != nil {
		return Take{}, err
	}
	known := map[int]bool{}
	for _, w := range before {
		if w.Owner == m.Owner {
			known[w.PID] = true
		}
	}
	env := append(os.Environ(), "ADMIN_CLIP=1", "ADMIN_CLIP_START="+startFile)
	front := frontmostApp(ctx)
	log.Printf("macos: frontmost before launch: %s", front)
	defer func() { log.Printf("macos: frontmost after: %s (before: %s)", frontmostApp(ctx), front) }()

	// Quit every process that opened an owner window during this take,
	// whether or not the take succeeds, so no stray copy is mistaken for the
	// user's own instance next time.
	defer quitNew(m.Owner, known, log)
	if err := launchApp(ctx, c, env, dir, log); err != nil {
		return Take{}, err
	}

	win, err := awaitWindow(ctx, m, known, log)
	if err != nil {
		return Take{}, err
	}

	raw := filepath.Join(dir, "raw.mov")
	os.Remove(raw)
	capture := exec.Command("screencapture", "-v", "-x", "-o", "-l"+strconv.Itoa(win.ID), raw)
	log.Detailf("exec: screencapture -v -x -o -l%d %s", win.ID, raw)
	captureStart := time.Now()
	if err := capture.Start(); err != nil {
		return Take{}, fmt.Errorf("screencapture: %w", err)
	}
	stopped := false
	stop := func() error {
		if stopped {
			return nil
		}
		stopped = true
		capture.Process.Signal(syscall.SIGINT)
		return capture.Wait()
	}
	defer stop()

	// Give screencapture time to start before t0, so the take holds
	// frames before the demo begins.
	if err := sleepCtx(ctx, captureLead); err != nil {
		return Take{}, err
	}
	if err := os.WriteFile(startFile, nil, 0o644); err != nil {
		return Take{}, fmt.Errorf("touch %s: %w", startFile, err)
	}
	touched := time.Now()
	log.Printf("macos: t0: touched %s", startFile)

	for i := 0; i < c.Repeat; i++ {
		for _, s := range c.Steps {
			if err := runMacStep(ctx, c.Root, env, s, log); err != nil {
				return Take{}, err
			}
		}
	}
	end := touched.Add(seconds(c.Warmup + float64(c.Repeat)*c.Period + c.Fade + captureTail))
	if err := sleepCtx(ctx, time.Until(end)); err != nil {
		return Take{}, err
	}
	if err := stop(); err != nil {
		return Take{}, fmt.Errorf("screencapture: %w", err)
	}
	wall := time.Since(captureStart).Seconds()
	dur, err := probeDuration(ctx, log, dir, raw)
	if err != nil {
		return Take{}, err
	}
	t0, lag := takeT0(touched.Sub(captureStart).Seconds(), wall, dur)
	log.Printf("macos: take %.2fs over %.2fs wall, start lag %.2fs, t0 %.2fs", dur, wall, lag, t0)
	return Take{Path: raw, T0: t0}, nil
}

// takeT0 places t0 in the video. screencapture's first frame lags its
// launch, so the video is shorter than the wall time by that lag; t0 is the
// touch's offset from the launch minus the lag.
func takeT0(touchAfterLaunch, wall, videoSeconds float64) (t0, lag float64) {
	lag = wall - videoSeconds
	return touchAfterLaunch - lag, lag
}

// launchApp runs [clip].launch to completion, within [clip.macos].timeout.
// Its output goes to a file, not a pipe: an app the launch backgrounds keeps
// a pipe open and would hold the wait until it quit.
func launchApp(ctx context.Context, c Config, env []string, dir string, log *Log) error {
	log.Printf("macos: launch: %s", c.Launch)
	outPath := filepath.Join(dir, "launch.out")
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()
	lctx, cancel := context.WithTimeout(ctx, seconds(c.MacOS.Timeout))
	defer cancel()
	cmd := exec.CommandContext(lctx, "/bin/sh", "-c", c.Launch)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = c.Root, env, out, out
	err = cmd.Run()
	b, _ := os.ReadFile(outPath)
	log.raw(b)
	if err != nil {
		return fmt.Errorf("launch: %w\n%s", err, tail(string(b), 8))
	}
	return nil
}

const (
	captureLead = 1500 * time.Millisecond
	captureTail = 1.0
)

// awaitWindow polls until a process not in known, owned by m.Owner, shows a
// window matching m.Title at least minWindowWidth wide, then waits until its
// bounds hold still for m.Settle seconds.
func awaitWindow(ctx context.Context, m MacOSConfig, known map[int]bool, log *Log) (window, error) {
	deadline := time.Now().Add(seconds(m.Timeout))
	var cur window
	var since time.Time
	for {
		ws, err := listWindows(ctx)
		if err != nil {
			return window{}, err
		}
		found := pickWindow(ws, m, known)
		switch {
		case found == nil:
			cur, since = window{}, time.Time{}
		case found.ID != cur.ID || found.Bounds != cur.Bounds:
			cur, since = *found, time.Now()
			log.Printf("macos: window %d pid %d %q at %.0fx%.0f+%.0f+%.0f", cur.ID, cur.PID, cur.Name,
				cur.Bounds.Width, cur.Bounds.Height, cur.Bounds.X, cur.Bounds.Y)
		case time.Since(since) >= seconds(m.Settle):
			return cur, nil
		}
		if time.Now().After(deadline) {
			if cur.ID == 0 {
				hint := ""
				if m.Title != "" {
					hint = " (window titles read empty without Screen Recording permission for this terminal)"
				}
				return window{}, fmt.Errorf("no new %q window titled %q appeared within %.0fs%s", m.Owner, m.Title, m.Timeout, hint)
			}
			return window{}, fmt.Errorf("window %d never held still for %.1fs within %.0fs", cur.ID, m.Settle, m.Timeout)
		}
		if err := sleepCtx(ctx, 200*time.Millisecond); err != nil {
			return window{}, err
		}
	}
}

const minWindowWidth = 200

// pickWindow returns the first window a process outside known, owned by
// m.Owner, shows with m.Title in its title and at least minWindowWidth wide.
func pickWindow(ws []window, m MacOSConfig, known map[int]bool) *window {
	for i, w := range ws {
		if w.Owner == m.Owner && !known[w.PID] && w.Bounds.Width >= minWindowWidth &&
			strings.Contains(w.Name, m.Title) {
			return &ws[i]
		}
	}
	return nil
}

// runMacStep runs one `sh <command>` or `wait <seconds>` step.
func runMacStep(ctx context.Context, root string, env []string, step string, log *Log) error {
	verb, arg, _ := strings.Cut(strings.TrimSpace(step), " ")
	log.Printf("macos: step: %s", step)
	switch verb {
	case "wait":
		s, err := strconv.ParseFloat(strings.TrimSpace(arg), 64)
		if err != nil || s < 0 {
			return fmt.Errorf("step %q: wait takes seconds", step)
		}
		return sleepCtx(ctx, seconds(s))
	case "sh":
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", arg)
		cmd.Dir, cmd.Env = root, env
		out, err := cmd.CombinedOutput()
		log.raw(out)
		if err != nil {
			return fmt.Errorf("step %q: %w", step, err)
		}
		return nil
	}
	return fmt.Errorf("step %q: the macos-window recorder takes `sh <command>` or `wait <seconds>`", step)
}

// frontmostApp names the app holding focus, so the log shows whether a
// recording took it.
func frontmostApp(ctx context.Context) string {
	asn, err := exec.CommandContext(ctx, "lsappinfo", "front").Output()
	if err != nil {
		return "unknown: " + err.Error()
	}
	out, err := exec.CommandContext(ctx, "lsappinfo", "info", "-only", "name", strings.TrimSpace(string(asn))).Output()
	if err != nil {
		return "unknown: " + err.Error()
	}
	_, name, _ := strings.Cut(strings.TrimSpace(string(out)), "=")
	return strings.Trim(name, `"`)
}

// quitNew sends SIGTERM to every process owning an owner window that was
// not in known before launch.
func quitNew(owner string, known map[int]bool, log *Log) {
	ws, err := listWindows(context.Background())
	if err != nil {
		log.Printf("macos: quit: %v", err)
		return
	}
	done := map[int]bool{}
	for _, w := range ws {
		if w.Owner != owner || known[w.PID] || done[w.PID] {
			continue
		}
		done[w.PID] = true
		p, err := os.FindProcess(w.PID)
		if err == nil {
			err = p.Signal(syscall.SIGTERM)
		}
		log.Printf("macos: quit pid %d (err=%v)", w.PID, err)
	}
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
