// Work live preview — four directions for showing live output on the Work tab
// without the row list and the output rect fighting each other.
//
// The complaint this answers, in the incumbent (internal/tui/workview.go):
//   - vertical split: the list is 2/3 of the width and grows without bound, so
//     on a project with 34 commands and 3 workers it runs the full height right
//     up against the output pane and the two stop reading as separate things.
//   - horizontal split (bottomOrient): the detail band is full width but the
//     list above it is capped at half the screen, so a tall terminal wastes
//     rows while a wide one gives the output a ~30-column column.
//
// Three things every direction here must do, not just one of them:
//   - Commands and Workers are separate sections under one cursor. A worker is
//     a Claude session running mostly autonomously in its own worktree; it has
//     its own state, its own current activity, and a real terminal session you
//     can attach to. It is not a command and never renders as one.
//   - The cursor drives the preview. Moving onto a running row previews that
//     row; there is no second selector to keep in sync.
//   - Hovering something that is NOT running has a designed state — the row's
//     own detail and its last log, never an empty rect or a stale tail.
//
// Log paths are absolute everywhere. Where one cannot fit, whole middle
// segments are elided — "/Users/…/logs/worker-chess-4kq2.log" — so it still
// reads as a path instead of as a corrupted string.
//
// harness.go supplies the variant picker, the state axes, the footer bar, the
// geometry assertion and the frame dump. Do not edit it; rebuild instead.
//
//	go run .                 drive it — [ ] switch direction, ⌥v / ⌥c / ⌥b axes
//	go run . -dump -dir .    write every variant × state as a .txt frame
package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// DumpWidth and DumpHeight are the grid every dumped frame must be, exactly.
// 120×36 is a representative real terminal — the width the incumbent's narrow
// output column is judged at. Every Render computes from s.W/s.H, never these,
// so `go run . -w 80 -h 24` is the 80-column check.
const (
	DumpWidth  = 120
	DumpHeight = 36
)

var Axes = []Axis{
	// Where the cursor sits — the three hover states every direction owes an
	// answer to. This is the axis, and the list cursor follows it (and drives
	// it back on ↑↓), so what you drive by hand and what gets dumped are one
	// thing.
	{Key: "cursor", Label: "Cursor on", Values: []string{"live-cmd", "idle-cmd", "worker"}, Hotkey: "c"},
	{Key: "screen", Label: "Screen", Values: []string{"browse", "attached"}, Hotkey: "v"},
	// Live-only: nothing running anywhere. Worth driving, not worth doubling
	// every frame name.
	{Key: "busy", Label: "Anything running", Values: []string{"yes", "no"}, Hotkey: "b", NoDump: true},
}

// ─── palette (internal/tui/styles.go — ANSI indices, never hex) ──────────────

var (
	cFg     = lipgloss.Color("7")
	cFaint  = lipgloss.Color("8")
	cBlue   = lipgloss.Color("12")
	cGreen  = lipgloss.Color("10")
	cRed    = lipgloss.Color("9")
	cYellow = lipgloss.Color("11")
	cCyan   = lipgloss.Color("14")
	cMag    = lipgloss.Color("13")
	cBright = lipgloss.Color("15")
	cBg     = lipgloss.Color("0")

	faint  = lipgloss.NewStyle().Foreground(cFaint)
	bright = lipgloss.NewStyle().Foreground(cBright).Bold(true)
	cyan   = lipgloss.NewStyle().Foreground(cCyan)
	mag    = lipgloss.NewStyle().Foreground(cMag)
	green  = lipgloss.NewStyle().Foreground(cGreen)
	yellow = lipgloss.NewStyle().Foreground(cYellow)
	red    = lipgloss.NewStyle().Foreground(cRed)
	blue   = lipgloss.NewStyle().Foreground(cBlue)
	fg     = lipgloss.NewStyle().Foreground(cFg)
	pill   = lipgloss.NewStyle().Foreground(cBg).Background(cBlue).Bold(true)
)

const root = "/Users/pierce/Projects/social-chess"

// ─── the live thing behind a row ─────────────────────────────────────────────

// live is one thing currently running. A command and a worker share the state,
// elapsed, absolute log path and output tail; everything below `activity` is a
// worker's alone — a Claude session has a current activity, a branch, a
// worktree, and a terminal session that either dies with the dashboard or
// outlives it.
type live struct {
	worker   bool
	name     string
	state    string // running | waiting | failed
	elapsed  string
	log      string
	url      string
	activity string
	branch   string
	worktree string
	where    string // "" = hosted in the dashboard; otherwise a launcher handle
	tail     []string
}

func (l live) glyph() (string, lipgloss.Style) {
	switch l.state {
	case "waiting":
		return "◐", yellow
	case "failed":
		return "✗", red
	default:
		if l.worker {
			return "◈", green
		}
		return "●", green
	}
}

