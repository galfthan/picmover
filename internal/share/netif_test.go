package share

import (
	"errors"
	"net"
	"regexp"
	"strconv"
	"testing"
	"time"

	"rsc.io/qr"
)

func ipnet(s string) *net.IPNet {
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	n.IP = ip
	return n
}

func TestCandidatesFrom(t *testing.T) {
	up := net.FlagUp | net.FlagBroadcast
	got := CandidatesFrom([]Iface{
		{Name: "lo", Flags: up | net.FlagLoopback, Addrs: []net.Addr{ipnet("127.0.0.1/8")}},
		{Name: "wlp3s0", Flags: up, Addrs: []net.Addr{ipnet("192.168.1.20/24"), ipnet("fe80::1/64")}},
		{Name: "eth0", Flags: net.FlagBroadcast, Addrs: []net.Addr{ipnet("192.168.2.5/24")}}, // down
		{Name: "docker0", Flags: up, Addrs: []net.Addr{ipnet("172.17.0.1/16")}},
		{Name: "wg0", Flags: up | net.FlagPointToPoint, Addrs: []net.Addr{ipnet("10.8.0.2/24")}},
		{Name: "enp1", Flags: up, Addrs: []net.Addr{ipnet("85.1.2.3/24"), ipnet("10.0.0.7/8")}},
		{Name: "vEthernet (WSL)", Flags: up, Addrs: []net.Addr{ipnet("172.20.0.1/20")}},
	})
	type c struct {
		ip, iface, subnet string
		virtual           bool
	}
	want := []c{{"192.168.1.20", "wlp3s0", "192.168.1.0/24", false}, {"172.17.0.1", "docker0", "172.17.0.0/16", true},
		{"10.0.0.7", "enp1", "10.0.0.0/8", false}, {"172.20.0.1", "vEthernet (WSL)", "172.20.0.0/20", true}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, w := range want {
		g := got[i]
		if g.IP.String() != w.ip || g.Iface != w.iface || g.Subnet.String() != w.subnet || g.Virtual != w.virtual {
			t.Errorf("%d: %+v, want %+v", i, g, w)
		}
	}
	// The default route's address wins; a virtual one is never the
	// fallback; the user's interface wins over both.
	if ch, _ := Choose(got, "", net.ParseIP("10.0.0.7")); ch.Iface != "enp1" {
		t.Errorf("default route: %+v", ch)
	}
	if ch, _ := Choose(got[1:2], "", nil); ch.Iface != "" {
		t.Errorf("chose a virtual interface by default: %+v", ch)
	}
	if ch, _ := Choose(got, "docker0", net.ParseIP("10.0.0.7")); ch.Iface != "docker0" {
		t.Errorf("user choice: %+v", ch)
	}
}

func TestCompareHomeNetwork(t *testing.T) {
	home := Home{Subnet: "192.168.1.0/24", Gateway: "192.168.1.1"}
	gw := net.ParseIP("192.168.1.1")
	cand := func(ip, subnet string) Candidate {
		_, n, _ := net.ParseCIDR(subnet)
		return Candidate{IP: net.ParseIP(ip).To4(), Iface: "wlp3s0", Subnet: n}
	}
	bound := net.ParseIP("192.168.1.20")
	if v, _ := Compare(home, "wlp3s0", bound, []Candidate{cand("192.168.1.20", "192.168.1.0/24")}, gw); v != NetSame {
		t.Errorf("same: %s", v)
	}
	v, next := Compare(home, "wlp3s0", bound, []Candidate{cand("192.168.1.33", "192.168.1.0/24")}, gw)
	if v != NetRebind || next.IP.String() != "192.168.1.33" {
		t.Errorf("new lease: %s %+v", v, next)
	}
	if v, _ := Compare(home, "wlp3s0", bound, []Candidate{cand("192.168.1.33", "192.168.1.0/24")}, net.ParseIP("192.168.1.254")); v != NetPause {
		t.Errorf("another gateway: %s", v)
	}
	if v, _ := Compare(home, "wlp3s0", bound, []Candidate{cand("10.1.2.3", "10.0.0.0/8")}, net.ParseIP("10.0.0.1")); v != NetPause {
		t.Errorf("cafe: %s", v)
	}
	if v, _ := Compare(home, "wlp3s0", bound, nil, nil); v != NetPause {
		t.Errorf("no address: %s", v)
	}
	// The same address on another network (same private range, other
	// gateway) is not home.
	if v, _ := Compare(home, "wlp3s0", bound, []Candidate{cand("192.168.1.20", "192.168.1.0/24")}, net.ParseIP("192.168.1.254")); v != NetPause {
		t.Errorf("same address, other gateway: %s", v)
	}
	// Without a gateway the subnet decides.
	if v, _ := Compare(Home{Subnet: home.Subnet}, "wlp3s0", bound, []Candidate{cand("192.168.1.33", "192.168.1.0/24")}, nil); v != NetRebind {
		t.Errorf("no gateway: %s", v)
	}
}

// fakeConn is a net.Conn with a chosen remote address.
type fakeConn struct {
	net.Conn
	remote net.Addr
	closed bool
}

func (c *fakeConn) RemoteAddr() net.Addr             { return c.remote }
func (c *fakeConn) Close() error                     { c.closed = true; return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

type fakeListener struct {
	net.Listener
	conns []*fakeConn
}

func (l *fakeListener) Accept() (net.Conn, error) {
	if len(l.conns) == 0 {
		return nil, errors.New("done")
	}
	c := l.conns[0]
	l.conns = l.conns[1:]
	return c, nil
}

func TestFilterListener(t *testing.T) {
	tcp := func(s string) net.Addr { return &net.TCPAddr{IP: net.ParseIP(s), Port: 5555} }
	outside, inside, lo := &fakeConn{remote: tcp("192.168.2.9")}, &fakeConn{remote: tcp("192.168.1.34")}, &fakeConn{remote: tcp("127.0.0.1")}
	ln := FilterListener(&fakeListener{conns: []*fakeConn{outside, inside, lo}}, AllowFrom(ipnet("192.168.1.0/24")),
		ConnLimits{Total: 1})
	c, err := ln.Accept()
	if err != nil || c.(*slotConn).Conn != inside || !outside.closed {
		t.Fatalf("first accept %v %v, outside closed %v", c, err, outside.closed)
	}
	// The cap: the loopback connection finds no free slot and is closed.
	if _, err := ln.Accept(); err == nil || !lo.closed {
		t.Fatalf("over the cap: %v, closed %v", err, lo.closed)
	}
	c.Close()
	c.Close() // frees its slot once
	ln = FilterListener(&fakeListener{conns: []*fakeConn{{remote: tcp("127.0.0.1")}}}, AllowFrom(ipnet("192.168.1.0/24")),
		ConnLimits{Total: 1})
	if _, err := ln.Accept(); err != nil {
		t.Fatal("loopback refused")
	}
}

// S3: one address cannot take more than PerIP connections; others still
// get theirs, and a closed connection frees its address's slot.
func TestFilterListenerPerIP(t *testing.T) {
	tcp := func(s string) net.Addr { return &net.TCPAddr{IP: net.ParseIP(s), Port: 5555} }
	var conns []*fakeConn
	for i := 0; i < 4; i++ {
		conns = append(conns, &fakeConn{remote: tcp("192.168.1.66")})
	}
	other := &fakeConn{remote: tcp("192.168.1.34")}
	again := &fakeConn{remote: tcp("192.168.1.66")}
	fl := &fakeListener{conns: append(conns, other)}
	ln := FilterListener(fl, AllowFrom(ipnet("192.168.1.0/24")), ConnLimits{Total: 10, PerIP: 3})
	var got []net.Conn
	for i := 0; i < 4; i++ {
		c, err := ln.Accept()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, c)
	}
	if !conns[3].closed || got[3].(*slotConn).Conn != other {
		t.Fatalf("the 4th connection of one address was not refused")
	}
	got[0].Close()
	fl.conns = []*fakeConn{again}
	if c, err := ln.Accept(); err != nil || c.(*slotConn).Conn != again {
		t.Fatalf("a freed slot was not reused: %v", err)
	}
}

// S3: a connection that sends nothing is closed after the first-byte
// deadline; one that speaks gets the deadline its server asks for.
func TestFilterListenerFirstByte(t *testing.T) {
	raw, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := FilterListener(raw, AllowFrom(nil), ConnLimits{Total: 4, PerIP: 4, FirstByte: 100 * time.Millisecond})
	defer ln.Close()
	silent, err := net.Dial("tcp4", raw.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(time.Hour)) // the server's own, longer deadline
	start := time.Now()
	if _, err := c.Read(make([]byte, 1)); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("silent connection: %v after %v", err, time.Since(start))
	}
	c.Close()

	talker, err := net.Dial("tcp4", raw.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer talker.Close()
	c, err = ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(time.Hour))
	talker.Write([]byte("x"))
	if _, err := c.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // past the first-byte deadline
	talker.Write([]byte("y"))
	if _, err := c.Read(make([]byte, 1)); err != nil {
		t.Fatalf("after the first byte the server's deadline applies: %v", err)
	}
}

