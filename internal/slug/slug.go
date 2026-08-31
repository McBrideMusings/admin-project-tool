// Package slug derives a short branch name from an issue. Deliberately simple:
// issue number plus a few sanitized title words. None of wtree's tuning knobs.
package slug

import (
	"fmt"
	"strings"
)

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
