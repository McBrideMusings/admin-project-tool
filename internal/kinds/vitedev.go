package kinds

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

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/ports"
	"github.com/McBrideMusings/admin-project-tool/internal/reload"
	"github.com/McBrideMusings/admin-project-tool/internal/supervise"
)

// devMount is one parsed `name=path` entry from DEV_MOUNTS.
type devMount struct {
	Name string
	Path string
}

// parseDevMounts parses DEV_MOUNTS. A malformed entry (missing "=", empty
// name/path, or a name of "." or "..") returns (nil, false) — the caller
// treats that as refuse-to-start.
func parseDevMounts(h *luahost.Host, raw string) ([]devMount, bool) {
	var mounts []devMount
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		idx := strings.Index(entry, "=")
		if idx < 0 {
			h.Err(fmt.Sprintf("DEV_MOUNTS entry missing '=': %q", entry))
			return nil, false
		}
		name := strings.TrimSpace(entry[:idx])
		path := expandHome(strings.TrimSpace(entry[idx+1:]))
		if name == "" || path == "" {
			h.Err(fmt.Sprintf("DEV_MOUNTS entry has empty name or path: %q", entry))
			return nil, false
		}
		if name == "." || name == ".." {
			h.Err(fmt.Sprintf("DEV_MOUNTS name must not be '.' or '..': %q", name))
			return nil, false
		}
		mounts = append(mounts, devMount{Name: name, Path: path})
	}
	return mounts, true
}

func expandHome(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// setupDevMounts validates every mount target exists, then ensures
// `dataDir/<name>` is a
// symlink to it — refusing to clobber a non-symlink — then prune any
// symlink directly under dataDir whose name is no longer in mounts.
// mountsOK=false (parseDevMounts's failure signal) is a hard refuse, mounts
// == nil is fine (nothing declared).
func setupDevMounts(h *luahost.Host, dataDir string, mounts []devMount, mountsOK bool) bool {
	if !mountsOK {
		return false
	}
	if len(mounts) == 0 {
		return true
	}

	var missing []devMount
	for _, m := range mounts {
		if fi, err := os.Stat(m.Path); err != nil || !fi.IsDir() {
			missing = append(missing, m)
		}
	}
	if len(missing) > 0 {
		h.Err("DEV_MOUNTS targets unavailable — refusing to start:")
		for _, m := range missing {
			h.Err("  " + m.Name + ": " + m.Path)
		}
		h.Info("Reconnect the share(s) and retry, or unset DEV_MOUNTS.")
		return false
	}

	keep := map[string]bool{}
	for _, m := range mounts {
		keep[m.Name] = true
		link := filepath.Join(dataDir, m.Name)
		_ = os.MkdirAll(filepath.Dir(link), 0o755)
		if target, err := os.Readlink(link); err == nil {
			if target == m.Path {
				continue
			}
			_ = os.Remove(link)
		} else if _, err := os.Lstat(link); err == nil {
			h.Err(link + " exists and is not a symlink — refusing to overwrite")
			return false
		}
		if err := os.Symlink(m.Path, link); err != nil {
			h.Err("could not create mount " + link + ": " + err.Error())
			return false
		}
		h.Ok("mount: " + link + " -> " + m.Path)
	}

	entries, err := os.ReadDir(dataDir)
	if err == nil {
		for _, e := range entries {
			if keep[e.Name()] {
				continue
			}
			link := filepath.Join(dataDir, e.Name())
			if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(link)
				h.Info("pruned stale mount: " + link)
			}
		}
	}
	return true
}

// ensureDeps installs only when checkPath is missing. dir is where
// installCmd runs, and
// checkPath is resolved against it — both are project-relative, so neither
// may be left to the process's own cwd.
func ensureDeps(h *luahost.Host, dir, checkPath, installCmd string) error {
	if _, err := os.Stat(resolveUnder(dir, checkPath)); err == nil {
		return nil
	}
	h.Info("Installing dependencies...")
	res, err := h.RunCmd(installCmd, luahost.RunCmdOptions{Dir: dir, CaptureLog: true})
	if err != nil {
		return err
	}
	if !res.Ok {
		return fmt.Errorf("%s failed", installCmd)
	}
	return nil
}

