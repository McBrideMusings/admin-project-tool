package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Run dispatches one verb against cfg — the entry point for a `service`
// command. macOS runs the launchd verbs, Linux the systemd --user verbs: same
// config table, same verb names, same behaviour either way.
func Run(raw map[string]any, args []string, r Reporter) error {
	verb := ""
	if len(args) > 0 {
		verb = args[0]
	}
	switch verb {
	case "install", "uninstall", "status", "restart", "stop", "start":
	case "":
		r.Info(Usage)
		return nil
	default:
		return fmt.Errorf("unknown service verb: %s\n\n%s", verb, Usage)
	}

	c, err := ReadConfig(raw, r)
	if err != nil {
		return err
	}

	if runtime.GOOS == "linux" {
		switch verb {
		case "install":
			return installSystemd(c, r)
		case "uninstall":
			return uninstallSystemd(c, r)
		case "status":
			return statusSystemd(c, r)
		case "restart":
			return systemctlOK(r, c, "restarted", "restart", c.Label+".service")
		case "stop":
			return stopSystemd(c, r)
		default:
			return startSystemd(c, r)
		}
	}

	switch verb {
	case "install":
		return install(c, r)
	case "uninstall":
		return uninstall(c, r)
	case "status":
		return status(c, r)
	case "restart":
		if err := launchctl(true, "kickstart", "-k", domain()+"/"+c.Label); err != nil {
			return err
		}
		r.Ok("restarted " + c.Label)
		return nil
	case "stop":
		bootout(c.Label)
		r.Ok("unloaded " + c.Label + " (plist left in place — `admin service start` to bring it back)")
		return nil
	default:
		return start(c, r)
	}
}

// --- launchd (macOS) ------------------------------------------------------

