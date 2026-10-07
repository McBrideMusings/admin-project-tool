package clip

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseDefaultsAndErrors(t *testing.T) {
	c, err := Parse(map[string]any{"recorder": "terminal", "period": 4.8, "budget": "2MB",
		"steps": []any{"Down"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := Parse(map[string]any{"recorder": "terminal", "period": 4.8, "budget": int64(750000)}, nil); err != nil || b.Budget != 750000 {
		t.Errorf("integer budget: %d, %v", b.Budget, err)
	}
	if c.Budget != 2<<20 || c.Repeat != 2 || c.Width != 1280 || c.FPS != 15 || c.Fade != 0.5 {
		t.Errorf("defaults: %+v", c)
	}
	for name, raw := range map[string]map[string]any{
		"no recorder":      {"period": 5.0},
		"unknown recorder": {"recorder": "vr-headset", "period": 5.0},
		"no period":        {"recorder": "terminal"},
		"fade >= period":   {"recorder": "terminal", "period": 1.0, "fade": 1.0},
		"bad budget":       {"recorder": "terminal", "period": 5.0, "budget": "lots"},
		"string quality":   {"recorder": "terminal", "period": 5.0, "quality": "70"},
		"misspelled key":   {"recorder": "terminal", "period": 5.0, "seam_treshold": 1.0},
		"unknown sub key":  {"recorder": "terminal", "period": 5.0, "terminal": map[string]any{"colour": "red"}},
		"quality too low":  {"recorder": "terminal", "period": 5.0, "quality": int64(10)},
	} {
		if _, err := Parse(raw, nil); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"2MB": 2 << 20, "500kb": 500 << 10, "750000": 750000, "": 0} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}

// frames builds a take whose frame i has value pattern[i % len(pattern)].
func periodicFrames(n int, pattern []byte) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = []byte{pattern[i%len(pattern)], pattern[i%len(pattern)]}
	}
	return out
}

func TestFindSeamPicksTheMatchingStart(t *testing.T) {
	// Period of 10 frames at 10fps = 1s, but only frames whose value equals
	// the value 10 frames later match; break periodicity everywhere except
	// at frame 7 by making the take drift.
	frames := make([][]byte, 40)
	for i := range frames {
		frames[i] = []byte{byte(i * 3)}
	}
	frames[17] = frames[7]
	start, diff, err := FindSeam(frames, 10, 0.6, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	if start != 7 || diff != 0 {
		t.Errorf("start=%d diff=%.2f, want frame 7 and 0", start, diff)
	}
	if _, _, err := FindSeam(periodicFrames(12, []byte{1, 2}), 10, 0.5, 10, 5); err == nil {
		t.Error("a take shorter than period+fade past the window must error")
	}
}

func TestCutFilterCrossfadesOnlyWhenAsked(t *testing.T) {
	plain := CutFilter("", 1280, 15, 144, 0)
	if strings.Contains(plain, "blend") || !strings.Contains(plain, "trim=start_frame=15:end_frame=159") {
		t.Errorf("plain: %s", plain)
	}
	faded := CutFilter("100:100:0:0", 1280, 15, 144, 15)
	for _, want := range []string{"crop=100:100:0:0,", "[a]trim=start_frame=159:end_frame=174", "blend=", "[c]trim=start_frame=30:end_frame=159", "concat=n=2"} {
		if !strings.Contains(faded, want) {
			t.Errorf("faded filter lacks %q: %s", want, faded)
		}
	}
}

func TestTapeRepeatsStepsAfterReady(t *testing.T) {
	c := Config{Launch: "admin", Ready: "Commands", Repeat: 2, Steps: []string{"Down", "Sleep 1s"},
		Terminal: TerminalConfig{FontSize: 28, WidthPx: 1920, HeightPx: 1080}}
	tape := Tape(c, "/tmp/raw.mp4")
	show := strings.Index(tape, "Show")
	if show < strings.Index(tape, "Wait+Screen@30s /Commands/") || show < strings.Index(tape, `Type "admin"`) {
		t.Errorf("launch and ready must precede Show:\n%s", tape)
	}
	if n := strings.Count(tape[show:], "Down\n"); n != 2 {
		t.Errorf("steps ran %d times after Show, want 2:\n%s", n, tape)
	}
}

func testsrc(t *testing.T, dir string, loops int) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	ctx := context.Background()
	src := filepath.Join(dir, "src.mp4")
	if err := run(ctx, nil, dir, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30:duration=1",
		"-c:v", "libx264", "-g", "1", "-pix_fmt", "yuv420p", src); err != nil {
		t.Fatal(err)
	}
	take := filepath.Join(dir, "take.mp4")
	if err := run(ctx, nil, dir, "ffmpeg", "-v", "error", "-stream_loop", strconv.Itoa(loops-1), "-i", src, "-c", "copy", take); err != nil {
		t.Fatal(err)
	}
	return take
}

// TestCutPeriodicTake runs the real ffmpeg pipeline over a take made by
// looping a 1s source three times: the seam must match and the clip must be
// exactly one period of frames.
func TestCutPeriodicTake(t *testing.T) {
	dir := t.TempDir()
	c := Config{Period: 1, Warmup: 0.5, Fade: 0.3, SeamThreshold: 0.5, Width: 320}
	var r Result
	if err := Cut(context.Background(), c, Take{Path: testsrc(t, dir, 3)}, dir, nil, &r); err != nil {
		t.Fatal(err)
	}
	if r.SeamDiff > 0.5 || r.Crossfade != 0 {
		t.Errorf("seam diff %.2f crossfade %.2f on a periodic take", r.SeamDiff, r.Crossfade)
	}
	if r.Seconds != 1 || len(r.Stills) != 5 {
		t.Errorf("clip %.3fs with %d stills, want exactly 1s and 5", r.Seconds, len(r.Stills))
	}
}

// TestCutCrossfadesANonPeriodicTake cuts a 0.7s period out of a 1s-periodic
// take, so no start matches: the crossfade path must run through ffmpeg and
// still produce exactly one period of frames.
func TestCutCrossfadesANonPeriodicTake(t *testing.T) {
	dir := t.TempDir()
	c := Config{Period: 0.7, Warmup: 0.5, Fade: 0.2, SeamThreshold: 0.5, Width: 320}
	var r Result
	if err := Cut(context.Background(), c, Take{Path: testsrc(t, dir, 3)}, dir, nil, &r); err != nil {
		t.Fatal(err)
	}
	if r.Crossfade != 0.2 {
		t.Errorf("crossfade %.2f with seam diff %.2f, want 0.2", r.Crossfade, r.SeamDiff)
	}
	if r.Seconds != 0.7 {
		t.Errorf("clip %.3fs, want exactly 0.7s", r.Seconds)
	}
}

func TestPickWindowSkipsTheUsersOwnInstance(t *testing.T) {
	ws := []window{
		{ID: 1, PID: 10, Owner: "NTS Radio", Bounds: winRect{Width: 1040}}, // already running before launch
		{ID: 2, PID: 20, Owner: "NTS Radio", Bounds: winRect{Width: 40}},   // a sliver, not the main window
		{ID: 3, PID: 20, Owner: "NTS Radio", Name: "Radio", Bounds: winRect{Width: 1040}},
	}
	got := pickWindow(ws, MacOSConfig{Owner: "NTS Radio"}, map[int]bool{10: true})
	if got == nil || got.ID != 3 {
		t.Fatalf("picked %+v, want window 3", got)
	}
	if pickWindow(ws, MacOSConfig{Owner: "NTS Radio", Title: "Settings"}, map[int]bool{10: true}) != nil {
		t.Error("a title filter that matches nothing must pick nothing")
	}
}

func TestMacStepsAndOwnerRequired(t *testing.T) {
	ctx := context.Background()
	if err := runMacStep(ctx, t.TempDir(), nil, "wait 0", nil); err != nil {
		t.Errorf("wait 0: %v", err)
	}
	if err := runMacStep(ctx, t.TempDir(), nil, "sh exit 3", nil); err == nil {
		t.Error("a failing sh step must fail the take")
	}
	if err := runMacStep(ctx, t.TempDir(), nil, "ax-press play", nil); err == nil {
		t.Error("an unknown step verb must be rejected")
	}
	if _, err := Parse(map[string]any{"recorder": "macos-window", "period": 5.0, "launch": "open x.app"}, nil); err == nil {
		t.Error("macos-window without [clip.macos].owner must be rejected")
	}
}

func TestTakeT0SubtractsTheCaptureLag(t *testing.T) {
	// Touched 1.5s after screencapture launched; 13.56s of wall time held a
	// 13.29s video, so the first frame came 0.27s late and t0 is 1.23s in.
	t0, lag := takeT0(1.5, 13.56, 13.29)
	if math.Abs(lag-0.27) > 1e-9 || math.Abs(t0-1.23) > 1e-9 {
		t.Errorf("t0=%.4f lag=%.4f, want 1.23 and 0.27", t0, lag)
	}
}

func TestLaunchReturnsWhenTheAppIsBackgrounded(t *testing.T) {
	dir := t.TempDir()
	c := Config{Launch: "sleep 30 &", Root: dir, MacOS: MacOSConfig{Timeout: 10}}
	start := time.Now()
	if err := launchApp(context.Background(), c, nil, dir, nil); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("launch held for %s by a backgrounded child", d)
	}
}

