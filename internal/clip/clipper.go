package clip

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// cutFPS is the frame rate the take is normalised to before cutting; the
// seam search compares frames on this grid.
const cutFPS = 30

// seamWindow is how far, in seconds, either side of the nominal cut start
// the seam search looks.
const seamWindow = 0.5

// Result is what one clip run produced; it is written to clip.json.
type Result struct {
	Recorder    string    `json:"recorder"`
	Take        string    `json:"take"`
	TakeSeconds float64   `json:"take_seconds"`
	CutStart    float64   `json:"cut_start"`
	Period      float64   `json:"period"`
	SeamDiff    float64   `json:"seam_diff"`
	Crossfade   float64   `json:"crossfade"`
	Crop        string    `json:"crop,omitempty"`
	Video       string    `json:"video"`
	Seconds     float64   `json:"seconds"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	WebP        string    `json:"webp"`
	Bytes       int64     `json:"bytes"`
	Quality     int       `json:"quality"`
	Budget      int64     `json:"budget,omitempty"`
	Stills      []string  `json:"stills"`
	Attempts    []Attempt `json:"attempts"`
	Published   bool      `json:"published"`
	Error       string    `json:"error,omitempty"`
}

// Attempt is one WebP encode at one quality.
type Attempt struct {
	Quality int   `json:"quality"`
	Bytes   int64 `json:"bytes"`
}

// Cut finds the best seam in take, cuts one period, crossfades when the seam
// differs, and writes clip.mp4 and five stills into dir.
func Cut(ctx context.Context, c Config, take Take, dir string, log *Log, r *Result) error {
	dur, err := probeDuration(ctx, log, dir, take.Path)
	if err != nil {
		return err
	}
	r.TakeSeconds = dur
	crop := c.Crop
	if crop == "" {
		crop = take.Crop
	}
	r.Crop = crop

	frames, err := grayFrames(ctx, log, dir, take.Path, crop)
	if err != nil {
		return err
	}
	period := int(math.Round(c.Period * cutFPS))
	fadeFrames := int(math.Round(c.Fade * cutFPS))
	start, diff, err := FindSeam(frames, cutFPS, take.T0+c.Warmup, period, fadeFrames)
	if err != nil {
		return fmt.Errorf("take is %.2fs: %w", dur, err)
	}
	r.CutStart, r.SeamDiff = float64(start)/cutFPS, diff
	fade := 0
	if diff > c.SeamThreshold {
		fade = fadeFrames
	}
	r.Crossfade = float64(fade) / cutFPS
	log.Printf("seam: start=%.3fs (frame %d) changed=%.2f%% threshold=%.2f%% crossfade=%.2fs",
		r.CutStart, start, diff, c.SeamThreshold, r.Crossfade)

	r.Video = filepath.Join(dir, "clip.mp4")
	if err := run(ctx, log, dir, "ffmpeg", "-y", "-v", "error", "-i", take.Path,
		"-filter_complex", CutFilter(crop, c.Width, start, period, fade),
		"-map", "[out]", "-an", "-r", strconv.Itoa(cutFPS), "-c:v", "libx264", "-crf", "14", "-pix_fmt", "yuv420p", r.Video); err != nil {
		return err
	}
	n, err := countFrames(ctx, log, dir, r.Video)
	if err != nil {
		return err
	}
	r.Seconds = float64(n) / cutFPS
	if r.Width, r.Height, err = probeSize(ctx, log, dir, r.Video); err != nil {
		return err
	}
	if n != period {
		return fmt.Errorf("clip has %d frames, one period is %d", n, period)
	}
	return writeStills(ctx, dir, log, r)
}

// CutFilter is the ffmpeg filter graph that normalises the take to cutFPS,
// then either keeps the period frames from start or, with fade > 0, blends
// the fade frames that follow the period into the period's first fade
// frames, so the last frame flows into the first. Cutting by frame index
// keeps the clip exactly period frames long.
func CutFilter(crop string, width, start, period, fade int) string {
	pre := fmt.Sprintf("fps=%d,scale=%d:-2:flags=lanczos,format=yuv420p", cutFPS, width)
	if crop != "" {
		pre = "crop=" + crop + "," + pre
	}
	trim := func(from, to int) string {
		return fmt.Sprintf("trim=start_frame=%d:end_frame=%d,setpts=PTS-STARTPTS", from, to)
	}
	if fade == 0 {
		return fmt.Sprintf("[0:v]%s,%s[out]", pre, trim(start, start+period))
	}
	secs := strconv.FormatFloat(float64(fade)/cutFPS, 'f', 6, 64)
	return fmt.Sprintf("[0:v]%s,split=3[a][b][c];"+
		"[a]%s[tail];[b]%s[head];"+
		"[tail][head]blend=all_expr='A*(1-T/%s)+B*(T/%s)'[x];"+
		"[c]%s[body];"+
		"[x][body]concat=n=2:v=1:a=0[out]",
		pre, trim(start+period, start+period+fade), trim(start, start+fade), secs, secs, trim(start+fade, start+period))
}

// FindSeam returns the cut start frame within seamWindow of nominal seconds
// whose frame best matches the frame period frames later, and the
// percentage of pixels that differ between them. The take must hold fade
// frames past the cut, in case the seam needs a crossfade.
func FindSeam(frames [][]byte, fps int, nominal float64, period, fade int) (int, float64, error) {
	lo := max(0, int(math.Round((nominal-seamWindow)*float64(fps))))
	hi := min(int(math.Round((nominal+seamWindow)*float64(fps))), len(frames)-period-fade)
	if hi < lo {
		return 0, 0, fmt.Errorf("too short to cut %d frames from %.2fs with %d to spare: record longer (raise repeat)",
			period, nominal, fade)
	}
	best, bestDiff := lo, math.Inf(1)
	for i := lo; i <= hi; i++ {
		if d := changedPercent(frames[i], frames[i+period]); d < bestDiff {
			best, bestDiff = i, d
		}
	}
	return best, bestDiff, nil
}

// changedPercent is the percentage of pixels whose gray value differs by more
// than pixelTolerance between a and b. A count of changed pixels, unlike a
// mean difference, separates a moved cursor row from encoder noise.
func changedPercent(a, b []byte) float64 {
	n := min(len(a), len(b))
	if n == 0 {
		return 0
	}
	changed := 0
	for i := 0; i < n; i++ {
		d := int(a[i]) - int(b[i])
		if d > pixelTolerance || d < -pixelTolerance {
			changed++
		}
	}
	return 100 * float64(changed) / float64(n)
}

// pixelTolerance is the gray-level difference treated as encoder noise.
const pixelTolerance = 16

const grayW, grayH = 160, 90

// grayFrames decodes the cropped region of path to small gray frames on the
// cutFPS grid, so the seam is measured on what ships.
func grayFrames(ctx context.Context, log *Log, dir, path, crop string) ([][]byte, error) {
	vf := fmt.Sprintf("fps=%d,scale=%d:%d,format=gray", cutFPS, grayW, grayH)
	if crop != "" {
		vf = "crop=" + crop + "," + vf
	}
	out, err := output(ctx, log, dir, "ffmpeg", "-v", "error", "-i", path, "-vf", vf,
		"-f", "rawvideo", "-")
	if err != nil {
		return nil, err
	}
	size := grayW * grayH
	var frames [][]byte
	for i := 0; i+size <= len(out); i += size {
		frames = append(frames, out[i:i+size])
	}
	log.Printf("seam: decoded %d gray frames at %dfps", len(frames), cutFPS)
	return frames, nil
}

// writeStills writes the first frame, the last frame and three between them
// as stills/still-0..4.png: the last beside the first is the seam.
func writeStills(ctx context.Context, dir string, log *Log, r *Result) error {
	sdir := filepath.Join(dir, "stills")
	if err := os.RemoveAll(sdir); err != nil {
		return err
	}
	if err := os.MkdirAll(sdir, 0o755); err != nil {
		return err
	}
	last := int(math.Round(r.Seconds*cutFPS)) - 1
	var picks []string
	for _, frac := range []float64{0, 0.25, 0.5, 0.75, 1} {
		picks = append(picks, fmt.Sprintf("eq(n\\,%d)", int(frac*float64(last))))
	}
	if err := run(ctx, log, dir, "ffmpeg", "-y", "-v", "error", "-i", r.Video,
		"-vf", "select="+strings.Join(picks, "+"), "-fps_mode", "passthrough",
		"-start_number", "0", filepath.Join(sdir, "still-%d.png")); err != nil {
		return err
	}
	r.Stills = nil
	for i := range picks {
		p := filepath.Join(sdir, fmt.Sprintf("still-%d.png", i))
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("still %d of %d missing: %w", i, len(picks), err)
		}
		r.Stills = append(r.Stills, p)
	}
	return nil
}

// minQuality is the lowest libwebp quality Encode tries.
const minQuality = 25

// Encode writes clip.webp from the cut video, lowering quality in steps of 15
// while it is over Budget, down to minQuality.
func Encode(ctx context.Context, c Config, dir string, log *Log, r *Result) error {
	r.WebP = filepath.Join(dir, "clip.webp")
	r.Budget = c.Budget
	r.Attempts = nil
	for q := c.Quality; ; q -= 15 {
		if err := run(ctx, log, dir, "ffmpeg", "-y", "-v", "error", "-i", r.Video,
			"-vf", fmt.Sprintf("fps=%d", c.FPS), "-an", "-loop", "0",
			"-c:v", "libwebp_anim", "-quality", strconv.Itoa(q), "-compression_level", "6", r.WebP); err != nil {
			return err
		}
		st, err := os.Stat(r.WebP)
		if err != nil {
			return err
		}
		r.Bytes, r.Quality = st.Size(), q
		r.Attempts = append(r.Attempts, Attempt{Quality: q, Bytes: st.Size()})
		log.Printf("webp: quality=%d bytes=%d budget=%d", q, st.Size(), c.Budget)
		if c.Budget == 0 || st.Size() <= c.Budget {
			return nil
		}
		if q-15 < minQuality {
			return fmt.Errorf("webp is %d bytes at quality %d, over the %d-byte budget: shorten the period, narrow width or lower fps",
				st.Size(), q, c.Budget)
		}
	}
}

// countFrames decodes path and returns its exact frame count.
func countFrames(ctx context.Context, log *Log, dir, path string) (int, error) {
	out, err := output(ctx, log, dir, "ffprobe", "-v", "error", "-count_frames", "-select_streams", "v:0",
		"-show_entries", "stream=nb_read_frames", "-of", "csv=p=0", path)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("ffprobe frame count %q: %w", out, err)
	}
	return n, nil
}

func probeDuration(ctx context.Context, log *Log, dir, path string) (float64, error) {
	out, err := output(ctx, log, dir, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path)
	if err != nil {
		return 0, err
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe duration %q: %w", out, err)
	}
	return d, nil
}

func probeSize(ctx context.Context, log *Log, dir, path string) (int, int, error) {
	out, err := output(ctx, log, dir, "ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0:s=x", path)
	if err != nil {
		return 0, 0, err
	}
	var w, h int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%dx%d", &w, &h); err != nil {
		return 0, 0, fmt.Errorf("ffprobe size %q: %w", out, err)
	}
	return w, h, nil
}
