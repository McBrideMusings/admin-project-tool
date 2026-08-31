package luahost

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A child that asks a question has to be answerable, and its question has to be
// visible. Both were broken at once, which is why it read as a hang rather than
// as a prompt: os/exec gives a nil Stdin the null device, so the child read
// instant EOF; and the output path scanned whole lines, so a prompt with no
// trailing newline never reached the terminal.

func TestRunCmdGivesTheChildTheParentsStdin(t *testing.T) {
	dir := t.TempDir()
	h := New(dir)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	go func() {
		w.WriteString("deploy\n")
		w.Close()
	}()
	h.Stdin = r

	var out bytes.Buffer
	h.Stdout = &out

	script := filepath.Join(dir, "child.sh")
	os.WriteFile(script, []byte("read ans\necho \"GOT:$ans\"\n"), 0o755)

	res, err := h.RunCmd("sh "+script, RunCmdOptions{})
	if err != nil {
		t.Fatalf("RunCmd: %v", err)
	}
	if !res.Ok {
		t.Fatalf("child failed; output=%q", res.Output)
	}
	if !strings.Contains(res.Output, "GOT:deploy") {
		t.Errorf("child did not receive stdin; got %q, want it to contain GOT:deploy", res.Output)
	}
}

func TestRunCmdShowsAPromptThatHasNoTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	h := New(dir)

	// Never closed while the child runs: the child blocks on the read, which is
	// exactly the state a real prompt sits in. If the prompt only appears after
	// the process exits, this test hangs and then fails, which is the bug.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	h.Stdin = r

	var out safeBuffer
	h.Stdout = &out

	// The sleep is what makes this isolate the OUTPUT defect rather than the
	// stdin one. Without it a child on the old code hit EOF, exited at once, and
	// the line flushed on exit — so the prompt appeared and the test passed
	// against broken code. Sleeping holds the child open in exactly the state a
	// real prompt sits in: written, unterminated, waiting.
	script := filepath.Join(dir, "child.sh")
	os.WriteFile(script, []byte("printf 'Type \"deploy\": '\nsleep 3\nread ans\n"), 0o755)

	appeared := make(chan struct{})
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(out.String(), `Type "deploy": `) {
				close(appeared)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		// Not seen while the child was still running: that is the bug.
	}()

	go func() {
		<-appeared
		w.WriteString("deploy\n")
		w.Close()
	}()

	if _, err := h.RunCmd("sh "+script, RunCmdOptions{}); err != nil {
		t.Fatalf("RunCmd: %v", err)
	}
	select {
	case <-appeared:
	default:
		t.Errorf("prompt did not reach the terminal while the child was still "+
			"waiting on it; got %q", out.String())
	}
}

// safeBuffer guards the buffer the pump writes from the goroutine above reading
// it concurrently.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
