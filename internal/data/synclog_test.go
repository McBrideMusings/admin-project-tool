package data

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLogSyncKeepsTheErrorVerbatim pins that a failed section's line carries
// the repo, the section and the whole error — the thing the header cannot show
// — and that a success line carries its detail instead.
func TestLogSyncKeepsTheErrorVerbatim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "sync.log")
	t.Cleanup(UseSyncLogPath(path))

	LogSync("owner/repo", "prs", 1500*time.Millisecond, "n=3", nil)
	LogSync("owner/repo", "merged", 30*time.Second, "heads=9", errors.New("exit status 1: HTTP 502\nbad gateway"))

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d:\n%s", len(lines), b)
	}
	if !strings.HasSuffix(lines[0], " sync owner/repo prs ok took=1.5s n=3") {
		t.Errorf("success line = %q", lines[0])
	}
	if !strings.HasSuffix(lines[1], ` sync owner/repo merged FAIL took=30s err="exit status 1: HTTP 502\nbad gateway"`) {
		t.Errorf("failure line = %q", lines[1])
	}
	if _, err := time.Parse("2006-01-02T15:04:05.000Z", strings.Fields(lines[1])[0]); err != nil {
		t.Errorf("line does not start with an ISO timestamp: %q", lines[1])
	}
}

// TestFetchAllLogsEachSectionsOwnError pins FetchAll's wiring: every section
// that fails writes its own FAIL line carrying its own error, and one that
// succeeds writes ok — so a swapped field or a dropped errOf shows up here.
func TestFetchAllLogsEachSectionsOwnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.log")
	t.Cleanup(UseSyncLogPath(path))
	t.Setenv("HOME", t.TempDir()) // FetchAll saves the cache under the user cache dir
	prevPRs, prevIssues, prevMerged := fetchPRsFn, fetchIssuesFn, fetchMergedFn
	t.Cleanup(func() { fetchPRsFn, fetchIssuesFn, fetchMergedFn = prevPRs, prevIssues, prevMerged })
	fetchPRsFn = func(context.Context, string) Remote { return Remote{PRsFetchError: "prs broke"} }
	fetchIssuesFn = func(context.Context, string) Remote { return Remote{IssuesFetchError: "issues broke"} }
	fetchMergedFn = func(context.Context, string, int) Remote { return Remote{MergedFetchedAt: time.Now()} }

	FetchAll(context.Background(), "owner/repo", 200, true)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"sync owner/repo start ok issues=true merged_limit=200",
		`sync owner/repo prs FAIL`, `err="prs broke"`,
		`sync owner/repo issues FAIL`, `err="issues broke"`,
		"sync owner/repo merged ok",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("log is missing %q:\n%s", want, b)
		}
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, " prs ") && !strings.Contains(line, "prs broke") ||
			strings.Contains(line, " issues FAIL") && !strings.Contains(line, "issues broke") {
			t.Errorf("a section logged another section's error: %q", line)
		}
	}
}
