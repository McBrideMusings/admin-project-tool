package kinds

import (
	"fmt"
	"sort"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// ValidateNoComplete rejects a non-bool `no_complete` on an action's own top
// level — TOML has no truthy strings, so `no_complete = "true"` is a typo
// worth reporting rather than silently treating as unset (admin check's job,
// alongside kinds.ValidateDetach, kinds.ValidateWorkflows and
// validate.Terminal). Reports every offending action, not just the first.
//
// Unlike ValidateDetach, this never descends into a multi-target action's
// inline target subtables: `no_complete` suppresses the single
// `h.Ok(name + " complete")` line a flat action prints on success, and a
// multi-target dispatch has no such line of its own to suppress.
func ValidateNoComplete(actions map[string]manifest.Action) error {
	names := make([]string, 0, len(actions))
	for n := range actions {
		names = append(names, n)
	}
	sort.Strings(names)

	var problems []string
	for _, name := range names {
		action := actions[name]
		v, present := action["no_complete"]
		if !present {
			continue
		}
		if _, ok := v.(bool); !ok {
			problems = append(problems, fmt.Sprintf(
				"action %q: no_complete must be a boolean (got %#v)", name, v))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	msg := problems[0]
	for _, p := range problems[1:] {
		msg += "\n" + p
	}
	return fmt.Errorf("%s", msg)
}