// viteDevOptions is runViteDev's parameter list.
type viteDevOptions struct {
	VitePort     int
	BackendPort  int
	DataDir      string
	FrontendDir  string
	BackendDir   string
	BackendCmd   string
	BackendEnv   map[string]string
	ViteEnv      map[string]string
	DevMountsRaw string
	HasDevMounts bool // DEV_MOUNTS explicitly passed (even if empty) vs. read from the environment
	ViteLogName  string
	// Unsupervised is the action's `supervise = false`.
	Unsupervised bool
}

// runViteDev runs Vite detached in the background (its own process group,
// log redirected to tmp/<ViteLogName>); the backend runs in the foreground
// under reload.RunWithReload so a SIGUSR1 reload/restart applies to the
// backend only. Returns the backend's exit code.
//
// A startup sweep for orphaned nodemon/cargo-watch processes left by an
// uncleanly-shut-down prior dev session is out of scope here: it would be a
// project-wide ps/argv sweep, admin-3rgp.15's scope, not vite-dev's own
// preflight. PreflightPort (this file's other dependency, internal/ports)
// still catches the common case — a stale process still holding the port —
// which is the failure such a sweep exists to prevent for a process that
// changed ports or exited between kills.
func runViteDev(h *luahost.Host, o viteDevOptions) int {
	frontend := resolveUnder(h.ProjectRoot, o.FrontendDir)
	pkg := pkgRunner(frontend)
	if err := ensureDeps(h, frontend, "node_modules", pkg+" install"); err != nil {
		h.Err(pkg + " install failed: " + err.Error())
		return 1
	}

	if err := os.MkdirAll(o.DataDir, 0o755); err != nil {
		h.Err("could not create data dir " + o.DataDir + ": " + err.Error())
		return 1
	}

	devMountsRaw := o.DevMountsRaw
	if !o.HasDevMounts {
		devMountsRaw = os.Getenv("DEV_MOUNTS")
	}
	mounts, mountsOK := parseDevMounts(h, devMountsRaw)
	if !setupDevMounts(h, o.DataDir, mounts, mountsOK) {
		return 1
	}

	if !ports.PreflightPort(o.VitePort, "vite") {
		return 1
	}
	if !ports.PreflightPort(o.BackendPort, "backend") {
		return 1
	}

	logDir := h.LogDir
	_ = os.MkdirAll(logDir, 0o755)
	viteLogName := o.ViteLogName
	if viteLogName == "" {
		viteLogName = "vite.log"
	}
	viteLog := filepath.Join(logDir, viteLogName)
	viteFh, err := os.Create(viteLog)
	if err != nil {
		h.Err("could not create vite log " + viteLog + ": " + err.Error())
		return 1
	}
	defer viteFh.Close()

	mergedViteEnv := map[string]string{"VITE_PORT": strconv.Itoa(o.VitePort)}
	for k, v := range o.ViteEnv {
		mergedViteEnv[k] = v
	}
	envParts := make([]string, 0, len(mergedViteEnv))
	for _, k := range sortedStringMapKeys(mergedViteEnv) {
		envParts = append(envParts, k+"="+shellQuote(mergedViteEnv[k]))
	}
	inner := pkg + " run dev"
	if len(envParts) > 0 {
		// Env prefix must apply to `<pkg> run dev`, not to `cd` — putting it
		// before the chain leaks the var only to `cd`, which ignores it.
		inner = strings.Join(envParts, " ") + " " + inner
	}
	// Dir rather than a `cd` prefix: FrontendDir is relative to the project
	// (it defaults to "web"), so a `cd` only lands in the right place when
	// the shell already starts there — which is a fact about where `admin`
	// was typed, not about the project.
	viteProc := exec.Command("/bin/sh", "-c", inner)
	viteProc.Dir = frontend
	viteProc.Stdin = nil
	viteProc.Stdout = viteFh
	viteProc.Stderr = viteFh
	viteProc.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	viteRun := supervise.Start(viteProc, h.ProjectRoot, !o.Unsupervised)
	if err := viteProc.Start(); err != nil {
		h.Err("could not start vite: " + err.Error())
		return 1
	}
	defer func() {
		if viteProc.Process != nil {
			_ = syscall.Kill(-viteProc.Process.Pid, syscall.SIGTERM)
			_ = viteProc.Wait()
			viteRun.Stop()
		}
	}()

	ready := ports.WaitForPort(o.VitePort, 15*time.Second)

	domain := strings.TrimSpace(os.Getenv("DEV_DOMAIN"))
	devURL := fmt.Sprintf("http://localhost:%d", o.VitePort)
	if domain != "" {
		devURL = fmt.Sprintf("https://%d.%s", o.VitePort, domain)
	}
	if ready {
		h.Ok("Vite dev server: " + devURL)
	} else {
		h.Warn("Vite dev server (may not be ready): " + devURL)
	}
	h.Info("Vite log: " + viteLog)
	fmt.Fprintln(h.Stdout)

	var allowedRootsList []string
	for _, m := range mounts {
		if real, err := filepath.EvalSymlinks(m.Path); err == nil {
			allowedRootsList = append(allowedRootsList, real)
		} else {
			allowedRootsList = append(allowedRootsList, m.Path)
		}
	}
	allowedRoots := strings.Join(allowedRootsList, ",")

	backendEnvParts := make([]string, 0, len(o.BackendEnv)+2)
	for _, k := range sortedStringMapKeys(o.BackendEnv) {
		backendEnvParts = append(backendEnvParts, k+"="+shellQuote(o.BackendEnv[k]))
	}
	backendEnvParts = append(backendEnvParts, "PORT="+strconv.Itoa(o.BackendPort))
	if allowedRoots != "" {
		backendEnvParts = append(backendEnvParts, "ALLOWED_SYMLINK_ROOTS="+shellQuote(allowedRoots))
	}
	envStr := strings.Join(backendEnvParts, " ")

	// `exec env ...` so the shell replaces itself with the backend binary
	// (cargo-watch / nodemon / etc.) instead of fork-exec'ing it: admin's
	// pdeathsig-equivalent (Setpgid + explicit killpg here) only reaches the
	// shell otherwise.
	backendCmd := "exec env " + envStr + " " + o.BackendCmd
	return reload.RunWithReload(backendCmd, "dev", reload.Options{
		// Same reason as vite's own Dir above: BackendDir is relative to the
		// project (it defaults to "."), and the reload loop respawns the
		// child repeatedly, so an inherited cwd is wrong every restart.
		Dir:          resolveUnder(h.ProjectRoot, o.BackendDir),
		Root:         h.ProjectRoot,
		Unsupervised: o.Unsupervised,
		Stdout:       h.Stdout,
		LogFn:        h.AppendLog,
		Info:         h.Info,
		Warn:         h.Warn,
	})
}

func sortedStringMapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// viteDevFromConfig reads the manifest's [vite_dev] table and calls
// runViteDev.
func viteDevFromConfig(h *luahost.Host, cfg map[string]any, unsupervised bool) int {
	opts, err := buildViteDevOptions(h.ProjectRoot, cfg)
	if err != nil {
		h.Err(err.Error())
		return 1
	}
	opts.Unsupervised = unsupervised
	return runViteDev(h, opts)
}

// buildViteDevOptions does the config-table-to-viteDevOptions translation,
// split out from viteDevFromConfig so it's testable without spinning up a
// real vite/backend process. It still performs the real side effect of
// creating each data_subdirs directory (and the plain data root when none is
// configured as the root) — that part of the adapter has nothing to do with
// launching a process, so a test can assert on the filesystem exactly as
// production does.
func buildViteDevOptions(root string, cfg map[string]any) (viteDevOptions, error) {
	if len(cfg) == 0 {
		return viteDevOptions{}, fmt.Errorf("vite_dev_from_config: empty config")
	}

	dataDirRaw := cfgString(cfg, "data_dir", "data")
	// Against the project root, not the process cwd: data_dir defaults to
	// the relative "data", and filepath.Abs would anchor it to wherever
	// `admin` was typed.
	projectData := resolveUnder(root, dataDirRaw)
	subdirs := cfgList(cfg, "data_subdirs")
	backendEnv := cfgStringMap(cfgMap(cfg, "backend_extra_env"))

	var dataRootCount int
	for _, raw := range subdirs {
		if s, ok := raw.(map[string]any); ok && cfgBool(s, "is_data_root", false) {
			dataRootCount++
		}
	}
	if dataRootCount > 1 {
		return viteDevOptions{}, fmt.Errorf("vite_dev: multiple data_subdirs have is_data_root=true")
	}

	var dataRoot string
	for i, raw := range subdirs {
		sub, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := cfgString(sub, "name", "")
		path := filepath.Join(projectData, name)
		_ = os.MkdirAll(path, 0o755)
		envName := cfgString(sub, "export_as", "")
		if envName == "" {
			envName = cfgString(sub, "env", "")
		}
		if envName != "" {
			backendEnv[envName] = path
		}
		if cfgBool(sub, "is_data_root", false) || (dataRoot == "" && i == 0) {
			dataRoot = path
		}
	}
	if dataRoot == "" {
		_ = os.MkdirAll(projectData, 0o755)
		dataRoot = projectData
	}

	rootEnv := cfgString(cfg, "data_root_env", "")
	if rootEnv != "" {
		backendEnv[rootEnv] = dataRoot
	}

	for _, raw := range cfgList(cfg, "forward_env") {
		name, ok := raw.(string)
		if !ok {
			continue
		}
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			backendEnv[name] = v
		}
	}

	var mountParts []string
	for _, raw := range cfgList(cfg, "mounts") {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := cfgString(m, "name", "")
		if name == "" {
			continue
		}
		var path string
		if p, ok := m["path"].(string); ok {
			path = manifest.ResolveComputed(p)
		} else {
			envVal := os.Getenv(cfgString(m, "env", ""))
			if envVal != "" {
				path = envVal
			} else {
				path = manifest.ResolveComputed(cfgString(m, "default", ""))
			}
		}
		if path != "" {
			mountParts = append(mountParts, name+"="+path)
		}
	}

	return viteDevOptions{
		VitePort:     cfgInt(cfg, "vite_port", 0),
		BackendPort:  cfgInt(cfg, "backend_port", 0),
		DataDir:      dataRoot,
		FrontendDir:  cfgString(cfg, "frontend_dir", "web"),
		BackendDir:   cfgString(cfg, "backend_dir", "."),
		BackendCmd:   cfgString(cfg, "backend_cmd", "node server.js"),
		BackendEnv:   backendEnv,
		ViteEnv:      cfgStringMap(cfgMap(cfg, "vite_env")),
		DevMountsRaw: strings.Join(mountParts, ","),
		HasDevMounts: len(mountParts) > 0,
	}, nil
}

// makeViteDev is kind="vite-dev"'s factory. `pre_kill` (admin-tlhw.3) names
// whatever command or action the project wants run before this dev loop
// starts — absent means no hook fires.
func makeViteDev(name string, action manifest.Action, m *manifest.Manifest, surf *Surface) (Runner, error) {
	cfg := archetypeConfigTable(m, "vite_dev")
	preRun := cfgString(cfg, "pre_kill", "")

	return func(args []string) error {
		h := newHost(m)
		runPreHook(m, surf, preRun, args, h.Warn)
		rc := viteDevFromConfig(h, cfg, !superviseEnabled(action))
		if rc != 0 && rc != 130 {
			return fmt.Errorf("action %q: vite-dev exited %d", name, rc)
		}
		return nil
	}, nil
}
