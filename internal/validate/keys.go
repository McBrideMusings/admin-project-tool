package validate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/keymap"
)

// RowBinding is one entry out of a resolved [keys.<row-kind>] table: a key
// bound, for rows of one kind, to the name of a command declared elsewhere
// in the same manifest.
type RowBinding struct {
	Kind    keymap.RowKind
	Key     string
	Command string
}

// reservedKeys is ADR-0018's Consequence "a reserved set is never
// bindable" — navigation and escape a config file cannot take away.
var reservedKeys = map[string]bool{
	"q": true, "ctrl+c": true, "esc": true, "tab": true, "shift+tab": true,
	"up": true, "down": true, "left": true, "right": true,
	"j": true, "k": true, "1": true, "2": true, "3": true, "4": true, "/": true,
}

// RowKeys parses and validates the raw top-level `[keys]` table — the
// (row kind, key) -> command lookup ADR-0018 describes. It is a distinct
// entry point from ParseKeys, which validates an unrelated feature (an
// interactive-shell/bridge-listen action's own `keys` table of run/action/
// signal bindings); the two share a manifest keyword but nothing else.
//
// raw is whatever TOML decoded the resolved manifest's top-level "keys"
// value into — nil/absent (no [keys] table at all) returns no bindings, no
// shadows, and no error, so a manifest that predates this feature is
// unaffected in every respect. commands is the set of command names the
// manifest actually declares (after archetype merge), used to catch a
// binding that names a command that doesn't exist.
//
// Every problem found is collected into one joined *Error (never returned
// at the first), matching CommandRouting/checkCommandRouting's shape.
// Shadowing a built-in is reported through the separate shadows return
// value rather than err — ADR-0018's "shadowing is reported, not blocked":
// it is not a validation failure, so admin check must still exit 0.
func RowKeys(raw any, commands map[string]bool) (bindings []RowBinding, shadows []string, err *Error) {
	if raw == nil {
		return nil, nil, nil
	}
	rawMap, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, Errorf("'keys' must be a table")
	}

	var problems []string

	kindNames := make([]string, 0, len(rawMap))
	for k := range rawMap {
		kindNames = append(kindNames, k)
	}
	sort.Strings(kindNames)

	for _, kindName := range kindNames {
		kind, ok := keymap.Kind(kindName)
		if !ok {
			problems = append(problems, notBindableMessage(kindName))
			continue
		}

		rowRaw, ok := rawMap[kindName].(map[string]any)
		if !ok {
			problems = append(problems, fmt.Sprintf("keys.%s: must be a table", kindName))
			continue
		}

		keyNames := make([]string, 0, len(rowRaw))
		for k := range rowRaw {
			keyNames = append(keyNames, k)
		}
		sort.Strings(keyNames)

		builtinLabel := builtinLabels(kind)

		for _, key := range keyNames {
			lead := fmt.Sprintf("keys.%s.%s", kindName, key)
			v := rowRaw[key]
			cmdName, ok := v.(string)
			if !ok {
				problems = append(problems, fmt.Sprintf("%s: must be a string naming a command (got %#v)", lead, v))
				continue
			}

			if reservedKeys[key] {
				problems = append(problems, fmt.Sprintf(
					"%s: key %q is reserved — navigation and escape (q, ctrl+c, esc, tab, shift+tab, the arrow keys, j, k, 1-4, /) cannot be rebound",
					lead, key))
				continue
			}

			if !commands[cmdName] {
				problems = append(problems, fmt.Sprintf(
					"%s: names command %q, which is not declared in this manifest", lead, cmdName))
				continue
			}

			bindings = append(bindings, RowBinding{Kind: kind, Key: key, Command: cmdName})

			if label, shadowed := builtinLabel[key]; shadowed {
				shadows = append(shadows, fmt.Sprintf("%s shadows the %s", lead, label))
			}
		}
	}

	if len(problems) > 0 {
		return bindings, shadows, &Error{msg: strings.Join(problems, "\n")}
	}
	return bindings, shadows, nil
}

// notBindableMessage builds the "not a bindable row kind" error for a
// [keys.<word>] table name that isn't one of keymap.Bindable — naming the
// Work tab's own rows specifically when that's what was written, per
// acceptance criteria that the message lists the bindable kinds either way.
func notBindableMessage(word string) string {
	names := make([]string, len(keymap.Bindable))
	for i, k := range keymap.Bindable {
		names[i] = string(k)
	}
	list := strings.Join(names, ", ")

	for _, workRow := range keymap.NotBindable {
		if word == workRow {
			return fmt.Sprintf(
				"keys.%s: %q is one of the Work tab's own rows (command, commandTarget, worker) — its Enter already means \"run this command\" / \"attach to this worker\" and can't be rebound. Bindable row kinds: %s",
				word, word, list)
		}
	}
	return fmt.Sprintf("keys.%s: %q is not a bindable row kind. Bindable row kinds: %s", word, word, list)
}

// builtinLabels indexes keymap.Builtins(kind) by Key for shadow lookup,
// skipping any Binding with no bindable Key (e.g. `↑↓ move`, `⌥↵/W
// worktree`) since a manifest cannot bind a key to those in the first
// place.
func builtinLabels(kind keymap.RowKind) map[string]string {
	out := map[string]string{}
	for _, b := range keymap.Builtins(kind) {
		if b.Key == "" {
			continue
		}
		out[b.Key] = b.Label
	}
	return out
}
