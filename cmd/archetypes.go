package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/archetype"
	"github.com/McBrideMusings/admin-project-tool/internal/git"
)

// archetypesCmd groups the archetype deployment path (admin-3rgp.29): the
// binary itself ships no archetypes (`go install` embeds none — see
// internal/archetype/search.go's embedded() slot), so making one available
// anywhere is always this command's job, never the build's.
var archetypesCmd = &cobra.Command{
	Use:   "archetypes",
	Short: "Deploy or list archetype directories (~/.admin/archetypes/)",
}

var archetypesDeployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Copy this repository's archetypes/ into ~/.admin/archetypes/",
	Long: `Copy every archetype directory (one containing archetype.toml) from
source (default: <repo root>/archetypes) into dest (default:
~/.admin/archetypes/), replacing whatever was there.

This is independent of building the binary: it neither requires nor performs
a 'go install', and an archetype it deploys is visible to every project on
this machine the next time their manifest resolves — no rebuild needed.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		source, _ := cmd.Flags().GetString("source")
		dest, _ := cmd.Flags().GetString("dest")
		return runArchetypesDeploy(cmd.Context(), source, dest)
	},
}

var archetypesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List archetypes visible on the search path",
	Long: `List every archetype name visible anywhere on the search path — the
reserved (currently empty) embedded slot, ~/.admin/archetypes/, then this
repo's own .admin/archetypes/ — later slots winning on a name collision.

Reports "no archetypes available" rather than failing when the search path is
entirely empty, which is the normal state for a freshly 'go install'-ed
binary before 'admin archetypes deploy' has ever run.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runArchetypesList(cmd.Context())
	},
}

func init() {
	archetypesDeployCmd.Flags().String("source", "", "directory of archetype subdirectories to deploy (default: <repo root>/archetypes)")
	archetypesDeployCmd.Flags().String("dest", "", "directory to deploy into (default: ~/.admin/archetypes)")
	archetypesCmd.AddCommand(archetypesDeployCmd, archetypesListCmd)
	rootCmd.AddCommand(archetypesCmd)
}

func runArchetypesDeploy(ctx context.Context, source, dest string) error {
	if source == "" {
		repoRoot, err := git.RepoRoot(ctx)
		if err != nil {
			return fmt.Errorf("no --source given and %w — pass --source explicitly outside a git repo", err)
		}
		source = filepath.Join(repoRoot, "archetypes")
	}
	if dest == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("no --dest given and could not resolve the home directory: %w", err)
		}
		dest = archetype.HomeArchetypesDir(home)
	}

	names, err := archetype.Deploy(source, dest)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Fprintf(os.Stderr, "no archetype directories found in %s — %s is now empty\n", source, dest)
		return nil
	}
	fmt.Fprintf(os.Stderr, "deployed %d archetype(s) to %s:\n", len(names), dest)
	for _, name := range names {
		fmt.Fprintf(os.Stderr, "  %s\n", name)
	}
	return nil
}

func runArchetypesList(ctx context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not resolve the home directory: %w", err)
	}
	// A repo is optional here — listing archetypes is useful even outside one,
	// and repoRoot simply contributes nothing when there isn't one (List()
	// treats a missing search-path directory as empty, never an error).
	repoRoot, _ := git.RepoRoot(ctx)

	sp := archetype.DefaultSearchPath(home, repoRoot)
	found, err := sp.List()
	if err != nil {
		return err
	}
	if len(found) == 0 {
		fmt.Fprintln(os.Stderr, "no archetypes available — run: admin archetypes deploy")
		return nil
	}

	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(os.Stderr, "%s\t(%s)\n", name, found[name].Label)
	}
	return nil
}
