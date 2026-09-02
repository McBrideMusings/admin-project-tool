package kinds

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// httpClientTimeout mirrors admin_lib/interp.py::_make_http's urlopen(...,
// timeout=15).
const httpClientTimeout = 15 * time.Second

// makeHTTP is kind="http"'s factory — the Go port of
// admin_lib/interp.py::_make_http. `run` is the URL, `method` defaults to
// POST, and an optional `body` is sent as JSON.
//
// Deliberate parity quirk carried over from Python: a failed or >=400
// request only warns — the Runner returns nil — never fails the action.
// This is not an oversight; _make_http's _fn has no sys.exit on any request
// outcome, only on... nothing, actually: it never exits at all. Only a
// resolution failure (an unset ${VAR} the URL or body references, with no
// default) is a hard error here, matching resolve_env's own
// sys.exit(2)-equivalent behaviour.
func makeHTTP(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	urlTmpl, ok := actionString(action, "run")
	if !ok || urlTmpl == "" {
		return nil, fmt.Errorf(`action %q: kind="http" requires 'run' (the URL)`, name)
	}
	method, _ := actionString(action, "method")
	if method == "" {
		method = "POST"
	}
	bodyTmpl, _ := actionString(action, "body")

	return func(args []string) error {
		h := newHost(m)
		url, err := manifest.ResolveEnv(urlTmpl)
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		h.Info(fmt.Sprintf("http %s %s", method, url))

		var reqBody *strings.Reader
		if bodyTmpl != "" {
			resolvedBody, err := manifest.ResolveEnv(bodyTmpl)
			if err != nil {
				return fmt.Errorf("action %q: %w", name, err)
			}
			reqBody = strings.NewReader(resolvedBody)
		} else {
			reqBody = strings.NewReader("")
		}

		req, err := http.NewRequest(method, url, reqBody)
		if err != nil {
			h.Warn("http request failed: " + err.Error())
			return nil
		}
		if bodyTmpl != "" {
			req.Header.Set("Content-Type", "application/json")
		}

		client := &http.Client{Timeout: httpClientTimeout}
		resp, err := client.Do(req)
		if err != nil {
			h.Warn("http request failed: " + err.Error())
			return nil
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			h.Warn(fmt.Sprintf("http %d from %s", resp.StatusCode, url))
		} else {
			h.Ok("done")
		}
		return nil
	}, nil
}
