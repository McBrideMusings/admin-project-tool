package kinds

import (
	"bytes"
	"testing"

	"github.com/creack/pty"

	"github.com/McBrideMusings/admin-project-tool/internal/frame"
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

// TestEnterFramePlainDispatchOnRealTerminalRaisesAFrame proves the "a plain
// shell run still raises a footer" half of the hosted-pty frame-suppression
// fix: with ADMIN_NO_FRAME unset and h.Stdout a REAL terminal (a pty, unlike
// every other test in this file's bytes.Buffer), enterFrame's Activate call
// must actually succeed — a plain `admin dev` typed at a terminal must keep
// its footer exactly as before StartHostedPTY started setting
// HostedNoFrameEnv on its own hosted child's environment.
func TestEnterFramePlainDispatchOnRealTerminalRaisesAFrame(t *testing.T) {
	t.Setenv("ADMIN_NO_FRAME", "")
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	t.Cleanup(func() { _ = master.Close(); _ = slave.Close() })

	h := luahost.New(t.TempDir())
	h.Stdout = slave

	f := enterFrame(h, "dev", []any{"http://x"}, false, false, nil)
	if f == nil {
		t.Fatal("expected a frame — a plain dispatch on a real terminal with no ADMIN_NO_FRAME must still raise the footer")
	}
	frame.Deactivate(f)
}

// TestEnterFrameHostedNoFrameEnvSuppressesEvenAnExplicitFooterAsk is the
// hosted-pty half: HostedNoFrameEnv (ADMIN_NO_FRAME) set on the process —
// exactly what StartHostedPTY sets on its child's environment — must
// suppress the footer even on a real terminal AND even for an explicit
// footer=true ask, which enterFrame's explicitlyAsked flag lets bypass the
// CanActivate pre-check. If explicitlyAsked bypassed ADMIN_NO_FRAME too, a
// manifest declaring `footer = true` could re-enable the footer inside a
// hosted run — this pins that it can't: frame.Activate itself checks
// ADMIN_NO_FRAME unconditionally, after any pre-check enterFrame skips.
func TestEnterFrameHostedNoFrameEnvSuppressesEvenAnExplicitFooterAsk(t *testing.T) {
	t.Setenv("ADMIN_NO_FRAME", "1")
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	t.Cleanup(func() { _ = master.Close(); _ = slave.Close() })

	h := luahost.New(t.TempDir())
	h.Stdout = slave

	if f := enterFrame(h, "dev", nil, true, true, nil); f != nil {
		frame.Deactivate(f)
		t.Fatal("expected nil — ADMIN_NO_FRAME must suppress the footer even for an explicit footer=true ask on a real terminal")
	}
}
