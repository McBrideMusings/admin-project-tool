package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// tampermonkeyID is Tampermonkey's Chrome Web Store id, stable across installs.
const tampermonkeyID = "dhdgffkkebhmkfjojejmpbldmpobfkfo"

type browser struct {
	Name     string
	BundleID string
	App      string
	UserData string // relative to ~/Library/Application Support
}

// browsers lists every supported browser in display order. Only installed
// ones are acted on.
var browsers = []browser{
	{"Arc", "company.thebrowser.Browser", "/Applications/Arc.app", "Arc/User Data"},
	{"Chrome", "com.google.Chrome", "/Applications/Google Chrome.app", "Google/Chrome"},
	{"Brave", "com.brave.Browser", "/Applications/Brave Browser.app", "BraveSoftware/Brave-Browser"},
}

func (b browser) installed() bool {
	_, err := os.Stat(b.App)
	return err == nil
}

func (b browser) userDataDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", b.UserData)
}

// selectBrowsers returns the installed browsers, limited to names when given.
func selectBrowsers(names []string) []browser {
	var out []browser
	for _, b := range browsers {
		if !b.installed() {
			continue
		}
		if len(names) > 0 && !contains(names, b.Name) {
			continue
		}
		out = append(out, b)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// managedPrefsDir is where macOS keeps *forced* preferences for the current
// user. Chromium tags a value from here MANDATORY; the same key written with a
// plain `defaults write` is only RECOMMENDED, and chrome.storage.managed drops
// everything but MANDATORY. The directory is root-owned, hence sudo.
func managedPrefsDir() string {
	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	return filepath.Join("/Library/Managed Preferences", name)
}

func policyPath(bundleID string) string {
	return filepath.Join(managedPrefsDir(), bundleID+".extensions."+tampermonkeyID+".plist")
}

// writePolicies stages one plist per browser and installs them root-owned in
// a single sudo call, so one password prompt covers every browser.
func writePolicies(entry policyEntry, targets []browser) ([]string, error) {
	tmp, err := os.MkdirTemp("", "admin-tampermonkey-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	var staged, installed []string
	for _, b := range targets {
		p := filepath.Join(tmp, filepath.Base(policyPath(b.BundleID)))
		if err := os.WriteFile(p, policyPlist(entry), 0o644); err != nil {
			return nil, err
		}
		staged = append(staged, p)
		installed = append(installed, policyPath(b.BundleID))
	}
	dir := managedPrefsDir()
	mkdir := exec.Command("sudo", "-p", "[admin tampermonkey] sudo password (writes "+dir+"): ",
		"install", "-d", "-m", "755", "-o", "root", "-g", "wheel", dir)
	if err := runAttached(mkdir); err != nil {
		return nil, err
	}
	args := append([]string{"install", "-m", "644", "-o", "root", "-g", "wheel"}, staged...)
	if err := runAttached(exec.Command("sudo", append(args, dir)...)); err != nil {
		return nil, err
	}
	return installed, nil
}

func runAttached(cmd *exec.Cmd) error {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// readPolicyHashes returns the hash of every jsonImport entry in a browser's
// policy plist, or nil when there is none. plutil reads both XML and binary
// plists.
func readPolicyHashes(bundleID string) []string {
	out, err := exec.Command("plutil", "-convert", "json", "-o", "-", policyPath(bundleID)).Output()
	if err != nil {
		return nil
	}
	var doc struct {
		JSONImport []struct {
			Hash string `json:"hash"`
		} `json:"jsonImport"`
	}
	if json.Unmarshal(out, &doc) != nil {
		return nil
	}
	var hashes []string
	for _, e := range doc.JSONImport {
		hashes = append(hashes, e.Hash)
	}
	return hashes
}

func clearPolicies(targets []browser) error {
	var paths []string
	for _, b := range targets {
		if _, err := os.Stat(policyPath(b.BundleID)); err == nil {
			paths = append(paths, policyPath(b.BundleID))
		}
	}
	if len(paths) == 0 {
		return nil
	}
	return runAttached(exec.Command("sudo", append([]string{"rm", "-f"}, paths...)...))
}

// isRunning reports whether this user's main browser process (not a helper)
// is up.
func isRunning(b browser) bool {
	pattern := "^" + regexp.QuoteMeta(b.App) + "/Contents/MacOS/[^/ ]+( |$)"
	return exec.Command("pgrep", "-u", strconv.Itoa(os.Getuid()), "-f", pattern).Run() == nil
}

// quitBrowser asks the app to quit — it restores its windows on relaunch —
// and waits up to 30s for it to exit.
func quitBrowser(b browser) bool {
	_ = exec.Command("osascript", "-e", fmt.Sprintf("quit app %q", b.App)).Run()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if !isRunning(b) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func launchBrowser(b browser) {
	_ = exec.Command("open", "-a", b.App).Run()
}

// profiles maps each profile directory to its display name, from the
// browser's Local State. Internal profiles (names starting "__") are left out.
func profiles(userData string) map[string]string {
	raw, err := os.ReadFile(filepath.Join(userData, "Local State"))
	if err != nil {
		return nil
	}
	var state struct {
		Profile struct {
			InfoCache map[string]struct {
				Name string `json:"name"`
			} `json:"info_cache"`
		} `json:"profile"`
	}
	if json.Unmarshal(raw, &state) != nil {
		return nil
	}
	out := map[string]string{}
	for dir, info := range state.Profile.InfoCache {
		if strings.HasPrefix(info.Name, "__") {
			continue
		}
		name := info.Name
		if name == "" {
			name = dir
		}
		out[dir] = name
	}
	return out
}

type profileState struct {
	Delivered []string          `json:"delivered"`
	Installed map[string]string `json:"installed"`
	Consumed  []string          `json:"consumed"`
}

// readProfileState reports what reached one profile: the policy hashes in its
// managed storage, the scripts installed (@meta# records) and the policy
// hashes Tampermonkey has already applied.
func readProfileState(userData, profileDir string) profileState {
	base := filepath.Join(userData, profileDir)
	st := profileState{Installed: map[string]string{}}

	managed := leveldbLogKV(filepath.Join(base, "Managed Extension Settings", tampermonkeyID))
	if rec, ok := managed["jsonImport"]; ok && len(rec.value) > 0 {
		raw := rec.value
		var entries []struct {
			Hash string `json:"hash"`
		}
		if json.Unmarshal(raw, &entries) == nil {
			for _, e := range entries {
				st.Delivered = append(st.Delivered, e.Hash)
			}
		} else {
			st.Delivered = []string{"<unparseable>"}
		}
	}

	local := leveldbLogKV(filepath.Join(base, "Local Extension Settings", tampermonkeyID))
	// A script reinstalled under a new uid leaves an older @meta# record with
	// the same name; the highest sequence number is the current one.
	installedSeq := map[string]uint64{}
	for key, lv := range local {
		val := lv.value
		switch {
		case strings.Contains(key, "@meta#"):
			var rec struct {
				Value struct {
					Name    any `json:"name"`
					Version any `json:"version"`
				} `json:"value"`
			}
			if json.Unmarshal(val, &rec) == nil {
				name := fmt.Sprint(rec.Value.Name)
				if seq, seen := installedSeq[name]; !seen || lv.seq > seq {
					installedSeq[name] = lv.seq
					st.Installed[name] = fmt.Sprint(rec.Value.Version)
				}
			}
		case strings.Contains(key, "managed") && strings.Contains(key, "consumed"):
			var rec struct {
				Value map[string]any `json:"value"`
			}
			if json.Unmarshal(val, &rec) == nil {
				st.Consumed = nil
				for h := range rec.Value {
					st.Consumed = append(st.Consumed, h)
				}
				sort.Strings(st.Consumed)
			}
		}
	}
	return st
}

type browserState struct {
	Policy   []string                `json:"policy"`
	Profiles map[string]profileState `json:"profiles"`
}

// collectStatus reports, per installed browser, the policy on disk and what
// reached each profile that has Tampermonkey.
func collectStatus(names []string) map[string]browserState {
	out := map[string]browserState{}
	for _, b := range selectBrowsers(names) {
		st := browserState{Policy: readPolicyHashes(b.BundleID), Profiles: map[string]profileState{}}
		if st.Policy == nil {
			st.Policy = []string{}
		}
		userData := b.userDataDir()
		for dir, label := range profiles(userData) {
			if fi, err := os.Stat(filepath.Join(userData, dir, "Local Extension Settings", tampermonkeyID)); err == nil && fi.IsDir() {
				st.Profiles[label] = readProfileState(userData, dir)
			}
		}
		out[b.Name] = st
	}
	return out
}
