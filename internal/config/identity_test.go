package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestIdentityMatching pins the comparisons. Case-insensitive because git records
// the case a commit was authored with and the same person's address comes back
// capitalised differently from different machines.
func TestIdentityMatching(t *testing.T) {
	id := Identity{Login: "McBrideMusings", Emails: []string{"pierce@example.com"}}
	if !id.Owns("PIERCE@Example.com") {
		t.Error("an address differing only in case did not match")
	}
	if !id.IsMe("mcbridemusings") {
		t.Error("a login differing only in case did not match")
	}
	if id.Owns("") || id.IsMe("") {
		t.Error("an empty value matched — unknown and mine are different facts")
	}
	if (Identity{}).Known() {
		t.Error("an empty identity reported itself as usable")
	}
}

// TestSeedIdentityIsWriteOnce checks the first run seeds and every later run leaves
// the file alone — an email added by hand (a work address, an agent's address) must
// never be re-guessed away on the next launch.
func TestSeedIdentityIsWriteOnce(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	wrote, err := SeedIdentity("McBrideMusings", "pierce@example.com")
	if err != nil || !wrote {
		t.Fatalf("first run did not seed: wrote=%v err=%v", wrote, err)
	}
	path := filepath.Join(dir, ".config", "repo", "config.toml")
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		t.Fatalf("seeded file does not parse: %v", err)
	}
	if cfg.Identity.Login != "McBrideMusings" || !cfg.Identity.Owns("pierce@example.com") {
		t.Fatalf("seeded identity is wrong: %+v", cfg.Identity)
	}

	// A hand-added address, then another launch.
	body, _ := os.ReadFile(path)
	edited := strings.Replace(string(body),
		`emails = ["pierce@example.com"]`,
		`emails = ["pierce@example.com", "pierce@work.example"]`, 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	wrote, err = SeedIdentity("SomeoneElse", "other@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Error("a later run re-seeded over an existing identity")
	}
	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), "pierce@work.example") {
		t.Error("the hand-added address was lost")
	}
}

// TestSeedIdentityNeedsSomethingToSay: with neither `gh` nor git able to answer,
// writing a [identity] with no values would only look like the question was
// settled.
func TestSeedIdentityNeedsSomethingToSay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if wrote, _ := SeedIdentity("", ""); wrote {
		t.Error("seeded an empty identity")
	}
}

// TestIdentityMergesGlobalThenRepo: there is one of you, so a per-repo login
// replaces the global one — but a work address is an addition to your identity,
// not a replacement, so emails append.
func TestIdentityMergesGlobalThenRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".config", "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	global := "[identity]\nlogin = \"personal\"\nemails = [\"me@home.example\"]\n"
	if err := os.WriteFile(filepath.Join(home, ".config", "repo", "config.toml"), []byte(global), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	local := "[identity]\nlogin = \"work\"\nemails = [\"me@work.example\", \"me@home.example\"]\n"
	if err := os.WriteFile(filepath.Join(repo, ".repo", "config.toml"), []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Identity.Login != "work" {
		t.Errorf("login = %q, want the per-repo one to win", cfg.Identity.Login)
	}
	if !cfg.Identity.Owns("me@home.example") || !cfg.Identity.Owns("me@work.example") {
		t.Errorf("emails did not accumulate across both files: %v", cfg.Identity.Emails)
	}
	if len(cfg.Identity.Emails) != 2 {
		t.Errorf("an address repeated in both files was duplicated: %v", cfg.Identity.Emails)
	}
}
