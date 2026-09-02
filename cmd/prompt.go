package cmd

import "github.com/McBrideMusings/admin-project-tool/internal/prompt"

// confirm delegates to internal/prompt.Confirm — moved there (admin-spjp.2)
// so internal/kinds' bare-CLI workflow dispatch can share the same seam;
// internal/kinds cannot import this package, since cmd already imports
// internal/kinds. cmd/worktree.go and cmd/prune.go's call sites are
// unaffected by the move.
func confirm(msg string) bool {
	return prompt.Confirm(msg)
}
