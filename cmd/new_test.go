package cmd

import "testing"

func TestStarterProjectName(t *testing.T) {
	// Expected values are Python's `name.replace("-", " ").title()`.
	for dir, want := range map[string]string{
		"/x/MY-APP":    "My App",
		"/x/my-app":    "My App",
		"/x/app2go":    "App2Go",
		"/x/it's-mine": "It'S Mine",
	} {
		if got := starterProjectName(dir); got != want {
			t.Errorf("starterProjectName(%q) = %q, want %q", dir, got, want)
		}
	}
}
