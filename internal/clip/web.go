package clip

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// webRecorder renders a web page frame by frame in headless Chrome, driven
// from Go over the DevTools protocol. [clip].launch starts the page's
// server with ADMIN_CLIP=1 and ADMIN_CLIP_PORT (a free port) in its
// environment; [clip.web].url, with $ADMIN_CLIP_PORT replaced, is opened at
// [clip.web] width × height CSS pixels and scale. The page must expose
// window.__clip = { period, seek(t) }: seek(t) draws the frame at t seconds
// and resolves once it is painted. Each frame is seek(i/30) and a
// screenshot, so the take is frame-exact, never real-time, and T0 is 0. No
// browser window is ever shown.
type webRecorder struct{}

// clipProbeJS reports window.__clip.period once the page exposes a seekable
// __clip, else 0.
const clipProbeJS = `(window.__clip && typeof window.__clip.seek === "function" && window.__clip.period > 0) ? window.__clip.period : 0`

func (webRecorder) Record(ctx context.Context, c Config, dir string, log *Log) (Take, error) {
	w := c.Web
	port, err := freePort()
	if err != nil {
		return Take{}, err
	}
	env := append(os.Environ(), "ADMIN_CLIP=1", "ADMIN_CLIP_PORT="+strconv.Itoa(port))
	url := expandPort(w.URL, port)

	front := frontmostApp(ctx)
	log.Printf("web: frontmost before: %s", front)
	defer func() { log.Printf("web: frontmost after: %s (before: %s)", frontmostApp(ctx), front) }()

	exited := make(chan error, 1)
	if c.Launch != "" {
		server, err := startServer(c, env, dir, log)
		if err != nil {
			return Take{}, err
		}
		go func() { exited <- server.Wait() }()
		defer stopServer(server, exited, log)
	}

	if err := awaitHTTP(ctx, url, time.Now().Add(seconds(w.Timeout)), exited, log); err != nil {
		return Take{}, err
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.WindowSize(w.Width, w.Height))
	if w.Browser != "" {
		opts = append(opts, chromedp.ExecPath(w.Browser))
	}
	actx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()
	bctx, _ := chromedp.NewContext(actx)
	// Cancel closes Chrome and waits for it to exit, so no browser outlives
	// the take.
	defer chromedp.Cancel(bctx)
	// Start the browser on bctx itself: chromedp binds Chrome's lifetime to
	// the context of the first Run, so a timeout context here would close it.
	if err := chromedp.Run(bctx); err != nil {
		return Take{}, fmt.Errorf("start Chrome: %w", err)
	}
	chromedp.ListenTarget(bctx, func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			var parts []string
			for _, a := range e.Args {
				parts = append(parts, strings.Trim(string(a.Value), `"`))
			}
			log.Detailf("web: console.%s %s", e.Type, strings.Join(parts, " "))
		case *runtime.EventExceptionThrown:
			log.Printf("web: page exception: %s", e.ExceptionDetails.Error())
		}
	})

	setup := chromedp.Tasks{
		emulation.SetDeviceMetricsOverride(int64(w.Width), int64(w.Height), w.Scale, false),
	}
	if w.Scheme != "" {
		setup = append(setup, emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{
			{Name: "prefers-color-scheme", Value: w.Scheme},
		}))
	}
	setup = append(setup, chromedp.Navigate(url))
	log.Printf("web: open %s at %dx%d scale %g", url, w.Width, w.Height, w.Scale)
	if err := runWithin(bctx, seconds(w.Timeout), setup); err != nil {
		return Take{}, fmt.Errorf("open %s: %w", url, err)
	}

	period, err := awaitClip(bctx, time.Now().Add(seconds(w.Timeout)))
	if err != nil {
		return Take{}, err
	}
	if math.Abs(period-c.Period) > 1e-3 {
		return Take{}, fmt.Errorf("the page's window.__clip.period is %gs but [clip].period is %gs", period, c.Period)
	}

	frames := filepath.Join(dir, "frames")
	if err := os.RemoveAll(frames); err != nil {
		return Take{}, err
	}
	if err := os.MkdirAll(frames, 0o755); err != nil {
		return Take{}, err
	}
	defer os.RemoveAll(frames)
	n := int(math.Ceil((c.Warmup+c.Period+c.Fade)*cutFPS)) + 1
	log.Printf("web: rendering %d frames by seek", n)
	start := time.Now()
	for i := 0; i < n; i++ {
		var png []byte
		seek := fmt.Sprintf("window.__clip.seek(%s)", strconv.FormatFloat(float64(i)/cutFPS, 'f', -1, 64))
		if err := runWithin(bctx, frameTimeout,
			chromedp.Evaluate(seek, nil, func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }),
			chromedp.CaptureScreenshot(&png),
		); err != nil {
			return Take{}, fmt.Errorf("frame %d: %w", i, err)
		}
		if err := os.WriteFile(filepath.Join(frames, fmt.Sprintf("f%05d.png", i)), png, 0o644); err != nil {
			return Take{}, err
		}
	}
	log.Printf("web: rendered %d frames in %s", n, time.Since(start).Round(time.Millisecond))

	raw := filepath.Join(dir, "raw.mp4")
	if err := run(ctx, log, dir, "ffmpeg", "-y", "-v", "error", "-framerate", strconv.Itoa(cutFPS),
		"-i", filepath.Join(frames, "f%05d.png"), "-c:v", "libx264", "-crf", "10", "-pix_fmt", "yuv420p", raw); err != nil {
		return Take{}, err
	}
	return Take{Path: raw}, nil
}