// lives is every running thing, commands first then workers — the order the
// chip rows and border tabs use, so the two sections never interleave.
var lives = []live{
	{
		name: "preview devvit", state: "running", elapsed: "4m12s",
		log: root + "/.admin/logs/preview-devvit.log",
		url: "https://www.reddit.com/r/worldbuilder_dev",
		tail: []string{
			"14:55:41  [devvit] bundling client…",
			"14:55:44  [devvit] bundling server…",
			"14:55:47  [devvit] uploading 4.2 MB",
			"14:55:52  [devvit] app version 0.0.1.30 created",
			"14:55:53  [devvit] installing to r/worldbuilder_dev",
			"14:55:56  Updating... ⣷",
			"14:55:58  Updating... ⣟",
			"14:56:00  Updating... ⢿",
			"14:56:02  Updating... ⣽",
			"14:56:04  Updating... ⣷",
			"14:56:06  Updating... ⣟",
			"14:56:08  Updating... ⢿",
			"14:56:10  Updating... ⣯",
			"14:56:11  Updating... ⣷",
			"14:56:12  Updating... ⣻",
			"14:56:13  Updating... ⢿",
			"14:56:14  Updating... ⡿",
			"14:56:15  Success!",
			"14:56:15  ✓ v0.0.1.30 ➜ https://www.reddit.com/r/worldbuilder_dev",
		},
	},
	{
		name: "dev", state: "running", elapsed: "41m",
		log: root + "/.admin/logs/dev.log",
		url: "http://localhost:5173",
		tail: []string{
			"14:52:44  [vite] hmr update /src/board/Square.tsx",
			"14:53:01  [api] POST /attempts 201 27ms",
			"14:53:18  [supabase] realtime: 1 subscriber",
			"14:53:37  [api] GET /profile/me 200 7ms",
			"14:53:52  [vite] hmr update /src/hud/Timer.tsx",
			"14:54:06  [engine] eval depth 16 +0.38",
			"14:54:15  [api] GET /puzzles/next 200 10ms",
			"14:54:31  [vite] hmr update /src/board/Board.css",
			"14:55:02  [vite] page reload src/board/Piece.tsx",
			"14:55:48  [api] GET /puzzles/next 200 12ms",
			"14:56:01  [engine] eval depth 18 +0.42",
			"14:56:09  [api] POST /attempts 201 31ms",
			"14:56:14  [api] GET /puzzles/next 200 9ms",
		},
	},
	{
		name: "test workers", state: "waiting", elapsed: "12s",
		log: root + "/.admin/logs/test-workers.log",
		tail: []string{
			"14:56:02  [vitest] workers pool: miniflare",
			"14:56:03  ✓ workers/health.test.ts (3)",
			"14:56:04  ✓ workers/cors.test.ts (5)",
			"14:56:05  ✓ workers/router.test.ts (14)",
			"14:56:09  ✓ workers/auth.test.ts (8)",
			"14:56:12  ❯ workers/puzzles.test.ts 3/11",
			"14:56:14    · waiting for miniflare port 8787",
		},
	},
	{
		worker: true,
		name:   "chess-4kq2", state: "running", elapsed: "18m",
		log:      root + "/.admin/logs/worker-chess-4kq2.log",
		activity: "editing src/engine/eval.ts — 4 files, 2 tests green",
		branch:   "feat/chess-4kq2-eval-cache",
		worktree: "/Users/pierce/.worktrees/social-chess-chess-4kq2",
		where:    "herdr:pane-7",
		tail: []string{
			"14:49:12  read src/engine/eval.ts (412 lines)",
			"14:50:03  edit src/engine/eval.ts — memoize pawn structure",
			"14:51:20  run  bun test engine/eval",
			"14:51:44  ✓ 18 pass, 0 fail",
			"14:52:31  read src/engine/search.ts (890 lines)",
			"14:54:02  edit src/engine/search.ts — reuse eval cache",
			"14:55:10  run  bun test engine",
			"14:55:52  ✗ engine/search.test.ts: transposition depth mismatch",
			"14:56:06  read src/engine/tt.ts (204 lines)",
			"14:56:13  edit src/engine/tt.ts — key on cache generation",
		},
	},
	{
		worker: true,
		name:   "chess-9wpe", state: "waiting", elapsed: "6m",
		log:      root + "/.admin/logs/worker-chess-9wpe.log",
		activity: "asked a question — needs a decision before it continues",
		branch:   "fix/chess-9wpe-puzzle-import",
		worktree: "/Users/pierce/.worktrees/social-chess-chess-9wpe",
		tail: []string{
			"14:48:55  run  bun run puzzles:import --dry-run",
			"14:49:40  ✓ 12,004 rows parsed, 118 rejected",
			"14:50:22  read migrations/0007_puzzles.sql",
			"14:51:09  ❯ rejected rows all fail the rating-deviation check",
			"14:52:14  ❯ drop them, or widen the check and re-import?",
			"14:52:14    · waiting for an answer",
		},
	},
}

func liveAt(i int) *live {
	if i < 0 || i >= len(lives) {
		return nil
	}
	return &lives[i]
}

// ─── rows: two sections, one cursor ──────────────────────────────────────────

type rowKind int

const (
	rowHeader rowKind = iota
	rowBlank
	rowCmd
	rowTarget
	rowWorker
)

type row struct {
	kind rowKind
	name string
	// commands
	cmdKind string
	desc    string
	lastLog string
	lastRun string
	last    bool // last target under its parent
	// workers
	wState   string // an idle worker still has a state — "exited", "failed"
	wSummary string
	wBranch  string
	wLog     string
	// the running thing behind this row, or -1
	liveIdx int
}

func (r row) selectable() bool { return r.kind == rowCmd || r.kind == rowTarget || r.kind == rowWorker }

// rows is the social-chess manifest — 34 command rows, the length at which the
// incumbent's list stops leaving the output pane any room — plus the Workers
// section beneath it.
var rows = buildRows()

