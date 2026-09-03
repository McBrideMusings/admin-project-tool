package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/kinds"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// cmdCmd is the reserved `admin cmd` verb (admin-u7qi.1): with no args it
// lists every command the current project's admin.toml declares in a
// minimal arrow-key-navigable picker (admin-u7qi.4) and runs the highlighted
// one through the exact same kinds.RunCommand path `admin <name>` already
// uses — so a picked command gets identical launcher/detach/reentry behavior
// to a typed name. It is registered on rootCmd in cmd/root.go's init(),
// which is what makes dispatchArgs' loop over rootCmd.Commands() match
// `admin cmd` before ever consulting the manifest for a command literally
// named "cmd" (see reservedCmdVerbs in cmd/manifest.go, which admin check's
// checkReservedCollisions enforces against a manifest declaring the name).
var cmdCmd = &cobra.Command{
	Use:   "cmd",
	Short: "pick this project's admin.toml commands from an arrow-key list and run it inline",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCmdPicker(cmd.Context())
	},
}

// commandDesc looks up name's Desc the way kinds.resolveCommand does
// (custom shadows archetype-provided — admin_lib/archetypes.py::resolve's
// rule) — that resolver is unexported to this package, and the picker only
// needs the description, not the whole dispatch machinery resolveCommand
// carries.
func commandDesc(m *manifest.Manifest, name string) string {
	if c, ok := m.Custom[name]; ok {
		return c.Desc
	}
	if c, ok := m.Overrides[name]; ok {
		return c.Desc
	}
	return ""
}

// cmdPickerModel is a minimal bubbletea model for the `admin cmd` picker: a
// cursor over a fixed list of command names/descs. It holds no I/O of its
// own — Update takes plain tea.Msg values and is exercised directly by
// tests without a live tea.Program — so the picker's key/cursor logic stays
// hermetic under `go test` while runCmdPicker wires the same model into a
// real tea.NewProgram for terminal use.
type cmdPickerModel struct {
	names     []string
	descs     []string
	cursor    int
	chosen    string
	confirmed bool
	cancelled bool
}

func newCmdPickerModel(m *manifest.Manifest, names []string) cmdPickerModel {
	descs := make([]string, len(names))
	for i, name := range names {
		descs[i] = commandDesc(m, name)
	}
	return cmdPickerModel{names: names, descs: descs}
}

func (p cmdPickerModel) Init() tea.Cmd {
	return nil
}

// Update handles the picker's whole interaction surface: up/k and down/j
// move the cursor (clamped, no wraparound), enter confirms the highlighted
// command, and q/esc/ctrl+c cancels — both terminal cases return tea.Quit.
func (p cmdPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return p, nil
	}
	switch keyMsg.String() {
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down", "j":
		if p.cursor < len(p.names)-1 {
			p.cursor++
		}
	case "enter":
		p.confirmed = true
		p.chosen = p.names[p.cursor]
		return p, tea.Quit
	case "q", "esc", "ctrl+c":
		p.cancelled = true
		return p, tea.Quit
	}
	return p, nil
}

// View renders one line per command, marking the highlighted line with
// "> " — no altscreen, no lipgloss styling: the point is a visible cursor
// moving over the list, not a scaled-down copy of the Work tab's rendering.
func (p cmdPickerModel) View() string {
	var b strings.Builder
	for i, name := range p.names {
		marker := "  "
		if i == p.cursor {
			marker = "> "
		}
		if desc := p.descs[i]; desc != "" {
			fmt.Fprintf(&b, "%s%s — %s\n", marker, name, desc)
		} else {
			fmt.Fprintf(&b, "%s%s\n", marker, name)
		}
	}
	b.WriteString("(up/down or j/k to move, enter to run, q/esc to cancel)\n")
	return b.String()
}

// runCmdPicker resolves the project manifest, then drives a cmdPickerModel
// in a live tea.Program over stderr (cmd/'s own output already goes there
// so stdout stays clean for the cd sentinel) letting the operator move a
// cursor with up/down/j/k and pick a command with enter. q/esc/ctrl+c
// cancels without running anything. A confirmed pick runs via
// kinds.RunCommand(m, kinds.Default, name, nil, nil) — the same call
// registerManifestCommand's RunE makes for `admin <name>`.
//
// The ok/err handling mirrors registerManifestCommand: ok is false only
// when there is no admin.toml at all (never paired with a non-nil err, per
// resolveProjectManifest's own contract), so that case is checked first and
// reported plainly; a manifest that exists but fails to parse/resolve is
// reported as invalid rather than silently producing an empty list.
func runCmdPicker(ctx context.Context) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	m, ok, err := resolveProjectManifest(ctx, wd)
	if !ok {
		return fmt.Errorf("no admin.toml in %s or any directory above it, so this project has no admin commands", wd)
	}
	if err != nil {
		return fmt.Errorf("admin.toml is invalid: %w", err)
	}

	names := kinds.CommandNames(m)
	if len(names) == 0 {
		return fmt.Errorf("admin.toml declares no commands")
	}

	model := newCmdPickerModel(m, names)
	prog := tea.NewProgram(model, tea.WithContext(ctx), tea.WithOutput(os.Stderr))
	final, err := prog.Run()
	if err != nil {
		return fmt.Errorf("running command picker: %w", err)
	}

	picked, ok := final.(cmdPickerModel)
	if !ok || picked.cancelled || !picked.confirmed {
		return nil
	}

	return kinds.RunCommand(m, kinds.Default, picked.chosen, nil, nil)
}
