//go:build darwin

package luahost

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const rawBuildLogName = "admin-xcode-build.log"

func (h *Host) rawBuildLogPath() string {
	return filepath.Join(h.LogDir, rawBuildLogName)
}

// XcodebuildOptions is the argument to XcodebuildFiltered and
// TargetBuildDir — ports the positional arguments admin_lib/apple.py's
// xcodebuild_filtered/target_build_dir take.
type XcodebuildOptions struct {
	Project     string // .xcodeproj or .xcworkspace path; "" omits -project/-workspace
	Scheme      string
	Config      string
	Destination string
	// Action is the xcodebuild verb ("build", "test", …); defaults to
	// "build" when empty. Unused by TargetBuildDir.
	Action string
	// ExtraSettings is appended verbatim (already-composed KEY=value pairs,
	// e.g. from a future _compose_dev_settings port) — this function does
	// not interpret it.
	ExtraSettings string
	// AllowProvisioning defaults to true when unset; set AllowProvisioningSet
	// to force false.
	AllowProvisioning    bool
	AllowProvisioningSet bool
}

func (o XcodebuildOptions) allowProvisioning() bool {
	if !o.AllowProvisioningSet {
		return true
	}
	return o.AllowProvisioning
}

func (o XcodebuildOptions) projectFlag() string {
	if o.Project == "" {
		return ""
	}
	flag := "-project"
	if strings.HasSuffix(o.Project, ".xcworkspace") {
		flag = "-workspace"
	}
	return fmt.Sprintf(`%s "%s"`, flag, o.Project)
}

// TargetBuildDir asks xcodebuild for TARGET_BUILD_DIR — the exact directory
// the build for this scheme/config/destination writes into — rather than
// globbing DerivedData, which is keyed by the project's absolute path and so
// accumulates one entry per git worktree (see admin_lib/apple.py's
// target_build_dir doc comment for the incident this avoids). Returns ""
// with a nil error if the query ran but the setting wasn't in its output.
func TargetBuildDir(opts XcodebuildOptions) (string, error) {
	parts := []string{"xcodebuild"}
	if f := opts.projectFlag(); f != "" {
		parts = append(parts, f)
	}
	parts = append(parts,
		fmt.Sprintf(`-scheme "%s"`, opts.Scheme),
		fmt.Sprintf("-configuration %s", opts.Config),
		fmt.Sprintf(`-destination "%s"`, opts.Destination),
		"-showBuildSettings",
	)
	if opts.ExtraSettings != "" {
		parts = append(parts, opts.ExtraSettings)
	}
	if opts.allowProvisioning() {
		parts = append(parts, "-allowProvisioningUpdates")
		parts = append(parts, AppStoreConnectAuthFlags()...)
	}

	out, err := execOutput("/bin/sh", "-c", strings.Join(parts, " "))
	if err != nil {
		return "", nil // matches Python: non-zero exit -> None, not an error
	}
	for _, line := range strings.Split(out, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), " = ")
		if found && key == "TARGET_BUILD_DIR" {
			return strings.TrimSpace(value), nil
		}
	}
	return "", nil
}

// XcodebuildResult is what XcodebuildFiltered reports.
type XcodebuildResult struct {
	Ok bool
	// Output is the grep-filtered stream tee'd to the terminal (BUILD/error:/
	// warning: lines) — the same text a caller would have seen scroll by.
	Output string
	// RawLogPath is where the full untruncated build log was written
	// (h.LogDir/admin-xcode-build.log), for DiagnoseBuildFailure or a
	// caller that wants to show more than the filtered stream.
	RawLogPath string
	// Warnings carries the sign-in / API-key-rejection explanations the
	// Python original prints via warn() mid-build — the Lua binding
	// relays these through h.Warn so the caller's log gets them too.
	Warnings []string
}

