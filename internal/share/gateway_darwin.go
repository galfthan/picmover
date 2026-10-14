package share

import (
	"context"
	"net"
	"os/exec"
	"strings"
	"time"
)

// defaultGateway asks "route -n get default" for the IPv4 default
// gateway (nil when there is none).
func defaultGateway() net.IP {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/sbin/route", "-n", "get", "default").Output()
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && k == "gateway" {
			if ip := net.ParseIP(strings.TrimSpace(v)).To4(); ip != nil {
				return ip
			}
		}
	}
	return nil
}
