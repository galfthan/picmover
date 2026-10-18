package main

import (
	"errors"
	"net"
)

// defaultLANIP is the local IPv4 address of the default route, found by
// "dialling" UDP (no packet is sent), as the design's netif does.
func defaultLANIP() (net.IP, error) {
	c, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return nil, err
	}
	defer c.Close()
	ip := c.LocalAddr().(*net.UDPAddr).IP.To4()
	if ip == nil || !ip.IsPrivate() {
		return nil, errors.New("the default route's address " + c.LocalAddr().String() + " is not a private IPv4 address; pass -ip")
	}
	return ip, nil
}

// subnetOf returns the interface name and network that hold ip, for the
// firewall hint.
func subnetOf(ip net.IP) (string, *net.IPNet) {
	ifs, err := net.Interfaces()
	if err != nil {
		return "", nil
	}
	for _, ifc := range ifs {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
				return ifc.Name, &net.IPNet{IP: n.IP.Mask(n.Mask), Mask: n.Mask}
			}
		}
	}
	return "", nil
}
