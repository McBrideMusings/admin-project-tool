package clip

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
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
