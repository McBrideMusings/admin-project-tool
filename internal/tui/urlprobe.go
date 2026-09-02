// urlprobe.go is the Work-tab detail pane's reachability check (admin-1azz):
// a package-level, non-blocking cache over kinds.CheckURL so a hung host
// never stalls a repaint. Status is a pure map read; Refresh is the only
// thing that starts real work, one goroutine per stale url, and it always
// returns immediately.
package tui

import (
	"sync"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
)

// probeState is a url's last-known reachability, as read back by the
// detail pane's urlRow renderer.
type probeState int

const (
	probeUnknown probeState = iota // never probed, or a probe is still in flight
	probeUp
	probeDown
)

// probeURL is urlProber's test seam, bound to kinds.CheckURL by default so
// the dashboard reuses the exact same HTTP check print-urls already makes
// rather than a second one. A test overrides it to force a fixed result, or
// to block forever and prove Refresh never holds up a repaint waiting on it.
var probeURL = kinds.CheckURL

// probeTTL bounds how often the same url is re-checked: long enough that
// the Work tab's 1s tick doesn't fire a fresh HTTP request every repaint,
// short enough that a url coming up (or going down) shows within a few
// ticks of the pane being open.
const probeTTL = 5 * time.Second

// urlProber is a package-level, mutex-guarded reachability cache — the same
// singleton shape internal/livework.Default uses for the same reason: one
// process, one shared source of truth, read from render and written from
// background goroutines.
type urlProber struct {
	mu       sync.Mutex
	state    map[string]probeState
	inflight map[string]bool
	at       map[string]time.Time
}

var urlProbes = newURLProber()

func newURLProber() *urlProber {
	return &urlProber{
		state:    map[string]probeState{},
		inflight: map[string]bool{},
		at:       map[string]time.Time{},
	}
}

// Status reports url's last-known reachability. Pure map read under the
// mutex — never starts a probe, never blocks on one in flight.
func (p *urlProber) Status(url string) probeState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state[url]
}

// Refresh starts a probe for every url in urls that is stale (never probed,
// or last probed more than probeTTL ago) and not already in flight. It
// returns immediately regardless of how many probes it started or how long
// any of them take — each runs on its own goroutine, and a url whose probe
// hasn't landed yet simply stays at its current (possibly probeUnknown)
// state until it does.
func (p *urlProber) Refresh(urls []string) {
	now := time.Now()
	for _, u := range urls {
		if u == "" {
			continue
		}
		p.mu.Lock()
		stale := now.Sub(p.at[u]) >= probeTTL
		if p.inflight[u] || !stale {
			p.mu.Unlock()
			continue
		}
		p.inflight[u] = true
		p.mu.Unlock()

		go func(u string) {
			up := probeURL(u)
			p.mu.Lock()
			if up {
				p.state[u] = probeUp
			} else {
				p.state[u] = probeDown
			}
			p.at[u] = time.Now()
			p.inflight[u] = false
			p.mu.Unlock()
		}(u)
	}
}

// reset clears every recorded probe. Test-only: urlProbes is a process-wide
// singleton, so a test that stubs probeURL and asserts on a specific url
// must reset first or use a url no other test in this package touches —
// the same caveat livework.Default's own tests carry (see
// livestate_race_test.go).
func (p *urlProber) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = map[string]probeState{}
	p.inflight = map[string]bool{}
	p.at = map[string]time.Time{}
}
