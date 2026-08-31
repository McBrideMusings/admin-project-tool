//go:build windows

package keybindings

import (
	"errors"

	"github.com/McBrideMusings/admin-project-tool/internal/validate"
)

// errUnsupported mirrors internal/reload's own Windows stub: process-group
// signals (Setpgid, SIGINT/SIGTERM to a whole group) don't exist on
// Windows, and this package's subprocess loop depends on them exactly like
// internal/reload.RunWithReload does.
var errUnsupported = errors.New("keybindings: interactive-shell/bridge-listen is not supported on Windows yet")

// Run always fails on Windows. keys is accepted (and ignored) so callers
// compile unchanged.
func Run(cmdline string, keys map[string]validate.KeyBinding, opts Options) (int, error) {
	if opts.Warn != nil {
		opts.Warn(errUnsupported.Error())
	}
	return 1, errUnsupported
}
