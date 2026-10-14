package share

import "testing"

func TestParseProcRoute(t *testing.T) {
	table := `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
wlp3s0	0001A8C0	00000000	0001	0	0	600	00FFFFFF	0	0	0
wlp3s0	00000000	0101A8C0	0003	0	0	600	00000000	0	0	0
`
	ip, err := parseProcRoute(table)
	if err != nil || ip.String() != "192.168.1.1" {
		t.Fatalf("gateway %v %v", ip, err)
	}
	if _, err := parseProcRoute("Iface\tDestination\n"); err == nil {
		t.Fatal("found a gateway in an empty table")
	}
}
