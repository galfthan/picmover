package share

import (
	"encoding/binary"
	"encoding/hex"
	"net"
	"os"
	"strings"
)

// defaultGateway reads the IPv4 default route's gateway from
// /proc/net/route (nil when there is none).
func defaultGateway() net.IP {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return nil
	}
	ip, _ := parseProcRoute(string(b))
	return ip
}

// parseProcRoute finds the default route (destination 0, mask 0, flag
// RTF_GATEWAY) of a /proc/net/route table; its fields are little-endian
// hex.
func parseProcRoute(table string) (net.IP, error) {
	for i, line := range strings.Split(table, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		raw, err := hex.DecodeString(f[2])
		if err != nil || len(raw) != 4 {
			continue
		}
		v := binary.LittleEndian.Uint32(raw)
		if v == 0 {
			continue
		}
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, v)
		return ip, nil
	}
	return nil, errNoGateway
}
