// configtab.go renders the Config tab (admin-3rgp.21, admin-equg): a
// read-only readout of the values that drive the dashboard, showing for each
// one whether it was declared and, when it was, where — the project's own
// admin.toml, or one of its merged archetypes. A key nothing declared shows
// its default and says so, rather than rendering a guessed value with no way
// to tell it apart from a real one.
//
// Every value here comes from the SAME source: repoRoot's admin.toml,
// merged with its declared archetypes exactly like a command or action. The
// scalar keys (`tracker`, `uses_pull_requests`) and the five config tables
// ([identity], [worktrees], [[populate]], [beads], [workflow]) are shown in
// separate sections purely for readability — they share one origin
// mechanism, computeOrigins in work.go, which this tab reuses rather than
// inventing a second one.
//
// The tab is an ordinary rowItem list (admin-equg), like every other flat
// list tab, so it inherits the cursor, the hit map, click-to-activate and
// the filter for free. Two row kinds carry the semantics on top of the
// shared list machinery: rowConfigValue for a value with nothing to open
// (tracker, the concurrency ceiling), and rowConfigPath for a row naming an
// absolute path — a key file, the worktree root, a populate pattern — that
// enter or a click opens with the platform's default handler.
package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
)

// declared is one row's resolved value plus where it came from — "" means
// nothing declared it anywhere, which renderConfigRow shows as defaulted
// rather than blank.
type declared struct {
	value  string
	origin string
}

// configRowData is the payload riding on a rowConfigValue/rowConfigPath
// rowItem — the same one-field-per-tab precedent as work.go's workRowData.
// path and absent are set only for rowConfigPath rows; label names the row
// for the "does not exist"/"nothing to open" status lines either kind can
// produce.
type configRowData struct {
	path   string
	absent bool
	label  string
}

// keyConfigFiles is the generic key repo config file list (admin-equg): the
// ones worth having in any repo, in the order the tab lists them. Explicitly
// NOT .repo/config.toml or ~/.config/repo/config.toml — those tiers are
// being retired entirely, and listing them here would advertise a tier on
// its way out.
var keyConfigFiles = []string{
	"admin.toml",
	".gitignore",
	".claude/settings.json",
	".claude/settings.local.json",
	"CLAUDE.md",
	"CLAUDE.local.md",
	"AGENTS.md",
}

// pathAbsent reports whether path cannot be stat'd — missing file, missing
// directory, or (for a populate glob) a pattern that isn't a literal path at
// all. Any of those is "nothing there to open" as far as this tab cares.
func pathAbsent(path string) bool {
	_, err := os.Stat(path)
	return err != nil
}

// configRows builds the Config tab's row list: the admin.toml-declared
// scalar keys, the five config tables, then the key-file section — the same
// order renderConfigBody drew before this tab gained a cursor.
func (m Model) configRows() []rowItem {
	var rows []rowItem
	rows = append(rows, headerRow("MANIFEST", seg{t: "admin.toml", fg: cFaint}))
	rows = append(rows, m.manifestConfigRows()...)
	rows = append(rows, blankRow())
	rows = append(rows, headerRow("IDENTITY & WORKTREES", seg{t: "admin.toml", fg: cFaint}))
	rows = append(rows, m.configTableRows()...)
	rows = append(rows, blankRow())
	rows = append(rows, headerRow("KEY FILES"))
	rows = append(rows, m.keyFileRows()...)
	return rows
}

// manifestConfigRows renders the admin.toml-declared keys: tracker and
// uses_pull_requests. A project with no admin.toml at all declares neither,
// which is exactly what "not declared" already means — no special case
// needed beyond that m.manifest is nil.
func (m Model) manifestConfigRows() []rowItem {
	if !m.manifestOK {
		return []rowItem{{kind: rowText, label: "no admin.toml — nothing declared here"}}
	}
	if m.manifestErr != "" {
		return []rowItem{{kind: rowText, leftSegs: []seg{{t: "manifest error: " + m.manifestErr, fg: cRed}}}}
	}

	tracker := declared{origin: m.keyOrigin["tracker"]}
	if m.manifest.HasTracker {
		tracker.value = m.manifest.Tracker
	}
	pr := declared{origin: m.keyOrigin["uses_pull_requests"]}
	if m.manifest.HasUsesPullRequests {
		pr.value = strconv.FormatBool(m.manifest.UsesPullRequests)
	} else {
		pr.value = "false"
	}

	return []rowItem{
		renderConfigRow("tracker", tracker, "no tracker declared"),
		renderConfigRow("uses pull requests", pr, ""),
	}
}

// configTableRows renders the five config.Config tables — identity,
// worktree root, populate patterns, the default workflow agent and its
// concurrency ceiling — resolved from m.cfgMerged, with each row's origin
// coming from m.keyOrigin (admin.toml itself, or the contributing
// archetype's name; never a dotfile — there is no other file left to name).
// The worktree root and each populate pattern are path rows: openable, and
// marked absent when nothing is there to open.
func (m Model) configTableRows() []rowItem {
	if m.cfgErr != "" {
		return []rowItem{{kind: rowText, leftSegs: []seg{{t: "config error: " + m.cfgErr, fg: cRed}}}}
	}

	var out []rowItem

	identityOrigin := m.keyOrigin["identity"]

	login := declared{value: m.cfgMerged.Identity.Login, origin: identityOrigin}
	out = append(out, renderConfigRow("login", login, "not declared"))

	emails := declared{}
	if len(m.cfgMerged.Identity.Emails) > 0 {
		origin := identityOrigin
		if origin == "" {
			origin = "defaulted"
		}
		labeled := make([]string, len(m.cfgMerged.Identity.Emails))
		for i, e := range m.cfgMerged.Identity.Emails {
			labeled[i] = e + " (" + origin + ")"
		}
		emails.value = strings.Join(labeled, ", ")
	}
	out = append(out, renderConfigRow("emails", emails, "not declared"))

	out = append(out, m.worktreeRootRow())
	out = append(out, m.populateRows(m.cfgMerged.Populate, m.keyOrigin["populate"])...)

	agent := declared{value: m.cfgMerged.Workflow.Agent, origin: m.keyOrigin["workflow"]}
	out = append(out, renderConfigRow("default agent", agent, "not declared"))

	ceiling := declared{origin: m.keyOrigin["workflow"]}
	if m.cfgMerged.Workflow.Ceiling > 0 {
		ceiling.value = strconv.Itoa(m.cfgMerged.Workflow.Ceiling)
	}
	out = append(out, renderConfigRow("concurrency ceiling", ceiling, "not declared — dispatcher default applies"))

	return out
}

