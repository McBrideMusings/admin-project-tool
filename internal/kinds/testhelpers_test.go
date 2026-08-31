package kinds

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile writes content to dir/relPath, creating any needed parent
// directories, and fails the test on error.
func writeFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", full, err)
	}
}

// assertFileExists fails the test unless dir/relPath exists.
func assertFileExists(t *testing.T, dir, relPath string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("expected %s to exist: %v", full, err)
	}
}

// mustReadFile reads dir/relPath, failing the test on error.
func mustReadFile(t *testing.T, dir, relPath string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, relPath))
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", filepath.Join(dir, relPath), err)
	}
	return data
}

// assertFileMissing fails the test if dir/relPath exists.
func assertFileMissing(t *testing.T, dir, relPath string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	if _, err := os.Stat(full); err == nil {
		t.Fatalf("expected %s to NOT exist", full)
	}
}