// XcodebuildFiltered runs xcodebuild with grep-filtered output (BUILD/error:/
// warning: lines only), the full log tee'd to h.LogDir/admin-xcode-build.log,
// under the per-checkout Xcode build lock (AcquireXcodeLock) so a second
// concurrent build in the same checkout refuses immediately instead of
// silently queuing. Ports admin_lib/apple.py's xcodebuild_filtered,
// including its at-most-two-attempt retry: once when the App Store Connect
// key is rejected (drops the key and retries on the Apple ID instead), and
// once when the failure looks like a signed-out Xcode and stdin is an
// interactive terminal (prints instructions and waits for Return).
//
// WARNING (carried over from the Python original): ExtraSettings applies to
// every target in the project, including SwiftPM resource bundles — only
// PRODUCT_BUNDLE_IDENTIFIER is safe there; PRODUCT_NAME and
// INFOPLIST_KEY_CFBundleDisplayName cause "Multiple commands produce"
// failures. Use DevMacOSPostprocess for product/display-name differences.
func (h *Host) XcodebuildFiltered(opts XcodebuildOptions) (XcodebuildResult, error) {
	action := opts.Action
	if action == "" {
		action = "build"
	}
	base := []string{"xcodebuild"}
	if f := opts.projectFlag(); f != "" {
		base = append(base, f)
	}
	base = append(base,
		fmt.Sprintf(`-scheme "%s"`, opts.Scheme),
		fmt.Sprintf("-configuration %s", opts.Config),
		fmt.Sprintf(`-destination "%s"`, opts.Destination),
		action,
	)
	if opts.ExtraSettings != "" {
		base = append(base, opts.ExtraSettings)
	}

	dropKey := false
	compose := func() string {
		tail := append([]string{}, base...)
		if opts.allowProvisioning() {
			tail = append(tail, "-allowProvisioningUpdates")
			if !dropKey {
				tail = append(tail, AppStoreConnectAuthFlags()...)
			}
		}
		tail = append(tail, "2>&1")
		return strings.Join(tail, " ")
	}

	description := fmt.Sprintf(`xcodebuild %s -scheme "%s" -destination "%s"`, action, opts.Scheme, opts.Destination)
	lock, err := h.AcquireXcodeLock(description)
	if err != nil {
		return XcodebuildResult{}, err
	}
	if lock == nil {
		// AcquireXcodeLock already called h.Err naming the running build.
		return XcodebuildResult{Ok: false}, nil
	}
	defer h.ReleaseXcodeLock(lock)

	logPath := h.rawBuildLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return XcodebuildResult{}, err
	}

	var result XcodebuildResult
	result.RawLogPath = logPath

	for attempt := 1; attempt <= 2; attempt++ {
		header := fmt.Sprintf("# %s @ %s\n", h.ProjectRoot, time.Now().Format("2006-01-02 15:04:05"))
		if err := os.WriteFile(logPath, []byte(header), 0o644); err != nil {
			return XcodebuildResult{}, err
		}
		buildCmd := compose()
		// `^\*\*` and not a bare `BUILD`: xcodebuild dumps every build
		// setting as `    export NAME=value`, and dozens of those names
		// contain the substring BUILD — BUILD_DIR, BUILD_ROOT, BUILD_STYLE,
		// CONFIGURATION_BUILD_DIR, FUSE_BUILD_PHASES, even
		// VERSION_INFO_BUILDER. A bare `BUILD` matched all of them, so one
		// tvOS Release deploy printed 74 lines of which 3 were real and the
		// rest were environment noise, burying `** BUILD SUCCEEDED **` and
		// making a working deploy read as a hung one. Every xcodebuild status
		// marker (`** BUILD/CLEAN/TEST SUCCEEDED|FAILED|INTERRUPTED **`)
		// starts at column 0, and no `export` line does, so anchoring is what
		// separates them — measured against a real 121 KB log: 74 lines
		// before, 3 after, no non-export line lost.
		piped := fmt.Sprintf(`%s | tee -a %q | grep -E '^\*\*|error:|warning:' ; exit ${PIPESTATUS[0]}`, buildCmd, logPath)
		res, err := h.RunCmd(piped, RunCmdOptions{CaptureLog: true})
		if err != nil {
			return XcodebuildResult{}, err
		}
		result.Ok = res.Ok
		result.Output = res.Output

		if res.Ok || attempt == 2 {
			return result, nil
		}

		logText, _ := os.ReadFile(logPath)
		text := string(logText)

		if AppStoreConnectKeyRejected(text) && !dropKey {
			result.Warnings = append(result.Warnings,
				"xcodebuild rejected the App Store Connect API key:",
				"  Authentication failed: Make sure a bearer token was provided …",
				"The key is not the only way in, and passing it overrides the Apple ID",
				"signed into Xcode. Retrying without it, on the account instead.")
			dropKey = true
			continue
		}

		if !h.offerXcodeSignIn(text) {
			return result, nil
		}
	}
	return result, nil
}

// offerXcodeSignIn reports whether XcodebuildFiltered should retry: true
// when the failure looks like a signed-out Xcode AND h.Stdin is an
// interactive terminal and the operator pressed Return (anything but
// n/no/q/quit). A non-interactive run (CI, an agent) gets the explanation
// and no wait — a prompt nobody can answer is a hang, not a pause. Ports
// admin_lib/apple.py's _offer_xcode_sign_in.
func (h *Host) offerXcodeSignIn(buildLogText string) bool {
	if !XcodeIsSignedOut(buildLogText) {
		return false
	}
	for _, line := range signedOutMessage() {
		h.Warn(line)
	}

	stdinFile, isFile := h.Stdin.(*os.File)
	if !isFile || !isTTY(stdinFile) {
		h.Warn("Not a terminal, so not waiting — sign in and run this again.")
		return false
	}

	fmt.Fprint(h.Stdout, "Signed in? Press Return to retry, or Ctrl-C to stop: ")
	buf := make([]byte, 256)
	n, err := stdinFile.Read(buf)
	if err != nil {
		fmt.Fprintln(h.Stdout)
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(string(buf[:n])))
	switch answer {
	case "n", "no", "q", "quit":
		return false
	default:
		return true
	}
}
