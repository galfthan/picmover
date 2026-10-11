//go:build windows || linux

package shell

import (
	"path/filepath"
	"testing"
)

func TestTrayLabels(t *testing.T) {
	for n, want := range map[int]string{0: "Quit", 1: "Quit (1 job running)", 3: "Quit (3 jobs running)"} {
		if got := quitLabel(n); got != want {
			t.Errorf("quitLabel(%d) = %q", n, got)
		}
	}
	ws := filepath.Join(t.TempDir(), "media")
	if title, tip := workspaceLabel(ws); title != "Workspace: media" || tip != ws {
		t.Errorf("workspaceLabel = %q %q", title, tip)
	}
	if title, _ := workspaceLabel(""); title != "No workspace chosen" {
		t.Errorf("unset label %q", title)
	}
	if len(trayIcon()) == 0 {
		t.Error("no tray icon")
	}
}
