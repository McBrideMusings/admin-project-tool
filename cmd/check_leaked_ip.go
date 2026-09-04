package cmd

import (
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/validate"
)

// admin-cxdv.2.1: a brand-new filesystem-content scanner — no existing
// ${VAR} scan to extend, since internal/argvscan is an unrelated
// command-argv-coverage guard and internal/manifest's envrefs.go/
// envresolve.go only resolve ${VAR} at runtime, never fail a check. This
// walks the files a project is actually about to ship — [deploy].src (an
// rsynced directory) and [install_template].src (a single file) — and fails
// admin check when any of them carries a literal RFC1918 or CGNAT address:
// an internal address baked into a shipped artifact is a leak the manifest's
// own ${VAR} indirection was supposed to prevent.

// leakedIPRange names one CIDR block this check refuses to see shipped
// literally, paired with the label its error message names it by.
type leakedIPRange struct {
	name string
	net  *net.IPNet
}

// leakedIPRanges is the fixed set of "never ship this literally" blocks:
// the three RFC1918 private-address ranges plus the CGNAT range. Built once
// at package init from literal, always-valid CIDR strings.
var leakedIPRanges = mustParseLeakedIPRanges()

func mustParseLeakedIPRanges() []leakedIPRange {
	specs := []struct{ name, cidr string }{
		{"RFC1918 10.0.0.0/8", "10.0.0.0/8"},
		{"RFC1918 172.16.0.0/12", "172.16.0.0/12"},
		{"RFC1918 192.168.0.0/16", "192.168.0.0/16"},
		{"CGNAT 100.64.0.0/10", "100.64.0.0/10"},
	}
	out := make([]leakedIPRange, 0, len(specs))
	for _, s := range specs {
		_, ipnet, err := net.ParseCIDR(s.cidr)
		if err != nil {
			panic("check_leaked_ip: invalid literal CIDR " + s.cidr + ": " + err.Error())
		}
		out = append(out, leakedIPRange{name: s.name, net: ipnet})
	}
	return out
}

// ipv4TokenRe finds IPv4-dotted-quad-shaped tokens in a line. It is
// deliberately loose (each octet just 1-3 digits, no range check) — net.ParseIP
// below is what rejects a malformed or out-of-range candidate like
// "999.1.1.1" or "10.0.0", so the regex only needs to find candidates, never
// validate them.
var ipv4TokenRe = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)

// checkLeakedIPs is admin-cxdv.2.1: fails admin check when a literal
// RFC1918 or CGNAT address appears in any text file under [deploy].src
// (honoring [deploy].exclude — files rsync would skip are not "shipped")
// or in the [install_template].src file. Either table being absent, or its
// resolved path not existing yet on disk, is silently skipped: a missing
// build artifact is a different problem than a leaked address, and not this
// check's job to catch.
func checkLeakedIPs(m *manifest.Manifest) error {
	var hits []leakedIPHit
	var setupErrs []string

	deployRoot := ""
	if files, root, err := deploySrcFiles(m); err != nil {
		setupErrs = append(setupErrs, err.Error())
	} else {
		deployRoot = root
		for _, path := range files {
			hits = append(hits, scanFileForLeakedIPs(path)...)
		}
	}

	if src := kinds.InstallTemplateSrc(m); src != "" {
		path := resolveAgainstRoot(m, src)
		// Skip when this path was already walked as part of [deploy].src —
		// scanning it again would report the same leak twice for the same
		// file+line (admin-cxdv.2.1 round 2).
		if !isUnderRoot(path, deployRoot) {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				hits = append(hits, scanFileForLeakedIPs(path)...)
			}
		}
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].path != hits[j].path {
			return hits[i].path < hits[j].path
		}
		return hits[i].line < hits[j].line
	})

	problems := setupErrs
	for _, h := range hits {
		problems = append(problems, fmt.Sprintf(
			"%s:%d: literal address %s is in %s range — do not ship an internal address",
			h.path, h.line, h.ip, h.rangeName,
		))
	}

	if len(problems) == 0 {
		return nil
	}
	return validate.Errorf("%s", strings.Join(problems, "\n"))
}

