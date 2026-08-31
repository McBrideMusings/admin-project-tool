//go:build darwin

// Package luahost's Apple platform surface is darwin-only, gated by this
// build tag on every file that implements it: xcodebuild, simctl, devicectl,
// codesign and syscall.Flock are all macOS-specific, and admin-3rgp.41
// (internal/data's unix.Flock breaking the Windows cross-compile) is exactly
// the class of mistake this tag exists to not repeat. apple_stub.go supplies
// the `!darwin` no-op so register.go's call site compiles everywhere; a
// non-Apple archetype never touches this file at all.
package luahost

// Package-level primitives ported from admin_lib/apple.py — the Apple
// platform surface ADR-0010 calls "business logic for one archetype", bound
// from Go per that ADR's rule ("Bind capability from Go … rather than
// expecting it from Lua's stdlib") exactly like json/glob/plist already are.
//
// Scope of this file and its siblings (apple_xcode.go, apple_devices.go,
// apple_register.go): the OS-facing primitives an archetype's Lua body
// composes into build/dev/deploy commands — running xcodebuild, resolving
// and driving simulators and physical devices, code-signing, and the
// small string/log-parsing helpers those need. Three things admin_lib/
// apple.py also contains are deliberately NOT ported here — see
// docs/architecture/lua-host-api.md's "What is intentionally not here yet"
// for the full reasoning:
//
//   - Continuous dev-loop supervision (dev_mac/dev_ios/dev_tv/dev_device's
//     rebuild-on-signal loop) — needs the reload+output-pump machinery
//     admin-3rgp.11/.17/.35 have not yet ported to Go. Binding it now would
//     mean re-hand-rolling the pump, which the repo's hard rule forbids.
//   - The icon backup/swap/composer subsystem (admin_lib/apple.py's
//     backup_icons/swap_dev_icons/icon-composer functions, ~1000 lines) —
//     a distinct concern (build-time asset substitution) from the platform
//     verbs this ticket names.
//   - Xcode-project introspection for the dev/prod bundle-suffix scheme
//     (_detect_project_path, _read_pbxproj, _compose_dev_settings,
//     _entitlements_paths) — pbxproj parsing and a project-specific naming
//     convention, not a general host capability.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// errWouldBlock signals that flockExclusiveNonblocking found the lock
// already held by another process — the contended case AcquireXcodeLock
// treats as a refusal, not an error.
var errWouldBlock = errors.New("lock already held")

// flockExclusiveNonblocking takes an exclusive, non-blocking flock(2) on f —
// stdlib syscall.Flock, not golang.org/x/sys/unix, precisely so this file's
// darwin build tag is the only thing standing between it and a Windows
// build (see this file's package doc comment).
func flockExclusiveNonblocking(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errWouldBlock
	}
	return err
}

func flockUnlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// execOutput runs name with args and returns its captured stdout. A package
// var (not a method) so tests can redirect it without threading a runner
// through every call site — mirrors the Python original's bare
// subprocess.run(...).stdout reads, which is what every function in this
// file replaces one-for-one.
var execOutput = func(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

// execRun runs name with args, discarding output, for the fire-and-forget
// calls (simctl boot, simctl install, etc.) the Python original made with
// subprocess.run(..., capture_output=True) and never inspected the result of.
var execRun = func(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

// --- Platform catalogs ---------------------------------------------------
// Mirrors admin_lib/apple.py's SIM_PLATFORMS / DEVICE_PLATFORMS /
// APPLE_PLATFORMS tables. Kept as Go data (not Lua) because they are
// referenced by the Go-side functions below (resolveSimulator, deviceInfo
// filtering); an archetype's Lua body needing the raw table gets it via
// apple.sim_platforms() / apple.device_platforms() (see apple_register.go).

type simPlatform struct {
	Label        string
	RuntimeMatch string // substring of the "-- iOS 26.5 --" runtime header
	DeviceMatch  string // substring of the device name
}

var simPlatforms = map[string]simPlatform{
	"ios": {Label: "iOS", RuntimeMatch: "iOS", DeviceMatch: "iPhone"},
	"tv":  {Label: "tvOS", RuntimeMatch: "tvOS", DeviceMatch: "Apple TV"},
}

type devicePlatform struct {
	Label        string
	Destination  string
	Platform     string // xcrun devicectl's hardwareProperties.platform value
	DeviceTypes  []string
	ConnectHints []string
}

var devicePlatforms = map[string]devicePlatform{
	"ios": {
		Label:       "iOS",
		Destination: "generic/platform=iOS",
		Platform:    "iOS",
		DeviceTypes: []string{"iPhone", "iPad"},
		ConnectHints: []string{
			"Connect via USB, or enable wireless debugging:",
			"  Xcode > Window > Devices and Simulators > select device > Connect via network",
		},
	},
	"tv": {
		Label:       "tvOS",
		Destination: "generic/platform=tvOS",
		Platform:    "tvOS",
		DeviceTypes: []string{"appleTV"},
		ConnectHints: []string{
			"Pair the Apple TV once, over the network:",
			"  Apple TV: Settings > Remotes and Devices > Remote App and Devices (leave it open)",
			"  Mac: Xcode > Window > Devices and Simulators > select the Apple TV > enter the PIN",
		},
	},
}

// simPlatformKeys/devicePlatformKeys return the map's keys sorted, so any
// caller reporting "expected one of: ..." is deterministic across runs (a Go
// map's iteration order is not) — matches the Python original's `sorted(...)`
// in the same error paths.
func simPlatformKeys() []string {
	keys := make([]string, 0, len(simPlatforms))
	for k := range simPlatforms {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func devicePlatformKeys() []string {
	keys := make([]string, 0, len(devicePlatforms))
	for k := range devicePlatforms {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- Log predicates --------------------------------------------------------
// Ports admin_lib/apple.py's _LOG_LEVEL_PREDICATES + log_predicate. Pure
// string composition, no process — bound alongside the process-touching
// functions in this file because it is part of the same "platform verbs"
// surface and every caller of it also calls into simctl/devicectl.

var logLevelPredicates = map[string]func(process, subsystem string) string{
	"app": func(process, subsystem string) string {
		return fmt.Sprintf("subsystem == '%s'", subsystem)
	},
	"debug": func(process, subsystem string) string {
		return fmt.Sprintf(
			"(subsystem == '%s' OR process == '%s') "+
				"AND NOT (subsystem BEGINSWITH 'com.apple.launchservices') "+
				"AND NOT (subsystem BEGINSWITH 'com.apple.runningboard')",
			subsystem, process)
	},
	"system": func(process, subsystem string) string {
		return fmt.Sprintf("process == '%s' AND subsystem != '%s'", process, subsystem)
	},
	"verbose": func(process, subsystem string) string {
		return fmt.Sprintf("process == '%s'", process)
	},
}

// LogPredicate returns a macOS `log stream` predicate for the given level
// ("app", "debug", "system", "verbose"), or an error naming the accepted set
// when level is none of those — the single source of truth
// logLevelPredicates's keys are, so the error message can't drift from what
// is actually accepted.
func LogPredicate(process, subsystem, level string) (string, error) {
	build, ok := logLevelPredicates[level]
	if !ok {
		keys := make([]string, 0, len(logLevelPredicates))
		for k := range logLevelPredicates {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return "", fmt.Errorf("unknown log level: %s (expected one of: %s)", level, strings.Join(keys, ", "))
	}
	return build(process, subsystem), nil
}

// --- Build-log diagnosis ----------------------------------------------------
// Ports xcode_is_signed_out, app_store_connect_key_rejected,
// diagnose_build_failure. Pure string matching against already-captured log
// text, so these take the text directly rather than re-reading a path —
// XcodebuildFiltered (apple_xcode.go) is the one caller that has a path.

// XcodeIsSignedOut reports whether buildLog shows a build that failed
// because no Apple ID is signed in — see admin_lib/apple.py's
// xcode_is_signed_out for why the two error shapes checked here are both
// symptoms of the same missing-account cause.
func XcodeIsSignedOut(buildLog string) bool {
	if strings.Contains(buildLog, "No Accounts: Add a new account") {
		return true
	}
	if strings.Contains(buildLog, "requires a provisioning profile with the") {
		return true
	}
	hasWildcard := strings.Contains(buildLog, `Provisioning profile "iOS Team Provisioning Profile: *"`) ||
		strings.Contains(buildLog, `Provisioning profile "Mac Team Provisioning Profile: *"`)
	return hasWildcard && strings.Contains(buildLog, "doesn't include")
}

// AppStoreConnectKeyRejected reports whether buildLog shows xcodebuild
// refusing an -authenticationKey* credential outright (distinct from
// XcodeIsSignedOut — see admin_lib/apple.py's app_store_connect_key_rejected
// for why the remedies are opposites).
func AppStoreConnectKeyRejected(buildLog string) bool {
	return strings.Contains(buildLog, "Authentication failed: Make sure a bearer token was provided")
}

// DiagnoseBuildFailure reads logPath and returns the remediation lines to
// warn() with, or nil if the log is unreadable or names nothing this
// function recognizes. Ports admin_lib/apple.py's diagnose_build_failure,
// minus its side effect of calling warn() itself — the Lua binding
// (apple_register.go) does that so this stays a pure function of the log
// text plus the path it read.
func DiagnoseBuildFailure(logPath string) []string {
	content, err := os.ReadFile(logPath)
	if err != nil {
		return nil
	}
	text := string(content)
	var lines []string
	if strings.Contains(text, "errSecInternalComponent") {
		lines = append(lines,
			"Codesign failed: login keychain is locked.",
			"Unlock it with:",
			"  security unlock-keychain ~/Library/Keychains/login.keychain-db",
			`  security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k YOUR_PASSWORD ~/Library/Keychains/login.keychain-db`,
		)
	}
	if XcodeIsSignedOut(text) {
		lines = append(lines, signedOutMessage()...)
	}
	return lines
}

// signedOutMessage is the fixed explanation admin_lib/apple.py's
// _report_signed_out prints — kept as one function so XcodebuildFiltered's
// retry path and DiagnoseBuildFailure's post-mortem path show the identical
// text rather than two hand-copied versions drifting apart.
func signedOutMessage() []string {
	return []string{
		"This build could not get a provisioning profile carrying the capability it",
		"asked for. Two things cause that, and the log cannot tell them apart:",
		"",
		"  1. Xcode has no Apple ID signed in — by far the more common one, and it",
		"     does not say so: it falls back to the cached wildcard team profile and",
		"     then reports whatever that profile lacks.",
		"       Xcode ▸ Settings (⌘,) ▸ Accounts ▸ + ▸ Apple ID ▸ sign in",
		"",
		"  2. The App ID really is missing the capability.",
		"       developer.apple.com/account ▸ Identifiers ▸ <your bundle id>",
		"",
		"Check 1 first. Neither needs a project setting changed or a profile",
		"downloaded by hand.",
	}
}

// --- App Store Connect API key ---------------------------------------------

// AppStoreConnectAuthFlags returns the -authenticationKey* xcodebuild flags
// when APP_STORE_CONNECT_KEY_PATH/_KEY_ID/_ISSUER_ID are all set and the key
// file exists, or nil otherwise — ports admin_lib/apple.py's
// app_store_connect_auth_flags, minus its "stop offering the key for the
// rest of this run" latch (that's per-run state XcodebuildFiltered owns, not
// this pure function).
func AppStoreConnectAuthFlags() []string {
	path := strings.TrimSpace(os.Getenv("APP_STORE_CONNECT_KEY_PATH"))
	keyID := strings.TrimSpace(os.Getenv("APP_STORE_CONNECT_KEY_ID"))
	issuer := strings.TrimSpace(os.Getenv("APP_STORE_CONNECT_ISSUER_ID"))
	if path == "" || keyID == "" || issuer == "" {
		return nil
	}
	expanded := expandUser(os.ExpandEnv(path))
	if info, err := os.Stat(expanded); err != nil || info.IsDir() {
		return nil
	}
	return []string{
		fmt.Sprintf(`-authenticationKeyPath "%s"`, expanded),
		fmt.Sprintf(`-authenticationKeyID "%s"`, keyID),
		fmt.Sprintf(`-authenticationKeyIssuerID "%s"`, issuer),
	}
}

func expandUser(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// --- App bundle discovery ---------------------------------------------------

// FindApp globs pattern (after expanding a leading ~) and returns the
// newest match by mtime, or "" if nothing matched — ports
// admin_lib/apple.py's find_app.
func FindApp(pattern string) (string, error) {
	matches, err := filepath.Glob(expandUser(pattern))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", nil
	}
	best := matches[0]
	bestMTime := mtimeOrZero(best)
	for _, m := range matches[1:] {
		if mt := mtimeOrZero(m); mt.After(bestMTime) {
			best, bestMTime = m, mt
		}
	}
	return best, nil
}

func mtimeOrZero(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// derivedDataRoot is where CleanDerivedData looks for a project's
// DerivedData directories — a var, not a literal, so a test can point it at
// a temp directory instead of the real
// ~/Library/Developer/Xcode/DerivedData.
var derivedDataRoot = expandUser("~/Library/Developer/Xcode/DerivedData")

// CleanDerivedData removes every DerivedData directory for projectName —
// ports admin_lib/apple.py's clean_derived_data.
func CleanDerivedData(projectName string) error {
	pattern := filepath.Join(derivedDataRoot, fmt.Sprintf("%s-*", projectName))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	for _, m := range matches {
		if err := os.RemoveAll(m); err != nil {
			return err
		}
	}
	return nil
}

// --- Per-checkout Xcode build lock ------------------------------------------
// Ports _acquire_xcode_lock / _release_xcode_lock / run_locked_xcode: two
// admin invocations against the same checkout (a test run started while a
// dev build is in flight) both call xcodebuild, and the second would
// otherwise queue silently behind Xcode's own lock and look like a slow
// build. Refusing outright beats an unannounced multi-minute wait.

const xcodeLockName = "admin-xcode-build.lock"

// AppleLock is a held per-checkout Xcode build lock. The zero value is not
// valid; obtain one from Host.AcquireXcodeLock.
type AppleLock struct {
	f *os.File
}

// AcquireXcodeLock takes the exclusive per-checkout Xcode build lock under
// h.LogDir, or returns (nil, nil) after calling h.Err naming the build
// already running (matching the Python original's "print and return None"
// shape — a caller checks the returned lock for nil, not the error, to
// decide whether to proceed). A non-nil error means the lock file itself
// could not be opened or locked for a reason other than contention.
func (h *Host) AcquireXcodeLock(description string) (*AppleLock, error) {
	lockPath := filepath.Join(h.LogDir, xcodeLockName)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := flockExclusiveNonblocking(f); err != nil {
		if err == errWouldBlock {
			holder, _ := os.ReadFile(lockPath)
			f.Close()
			h.Err(fmt.Sprintf("Refusing to start — %s is already running in this checkout.",
				firstNonEmpty(strings.TrimSpace(string(holder)), "another Xcode build")))
			return nil, nil
		}
		f.Close()
		return nil, err
	}
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	started := time.Now().Format("15:04:05")
	fmt.Fprintf(f, "%s, started at %s (pid %d)", description, started, os.Getpid())
	return &AppleLock{f: f}, nil
}

// ReleaseXcodeLock releases and closes l. A no-op on nil, matching the
// Python original's _release_xcode_lock(None) no-op.
func (h *Host) ReleaseXcodeLock(l *AppleLock) {
	if l == nil {
		return
	}
	_ = flockUnlock(l.f)
	l.f.Close()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// --- Code signing ------------------------------------------------------------

// ResolveSigningIdentity queries the keychain for a code-signing identity
// matching selector (a case-insensitive substring of the identity's
// description — a team name, "Apple Development", etc; empty auto-picks
// when exactly one identity exists), returning its SHA-1 hash. warnLines
// carries the same "using ad-hoc" / "multiple match" explanations the
// Python original prints via warn() when it returns ""/no match — the Lua
// binding is what actually calls h.Warn with them.
// Ports admin_lib/apple.py's resolve_signing_identity.
func ResolveSigningIdentity(selector string) (identity string, warnLines []string) {
	out, err := execOutput("security", "find-identity", "-v", "-p", "codesigning")
	if err != nil {
		return "", []string{"could not run `security find-identity` — using ad-hoc signing"}
	}
	type ident struct{ hash, desc string }
	var ids []ident
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		parenIdx := strings.Index(trimmed, ")")
		if parenIdx < 0 {
			continue
		}
		rest := strings.TrimSpace(trimmed[parenIdx+1:])
		fields := strings.SplitN(rest, " ", 2)
		if len(fields) != 2 || len(fields[0]) != 40 {
			continue
		}
		desc := strings.TrimSpace(fields[1])
		desc = strings.TrimPrefix(desc, `"`)
		desc = strings.TrimSuffix(desc, `"`)
		if desc == fields[1] {
			// no surrounding quotes -> not a real identity row (e.g. the
			// trailing "N valid identities found" summary line)
			continue
		}
		ids = append(ids, ident{hash: fields[0], desc: desc})
	}

	matched := ids
	if selector != "" {
		sel := strings.ToLower(selector)
		matched = nil
		for _, id := range ids {
			if strings.Contains(strings.ToLower(id.desc), sel) {
				matched = append(matched, id)
			}
		}
	}

	switch len(matched) {
	case 1:
		return matched[0].hash, nil
	case 0:
		hint := ""
		if selector != "" {
			hint = fmt.Sprintf(" matching %q", selector)
		}
		return "", []string{fmt.Sprintf(
			"no code-signing identity%s found — using ad-hoc signing "+
				"(macOS will re-prompt for permissions on each rebuild)", hint)}
	default:
		names := make([]string, len(matched))
		for i, id := range matched {
			names[i] = id.desc
		}
		return "", []string{fmt.Sprintf(
			"multiple code-signing identities match — set [apple].sign_identity to "+
				"a unique substring to disambiguate: %s. Using ad-hoc.", strings.Join(names, ", "))}
	}
}

// SignOptions configures Sign. Identifier is the point of this function over
// calling codesign directly — see Sign's doc comment.
type SignOptions struct {
	// Identity is an explicit identity name/hash, or "-" for ad-hoc. Empty
	// resolves $CODE_SIGN_IDENTITY, then the keychain (via
	// ResolveSigningIdentity with no selector), then falls back to ad-hoc.
	Identity string
	// Identifier binds the signature's designated requirement to a stable
	// string instead of the binary's own cdhash — see Sign's doc comment.
	Identifier string
	// Deep passes --deep to codesign (default true when Deep is unset via
	// the zero value — callers that want --no-deep pass DeepSet=true,
	// Deep=false).
	Deep    bool
	DeepSet bool
}

// signResult is what Sign reports back, since it can no longer sys.exit(1)
// like the Python original on failure — the caller (a Lua body via
// apple_register.go) decides what a failed sign means for its own flow.
type SignResult struct {
	Identity string // the identity actually used
	Stable   bool   // true when Identifier was set and the signed attempt used it
}

// Sign code-signs path with a stable identifier, so macOS's TCC keychain
// grants (camera, Documents, …) survive a rebuild. An ad-hoc-signed binary's
// designated requirement is its raw cdhash, which changes every build, so
// without Identifier each rebuild looks like a new app to macOS. Ports
// admin_lib/apple.py's sign().
func Sign(path string, opts SignOptions) (SignResult, error) {
	if _, err := os.Stat(path); err != nil {
		return SignResult{}, fmt.Errorf("sign: no such path: %s", path)
	}
	identity := opts.Identity
	if identity == "" {
		identity = os.Getenv("CODE_SIGN_IDENTITY")
	}
	if identity == "" {
		identity, _ = ResolveSigningIdentity("")
	}
	if identity == "" {
		identity = "-"
	}
	deep := true
	if opts.DeepSet {
		deep = opts.Deep
	}

	base := []string{"codesign", "--force"}
	if deep {
		base = append(base, "--deep")
	}

	type attempt struct {
		args   []string
		stable bool
	}
	var attempts []attempt
	if opts.Identifier != "" {
		a := append(append([]string{}, base...), "--identifier", opts.Identifier, "--sign", identity, path)
		attempts = append(attempts, attempt{args: a, stable: true})
	}
	attempts = append(attempts, attempt{args: append(append([]string{}, base...), "--sign", identity, path), stable: false})

	var lastErr error
	for _, a := range attempts {
		cmd := exec.Command(a.args[0], a.args[1:]...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return SignResult{Identity: identity, Stable: a.stable}, nil
		}
		lastErr = fmt.Errorf("codesign failed for %s: %s", path, strings.TrimSpace(string(out)))
	}
	return SignResult{}, lastErr
}

// DevMacOSPostprocess renames a Debug .app to <devProductName>.app, patches
// CFBundleName/CFBundleDisplayName, and re-signs. Ports
// admin_lib/apple.py's dev_macos_postprocess — see that function's docstring
// (ADR-0007) for why the Dock/Finder tooltip requires renaming the bundle
// file itself rather than only patching the plist. Returns the renamed path.
func DevMacOSPostprocess(builtAppPath, devProductName, devDisplayName, signIdentity string) (string, error) {
	if !strings.HasSuffix(builtAppPath, ".app") {
		return "", fmt.Errorf("dev_macos_postprocess: not an .app bundle: %s", builtAppPath)
	}
	if info, err := os.Stat(builtAppPath); err != nil || !info.IsDir() {
		return "", fmt.Errorf("dev_macos_postprocess: not an .app bundle: %s", builtAppPath)
	}
	infoPlist := filepath.Join(builtAppPath, "Contents", "Info.plist")
	if _, err := os.Stat(infoPlist); err != nil {
		return "", fmt.Errorf("dev_macos_postprocess: missing Info.plist in %s", builtAppPath)
	}

	dest := filepath.Join(filepath.Dir(builtAppPath), devProductName+".app")
	if _, err := os.Stat(dest); err == nil {
		if err := os.RemoveAll(dest); err != nil {
			return "", err
		}
	}
	if err := os.Rename(builtAppPath, dest); err != nil {
		return "", err
	}

	destPlist := filepath.Join(dest, "Contents", "Info.plist")
	if identity := signIdentity; identity == "" {
		signIdentity = "-"
	}
	var warnings []string
	if out, err := exec.Command("plutil", "-replace", "CFBundleDisplayName", "-string", devDisplayName, destPlist).CombinedOutput(); err != nil {
		warnings = append(warnings, fmt.Sprintf("plutil failed to patch CFBundleDisplayName: %s", strings.TrimSpace(string(out))))
	}
	if out, err := exec.Command("plutil", "-replace", "CFBundleName", "-string", devProductName, destPlist).CombinedOutput(); err != nil {
		warnings = append(warnings, fmt.Sprintf("plutil failed to patch CFBundleName: %s", strings.TrimSpace(string(out))))
	}
	if out, err := exec.Command("codesign", "--force", "--sign", signIdentity, dest).CombinedOutput(); err != nil {
		label := "ad-hoc"
		if signIdentity != "-" {
			label = "identity " + signIdentity
		}
		warnings = append(warnings, fmt.Sprintf("codesign (%s) failed: %s", label, strings.TrimSpace(string(out))))
	}
	if len(warnings) > 0 {
		return dest, &postprocessWarning{lines: warnings}
	}
	return dest, nil
}

// postprocessWarning carries non-fatal warnings from DevMacOSPostprocess —
// the rename+patch succeeded (dest is usable) but a cosmetic step failed.
// The caller (apple_register.go) unwraps this to call h.Warn per line
// instead of treating it as a hard failure, matching the Python original's
// warn()-and-continue behaviour.
type postprocessWarning struct{ lines []string }

func (e *postprocessWarning) Error() string { return strings.Join(e.lines, "; ") }
