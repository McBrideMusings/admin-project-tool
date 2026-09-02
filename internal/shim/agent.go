package shim

import (
	"context"
	"encoding/json"
	"os/exec"
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
	return AgentStatusContext(context.Background(), name)
}

// AgentStatusContext is AgentStatus bound to ctx, so a caller that polls
// repeatedly (the Work tab's per-tick poller — admin-spjp.3) can bound each
// query with a timeout instead of risking a wedged herdr hanging forever.
// dispatchOne's one-shot post-settle call goes through AgentStatus above and
// stays unbounded/unchanged.
func AgentStatusContext(ctx context.Context, name string) (string, error) {
	out, err := exec.CommandContext(ctx, "herdr", "agent", "get", name).Output()
	if err != nil {
		return "", err
	}
	var resp struct {
		Result struct {
			Agent struct {
				Status string `json:"agent_status"`
			} `json:"agent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", err
	}
	return resp.Result.Agent.Status, nil
}
