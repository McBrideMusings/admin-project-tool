package kinds

import (
	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// enterFrame is the Go port of admin_lib/interp.py's _enter_frame, called by
// each kind factory that can declare `urls`/`footer` on itself — mirroring
// the reference architecture exactly: Python's _make_shell and
// _make_multi_target each call _enter_frame themselves rather than a shared
// dispatch wrapper doing it once for every kind. A generic wrapper can't
// work here: a multi-target's label and log-context name depend on which
// target gets picked, information only its own closure has once it has read
// args — the same reason _enter_frame in the Python original is called once
// per target, after picking, not once per command.
//
// hasFooter/footerVal carry the tri-state Python gets from
// cmd.meta.get("footer") returning None/True/False: hasFooter=false means
// "the key was absent" (decide from the resolved URL count) — TOML has no
// undecided-boolean literal, so that's threaded as a second return from
// actionBool rather than a pointer.
//
// h is reused (not a fresh Host) for both the log context this sets and the
// h.RunCmd the caller goes on to make, so the log path the footer names is
// always the same file the kind's own output actually lands in.
func enterFrame(h *luahost.Host, label string, rawURLs []any, hasFooter, footerVal bool, m *manifest.Manifest) *frame.Frame {
	resolved, err := frame.ResolveURLs(rawURLs, m)
	if err != nil {
		// A bad ${VAR} in a urls entry degrades the footer, not the
		// action — the action may not even need it to succeed.
		h.Warn("could not resolve action urls for footer: " + err.Error())
		resolved = nil
	}
	if !frame.ShouldActivate(len(resolved), hasFooter, footerVal) {
		return nil
	}
	h.SetLogContext([]string{label}, luahost.RetainUnset)
	return frame.Activate(label, resolved, h.MaterializeLogFile(), h.Stdout)
}
