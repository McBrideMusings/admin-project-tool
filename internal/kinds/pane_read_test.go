package kinds

import (
	"context"
	"testing"
)

func TestReadLauncherPanePassesHandleAndReturnsStdout(t *testing.T) {
	got, err := ReadLauncherPane(context.Background(), `printf 'pane:%s' "$ADMIN_HANDLE"`, "h-9")
	if err != nil {
		t.Fatal(err)
	}
	if got != "pane:h-9" {
		t.Errorf("ReadLauncherPane = %q, want %q", got, "pane:h-9")
	}
}
