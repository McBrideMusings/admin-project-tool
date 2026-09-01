// configtab.go renders the Config tab (admin-3rgp.21): a read-only readout
// of the values that drive the dashboard, showing for each one whether it
// was declared and, when it was, where — the project's own admin.toml, or
// one of its merged archetypes. A key nothing declared shows its default and
// says so, rather than rendering a guessed value with no way to tell it
// apart from a real one.
//
// Every value here comes from the SAME source: repoRoot's admin.toml,
// merged with its declared archetypes exactly like a command or action. The
// scalar keys (`tracker`, `uses_pull_requests`) and the five config tables
// ([identity], [worktrees], [[populate]], [beads], [workflow]) are shown in
// separate sections purely for readability — they share one origin
// mechanism, computeOrigins in work.go, which this tab reuses rather than
// inventing a second one.
package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/McBrideMusings/admin-project-tool/internal/config"
)

// declared is one row's resolved value plus where it came from — "" means
// nothing declared it anywhere, which renderConfigRow shows as defaulted
// rather than blank.
type declared struct {
	value  string
	origin string
}

// renderConfigBody draws the Config tab: the top-level manifest keys first,
// then the five config tables, each row a label, its resolved value, and
// its origin.
func (m Model) renderConfigBody(width, height int) []string {
	var lines []string
	lines = append(lines, sectionHeader("MANIFEST", "admin.toml"))
	lines = append(lines, m.manifestConfigLines(width)...)
	lines = append(lines, "")
	lines = append(lines, sectionHeader("IDENTITY & WORKTREES", "admin.toml"))
	lines = append(lines, m.configTableLines(width)...)
	return pad(lines, height)
}

func sectionHeader(title, subtitle string) string {
	return lipgloss.NewStyle().Bold(true).Render(" "+title) + "  " + faintStyle.Render(subtitle)
}

// manifestConfigLines renders the admin.toml-declared keys: tracker and
// uses_pull_requests. A project with no admin.toml at all declares neither,
// which is exactly what "not declared" already means — no special case
// needed beyond that m.manifest is nil.
func (m Model) manifestConfigLines(width int) []string {
	if !m.manifestOK {
		return []string{"  " + mutedStyle.Render("no admin.toml — nothing declared here")}
	}
	if m.manifestErr != "" {
		return []string{"  " + lipgloss.NewStyle().Foreground(cRed).Render("manifest error: "+m.manifestErr)}
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

	var out []string
	out = append(out, renderConfigRow("tracker", tracker, "no tracker declared", width))
	out = append(out, renderConfigRow("uses pull requests", pr, "", width))
	return out
}

// configTableLines renders the five config.Config tables — identity,
// worktree root, populate patterns, the default workflow agent and its
// concurrency ceiling — resolved from m.cfgMerged, with each row's origin
// coming from m.keyOrigin (admin.toml itself, or the contributing
// archetype's name; never a dotfile — there is no other file left to name).
func (m Model) configTableLines(width int) []string {
	if m.cfgErr != "" {
		return []string{"  " + lipgloss.NewStyle().Foreground(cRed).Render("config error: "+m.cfgErr)}
	}

	var out []string

	identityOrigin := m.keyOrigin["identity"]

	login := declared{value: m.cfgMerged.Identity.Login, origin: identityOrigin}
	out = append(out, renderConfigRow("login", login, "not declared", width))

	emails := declared{}
	if len(m.cfgMerged.Identity.Emails) > 0 {
		origin := identityOrigin
		if origin == "" {
			origin = "defaulted"
		}
		labeled := make([]string, len(m.cfgMerged.Identity.Emails))
		for i, e := range m.cfgMerged.Identity.Emails {
			labeled[i] = e + " " + faintStyle.Render("("+origin+")")
		}
		emails.value = strings.Join(labeled, ", ")
	}
	out = append(out, renderConfigRow("emails", emails, "not declared", width))

	root := declared{value: config.WorktreeRoot(m.cfgMerged), origin: m.keyOrigin["worktrees"]}
	out = append(out, renderConfigRow("worktree root", root, "", width))

	out = append(out, renderPopulateRow(m.cfgMerged.Populate, m.keyOrigin["populate"], width)...)

	agent := declared{value: m.cfgMerged.Workflow.Agent, origin: m.keyOrigin["workflow"]}
	out = append(out, renderConfigRow("default agent", agent, "not declared", width))

	ceiling := declared{origin: m.keyOrigin["workflow"]}
	if m.cfgMerged.Workflow.Ceiling > 0 {
		ceiling.value = strconv.Itoa(m.cfgMerged.Workflow.Ceiling)
	}
	out = append(out, renderConfigRow("concurrency ceiling", ceiling, "not declared — dispatcher default applies", width))

	return out
}

// renderPopulateRow renders the populate patterns as one list — a pattern's
// mode is part of what it declares, and origin names admin.toml or the
// contributing archetype (whichever last declared [[populate]]), never a
// dotfile.
func renderPopulateRow(entries []config.Entry, origin string, width int) []string {
	if len(entries) == 0 {
		return []string{renderConfigRow("populate patterns", declared{}, "none declared", width)}
	}
	if origin == "" {
		origin = "defaulted"
	}
	label := lipgloss.NewStyle().Bold(false).Render(padEnd("populate patterns", 22))
	out := []string{"  " + faintStyle.Render(label)}
	for _, e := range entries {
		line := "    " + e.Pattern + " " + faintStyle.Render("("+string(e.Mode)+", "+origin+")")
		out = append(out, truncR(line, width))
	}
	return out
}

// renderConfigRow draws one "label   value   (origin)" line, width-clamped.
// An empty value falls back to placeholder (rendered dim) and the origin is
// omitted — nothing declared it, so there is nothing to attribute.
func renderConfigRow(label string, d declared, placeholder string, width int) string {
	name := padEnd(label, 22)
	val := d.value
	if val == "" {
		return truncR("  "+faintStyle.Render(name)+faintStyle.Render(placeholder), width)
	}
	origin := d.origin
	if origin == "" {
		origin = "defaulted"
	}
	line := "  " + faintStyle.Render(name) + val + "  " + faintStyle.Render("("+origin+")")
	return truncR(line, width)
}