func TestExpandPortAndWebConfig(t *testing.T) {
	if got := expandPort("http://127.0.0.1:$ADMIN_CLIP_PORT/?clip&p=${ADMIN_CLIP_PORT}", 5123); got != "http://127.0.0.1:5123/?clip&p=5123" {
		t.Errorf("expandPort: %s", got)
	}
	if _, err := Parse(map[string]any{"recorder": "web", "period": 4.5}, nil); err == nil {
		t.Error("web without [clip.web].url must be rejected")
	}
	for name, web := range map[string]map[string]any{
		"unknown scheme":   {"url": "http://x/", "scheme": "sepia"},
		"odd pixel width":  {"url": "http://x/", "width": int64(1281), "scale": 1.0},
		"fractional width": {"url": "http://x/", "width": 1280.5},
	} {
		if _, err := Parse(map[string]any{"recorder": "web", "period": 4.5, "web": web}, nil); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}

// seekPage paints a solid colour that cycles with period p seconds and
// exposes window.__clip.
func seekPage(p float64) string {
	return `<!doctype html><body style="margin:0"><canvas id=c width=320 height=180></canvas><script>
const c = document.getElementById("c").getContext("2d");
const period = ` + strconv.FormatFloat(p, 'f', -1, 64) + `;
function seek(t) { const u = (t % period) / period; c.fillStyle = "hsl(" + Math.round(u * 360) + ",80%,50%)"; c.fillRect(0, 0, 320, 180); }
seek(0);
window.__clip = { period, seek(t) { seek(t); return new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))); } };
</script></body>`
}

// TestWebRecorderRendersAFrameExactLoop drives headless Chrome over a page
// served in-process: the take must hold warmup+period+fade frames and the
// seam must match exactly; a page whose period disagrees must be refused.
func TestWebRecorderRendersAFrameExactLoop(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		p := 1.0
		if r.URL.Query().Has("wrong") {
			p = 2.0
		}
		w.Write([]byte(seekPage(p)))
	}))
	defer srv.Close()
	dir := t.TempDir()
	c := Config{Period: 1, Fade: 0.2, SeamThreshold: 0.5, Width: 320,
		Web: WebConfig{URL: srv.URL + "/?clip", Width: 320, Height: 180, Scale: 1, Timeout: 20}}
	ctx := context.Background()
	take, err := webRecorder{}.Record(ctx, c, dir, nil)
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			t.Skip("no Chrome installed")
		}
		t.Fatal(err)
	}
	var r Result
	if err := Cut(ctx, c, take, dir, nil, &r); err != nil {
		t.Fatal(err)
	}
	if r.SeamDiff > 0.5 || r.Crossfade != 0 || r.Seconds != 1 {
		t.Errorf("seam %.2f crossfade %.2f length %.3fs, want a matching seam and exactly 1s", r.SeamDiff, r.Crossfade, r.Seconds)
	}
	c.Web.URL = srv.URL + "/?clip&wrong"
	if _, err := (webRecorder{}).Record(ctx, c, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "period") {
		t.Errorf("a page period of 2s against [clip].period 1s must be refused, got %v", err)
	}
}

// TestWebRecorderStartsAndStopsTheLaunch serves the page through [clip].launch
// on $ADMIN_CLIP_PORT and checks the launch's process group is gone after.
func TestWebRecorderStartsAndStopsTheLaunch(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	site := t.TempDir()
	if err := os.WriteFile(filepath.Join(site, "index.html"), []byte(seekPage(1)), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	c := Config{Period: 1, Root: site, Launch: `echo $$ > server.pid; exec python3 -m http.server "$ADMIN_CLIP_PORT" --bind 127.0.0.1`,
		Web: WebConfig{URL: "http://127.0.0.1:$ADMIN_CLIP_PORT/?clip", Width: 320, Height: 180, Scale: 1, Timeout: 20}}
	if _, err := (webRecorder{}).Record(context.Background(), c, dir, nil); err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			t.Skip("no Chrome installed")
		}
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(site, "server.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid > 0 && syscall.Kill(pid, 0) == nil {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("launch pid %d still running after the take", pid)
	}
}