func buildRows() []row {
	c := func(name, kind, desc string, l int) row {
		return row{kind: rowCmd, name: name, cmdKind: kind, desc: desc, liveIdx: l,
			lastLog: root + "/.admin/logs/" + name + ".log"}
	}
	t := func(name, desc string, last bool, l int) row {
		return row{kind: rowTarget, name: name, desc: desc, last: last, liveIdx: l,
			lastLog: root + "/.admin/logs/" + name + ".log"}
	}
	out := []row{{kind: rowHeader, name: "Commands", liveIdx: -1}}
	out = append(out,
		c("setup", "sequence", "Start Postgres, install deps, migrate the dev database", -1),
		c("build", "shell", "Build the client bundle", -1),
		c("dev", "sequence", "Run the dev stack (vite + wrangler + engine)", 1),
		c("preview", "multi-tgt", "Deploy to preview (cf / devvit)", -1),
		t("cf", "bun run deploy:preview:cf", false, -1),
		t("devvit", "bun run deploy:preview:devvit", true, 0),
		c("staging", "multi-tgt", "Deploy to staging (cf / devvit)", -1),
		t("cf", "bun run deploy:staging:cf", false, -1),
		t("devvit", "@devvit-staging", true, -1),
		c("prod", "multi-tgt", "Deploy to prod (cf)", -1),
		c("test", "multi-tgt", "Run the test suites (node|workers|engine|rules|all)", -1),
		t("all", "@test-all", false, -1),
		t("engine", "@test-engine", false, -1),
		t("node", "@test-node", false, -1),
		t("rules", "@test-rules", false, -1),
		t("workers", "@test-workers", true, 2),
		c("vet", "shell", "Typecheck and lint migrations", -1),
		c("fmt", "shell", "Format the tree with prettier", -1),
		c("clean", "shell", "Remove build outputs", -1),
		c("kill", "sequence", "Kill stray dev processes, free the ports, stop Postgres", -1),
		c("db", "sequence", "Recreate and migrate the local dev database", -1),
		c("perft", "shell", "Run the deep perft conformance suite for chess-rules", -1),
		c("db-migrate", "multi-tgt", "Run pending dbmate migrations against a target", -1),
		t("preview", "@db-migrate-preview", false, -1),
		t("prod", "@db-migrate-prod", false, -1),
		t("staging", "@db-migrate-staging", true, -1),
		c("puzzles-import", "multi-tgt", "Import the Lichess puzzle corpus into a target", -1),
		t("local", "@puzzles-import-local", false, -1),
		t("preview", "@puzzles-import-preview", false, -1),
		t("prod", "@puzzles-import-prod", true, -1),
		c("puzzle-report", "multi-tgt", "Solve rate / retries / hints per difficulty band", -1),
		t("local", "@puzzle-difficulty-report-local", false, -1),
		t("prod", "@puzzle-difficulty-report-prod", true, -1),
		c("prototype", "multi-tgt", "Open a prototype", -1),
	)
	// perft has actually been run before; the hover state shows its last log.
	for i := range out {
		if out[i].name == "perft" {
			out[i].lastRun = "last run 2d ago · exit 0"
		}
	}
	out = append(out,
		row{kind: rowBlank, liveIdx: -1},
		row{kind: rowHeader, name: "Workers", liveIdx: -1},
		row{kind: rowWorker, name: "chess-4kq2", liveIdx: 3},
		row{kind: rowWorker, name: "chess-9wpe", liveIdx: 4},
		row{kind: rowWorker, name: "chess-2f1x", liveIdx: -1,
			wState: "exited", wSummary: "exit 0 · 34m · pushed fix/chess-2f1x-hint-copy",
			wBranch: "fix/chess-2f1x-hint-copy",
			wLog:    root + "/.admin/logs/worker-chess-2f1x.log"},
	)
	return out
}

// ─── live state (package level, so Reset must clear it) ──────────────────────

var cursor = 6 // the running "preview devvit" target row

func resetAll() { cursor = 6 }

// busy answers the NoDump axis: with nothing running, every row's own live
// reference is ignored and the whole dashboard is in its quiet state.
func busy(s State) bool { return !s.Is("busy", "no") }

// liveOf is the running thing behind a row, honouring the busy axis.
func liveOf(s State, r row) *live {
	if !busy(s) {
		return nil
	}
	return liveAt(r.liveIdx)
}

// class is which cursor-axis value a row sits at. It is what keeps the axis and
// the list cursor in agreement in both directions: ↑↓ writes the moved-to row's
// class back onto the axis, and Render snaps the cursor to the axis's own
// representative row when the axis was changed by hand.
func class(s State, i int) string {
	r := rows[i]
	if liveOf(s, r) == nil {
		return "idle-cmd"
	}
	if r.kind == rowWorker {
		return "worker"
	}
	return "live-cmd"
}

// syncCursor is called at the top of every Render: if the cursor no longer sits
// at the axis's value (⌥c was pressed, or busy went to "no"), move it to the
// first row that does.
func syncCursor(s State) {
	want := s.Get("cursor")
	if cursor >= 0 && cursor < len(rows) && rows[cursor].selectable() && class(s, cursor) == want {
		return
	}
	for i := range rows {
		if rows[i].selectable() && class(s, i) == want {
			cursor = i
			return
		}
	}
	// No row sits at that value — nothing is running, so the whole list is
	// idle. Park on a plain command rather than a header.
	for i := range rows {
		if rows[i].kind == rowCmd {
			cursor = i
			return
		}
	}
}

// cur is the previewed thing: whatever the cursor is on. There is no second
// selector, which is the point.
func cur(s State) (row, *live) {
	r := rows[cursor]
	return r, liveOf(s, r)
}

