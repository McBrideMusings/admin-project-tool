package populate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// reflink clones src to dst file by file with the FICLONE ioctl, which shares
// blocks on btrfs and on XFS formatted with reflink=1. Linux has no
// whole-directory clone, so the tree is walked and each file cloned.
func reflink(src, dst string) error {
	return copyTree(src, dst, ficlone)
}

func ficlone(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if err := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); err != nil {
		out.Close()
		return fmt.Errorf("FICLONE: %w", err)
	}
	return out.Close()
}

// reflinkUnsupported reports whether err means the filesystem cannot clone
// (ext4, tmpfs, XFS without reflink) or src and dst are on different
// filesystems. Older kernels answer an unknown ioctl with ENOTTY or EINVAL
// rather than EOPNOTSUPP.
func reflinkUnsupported(err error) bool {
	return errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EXDEV) ||
		errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EINVAL)
}
