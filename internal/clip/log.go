package clip

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Log writes timestamped lines to <out>/clip.log and echoes them to Echo.
type Log struct {
	f    *os.File
	Echo io.Writer
}

// OpenLog creates (truncating) path.
func OpenLog(path string, echo io.Writer) (*Log, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &Log{f: f, Echo: echo}, nil
}

// Printf logs a progress line and echoes it.
func (l *Log) Printf(format string, a ...any) { l.line(true, format, a...) }

// Detailf logs a line to the file only: subprocess argv and exit status.
func (l *Log) Detailf(format string, a ...any) { l.line(false, format, a...) }

func (l *Log) line(echo bool, format string, a ...any) {
	if l == nil {
		return
	}
	msg := fmt.Sprintf(format, a...)
	if l.f != nil {
		fmt.Fprintf(l.f, "%s %s\n", time.Now().Format(time.RFC3339Nano), msg)
	}
	if echo && l.Echo != nil {
		fmt.Fprintf(l.Echo, "clip: %s\n", msg)
	}
}

// raw appends a subprocess's output to the log file only.
func (l *Log) raw(b []byte) {
	if l != nil && l.f != nil && len(b) > 0 {
		l.f.Write(b)
		if b[len(b)-1] != '\n' {
			l.f.Write([]byte("\n"))
		}
	}
}

func (l *Log) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	return l.f.Close()
}

// run executes name args in dir, logging argv, duration and output; the error
// carries the output's last lines.
func run(ctx context.Context, log *Log, dir, name string, args ...string) error {
	_, err := output(ctx, log, dir, name, args...)
	return err
}

// output is run that also returns stdout, which is kept out of the log.
func output(ctx context.Context, log *Log, dir string, name string, args ...string) ([]byte, error) {
	log.Detailf("exec: %s %s", name, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	err := cmd.Run()
	log.raw(stderr.Bytes())
	log.Detailf("exit: %s after %s (err=%v)", name, time.Since(start).Round(time.Millisecond), err)
	if err != nil {
		return nil, fmt.Errorf("%s: %w\n%s", name, err, tail(stderr.String(), 8))
	}
	return stdout.Bytes(), nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
