// Package clip records a short looping demo of a project's app and encodes it
// as an animated WebP. It is the mechanism behind kind = "clip": a recorder
// per surface (the terminal today) captures a take, and one shared clipper
// cuts a single period out of it, joins the seam, crops, scales and encodes.
//
// What the demo shows is the project's: either [clip].steps, written in the
// recorder's own words, or an in-app demo the app runs itself when it sees
// ADMIN_CLIP=1, starting its timeline when the file named by
// ADMIN_CLIP_START appears and repeating every `period` seconds.
package clip

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Config is the [clip] table.
type Config struct {
	// Recorder names the surface: "terminal" is the one implemented.
	Recorder string
	// Launch starts the app. The terminal recorder types it into its
	// terminal before recording begins.
	Launch string
	// Ready is a regex the terminal recorder waits for on screen after
	// Launch, before recording begins.
	Ready string
	// Period is the loop length in seconds: the clip is exactly one period.
	Period float64
	// Warmup is the seconds of recording before the nominal cut start.
	Warmup float64
	// Repeat is how many times the steps run during the recording.
	Repeat int
	// Steps is the choreography, in the recorder's own words.
	Steps []string
	// Fade is the crossfade, in seconds, used when the seam does not match.
	Fade float64
	// SeamThreshold is the percentage of changed pixels between the cut's
	// first frame and the frame one period later above which the seam gets
	// a crossfade.
	SeamThreshold float64
	// Width is the output width in pixels; FPS the output frame rate.
	Width int
	FPS   int
	// Quality is the first libwebp quality tried; lower ones are tried in
	// steps of 15 down to 25 while the file is over Budget.
	Quality int
	// Budget is the maximum WebP size in bytes; 0 means no limit.
	Budget int64
	// Crop is an ffmpeg crop "w:h:x:y" applied before scaling; empty means
	// the recorder's own content box, or none.
	Crop string
	// Out is the directory every artifact of a run is written to.
	Out string
	// Publish is a shell command run after a passing clip, with
	// ADMIN_CLIP_FILE set to the WebP's path.
	Publish string

	Terminal TerminalConfig
}

// TerminalConfig is [clip.terminal]: how the terminal recorder's virtual
// terminal looks.
type TerminalConfig struct {
	FontSize   int
	FontFamily string
	Theme      string
	Shell      string
	// Pixel size of the virtual terminal; vhs sizes the grid from these.
	WidthPx, HeightPx int
}

// Parse reads a raw [clip] table. resolve expands ${VAR} in string values.
// An unknown key or a value of the wrong type is an error, never a default.
func Parse(raw map[string]any, resolve func(string) string) (Config, error) {
	if resolve == nil {
		resolve = func(s string) string { return s }
	}
	r := &reader{raw: raw, name: "clip"}
	c := Config{
		Recorder:      resolve(r.str("recorder", "")),
		Launch:        resolve(r.str("launch", "")),
		Ready:         r.str("ready", ""),
		Period:        r.num("period", 0),
		Warmup:        r.num("warmup", 0),
		Repeat:        int(r.num("repeat", 2)),
		Fade:          r.num("fade", 0.5),
		SeamThreshold: r.num("seam_threshold", 0.5),
		Width:         int(r.num("width", 1280)),
		FPS:           int(r.num("fps", 15)),
		Quality:       int(r.num("quality", 70)),
		Crop:          r.str("crop", ""),
		Out:           resolve(r.str("out", "")),
		Publish:       r.str("publish", ""),
		Terminal:      TerminalConfig{FontSize: 28, WidthPx: 1920, HeightPx: 1080},
	}
	for _, s := range r.strs("steps") {
		c.Steps = append(c.Steps, resolve(s))
	}
	c.Budget = r.size("budget")
	if t := r.table("terminal"); t != nil {
		c.Terminal = TerminalConfig{
			FontSize:   int(t.num("font_size", 28)),
			FontFamily: t.str("font_family", ""),
			Theme:      t.str("theme", ""),
			Shell:      t.str("shell", ""),
			WidthPx:    int(t.num("width_px", 1920)),
			HeightPx:   int(t.num("height_px", 1080)),
		}
		t.unknown()
	}
	r.unknown()
	if r.err != nil {
		return c, r.err
	}

	switch {
	case c.Recorder == "":
		return c, fmt.Errorf("recorder is required (%s)", strings.Join(RecorderNames(), " | "))
	case lookupRecorder(c.Recorder) == nil:
		return c, fmt.Errorf("recorder %q is not one of %s", c.Recorder, strings.Join(RecorderNames(), " | "))
	case c.Period <= 0:
		return c, fmt.Errorf("period must be > 0 seconds")
	case c.Warmup < 0:
		return c, fmt.Errorf("warmup must be >= 0 seconds")
	case c.Repeat < 1:
		return c, fmt.Errorf("repeat must be >= 1")
	case c.Fade < 0 || c.Fade >= c.Period:
		return c, fmt.Errorf("fade must be >= 0 and shorter than period")
	case c.Width <= 0 || c.FPS <= 0:
		return c, fmt.Errorf("width and fps must be > 0")
	case c.Quality < minQuality || c.Quality > 100:
		return c, fmt.Errorf("quality must be %d-100", minQuality)
	}
	return c, nil
}

