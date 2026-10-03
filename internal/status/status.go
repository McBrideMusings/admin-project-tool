// Package status runs the built-in checkers behind a manifest's
// [status.<label>] tables: one reachability check (http), one JSON readout
// (json) and one shell readout (command). A project configures a checker; it
// never reimplements one.
package status

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jmespath-community/go-jmespath"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// Timeout bounds every network request and command a checker makes, so a hung
// host or script can delay one readout but never the dashboard around it.
var Timeout = 5 * time.Second

// State is the coarse health a row's dot shows.
type State int

const (
	Unknown State = iota
	Up
	Down
)

// Result is one readout: the text to show and whether the check succeeded.
type Result struct {
	State State
	Text  string
}

// CheckURL reports whether url answers with a 2xx/3xx status. Any error
// (connection refused, timeout, TLS failure) reads as down.
func CheckURL(rawURL string) bool {
	_, err := get(rawURL, nil, false)
	return err == nil
}

func get(rawURL string, headers map[string]string, wantBody bool) ([]byte, error) {
	client := &http.Client{
		Timeout: Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if !wantBody {
		return nil, nil
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

// Run evaluates one entry. dir is the working directory of a command entry
// (the project root). A failed check is a Down result carrying the reason,
// never a stale value.
func Run(spec manifest.StatusSpec, dir string) Result {
	switch spec.Kind {
	case manifest.StatusHTTP:
		return runHTTP(spec)
	case manifest.StatusJSON:
		return runJSON(spec)
	case manifest.StatusCommand:
		return runCommand(spec, dir)
	}
	return Result{State: Down, Text: fmt.Sprintf("unknown kind %q", spec.Kind)}
}

func resolve(s string) (string, error) {
	return manifest.ResolveEnv(manifest.ResolveComputed(s))
}

func resolveHeaders(h map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(h))
	for k, v := range h {
		r, err := resolve(v)
		if err != nil {
			return nil, err
		}
		out[k] = r
	}
	return out, nil
}

func runHTTP(spec manifest.StatusSpec) Result {
	url, err := resolve(spec.URL)
	if err != nil {
		return Result{State: Down, Text: err.Error()}
	}
	headers, err := resolveHeaders(spec.Headers)
	if err != nil {
		return Result{State: Down, Text: err.Error()}
	}
	if _, err := get(url, headers, false); err != nil {
		return Result{State: Down, Text: "down"}
	}
	return Result{State: Up, Text: "up"}
}

func runJSON(spec manifest.StatusSpec) Result {
	url, err := resolve(spec.URL)
	if err != nil {
		return Result{State: Down, Text: err.Error()}
	}
	headers, err := resolveHeaders(spec.Headers)
	if err != nil {
		return Result{State: Down, Text: err.Error()}
	}
	body, err := get(url, headers, true)
	if err != nil {
		return Result{State: Down, Text: "unreachable: " + reason(err)}
	}
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return Result{State: Down, Text: "not JSON: " + err.Error()}
	}
	values := make(map[string]string, len(spec.Pull))
	for _, name := range sortedNames(spec.Pull) {
		v, err := jmespath.Search(spec.Pull[name], doc)
		if err != nil {
			return Result{State: Down, Text: fmt.Sprintf("pull %s: %v", name, err)}
		}
		values[name] = format(v)
	}
	return Result{State: Up, Text: clean(fill(spec.Show, values))}
}

func runCommand(spec manifest.StatusSpec, dir string) Result {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", manifest.ResolveComputed(spec.Run))
	cmd.Dir = dir
	// A script that backgrounds a child keeps stdout open after sh is killed;
	// WaitDelay closes the pipe so Output returns instead of waiting for it.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return Result{State: Down, Text: "timed out"}
		}
		return Result{State: Down, Text: err.Error()}
	}
	for _, line := range strings.Split(string(out), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return Result{State: Up, Text: clean(t)}
		}
	}
	return Result{State: Up, Text: ""}
}

// format renders a JMESPath result for a show template: null is "-", a whole
// number drops its ".0", a string is verbatim, anything else is compact JSON.
func format(v any) string {
	switch x := v.(type) {
	case nil:
		return "-"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

var placeholder = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

func fill(tmpl string, values map[string]string) string {
	return placeholder.ReplaceAllStringFunc(tmpl, func(m string) string {
		if v, ok := values[m[1:len(m)-1]]; ok {
			return v
		}
		return m
	})
}

// Validate checks what a manifest author can get wrong without running
// anything: a pull expression that does not compile, or a {name} in show
// that no pull entry produces. `admin check` reports every problem.
func Validate(label string, spec manifest.StatusSpec) []error {
	var errs []error
	if spec.Kind != manifest.StatusJSON {
		return nil
	}
	for _, n := range sortedNames(spec.Pull) {
		if _, err := jmespath.Compile(spec.Pull[n]); err != nil {
			errs = append(errs, fmt.Errorf("[status.%s] pull.%s: %v", label, n, err))
		}
	}
	for _, m := range placeholder.FindAllStringSubmatch(spec.Show, -1) {
		if _, ok := spec.Pull[m[1]]; !ok {
			errs = append(errs, fmt.Errorf("[status.%s] show uses {%s} but pull has no %q", label, m[1], m[1]))
		}
	}
	return errs
}

func sortedNames(m map[string]string) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// reason is err without the request URL a *url.Error carries: the URL may
// hold a token the manifest resolved from the environment.
func reason(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

// clean replaces every control character with a space, so a response or
// command that prints an escape sequence cannot drive the terminal.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
