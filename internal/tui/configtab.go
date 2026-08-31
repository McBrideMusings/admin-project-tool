// configtab.go renders the Config tab (admin-3rgp.21): a read-only readout
// of the values that drive the dashboard, showing for each one whether it
// was declared and, when it was, where — the project's own admin.toml, a
// merged archetype, the global config file (~/.config/repo/config.toml), or
// the per-repo one (<repoRoot>/.repo/config.toml). A key nothing declared
// shows its default and says so, rather than rendering a guessed value with
// no way to tell it apart from a real one.
//
// Two sources feed this, and they stay visibly separate because they are
// declared in different files for different reasons: `tracker` and
// `uses_pull_requests` are admin.toml keys (admin-3rgp.22 reads them),
// merged with archetypes exactly like a command or action — see
// computeOrigins in work.go, which this tab reuses rather than inventing a
// second origin mechanism. Identity, the worktree root, populate patterns,
// the default agent and the concurrency ceiling are internal/config's
// global+local dotfiles instead: personal-machine settings no archetype
// should be able to declare on your behalf.
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

// renderConfigBody draws the Config tab: the manifest-declared keys first,
// then the dotfile-declared ones, each row a label, its resolved value, and
// its origin.
func (m Model) renderConfigBody(width, height int) []string {
	var lines []string
	lines = append(lines, sectionHeader("MANIFEST", "admin.toml"))
	lines = append(lines, m.manifestConfigLines(width)...)
	lines = append(lines, "")
	lines = append(lines, sectionHeader("IDENTITY & WORKTREES", "~/.config/repo/config.toml + .repo/config.toml"))
	lines = append(lines, m.dotfileConfigLines(width)...)
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

// dotfileConfigLines renders the internal/config-declared keys: identity,
// worktree root, populate patterns, the default workflow agent and its
// concurrency ceiling. Each resolves against m.cfgGlobal/m.cfgLocal (read
// separately by loadConfigCmd, alongside the merge in m.cfgMerged) so the
// origin can name the actual file rather than just "declared somewhere".
func (m Model) dotfileConfigLines(width int) []string {
	if m.cfgErr != "" {
		return []string{"  " + lipgloss.NewStyle().Foreground(cRed).Render("config error: "+m.cfgErr)}
	}

	var out []string

	login := declared{value: m.cfgMerged.Identity.Login}
	switch {
	case strings.TrimSpace(m.cfgLocal.Identity.Login) != "":
		login.origin = "local config"
	case strings.TrimSpace(m.cfgGlobal.Identity.Login) != "":
		login.origin = "global config"
	}
	out = append(out, renderConfigRow("login", login, "not declared", width))

	emails := declared{}
	if len(m.cfgMerged.Identity.Emails) > 0 {
		labeled := make([]string, len(m.cfgMerged.Identity.Emails))
		for i, e := range m.cfgMerged.Identity.Emails {
			labeled[i] = e + " " + faintStyle.Render("("+emailOrigin(e, m.cfgGlobal, m.cfgLocal)+")")
		}
		emails.value = strings.Join(labeled, ", ")
	}
	out = append(out, renderConfigRow("emails", emails, "not declared", width))

	root := declared{value: config.WorktreeRoot(m.cfgMerged)}
	switch {
	case strings.TrimSpace(m.cfgLocal.Worktrees.Root) != "":
		root.origin = "local config"
	case strings.TrimSpace(m.cfgGlobal.Worktrees.Root) != "":
		root.origin = "global config"
	default:
		root.origin = "defaulted"
	}
	out = append(out, renderConfigRow("worktree root", root, "", width))

	out = append(out, renderPopulateRow(m.cfgGlobal.Populate, m.cfgLocal.Populate, width)...)

	agent := declared{value: m.cfgMerged.Workflow.Agent}
	switch {
	case strings.TrimSpace(m.cfgLocal.Workflow.Agent) != "":
		agent.origin = "local config"
	case strings.TrimSpace(m.cfgGlobal.Workflow.Agent) != "":
		agent.origin = "global config"
	}
	out = append(out, renderConfigRow("default agent", agent, "not declared", width))

	ceiling := declared{}
	if m.cfgMerged.Workflow.Ceiling > 0 {
		ceiling.value = strconv.Itoa(m.cfgMerged.Workflow.Ceiling)
	}
	switch {
	case m.cfgLocal.Workflow.Ceiling > 0:
		ceiling.origin = "local config"
	case m.cfgGlobal.Workflow.Ceiling > 0:
		ceiling.origin = "global config"
	}
	out = append(out, renderConfigRow("concurrency ceiling", ceiling, "not declared — dispatcher default applies", width))

	return out
}

// emailOrigin says which file an already-merged email address came from,
// checked case-insensitively (config.Config does the same when merging).
// Global is checked first only because the merge itself appends local after
// global — an address in both files reads as "global", which is the file
// that would keep it if the other were deleted.
func emailOrigin(email string, global, local config.Config) string {
	for _, e := range global.Identity.Emails {
		if strings.EqualFold(e, email) {
			return "global"
		}
	}
	for _, e := range local.Identity.Emails {
		if strings.EqualFold(e, email) {
			return "local"
		}
	}
	return "?"
}

// renderPopulateRow renders the populate patterns as two labeled groups
// (global then local, matching config.Load's own append order) rather than
// one merged list — a pattern's mode and its source file are both part of
// what it declares, and folding the two files together would lose which
// file to edit to change one entry.
func renderPopulateRow(global, local []config.Entry, width int) []string {
	if len(global) == 0 && len(local) == 0 {
		return []string{renderConfigRow("populate patterns", declared{}, "none declared", width)}
	}
	label := lipgloss.NewStyle().Bold(false).Render(padEnd("populate patterns", 22))
	out := []string{"  " + faintStyle.Render(label)}
	for _, group := range []struct {
		name    string
		entries []config.Entry
	}{
		{"global config", global},
		{"local config", local},
	} {
		for _, e := range group.entries {
			line := "    " + e.Pattern + " " + faintStyle.Render("("+string(e.Mode)+", "+group.name+")")
			out = append(out, truncR(line, width))
		}
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
