// workurls_test.go covers admin-1azz's acceptance criteria: the live band's
// field block showing a running command's log and resolved urls with a
// non-blocking reachability indicator (admin-3rgp.72.6 moved this from the
// old right-hand detail pane onto the full-width band's bandFieldsCommand,
// with the row's own state/elapsed now said once on the band's title rule
// instead — see TestBandTitleShowsRunningStateAndElapsed in
// workband_test.go). Follows work_test.go's workTestModel + livework.
// Default fixture pattern used throughout this package rather than standing
// up a full headless Frame (which needs a real repo on disk) —
// commandRowItem/bandFieldsCommand ARE the dashboard's own rendering path
// for this block, just invoked directly.
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/livework"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// stubProbe swaps urlProbes' probe function for a fixed-result function for
// the life of the test, resets urlProbes so no earlier test's cached state
// leaks in, and restores both on cleanup. The swap goes through setProbe
// (mutex-guarded) rather than a package var, so it is race-free against any
// goroutine a prior test leaked.
func stubProbe(t *testing.T, fn func(string) bool) {
	t.Helper()
	orig := urlProbes.setProbe(fn)
	urlProbes.reset()
	t.Cleanup(func() {
		urlProbes.setProbe(orig)
		urlProbes.reset()
	})
}

// TestBandShowsLiveLogElapsedAndURLs is the running-command half of the
// acceptance criteria: the band's title rule shows the elapsed value and its
// field block shows the live entry's own log path (not a glob) plus one row
// per configured url.
func TestBandShowsLiveLogElapsedAndURLs(t *testing.T) {
	stubProbe(t, func(string) bool { return true })

	m := &manifest.Manifest{
		Status: map[string]manifest.StatusSpec{"api": {Kind: "http", URL: "http://example.com/api"}},
		Custom: map[string]manifest.Command{
			"dev": {Desc: "start dev server", Steps: []string{"dev"}},
		},
		Actions: map[string]manifest.Action{
			"dev": {"kind": "shell", "run": "run-dev", "urls": []any{"api"}},
		},
	}
	mm := workTestModel(m)

	freshLivework(t)
	live := &livework.Entry{
		ID: "dev-detail-test", Name: "dev", Handle: "pty-dev-detail-test", State: livework.StateRunning,
		LogPath: "/live/dev.log", StartedAt: time.Now().Add(-5 * time.Minute),
	}
	if err := livework.Default.Add(live); err != nil {
		t.Fatalf("Add(%s): %v", live.ID, err)
	}
	row := mm.commandRowItem(workCmdRow{name: "dev", cmd: m.Custom["dev"]}, map[string]*livework.Entry{"dev": live})

	title := bandTitle(80, live, row.work.cmdName)
	if !strings.Contains(title, "5m") {
		t.Errorf("band title missing elapsed value:\n%s", title)
	}

	fields := strings.Join(bandFieldsCommand(row.work, 60), "\n")
	if !strings.Contains(fields, "/live/dev.log") {
		t.Errorf("band fields missing live log path:\n%s", fields)
	}
	if !strings.Contains(fields, "api") || !strings.Contains(fields, "http://example.com/api") {
		t.Errorf("band fields missing the resolved [status]-keyed row:\n%s", fields)
	}
}

// TestFrameURLsBareVsKeyedLabel proves the [status]-keyed entry renders its
// key as the label and a bare URL renders as itself — both through the same
// kinds.FrameURLs path commandRowItem calls.
func TestFrameURLsBareVsKeyedLabel(t *testing.T) {
	m := &manifest.Manifest{
		Status: map[string]manifest.StatusSpec{"web": {Kind: "http", URL: "http://example.com"}},
		Custom: map[string]manifest.Command{"serve": {Desc: "serve", Steps: []string{"serve"}}},
		Actions: map[string]manifest.Action{
			"serve": {"kind": "shell", "run": "run-serve", "urls": []any{"web", "http://raw.example.com"}},
		},
	}
	mm := workTestModel(m)
	row := mm.commandRowItem(workCmdRow{name: "serve", cmd: m.Custom["serve"]}, map[string]*livework.Entry{})

	if len(row.work.urls) != 2 {
		t.Fatalf("urls = %+v, want 2 entries", row.work.urls)
	}
	if row.work.urls[0].Label != "web" || row.work.urls[0].URL != "http://example.com" {
		t.Errorf("keyed entry = %+v, want label %q url %q", row.work.urls[0], "web", "http://example.com")
	}
	if row.work.urls[1].Label != row.work.urls[1].URL || row.work.urls[1].URL != "http://raw.example.com" {
		t.Errorf("bare entry = %+v, want a self-labelled bare url", row.work.urls[1])
	}
}