var svgModule = regexp.MustCompile(`M(\d+) (\d+)h1v1h-1z`)

// The SVG draws exactly the encoder's dark modules (offset by the quiet
// zone), so it decodes to the URL as far as the encoder does.
func TestQRSVG(t *testing.T) {
	const u = "https://192.168.1.20:7443/#/pair/abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ"
	svg, err := QRSVG(u)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := qr.Encode(u, qr.M)
	got := map[[2]int]bool{}
	for _, m := range svgModule.FindAllStringSubmatch(svg, -1) {
		x, _ := strconv.Atoi(m[1])
		y, _ := strconv.Atoi(m[2])
		got[[2]int{x - qrQuiet, y - qrQuiet}] = true
	}
	n := 0
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.Black(x, y) != got[[2]int{x, y}] {
				t.Fatalf("module %d,%d differs", x, y)
			}
			if c.Black(x, y) {
				n++
			}
		}
	}
	if n != len(got) || !regexp.MustCompile(`viewBox="0 0 `+strconv.Itoa(c.Size+8)).MatchString(svg) {
		t.Fatalf("extra modules or viewBox: %d %d", n, len(got))
	}
	if txt, err := QRTerminal(u); err != nil || len(txt) == 0 {
		t.Fatal("terminal QR")
	}
}