// ParseSize reads "2MB", "500KB", "750000" (bytes). Units are powers of 1024.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		m      int64
	}{{"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(s, u.suffix) {
			mult = u.m
			s = strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			break
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("%q is not a size like 2MB", s)
	}
	return int64(f * float64(mult)), nil
}

// reader reads typed values off one raw table, remembering the first type
// error and which keys were read so unknown() can name the rest.
type reader struct {
	raw   map[string]any
	name  string
	seen  map[string]bool
	err   error
	sub   []*reader
}

func (r *reader) get(k string) (any, bool) {
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	r.seen[k] = true
	v, ok := r.raw[k]
	return v, ok
}

func (r *reader) fail(k, want string, v any) {
	if r.err == nil {
		r.err = fmt.Errorf("%s.%s must be %s, not %T", r.name, k, want, v)
	}
}

func (r *reader) str(k, def string) string {
	v, ok := r.get(k)
	if !ok {
		return def
	}
	s, ok := v.(string)
	if !ok {
		r.fail(k, "a string", v)
	}
	return s
}

func (r *reader) num(k string, def float64) float64 {
	v, ok := r.get(k)
	if !ok {
		return def
	}
	switch n := v.(type) {
	case int64:
		return float64(n)
	case int:
		return float64(n)
	case float64:
		return n
	}
	r.fail(k, "a number", v)
	return def
}

func (r *reader) strs(k string) []string {
	v, ok := r.get(k)
	if !ok {
		return nil
	}
	var out []string
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		for _, e := range l {
			s, ok := e.(string)
			if !ok {
				r.fail(k, "a list of strings", e)
				return nil
			}
			out = append(out, s)
		}
		return out
	}
	r.fail(k, "a list of strings", v)
	return nil
}

// size reads a byte count: an integer, or a string like "2MB".
func (r *reader) size(k string) int64 {
	v, ok := r.get(k)
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		b, err := ParseSize(n)
		if err != nil && r.err == nil {
			r.err = fmt.Errorf("%s.%s: %w", r.name, k, err)
		}
		return b
	}
	r.fail(k, `a size like "2MB" or a byte count`, v)
	return 0
}

func (r *reader) table(k string) *reader {
	v, ok := r.get(k)
	if !ok {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		r.fail(k, "a table", v)
		return nil
	}
	sub := &reader{raw: m, name: r.name + "." + k}
	r.sub = append(r.sub, sub)
	return sub
}

// unknown records an error naming the first key nothing read, then folds in
// any sub-table's error.
func (r *reader) unknown() {
	var extra []string
	for k := range r.raw {
		if !r.seen[k] {
			extra = append(extra, r.name+"."+k)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 && r.err == nil {
		r.err = fmt.Errorf("unknown key %s", strings.Join(extra, ", "))
	}
	for _, s := range r.sub {
		if s.err != nil && r.err == nil {
			r.err = s.err
		}
	}
}
