package populate

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// reflinkFn is the platform's copy-on-write copy of src to a dst that does not
// exist yet. A test swaps it to force the full-copy path on a filesystem that
// would otherwise share blocks.
var reflinkFn = reflink

// clonePath copies src (a file or a directory) to dst copy-on-write, so the
// worktree gets its own copy that shares disk blocks with the primary's until
// either side writes. When the filesystem cannot share blocks it makes a full
// copy instead and returns why, so the caller can say a slow populate was a
// full copy. dst must not exist: clonePath removes a half-made dst on the way
// to the full copy, and must never remove anything it did not create.
func clonePath(src, dst string) (fullCopy string, err error) {
	if _, err := os.Lstat(dst); err == nil {
		return "", fmt.Errorf("clone %s: destination already exists", dst)
	}
	refErr := reflinkFn(src, dst)
	if refErr == nil {
		return "", nil
	}
	if err := os.RemoveAll(dst); err != nil {
		return "", err
	}
	if !reflinkUnsupported(refErr) {
		return "", refErr
	}
	if err := copyTree(src, dst, copyFile); err != nil {
		return "", err
	}
	return refErr.Error(), nil
}

// copyTree recreates src at dst: directories with their permissions, symlinks
// as symlinks, and each regular file through copyOne. src may be a single file.
//
// Each directory is created owner-writable and gets its real permissions only
// once everything under it is written, deepest first: a read-only directory in
// a vendored cache would otherwise refuse its own children.
func copyTree(src, dst string, copyOne func(src, dst string, mode fs.FileMode) error) error {
	type dirPerm struct {
		path string
		perm fs.FileMode
	}
	var dirs []dirPerm
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			dirs = append(dirs, dirPerm{target, info.Mode().Perm()})
			return os.MkdirAll(target, 0o700)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.Type().IsRegular():
			return copyOne(path, target, info.Mode().Perm())
		default:
			return fmt.Errorf("clone %s: not a regular file, directory or symlink", path)
		}
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i].path, dirs[i].perm); err != nil {
			return err
		}
	}
	return nil
}

// copyFile writes a byte-for-byte copy of src to a new file at dst.
func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
