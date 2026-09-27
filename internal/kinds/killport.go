package kinds

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/ports"
)

// parsePorts converts every string in values to an int.
func parsePorts(values []string) ([]int, error) {
	out := make([]int, 0, len(values))
	for _, v := range values {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("kill-port: invalid port number: %v", err)
		}
		out = append(out, n)
	}
	return out, nil
}

// makeKillPort is kind="kill-port"'s factory. `run` is a space-separated
// list of default ports, validated at build time; a Runner invocation may
// override them with its own argv, or (with no default configured) must
// supply at least one port. Each port is cleared via
// ports.KillPortInteractive, which prints who's holding it, prompts on a
// TTY (force-kills otherwise), and escalates SIGTERM to SIGKILL — the
// acceptance criteria's "identifies the processes holding a port, kills
// them in escalating passes, and reports what it killed" made concrete.
func makeKillPort(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	raw, _ := actionString(action, "run")
	raw = strings.TrimSpace(raw)
	var defaultPorts []int
	if raw != "" {
		fields := strings.Fields(raw)
		for _, f := range fields {
			if _, err := strconv.Atoi(f); err != nil {
				return nil, fmt.Errorf("action %q: kind=\"kill-port\" run must be port numbers, got %q", name, f)
			}
		}
		parsed, err := parsePorts(fields)
		if err != nil {
			return nil, fmt.Errorf("action %q: %w", name, err)
		}
		defaultPorts = parsed
	}

	return func(args []string) error {
		var toKill []int
		switch {
		case len(defaultPorts) > 0 && len(args) > 0:
			parsed, err := parsePorts(args)
			if err != nil {
				return fmt.Errorf("action %q: %w", name, err)
			}
			toKill = parsed
		case len(defaultPorts) > 0:
			toKill = defaultPorts
		case len(args) > 0:
			parsed, err := parsePorts(args)
			if err != nil {
				return fmt.Errorf("action %q: %w", name, err)
			}
			toKill = parsed
		default:
			return fmt.Errorf("action %q: kill-port: port argument required", name)
		}

		for _, port := range toKill {
			if !ports.KillPortInteractive(port) {
				return fmt.Errorf("action %q: failed to clear port %d", name, port)
			}
		}
		return nil
	}, nil
}
