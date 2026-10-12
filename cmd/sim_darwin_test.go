//go:build darwin

package cmd

import (
	"strings"
	"testing"
)

// An .app with no readable Info.plist fails before any simulator is touched.
func TestSimLaunchRefusesAppWithoutBundleID(t *testing.T) {
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	app := t.TempDir() + "/Empty.app"
	rootCmd.SetArgs([]string{"sim", "launch", app})
	err := rootCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "CFBundleIdentifier") {
		t.Fatalf("admin sim launch %s: err = %v, want a CFBundleIdentifier error", app, err)
	}
}
