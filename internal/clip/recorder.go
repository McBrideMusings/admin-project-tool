package clip

import (
	"context"
	"sort"
)

// Take is one raw recording a recorder hands the clipper.
type Take struct {
	// Path is the recorded video.
	Path string
	// T0 is where, in seconds into Path, the demo timeline starts.
	T0 float64
	// Crop is the recorder's content box as an ffmpeg crop "w:h:x:y", or
	// empty when the whole frame is content.
	Crop string
}

// Recorder captures one surface: launch the app, find its surface, record
// warmup + Repeat periods of the choreography, stop.
type Recorder interface {
	Record(ctx context.Context, c Config, dir string, log *Log) (Take, error)
}

var recorders = map[string]Recorder{
	"terminal":     terminalRecorder{},
	"macos-window": macWindowRecorder{},
}

func lookupRecorder(name string) Recorder { return recorders[name] }

// RecorderNames lists the implemented recorders, sorted.
func RecorderNames() []string {
	out := make([]string, 0, len(recorders))
	for n := range recorders {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
