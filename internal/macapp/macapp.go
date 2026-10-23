// Package macapp installs a built macOS .app bundle into place. It is the one
// implementation every install path uses — deploy_mac, dev_mac --install, the
// electron kind, and the `apple.install_app` Lua binding — so the three things
// that are easy to get wrong are handled once:
//
//   - The new bundle is staged beside the destination with `ditto` while the
//     running copy keeps running, so the app is closed for the shortest
//     possible time. Only then is the running copy stopped (SIGTERM, then
//     SIGKILL) and waited for. Copying over a running bundle corrupts it on
//     disk and leaves the old binary mapped.
//   - The swap is atomic: the staged bundle is renamed into place, and a
//     failed rename puts the previous bundle back. `rm -rf` then copy leaves a
//     window with no app installed.
//   - LaunchServices is told the bundle changed, so the Dock and `open` stop
//     using the previous registration.
package macapp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Logger is the output Install reports progress through.
type Logger interface {
	Info(string)
	Warn(string)
}

const lsregister = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"

// Resolve turns a bundle path that may carry a glob into exactly one existing
// bundle. electron-forge names its output dir with an arch suffix, so a glob
// is normal; more than one match means a stale build is lying around, and
// picking either would install the wrong binary, so that is an error.
func Resolve(pattern string) (string, error) {
	path := pattern
	if strings.ContainsAny(pattern, "*?[") {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return "", fmt.Errorf("bad bundle pattern %q: %w", pattern, err)
		}
		sort.Strings(matches)
		switch len(matches) {
		case 0:
			return "", fmt.Errorf("no app bundle matched %s", pattern)
		case 1:
			path = matches[0]
		default:
			return "", fmt.Errorf("%s matched several bundles — remove the stale build(s) or pass an exact path: %s",
				pattern, strings.Join(matches, ", "))
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return "", fmt.Errorf("no such app bundle: %s", abs)
	}
	return abs, nil
}

// Install copies the bundle at src to dest, replacing any bundle there. It
// reports whether a copy was running out of dest when it started, so a caller
// can relaunch what it stopped.
func Install(src, dest string, log Logger) (wasRunning bool, err error) {
	dest = strings.TrimRight(dest, "/")

	staged := dest + ".installing"
	previous := dest + ".previous"
	_ = os.RemoveAll(staged)
	_ = os.RemoveAll(previous)

	// ditto, not a recursive copy: a signed bundle's validity depends on its
	// exact symlink and resource-fork structure (*.framework/Versions/Current).
	// The copy happens while the old app is still running, so the app is
	// closed only for the stop, two renames and the caller's relaunch.
	if out, err := exec.Command("ditto", src, staged).CombinedOutput(); err != nil {
		_ = os.RemoveAll(staged)
		return false, fmt.Errorf("copy failed: %s", strings.TrimSpace(string(out)))
	}

	if pids := pidsRunningFrom(dest); len(pids) > 0 {
		wasRunning = true
		stop(dest, pids, log)
	}

	if _, err := os.Stat(dest); err == nil {
		if err := os.Rename(dest, previous); err != nil {
			_ = os.RemoveAll(staged)
			return wasRunning, fmt.Errorf("could not move the old %s aside: %w", dest, err)
		}
	}
	if err := os.Rename(staged, dest); err != nil {
		// Put the old bundle back rather than leave nothing installed.
		if _, statErr := os.Stat(previous); statErr == nil {
			_ = os.Rename(previous, dest)
		}
		_ = os.RemoveAll(staged)
		return wasRunning, fmt.Errorf("could not swap in the new bundle: %w", err)
	}
	_ = os.RemoveAll(previous)

	RefreshRegistration(dest)
	return wasRunning, nil
}

// RefreshRegistration bumps the bundle's mtime and re-registers it with
// LaunchServices so the next launch picks up a changed icon. Best-effort: a
// cache refresh is never worth failing an install over.
func RefreshRegistration(app string) {
	now := time.Now()
	_ = os.Chtimes(app, now, now)
	if _, err := os.Stat(lsregister); err == nil {
		_ = exec.Command(lsregister, "-f", app).Run()
	}
}

// stop sends SIGTERM to every pid running out of app, waits up to 5s, then
// SIGKILLs whatever ignored it and waits up to 2s more.
func stop(app string, pids []int, log Logger) {
	log.Info(fmt.Sprintf("stopping running %s (pids: %v)", filepath.Base(app), pids))
	signal(pids, syscall.SIGTERM)
	if waitGone(app, 5*time.Second) {
		return
	}
	stubborn := pidsRunningFrom(app)
	log.Warn(filepath.Base(app) + " ignored SIGTERM, sending SIGKILL")
	signal(stubborn, syscall.SIGKILL)
	waitGone(app, 2*time.Second)
}

func signal(pids []int, sig syscall.Signal) {
	for _, pid := range pids {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(sig)
		}
	}
}

func waitGone(app string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(pidsRunningFrom(app)) == 0 {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return len(pidsRunningFrom(app)) == 0
}

// pidsRunningFrom lists pids whose executable lives inside app. Matching the
// bundle path rather than the process name is deliberate: a name match can hit
// an unrelated app, and what has to stop is whatever runs out of the bundle
// about to be replaced.
func pidsRunningFrom(app string) []int {
	out, err := exec.Command("ps", "-Ao", "pid=,comm=").Output()
	if err != nil {
		return nil
	}
	return parsePids(string(out), app)
}

func parsePids(psOutput, app string) []int {
	prefix := strings.TrimRight(app, "/") + "/"
	var pids []int
	for _, line := range strings.Split(psOutput, "\n") {
		fields := strings.SplitN(strings.TrimSpace(line), " ", 2)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(fields[1]), prefix) {
			pids = append(pids, pid)
		}
	}
	return pids
}
