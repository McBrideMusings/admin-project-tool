package git

import (
	"context"
	"testing"
)

func TestCheckoutOrCreateAlreadyOn(t *testing.T) {
	dir := setupOriginRepo(t)
	ctx := context.Background()

	action, err := CheckoutOrCreate(ctx, dir, "main")
	if err != nil {
		t.Fatalf("CheckoutOrCreate: %v", err)
	}
	if action != "already on" {
		t.Fatalf("action = %q, want %q", action, "already on")
	}
}

func TestCheckoutOrCreateSwitchesToExistingLocal(t *testing.T) {
	dir := setupOriginRepo(t)
	commitOn(t, dir, "feature")
	gitT(t, dir, "switch", "-q", "main")
	ctx := context.Background()

	action, err := CheckoutOrCreate(ctx, dir, "feature")
	if err != nil {
		t.Fatalf("CheckoutOrCreate: %v", err)
	}
	if action != "switched to" {
		t.Fatalf("action = %q, want %q", action, "switched to")
	}
	if got := CurrentBranch(ctx, dir); got != "feature" {
		t.Fatalf("current branch = %q, want %q", got, "feature")
	}
}

func TestCheckoutOrCreateFetchesRemoteOnly(t *testing.T) {
	dir := setupOriginRepo(t)
	commitOn(t, dir, "remote-only")
	gitT(t, dir, "push", "-q", "origin", "remote-only")
	gitT(t, dir, "switch", "-q", "main")
	gitT(t, dir, "branch", "-D", "remote-only")
	ctx := context.Background()

	if BranchExistsLocal(ctx, dir, "remote-only") {
		t.Fatal("test setup: remote-only should not exist locally yet")
	}
	action, err := CheckoutOrCreate(ctx, dir, "remote-only")
	if err != nil {
		t.Fatalf("CheckoutOrCreate: %v", err)
	}
	if action != "checked out" {
		t.Fatalf("action = %q, want %q", action, "checked out")
	}
	if got := CurrentBranch(ctx, dir); got != "remote-only" {
		t.Fatalf("current branch = %q, want %q", got, "remote-only")
	}
}

func TestCheckoutOrCreateForksNewBranchFromOrigin(t *testing.T) {
	dir := setupOriginRepo(t)
	ctx := context.Background()

	action, err := CheckoutOrCreate(ctx, dir, "brand-new")
	if err != nil {
		t.Fatalf("CheckoutOrCreate: %v", err)
	}
	if action != "created" {
		t.Fatalf("action = %q, want %q", action, "created")
	}
	if got := CurrentBranch(ctx, dir); got != "brand-new" {
		t.Fatalf("current branch = %q, want %q", got, "brand-new")
	}
	if !BranchExistsLocal(ctx, dir, "brand-new") {
		t.Fatal("brand-new was not created locally")
	}
}
