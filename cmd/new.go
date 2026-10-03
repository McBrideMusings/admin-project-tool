package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/McBrideMusings/admin-project-tool/internal/detect"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// newCmd is admin-3rgp.55, registered as a real cobra command so dispatchArgs'
// loop over rootCmd.Commands() (root.go's dispatchArgs, ~line 136) finds
// "new" and never falls through to branch shorthand — reservedToolVerbs
// (cmd/manifest.go) has always reserved the word; only the cobra command was
// missing.
//
// It scaffolds a starter admin.toml from internal/detect's file-layout
// heuristics (previously wired to nothing but its own tests) and finishes by
// invoking runCheck on the freshly written file.
var newCmd = &cobra.Command{
	Use:   "new [DIR]",
	Short: "Detect the project stack and write a starter admin.toml",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runNew(cmd, args)
	},
}

func init() {
	newCmd.Flags().Bool("force", false, "overwrite an existing admin.toml")
	rootCmd.AddCommand(newCmd)
}

func runNew(cmd *cobra.Command, args []string) error {
	// checkTargetDir (cmd/check.go): same [DIR]-or-cwd convention `admin
	// check` uses, so `admin new` and `admin check` never disagree about
	// what "the project" means.
	dir, err := checkTargetDir(args)
	if err != nil {
		return err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	force, _ := cmd.Flags().GetBool("force")

	manifestPath := filepath.Join(dir, manifest.Filename)
	if _, statErr := os.Stat(manifestPath); statErr == nil && !force {
		return fmt.Errorf("%s already exists — pass --force to overwrite", manifestPath)
	}

	fmt.Fprintf(os.Stderr, "detecting project stack in %s\n", dir)
	result := detect.Run(dir)
	fmt.Fprintf(os.Stderr, "detector matched: %s\n", result.Detector)

	projectName := result.ProjectName
	if projectName == "" {
		projectName = starterProjectName(dir)
	}

	starter := manifest.Starter{
		ProjectName: projectName,
		Archetypes:  result.Archetypes,
		Tables:      result.Tables,
	}
	if err := os.WriteFile(manifestPath, []byte(manifest.Dump(starter)), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", manifestPath)

	if err := ensureTmpGitignore(dir); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "run 'admin <command>' from anywhere in this project (the tool is on PATH).")

	// Validate the starter manifest resolves before declaring success.
	return runCheck(cmd, []string{dir})
}

// starterProjectName falls back to the target directory's base name,
// title-cased with "-" replaced by " ", when detect.Run proposed none.
func starterProjectName(dir string) string {
	// A letter that follows a non-letter is upper-cased, every other letter
	// is lower-cased. "-" becomes a space first, so empty words survive
	// ("a--b" -> "A  B").
	prevLetter := false
	return strings.Map(func(r rune) rune {
		if r == '-' {
			r = ' '
		}
		isLetter := unicode.IsLetter(r)
		defer func() { prevLetter = isLetter }()
		switch {
		case !isLetter:
			return r
		case prevLetter:
			return unicode.ToLower(r)
		default:
			return unicode.ToUpper(r)
		}
	}, filepath.Base(dir))
}

// ensureTmpGitignore ensures a fresh starter manifest's tmp/.gitignore
// exists, so tmp/'s own contents (logs, scratch) never get committed by
// accident.
func ensureTmpGitignore(dir string) error {
	tmp := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	gi := filepath.Join(tmp, ".gitignore")
	_, err := os.Stat(gi)
	if err == nil {
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(gi, []byte("*\n!.gitignore\n"), 0o644)
}
