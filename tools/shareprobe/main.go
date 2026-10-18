// Command shareprobe is the Phase 0 device probe for picmover's LAN
// sharing (docs/design/lan-share.md, "Phase 0 probe"). It is a throwaway
// developer tool, not part of picmover: it serves a self-checking test
// page over HTTPS with a name-constrained local CA, so the browser
// behaviours the design marks "unverified" can be measured on real
// phones (iOS Safari, Android Chrome, Sailfish Browser).
//
//	go build -tags nodynamic ./tools/shareprobe && ./shareprobe
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	var (
		ipFlag   = flag.String("ip", "", "LAN IPv4 address to listen on (default: the address of the default route)")
		port     = flag.Int("port", 7443, "HTTPS port; port+1 serves the name-constraint test certificate")
		dir      = flag.String("dir", filepath.Join(os.TempDir(), "picmover-shareprobe"), "directory for the CA, certificates, clips and results (never a library)")
		hostFlag = flag.String("hostname", "", "host name for the .local URL (default: this computer's name)")
		ffmpeg   = flag.String("ffmpeg", "", "ffmpeg binary for the test clips (default: ffmpeg from PATH)")
		extra    = flag.String("clips", "", "optional directory of your own videos (e.g. an iPhone .mov) to test as well")
		verbose  = flag.Bool("v", false, "print every page load and media request")
		invert   = flag.Bool("qr-invert", false, "invert the terminal QR code (for light terminal themes)")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: shareprobe [flags]\n\nPhase 0 phone probe for picmover LAN sharing (see docs/design/lan-share.md).\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	log.SetFlags(0)

	ip := net.ParseIP(*ipFlag).To4()
	if *ipFlag == "" {
		var err error
		if ip, err = defaultLANIP(); err != nil {
			log.Fatalf("shareprobe: %v", err)
		}
	} else if ip == nil {
		log.Fatalf("shareprobe: -ip %q is not an IPv4 address", *ipFlag)
	}
	hostname := *hostFlag
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	local := localName(hostname)
	if *ffmpeg == "" {
		*ffmpeg, _ = exec.LookPath("ffmpeg")
	}
	absDir, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(absDir, 0o700); err != nil {
		log.Fatalf("shareprobe: %v", err)
	}
	if _, err := os.Stat(filepath.Join(absDir, ".picmover")); err == nil {
		log.Fatalf("shareprobe: %s looks like a picmover library; use another -dir", absDir)
	}

	certs, notes, err := loadOrCreateCerts(absDir, ip, hostname, time.Now())
	if err != nil {
		log.Fatalf("shareprobe: certificates: %v", err)
	}
	for _, n := range notes {
		fmt.Println("·", n)
	}
	fmt.Println("· preparing test clips (first run takes a few seconds)…")
	ready, clipNotes := makeClips(context.Background(), *ffmpeg, filepath.Join(absDir, "clips"))
	for _, n := range clipNotes {
		fmt.Println("·", n)
	}

	base := "https://" + ip.String() + ":" + strconv.Itoa(*port)
	urls := pageURLs{IP: base, Outside: "https://" + ip.String() + ":" + strconv.Itoa(*port+1)}
	if local != "" {
		urls.Local = "https://" + local + ":" + strconv.Itoa(*port)
	}
	p, err := newProbe(absDir, certs, urls, ready, *extra, os.Stdout)
	if err != nil {
		log.Fatalf("shareprobe: %v", err)
	}
	p.verbose = *verbose
	defer p.Close()

	h := p.handler()
	errc := make(chan error, 2)
	var servers []*http.Server
	for i, c := range []tls.Certificate{certs.Leaf, certs.Outside} {
		addr := net.JoinHostPort(ip.String(), strconv.Itoa(*port+i))
		ln, err := net.Listen("tcp4", addr)
		if err != nil {
			log.Fatalf("shareprobe: %v (is another probe or picmover running?)", err)
		}
		srv := &http.Server{
			Handler:           h,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       2 * time.Minute,
			TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{c}},
			ErrorLog:          log.New(tlsErrFilter{verbose: *verbose}, "", 0),
		}
		servers = append(servers, srv)
		go func() { errc <- srv.ServeTLS(ln, "", "") }()
	}

	pairIP := base + "/pair#" + p.token
	pairLocal := ""
	if local != "" {
		pairLocal = urls.Local + "/pair#" + p.token
	}
	printBanner(bannerInfo{
		ip: ip, port: *port, dir: absDir, base: base, local: urls.Local, outside: urls.Outside,
		pairIP: pairIP, pairLocal: pairLocal, code: p.code, invert: *invert, certs: certs,
		clips: len(ready), results: p.resultsPath(),
	})

	// Enter reprints the QR codes.
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			printQR(pairIP, "IP address", *invert)
			if pairLocal != "" {
				printQR(pairLocal, ".local name", *invert)
			}
			fmt.Printf("Code: %s\n", p.code)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case s := <-sig:
		fmt.Println("\nstopping on", s)
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("shareprobe: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(ctx)
	}
	fmt.Println("results are in", p.resultsPath())
}

