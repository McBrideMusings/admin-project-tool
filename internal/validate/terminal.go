package validate

import (
	"fmt"
	"sort"
	"strings"
)

// TerminalKeys are the only keys a [terminal] table (ADR-0020) accepts: the
// launcher verbs spawn, attach, kill, status and read, and the locale default.
var TerminalKeys = []string{"spawn", "attach", "kill", "status", "read", "locale"}

// terminalKeySet is TerminalKeys as a membership set.
var terminalKeySet = map[string]bool{
	"spawn": true, "attach": true, "kill": true, "status": true, "read": true, "locale": true,
}

// terminalRequired are the keys that must all be present together — a
// [terminal] table declaring only some of them produces a handle nothing
// can reach (a spawn with no attach, say).
var terminalRequired = []string{"spawn", "attach", "kill"}

// Terminal validates the resolved manifest's raw top-level [terminal] table
// (ADR-0020's spawn/attach/kill/status/read launcher contract, plus the
// project-wide `locale` default, "window" or "hosted"; every other key is a string). raw is whatever
// TOML decoded manifest.Manifest.Terminal into — nil/absent (no [terminal]
// table at all) is not an error, since declaring a launcher is opt-in.
//
// Every problem found is collected into one joined *Error (never returned at
// the first), matching RowKeys' collect-then-join style: an unknown key
// names the legal keys, a non-string or empty-string value under a
// legal key is reported, and — only once the table is otherwise well-formed
// enough to check — a [terminal] table missing one of spawn/attach/kill
// while declaring at least one of them reports the missing key by name.
func Terminal(raw any) *Error {
	if raw == nil {
		return nil
	}
	rawMap, ok := raw.(map[string]any)
	if !ok {
		return Errorf("'terminal' must be a table")
	}
	if len(rawMap) == 0 {
		return nil
	}

	var problems []string

	keyNames := make([]string, 0, len(rawMap))
	for k := range rawMap {
		keyNames = append(keyNames, k)
	}
	sort.Strings(keyNames)

	legalList := strings.Join(TerminalKeys, ", ")

	for _, key := range keyNames {
		if !terminalKeySet[key] {
			problems = append(problems, fmt.Sprintf(
				"terminal: unknown key %q — [terminal] accepts only %s", key, legalList))
			continue
		}
		v := rawMap[key]
		if key == "locale" {
			if why := CheckLocale(v); why != "" {
				problems = append(problems, "terminal.locale: "+why)
			}
			continue
		}
		s, isStr := v.(string)
		if !isStr {
			problems = append(problems, fmt.Sprintf(
				"terminal.%s: must be a string (got %#v)", key, v))
			continue
		}
		if strings.TrimSpace(s) == "" {
			problems = append(problems, fmt.Sprintf(
				"terminal.%s: must be a non-empty string", key))
		}
	}

	// A launcher is only needed once something asks for one: a launcher key
	// declared, or locale = "window". locale = "hosted" alone names the
	// dashboard's own pty and needs no spawn command.
	needsLauncher := rawMap["locale"] == LocaleWindow
	for _, k := range TerminalKeys {
		if _, present := rawMap[k]; present && k != "locale" {
			needsLauncher = true
		}
	}

	var missing []string
	for _, req := range terminalRequired {
		if !needsLauncher {
			break
		}
		if _, present := rawMap[req]; !present {
			missing = append(missing, req)
		}
	}
	if len(missing) > 0 {
		declared := make([]string, 0, len(terminalRequired))
		for _, req := range terminalRequired {
			if _, present := rawMap[req]; present {
				declared = append(declared, req)
			}
		}
		var msg string
		if len(declared) == 0 {
			msg = "[terminal] declares none of spawn, attach or kill — spawn, attach and kill are required together"
		} else {
			msg = fmt.Sprintf(
				"[terminal] declares %s but not %s — spawn, attach and kill are required together",
				strings.Join(declared, ", "), strings.Join(missing, ", "))
		}
		problems = append(problems, msg)
	}

	if len(problems) == 0 {
		return nil
	}
	return &Error{msg: strings.Join(problems, "\n")}
}