func keys(key string, s State, set func(axis, value string)) bool {
	move := func(d int) {
		for i := cursor + d; i >= 0 && i < len(rows); i += d {
			if rows[i].selectable() {
				cursor = i
				set("cursor", class(s, i))
				return
			}
		}
	}
	switch key {
	case "down", "j":
		move(1)
		return true
	case "up", "k":
		move(-1)
		return true
	case "tab", ".":
		// Jump to the next RUNNING row, in list order. Still the same cursor —
		// a shortcut through the list, not a separate selector.
		for n := 1; n <= len(rows); n++ {
			i := (cursor + n) % len(rows)
			if rows[i].selectable() && liveOf(s, rows[i]) != nil {
				cursor = i
				set("cursor", class(s, i))
				break
			}
		}
		return true
	case "a", "enter":
		if _, l := cur(s); l != nil {
			set("screen", "attached")
		}
		return true
	case "esc":
		set("screen", "browse")
		return true
	}
	return false
}

// ─── chrome ──────────────────────────────────────────────────────────────────

func chrome(w int, attached bool, breadcrumb string) []string {
	left := fg.Render("Forevr-Games/social-chess") + faint.Render("   ") +
		green.Render("puzzles") + faint.Render("  ✔ clean")
	right := faint.Render("live")
	head := left + strings.Repeat(" ", maxi(w-VW(left)-VW(right)-1, 1)) + right + " "
	if attached {
		return []string{Pad(head, w), Pad(" "+breadcrumb, w), Pad("", w)}
	}
	tabs := " " + pill.Render(" Work 39 ") +
		faint.Render("    Repo     PRs 4     Beads 14     Config")
	return []string{Pad(head, w), Pad(tabs, w), Pad("", w)}
}

func footer(w int, hint string) []string {
	return []string{
		Pad(faint.Render(strings.Repeat("─", w)), w),
		Pad(" "+faint.Render(hint), w),
	}
}

// browseKeys is the footer for a browse screen, which depends on what the
// cursor is on — the same rule the real dashboard's per-row footer follows.
func browseKeys(s State) string {
	r, l := cur(s)
	switch {
	case l != nil && r.kind == rowWorker:
		return "↑↓ move · a attach · k kill · ⇥ next running · / filter · q quit"
	case l != nil:
		return "↑↓ move · a attach · k kill · ⇥ next running · ↵ restart · q quit"
	case r.kind == rowWorker:
		return "↑↓ move · ↵ open transcript · x retire · / filter · q quit"
	default:
		return "↑↓ move · ↵ run · l last log · / filter · q quit · ←→ tab"
	}
}

// ─── row rendering ───────────────────────────────────────────────────────────

// rowLine renders one row at width w. dense drops the description column,
// which is what lets a direction shrink the list into a rail instead of letting
// it own two thirds of the screen forever.
func rowLine(s State, i, w int, dense bool) []string {
	r := rows[i]
	sel := i == cursor
	switch r.kind {
	case rowBlank:
		return []string{Pad("", w)}
	case rowHeader:
		n := 0
		if r.name == "Workers" {
			for _, x := range rows {
				if x.kind == rowWorker {
					n++
				}
			}
		} else {
			for _, x := range rows {
				if x.kind == rowCmd || x.kind == rowTarget {
					n++
				}
			}
		}
		label := bright.Render(r.name) + faint.Render(fmt.Sprintf("  %d", n))
		return []string{Pad(" "+label, w)}
	}

	// Every non-header row is inset one column, matching the section headers
	// above it; the width math below therefore runs against w-1.
	full := w
	w--
	out1 := func(line string) []string { return []string{Pad(" "+line, full)} }

	marker := faint.Render("◇ ")
	if sel {
		marker = blue.Render("◆ ")
	}
	name := r.name
	if r.kind == rowTarget {
		conn := "├ "
		if r.last {
			conn = "└ "
		}
		if sel {
			name = faint.Render(conn) + blue.Render(r.name)
		} else {
			name = faint.Render(conn) + name
		}
	} else if sel {
		name = blue.Render(name)
	}

	nameW := 16
	if dense {
		nameW = maxi(w-4, 6)
	}
	head := marker + Pad(name, nameW)
	l := liveOf(s, r)

	if dense {
		g := " "
		if l != nil {
			gl, st := l.glyph()
			g = st.Render(gl)
		} else if r.kind == rowWorker && r.wState != "" {
			g = faint.Render("·")
		}
		return out1(marker + Pad(name, maxi(w-4, 4)) + g)
	}

	if l != nil {
		gl, st := l.glyph()
		state := st.Render(gl+" "+Pad(l.state, 8)) + faint.Render(Pad(l.elapsed, 7))
		rest := maxi(w-nameW-2-VW(Strip(state)), 4)
		mid := l.activity
		if mid == "" {
			mid = strings.TrimSpace(l.tail[len(l.tail)-1][8:])
		}
		// The where-segment is a worker's own column: which terminal session it
		// lives in. It is dropped rather than allowed to overflow once the row
		// is too narrow to hold it AND four cells of activity — an overflowing
		// row gets truncated by the caller and reads as "herdr:" mid-word.
		where := ""
		if l.where != "" {
			where = " " + mag.Render(l.where)
		} else if l.worker {
			where = " " + cyan.Render("hosted")
		}
		wW := VW(Strip(where))
		if rest-wW < 8 {
			where, wW = "", 0
		}
		midW := maxi(rest-wW, 4)
		return out1(head + state + faint.Render(Pad(truncR(mid, midW), midW)) + where)
	}

	if r.kind == rowWorker {
		return out1(head + faint.Render(Pad(r.wState, 10)+truncR(r.wSummary, maxi(w-nameW-14, 4))))
	}
	return out1(head + faint.Render(Pad(r.cmdKind, 10)+truncR(r.desc, maxi(w-nameW-14, 4))))
}

