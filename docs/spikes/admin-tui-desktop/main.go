// Reference-frame renderer for the admin dashboard TUI (admin-3rgp.19).
//
// Throwaway. Renders the settled design as ten 96x30 text frames, one per named
// state, and writes them beside this file. Text rather than screenshots because
// the surface IS a character grid — a dumped frame is how a UI change is checked
// at the surface in this codebase.
//
// Colour is deliberately absent from the output. The palette rules live in
// internal/tui/design_parity_test.go (16-colour ANSI theme slots, active tab is
// a pill and the only background-painted element on the tab bar). A .txt frame
// pins geometry; the tests pin colour.
//
//	go run main.go
package main

import (
	"fmt"
	"os"
	"strings"
)

const (
	W = 96
	H = 30
)

// ---------- frame primitives ----------

type frame struct {
	rows []string
	// fill is the template used for padding rows. A two-column frame sets it so
	// the divider runs the full height instead of stopping at the last row with
	// content — a column rule that ends mid-frame reads as a broken layout.
	fill string
}

// row renders one list row. The selection marker occupies its own leading cell so
// a selected row's text stays on the same column as its neighbours.
func row(selected bool, s string) string {
	if selected {
		return "▸ " + s
	}
	return "  " + s
}

func (f *frame) add(s string) { f.rows = append(f.rows, s) }

func (f *frame) rule(ch string) { f.add(strings.Repeat(ch, W)) }

// tabbar renders the tab strip. The active tab is drawn as a pill — in the real
// TUI that is a themed background; in text it is the bracket pair, and it is the
// only decorated element on the line.
func (f *frame) tabbar(tabs []string, active int) {
	var b strings.Builder
	b.WriteString(" ")
	for i, t := range tabs {
		if i == active {
			b.WriteString("[ " + t + " ]")
		} else {
			b.WriteString("  " + t + "  ")
		}
	}
	f.add(pad(b.String()))
}

// footer is the context-sensitive keybinding row. A key that works but is never
// displayed does not exist, so every frame carries its own.
func (f *frame) footer(keys string) { f.add(pad(" " + keys)) }

// pad is rune-aware on purpose: the frames use box-drawing characters, which are
// three bytes each in UTF-8. A byte-counting pad truncates a 96-column rule to 32
// columns and reports success.
func pad(s string) string {
	r := []rune(s)
	if len(r) > W {
		return string(r[:W])
	}
	return s + strings.Repeat(" ", W-len(r))
}

// split lays one row of the Commands tab's two-column split.
func split(left, right string, leftW int) string {
	r := []rune(left)
	if len(r) > leftW {
		r = r[:leftW]
	}
	l := string(r) + strings.Repeat(" ", leftW-len(r))
	return pad(" " + l + " │ " + right)
}

// finish pads the frame to exactly H rows, then appends the footer last.
func (f *frame) finish(keys string) string {
	for len(f.rows) < H-2 {
		f.add(f.fill)
	}
	f.rows = f.rows[:H-2]
	f.rule("─")
	f.footer(keys)
	for i, r := range f.rows {
		f.rows[i] = pad(r)
	}
	return strings.Join(f.rows, "\n") + "\n"
}

func header(title string) *frame {
	f := &frame{}
	f.add(pad(" admin · admin-project-tool                                    feat/one-tool-go-merge"))
	return f
}

var tabsFull = []string{"Commands", "Repo", "Issues", "Live", "Config"}

// ---------- the ten states ----------

func cmdRow(sel bool, name, last string) string {
	return row(sel, fmt.Sprintf("%-16s %s", name, last))
}

func commands() string {
	f := header("")
	f.tabbar(tabsFull, 0)
	f.rule("─")
	lw := 30
	f.fill = split("", "", lw)
	f.add(split("  COMMAND          LAST RUN", "deploy", lw))
	f.add(split("", strings.Repeat("─", 58), lw))
	f.add(split(cmdRow(false, "build", "2h ago"), "kind      sequence  ·  3 steps", lw))
	f.add(split(cmdRow(false, "check", "2h ago"), "archetype (manifest)", lw))
	f.add(split(cmdRow(true, "deploy", "18m ago"), "last run  18m ago · exit 0 · 41s", lw))
	f.add(split(cmdRow(false, "docs", "—"), "log       ~/.admin/logs/deploy.log", lw))
	f.add(split(cmdRow(false, "logs", "—"), "", lw))
	f.add(split(cmdRow(false, "test", "6m ago"), "STEPS", lw))
	f.add(split("", "  1  install      shell", lw))
	f.add(split("", "     bash install.sh", lw))
	f.add(split("", "     from  archetype: simple", lw))
	f.add(split("", "", lw))
	f.add(split("", "  2  tampermonkey  lua", lw))
	f.add(split("", "     scripts/provision.lua", lw))
	f.add(split("", "     from  archetype: apple", lw))
	f.add(split("", "", lw))
	f.add(split("", "  3  verify        shell", lw))
	f.add(split("", "     admin check .", lw))
	f.add(split("", "     from  (manifest)", lw))
	return f.finish("↑↓ move   ⏎ run   tab switch   / filter   ? help   q quit")
}

