// Package metro manages the React Native Metro bundler lifecycle — the Go
// port of admin_lib/metro.py. Metro is the JS bundler a React Native app
// fetches its bundle from when it launches; without one running, a debug
// build shows the red "No script URL provided" screen instead of the app.
// One Metro serves every target of a project (iOS simulator, physical
// device, Android emulator all pull from the same port), so it is started
// once and reused rather than owned by any single dev command, and left
// running when a dev command exits — StopMetro is for a `down`/`kill`
// command to call explicitly.
//
// Not yet wired into a kind or a Lua host binding: the higher-level
// wrappers admin_lib/react_native.py builds on top of this (ensure_metro_for,
// dev_android, apple_config_for) drive admin_lib.apple / admin_lib.android
// directly for the iOS/Android build+launch step, and neither of those has a
// Go port yet — admin-3rgp.12 (Apple kinds) and an as-yet-untracked Android
// port own that. This package is the self-contained half admin-3rgp.14
// ("port... the metro, react-native and electron helpers") is scoped to:
// real, callable, and ready for whichever ticket wires a React Native dev
// loop to call it, rather than a stub that would need rewriting anyway.
package metro

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DefaultPort mirrors admin_lib/metro.py's DEFAULT_PORT.
const DefaultPort = 8081

// statusBody is the exact response Metro's /status answers with. Checking
// the body (not just a 200) avoids mistaking some other service already
// holding the port for a live bundler.
const statusBody = "packager-status:running"

// Running is the Go port of admin_lib/metro.py::metro_running: true if a
// Metro bundler is answering on port.
func Running(port int) bool {
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/status", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	buf := make([]byte, 200)
	n, _ := io.ReadFull(resp.Body, buf)
	return strings.Contains(string(buf[:n]), statusBody)
}

func pidAlive(pidFile string) (int, bool) {
	if pidFile == "" {
		return 0, false
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return 0, false
	}
	return pid, true
}

// Ensure is the Go port of admin_lib/metro.py::ensure_metro: start Metro in
// cwd unless one is already serving port. Returns (started, error) — started
// is false and error is nil when a bundler was already up; error is
// non-nil if a freshly started Metro never answers /status within timeout,
// matching Python's RuntimeError-on-failure contract (the caller must not
// go on to build+launch an app that would only show a red screen).
func Ensure(cwd string, port int, logFile, pidFile string, timeout time.Duration) (bool, error) {
	if Running(port) {
		return false, nil
	}

	var out *os.File
	if logFile != "" {
		if dir := filepath.Dir(logFile); dir != "" {
			_ = os.MkdirAll(dir, 0o755)
		}
		f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return false, fmt.Errorf("could not open metro log %s: %w", logFile, err)
		}
		defer f.Close()
		out = f
	}

	cmd := exec.Command("npx", "react-native", "start", "--port", strconv.Itoa(port))
	cmd.Dir = cwd
	cmd.Stdin = nil
	if out != nil {
		cmd.Stdout = out
		cmd.Stderr = out
	} else {
		cmd.Stdout = nil
		cmd.Stderr = nil
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("could not start Metro: %w", err)
	}

	if pidFile != "" {
		if dir := filepath.Dir(pidFile); dir != "" {
			_ = os.MkdirAll(dir, 0o755)
		}
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
	}

	deadline := time.Now().Add(timeout)
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	for time.Now().Before(deadline) {
		if Running(port) {
			return true, nil
		}
		select {
		case err := <-waitCh:
			suffix := ""
			if logFile != "" {
				suffix = " — see " + logFile
			}
			return false, fmt.Errorf("Metro exited immediately (%v)%s", err, suffix)
		default:
		}
		time.Sleep(250 * time.Millisecond)
	}

	suffix := ""
	if logFile != "" {
		suffix = " — see " + logFile
	}
	return false, fmt.Errorf("Metro did not answer :%d/status within %s%s", port, timeout, suffix)
}

// Stop is the Go port of admin_lib/metro.py::stop_metro: stop a Metro
// started by Ensure. Returns true if one was stopped. The port is the
// source of truth, not the pid file — a pid file goes stale easily (Ensure
// writes none when it finds a bundler already up), so trusting it alone
// would report "no Metro running" at a port still answering, and leave it
// answering.
func Stop(pidFile string, port int) bool {
	running := Running(port)
	pid, pidOK := pidAlive(pidFile)

	if !running {
		if pidFile != "" {
			_ = os.Remove(pidFile)
		}
		return false
	}
	if !pidOK {
		return false
	}

	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	if pidFile != "" {
		_ = os.Remove(pidFile)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !Running(port) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
