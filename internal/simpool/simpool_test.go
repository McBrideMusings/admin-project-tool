package simpool

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodUDID = "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"

func fakeTool(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sim")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvTool, p)
	held = nil
	return p
}

func TestAcquireWithoutToolIsNil(t *testing.T) {
	t.Setenv(EnvTool, "")
	l, err := Acquire("", "")
	if l != nil || err != nil {
		t.Fatalf("got %v, %v; want nil, nil", l, err)
	}
	if !strings.Contains(Status(), "not configured") {
		t.Fatalf("status = %q", Status())
	}
}

func TestAcquireReadsUDIDAndReleases(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	fakeTool(t, `echo "$@" >> `+log+"\n"+`if [ "$1" = acquire ]; then echo `+goodUDID+"; fi\n")
	l, err := Acquire("com.apple.dev", "com.apple.rt")
	if err != nil || l.UDID != goodUDID {
		t.Fatalf("lease %+v err %v", l, err)
	}
	if err := ReleaseHeld(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(log)
	want := "acquire --device com.apple.dev --runtime com.apple.rt\nrelease\n"
	if string(got) != want {
		t.Fatalf("calls %q, want %q", got, want)
	}
}

func TestBusyAndGarbageOutput(t *testing.T) {
	fakeTool(t, "echo no slot >&2; exit 75\n")
	if _, err := Acquire("", ""); !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "no slot") {
		t.Fatalf("err = %v", err)
	}
	fakeTool(t, "echo not-a-udid\n")
	if _, err := Acquire("", ""); err == nil {
		t.Fatal("garbage output accepted")
	}
}
