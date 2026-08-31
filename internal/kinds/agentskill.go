package kinds

import (
	"fmt"

	"github.com/McBrideMusings/admin-project-tool/internal/agentskill"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// makeAgentSkill is kind="agent-skill"'s factory: install a project's
// agent-facing SKILL.md (plus reference/*.md) into ~/.claude/skills/<name> and
// ~/.codex/skills/<name>, so an agent working in the project can discover its
// CLI.
//
//	[actions.agent-skill]
//	kind = "agent-skill"
//	run  = "agent-skill"        # repo-relative source dir holding SKILL.md
//	skill_name = "my-tool-ctl"  # defaults to the source dir's own name
//
// Refuses to overwrite a skill it did not write: every installed file carries
// a marker line, and a target that exists without it is left alone.
func makeAgentSkill(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	srcDir, ok := actionString(action, "run")
	if !ok || srcDir == "" {
		return nil, fmt.Errorf(
			`action %q: kind="agent-skill" requires 'run' (the dir holding SKILL.md)`, name)
	}
	skillName, _ := actionString(action, "skill_name")
	explicit, _ := actionStringList(action, "targets")

	return func(args []string) error {
		h := newHost()
		src := manifest.ResolveComputed(srcDir)

		n := skillName
		if n == "" {
			n = defaultSkillName(src)
		}
		dests := explicit
		if len(dests) == 0 {
			dests = agentskill.DefaultTargets(manifest.ResolveComputed(n))
		}

		if _, _, err := agentskill.Install(src, dests, h); err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		return nil
	}, nil
}

// defaultSkillName is the source dir's own basename — `run = "agent-skill"`
// with no skill_name installs a skill called "agent-skill".
func defaultSkillName(src string) string {
	for i := len(src) - 1; i >= 0; i-- {
		if src[i] == '/' {
			return src[i+1:]
		}
	}
	return src
}
