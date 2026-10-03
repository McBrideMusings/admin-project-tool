package data

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// syncLogCap bounds the sync log. A poll writes a handful of lines every
// PollInterval for as long as a dashboard stays open, so past this size the
// file is rotated to sync.log.1 (replacing the previous one) and a fresh one
// started — two files at most, never an unbounded one.
const syncLogCap = 1 << 20

// syncLogPath is where LogSync appends. A var so a test can point it at a
// temp dir instead of the real log directory.
var syncLogPath = defaultSyncLogPath

var syncLogMu sync.Mutex

// SyncLogPath is the file every dashboard sync writes its events to — one file
// shared by every repo, each line naming its repo. It exists because the
// header's "sync failed" only says that a sync failed; this is where what it
// failed with is kept after the fact.
func SyncLogPath() string {
	syncLogMu.Lock()
	defer syncLogMu.Unlock()
	return syncLogPath()
}

// UseSyncLogPath points LogSync at path instead of the real log directory and
// returns a func that puts the default back — for a test package whose tests
// run real syncs and must not write into the user's log. Mirrors
// viewstate.UseGlobalPath.
func UseSyncLogPath(path string) (restore func()) {
	syncLogMu.Lock()
	defer syncLogMu.Unlock()
	prev := syncLogPath
	syncLogPath = func() string { return path }
	return func() {
		syncLogMu.Lock()
		defer syncLogMu.Unlock()
		syncLogPath = prev
	}
}

func defaultSyncLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "admin", "sync.log")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Logs", "admin", "sync.log")
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "admin", "log", "sync.log")
}

// LogSync appends one event line for repo nwo:
//
//	2026-10-02T18:03:07.412Z sync Forevr-Games/social-chess prs ok took=1.204s n=13
//	2026-10-02T18:03:07.412Z sync Forevr-Games/social-chess merged FAIL took=30.001s err="…"
//
// section names the unit of work (start, prs, issues, merged, prune); took is
// zero for an event with no duration; detail is free-form key=value text for a
// success and is ignored on a failure, which carries the error verbatim
// instead. Best-effort like the cache: a log that cannot be written never
// fails the sync it describes.
func LogSync(nwo, section string, took time.Duration, detail string, err error) {
	line := time.Now().UTC().Format("2006-01-02T15:04:05.000Z") + " sync " + nwo + " " + section
	if err != nil {
		line += " FAIL"
	} else {
		line += " ok"
	}
	if took > 0 {
		line += " took=" + took.Round(time.Millisecond).String()
	}
	if err != nil {
		line += fmt.Sprintf(" err=%q", err.Error())
	} else if detail != "" {
		line += " " + detail
	}

	syncLogMu.Lock()
	defer syncLogMu.Unlock()
	path := syncLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > syncLogCap {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line + "\n")
}
