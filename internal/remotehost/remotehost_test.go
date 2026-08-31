package remotehost

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIsLocalHostRecognizesLoopback proves the bare loopback names (with or
// without a "user@" prefix) are always local, with no env var needed.
func TestIsLocalHostRecognizesLoopback(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "root@localhost", "root@127.0.0.1"} {
		if !IsLocalHost(host) {
			t.Errorf("IsLocalHost(%q) = false, want true", host)
		}
	}
}

// TestIsLocalHostADMIN_LOCALOverride proves ADMIN_LOCAL=true forces every
// host to be treated as local, even a name that would not otherwise resolve
// to this machine at all.
//
// MUTATION EVIDENCE: remove the ADMIN_LOCAL check from IsLocalHost entirely
// and this test fails: "definitely-not-a-real-host.invalid" would fail DNS
// resolution and IsLocalHost would return false — verified by hand-removing
// the switch statement, re-running (got IsLocalHost() = false, want true),
// then reverting.
func TestIsLocalHostADMIN_LOCALOverride(t *testing.T) {
	t.Setenv("ADMIN_LOCAL", "true")
	if !IsLocalHost("definitely-not-a-real-host.invalid") {
		t.Error("IsLocalHost() = false, want true with ADMIN_LOCAL=true")
	}
}

// TestIsLocalHostRejectsUnresolvableRemote proves an unresolvable remote
// name (no ADMIN_LOCAL override) is NOT treated as local.
func TestIsLocalHostRejectsUnresolvableRemote(t *testing.T) {
	t.Setenv("ADMIN_LOCAL", "")
	if IsLocalHost("definitely-not-a-real-host.invalid") {
		t.Error("IsLocalHost() = true, want false for an unresolvable host with no ADMIN_LOCAL override")
	}
}

// fakeReporter records Info/Ok/Err calls for assertions.
type fakeReporter struct {
	infos, oks, errs []string
}

func (f *fakeReporter) Info(msg string) { f.infos = append(f.infos, msg) }
func (f *fakeReporter) Ok(msg string)   { f.oks = append(f.oks, msg) }
func (f *fakeReporter) Err(msg string)  { f.errs = append(f.errs, msg) }

// TestScpToLocalCopiesFile proves the local-host branch of ScpTo actually
// copies the file's bytes to dest, creating dest's parent directory.
func TestScpToLocalCopiesFile(t *testing.T) {
	t.Setenv("ADMIN_LOCAL", "true")
	dir := t.TempDir()
	src := filepath.Join(dir, "template.xml")
	if err := os.WriteFile(src, []byte("<Container/>"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	dest := filepath.Join(dir, "nested", "my-app.xml")

	r := &fakeReporter{}
	if err := ScpTo(r, src, "anyhost", dest); err != nil {
		t.Fatalf("ScpTo: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile(dest): %v", err)
	}
	if string(got) != "<Container/>" {
		t.Errorf("dest content = %q, want %q", got, "<Container/>")
	}
	if len(r.oks) != 1 {
		t.Errorf("Ok() calls = %v, want exactly one", r.oks)
	}
}

// TestScpToLocalReportsMissingSource proves a missing source file surfaces
// as an error through both the return value and Err().
func TestScpToLocalReportsMissingSource(t *testing.T) {
	t.Setenv("ADMIN_LOCAL", "true")
	dir := t.TempDir()
	r := &fakeReporter{}
	err := ScpTo(r, filepath.Join(dir, "missing.xml"), "anyhost", filepath.Join(dir, "dest.xml"))
	if err == nil {
		t.Fatal("ScpTo with a missing source should error")
	}
	if len(r.errs) != 1 {
		t.Errorf("Err() calls = %v, want exactly one", r.errs)
	}
}
