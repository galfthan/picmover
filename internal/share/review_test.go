package share

import (
	"net"
	"testing"
	"time"
)

// S1, R3: Locate tells the home network from another one and from no
// network at all (a transient state: Wi-Fi down, just woken up).
func TestLocate(t *testing.T) {
	home := Home{Subnet: "192.168.1.0/24", Gateway: "192.168.1.1"}
	gw := net.ParseIP("192.168.1.1")
	cand := func(ip, subnet, iface string, virtual bool) Candidate {
		_, n, _ := net.ParseCIDR(subnet)
		return Candidate{IP: net.ParseIP(ip).To4(), Iface: iface, Subnet: n, Virtual: virtual}
	}
	wifi := cand("192.168.1.20", "192.168.1.0/24", "wlp3s0", false)
	eth := cand("192.168.1.21", "192.168.1.0/24", "eth0", false)
	docker := cand("172.17.0.1", "172.17.0.0/16", "docker0", true)
	cafe := cand("10.1.2.3", "10.0.0.0/8", "wlp3s0", false)
	for _, c := range []struct {
		name  string
		iface string
		cands []Candidate
		gw    net.IP
		where string
		ip    string
	}{
		{"home", "wlp3s0", []Candidate{wifi}, gw, NetHome, "192.168.1.20"},
		{"home, the chosen interface first", "wlp3s0", []Candidate{eth, wifi}, gw, NetHome, "192.168.1.20"},
		{"home on another interface", "wlp3s0", []Candidate{eth}, gw, NetHome, "192.168.1.21"},
		{"nothing", "wlp3s0", nil, nil, NetNone, ""},
		{"only virtual networks", "wlp3s0", []Candidate{docker}, nil, NetNone, ""},
		{"cafe", "wlp3s0", []Candidate{cafe, docker}, net.ParseIP("10.0.0.1"), NetAway, ""},
		{"home subnet, other gateway", "wlp3s0", []Candidate{wifi}, net.ParseIP("192.168.1.254"), NetAway, ""},
		{"home subnet on a virtual interface", "wlp3s0", []Candidate{cand("192.168.1.5", "192.168.1.0/24", "tun0", true)}, gw, NetNone, ""},
		{"a virtual interface the user chose", "tun0", []Candidate{cand("192.168.1.5", "192.168.1.0/24", "tun0", true)}, gw, NetHome, "192.168.1.5"},
	} {
		where, got := Locate(home, c.iface, c.cands, c.gw)
		if where != c.where || (c.ip != "" && got.IP.String() != c.ip) {
			t.Errorf("%s: %s %v", c.name, where, got.IP)
		}
	}
}

// R6: a leaf that names the bound address stays when another interface
// comes up; a new bound address or the name reissues it.
func TestEnsureForKeepsLeaf(t *testing.T) {
	now := time.Now()
	c, err := LoadCerts(t.TempDir(), "terra", now)
	if err != nil {
		t.Fatal(err)
	}
	lo := net.IPv4(127, 0, 0, 1).To4()
	bound := net.IPv4(192, 168, 1, 20).To4()
	dns := []string{"terra.local"}
	if re, err := c.EnsureFor([]net.IP{bound, lo}, []net.IP{bound, lo}, dns, now); err != nil || !re {
		t.Fatalf("first leaf: %v %v", re, err)
	}
	extra := net.IPv4(10, 0, 0, 5).To4()
	if re, _ := c.EnsureFor([]net.IP{bound, lo}, []net.IP{bound, extra, lo}, dns, now); re {
		t.Fatal("a new interface reissued the leaf")
	}
	moved := net.IPv4(192, 168, 1, 33).To4()
	if re, _ := c.EnsureFor([]net.IP{moved, lo}, []net.IP{moved, extra, lo}, dns, now); !re {
		t.Fatal("a new bound address kept the leaf")
	}
	if l := c.Leaf().Leaf; len(l.IPAddresses) != 3 {
		t.Fatalf("the new leaf names %v", l.IPAddresses)
	}
}