func plistPath(label string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

func domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func launchctl(check bool, args ...string) error {
	cmd := exec.Command("launchctl", args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	err := cmd.Run()
	if err != nil && check {
		detail := strings.TrimSpace(errBuf.String())
		if detail == "" {
			detail = strings.TrimSpace(out.String())
		}
		return fmt.Errorf("launchctl %s failed: %s", strings.Join(args, " "), detail)
	}
	return nil
}

func launchctlOutput(args ...string) (string, bool) {
	cmd := exec.Command("launchctl", args...)
	out, err := cmd.Output()
	return string(out), err == nil
}

func isLoaded(label string) bool {
	_, ok := launchctlOutput("print", domain()+"/"+label)
	return ok
}

func bootout(label string) { _ = launchctl(false, "bootout", domain()+"/"+label) }

// waitUnloaded blocks until label is really gone from launchd's tables.
// bootout of a KeepAlive job is async — the old process is still tearing down
// when it returns. Bootstrapping (or swapping the binary) immediately races it:
// a bootstrap can fail with EIO, and a binary swap can land under the old
// running image.
func waitUnloaded(label string) {
	for i := 0; i < 25; i++ {
		if !isLoaded(label) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func install(c Config, r Reporter) error {
	binaryChanged := true
	if c.Source != "" {
		if _, err := os.Stat(c.Source); err != nil {
			return fmt.Errorf("%s does not exist — build it first", c.Source)
		}
		binaryChanged = !filesIdentical(c.Source, c.Program)
	}

	plist := plistPath(c.Label)
	desired := RenderPlist(c)
	current, hadPlist := readIfExists(plist)

	if c.Source != "" && !binaryChanged && hadPlist && bytes.Equal(current, desired) && isLoaded(c.Label) {
		r.Ok(filepath.Base(c.Program) + " unchanged and already loaded — nothing to do")
		return nil
	}

	if c.Source != "" && binaryChanged {
		// STOP before the binary is replaced: swapping the file under a live
		// daemon is what corrupts it, so the old process must be fully gone
		// before installBinary touches its path.
		if isLoaded(c.Label) {
			bootout(c.Label)
			waitUnloaded(c.Label)
		}
		if err := os.MkdirAll(filepath.Dir(c.Program), 0o755); err != nil {
			return err
		}
		if err := installBinary(c.Source, c.Program); err != nil {
			return err
		}
	}

	if err := checkProgram(c); err != nil {
		return err
	}
	if err := makeLogDirs(c); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return err
	}

	if hadPlist && bytes.Equal(current, desired) && isLoaded(c.Label) {
		// Unchanged and already registered: restarting in place picks up a
		// freshly built binary without creating a new Background Task
		// Management record (see the package doc).
		if err := launchctl(true, "kickstart", "-k", domain()+"/"+c.Label); err != nil {
			return err
		}
		r.Ok("restarted " + c.Label + " (plist unchanged)")
		reportPaths(c, r)
		return nil
	}

	if err := os.WriteFile(plist, desired, 0o644); err != nil {
		return err
	}
	// bootout first so a changed plist replaces the running agent instead of
	// failing with "service already loaded".
	bootout(c.Label)
	if err := launchctl(true, "bootstrap", domain(), plist); err != nil {
		return err
	}
	_ = launchctl(false, "enable", domain()+"/"+c.Label)

	verb := "installed and loaded"
	if hadPlist {
		verb = "reloaded"
	}
	r.Ok(verb + " " + c.Label)
	reportPaths(c, r)
	if !hadPlist {
		r.Warn("macOS will show a one-time 'App Background Activity' notification " +
			"for this agent; it is listed under Login Items & Extensions.")
	}
	return nil
}

func uninstall(c Config, r Reporter) error {
	bootout(c.Label)
	_ = os.Remove(plistPath(c.Label))
	r.Ok("unloaded and removed " + c.Label)
	return nil
}

func start(c Config, r Reporter) error {
	plist := plistPath(c.Label)
	if _, err := os.Stat(plist); err != nil {
		return fmt.Errorf("%s does not exist — run `admin service install` first", plist)
	}
	if err := launchctl(true, "bootstrap", domain(), plist); err != nil {
		return err
	}
	r.Ok("loaded " + c.Label)
	return nil
}

func status(c Config, r Reporter) error {
	plist := plistPath(c.Label)
	if _, err := os.Stat(plist); err != nil {
		r.Info("not installed (no " + plist + ")")
		return nil
	}
	out, ok := launchctlOutput("print", domain()+"/"+c.Label)
	if !ok {
		r.Info("installed but not loaded (" + plist + ")")
		return nil
	}
	// `launchctl print` is verbose; these three lines answer "is it up, as what
	// pid, and did it die". Only the top level counts — the nested dictionaries
	// carry their own `state = active` lines, and printing those alongside the
	// service's own state reads as three conflicting answers to one question.
	// launchctl indents top-level keys with exactly one tab.
	wanted := []string{"state = ", "pid = ", "last exit code = "}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "\t\t") {
			continue
		}
		trimmed := strings.TrimSpace(line)
		for _, w := range wanted {
			if strings.HasPrefix(trimmed, w) {
				r.Info(trimmed)
				break
			}
		}
	}
	return nil
}

// --- systemd --user (Linux) -----------------------------------------------

func systemdUnitPath(label string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", label+".service")
}

func systemctl(check bool, args ...string) (string, error) {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	err := cmd.Run()
	if err != nil && check {
		detail := strings.TrimSpace(errBuf.String())
		if detail == "" {
			detail = strings.TrimSpace(out.String())
		}
		return out.String(), fmt.Errorf("systemctl --user %s failed: %s", strings.Join(args, " "), detail)
	}
	return out.String(), nil
}

func systemdIsActive(label string) bool {
	_, err := systemctl(true, "is-active", "--quiet", label+".service")
	return err == nil
}

func installSystemd(c Config, r Reporter) error {
	binaryChanged := true
	if c.Source != "" {
		if _, err := os.Stat(c.Source); err != nil {
			return fmt.Errorf("%s does not exist — build it first", c.Source)
		}
		binaryChanged = !filesIdentical(c.Source, c.Program)
	}

	unitPath := systemdUnitPath(c.Label)
	desired := RenderSystemdUnit(c)
	current, hadUnit := readIfExists(unitPath)

	if c.Source != "" && !binaryChanged && hadUnit && bytes.Equal(current, desired) && systemdIsActive(c.Label) {
		r.Ok(filepath.Base(c.Program) + " unchanged and already running — nothing to do")
		return nil
	}

	if c.Source != "" && binaryChanged {
		if systemdIsActive(c.Label) {
			_, _ = systemctl(false, "stop", c.Label+".service")
		}
		if err := os.MkdirAll(filepath.Dir(c.Program), 0o755); err != nil {
			return err
		}
		if err := installBinary(c.Source, c.Program); err != nil {
			return err
		}
	}

	if err := checkProgram(c); err != nil {
		return err
	}
	if err := makeLogDirs(c); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, desired, 0o644); err != nil {
		return err
	}
	if _, err := systemctl(true, "daemon-reload"); err != nil {
		return err
	}
	if _, err := systemctl(true, "enable", "--now", c.Label+".service"); err != nil {
		return err
	}
	// Persist the service across logout without an active session.
	_ = exec.Command("loginctl", "enable-linger", os.Getenv("USER")).Run()

	verb := "installed and started"
	if hadUnit {
		verb = "reloaded"
	}
	r.Ok(verb + " " + c.Label + " (systemd --user)")
	reportPaths(c, r)
	return nil
}

