// Package slug derives short names: a branch name from an issue (issue number
// plus a few sanitized title words, none of wtree's tuning knobs), and a
// per-checkout name from a checkout's root directory.
package slug

import (
	"fmt"
	"path/filepath"
	"strings"
)

// FromRoot names a checkout by its root directory: the path below home with
// every "/" turned into "-", or, for a root outside home, the full path with
// its leading "/" dropped. It is the rule ~/.claude's hooks/repo-slug.sh
// applies, so ~/Projects/muxarr is "Projects-muxarr" and its linked worktree
// ~/.worktrees/muxarr is ".worktrees-muxarr" — the directory name alone would
// give both "muxarr". Never empty: a caller interpolates it into a value like
// --defaults-suite=<slug>, where an empty one silently selects the default.
func FromRoot(root, home string) string {
	root = filepath.Clean(root)
	rel := root
	if home != "" {
		if r, ok := strings.CutPrefix(root, filepath.Clean(home)+"/"); ok {
			rel = r
		}
	}
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "root"
	}
	return strings.ReplaceAll(rel, "/", "-")
}

const maxWords = 5

// FromIssue builds a branch name like "42-fix-the-thing" from an issue number
// and title.
func FromIssue(number int, title string) string {
	words := titleWords(title)
	if words == "" {
		return fmt.Sprintf("issue-%d", number)
	}
	return fmt.Sprintf("%d-%s", number, words)
}

// FromBead builds a branch name like "bd-4f2-add-password-reset" from a bead's
// id and title. A bead's id already carries its workspace prefix, so unlike an
// issue number it needs no prefix of its own — and a title that sanitizes down
// to nothing leaves the id standing alone as a perfectly good branch name.
func FromBead(id, title string) string {
	words := titleWords(title)
	if words == "" {
		return id
	}
	return id + "-" + words
}

// IssueLabel names an issue's worker space, like "Issue-42-fix-the-thing".
func IssueLabel(number int, title string) string {
	return withWords(fmt.Sprintf("Issue-%d", number), title)
}

// BeadLabel names a bead's worker space, like "Bead-l37.2-add-password-reset".
// The id loses its workspace prefix ("social-chess-l37.2" → "l37.2"): every
// space under one repo shares that prefix, so it only crowds the sidebar.
func BeadLabel(id, title string) string {
	return withWords("Bead-"+id[strings.LastIndex(id, "-")+1:], title)
}

func withWords(head, title string) string {
	if words := titleWords(title); words != "" {
		return head + "-" + words
	}
	return head
}

// titleWords is the shared "first few sanitized words of a title" part, joined
// with dashes. Empty when the title has no usable characters at all.
func titleWords(title string) string {
	var kept []string
	for w := range strings.FieldsSeq(strings.ToLower(title)) {
		w = sanitize(w)
		if w == "" {
			continue
		}
		kept = append(kept, w)
		if len(kept) >= maxWords {
			break
		}
	}
	return strings.Join(kept, "-")
}

func sanitize(w string) string {
	var b strings.Builder
	for _, r := range w {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		}
	}
	return b.String()
}
