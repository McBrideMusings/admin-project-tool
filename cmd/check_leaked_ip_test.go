package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// writeDeployFile writes content into <root>/<rel>, creating parent
// directories as needed, and returns the absolute path written.
func writeDeployFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// deployManifest builds a *manifest.Manifest rooted at root declaring
// [deploy].src = "deploy" (plus exclude, when given) and no install_template
// table — the shape checkLeakedIPs reads via kinds.ArchetypeConfigTable.
func deployManifest(root string, exclude []string) *manifest.Manifest {
	deploy := map[string]any{"src": "deploy", "dest": "/opt/app"}
	if exclude != nil {
		excludeAny := make([]any, len(exclude))
		for i, e := range exclude {
			excludeAny[i] = e
		}
		deploy["exclude"] = excludeAny
	}
	return &manifest.Manifest{
		Root:     root,
		Settings: map[string]map[string]any{"deploy": deploy},
	}
}

// TestCheckLeakedIPsFailsOnRFC1918InDeploySrc is the ticket's first
// acceptance criterion: a literal RFC1918 address under [deploy].src fails
// with the file and line named.
func TestCheckLeakedIPsFailsOnRFC1918InDeploySrc(t *testing.T) {
	root := t.TempDir()
	path := writeDeployFile(t, root, "deploy/config.env", "HOST=example.com\nADDR=192.168.1.50\n")
	m := deployManifest(root, nil)

	err := checkLeakedIPs(m)
	if err == nil {
		t.Fatal("checkLeakedIPs = nil, want an error naming the leaked address")
	}
	if !strings.Contains(err.Error(), path+":2:") {
		t.Fatalf("error %q does not name %s:2", err.Error(), path)
	}
	if !strings.Contains(err.Error(), "192.168.1.50") {
		t.Fatalf("error %q does not name the leaked address", err.Error())
	}
}

// TestCheckLeakedIPsRespectsExclude proves a file matched by
// [deploy].exclude is never scanned, even though it carries a literal
// RFC1918 address.
func TestCheckLeakedIPsRespectsExclude(t *testing.T) {
	root := t.TempDir()
	writeDeployFile(t, root, "deploy/vendor/config.env", "ADDR=10.0.0.5\n")
	m := deployManifest(root, []string{"vendor/**", "vendor"})

	if err := checkLeakedIPs(m); err != nil {
		t.Fatalf("checkLeakedIPs = %v, want nil (excluded path)", err)
	}
}

// TestCheckLeakedIPsEnvRefOnlyPasses proves a deploy file whose only address
// reference is a ${VAR} placeholder — no literal fallback — passes.
func TestCheckLeakedIPsEnvRefOnlyPasses(t *testing.T) {
	root := t.TempDir()
	writeDeployFile(t, root, "deploy/config.env", "ADDR=${LAN_IP}\n")
	m := deployManifest(root, nil)

	if err := checkLeakedIPs(m); err != nil {
		t.Fatalf("checkLeakedIPs = %v, want nil (no literal address)", err)
	}
}

// TestCheckLeakedIPsSkipsBinaryFile proves a binary file containing
// IP-shaped bytes never triggers a failure.
func TestCheckLeakedIPsSkipsBinaryFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "deploy", "app.bin")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// "192.168.1.1" as bytes, with an embedded null byte so it sniffs binary.
	data := append([]byte("192.168.1.1"), 0x00, 0x01, 0x02)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	m := deployManifest(root, nil)

	if err := checkLeakedIPs(m); err != nil {
		t.Fatalf("checkLeakedIPs = %v, want nil (binary file)", err)
	}
}

// TestCheckLeakedIPsMissingPathPasses proves a manifest with no deploy or
// install_template files on disk yet passes — nothing to scan is not this
// check's job to flag.
func TestCheckLeakedIPsMissingPathPasses(t *testing.T) {
	root := t.TempDir()
	m := deployManifest(root, nil) // "deploy" dir never created

	if err := checkLeakedIPs(m); err != nil {
		t.Fatalf("checkLeakedIPs = %v, want nil (nothing built yet)", err)
	}
}

// TestCheckLeakedIPsFailsOnCGNAT proves a literal 100.64.0.0/10 address
// fails alongside the RFC1918 ranges.
func TestCheckLeakedIPsFailsOnCGNAT(t *testing.T) {
	root := t.TempDir()
	writeDeployFile(t, root, "deploy/config.env", "ADDR=100.64.5.5\n")
	m := deployManifest(root, nil)

	err := checkLeakedIPs(m)
	if err == nil {
		t.Fatal("checkLeakedIPs = nil, want an error naming the CGNAT address")
	}
	if !strings.Contains(err.Error(), "100.64.5.5") || !strings.Contains(err.Error(), "CGNAT") {
		t.Fatalf("error %q does not name the CGNAT address/range", err.Error())
	}
}

// TestCheckLeakedIPsBoundaryOutsideRFC1918Passes proves exact CIDR
// arithmetic, not a prefix heuristic: 172.15.x.x and 172.32.x.x sit just
// outside the RFC1918 172.16.0.0/12 block and must pass.
func TestCheckLeakedIPsBoundaryOutsideRFC1918Passes(t *testing.T) {
	root := t.TempDir()
	writeDeployFile(t, root, "deploy/config.env", "A=172.15.9.9\nB=172.32.9.9\n")
	m := deployManifest(root, nil)

	if err := checkLeakedIPs(m); err != nil {
		t.Fatalf("checkLeakedIPs = %v, want nil (outside RFC1918 172.16.0.0/12)", err)
	}
}

// TestCheckLeakedIPsInstallTemplateSrc proves the [install_template].src
// file is scanned the same way as [deploy].src.
func TestCheckLeakedIPsInstallTemplateSrc(t *testing.T) {
	root := t.TempDir()
	path := writeDeployFile(t, root, "deploy/unraid-template.xml", "<Config>10.1.2.3</Config>\n")
	m := &manifest.Manifest{Root: root}

	err := checkLeakedIPs(m)
	if err == nil {
		t.Fatal("checkLeakedIPs = nil, want an error naming the leaked address")
	}
	if !strings.Contains(err.Error(), path+":1:") {
		t.Fatalf("error %q does not name %s:1", err.Error(), path)
	}
}

// TestCheckLeakedIPsNoDoubleReportWhenInstallTemplateInsideDeploySrc is the
// admin-cxdv.2.1 round-2 regression: a project that declares [deploy].src =
// "deploy" and no [install_template] table has its default install-template
// path ("deploy/unraid-template.xml") already covered by the [deploy].src
// walk. A leak there must be reported exactly once, not twice.
func TestCheckLeakedIPsNoDoubleReportWhenInstallTemplateInsideDeploySrc(t *testing.T) {
	root := t.TempDir()
	path := writeDeployFile(t, root, "deploy/unraid-template.xml", "<Config>10.1.2.3</Config>\n")
	m := deployManifest(root, nil)

	err := checkLeakedIPs(m)
	if err == nil {
		t.Fatal("checkLeakedIPs = nil, want an error naming the leaked address")
	}
	want := path + ":1:"
	if got := strings.Count(err.Error(), want); got != 1 {
		t.Fatalf("error %q contains %q %d times, want exactly 1", err.Error(), want, got)
	}
}
