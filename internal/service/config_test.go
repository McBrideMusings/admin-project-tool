package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recorder struct{ info, ok, warn, err []string }

func (r *recorder) Info(m string) { r.info = append(r.info, m) }
func (r *recorder) Ok(m string)   { r.ok = append(r.ok, m) }
func (r *recorder) Warn(m string) { r.warn = append(r.warn, m) }
func (r *recorder) Err(m string)  { r.err = append(r.err, m) }

func base() map[string]any {
	return map[string]any{
		"label":   "com.example.svc",
		"program": "${HOME}/bin/svc",
	}
}

// TestReadConfigResolvesEnvNotShell pins the bug that broke `admin deploy
// server` on the first real project to use kind="launchd": ResolveComputed
// leaves a plain ${HOME} intact for a shell to expand later, but `program` is
// exec'd by launchd straight out of a plist, so an unexpanded value is a
// literal path that does not exist.
func TestReadConfigResolvesEnvNotShell(t *testing.T) {
	t.Setenv("HOME", "/Users/example")
	c, err := ReadConfig(base(), &recorder{})
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if c.Program != "/Users/example/bin/svc" {
		t.Errorf("Program = %q, want the expanded path", c.Program)
	}
}

func TestReadConfigRejectsRelativeProgram(t *testing.T) {
	raw := base()
	raw["program"] = "bin/svc"
	if _, err := ReadConfig(raw, &recorder{}); err == nil {
		t.Fatal("expected a relative program to be rejected")
	}
}

func TestReadConfigRejectsUnknownKey(t *testing.T) {
	raw := base()
	raw["stdout_path"] = "/tmp/x.log" // the plausible typo for "stdout"
	_, err := ReadConfig(raw, &recorder{})
	if err == nil || !strings.Contains(err.Error(), "stdout_path") {
		t.Fatalf("expected the unknown key named in the error, got %v", err)
	}
}

// TestEnvFileKeysIsAnAllowlist is the security-relevant one: a dotenv file used
// this way holds many unrelated secrets, and only the named keys may ever reach
// a LaunchAgent plist.
func TestEnvFileKeysIsAnAllowlist(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	body := "export WANTED=yes\n# a comment\nUNWANTED=nope\nQUOTED=\"quoted value\"\n"
	if err := os.WriteFile(envPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	raw := base()
	raw["program"] = "/opt/svc"
	raw["env_file"] = envPath
	raw["env_file_keys"] = []any{"WANTED", "QUOTED"}

	c, err := ReadConfig(raw, &recorder{})
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if c.Env["WANTED"] != "yes" {
		t.Errorf("WANTED = %q, want the export- prefix stripped", c.Env["WANTED"])
	}
	if c.Env["QUOTED"] != "quoted value" {
		t.Errorf("QUOTED = %q, want the surrounding quotes stripped", c.Env["QUOTED"])
	}
	if _, present := c.Env["UNWANTED"]; present {
		t.Error("UNWANTED was lifted; env_file_keys must be an allowlist")
	}
}

func TestEnvFileMissingWarnsRatherThanFails(t *testing.T) {
	raw := base()
	raw["program"] = "/opt/svc"
	raw["env_file"] = "/nonexistent/.env"
	raw["env_file_keys"] = []any{"ANY"}

	rec := &recorder{}
	if _, err := ReadConfig(raw, rec); err != nil {
		t.Fatalf("a missing env_file must degrade, not fail: %v", err)
	}
	if len(rec.warn) == 0 {
		t.Error("expected a warning naming the missing env_file")
	}
}

func TestExplicitEnvWinsOverEnvFile(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("K=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := base()
	raw["program"] = "/opt/svc"
	raw["env_file"] = envPath
	raw["env_file_keys"] = []any{"K"}
	raw["env"] = map[string]any{"K": "explicit"}

	c, err := ReadConfig(raw, &recorder{})
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if c.Env["K"] != "explicit" {
		t.Errorf("K = %q, want the explicit env to win", c.Env["K"])
	}
}

func TestArgsCmdAppendsStdoutFields(t *testing.T) {
	raw := base()
	raw["program"] = "/opt/svc"
	raw["args"] = []any{"-v"}
	raw["args_cmd"] = "echo '-addr 127.0.0.1:8790,10.0.0.1:8790'"

	c, err := ReadConfig(raw, &recorder{})
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	want := []string{"-v", "-addr", "127.0.0.1:8790,10.0.0.1:8790"}
	if len(c.Args) != len(want) {
		t.Fatalf("Args = %v, want %v", c.Args, want)
	}
	for i := range want {
		if c.Args[i] != want[i] {
			t.Fatalf("Args = %v, want %v", c.Args, want)
		}
	}
}

// A service that falls back to a narrower bind address is right; one that
// refuses to deploy because the VPN is down is not.
func TestArgsCmdFailureDegrades(t *testing.T) {
	raw := base()
	raw["program"] = "/opt/svc"
	raw["args_cmd"] = "exit 3"

	rec := &recorder{}
	c, err := ReadConfig(raw, rec)
	if err != nil {
		t.Fatalf("a failing args_cmd must not fail the install: %v", err)
	}
	if len(c.Args) != 0 {
		t.Errorf("Args = %v, want none added", c.Args)
	}
	if len(rec.warn) == 0 {
		t.Error("expected a warning that args_cmd failed")
	}
}
