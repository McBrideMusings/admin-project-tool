// Package herdr is the first internal/agent.Driver implementation: it
// shells the `herdr agent ...` CLI, argv-identical to what
// internal/shim/agent.go did before ADR-0021 split "where an agent lives"
// (config, [terminal]) from "what is inside it" (this package). The socket
// API at crate::session::active_api_socket_path() is the obvious second
// driver — it would kill agentpoll's poll — and is deliberately not built
// here; see docs/adr/0021-agent-interaction-is-code-terminals-are-config.md.
package herdr

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/agent"
)

func init() {
	agent.Register("herdr", available, New())
}

// available reports whether this process is running where the herdr CLI is
// reachable — the same shape internal/shim.Available() checks, kept local
// so this package has no dependency on internal/shim.
func available() bool {
	if os.Getenv("HERDR_ENV") != "1" || os.Getenv("REPO_HERDR") == "off" {
		return false
	}
	_, err := exec.LookPath("herdr")
	return err == nil
}

// run is the single exec seam every verb below goes through, so tests can
// assert argv construction without a real herdr binary on PATH.
var run = func(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "herdr", args...).Output()
}

// Driver is the herdr-backed internal/agent.Driver.
type Driver struct{}

// New returns a herdr Driver. A plain constructor rather than a package-level
// value so a future test can hold one without touching the registry.
func New() *Driver { return &Driver{} }

// agentName derives a herdr agent name from an opaque launcher handle:
// `herdr agent start` requires a name matching [a-z][a-z0-9_-]{0,31}, so
// agent.SanitizeName lowercases it, strips everything else out and caps it
// at 32 characters rather than passing the handle straight through.
// Identity everywhere AFTER start is the handle itself (resolve_agent_target
// tries a pane id first, then falls back to a name), so this derived name
// only has to be valid and non-colliding at start time, not memorable.
func agentName(handle string) string {
	return agent.SanitizeName(handle, "agent", "a-", 32)
}

// Start runs `herdr agent start <name> --kind <kind> --pane <handle> [--
// <args...>]`. handle must already be a pane sitting at an interactive
// shell prompt.
func (d *Driver) Start(handle, kind string, args []string) error {
	argv := []string{"agent", "start", agentName(handle), "--kind", kind, "--pane", handle}
	if len(args) > 0 {
		argv = append(argv, "--")
		argv = append(argv, args...)
	}
	_, err := run(context.Background(), argv...)
	return err
}

// Prompt runs `herdr agent prompt <handle> <text> --wait [--until <s>]...`.
// --until is repeatable and requires --wait; an empty until list sends bare
// --wait, which settles on herdr's own default state set. resolve_agent_
// target tries a pane id first, so handle (not the derived agent name) is
// what addresses the running session from here on.
func (d *Driver) Prompt(handle, text string, until []agent.Status) error {
	argv := []string{"agent", "prompt", handle, text, "--wait"}
	for _, s := range until {
		argv = append(argv, "--until", string(s))
	}
	_, err := run(context.Background(), argv...)
	return err
}

// Info runs `herdr agent get <handle>` bound to ctx and returns the two
// fields callers read: the status word and the stripped terminal title.
func (d *Driver) Info(ctx context.Context, handle string) (agent.Info, error) {
	out, err := run(ctx, "agent", "get", handle)
	if err != nil {
		return agent.Info{}, err
	}
	var resp struct {
		Result struct {
			Agent struct {
				AgentStatus           string `json:"agent_status"`
				TerminalTitleStripped string `json:"terminal_title_stripped"`
			} `json:"agent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return agent.Info{}, err
	}
	a := resp.Result.Agent
	status := agent.Status(a.AgentStatus)
	if status == "" {
		status = agent.StatusUnknown
	}
	return agent.Info{
		Status:   status,
		Activity: strings.TrimSpace(a.TerminalTitleStripped),
	}, nil
}