func commandsWorkflowSelected() string {
	f := header("")
	f.tabbar(tabsFull, 0)
	f.rule("─")
	lw := 30
	f.fill = split("", "", lw)
	f.add(split("  COMMAND          LAST RUN", "retire-merged", lw))
	f.add(split("", strings.Repeat("─", 58), lw))
	f.add(split(cmdRow(false, "build", "2h ago"), "kind      workflow", lw))
	f.add(split(cmdRow(false, "check", "2h ago"), "archetype (manifest)", lw))
	f.add(split(cmdRow(false, "deploy", "18m ago"), "source    query", lw))
	f.add(split(cmdRow(false, "docs", "—"), "last run  never", lw))
	f.add(split(cmdRow(false, "logs", "—"), "", lw))
	f.add(split(cmdRow(true, "retire-merged", "never"), "TARGETS  resolved now · 4 match", lw))
	f.add(split(cmdRow(false, "test", "6m ago"), "  fix-detector-parity-skip", lw))
	f.add(split("", "  go-transient-kind-factories", lw))
	f.add(split("", "  docs-adr-lua", lw))
	f.add(split("", "  launchd-env-file", lw))
	f.add(split("", "", lw))
	f.add(split("", "A query-sourced workflow lists here", lw))
	f.add(split("", "and runs bare. A selection-sourced", lw))
	f.add(split("", "one does not appear in this list —", lw))
	f.add(split("", "it is offered on a row instead.", lw))
	return f.finish("↑↓ move   ⏎ run over 4 targets   tab switch   ? help   q quit")
}

func repo() string {
	f := header("")
	f.tabbar(tabsFull, 1)
	f.rule("─")
	f.add(pad(" BRANCHES"))
	f.add(pad("   feat/one-tool-go-merge          ↑1  ↓0   wt   2m    docs(adr): Lua and Bash are the"))
	f.add(pad(" ▸ main                            ↑0  ↓0        15h   Merge pull request #1016"))
	f.add(pad("   fix-detector-parity-skip        ↑1  ↓0   wt   2d    fix(detect): fail parity test"))
	f.add(pad(""))
	f.add(pad(" WORKTREES"))
	f.add(pad("   ~/.worktrees/admin-project-tool/fix-detector-parity-skip     clean"))
	f.add(pad(""))
	f.add(pad(" STASHES"))
	f.add(pad("   (none)"))
	f.add(pad(""))
	f.add(pad(" RECENT"))
	f.add(pad("   db2711a  docs(adr): Lua and Bash are the archetype authoring languages   2m"))
	f.add(pad("   f738934  feat(launchd): lift allowlisted env vars from a dotenv file     3h"))
	f.add(pad("   1023714  chore(beads): close admin-3rgp.34, file .36 blocking .35        3h"))
	return f.finish("↑↓ move   ⏎ checkout   w worktree   f fetch   tab switch   q quit")
}

func issues() string {
	f := header("")
	f.tabbar(tabsFull, 2)
	f.rule("─")
	f.add(pad(" READY                                                             tracker: beads"))
	f.add(pad("   admin-3rgp.36  P1  Embed a Lua VM and define the host API"))
	f.add(pad(" ▸ admin-3rgp.19  P1  Rebuild the reference frames for the settled design"))
	f.add(pad("   admin-3rgp.7   P1  Decide how admin <command> reaches the CLI, and wire it"))
	f.add(pad(""))
	f.add(pad(" BLOCKED"))
	f.add(pad("   admin-3rgp.20  P1  Commands tab with the resolved-action reader     ← .19"))
	f.add(pad("   admin-3rgp.28  P1  The live-work tab: agent workers and processes   ← .19"))
	f.add(pad("   admin-3rgp.35  P1  Implement the transient kind factories           ← .36"))
	f.add(pad("   admin-3rgp.37  P1  Rewrite the 14 archetypes in Lua and Bash        ← .36"))
	return f.finish("↑↓ move   ⏎ open   c claim   b branch   tab switch   q quit")
}

