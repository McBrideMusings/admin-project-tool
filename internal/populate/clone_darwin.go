package populate

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// reflink clones src to dst with clonefile(2), which on APFS copies a whole
// directory tree in one call, sharing every file's blocks with the source.
// CLONE_NOFOLLOW clones a symlink at src as a symlink, as copyTree does.
func reflink(src, dst string) error {
	if err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW); err != nil {
		return fmt.Errorf("clonefile: %w", err)
	}
	return nil
}

// reflinkUnsupported reports whether err means the filesystem cannot clone
// (not APFS) or src and dst are on different volumes, as opposed to a failure
// a full copy would hit too.
func reflinkUnsupported(err error) bool {
	return errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EXDEV)
}
