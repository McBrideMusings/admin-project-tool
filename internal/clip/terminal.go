package clip

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// terminalRecorder records a terminal program with vhs: a tape typed into
// vhs's own headless terminal, so no keystroke reaches the user's keyboard
// or focus. [clip].steps are vhs tape commands ("Down", "Enter",
// `Type "q"`, "Sleep 500ms"), run Repeat times after Launch has printed
// Ready. The recording starts at the first step, so T0 is 0.
type terminalRecorder struct{}

func (terminalRecorder) Record(ctx context.Context, c Config, dir string, log *Log) (Take, error) {
	if len(c.Steps) == 0 {
		return Take{}, fmt.Errorf("the terminal recorder needs [clip].steps: it has no in-app demo hook")
	}
	raw := filepath.Join(dir, "raw.mp4")
	tape := filepath.Join(dir, "clip.tape")
	if err := os.WriteFile(tape, []byte(Tape(c, raw)), 0o644); err != nil {
		return Take{}, fmt.Errorf("write tape: %w", err)
	}
	log.Printf("terminal: tape %s", tape)
	if err := run(ctx, log, dir, "vhs", tape); err != nil {
		return Take{}, err
	}
	return Take{Path: raw}, nil
}

// Tape renders the vhs tape for c, writing the video to out.
func Tape(c Config, out string) string {
	t := c.Terminal
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	line("Output %s", strconv.Quote(out))
	line("Set Width %d", t.WidthPx)
	line("Set Height %d", t.HeightPx)
	line("Set FontSize %d", t.FontSize)
	line("Set Framerate 30")
	line("Set Padding 0")
	if t.FontFamily != "" {
		line("Set FontFamily %s", strconv.Quote(t.FontFamily))
	}
	if t.Theme != "" {
		line("Set Theme %s", strconv.Quote(t.Theme))
	}
	if t.Shell != "" {
		line("Set Shell %s", strconv.Quote(t.Shell))
	}
	line("Env ADMIN_CLIP %s", strconv.Quote("1"))
	line("Hide")
	if c.Launch != "" {
		line("Type %s", strconv.Quote(c.Launch))
		line("Enter")
	}
	if c.Ready != "" {
		line("Wait+Screen@30s /%s/", strings.ReplaceAll(c.Ready, "/", `\/`))
	}
	line("Show")
	if c.Warmup > 0 {
		line("Sleep %dms", int(c.Warmup*1000))
	}
	for i := 0; i < c.Repeat; i++ {
		for _, s := range c.Steps {
			line("%s", s)
		}
	}
	return b.String()
}