func issuesSelected() string {
	f := header("")
	f.tabbar(tabsFull, 2)
	f.rule("─")
	lw := 44
	f.fill = split("", "", lw)
	f.add(split("READY", "admin-3rgp.19                    P1  open", lw))
	f.add(split(row(false, "admin-3rgp.36  P1  Embed a Lua VM"), strings.Repeat("─", 44), lw))
	f.add(split(row(true, "admin-3rgp.19  P1  Rebuild the ref"), "Rebuild the reference frames for the", lw))
	f.add(split(row(false, "admin-3rgp.7   P1  Decide how admi"), "settled design.", lw))
	f.add(split("", "", lw))
	f.add(split("BLOCKED", "BLOCKS", lw))
	f.add(split("  admin-3rgp.20  P1  Commands tab wit", "  .20  Commands tab with the reader", lw))
	f.add(split("  admin-3rgp.28  P1  The live-work ta", "  .28  The live-work tab", lw))
	f.add(split("  admin-3rgp.35  P1  Implement the tr", "  .29  Two deployment paths", lw))
	f.add(split("  admin-3rgp.37  P1  Rewrite the 14 a", "", lw))
	f.add(split("", "LABELS  afk", lw))
	return f.finish("↑↓ move   ⏎ open   c claim   b branch   tab switch   q quit")
}

func dispatchSheet() string {
	f := header("")
	f.tabbar(tabsFull, 2)
	f.rule("─")
	f.add(pad(" READY"))
	f.add(pad("   admin-3rgp.36  P1  Embed a Lua VM and define the host API"))
	f.add(pad(" ▸ admin-3rgp.19  P1  Rebuild the reference frames for the settled design"))
	f.add(pad(""))
	f.add(pad("        ┌──────────────────────────────────────────────────────────────┐"))
	f.add(pad("        │  Dispatch admin-3rgp.19                                      │"))
	f.add(pad("        │                                                              │"))
	f.add(pad("        │    agent      claude                                         │"))
	f.add(pad("        │    model      sonnet                                         │"))
	f.add(pad("        │    branch     rebuild-reference-frames                        │"))
	f.add(pad("        │    worktree   ~/.worktrees/admin-project-tool/rebuild-refe…   │"))
	f.add(pad("        │    lands to   feat/one-tool-go-merge                         │"))
	f.add(pad("        │                                                              │"))
	f.add(pad("        │    ⏎ dispatch          e edit          esc cancel            │"))
	f.add(pad("        └──────────────────────────────────────────────────────────────┘"))
	return f.finish("⏎ dispatch   e edit   esc cancel")
}

func swarm() string {
	f := header("")
	f.tabbar(tabsFull, 3)
	f.rule("─")
	f.add(pad(" AGENT WORKERS"))
	f.add(pad("   running   admin-3rgp.36  embed-lua-vm      writing internal/lua/host.go   pane 3"))
	f.add(pad(" ▸ waiting   admin-3rgp.7   cli-identity      needs input: root command name  pane 4"))
	f.add(pad("   done      admin-3rgp.34  fix-parity-skip   verdict PASS · c31e994          pane 2"))
	f.add(pad("   failed    admin-3rgp.33  detector-clear    go test ./internal/detect FAIL  pane 5"))
	f.add(pad(""))
	f.add(pad(" LONG-RUNNING PROCESSES"))
	f.add(pad("   running   admin docs     vitepress dev     http://localhost:5173           pane 1"))
	f.add(pad(""))
	f.add(pad(" One registry, both kinds. \"waiting\" is distinguishable from \"running\""))
	f.add(pad(" without attaching to anything."))
	return f.finish("↑↓ move   a attach   k kill   r retry   tab switch   q quit")
}

