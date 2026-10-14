//go:build linux

package share

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsWirelessLinux(t *testing.T) {
	old := sysNet
	sysNet = t.TempDir()
	defer func() { sysNet = old }()
	os.MkdirAll(filepath.Join(sysNet, "wlp3s0", "wireless"), 0o755)
	os.MkdirAll(filepath.Join(sysNet, "wlan1"), 0o755)
	os.WriteFile(filepath.Join(sysNet, "wlan1", "phy80211"), nil, 0o644)
	os.MkdirAll(filepath.Join(sysNet, "enp0s31f6"), 0o755)
	for name, want := range map[string]bool{"wlp3s0": true, "wlan1": true, "enp0s31f6": false, "missing": false,
		"../wlp3s0": false, "": false} {
		if got := isWireless(name); got != want {
			t.Errorf("isWireless(%q) = %v", name, got)
		}
	}
}
