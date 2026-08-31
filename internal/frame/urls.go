package frame

import (
	"fmt"
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// ShouldActivate is the pure decision admin_lib/interp.py's _enter_frame
// makes before ever touching frame.activate(): footer, when present,
// overrides the URL list in both directions (true pins a footer for an
// action with nothing to link — the elapsed-time rule and the log path are
// worth having on their own; false suppresses one the URLs would have
// raised). hasFooter=false (the key was absent from the manifest) means
// "decide from resolvedURLCount" — the original, pre-footer-key behaviour.
func ShouldActivate(resolvedURLCount int, hasFooter, footerVal bool) bool {
	if hasFooter {
		return footerVal
	}
	return resolvedURLCount > 0
}

// ResolveURLs turns a raw `urls` array (as decoded from TOML — each element
// a string or a {label=, url=} table) into resolved URLEntry rows, in two
// steps per entry:
//
//  1. An entry naming a key of the manifest's top-level [urls] table
//     resolves to that entry, label and all — so an action reuses what the
//     project already declared instead of restating the string. This
//     REPLACES any label the entry itself carried, matching
//     admin_lib/frame.py's resolve_urls exactly (a custom label on an entry
//     that also names a project URL is discarded in favour of the name).
//  2. ${VAR} is expanded — manifest.ResolveComputed first (derived vars
//     like ${LAN_IP} are not environment variables, and ResolveEnv alone
//     would leave a stale literal if a caller ever swaps it for something
//     that doesn't also know about ComputedVars), then manifest.ResolveEnv
//     for everything else.
//
// An entry that resolves to an empty URL is dropped, matching Python's
// trailing `if url` filter. Returns the first ResolveEnv error verbatim —
// same "library returns the error, caller decides" shape ResolveEnv itself
// documents.
func ResolveURLs(raw []any, m *manifest.Manifest) ([]URLEntry, error) {
	var out []URLEntry
	for _, item := range raw {
		label, rawURL, err := oneURLEntry(item)
		if err != nil {
			return nil, err
		}
		if m != nil {
			if projectURL, ok := m.Urls[rawURL]; ok {
				label, rawURL = rawURL, projectURL
			}
		}
		resolved := manifest.ResolveComputed(rawURL)
		resolved, err = manifest.ResolveEnv(resolved)
		if err != nil {
			return nil, err
		}
		if resolved == "" {
			continue
		}
		out = append(out, URLEntry{Label: label, URL: resolved})
	}
	return out, nil
}

func oneURLEntry(item any) (label, url string, err error) {
	switch v := item.(type) {
	case string:
		return v, v, nil
	case map[string]any:
		u, _ := v["url"].(string)
		if l, ok := v["label"].(string); ok {
			return l, u, nil
		}
		return u, u, nil
	default:
		return "", "", fmt.Errorf("urls entry must be a string or a {label=, url=} table, got %T", item)
	}
}

// reservedTargetKeys are the top-level keys on a kind="multi-target" action
// that are never themselves an inline target name — mirrors
// internal/kinds.multiTargetReserved, which this ticket adds "urls" and
// "footer" to for exactly this reason. Duplicated rather than imported:
// internal/kinds already depends on internal/manifest, and importing
// internal/kinds from here (internal/frame, which internal/kinds also
// depends on for its own footer wiring) would cycle.
var reservedTargetKeys = map[string]bool{
	"kind": true, "desc": true, "targets": true, "urls": true, "footer": true,
}

// ValidateURLRefs rejects a `urls` entry that is neither a real URL (a
// "://" substring) nor a key of projectURLs — the manifest's top-level
// [urls] table. Without this a typo ("local" when the table calls it
// "dev-cf") would silently become a literal, painting a footer row that
// goes nowhere. Meant to run at `admin check` / resolve time, mirroring
// admin_lib/frame.py's validate_url_refs; walks both an action's own `urls`
// and every inline target subtable's.
func ValidateURLRefs(actions map[string]manifest.Action, projectURLs map[string]string) error {
	names := make([]string, 0, len(actions))
	for n := range actions {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		act := actions[name]
		if err := validateURLEntries(name, "", act["urls"], projectURLs); err != nil {
			return err
		}

		targetKeys := make([]string, 0, len(act))
		for k := range act {
			targetKeys = append(targetKeys, k)
		}
		sort.Strings(targetKeys)
		for _, k := range targetKeys {
			if reservedTargetKeys[k] {
				continue
			}
			tbl, ok := act[k].(map[string]any)
			if !ok {
				continue
			}
			if err := validateURLEntries(name, k, tbl["urls"], projectURLs); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateURLEntries(actionName, target string, raw any, known map[string]string) error {
	if raw == nil {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("action %q: 'urls' must be a list", actionName)
	}
	for _, item := range list {
		var u string
		switch v := item.(type) {
		case string:
			u = v
		case map[string]any:
			u, _ = v["url"].(string)
		default:
			continue
		}
		if strings.Contains(u, "://") {
			continue
		}
		if _, ok := known[u]; ok {
			continue
		}
		where := fmt.Sprintf("action %q", actionName)
		if target != "" {
			where += fmt.Sprintf(" target %q", target)
		}
		hint := "no [urls] table is defined"
		if len(known) > 0 {
			names := make([]string, 0, len(known))
			for k := range known {
				names = append(names, k)
			}
			sort.Strings(names)
			hint = fmt.Sprintf("known [urls] names: %v", names)
		}
		return fmt.Errorf(
			"%s: urls entry %q is not a URL and does not name an entry in the project's [urls] table — %s",
			where, u, hint)
	}
	return nil
}
