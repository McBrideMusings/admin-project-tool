package service

import (
	"strings"
	"testing"
)

func renderCfg() Config {
	throttle := 10
	return Config{
		Label:       "com.example.svc",
		Program:     "/opt/svc",
		Args:        []string{"-addr", "127.0.0.1:8790"},
		Env:         map[string]string{"B": "two", "A": "one"},
		KeepAlive:   true,
		RunAtLoad:   true,
		Throttle:    &throttle,
		ProcessType: "Background",
		Stdout:      "/tmp/svc.log",
		Stderr:      "/tmp/svc.log",
	}
}

// TestRenderPlistIsDeterministic is the one that matters most. install()
// compares the rendered bytes against what is on disk and only re-registers
// when they differ, because macOS Background Task Management posts an "App
// Background Activity" notification for every newly registered plist. If the
// render varied between runs, every deploy would rewrite, re-register, and
// notify.
func TestRenderPlistIsDeterministic(t *testing.T) {
	c := renderCfg()
	first := string(RenderPlist(c))
	for i := 0; i < 20; i++ {
		if got := string(RenderPlist(c)); got != first {
			t.Fatal("RenderPlist is not byte-stable across calls — map iteration is leaking into the output")
		}
	}
}

func TestRenderPlistContent(t *testing.T) {
	out := string(RenderPlist(renderCfg()))
	for _, want := range []string{
		"<key>Label</key>",
		"<string>com.example.svc</string>",
		"<string>/opt/svc</string>",
		"<string>-addr</string>",
		"<key>KeepAlive</key>",
		"<true/>",
		"<key>ThrottleInterval</key>",
		"<integer>10</integer>",
		"<key>StandardOutPath</key>",
		"<key>ProcessType</key>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plist is missing %s\n%s", want, out)
		}
	}
	if !strings.HasPrefix(out, "<?xml") {
		t.Error("plist must start with the XML declaration")
	}
	// Keys sorted, matching Python plistlib.dumps(sort_keys=True), so a plist
	// written by the old tool and one written here compare equal and an upgrade
	// does not read as a change.
	if strings.Index(out, "<key>KeepAlive</key>") > strings.Index(out, "<key>Label</key>") {
		t.Error("top-level keys are not sorted")
	}
	if strings.Index(out, "<key>A</key>") > strings.Index(out, "<key>B</key>") {
		t.Error("environment keys are not sorted")
	}
}

// PATH is the one launchd hands an agent as /usr/bin:/bin:/usr/sbin:/sbin and
// nothing else, so a process shelling out to a toolchain fails on its first
// real unit of work.
func TestRenderPlistInheritsPathAndHome(t *testing.T) {
	t.Setenv("PATH", "/custom/bin")
	t.Setenv("HOME", "/Users/example")
	c := renderCfg()
	c.InheritPath, c.InheritHome = true, true

	out := string(RenderPlist(c))
	if !strings.Contains(out, "<string>/custom/bin</string>") {
		t.Error("inherit_path did not write the invoking shell's PATH")
	}
	if !strings.Contains(out, "<string>/Users/example</string>") {
		t.Error("inherit_home did not write HOME")
	}
}

func TestRenderPlistEscapesXML(t *testing.T) {
	c := renderCfg()
	c.Args = []string{"a&b", "<c>"}
	out := string(RenderPlist(c))
	if strings.Contains(out, "<string>a&b</string>") || strings.Contains(out, "<string><c></string>") {
		t.Error("XML metacharacters are not escaped")
	}
	if !strings.Contains(out, "a&amp;b") || !strings.Contains(out, "&lt;c&gt;") {
		t.Errorf("expected escaped forms in:\n%s", out)
	}
}

func TestRenderSystemdUnit(t *testing.T) {
	c := renderCfg()
	c.WorkingDir = "/srv"
	out := string(RenderSystemdUnit(c))
	for _, want := range []string{
		"[Unit]",
		"Description=com.example.svc",
		"ExecStart=/opt/svc -addr 127.0.0.1:8790",
		"Environment=A=one",
		"WorkingDirectory=/srv",
		"StandardOutput=append:/tmp/svc.log",
		"Restart=always",
		"RestartSec=10",
		"WantedBy=default.target",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unit is missing %q\n%s", want, out)
		}
	}
}

func TestRenderSystemdUnitQuotesExecStart(t *testing.T) {
	c := renderCfg()
	c.Program = "/opt/my svc"
	if !strings.Contains(string(RenderSystemdUnit(c)), "'/opt/my svc'") {
		t.Error("a program path with a space must be quoted in ExecStart")
	}
}
