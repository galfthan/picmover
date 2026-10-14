package server

import (
	"crypto/tls"
	"net"
	"testing"
	"time"

	"picmover/internal/share"
)

// S3: idle connections from one address neither exhaust the listener for
// other addresses nor stay open: a silent connection is closed after the
// first-byte deadline.
func TestShareReviewConnExhaustion(t *testing.T) {
	old := shareConnLimits
	shareConnLimits = share.ConnLimits{Total: 64, PerIP: 8, FirstByte: 300 * time.Millisecond}
	defer func() { shareConnLimits = old }()
	f := newShareFixture(t)
	addr := f.phoneHost()
	from := func(ip string) *net.Dialer {
		return &net.Dialer{Timeout: time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}}
	}
	handshake := func(ip string) error {
		c, err := tls.DialWithDialer(from(ip), "tcp4", addr, &tls.Config{InsecureSkipVerify: true})
		if err == nil {
			c.Close()
		}
		return err
	}
	var held []net.Conn
	defer func() {
		for _, c := range held {
			c.Close()
		}
	}()
	for i := 0; i < 20; i++ {
		c, err := from("127.0.0.1").Dial("tcp4", addr)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	time.Sleep(50 * time.Millisecond)
	if err := handshake("127.0.0.2"); err != nil {
		t.Fatalf("another address was refused while one held its connections: %v", err)
	}
	// The silent connections are closed by the server.
	for _, c := range held[:8] {
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatal("a silent connection got data")
		}
	}
	time.Sleep(50 * time.Millisecond)
	if err := handshake("127.0.0.1"); err != nil {
		t.Fatalf("the address's slots were not freed: %v", err)
	}
}