// listRows is the windowed row list. The window is computed against the cursor,
// and the section header of whatever section the cursor is in is pinned to the
// window's first line, so scrolling into Workers never leaves you looking at
// rows with no idea which section they belong to.
func listRows(s State, w, h int, dense bool) []string {
	start := 0
	if cursor >= h-1 {
		start = cursor - h + 2
	}
	if start+h > len(rows) {
		start = maxi(len(rows)-h, 0)
	}
	out := make([]string, 0, h)
	for i := start; i < len(rows) && len(out) < h; i++ {
		out = append(out, rowLine(s, i, w, dense)...)
	}
	if start > 0 && len(out) > 0 {
		hdr := "Commands"
		for i := start; i >= 0; i-- {
			if rows[i].kind == rowHeader {
				hdr = rows[i].name
				break
			}
		}
		out[0] = Pad(" "+bright.Render(hdr)+faint.Render(fmt.Sprintf("   ↑ %d more", start)), w)
	}
	if start+len(out) < len(rows) && len(out) > 0 {
		out[len(out)-1] = Pad(faint.Render("   ↓ more"), w)
	}
	for len(out) < h {
		out = append(out, Pad("", w))
	}
	return out
}

// tailRows renders the last n lines of a live thing's output at width w,
// bottom aligned the way output accumulates.
func tailRows(l live, w, n int) []string {
	if n < 1 {
		return nil
	}
	lines := l.tail
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]string, 0, n)
	for len(out) < n-len(lines) {
		out = append(out, Pad("", w))
	}
	for _, s := range lines {
		out = append(out, Pad(" "+faint.Render(s[:8])+colorize(s[8:], w-9), w))
	}
	return out
}

// colorize stands in for internal/logformat: level words, bracketed scope tags
// and the agent-transcript verbs, so a direction is judged on whether its
// output region is legible rather than only on whether it fits.
func colorize(s string, w int) string {
	s = truncR(s, maxi(w, 1))
	switch {
	case strings.Contains(s, "✗"), strings.Contains(s, "ERROR"):
		return red.Render(s)
	case strings.Contains(s, "✓"), strings.Contains(s, "Success"):
		return green.Render(s)
	case strings.Contains(s, "❯"), strings.Contains(s, "waiting"):
		return yellow.Render(s)
	case strings.Contains(s, "["):
		return cyan.Render(s)
	case strings.HasPrefix(strings.TrimSpace(s), "edit "), strings.HasPrefix(strings.TrimSpace(s), "run "):
		return mag.Render(s)
	}
	return fg.Render(s)
}

// fitPath keeps a log path absolute. When it will not fit, the middle is elided
// rather than either end, so the root and the file name both survive — the one
// thing a truncated path has to keep is which project and which file.
func fitPath(p string, w int) string {
	if VW(p) <= w {
		return p
	}
	if w < 12 {
		return truncR(p, maxi(w, 1))
	}
	// Elide whole path segments, never mid-segment: "/Users/pierce/Projects/
	// social-chess/.admin/logs/x.log" becomes "/Users/…/logs/x.log", which
	// still reads as a path. A mid-rune cut produces "/Users/pierce/Proj…hess/"
	// and reads as corruption.
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for k := len(segs) - 1; k >= 1; k-- {
		cand := "/" + segs[0] + "/…/" + strings.Join(segs[len(segs)-k:], "/")
		if VW(cand) <= w {
			return cand
		}
	}
	if cand := "…/" + segs[len(segs)-1]; VW(cand) <= w {
		return cand
	}
	return truncR(p, w)
}

// chips is the running-things selector: commands, then a separator, then
// workers, so the two never read as one pool. The selected chip is whatever the
// cursor is on — this is a readout, not a second control.
func chips(s State, w int) string {
	var cmds, wks []string
	_, sel := cur(s)
	for i := range lives {
		l := lives[i]
		gl, st := l.glyph()
		label := l.name + " " + l.elapsed
		part := faint.Render("◦ " + label)
		if sel != nil && l.name == sel.name {
			part = st.Render(gl) + " " + bright.Render(label)
		}
		if l.worker {
			wks = append(wks, part)
		} else {
			cmds = append(cmds, part)
		}
	}
	out := strings.Join(cmds, faint.Render("  "))
	if len(wks) > 0 {
		out += faint.Render("  ┊  ") + strings.Join(wks, faint.Render("  "))
	}
	return truncR(out, w)
}

func ruleLabel(label string, w int, right string) string {
	prefix := "─── " + label + " "
	suffix := ""
	if right != "" {
		suffix = " " + right + " ───"
	}
	return faint.Render(prefix + strings.Repeat("─", maxi(w-VW(prefix)-VW(suffix), 0)) + suffix)
}

// runningCount is the "2 commands · 2 workers" readout every direction puts on
// its live rule, so the sections stay counted separately even when the preview
// is showing one of them.
func runningCount(s State) string {
	if !busy(s) {
		return "nothing running"
	}
	c, wk := 0, 0
	for _, l := range lives {
		if l.worker {
			wk++
		} else {
			c++
		}
	}
	return fmt.Sprintf("%d commands · %d workers", c, wk)
}

// ─── the not-running preview ─────────────────────────────────────────────────