// TestTargetURLsInheritanceTriState pins the footer's own inherit rule as
// applied to the Work tab's per-target rows: an override-free target
// inherits the parent's list, an explicit list replaces it, and urls = []
// yields no url rows at all.
func TestTargetURLsInheritanceTriState(t *testing.T) {
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"preview": {Desc: "preview", Steps: []string{"preview"}},
		},
		Actions: map[string]manifest.Action{
			"preview": {
				"kind": "multi-target",
				"urls": []any{"http://parent.example.com"},
				"inherits": map[string]any{
					"run": "run-inherits",
				},
				"overrides": map[string]any{
					"run":  "run-overrides",
					"urls": []any{"http://child.example.com"},
				},
				"optsout": map[string]any{
					"run":  "run-optsout",
					"urls": []any{},
				},
			},
		},
	}
	mm := workTestModel(m)
	row := mm.commandRowItem(workCmdRow{name: "preview", cmd: m.Custom["preview"]}, map[string]*livework.Entry{})

	if len(row.work.targetURLs["inherits"]) != 1 || row.work.targetURLs["inherits"][0].URL != "http://parent.example.com" {
		t.Errorf("inherits target urls = %+v, want the parent's list", row.work.targetURLs["inherits"])
	}
	if len(row.work.targetURLs["overrides"]) != 1 || row.work.targetURLs["overrides"][0].URL != "http://child.example.com" {
		t.Errorf("overrides target urls = %+v, want its own list", row.work.targetURLs["overrides"])
	}
	if len(row.work.targetURLs["optsout"]) != 0 {
		t.Errorf("optsout target urls = %+v, want none", row.work.targetURLs["optsout"])
	}
}

// TestFrameURLsExpandsComputedVar proves a ${VAR}-style computed value
// (${LAN_IP}, resolved by manifest.ResolveComputed before ResolveEnv) shows
// resolved in the detail pane, matching the pinned footer's own expansion.
// An explicit LAN_IP env var wins over the computed lookup (ResolveEnv's own
// documented rule), which is what makes this deterministic in a test.
func TestFrameURLsExpandsComputedVar(t *testing.T) {
	t.Setenv("LAN_IP", "10.0.0.5")

	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"dev": {Desc: "dev", Steps: []string{"dev"}},
		},
		Actions: map[string]manifest.Action{
			"dev": {"kind": "shell", "run": "run-dev", "urls": []any{"http://${LAN_IP}:8787"}},
		},
	}
	mm := workTestModel(m)
	row := mm.commandRowItem(workCmdRow{name: "dev", cmd: m.Custom["dev"]}, map[string]*livework.Entry{})

	if len(row.work.urls) != 1 || row.work.urls[0].URL != "http://10.0.0.5:8787" {
		t.Errorf("urls = %+v, want ${LAN_IP} expanded to 10.0.0.5", row.work.urls)
	}
}

// TestMultiStepCommandCollectsAllStepsURLs pins the admin-tui-multistep-urls
// fix: a two-step command (social-chess's `dev` — db-ensure then dev) must
// show the running step's url row even though only the second step declares
// one. soleAction returns false for any command with more than one step, so
// before this fix wd.urls stayed empty for every multi-step command.
func TestMultiStepCommandCollectsAllStepsURLs(t *testing.T) {
	m := &manifest.Manifest{
		Custom: map[string]manifest.Command{
			"dev": {Desc: "start dev server", Steps: []string{"db-ensure", "dev"}},
		},
		Actions: map[string]manifest.Action{
			"db-ensure": {"kind": "shell", "run": "run-db-ensure"},
			"dev":       {"kind": "interactive-shell", "run": "run-dev", "urls": []any{"http://127.0.0.1:5173"}},
		},
	}
	mm := workTestModel(m)
	row := mm.commandRowItem(workCmdRow{name: "dev", cmd: m.Custom["dev"]}, map[string]*livework.Entry{})

	if len(row.work.urls) != 1 || row.work.urls[0].URL != "http://127.0.0.1:5173" {
		t.Errorf("urls = %+v, want the second step's url", row.work.urls)
	}
}

// TestURLProbeDistinguishesUpFromDown proves the reachability indicator
// actually differs between a url stubbed as up and one stubbed as down.
func TestURLProbeDistinguishesUpFromDown(t *testing.T) {
	stubProbe(t, func(u string) bool { return u == "http://up.example.com" })

	urlProbes.Refresh([]string{"http://up.example.com", "http://down.example.com"})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if urlProbes.Status("http://up.example.com") != probeUnknown &&
			urlProbes.Status("http://down.example.com") != probeUnknown {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if urlProbes.Status("http://up.example.com") != probeUp {
		t.Errorf("up url status = %v, want probeUp", urlProbes.Status("http://up.example.com"))
	}
	if urlProbes.Status("http://down.example.com") != probeDown {
		t.Errorf("down url status = %v, want probeDown", urlProbes.Status("http://down.example.com"))
	}
}

// TestURLProbeNeverBlocksOnAHungHost is the acceptance criterion in its own
// right: Refresh against a stubbed probe that never returns must itself
// return immediately, and the url's status stays probeUnknown until (if
// ever) the probe lands.
func TestURLProbeNeverBlocksOnAHungHost(t *testing.T) {
	block := make(chan struct{})
	stubProbe(t, func(string) bool {
		<-block
		return true
	})
	// Registered after stubProbe, so it runs first (Cleanup is LIFO):
	// unblock the leaked probe goroutine before stubProbe's own cleanup
	// swaps the probe function and resets the prober's maps out from under
	// it. Refresh already captured today's probe into a local before
	// starting the goroutine, so this leaked goroutine can never race that
	// swap — it just needs to be woken so it doesn't run forever.
	t.Cleanup(func() { close(block) })

	done := make(chan struct{})
	go func() {
		urlProbes.Refresh([]string{"http://hung.example.com"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Refresh blocked on a hung probe instead of returning immediately")
	}
	if got := urlProbes.Status("http://hung.example.com"); got != probeUnknown {
		t.Errorf("status while probe is in flight = %v, want probeUnknown", got)
	}
}