func uninstallSystemd(c Config, r Reporter) error {
	_, _ = systemctl(false, "disable", "--now", c.Label+".service")
	_ = os.Remove(systemdUnitPath(c.Label))
	_, _ = systemctl(false, "daemon-reload")
	r.Ok("uninstalled " + c.Label + ".service (systemd --user)")
	return nil
}

func stopSystemd(c Config, r Reporter) error {
	_, _ = systemctl(false, "stop", c.Label+".service")
	r.Ok("stopped " + c.Label + ".service (unit left in place — `admin service start` to bring it back)")
	return nil
}

func startSystemd(c Config, r Reporter) error {
	unitPath := systemdUnitPath(c.Label)
	if _, err := os.Stat(unitPath); err != nil {
		return fmt.Errorf("%s does not exist — run `admin service install` first", unitPath)
	}
	return systemctlOK(r, c, "started", "start", c.Label+".service")
}

func systemctlOK(r Reporter, c Config, past string, args ...string) error {
	if _, err := systemctl(true, args...); err != nil {
		return err
	}
	r.Ok(past + " " + c.Label + ".service")
	return nil
}

func statusSystemd(c Config, r Reporter) error {
	unitPath := systemdUnitPath(c.Label)
	if _, err := os.Stat(unitPath); err != nil {
		r.Info("not installed (no " + unitPath + ")")
		return nil
	}
	out, _ := systemctl(false, "show", c.Label+".service",
		"--property=ActiveState,SubState,MainPID,ExecMainStatus")
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			r.Info(line)
		}
	}
	return nil
}

// --- shared ---------------------------------------------------------------

// installBinary copies src over dest, which may be a currently-running
// program. See the package doc for why this can't be a plain copy.
func installBinary(src, dest string) error {
	tmp := fmt.Sprintf("%s.new.%d", dest, os.Getpid())
	defer os.Remove(tmp)

	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath("codesign"); err == nil {
			out, err := exec.Command("codesign", "--force", "--sign", "-", tmp).CombinedOutput()
			if err != nil {
				return fmt.Errorf("could not ad-hoc sign %s: %s", tmp, strings.TrimSpace(string(out)))
			}
		}
	}
	return os.Rename(tmp, dest)
}

func filesIdentical(a, b string) bool {
	da, err := os.ReadFile(a)
	if err != nil {
		return false
	}
	db, err := os.ReadFile(b)
	if err != nil {
		return false
	}
	return bytes.Equal(da, db)
}

func readIfExists(path string) ([]byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return data, true
}

func checkProgram(c Config) error {
	fi, err := os.Stat(c.Program)
	if err != nil {
		return fmt.Errorf("%s does not exist — build and install the binary first", c.Program)
	}
	if fi.Mode()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", c.Program)
	}
	return nil
}

func makeLogDirs(c Config) error {
	for _, p := range []string{c.Stdout, c.Stderr} {
		if p == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func reportPaths(c Config, r Reporter) {
	r.Info("  binary: " + c.Program)
	if c.Stdout != "" {
		r.Info("  log:    " + c.Stdout)
	}
}
