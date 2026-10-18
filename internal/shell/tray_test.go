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
	for _, c := range []struct {
		state, address, title string
		checked               bool
	}{
		{"on", "192.168.1.20", "Share on Wi-Fi (192.168.1.20)", true},
		{"paused", "network changed", "Share on Wi-Fi (paused: network changed)", true},
		{"paused", "waiting for the home network", "Share on Wi-Fi (paused: waiting for the home network)", true},
		{"paused", "turn sharing on once on the Share page to record the home network", "Share on Wi-Fi (paused)", true},
		{"off", "", "Share on Wi-Fi", false},
		{"error", "", "Share on Wi-Fi", false},
	} {
		if title, checked := shareLabel(c.state, c.address); title != c.title || checked != c.checked {
			t.Errorf("shareLabel(%q) = %q %v", c.state, title, checked)
		}
	}
	if len(trayIcon()) == 0 {
		t.Error("no tray icon")
	}
}
