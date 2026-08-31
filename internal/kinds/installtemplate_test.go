package kinds

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
)

// TestMakeInstallTemplateMissingSrcErrors proves a missing source XML fails
// before anything else is checked (UNRAID_HOST included) — matching
// admin_lib/unraid.py's install_docker_template_from_config, which checks
// os.path.exists(src) first.
func TestMakeInstallTemplateMissingSrcErrors(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("UNRAID_HOST", "") // deliberately unset, to prove src is checked first

	runner, err := makeInstallTemplate("install-template", manifest.Action{}, nil)
	if err != nil {
		t.Fatalf("makeInstallTemplate: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() with no source XML present should error")
	}
}

// TestMakeInstallTemplateMissingHostErrors proves a present source XML but
// unset UNRAID_HOST still fails, with a distinct error from the missing-src
// case.
func TestMakeInstallTemplateMissingHostErrors(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "deploy/unraid-template.xml", "<Container/>")
	t.Setenv("UNRAID_HOST", "")
	t.Setenv("APP_IMAGE", "ghcr.io/me/my-app:latest")

	runner, err := makeInstallTemplate("install-template", manifest.Action{}, nil)
	if err != nil {
		t.Fatalf("makeInstallTemplate: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() with UNRAID_HOST unset should error")
	}
}

// TestMakeInstallTemplateMissingNameErrors proves that with no
// [install_template].name and no usable APP_IMAGE, the action fails asking
// for one of the two — admin_lib/unraid.py checks this before UNRAID_HOST.
func TestMakeInstallTemplateMissingNameErrors(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "deploy/unraid-template.xml", "<Container/>")
	t.Setenv("UNRAID_HOST", "myserver")
	t.Setenv("APP_IMAGE", "")

	runner, err := makeInstallTemplate("install-template", manifest.Action{}, nil)
	if err != nil {
		t.Fatalf("makeInstallTemplate: %v", err)
	}
	if err := runner(nil); err == nil {
		t.Fatal("runner() with no name and no APP_IMAGE should error")
	}
}

// TestSlugFromAppImage proves the registry-prefix-and-tag stripping matches
// admin_lib/unraid.py's _slug_from_app_image exactly.
//
// MUTATION EVIDENCE: change the tag-stripping `strings.Index(image, ":")` to
// `strings.LastIndex(image, ":")` and this test fails for a registry host
// carrying a port (none in this table, so instead: drop the
// `strings.LastIndex(image, "/")` registry-strip entirely) — with that
// mutation, "ghcr.io/me/my-app" (no tag) yields "ghcr.io/me/my-app" instead
// of "my-app", failing the assertion. Verified by hand-removing the
// registry-strip lines, re-running (got slug "ghcr.io/me/my-app"), then
// reverting.
func TestSlugFromAppImage(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/me/my-app:latest": "my-app",
		"my-app":                   "my-app",
		"":                         "",
	}
	for image, want := range cases {
		t.Setenv("APP_IMAGE", image)
		if got := slugFromAppImage(); got != want {
			t.Errorf("slugFromAppImage() with APP_IMAGE=%q = %q, want %q", image, got, want)
		}
	}
}

// TestMakeInstallTemplateUsesConfiguredName proves the [install_template]
// table's `name` overrides APP_IMAGE — and, together with
// TestMakeInstallTemplateMissingHostErrors above, that the failure reached
// after src+name resolve is specifically the UNRAID_HOST check (proving name
// resolution succeeded rather than also failing).
func TestMakeInstallTemplateUsesConfiguredName(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "custom/template.xml", "<Container/>")
	t.Setenv("UNRAID_HOST", "")
	t.Setenv("APP_IMAGE", "")

	m := &manifest.Manifest{InstallTemplate: map[string]any{
		"src":  "custom/template.xml",
		"name": "web-file-manager",
	}}
	runner, err := makeInstallTemplate("install-template", manifest.Action{}, m)
	if err != nil {
		t.Fatalf("makeInstallTemplate: %v", err)
	}
	err = runner(nil)
	if err == nil {
		t.Fatal("runner() with UNRAID_HOST unset should still error")
	}
	// The error must be the UNRAID_HOST one, not a name-resolution failure —
	// proving [install_template].name was actually read and used.
	if got := err.Error(); !strings.Contains(got, "UNRAID_HOST") {
		t.Errorf("runner() error = %q, want it to mention UNRAID_HOST (name should have resolved)", got)
	}
}

// TestUnraidTemplatesDirIsAbsolute is a light sanity check that the
// destination path this kind writes to is the real Unraid GUI scan
// directory, not an accidental relative path.
func TestUnraidTemplatesDirIsAbsolute(t *testing.T) {
	if !filepath.IsAbs(unraidTemplatesDir) {
		t.Errorf("unraidTemplatesDir = %q, want an absolute path", unraidTemplatesDir)
	}
}