// tlsErrFilter hides the handshake errors every certificate warning
// causes ("remote error: tls: unknown certificate"), unless -v.
type tlsErrFilter struct{ verbose bool }

func (f tlsErrFilter) Write(b []byte) (int, error) {
	s := string(b)
	if !f.verbose && strings.Contains(s, "TLS handshake error") {
		return len(b), nil
	}
	return os.Stderr.Write(b)
}

func printQR(text, label string, invert bool) {
	q, err := qrTerminal(text)
	if err != nil {
		fmt.Println("QR:", err)
		return
	}
	if invert {
		q = strings.Map(func(r rune) rune {
			switch r {
			case '█':
				return ' '
			case ' ':
				return '█'
			case '▀':
				return '▄'
			case '▄':
				return '▀'
			}
			return r
		}, q)
	}
	fmt.Printf("\nPair (%s): %s\n%s", label, text, q)
}

type bannerInfo struct {
	ip                               net.IP
	port                             int
	dir, base, local, outside        string
	pairIP, pairLocal, code, results string
	invert                           bool
	certs                            *certSet
	clips                            int
}

func printBanner(b bannerInfo) {
	ifname, subnet := subnetOf(b.ip)
	sub := b.ip.String() + "/24"
	if subnet != nil {
		sub = subnet.String()
	}
	ports := fmt.Sprintf("%d:%d", b.port, b.port+1)
	portsDash := fmt.Sprintf("%d-%d", b.port, b.port+1)

	fmt.Printf(`
picmover share probe (Phase 0) — serving on %s (%s)
  test page      %s/
  .local page    %s/
  constraint     %s/   (certificate that also names %s; see item 10)
  CA download    %s/ca.crt   (DER)   %s/ca.pem   (PEM)
  CA SHA-256     %s
  leaf SHA-256   %s
  test clips     %d
  data dir       %s
  results        %s   (one JSON line per report; media requests in media.log)
`, b.ip, ifname, b.base, orNone(b.local), b.outside, outsideName, b.base, b.base,
		fingerprint(b.certs.CA.Raw), fingerprint(b.certs.Leaf.Certificate[0]), b.clips, b.dir, b.results)

	if others := otherAddrs(b.ip); len(others) > 0 {
		fmt.Printf("  other private addresses here: %s\n  (if the phones' Wi-Fi is on one of those networks, restart with -ip ADDRESS)\n", strings.Join(others, ", "))
	}

	fmt.Printf(`
FIREWALL — open the ports for this run only (nothing is changed by the probe):
  ufw:        sudo ufw allow from %[1]s to any port %[2]s proto tcp
              afterwards: sudo ufw delete allow from %[1]s to any port %[2]s proto tcp
  firewalld:  sudo firewall-cmd --get-active-zones        (find the Wi-Fi/LAN zone)
              sudo firewall-cmd --zone=ZONE --add-port=%[3]s/tcp   (runtime only; gone after reload/reboot)
              afterwards: sudo firewall-cmd --zone=ZONE --remove-port=%[3]s/tcp
  check:      sudo ufw status  /  sudo firewall-cmd --list-all --zone=ZONE
`, sub, strings.ReplaceAll(ports, ":", ","), portsDash)

	fmt.Printf(`
ON EACH PHONE (same Wi-Fi):
  1. Scan the QR below (iPhone: Camera app; Android: Camera or Lens). Sailfish,
     or any phone without a scanner: type this short URL into the browser —
     it tests the fragment the same way:  %s/pair#%s
  2. Accept the certificate warning ("Show Details → visit this website" on
     iPhone; "Advanced → Proceed" on Android; Sailfish: whatever it offers).
     Compare the leaf fingerprint above if the browser shows it.
  3. The page pairs, reloads itself once and runs the checklist (about 20 s).
     Tap each video's play button if it did not start; tap "Download original".
  4. Do the swipe test (item 8), then follow the page's "later" steps:
     close the browser completely, reopen it, open %s/ again
     and answer the two questions (items 2 and 7). Repeat after a day or a week.
  5. Optional (item 10): install the CA from %s/ca.crt, then open
     %s/ — a phone that enforces name constraints must still warn there.
  Every report is appended to the results file and summarised below.
  Press Enter to show the QR codes again; Ctrl+C to stop.
`, b.base, b.code, b.base, b.base, b.outside)

	printQR(b.pairIP, "IP address", b.invert)
	if b.pairLocal != "" {
		printQR(b.pairLocal, ".local name", b.invert)
	}
	fmt.Printf("Code (if the token is lost on the way): %s\n\n", b.code)
	if png, err := qrPNG(b.pairIP, 8); err == nil {
		qrFile := filepath.Join(b.dir, "pair-qr.png")
		if writeAtomic(qrFile, png, 0o600) == nil {
			fmt.Printf("(the same QR as an image: %s)\n\n", qrFile)
		}
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none: the host name is not a valid DNS label)"
	}
	return s
}

// otherAddrs lists the computer's other private IPv4 addresses.
func otherAddrs(ip net.IP) []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil || !n.IP.IsPrivate() || n.IP.Equal(ip) {
				continue
			}
			out = append(out, n.IP.String()+" ("+ifc.Name+")")
		}
	}
	return out
}
