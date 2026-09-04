package kinds

import (
	"fmt"
	"os"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/remotehost"
)

// unraidTemplatesDir is where Unraid's Docker GUI scans for user-defined
// container templates — files matching "my-*.xml" there show up under the
// "Add Container" picker on the host's webui. Mirrors
// admin_lib/unraid.py's _UNRAID_TEMPLATES_DIR.
const unraidTemplatesDir = "/boot/config/plugins/dockerMan/templates-user"

// slugFromAppImage strips a registry prefix and ":tag" from the APP_IMAGE
// env var to get a bare slug — "ghcr.io/me/my-app:latest" → "my-app". The
// Go port of admin_lib/unraid.py's _slug_from_app_image.
func slugFromAppImage() string {
	image := strings.TrimSpace(os.Getenv("APP_IMAGE"))
	if image == "" {
		return ""
	}
	if idx := strings.Index(image, ":"); idx >= 0 {
		image = image[:idx]
	}
	if idx := strings.LastIndex(image, "/"); idx >= 0 {
		image = image[idx+1:]
	}
	return image
}

// InstallTemplateSrc resolves the `[install_template].src` manifest key,
// defaulting to "deploy/unraid-template.xml" when unset — the single source
// of that literal default, shared by makeInstallTemplate and
// cmd/check_leaked_ip.go's scanner so the two can't drift apart.
func InstallTemplateSrc(m *manifest.Manifest) string {
	cfg := archetypeConfigTable(m, "install_template")
	src, _ := cfg["src"].(string)
	if src == "" {
		src = "deploy/unraid-template.xml"
	}
	return src
}

// makeInstallTemplate is kind="install-template"'s factory — the Go port of
// admin_lib/interp.py::_make_install_template, which calls
// admin_lib/unraid.py's install_docker_template_from_config. Reads the
// `[install_template]` manifest table for `src`/`name` (both optional) and
// UNRAID_HOST (required)/UNRAID_USER (default "root") from the environment —
// no host or user literal ever belongs in a tracked manifest.
func makeInstallTemplate(name string, action manifest.Action, m *manifest.Manifest, _ *Surface) (Runner, error) {
	cfg := archetypeConfigTable(m, "install_template")
	src := InstallTemplateSrc(m)
	cfgName, _ := cfg["name"].(string)

	return func(args []string) error {
		h := newHost(m)

		if _, err := os.Stat(src); err != nil {
			msg := "install-template: source XML not found: " + src
			h.Err(msg)
			return fmt.Errorf("action %q: %s", name, msg)
		}

		templateName := strings.TrimSpace(cfgName)
		if templateName == "" {
			templateName = strings.TrimSpace(slugFromAppImage())
		}
		if templateName == "" {
			msg := "install-template: cannot determine template name — set APP_IMAGE or [install_template].name"
			h.Err(msg)
			return fmt.Errorf("action %q: %s", name, msg)
		}

		host := strings.TrimSpace(os.Getenv("UNRAID_HOST"))
		if host == "" {
			msg := "install-template: UNRAID_HOST is not set"
			h.Err(msg)
			return fmt.Errorf("action %q: %s", name, msg)
		}
		user := strings.TrimSpace(os.Getenv("UNRAID_USER"))
		if user == "" {
			user = "root"
		}

		dst := unraidTemplatesDir + "/my-" + templateName + ".xml"
		fullHost := user + "@" + host
		h.Info("Installing template: " + src + " → " + fullHost + ":" + dst)
		if err := remotehost.ScpTo(h, src, fullHost, dst); err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		h.Ok("Template installed. Open the Unraid Docker GUI → Add Container → Template = my-" + templateName)
		return nil
	}, nil
}
