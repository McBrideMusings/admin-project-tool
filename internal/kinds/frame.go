package kinds

import (
	"github.com/McBrideMusings/admin-project-tool/internal/frame"
	"github.com/McBrideMusings/admin-project-tool/internal/luahost"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// FrameURLs resolves the urls a framed action WOULD pin, without activating
// a frame — the render-side sibling of enterFrame, reused by the Work tab's
// detail pane so it can show the same rows the footer would without a
// second resolver. It runs the identical two-step pipeline enterFrame's
// caller feeds it (frame.ResolveURLs: a named [status] entry's url, then
// ResolveComputed then ResolveEnv) and, for a non-empty target, the exact
// per-target inheritance resolveTargetFrame applies at dispatch time (an
// absent override inherits the action's own list, an explicit list
// replaces it, an empty list opts out). target == "" resolves the action's
// own urls with no per-target override applied at all.
func FrameURLs(action manifest.Action, target string, m *manifest.Manifest) ([]frame.URLEntry, error) {
	raw := actionURLs(action)
	if target != "" {
		tm := targetFrameMetaOf(action, target)
		raw, _, _ = resolveTargetFrame(raw, false, false, tm)
	}
	return frame.ResolveURLs(raw, m)
}

// enterFrame is called by each kind factory that can declare `urls`/`footer`
// on itself, rather than by a shared dispatch wrapper doing it once for
// every kind. A generic wrapper can't work here: a multi-target's label and
// log-context name depend on which target gets picked, information only its
// own closure has once it has read args — so enterFrame is called once per
// target, after picking, not once per command.
//
// hasFooter/footerVal carry a tri-state — absent/true/false — for the
// `footer` key: hasFooter=false means "the key was absent", which now
// defaults the footer ON regardless of the resolved URL count — TOML has no
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
	// Materializing the log context here (SetLogContext + MaterializeLogFile)
	// is a real side effect — it names and writes a log file, and announces
	// it on Stderr — that historically happened whenever the caller *asked*
	// for a footer: an explicit footer=true, or a non-empty urls list (the
	// pre-flip ShouldActivate formula, still exactly this). Both stay
	// unconditional, terminal or not: an explicit ask for the log path is
	// honoured even piped/CI, matching the tool's behaviour before the
	// footer-default flip.
	//
	// The flip's new case — hasFooter absent AND no urls — is different: it
	// is the tool's own default, not something the manifest asked for, so it
	// must not force a materialization Activate is about to decline anyway.
	// frame.CanActivate runs the same checks Activate itself will (a frame
	// already active, ADMIN_NO_FRAME, h.Stdout not a terminal); skipping it
	// only for this case is what keeps a plain CLI/piped dispatch — which
	// declares no urls and no footer — silent, exactly as before the flip.
	explicitlyAsked := (hasFooter && footerVal) || (!hasFooter && len(resolved) > 0)
	if !explicitlyAsked && !frame.CanActivate(h.Stdout) {
		return nil
	}
	h.SetLogContext([]string{label}, luahost.RetainUnset)
	return frame.Activate(label, resolved, h.MaterializeLogFile(), h.Stdout)
}
