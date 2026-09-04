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

// newCmd is admin-3rgp.55: the Go port of admin-run's cmd_new
// (admin-run:196-223), registered as a real cobra command so dispatchArgs'
// loop over rootCmd.Commands() (root.go's dispatchArgs, ~line 136) finds
// "new" and never falls through to branch shorthand — reservedToolVerbs
// (cmd/manifest.go) has always reserved the word; only the cobra command was
// missing.
//
// It scaffolds a starter admin.toml from internal/detect's file-layout
// heuristics (the Go port of admin_lib/detectors.py, previously wired to
// nothing but its own tests) and finishes by invoking runCheck on the
// freshly written file, mirroring admin-run:223's `return cmd_check(project_dir)`.
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
		PythonMin:   "3.11",
		Archetypes:  result.Archetypes,
		Modules:     nil,
		Urls:        result.URLs,
	}
	if err := os.WriteFile(manifestPath, []byte(manifest.Dump(starter)), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", manifestPath)

	if err := ensureTmpGitignore(dir); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "run 'admin <command>' from anywhere in this project (the tool is on PATH).")

	// Validate the starter manifest resolves before declaring success —
	// mirrors admin-run:226's `return cmd_check(project_dir)`.
	return runCheck(cmd, []string{dir})
}

// starterProjectName falls back to the target directory's base name,
// title-cased with "-" replaced by " ", when detect.Run proposed none —
// matching admin-run:208's
// `project_dir.resolve().name.replace("-", " ").title()`.
func starterProjectName(dir string) string {
	base := strings.ReplaceAll(filepath.Base(dir), "-", " ")
	words := strings.Fields(base)
	for i, w := range words {
		r := []rune(w)
		if len(r) > 0 {
			r[0] = unicode.ToUpper(r[0])
		}
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

// ensureTmpGitignore is the Go port of admin-run's _ensure_tmp_gitignore
// (admin-run:188-193): a fresh starter manifest needs tmp/.gitignore so
// tmp/'s own contents (logs, scratch) never get committed by accident.
func ensureTmpGitignore(dir string) error {
	tmp := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	gi := filepath.Join(tmp, ".gitignore")
	if _, err := os.Stat(gi); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(gi, []byte("*\n!.gitignore\n"), 0o644)
}
