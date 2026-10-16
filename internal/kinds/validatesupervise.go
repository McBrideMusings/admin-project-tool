package kinds

import (
	"fmt"
	"sort"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// ValidateSupervise rejects a non-bool `supervise` on an action's own top
// level — TOML has no truthy strings, so `supervise = "false"` would
// otherwise be read as the default (true) and the daemon the author meant to
// keep would be swept (admin check's job, beside ValidateNoComplete). Reports
// every offending action, not just the first.
func ValidateSupervise(actions map[string]manifest.Action) error {
	names := make([]string, 0, len(actions))
	for n := range actions {
		names = append(names, n)
	}
	sort.Strings(names)

	var problems []string
	for _, name := range names {
		v, present := actions[name]["supervise"]
		if !present {
			continue
		}
		if _, ok := v.(bool); !ok {
			problems = append(problems, fmt.Sprintf(
				"action %q: supervise must be a boolean (got %#v) — false lets a daemon the action starts outlive admin", name, v))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(problems, "\n"))
}