// leakedIPHit is one scanFileForLeakedIPs finding, kept structured until
// checkLeakedIPs sorts by path then line — sorting the rendered message
// strings directly would sort "10:" ahead of "2:" within the same file.
type leakedIPHit struct {
	path      string
	line      int
	ip        string
	rangeName string
}

// resolveAgainstRoot resolves a manifest-declared path against m.Root when
// it's relative, so [deploy].src/[install_template].src are read correctly
// regardless of the check's own working directory.
func resolveAgainstRoot(m *manifest.Manifest, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(m.Root, path)
}

// deploySrcFiles walks [deploy].src (when declared) and returns every file
// under it that survives [deploy].exclude, plus the resolved [deploy].src
// root directory itself (so a caller can tell whether some other path
// already falls inside the tree just walked). Both return values are
// zero-valued with no error when [deploy].src is absent/empty or the
// resolved directory doesn't exist yet.
func deploySrcFiles(m *manifest.Manifest) ([]string, string, error) {
	cfg := kinds.ArchetypeConfigTable(m, "deploy")
	src, _ := cfg["src"].(string)
	if src == "" {
		return nil, "", nil
	}
	root := resolveAgainstRoot(m, src)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, "", nil
	}

	var excludes []string
	if raw, ok := cfg["exclude"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				excludes = append(excludes, s)
			}
		}
	}

	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if excludeMatches(rel, d.Name(), excludes) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("[deploy].src %q: %w", src, err)
	}
	return files, root, nil
}

// isUnderRoot reports whether path lies inside root (or equals it) after
// filepath.Clean-ing both sides. root == "" (no [deploy].src resolved) means
// "nothing to be under" and always returns false. The comparison joins root
// with a trailing separator before the prefix check so a root named "deploy"
// does not falsely swallow a sibling like "deploy-extra/file".
func isUnderRoot(path, root string) bool {
	if root == "" {
		return false
	}
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// excludeMatches reports whether rel (the file's path relative to
// [deploy].src, using "/" separators) or its own basename matches any
// exclude pattern, via filepath.Match. This is a documented heuristic, not
// rsync's real filter-rule engine (no anchoring, no "**", no negation) — the
// ticket only requires "respecting [deploy].exclude", not byte-identical
// rsync semantics. A pattern containing "/" is matched against the full
// relative path (so a directory-qualified exclude like "vendor/**" still
// works); a bare pattern is matched against the basename only, mirroring
// internal/kinds/sync.go's matchesExclude precedent for local-copy excludes.
func excludeMatches(rel, base string, excludes []string) bool {
	rel = filepath.ToSlash(rel)
	for _, pat := range excludes {
		if strings.Contains(pat, "/") {
			if ok, _ := filepath.Match(pat, rel); ok {
				return true
			}
			continue
		}
		if ok, _ := filepath.Match(pat, base); ok {
			return true
		}
	}
	return false
}

// isBinaryContent sniffs data for a null byte or invalid UTF-8 — either is
// treated as "not a text file we should line-scan", matching the ticket's
// explicit requirement to skip a compiled binary, image, or sourcemap
// silently rather than produce a spurious failure.
func isBinaryContent(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return !utf8.Valid(data)
}

// scanFileForLeakedIPs reads path fully, skips it silently if it looks
// binary, then scans each line for an IPv4-shaped token that parses as a
// literal address inside one of leakedIPRanges — exact CIDR-arithmetic
// membership via net.IPNet.Contains, not a prefix/regex heuristic, so
// 172.15.x.x and 172.32.x.x correctly pass while 172.16-31.x.x correctly
// fails. Returns one hit per match, in line order.
func scanFileForLeakedIPs(path string) []leakedIPHit {
	data, err := os.ReadFile(path)
	if err != nil || isBinaryContent(data) {
		return nil
	}
	var hits []leakedIPHit
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		for _, tok := range ipv4TokenRe.FindAllString(line, -1) {
			ip := net.ParseIP(tok)
			if ip == nil {
				continue
			}
			ip4 := ip.To4()
			if ip4 == nil {
				continue
			}
			for _, r := range leakedIPRanges {
				if r.net.Contains(ip4) {
					hits = append(hits, leakedIPHit{path: path, line: i + 1, ip: tok, rangeName: r.name})
					break
				}
			}
		}
	}
	return hits
}