func config() string {
	f := header("")
	f.tabbar(tabsFull, 4)
	f.rule("─")
	f.add(pad(" RESOLVED CONFIGURATION                                    source: admin.toml"))
	f.add(pad(""))
	f.add(pad("   tracker                beads                              admin.toml"))
	f.add(pad("   uses_pull_requests     false                              admin.toml"))
	f.add(pad("   identity.login         pierce                             admin.toml"))
	f.add(pad("   identity.emails        3522175+McBrideMusings@users.noreply.github.com           admin.toml"))
	f.add(pad("   worktree_root          ~/.worktrees/admin-project-tool    default"))
	f.add(pad("   populate_patterns      admin.toml, .env*, CLAUDE.local.md default"))
	f.add(pad("   default_agent          claude                             default"))
	f.add(pad("   concurrency_ceiling    4                                  admin.toml"))
	f.add(pad("   enter.branch           checkout                           default"))
	f.add(pad("   enter.issue            open                               default"))
	f.add(pad(""))
	f.add(pad(" Values render resolved. Editing the manifest from here is out of scope."))
	return f.finish("↑↓ move   tab switch   q quit")
}

func running() string {
	f := header("")
	f.tabbar(tabsFull, 0)
	f.rule("─")
	lw := 30
	f.fill = split("", "", lw)
	f.add(split("  COMMAND          LAST RUN", "test  ·  running  ·  00:41", lw))
	f.add(split("", strings.Repeat("─", 58), lw))
	f.add(split(cmdRow(false, "build", "2h ago"), "ok   internal/archetype   cached", lw))
	f.add(split(cmdRow(false, "check", "2h ago"), "ok   internal/detect      0.63s", lw))
	f.add(split(cmdRow(false, "deploy", "18m ago"), "ok   internal/kinds       cached", lw))
	f.add(split(cmdRow(false, "docs", "—"), "ok   internal/manifest    cached", lw))
	f.add(split(cmdRow(false, "logs", "—"), "..   internal/tui", lw))
	f.add(split(cmdRow(true, "test", "running"), "", lw))
	f.add(split("", "log  ~/.admin/logs/test.log", lw))
	f.add(split("", "", lw))
	f.add(split("", "A transient kind runs inline in this", lw))
	f.add(split("", "frame. A long-running kind would be", lw))
	f.add(split("", "dispatched to a pane and listed under", lw))
	f.add(split("", "the live-work tab instead.", lw))
	return f.finish("c cancel   L full log   tab switch   q quit")
}

func noManifest() string {
	f := header("")
	// No manifest means no Commands tab at all — absent, not disabled.
	f.tabbar([]string{"Repo", "Issues", "Live", "Config"}, 0)
	f.rule("─")
	f.add(pad(" BRANCHES"))
	f.add(pad(" ▸ main                            ↑0  ↓0        4d    Initial commit"))
	f.add(pad(""))
	f.add(pad(" WORKTREES"))
	f.add(pad("   (none)"))
	f.add(pad(""))
	f.add(pad(" STASHES"))
	f.add(pad("   (none)"))
	f.add(pad(""))
	f.add(pad(" No admin.toml in this project, so there is no Commands tab. A tab the"))
	f.add(pad(" project cannot support is absent, never present-but-disabled."))
	f.add(pad(""))
	f.add(pad("   admin new    scaffold a manifest here"))
	return f.finish("↑↓ move   ⏎ checkout   tab switch   q quit")
}

// ---------- driver ----------

func main() {
	states := []struct {
		name string
		body string
	}{
		{"commands", commands()},
		{"commands-workflow-selected", commandsWorkflowSelected()},
		{"repo", repo()},
		{"issues", issues()},
		{"issues-selected", issuesSelected()},
		{"dispatch-sheet", dispatchSheet()},
		{"swarm", swarm()},
		{"config", config()},
		{"running", running()},
		{"no-manifest", noManifest()},
	}

	bad := 0
	for _, s := range states {
		lines := strings.Split(strings.TrimRight(s.body, "\n"), "\n")
		if len(lines) != H {
			fmt.Printf("FAIL %-28s %d rows, want %d\n", s.name, len(lines), H)
			bad++
		}
		for i, l := range lines {
			if len([]rune(l)) != W {
				fmt.Printf("FAIL %-28s row %d is %d cols, want %d\n", s.name, i, len([]rune(l)), W)
				bad++
			}
		}
		if err := os.WriteFile(s.name+".txt", []byte(s.body), 0o644); err != nil {
			fmt.Println("write:", err)
			os.Exit(1)
		}
		fmt.Printf("ok   %-28s %dx%d\n", s.name, W, H)
	}
	if bad > 0 {
		fmt.Printf("\n%d geometry violations\n", bad)
		os.Exit(1)
	}
	fmt.Printf("\n%d frames, all %dx%d\n", len(states), W, H)
}
