//go:build !darwin && !linux

package populate

import "errors"

// reflink has no copy-on-write call to make on this platform, so every clone
// is a full copy.
func reflink(src, dst string) error {
	return errors.ErrUnsupported
}

func reflinkUnsupported(err error) bool {
	return errors.Is(err, errors.ErrUnsupported)
}
