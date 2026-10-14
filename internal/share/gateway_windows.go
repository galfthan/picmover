package share

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// defaultGateway reads the IPv4 default route's next hop with
// GetIpForwardTable2 (nil when there is none). With several default
// routes, the one with the lowest metric wins.
func defaultGateway() net.IP {
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_INET, &table); err != nil || table == nil {
		return nil
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	var (
		best   net.IP
		metric uint32
	)
	for _, r := range table.Rows() {
		if r.DestinationPrefix.PrefixLength != 0 || r.NextHop.Family != windows.AF_INET {
			continue
		}
		// SOCKADDR_IN: family, port, then the address in network order.
		addr := (*[4]byte)(unsafe.Pointer(&r.NextHop.Data[0]))
		ip := net.IPv4(addr[0], addr[1], addr[2], addr[3]).To4()
		if ip.IsUnspecified() {
			continue
		}
		if best == nil || r.Metric < metric {
			best, metric = ip, r.Metric
		}
	}
	return best
}