// idlePreview is what a direction shows while the cursor sits on something that
// is NOT running. It is the row's own detail plus its last log — never a blank
// rect and never another row's tail.
func idlePreview(r row, w int) []string {
	if r.kind == rowWorker {
		out := []string{
			" " + bright.Render(r.name) + faint.Render("  ·  worker  ·  ") + faint.Render(r.wState),
			"",
			" " + faint.Render("outcome  ") + fg.Render(truncR(r.wSummary, maxi(w-11, 4))),
			" " + faint.Render("branch   ") + fg.Render(truncR(r.wBranch, maxi(w-11, 4))),
			" " + faint.Render("log      ") + faint.Render(fitPath(r.wLog, maxi(w-11, 8))),
			"",
			" " + faint.Render("↵ open transcript") + faint.Render("   x retire"),
		}
		return out
	}
	kindLabel := r.cmdKind
	if kindLabel == "" {
		kindLabel = "target"
	}
	out := []string{
		" " + bright.Render(r.name) + faint.Render("  ·  not running"),
		"",
		" " + faint.Render("kind     ") + fg.Render(kindLabel),
		" " + faint.Render("desc     ") + fg.Render(truncR(r.desc, maxi(w-11, 4))),
	}
	if r.lastRun != "" {
		out = append(out,
			" "+faint.Render("last log ")+faint.Render(fitPath(r.lastLog, maxi(w-11, 8))),
			" "+faint.Render("         ")+faint.Render(r.lastRun),
		)
	} else {
		out = append(out, " "+faint.Render("last log ")+faint.Render("never run"))
	}
	out = append(out, "", " "+faint.Render("↵ run")+faint.Render("   l last log"))
	return out
}

// livePreviewMeta is the condensed header shared by every direction: the log
// path (absolute), the URL for a command, and the branch/worktree/where for a
// worker. Not the tail — each direction places that itself.
func livePreviewMeta(l live, w int) []string {
	out := []string{" " + faint.Render("log      ") + faint.Render(fitPath(l.log, maxi(w-11, 8)))}
	if l.worker {
		out = append(out,
			" "+faint.Render("branch   ")+fg.Render(truncR(l.branch, maxi(w-11, 4))),
			" "+faint.Render("worktree ")+faint.Render(fitPath(l.worktree, maxi(w-11, 8))),
		)
		// The session line is the widest field a worker has, so its gloss is
		// the first thing dropped rather than allowed to run off the region.
		name, gloss := cyan.Render("hosted"), " — dies when the dashboard quits"
		if l.where != "" {
			name, gloss = mag.Render(l.where), " — a real terminal, outlives the dashboard"
		}
		avail := maxi(w-11-VW(Strip(name)), 0)
		if VW(gloss) > avail {
			gloss = ""
		}
		out = append(out, " "+faint.Render("session  ")+name+faint.Render(gloss))
		if l.activity != "" {
			out = append(out, " "+faint.Render("now      ")+fg.Render(truncR(l.activity, maxi(w-11, 4))))
		}
		return out
	}
	if l.url != "" {
		out = append(out, " "+faint.Render("url      ")+cyan.Render(truncR(l.url, maxi(w-11, 4))))
	}
	return out
}

