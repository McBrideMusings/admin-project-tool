package shim

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
)

// StartAgent starts a herdr-recognized coding agent of kind in the existing
// pane paneID, named name — `herdr agent start <name> --kind <kind> --pane
// <paneID>`. The pane must already exist and be at an idle shell prompt;
// this never splits or creates one (see SplitPane). Output is discarded:
// callers read state back through AgentWait/AgentDone rather than parsing
// `agent start`'s own JSON, matching OpenInHerdr's own reasoning for
// dropping stdout/stderr.
func StartAgent(name, kind, paneID string) error {
	cmd := exec.Command("herdr", "agent", "start", name, "--kind", kind, "--pane", paneID)
	return cmd.Run()
}

// PromptAndWait sends prompt to the named agent and blocks until herdr
// observes the agent settle into idle, done, or blocked — `herdr agent
// prompt <name> <prompt> --wait`. This is admin-3rgp.25's concurrency-
// ceiling mechanism: the goroutine dispatching one workflow target calls
// this and does not return (so does not free its ceiling slot) until the
// agent it started has actually stopped working, exactly the "no more than
// the ceiling run at once" acceptance criterion asks for — there is no
// separate polling loop because herdr's own --wait already blocks on the
// real lifecycle transition instead of a guessed one.
func PromptAndWait(name, prompt string) error {
	cmd := exec.Command("herdr", "agent", "prompt", name, prompt, "--wait")
	return cmd.Run()
}

// AgentStatus reports herdr's own status word for the named agent — idle,
// done, blocked, working, or unknown, per `herdr agent get <target>`. It is
// how a workflow target that just settled (PromptAndWait returned with no
// error) is told apart as "waiting on input" (blocked) rather than actually
// finished (idle/done): PromptAndWait itself blocks until any of those three
// settle states is reached, so its return alone can't distinguish them.
func AgentStatus(name string) (string, error) {
	info, err := AgentInfoContext(context.Background(), name)
	return info.Status, err
}

// AgentInfo is one `herdr agent get` reply's status word plus the agent's own
// one-line summary of what it is doing right now. Status is `agent_status`
// (idle, done, blocked, working, or unknown). Activity is
// `terminal_title_stripped` — verified live against a running herdr on
// 2026-09-02 (`herdr agent get madrid` returned
// `"terminal_title_stripped":"Madrid relay app implementation"` on the same
// JSON object as `agent_status`) — and is not schema-guaranteed by herdr: an
// older or unusual herdr build can omit it, in which case Activity is simply
// "".
type AgentInfo struct {
	Status   string
	Activity string
}

// AgentInfoContext is AgentStatus bound to ctx and widened to also return the
// agent's activity title, so a caller that polls repeatedly (the Work tab's
// per-tick poller — admin-spjp.3) can bound each query with a timeout instead
// of risking a wedged herdr hanging forever, and can read both fields off the
// single reply without a second subprocess. dispatchOne's one-shot
// post-settle call goes through the narrow AgentStatus above and stays
// unbounded/unchanged.
func AgentInfoContext(ctx context.Context, name string) (AgentInfo, error) {
	out, err := exec.CommandContext(ctx, "herdr", "agent", "get", name).Output()
	if err != nil {
		return AgentInfo{}, err
	}
	var resp struct {
		Result struct {
			Agent struct {
				Status string `json:"agent_status"`
				Title  string `json:"terminal_title_stripped"`
			} `json:"agent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return AgentInfo{}, err
	}
	return AgentInfo{
		Status:   resp.Result.Agent.Status,
		Activity: strings.TrimSpace(resp.Result.Agent.Title),
	}, nil
}