// worktreeRootRow renders the resolved worktree root as an openable
// directory row — one of the two path shapes named in the ticket's AC
// ("Enter on a directory row … opens the directory") alongside a populate
// pattern.
func (m Model) worktreeRootRow() rowItem {
	root := config.WorktreeRoot(m.cfgMerged)
	origin := m.keyOrigin["worktrees"]
	if root == "" {
		return renderConfigRow("worktree root", declared{origin: origin}, "")
	}
	return pathRow("worktree root", root, origin)
}

// populateRows renders the populate patterns as one label row followed by a
// path row per pattern — a pattern's mode is part of what it declares, and
// origin names admin.toml or the contributing archetype (whichever last
// declared [[populate]]), never a dotfile. A pattern that resolves to
// nothing on disk (including a glob, which never stats as a literal path)
// is simply an absent row that refuses to open.
func (m Model) populateRows(entries []config.Entry, origin string) []rowItem {
	if len(entries) == 0 {
		return []rowItem{renderConfigRow("populate patterns", declared{}, "none declared")}
	}
	if origin == "" {
		origin = "defaulted"
	}
	out := []rowItem{{kind: rowText, label: "populate patterns"}}
	for _, e := range entries {
		path := filepath.Join(m.repoRoot, e.Pattern)
		absent := pathAbsent(path)
		labelFg := cFg
		if absent {
			labelFg = cFaint
		}
		left := []seg{{t: "    " + e.Pattern, fg: labelFg}}
		right := []seg{{t: "(" + string(e.Mode) + ", " + origin + ")", fg: cFaint}}
		if absent {
			right = append(right, seg{t: "  absent", fg: cRed})
		}
		out = append(out, rowItem{
			kind: rowConfigPath, leftSegs: left, rightSegs: right,
			cfg: &configRowData{path: path, absent: absent, label: e.Pattern},
		})
	}
	return out
}

// keyFileRows renders the generic key config file list, one row per entry —
// present or not. A file that does not exist still gets a row, marked
// absent, which is how you find out a file was never created; opening one
// is refused with a status line rather than creating an empty one.
func (m Model) keyFileRows() []rowItem {
	rows := make([]rowItem, 0, len(keyConfigFiles))
	for _, rel := range keyConfigFiles {
		rows = append(rows, m.keyFileRow(rel))
	}
	return rows
}

func (m Model) keyFileRow(rel string) rowItem {
	abs := filepath.Join(m.repoRoot, rel)
	absent := pathAbsent(abs)
	labelFg := cFg
	var right []seg
	if absent {
		labelFg = cFaint
		right = []seg{{t: "absent", fg: cRed}}
	}
	left := []seg{{t: " " + rel, fg: labelFg}}
	return rowItem{
		kind: rowConfigPath, leftSegs: left, rightSegs: right,
		cfg: &configRowData{path: abs, absent: absent, label: rel},
	}
}

// pathRow renders a value row that is also an openable absolute path — the
// worktree root today. An absent path (a configured root that was never
// created) is marked rather than silently offered as if it could be opened.
func pathRow(label, path, origin string) rowItem {
	absent := pathAbsent(path)
	labelSeg := seg{t: padEnd(label, 22), fg: cFaint}
	valFg := cFg
	if absent {
		valFg = cFaint
	}
	o := origin
	if o == "" {
		o = "defaulted"
	}
	segs := []seg{{t: " "}, labelSeg, {t: path, fg: valFg}, seg{t: "  "}, seg{t: "(" + o + ")", fg: cFaint}}
	if absent {
		segs = append(segs, seg{t: "  absent", fg: cRed})
	}
	return rowItem{kind: rowConfigPath, leftSegs: segs, cfg: &configRowData{path: path, absent: absent, label: label}}
}

// renderConfigRow builds one "label   value   (origin)" row. An empty value
// falls back to placeholder (rendered dim) and the origin is omitted —
// nothing declared it, so there is nothing to attribute. It is selectable
// (rowConfigValue) so the cursor can land on it, but there is nothing to
// open — activate() says so rather than opening something arbitrary.
func renderConfigRow(label string, d declared, placeholder string) rowItem {
	labelSeg := seg{t: padEnd(label, 22), fg: cFaint}
	if d.value == "" {
		return rowItem{
			kind:     rowConfigValue,
			leftSegs: []seg{{t: " "}, labelSeg, {t: placeholder, fg: cFaint}},
			cfg:      &configRowData{label: label},
		}
	}
	origin := d.origin
	if origin == "" {
		origin = "defaulted"
	}
	segs := []seg{
		{t: " "}, labelSeg, {t: d.value, fg: cFg},
		{t: "  "}, {t: "(" + origin + ")", fg: cFaint},
	}
	return rowItem{kind: rowConfigValue, leftSegs: segs, cfg: &configRowData{label: label}}
}
