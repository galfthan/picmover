package share

import (
	"errors"
	"net"
	"strings"
	"sync"
)

// Iface is a network interface as Candidates sees it (net.Interface and
// its addresses; tests pass fakes).
type Iface struct {
	Name  string
	Flags net.Flags
	Addrs []net.Addr
}

// Candidate is a LAN address the share listener can bind.
type Candidate struct {
	IP     net.IP
	Iface  string
	Subnet *net.IPNet // the interface network holding IP
	// Virtual marks container, VM and VPN interfaces: listed as "other
	// networks", never chosen by default.
	Virtual bool
}

// virtualPrefixes are interface name prefixes of container, VM and VPN
// networks.
var virtualPrefixes = []string{"docker", "br-", "veth", "virbr", "vmnet", "vboxnet", "tun", "tap", "wg",
	"tailscale", "zt", "utun", "vethernet"}

// isVirtual reports whether an interface name marks a virtual network.
func isVirtual(name string) bool {
	n := strings.ToLower(name)
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// CandidatesFrom lists the RFC 1918 IPv4 addresses of the interfaces that
// are up and neither loopback nor point-to-point, in interface order.
func CandidatesFrom(ifs []Iface) []Candidate {
	var out []Candidate
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		for _, a := range ifc.Addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := n.IP.To4()
			if ip == nil || !ip.IsPrivate() {
				continue
			}
			mask := n.Mask
			if len(mask) == net.IPv6len {
				mask = mask[12:]
			}
			out = append(out, Candidate{IP: ip, Iface: ifc.Name, Subnet: &net.IPNet{IP: ip.Mask(mask), Mask: mask},
				Virtual: isVirtual(ifc.Name)})
		}
	}
	return out
}

// Netif reads the machine's network state. System is the real one; tests
// substitute their own.
type Netif interface {
	// Candidates lists the LAN addresses (CandidatesFrom of the system's
	// interfaces).
	Candidates() ([]Candidate, error)
	// DefaultIP is the local address of the default route (nil: none).
	DefaultIP() net.IP
	// Gateway is the default gateway (nil: unknown).
	Gateway() net.IP
}

// System is the machine's Netif.
var System Netif = systemNetif{}

type systemNetif struct{}

func (systemNetif) Candidates() ([]Candidate, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	list := make([]Iface, 0, len(ifs))
	for _, ifc := range ifs {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		list = append(list, Iface{Name: ifc.Name, Flags: ifc.Flags, Addrs: addrs})
	}
	return CandidatesFrom(list), nil
}

// DefaultIP "dials" UDP to a documentation address, which sends no
// packet, and reads the local address the route chose.
func (systemNetif) DefaultIP() net.IP {
	c, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return nil
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.To4()
	}
	return nil
}

func (systemNetif) Gateway() net.IP { return defaultGateway() }

// Choose picks the address to bind: the candidate on interface name when
// it has one (the user's choice), else the default route's when it is a
// candidate, else the first non-virtual candidate.
func Choose(cands []Candidate, name string, def net.IP) (Candidate, bool) {
	if name != "" {
		for _, c := range cands {
			if c.Iface == name {
				return c, true
			}
		}
	}
	if def != nil {
		for _, c := range cands {
			if c.IP.Equal(def) {
				return c, true
			}
		}
	}
	for _, c := range cands {
		if !c.Virtual {
			return c, true
		}
	}
	return Candidate{}, false
}

// Network verdicts of Compare.
const (
	NetSame   = "same"   // the bound address is still there
	NetRebind = "rebind" // a new address on the same home network
	NetPause  = "pause"  // another network, or no address
)

// Compare decides what a network check means for a listener bound to
// bound on interface iface in the home network: the address is still
// there (same); the interface has a new address in the same subnet and
// the gateway is unchanged (rebind to next); anything else, including a
// changed gateway, means another network (pause). An unknown gateway on
// either side compares by subnet only.
func Compare(home Home, iface string, bound net.IP, cands []Candidate, gw net.IP) (verdict string, next Candidate) {
	for _, c := range cands {
		if c.IP.Equal(bound) {
			if c.Subnet.String() == home.Subnet && sameGateway(home.Gateway, gw) {
				return NetSame, c
			}
			return NetPause, Candidate{}
		}
	}
	if !sameGateway(home.Gateway, gw) {
		return NetPause, Candidate{}
	}
	for _, c := range cands {
		if c.Iface == iface && c.Subnet.String() == home.Subnet {
			return NetRebind, c
		}
	}
	return NetPause, Candidate{}
}

func sameGateway(home string, gw net.IP) bool {
	if home == "" || gw == nil {
		return true
	}
	return home == gw.String()
}

// AllowFrom is the connection filter's rule: loopback, or an address in
// subnet.
func AllowFrom(subnet *net.IPNet) func(net.IP) bool {
	return func(ip net.IP) bool {
		return ip.IsLoopback() || (subnet != nil && subnet.Contains(ip))
	}
}

// MaxConns caps the share listener's concurrent connections.
const MaxConns = 64

// FilterListener wraps ln: a connection whose remote IP allow refuses, or
// one beyond max concurrent connections, is closed at Accept, before the
// TLS handshake.
func FilterListener(ln net.Listener, allow func(net.IP) bool, max int) net.Listener {
	return &filterListener{Listener: ln, allow: allow, slots: make(chan struct{}, max)}
}

type filterListener struct {
	net.Listener
	allow func(net.IP) bool
	slots chan struct{}
}

func (l *filterListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if !l.allow(remoteIP(c.RemoteAddr())) {
			c.Close()
			continue
		}
		select {
		case l.slots <- struct{}{}:
			return &slotConn{Conn: c, release: func() { <-l.slots }}, nil
		default:
			c.Close()
		}
	}
}

// remoteIP is the IP of a connection's remote address (nil when unknown).
func remoteIP(a net.Addr) net.IP {
	switch a := a.(type) {
	case *net.TCPAddr:
		return a.IP
	case nil:
		return nil
	}
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

// slotConn frees its connection slot once, at the first Close.
type slotConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *slotConn) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
}

// errNoGateway is returned by the per-OS gateway readers.
var errNoGateway = errors.New("no default gateway")
