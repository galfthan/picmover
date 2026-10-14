package share

import (
	"errors"
	"net"
	"strings"
	"sync"
	"time"
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
	// Wireless marks a Wi-Fi interface (Linux: from /sys/class/net;
	// elsewhere a guess from the name, false when unknown).
	Wireless bool
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
	cands := CandidatesFrom(list)
	for i := range cands {
		cands[i].Wireless = isWireless(cands[i].Iface)
	}
	return cands, nil
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

// Network places of Locate.
const (
	NetHome = "home" // a candidate on the home network
	NetAway = "away" // another network
	NetNone = "none" // no LAN address at all (Wi-Fi down, asleep): transient
)

// Locate finds the home network among cands: NetHome with the candidate
// to bind (on interface iface when it has one), NetAway when a
// non-virtual candidate is on another network or the gateway differs,
// NetNone when there is no non-virtual candidate (only container or VPN
// networks, or nothing). A virtual candidate counts as home only on the
// interface the user chose.
func Locate(home Home, iface string, cands []Candidate, gw net.IP) (string, Candidate) {
	lan := false
	for _, c := range cands {
		lan = lan || !c.Virtual
	}
	if !sameGateway(home.Gateway, gw) {
		if lan {
			return NetAway, Candidate{}
		}
		return NetNone, Candidate{}
	}
	var found *Candidate
	for i, c := range cands {
		if c.Subnet.String() != home.Subnet || (c.Virtual && c.Iface != iface) {
			continue
		}
		if found == nil || (c.Iface == iface && found.Iface != iface) {
			found = &cands[i]
		}
	}
	switch {
	case found != nil:
		return NetHome, *found
	case lan:
		return NetAway, Candidate{}
	}
	return NetNone, Candidate{}
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

// ConnLimits bound the share listener's connections.
type ConnLimits struct {
	Total int // concurrent connections in all
	PerIP int // concurrent connections of one remote address
	// FirstByte closes a connection that sends nothing for this long
	// (0: no such deadline). The TLS handshake and the request headers
	// then have the server's own timeouts.
	FirstByte time.Duration
}

// DefaultConnLimits are the share listener's limits: a phone over HTTP/2
// needs one connection, over HTTP/1.1 up to six, plus the occasional
// preconnect; one address cannot hold the listener's connections, and a
// connection that never speaks is gone after 5 seconds.
var DefaultConnLimits = ConnLimits{Total: 256, PerIP: 12, FirstByte: 5 * time.Second}

// FilterListener wraps ln: a connection whose remote IP allow refuses, or
// one beyond the total or per-address caps of lim, is closed at Accept,
// before the TLS handshake. A connection that sends no byte within
// lim.FirstByte times out.
func FilterListener(ln net.Listener, allow func(net.IP) bool, lim ConnLimits) net.Listener {
	return &filterListener{Listener: ln, allow: allow, lim: lim, per: map[string]int{}}
}

type filterListener struct {
	net.Listener
	allow func(net.IP) bool
	lim   ConnLimits

	mu    sync.Mutex
	total int
	per   map[string]int // open connections by remote IP
}

func (l *filterListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip := remoteIP(c.RemoteAddr())
		if !l.allow(ip) || !l.take(ip.String()) {
			c.Close()
			continue
		}
		key := ip.String()
		sc := &slotConn{Conn: c, release: func() { l.give(key) }}
		if l.lim.FirstByte > 0 {
			sc.early = time.Now().Add(l.lim.FirstByte)
			_ = c.SetReadDeadline(sc.early)
		}
		return sc, nil
	}
}

// take reserves a connection slot for key; false over a cap.
func (l *filterListener) take(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if (l.lim.Total > 0 && l.total >= l.lim.Total) || (l.lim.PerIP > 0 && l.per[key] >= l.lim.PerIP) {
		return false
	}
	l.total++
	l.per[key]++
	return true
}

// give frees key's slot.
func (l *filterListener) give(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total--
	if l.per[key]--; l.per[key] <= 0 {
		delete(l.per, key)
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

// slotConn frees its connection slot once, at the first Close. Until its
// first byte arrives, every read deadline is clamped to early (the
// first-byte deadline); after it, the deadline last asked for applies.
type slotConn struct {
	net.Conn
	once    sync.Once
	release func()

	mu    sync.Mutex
	early time.Time // zero once the first byte arrived, or without a first-byte deadline
	asked time.Time // the read deadline last asked for
}

func (c *slotConn) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
}

func (c *slotConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.mu.Lock()
		if !c.early.IsZero() {
			c.early = time.Time{}
			_ = c.Conn.SetReadDeadline(c.asked)
		}
		c.mu.Unlock()
	}
	return n, err
}

func (c *slotConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = t
	return c.Conn.SetReadDeadline(c.clampLocked(t))
}

func (c *slotConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = t
	if err := c.Conn.SetWriteDeadline(t); err != nil {
		return err
	}
	return c.Conn.SetReadDeadline(c.clampLocked(t))
}

// clampLocked is t, or the first-byte deadline while it is earlier.
func (c *slotConn) clampLocked(t time.Time) time.Time {
	if c.early.IsZero() || (!t.IsZero() && t.Before(c.early)) {
		return t
	}
	return c.early
}

// errNoGateway is returned by the per-OS gateway readers.
var errNoGateway = errors.New("no default gateway")
