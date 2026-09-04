package kinds

import (
	"bytes"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
)

// enterFrame's Activate call always declines in `go test` (h.Stdout is a
// bytes.Buffer, never a terminal), so these tests can't observe a painted
// footer — internal/frame's own tests cover that. What IS observable, and
// is exactly this ticket's acceptance criterion ("footer=true raises a
// frame for an action with no URLs; footer=false suppresses one the URLs
// would have raised"), is whether enterFrame decided there SHOULD be a
// footer at all: for an explicit ask (footer=true, or a non-empty urls
// list) it forces the log context to materialize before handing the path
// to frame.Activate, so "a log file got created" is a reliable proxy for
// "enterFrame decided yes" even when Activate itself says no.
//
// The one case that does NOT force materialization is the tool's own
// default — hasFooter absent and no urls declared. That is not something
// the manifest asked for, so enterFrame skips the log side effect there
// unless a frame can actually go up (frame.CanActivate) — otherwise every
// plain CLI/piped dispatch, which declares neither, would gain a stray log
// file and an unrequested Stderr announcement purely from the default
// flipping on.

func newFrameTestHost(t *testing.T) *luahost.Host {
	t.Helper()
	h := luahost.New(t.TempDir())
	h.Stdout = &bytes.Buffer{}
	return h
}

func TestEnterFrameFooterTrueMaterializesLogEvenWithNoURLs(t *testing.T) {
	h := newFrameTestHost(t)
	if f := enterFrame(h, "dev", nil, true, true, nil); f != nil {
		t.Fatal("expected nil — h.Stdout is not a terminal")
	}
	if h.LogFile() == "" {
		t.Fatal("expected a materialized log file even though Activate declined")
	}
}

func TestEnterFrameNoURLsAndNoFooterDoesNotMaterializeOffline(t *testing.T) {
	h := newFrameTestHost(t)
	if f := enterFrame(h, "dev", nil, false, false, nil); f != nil {
		t.Fatal("expected nil — h.Stdout is not a terminal")
	}
	if h.LogFile() != "" {
		t.Fatal("expected no materialized log file — the default footer is not an explicit ask, so it must not force a log side effect Activate is about to decline anyway")
	}
}

func TestEnterFrameFooterFalseSuppressesEvenWithURLs(t *testing.T) {
	h := newFrameTestHost(t)
	if f := enterFrame(h, "dev", []any{"http://x"}, true, false, nil); f != nil {
		t.Fatal("expected nil")
	}
	if h.LogFile() != "" {
		t.Fatal("expected no log context — footer=false suppresses even with urls")
	}
}

func TestEnterFrameURLsAloneMaterializeLog(t *testing.T) {
	h := newFrameTestHost(t)
	if f := enterFrame(h, "dev", []any{"http://x"}, false, false, nil); f != nil {
		t.Fatal("expected nil (not a tty)")
	}
	if h.LogFile() == "" {
		t.Fatal("expected a materialized log file")
	}
}
