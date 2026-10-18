//go:build !linux

package share

import "strings"

// isWireless reports a Wi-Fi interface, best effort from its name: on
// Windows the adapter is usually named "Wi-Fi" or "WLAN" (localized names
// are missed); on macOS the name (en0, en1) does not tell, so it is false.
func isWireless(name string) bool {
	n := strings.ToLower(name)
	for _, s := range []string{"wi-fi", "wifi", "wlan", "wireless"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}
