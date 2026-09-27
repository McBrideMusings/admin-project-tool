package dockerrun

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// memFlags is the set of flags whose value is a byte size, normalized to
// bytes before drift compare so "24g" and "25769803776" don't read as
// divergent.
var memFlags = map[string]bool{
	"--memory": true, "-m": true, "--memory-swap": true,
	"--memory-reservation": true, "--shm-size": true,
}

var memSizeRe = regexp.MustCompile(`(?i)^\s*(\d+(?:\.\d+)?)\s*([bkmg]?)\s*$`)

// NormalizeMemBytes canonicalizes a Docker --memory-shaped value to an
// integer byte-count string, matching docker's 1024-based RAMInBytes size
// suffixes (b/k/m/g, case-insensitive, fractional magnitudes allowed).
// Returns the value unchanged if it doesn't parse as a size.
func NormalizeMemBytes(value string) string {
	m := memSizeRe.FindStringSubmatch(value)
	if m == nil {
		return value
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return value
	}
	mult := 1.0
	switch strings.ToLower(m[2]) {
	case "k":
		mult = 1024
	case "m":
		mult = 1024 * 1024
	case "g":
		mult = 1024 * 1024 * 1024
	}
	return strconv.FormatInt(int64(n*mult), 10)
}

// ArgvPairAt reads the flag/value pair starting at argv[i], returning
// (flag, value, hasValue, nextIndex). The single source of truth for "does
// this flag take a value" question. Arity comes from BooleanFlags, never from whether the next token happens
// to start with "-" (a value can legitimately start with "-", e.g.
// `--ulimit core=-1`).
func ArgvPairAt(argv []string, i int) (flag string, value string, hasValue bool, next int) {
	flag = argv[i]
	if BooleanFlags[flag] || i+1 >= len(argv) {
		return flag, "", false, i + 1
	}
	return flag, argv[i+1], true, i + 2
}

// ArgvToPairSet canonicalizes an argv-pieces list into a set of
// "flag\tvalue" strings so two lists with different orderings compare
// cleanly. Byte-size flags are normalized to a byte count first.
func ArgvToPairSet(argv []string) map[string]bool {
	out := map[string]bool{}
	i := 0
	for i < len(argv) {
		flag, value, hasValue, next := ArgvPairAt(argv, i)
		i = next
		if !hasValue {
			out[flag+"\t"] = true
			continue
		}
		if memFlags[flag] {
			value = NormalizeMemBytes(value)
		}
		out[flag+"\t"+value] = true
	}
	return out
}

// ComputeDrift diffs target (from TOML) against live (from docker inspect).
// Returns (inTargetOnly, inLiveOnly), each a sorted list of "flag\tvalue"
// strings. Strict semantics: any divergence in either direction counts as
// drift.
func ComputeDrift(targetArgv, liveArgv []string) (inTargetOnly, inLiveOnly []string) {
	targetSet := ArgvToPairSet(targetArgv)
	liveSet := ArgvToPairSet(liveArgv)
	for k := range targetSet {
		if !liveSet[k] {
			inTargetOnly = append(inTargetOnly, k)
		}
	}
	for k := range liveSet {
		if !targetSet[k] {
			inLiveOnly = append(inLiveOnly, k)
		}
	}
	sort.Strings(inTargetOnly)
	sort.Strings(inLiveOnly)
	return inTargetOnly, inLiveOnly
}

// LiveArgvForDrift builds the live-side argv for drift comparison, stripping
// noise that's never expressible from TOML so the report only shows
// actionable drift. cfg (nil-safe) is the [docker_run] table being compared
// against, needed to resolve DeclaredOnly/ImageField rows.
func LiveArgvForDrift(inspect map[string]any, imageEnv []string, imageLabels map[string]string, imageWorkdir, imageUser string, cfg map[string]any) []string {
	args := BuildArgsFromInspect(inspect)
	imageEnvSet := map[string]bool{}
	for _, e := range imageEnv {
		imageEnvSet[e] = true
	}
	if cfg == nil {
		cfg = map[string]any{}
	}

	var filtered []string
	i := 0
	for i < len(args) {
		flag, val, hasVal, next := ArgvPairAt(args, i)
		i = next

		if flag == "--network" && val == "bridge" {
			continue
		}
		if flag == "-e" && imageEnvSet[val] {
			continue
		}
		if flag == "-e" && hasVal {
			name := val
			if idx := strings.Index(val, "="); idx >= 0 {
				name = val[:idx]
			}
			switch name {
			case "HOST_CONTAINERNAME", "HOST_HOSTNAME", "HOST_OS", "HOST_CONFIGURABLE":
				continue
			}
		}
		if flag == "--label" && hasVal {
			if idx := strings.Index(val, "="); idx >= 0 {
				k, v := val[:idx], val[idx+1:]
				if imageLabels[k] == v {
					continue
				}
			}
		}
		if row, ok := liveStripRows[flag]; ok && !truthy(cfg[row.Key]) {
			if row.DeclaredOnly {
				continue
			}
			if row.ImageField == "workdir" && val == imageWorkdir {
				continue
			}
			if row.ImageField == "user" && val == imageUser {
				continue
			}
		}
		if flag == "-v" && hasVal && strings.HasSuffix(val, ":rw") {
			filtered = append(filtered, flag, val[:len(val)-3])
			continue
		}

		filtered = append(filtered, flag)
		if hasVal {
			filtered = append(filtered, val)
		}
	}
	return filtered
}

// FormatDriftReport renders a drift report for human eyeballs. "-e" entries
// are masked by variable name (IsSecretEnvName) before printing, same as
// the deploy-time dry-run preview and admin diff's own masking. Returns ""
// when there is no drift.
func FormatDriftReport(inTargetOnly, inLiveOnly []string, refusing bool) string {
	if len(inTargetOnly) == 0 && len(inLiveOnly) == 0 {
		return ""
	}

	maskEntry := func(entry string) string {
		idx := strings.Index(entry, "\t")
		if idx < 0 {
			return entry
		}
		flag, value := entry[:idx], entry[idx+1:]
		if flag == "-e" {
			value = MaskEnvAssignment(value)
		}
		return flag + "\t" + value
	}

	fmtBlock := func(label string, entries []string) string {
		if len(entries) == 0 {
			return ""
		}
		masked := make([]string, len(entries))
		width := 0
		for i, e := range entries {
			masked[i] = maskEntry(e)
			flag := masked[i]
			if idx := strings.Index(flag, "\t"); idx >= 0 {
				flag = flag[:idx]
			}
			if len(flag) > width {
				width = len(flag)
			}
		}
		rows := make([]string, len(masked))
		for i, e := range masked {
			idx := strings.Index(e, "\t")
			flag, value := e, ""
			if idx >= 0 {
				flag, value = e[:idx], e[idx+1:]
			}
			rows[i] = "    " + flag + strings.Repeat(" ", width-len(flag)) + "  " + value
		}
		return "  " + label + ":\n" + strings.Join(rows, "\n") + "\n"
	}

	var out []string
	out = append(out, "Drift detected between live container and admin.toml ([docker_run]):\n")
	if b := fmtBlock("target has, live missing", inTargetOnly); b != "" {
		out = append(out, b)
	}
	if b := fmtBlock("live has, target missing", inLiveOnly); b != "" {
		out = append(out, b)
	}
	if refusing {
		out = append(out, "Refusing to deploy. Either back-port the live values to admin.toml,\n"+
			"or rerun with --force to overwrite (live state will be discarded).")
	}
	return strings.Join(out, "\n")
}
