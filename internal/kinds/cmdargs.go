package kinds

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// CommandArgs is a manifest command's argv after the tool has taken the words
// it owns. A command's arguments belong to its steps, with three exceptions
// that the tool reads itself, anywhere before the first bare "--":
//
//   - "-h" / "--help" print the command's help (CommandHelp) and run nothing:
//     no preflight, no guard, no step, no log file.
//   - "--dry-run" runs only the actions that declare `dry_run = true` — the
//     ones that implement a preview of their own — and prints every other
//     action instead of running it. The word stays in Args, since those
//     actions read it.
//   - "--" ends the tool's words. It is removed, and everything after it
//     reaches the steps unchanged, so `admin x -- --help` hands --help to the
//     program x wraps.
//
// The tool reads these words because a step cannot be trusted to: a
// kind="shell" step ignores its arguments entirely, so a --help or --dry-run
// handled by the second step of a command arrives after the first step has
// already done the real work.
type CommandArgs struct {
	Help   bool
	DryRun bool
	Args   []string
}

// ParseCommandArgs splits args into the tool's words and the steps' words —
// see CommandArgs.
func ParseCommandArgs(args []string) CommandArgs {
	var ca CommandArgs
	out := make([]string, 0, len(args))
	for i, a := range args {
		if a == "--" {
			out = append(out, args[i+1:]...)
			break
		}
		switch a {
		case "-h", "--help":
			ca.Help = true
			continue
		case "--dry-run":
			ca.DryRun = true
		}
		out = append(out, a)
	}
	ca.Args = out
	return ca
}

// dryRun is true while a command invoked with --dry-run runs. RunAction reads
// it for every action, which is what reaches a step, a multi-target's
// delegated target and a nested command alike.
var dryRun atomic.Bool

// DryRunKey is the action key an action sets when it implements --dry-run
// itself (reads the flag from its arguments and changes nothing).
const DryRunKey = "dry_run"

// dryRunCapable reports whether action declares `dry_run = true`.
func dryRunCapable(action manifest.Action) bool {
	v, _ := actionBool(action, DryRunKey)
	return v
}

// printDryRunSkip is what a dry run prints in place of an action it does not
// run: the action, its kind, and the command it would have run when it has
// one. Plain stdout, not the host logger, so a dry run that runs nothing
// writes no log file.
func printDryRunSkip(label, kind, run string) {
	line := fmt.Sprintf("dry run: not running %s (kind %s; it does not declare %s = true)", label, kind, DryRunKey)
	if run = strings.TrimSpace(run); run != "" {
		line += "\n  would run: " + run
	}
	fmt.Println(line)
}

// CommandHelp renders a command's help from the manifest: its description,
// each step with what it does, the targets of every multi-target step, and
// the words the tool reserves. Nothing is resolved or run to build it.
func CommandHelp(m *manifest.Manifest, name string) (string, error) {
	cmd, ok := resolveCommand(m, name)
	if !ok {
		return "", fmt.Errorf("unknown command %q", name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Usage: admin %s [arguments...]\n", name)
	if cmd.Desc != "" {
		fmt.Fprintf(&b, "\n%s\n", cmd.Desc)
	}

	width := 0
	for _, s := range cmd.Steps {
		width = max(width, len(s))
	}
	b.WriteString("\nSteps, in order (each receives the arguments):\n")
	for _, step := range cmd.Steps {
		action, ok := m.Actions[step]
		if !ok {
			fmt.Fprintf(&b, "  %-*s  (not declared in [actions])\n", width, step)
			continue
		}
		kind := actionKind(action)
		line := fmt.Sprintf("  %-*s  %s", width, step, kind)
		if d := actionSummary(action); d != "" {
			line += " — " + d
		}
		if kind != "multi-target" && dryRunCapable(action) {
			line += " [previews under --dry-run]"
		}
		b.WriteString(line + "\n")
		if kind == "multi-target" {
			keys := TargetKeys(action)
			tw := 0
			for _, k := range keys {
				tw = max(tw, len(k))
			}
			for _, k := range keys {
				fmt.Fprintf(&b, "      %-*s  %s\n", tw, k, TargetDescription(m, action, k))
			}
		}
	}

	b.WriteString(`
Reserved arguments (read by admin, before a bare --):
  -h, --help   print this help and run nothing
  --dry-run    run only the actions that declare dry_run = true; print the rest
  --           pass everything after it to the steps unchanged
`)
	return b.String(), nil
}

// actionKind is action's kind with the "shell" default RunAction applies.
func actionKind(action manifest.Action) string {
	if k, _ := actionString(action, "kind"); k != "" {
		return k
	}
	return "shell"
}

// actionSummary is one line saying what an action does: its desc, else the
// first line of its run string when that is a shell command.
func actionSummary(action manifest.Action) string {
	if d, ok := actionString(action, "desc"); ok && d != "" {
		return firstLine(d)
	}
	switch actionKind(action) {
	case "shell", "shell-passthrough":
		if r, ok := actionString(action, "run"); ok {
			return firstLine(r)
		}
	}
	return ""
}

// ValidateDryRun rejects a `dry_run` that is not a boolean, and one on a
// multi-target action: a multi-target only routes, so the key belongs on the
// action a target delegates to. admin check's job, beside ValidateNoComplete.
func ValidateDryRun(actions map[string]manifest.Action) error {
	names := make([]string, 0, len(actions))
	for n := range actions {
		names = append(names, n)
	}
	sort.Strings(names)

	var problems []string
	for _, name := range names {
		action := actions[name]
		v, present := action[DryRunKey]
		if !present {
			continue
		}
		if _, ok := v.(bool); !ok {
			problems = append(problems, fmt.Sprintf(
				"action %q: %s must be a boolean (got %#v)", name, DryRunKey, v))
			continue
		}
		if actionKind(action) == "multi-target" {
			problems = append(problems, fmt.Sprintf(
				"action %q: %s has no effect on a multi-target action — declare it on the action a target delegates to", name, DryRunKey))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(problems, "\n"))
}
