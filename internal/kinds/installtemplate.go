package kinds

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/McBrideMusings/admin-project-tool/internal/dockerrun"
	"github.com/McBrideMusings/admin-project-tool/internal/manifest"
	"github.com/McBrideMusings/admin-project-tool/internal/remotehost"
)

// unraidTemplatesDir is where Unraid's Docker GUI scans for user-defined
// container templates — files matching "my-*.xml" there show up under the
// "Add Container" picker on the host's webui.
const unraidTemplatesDir = "/boot/config/plugins/dockerMan/templates-user"

// slugFromAppImage strips a registry prefix and ":tag" from the APP_IMAGE
// env var to get a bare slug — "ghcr.io/me/my-app:latest" → "my-app".
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

// templateRefRe matches a ${NAME} or ${NAME:-default} reference and captures
// the name and whether a default was given.
var templateRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-)?`)

// renderTemplate expands every ${VAR} / ${VAR:-default} reference in text from
// the process environment, so the tracked template carries placeholders and
// the copy installed on the Unraid host carries real values. A reference with
// no value and no default is an error naming each such variable; a literal
// ${VAR} must never reach the host, where Docker rejects it.
func renderTemplate(text string) (string, error) {
	out, ok := dockerrun.ExpandEnvRefs(text, nil)
	if ok {
		return out, nil
	}
	var missing []string
	seen := map[string]bool{}
	for _, m := range templateRefRe.FindAllStringSubmatch(text, -1) {
		name, hasDefault := m[1], m[2] != ""
		if _, set := os.LookupEnv(name); !set && !hasDefault && !seen[name] {
			seen[name] = true
			missing = append(missing, name)
		}
	}
	return "", fmt.Errorf("unset variables: %s", strings.Join(missing, ", "))
}

// makeInstallTemplate is kind="install-template"'s factory. Reads the
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

		raw, err := os.ReadFile(src)
		if err != nil {
			h.Err("install-template: " + err.Error())
			return fmt.Errorf("action %q: %w", name, err)
		}
		rendered, err := renderTemplate(string(raw))
		if err != nil {
			h.Err("install-template: " + src + ": " + err.Error())
			return fmt.Errorf("action %q: %s: %w", name, src, err)
		}
		tmp, err := os.CreateTemp("", "my-"+templateName+"-*.xml")
		if err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		defer os.Remove(tmp.Name())
		if _, err := tmp.WriteString(rendered); err != nil {
			tmp.Close()
			return fmt.Errorf("action %q: %w", name, err)
		}
		if err := tmp.Close(); err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}

		dst := unraidTemplatesDir + "/my-" + templateName + ".xml"
		fullHost := user + "@" + host
		h.Info("Installing template: " + src + " → " + fullHost + ":" + dst)
		if err := remotehost.ScpTo(h, tmp.Name(), fullHost, dst); err != nil {
			return fmt.Errorf("action %q: %w", name, err)
		}
		h.Ok("Template installed. Open the Unraid Docker GUI → Add Container → Template = my-" + templateName)
		return nil
	}, nil
}
