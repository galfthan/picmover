//go:build linux

package share

import (
	"os"
	"path/filepath"
)

// sysNet is where Linux lists the network interfaces (tests change it).
var sysNet = "/sys/class/net"

// isWireless reports a Wi-Fi interface: the kernel lists a wireless/ or
// phy80211 entry for it.
func isWireless(name string) bool {
	if name == "" || filepath.Base(name) != name {
		return false
	}
	for _, e := range []string{"wireless", "phy80211"} {
		if _, err := os.Stat(filepath.Join(sysNet, name, e)); err == nil {
			return true
		}
	}
	return false
}