// frameTimeout bounds one seek and its screenshot.
const frameTimeout = 30 * time.Second

// runWithin runs actions on the browser, giving up after d.
func runWithin(bctx context.Context, d time.Duration, actions ...chromedp.Action) error {
	tctx, cancel := context.WithTimeout(bctx, d)
	defer cancel()
	return chromedp.Run(tctx, actions...)
}

// awaitClip polls the page until it exposes a seekable window.__clip and
// returns its period.
func awaitClip(ctx context.Context, deadline time.Time) (float64, error) {
	for {
		var period float64
		if err := runWithin(ctx, frameTimeout, chromedp.Evaluate(clipProbeJS, &period)); err != nil {
			return 0, fmt.Errorf("probe window.__clip: %w", err)
		}
		if period > 0 {
			return period, nil
		}
		if time.Now().After(deadline) {
			return 0, errors.New("the page never exposed window.__clip = { period, seek(t) }; the web recorder renders only seekable pages")
		}
		if err := sleepCtx(ctx, 250*time.Millisecond); err != nil {
			return 0, err
		}
	}
}

// expandPort replaces $ADMIN_CLIP_PORT and ${ADMIN_CLIP_PORT} in s.
func expandPort(s string, port int) string {
	p := strconv.Itoa(port)
	return strings.ReplaceAll(strings.ReplaceAll(s, "${ADMIN_CLIP_PORT}", p), "$ADMIN_CLIP_PORT", p)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("find a free port: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// startServer runs [clip].launch in its own process group, with its output
// in <dir>/launch.out.
func startServer(c Config, env []string, dir string, log *Log) (*exec.Cmd, error) {
	out, err := os.Create(filepath.Join(dir, "launch.out"))
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("/bin/sh", "-c", c.Launch)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = c.Root, env, out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	log.Printf("web: launch: %s", c.Launch)
	if err := cmd.Start(); err != nil {
		out.Close()
		return nil, fmt.Errorf("launch: %w", err)
	}
	out.Close()
	return cmd, nil
}

// stopServer ends the launch's whole process group: SIGTERM, then SIGKILL
// after 5s.
func stopServer(cmd *exec.Cmd, exited chan error, log *Log) {
	pgid := cmd.Process.Pid
	err := syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		syscall.Kill(-pgid, syscall.SIGKILL)
		<-exited
	}
	log.Printf("web: stopped launch process group %d (err=%v)", pgid, err)
}

// awaitHTTP polls url until it answers below 500, the deadline passes, or
// the launch exits.
func awaitHTTP(ctx context.Context, url string, deadline time.Time, exited chan error, log *Log) error {
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				log.Printf("web: %s answered %d", url, resp.StatusCode)
				return nil
			}
		}
		select {
		case werr := <-exited:
			exited <- werr
			return fmt.Errorf("launch exited before %s answered (%v); see launch.out", url, werr)
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not answer before the timeout (last error: %v)", url, err)
		}
		if err := sleepCtx(ctx, 250*time.Millisecond); err != nil {
			return err
		}
	}
}
