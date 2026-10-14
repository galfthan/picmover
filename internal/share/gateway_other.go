//go:build !linux && !darwin && !windows

package share

import "net"

// defaultGateway is unknown here: the home network check uses the subnet
// alone.
func defaultGateway() net.IP { return nil }
