// Command tampermonkey installs a userscript into Tampermonkey on every
// profile of Arc, Chrome and Brave without touching the browsers' storage.
//
// Tampermonkey 5.5+ reads a `jsonImport` list from the extension's managed
// storage every time its service worker starts. Each entry carries a
// provisioning JSON (the format of its Utilities → Export) and Tampermonkey's
// structural hash of it; Tampermonkey installs every script in it and
// remembers the hash, so one document is applied once. Chromium fills managed
// storage from browser policy, which on macOS is a forced preference domain
// per extension:
//
//	/Library/Managed Preferences/<user>/<bundle id>.extensions.<extension id>.plist
//
// So provisioning is: build the JSON, hash it, write one plist per browser
// with one sudo, and quit and relaunch each running browser, because Chromium
// reads policy at launch. The JSON travels inside a data: URL.
//
// Usage:
//
//	go run ./tools/tampermonkey [--browser NAME]... [--no-restart] SCRIPT.user.js...
//	go run ./tools/tampermonkey --status [--json] [--browser NAME]...
//	go run ./tools/tampermonkey --clear [--browser NAME]...
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	fs := flag.NewFlagSet("tampermonkey", flag.ContinueOnError)
	var names multiFlag
	fs.Var(&names, "browser", "limit to this browser (repeatable)")
	status := fs.Bool("status", false, "report policy, delivery and install state, then exit")
	clear := fs.Bool("clear", false, "remove the policy domain(s), then exit")
	asJSON := fs.Bool("json", false, "with --status: machine-readable output")
	noRestart := fs.Bool("no-restart", false, "write the policy without quitting and relaunching a running browser")
	if err := fs.Parse(argv); err != nil {
		return 2
	}

	switch {
	case *clear:
		targets := selectBrowsers(names)
		if err := clearPolicies(targets); err != nil {
			errorf("could not remove the policy: %v", err)
			return 1
		}
		for _, b := range targets {
			okf("%s: policy removed", b.Name)
		}
		return 0
	case *status:
		st := collectStatus(names)
		if *asJSON {
			out, _ := json.MarshalIndent(st, "", "  ")
			fmt.Println(string(out))
			return 0
		}
		return printStatus(st)
	}

	scripts := fs.Args()
	if len(scripts) == 0 {
		errorf("give at least one *.user.js, or --status / --clear")
		return 2
	}
	for _, p := range scripts {
		if fi, err := os.Stat(p); err != nil || fi.IsDir() {
			errorf("not a file: %s", p)
			return 1
		}
	}
	return provision(scripts, names, !*noRestart)
}

// provision writes the policy for every installed target browser whose policy
// is not already current, then quits and relaunches the running ones.
func provision(scripts, names []string, restart bool) int {
	doc, err := buildProvisioning(scripts)
	if err != nil {
		errorf("%v", err)
		return 1
	}
	entry, err := newPolicyEntry(doc)
	if err != nil {
		errorf("%v", err)
		return 1
	}
	targets := selectBrowsers(names)
	if len(targets) == 0 {
		errorf("no supported browser installed (Arc, Chrome, Brave)")
		return 1
	}
	short := entry.Hash[:12] + "…"
	var stale []browser
	for _, b := range targets {
		if hashes := readPolicyHashes(b.BundleID); len(hashes) == 1 && hashes[0] == entry.Hash {
			okf("%s: policy already current (%s) — nothing to do", b.Name, short)
		} else {
			stale = append(stale, b)
		}
	}
	if len(stale) == 0 {
		return 0
	}
	paths, err := writePolicies(entry, stale)
	if err != nil {
		errorf("could not write %s (sudo failed or no tty): %v", managedPrefsDir(), err)
		return 1
	}
	for i, b := range stale {
		okf("%s: jsonImport policy written to %s (%s)", b.Name, paths[i], short)
	}
	rc := 0
	for _, b := range stale {
		if !restart || !isRunning(b) {
			continue
		}
		infof("%s: quitting so the policy is read at launch…", b.Name)
		if quitBrowser(b) {
			launchBrowser(b)
			okf("%s: relaunched", b.Name)
		} else {
			errorf("%s: did not quit within 30s — relaunch it yourself", b.Name)
			rc = 1
		}
	}
	for _, s := range doc.Scripts {
		infof("  script: %s", s.Name)
	}
	infof("Tampermonkey applies the policy when its service worker next starts. Check with: admin tampermonkey-status")
	return rc
}

func printStatus(st map[string]browserState) int {
	if len(st) == 0 {
		warnf("no supported browser installed")
		return 1
	}
	for _, b := range browsers {
		bs, ok := st[b.Name]
		if !ok {
			continue
		}
		infof("%s: policy hashes in Managed Preferences: %s", b.Name, shortList(bs.Policy))
		labels := make([]string, 0, len(bs.Profiles))
		for l := range bs.Profiles {
			labels = append(labels, l)
		}
		sort.Strings(labels)
		for _, label := range labels {
			p := bs.Profiles[label]
			fmt.Printf("  [%s] delivered: %s  consumed: %s\n", label, shortList(p.Delivered), shortList(p.Consumed))
			scripts := make([]string, 0, len(p.Installed))
			for n := range p.Installed {
				scripts = append(scripts, n)
			}
			sort.Strings(scripts)
			for _, n := range scripts {
				fmt.Printf("           installed (seen in log): %s %s\n", n, p.Installed[n])
			}
		}
	}
	return 0
}

func shortList(hashes []string) string {
	if len(hashes) == 0 {
		return "none"
	}
	out := make([]string, len(hashes))
	for i, h := range hashes {
		if len(h) > 12 {
			h = h[:12]
		}
		out[i] = h + "…"
	}
	return "[" + strings.Join(out, ", ") + "]"
}

func infof(f string, a ...any)  { fmt.Printf("\033[36m"+f+"\033[0m\n", a...) }
func okf(f string, a ...any)    { fmt.Printf("\033[32m"+f+"\033[0m\n", a...) }
func warnf(f string, a ...any)  { fmt.Fprintf(os.Stderr, "\033[33m"+f+"\033[0m\n", a...) }
func errorf(f string, a ...any) { fmt.Fprintf(os.Stderr, "\033[31m"+f+"\033[0m\n", a...) }