func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func mini(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncR(s string, w int) string {
	r := []rune(Strip(s))
	if len(r) <= w {
		return s
	}
	if w < 1 {
		return ""
	}
	return string(r[:w-1]) + "…"
}

func body(s State) (w, h int) { return s.W, s.H - 5 }

// attachedID is the identity line an attached screen puts where the tab bar
// was: a worker says it is a worker and which session it lives in.
func attachedID(l live) string {
	gl, st := l.glyph()
	out := blue.Render("attached") + faint.Render("  ·  ") + st.Render(gl) + " " + bright.Render(l.name)
	if l.worker {
		out += faint.Render("  ·  worker  ·  " + l.state + "  ·  " + l.elapsed)
		if l.where != "" {
			out += faint.Render("  ·  ") + mag.Render(l.where)
		}
		return out
	}
	return out + faint.Render("  ·  "+l.state+"  ·  "+l.elapsed)
}

// ═════════════════════════════════════════════════════════════════════════════
// A — Dock: the live surface is a full-width band pinned to the bottom of the
// body. The list gives up height, never width, so a 40-row list scrolls in its
// own region and the output always gets every column.
// ═════════════════════════════════════════════════════════════════════════════

func renderDock(s State) string {
	syncCursor(s)
	w, bh := body(s)
	r, l := cur(s)

	if s.Is("screen", "attached") && l != nil {
		out := chrome(w, true, attachedID(*l))
		out = append(out, Pad(" "+chips(s, w-2), w))
		out = append(out, Pad(faint.Render(strings.Repeat("╌", w)), w))
		out = append(out, tailRows(*l, w, bh-2)...)
		return frame(out, w, s.H, footer(w, "⇥ next running · esc detach · k kill · q quit"))
	}

	out := chrome(w, false, "")
	dockH := mini(14, bh/2)
	out = append(out, listRows(s, w, bh-dockH, false)...)

	label, right := "live", runningCount(s)
	out = append(out, Pad(ruleLabel(label, w, right), w))
	if busy(s) {
		out = append(out, Pad(" "+chips(s, w-2), w))
	} else {
		out = append(out, Pad(" "+faint.Render("nothing running — ↵ starts the row under the cursor"), w))
	}
	if l == nil {
		// The band keeps its height — a dock that resized with the cursor would
		// reflow the list on every ↑↓. The unused rows carry the one line that
		// says why they are empty.
		det := idlePreview(r, w)
		for len(det) < dockH-3 {
			det = append(det, Pad("", w))
		}
		out = append(out, det[:dockH-3]...)
		out = append(out, Pad(" "+faint.Render("output appears here while it runs"), w))
		return frame(out, w, s.H, footer(w, browseKeys(s)))
	}
	meta := livePreviewMeta(*l, w)
	out = append(out, meta...)
	out = append(out, tailRows(*l, w, dockH-2-len(meta))...)
	return frame(out, w, s.H, footer(w, browseKeys(s)))
}

// ═════════════════════════════════════════════════════════════════════════════
// B — Ticker: no pane at all. One pinned line per running thing, grouped
// commands-then-workers directly above the footer, each carrying its own latest
// line. The list keeps the whole body; everything else is on demand.
// ═════════════════════════════════════════════════════════════════════════════

func renderTicker(s State) string {
	syncCursor(s)
	w, bh := body(s)
	r, l := cur(s)

	if s.Is("screen", "attached") && l != nil {
		out := chrome(w, true, attachedID(*l))
		out = append(out, Pad(" "+faint.Render("log  ")+faint.Render(fitPath(l.log, w-8)), w))
		out = append(out, Pad(faint.Render(strings.Repeat("─", w)), w))
		out = append(out, tailRows(*l, w, bh-2)...)
		return frame(out, w, s.H, footer(w, "esc detach · ⇥ next running · k kill · q quit"))
	}

	out := chrome(w, false, "")
	if !busy(s) {
		out = append(out, listRows(s, w, bh-3, false)...)
		out = append(out, Pad(ruleLabel("live", w, "nothing running"), w))
		out = append(out, Pad("   "+faint.Render(truncR(idleOneLine(r), w-4)), w))
		out = append(out, Pad("", w))
		return frame(out, w, s.H, footer(w, browseKeys(s)))
	}

	tickH := len(lives) + 3 // a rule, one row each, a group separator, the cursor's own line
	out = append(out, listRows(s, w, bh-tickH, false)...)
	out = append(out, Pad(ruleLabel("live", w, runningCount(s)), w))
	wroteSep := false
	for i := range lives {
		lv := lives[i]
		if lv.worker && !wroteSep {
			out = append(out, Pad(" "+faint.Render("  workers"), w))
			wroteSep = true
		}
		gl, st := lv.glyph()
		nm := Pad(lv.name, 15)
		if l != nil && l.name == lv.name {
			nm = bright.Render(Pad(lv.name, 15))
		}
		head := " " + st.Render(gl) + " " + nm + faint.Render(Pad(lv.elapsed, 7))
		msg := lv.activity
		if msg == "" {
			msg = strings.TrimSpace(lv.tail[len(lv.tail)-1][8:])
		}
		line := head + faint.Render("│ ") + colorize(msg, maxi(w-VW(Strip(head))-3, 4))
		out = append(out, Pad(line, w))
	}
	// The cursor's own row gets the one extra line: its absolute log path when
	// running, its detail when not.
	if l != nil {
		out = append(out, Pad("   "+faint.Render("log  ")+faint.Render(fitPath(l.log, w-10))+faint.Render("   a attach"), w))
	} else {
		out = append(out, Pad("   "+faint.Render(truncR(idleOneLine(r), w-4)), w))
	}
	return frame(out, w, s.H, footer(w, browseKeys(s)))
}

// idleOneLine is B's whole not-running answer: one line, since B's claim is
// that a pane is on demand. Name, kind, and the last log if there is one.
func idleOneLine(r row) string {
	if r.kind == rowWorker {
		return r.name + "  " + r.wState + " · " + r.wSummary + "  ·  ↵ open transcript"
	}
	if r.lastRun != "" {
		return r.name + "  not running · " + r.lastRun + "  ·  " + r.lastLog + "  ·  ↵ run"
	}
	return r.name + "  not running · never run  ·  ↵ run"
}

// ═════════════════════════════════════════════════════════════════════════════
// C — Output-first: the incumbent's split, inverted. The row list becomes a
// fixed 26-column rail carrying names and a state glyph only; output owns the
// rest of the width for good. A 41st row cannot narrow the output by one cell,
// which is the whole failure being fixed.
// ═════════════════════════════════════════════════════════════════════════════

func renderOutputFirst(s State) string {
	syncCursor(s)
	w, bh := body(s)
	r, l := cur(s)
	railW := 26

	if s.Is("screen", "attached") && l != nil {
		out := chrome(w, true, attachedID(*l))
		out = append(out, Pad(" "+faint.Render(fitPath(l.log, w-2)), w))
		// The rail collapses to a gutter of state glyphs — still enough to see
		// that other things are running and to jump to them.
		tail := tailRows(*l, w-4, bh-1)
		for i, line := range tail {
			g := "  "
			if i < len(lives) {
				gl, st := lives[i].glyph()
				if lives[i].name == l.name {
					g = st.Render(gl) + " "
				} else {
					g = faint.Render(gl) + " "
				}
			}
			out = append(out, Pad(" "+g+faint.Render("│")+line, w))
		}
		return frame(out, w, s.H, footer(w, "esc detach · ⇥ next running · k kill · q quit"))
	}

	out := chrome(w, false, "")
	rail := listRows(s, railW, bh, true)
	outW := w - railW - 3

	var right []string
	if l == nil {
		right = append([]string{Pad(ruleLabel("detail", outW, runningCount(s)), outW)}, idlePreview(r, outW)...)
	} else {
		gl, st := l.glyph()
		kindWord := "command"
		if l.worker {
			kindWord = "worker"
		}
		head := " " + st.Render(gl) + " " + bright.Render(l.name) +
			faint.Render("  ·  "+kindWord+"  ·  "+l.state+"  ·  "+l.elapsed)
		right = []string{
			Pad(" "+chips(s, outW-2), outW),
			Pad(faint.Render(strings.Repeat("╌", outW)), outW),
			Pad(head, outW),
		}
		right = append(right, livePreviewMeta(*l, outW)...)
		right = append(right, Pad(ruleLabel("output", outW, ""), outW))
		right = append(right, tailRows(*l, outW, bh-len(right))...)
	}

	for i := 0; i < bh; i++ {
		a, b := "", ""
		if i < len(rail) {
			a = rail[i]
		}
		if i < len(right) {
			b = right[i]
		}
		out = append(out, Pad(Pad(a, railW)+faint.Render("│ ")+b, w))
	}
	return frame(out, w, s.H, footer(w, browseKeys(s)))
}

// ═════════════════════════════════════════════════════════════════════════════
// D — HUD: nothing is docked and the list is never resized. A bordered panel
// floats over the bottom-right of the list, with the running things as tabs on
// its own top border — commands, a divider, then workers.
// ═════════════════════════════════════════════════════════════════════════════

func renderHUD(s State) string {
	syncCursor(s)
	w, bh := body(s)
	r, l := cur(s)

	if s.Is("screen", "attached") && l != nil {
		out := chrome(w, true, attachedID(*l))
		inner := w - 4
		out = append(out, Pad(" "+faint.Render("╭")+hudTabs(s, inner)+faint.Render("╮"), w))
		for _, line := range tailRows(*l, inner, bh-3) {
			out = append(out, Pad(" "+faint.Render("│")+Pad(line, inner)+faint.Render("│"), w))
		}
		out = append(out, Pad(" "+faint.Render("╰")+Pad(" "+faint.Render(fitPath(l.log, inner-2)), inner)+faint.Render("╯"), w))
		return frame(out, w, s.H, footer(w, "esc detach · ⇥ next running · k kill · q quit"))
	}

	out := chrome(w, false, "")
	boxW := mini(70, w-6)
	boxH := mini(12, bh-2)
	x0 := w - boxW - 2
	y0 := bh - boxH
	inner := boxW - 2

	full := listRows(s, w, bh, false)
	clipped := listRows(s, x0-1, bh, false)

	panel := make([]string, 0, boxH)
	if l == nil {
		panel = append(panel, faint.Render("╭─")+Pad(faint.Render(" not running ")+faint.Render(strings.Repeat("─", maxi(inner-15, 0))), inner-2)+faint.Render("─╮"))
		det := idlePreview(r, inner)
		for len(det) < boxH-2 {
			det = append(det, "")
		}
		for _, d := range det[:boxH-2] {
			panel = append(panel, faint.Render("│")+Pad(d, inner)+faint.Render("│"))
		}
		panel = append(panel, faint.Render("╰")+Pad(" "+faint.Render(runningCount(s)), inner)+faint.Render("╯"))
	} else {
		panel = append(panel, faint.Render("╭")+hudTabs(s, inner)+faint.Render("╮"))
		meta := livePreviewMeta(*l, inner)
		if len(meta) > 3 {
			meta = meta[:3] // the panel is small; the rest is one keypress away
		}
		for _, m := range meta {
			panel = append(panel, faint.Render("│")+Pad(m, inner)+faint.Render("│"))
		}
		for _, line := range tailRows(*l, inner, boxH-2-len(meta)) {
			panel = append(panel, faint.Render("│")+Pad(line, inner)+faint.Render("│"))
		}
		gl, st := l.glyph()
		status := st.Render(gl+" "+l.state) + faint.Render("  "+l.elapsed+"  ·  a attach  ·  esc collapse")
		panel = append(panel, faint.Render("╰")+Pad(" "+status, inner)+faint.Render("╯"))
	}

	for i := 0; i < bh; i++ {
		if i < y0 {
			out = append(out, Pad(full[i], w))
			continue
		}
		out = append(out, Pad(Pad(clipped[i], x0)+panel[i-y0], w))
	}
	return frame(out, w, s.H, footer(w, browseKeys(s)))
}

// hudTabs draws the running-things selector into the panel's own top border, so
// the panel costs no interior row to say what else is running. Commands, a
// divider, then workers.
func hudTabs(s State, w int) string {
	_, sel := cur(s)
	out := "─"
	prevWorker := false
	for i := range lives {
		l := lives[i]
		if l.worker && !prevWorker {
			out += faint.Render("┬")
			prevWorker = true
		}
		if sel != nil && l.name == sel.name {
			out += "[ " + bright.Render(l.name) + " ]"
		} else {
			out += faint.Render("─" + l.name + "─")
		}
	}
	return faint.Render(out + strings.Repeat("─", maxi(w-VW(Strip(out)), 0)))
}

// ─── plumbing ────────────────────────────────────────────────────────────────

// frame assembles a screen: rows already rendered, padded or clipped to the
// body budget, then the footer. Every row is exactly w columns and the result
// is exactly h rows — the two things the harness asserts.
func frame(out []string, w, h int, foot []string) string {
	want := h - len(foot)
	for len(out) < want {
		out = append(out, Pad("", w))
	}
	out = out[:want]
	out = append(out, foot...)
	for i := range out {
		out[i] = Pad(out[i], w)
	}
	return joinRows(out)
}

var Variants = []Variant{
	{
		Name:    "Dock",
		Caption: "full-width live band pinned under the list; the list gives up height, never width",
		Render:  renderDock,
		Key:     keys,
		Reset:   resetAll,
	},
	{
		Name:    "Ticker",
		Caption: "one pinned line per running thing, commands then workers; the pane is on demand",
		Render:  renderTicker,
		Key:     keys,
		Reset:   resetAll,
	},
	{
		Name:    "Output-first",
		Caption: "the split inverted — rows become a fixed 26-col rail, output owns the rest",
		Render:  renderOutputFirst,
		Key:     keys,
		Reset:   resetAll,
	},
	{
		Name:    "HUD",
		Caption: "a bordered panel floating over an unresized list, running things as tabs in its border",
		Render:  renderHUD,
		Key:     keys,
		Reset:   resetAll,
	},
}

func joinRows(out []string) string {
	var b strings.Builder
	for i, r := range out {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(r)
	}
	return b.String()
}
