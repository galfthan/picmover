package server

// Sharing on the home Wi-Fi (docs/design/lan-share.md): the share
// listener's lifecycle (on, off, paused on a network change, error) and
// the state it keeps. The admin routes that manage it are in
// share_admin.go, the phone's routes in share_routes.go and share_api.go.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"picmover/internal/share"
)

// Sharing states (GET /api/share state).
const (
	shareOff    = "off"
	shareOn     = "on"
	sharePaused = "paused" // the network changed; resumed on the Share page
	shareError  = "error"  // could not start (busy port, no LAN address)
)

const (
	// shareWatchEvery is how often the network is checked.
	shareWatchEvery = 15 * time.Second
	// shareExpiryEvery is how often share expiry is checked for the log.
	shareExpiryEvery = time.Minute
	// shareDrain bounds the share listener's shutdown.
	shareDrain = 5 * time.Second
)

// errAdminNotLoopback: sharing needs the admin listener on loopback.
var errAdminNotLoopback = errors.New("sharing needs the admin listener on a loopback address (the default --addr); the shared view has its own listener")

// shareHost is what the share listener answers to: the bound IP and
// port, and <hostname>.local when the mDNS name is on.
type shareHost struct {
	ip, port, local string
}

// sharing is the share listener of a Server and the state behind it.
// Everything persistent is in share.Manager (share.json); pairings, rate
// limits and the listener live here, in memory.
type sharing struct {
	s   *Server
	log *slog.Logger
	cmd string // names this process in share.lock ("serve", "picmover-app")
	dir string // the share directory; "" = share.DefaultDir()

	// Test hooks.
	netif      share.Netif
	listen     func(addr string) (net.Listener, error)
	now        func() time.Time
	hostname   string
	watchEvery time.Duration

	initMu  sync.Mutex
	mgr     *share.Manager // opened on first use
	certs   *share.Certs   // loaded when sharing starts
	handler http.Handler   // the phone's handler (shareMiddleware + share mux)

	pairs     *share.Pairings
	pairIP    *share.Limiter // POST /api/pair per remote IP
	pairAll   *share.Limiter // POST /api/pair in total
	authFail  *share.Lockout // failed authentication per remote IP
	perDevice *share.Limiter // authenticated API requests per device
	// perDeviceMedia: renditions and originals per device (a grid loads a
	// month of thumbnails at once over HTTP/2).
	perDeviceMedia *share.Limiter

	host atomic.Pointer[shareHost] // nil while not listening

	mu      sync.Mutex // serialises start and stop; guards the fields below
	state   string
	reason  string
	bound   share.Candidate
	port    int
	httpSrv *http.Server
	served  chan struct{} // closed when httpSrv's Serve returned
	stop    chan struct{} // ends the watcher
	autoOff *time.Timer
	autoAt  time.Time       // when auto-off turns sharing off; zero: never
	expired map[string]bool // shares whose expiry was logged
}

// newSharing returns the sharing of s, off.
func newSharing(s *Server, dir, cmd string) *sharing {
	host, _ := os.Hostname()
	if cmd == "" {
		cmd = "serve"
	}
	sh := &sharing{s: s, log: s.log, cmd: cmd, dir: dir, netif: share.System, now: time.Now, hostname: host,
		watchEvery: shareWatchEvery, state: shareOff, expired: map[string]bool{}}
	sh.listen = func(addr string) (net.Listener, error) { return net.Listen("tcp4", addr) }
	sh.pairs = share.NewPairings(sh.clock)
	sh.pairIP = share.PerMinute(5, sh.clock)
	sh.pairAll = share.PerMinute(60, sh.clock) // well-formed codes only
	sh.authFail = share.NewLockout(30, 5*time.Minute, sh.clock)
	sh.perDevice = share.NewLimiter(30, 100, sh.clock)
	sh.perDeviceMedia = share.NewLimiter(200, 500, sh.clock)
	return sh
}

// clock is the current time from the (test-replaceable) clock.
func (sh *sharing) clock() time.Time { return sh.now() }

// manager opens the share directory on first use (it is created 0700).
func (sh *sharing) manager() (*share.Manager, error) {
	sh.initMu.Lock()
	defer sh.initMu.Unlock()
	if sh.mgr != nil {
		return sh.mgr, nil
	}
	dir := sh.dir
	if dir == "" {
		d, err := share.DefaultDir()
		if err != nil {
			return nil, fmt.Errorf("no per-user configuration directory: %w", err)
		}
		dir = d
	}
	m, err := share.Open(dir, sh.log)
	if err != nil {
		return nil, err
	}
	m.SetClock(sh.clock)
	sh.mgr = m
	return m, nil
}

// loadCerts loads (creating the CA when missing) the certificates.
func (sh *sharing) loadCerts(m *share.Manager) (*share.Certs, error) {
	sh.initMu.Lock()
	defer sh.initMu.Unlock()
	if sh.certs != nil {
		return sh.certs, nil
	}
	c, err := share.LoadCerts(m.Dir(), sh.hostname, sh.now())
	if err != nil {
		return nil, err
	}
	sh.certs = c
	return c, nil
}

// adminLoopback reports whether the admin listener is on loopback.
func (sh *sharing) adminLoopback() bool {
	return IsLoopbackAddr(net.JoinHostPort(sh.s.guard.host, sh.s.guard.port))
}

// snapshot is the sharing state for the status JSON.
type shareSnapshot struct {
	state, reason string
	bound         share.Candidate
	port          int
	autoAt        time.Time
}

func (sh *sharing) snapshot() shareSnapshot {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return shareSnapshot{state: sh.state, reason: sh.reason, bound: sh.bound, port: sh.port, autoAt: sh.autoAt}
}

// url is the phone's URL of the bound address ("" while not listening).
func (h *shareHost) url() string {
	if h == nil {
		return ""
	}
	return "https://" + net.JoinHostPort(h.ip, h.port) + "/"
}

// Start turns sharing on (or resumes it after a network change): it takes
// share.lock, picks the LAN address, records the home network, makes sure
// the certificate fits, and listens. A busy port or a missing LAN address
// leave sharing in state error (returned as the error too); another
// process sharing is an *share.ErrManaged.
func (sh *sharing) Start() error {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.state == shareOn {
		return nil
	}
	if !sh.adminLoopback() {
		return errAdminNotLoopback
	}
	m, err := sh.manager()
	if err != nil {
		return sh.failLocked(err)
	}
	if err := m.Hold(sh.cmd); err != nil {
		var me *share.ErrManaged
		if errors.As(err, &me) {
			return err
		}
		return sh.failLocked(err)
	}
	if err := sh.listenLocked(m, true); err != nil {
		m.Release()
		return err
	}
	cfg, _ := m.Snapshot()
	sh.startTimersLocked(cfg)
	return nil
}

// failLocked records state error with err as the reason.
func (sh *sharing) failLocked(err error) error {
	sh.state, sh.reason = shareError, err.Error()
	sh.log.Warn("sharing on Wi-Fi could not start", "err", err)
	return err
}

// errPortBusy is the reason for a port in use.
type errPortBusy struct{ port int }

func (e *errPortBusy) Error() string {
	return fmt.Sprintf("Port %d is in use by another program; choose another port on the Share page", e.port)
}

// listenLocked chooses the address, records the home network when home
// is set, ensures the leaf and starts the HTTPS server. On failure the
// state is error.
func (sh *sharing) listenLocked(m *share.Manager, home bool) error {
	cfg, err := m.Snapshot()
	if err != nil {
		return sh.failLocked(err)
	}
	cands, err := sh.netif.Candidates()
	if err != nil {
		return sh.failLocked(err)
	}
	cand, ok := share.Choose(cands, cfg.Interface, sh.netif.DefaultIP())
	if !ok {
		return sh.failLocked(errors.New("no Wi-Fi or LAN address found; connect this computer to the home network"))
	}
	if home {
		h := share.Home{Subnet: cand.Subnet.String()}
		if gw := sh.netif.Gateway(); gw != nil {
			h.Gateway = gw.String()
		}
		if err := m.Update(func(c *share.Config, _ time.Time) error { c.Home = &h; return nil }); err != nil {
			return sh.failLocked(err)
		}
	}
	certs, err := sh.loadCerts(m)
	if err != nil {
		return sh.failLocked(err)
	}
	if _, err := certs.Ensure(leafIPs(cands, cand), leafDNS(sh.hostname), sh.now(), false); err != nil {
		return sh.failLocked(err)
	}
	ln, err := sh.listen(net.JoinHostPort(cand.IP.String(), strconv.Itoa(cfg.Port)))
	if err != nil {
		if isAddrInUse(err) {
			return sh.failLocked(&errPortBusy{port: cfg.Port})
		}
		return sh.failLocked(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	sh.port, _ = strconv.Atoi(port)
	sh.bound = cand
	local := ""
	if cfg.MDNS {
		local = share.LocalName(sh.hostname)
	}
	sh.host.Store(&shareHost{ip: cand.IP.String(), port: port, local: local})
	srv := &http.Server{
		Handler:           sh.handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		TLSConfig:         certs.TLSConfig(),
		// Phones that refuse the self-signed certificate end every first
		// handshake with an error: Debug only.
		ErrorLog: slog.NewLogLogger(sh.log.Handler(), slog.LevelDebug),
	}
	done := make(chan struct{})
	fl := share.FilterListener(ln, share.AllowFrom(cand.Subnet), share.MaxConns)
	go func() {
		defer close(done)
		// ServeTLS sets up HTTP/2 on the TLS config (GetCertificate
		// supplies the leaf, so no files are named).
		if err := srv.ServeTLS(fl, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			sh.log.Error("share listener stopped", "err", err)
		}
	}()
	sh.httpSrv, sh.served = srv, done
	sh.state, sh.reason = shareOn, ""
	leaf, _ := certs.Fingerprints()
	sh.log.Info("Sharing on Wi-Fi at "+sh.host.Load().url(), "certificate", leaf)
	return nil
}

// isAddrInUse reports a listen error for a busy port.
func isAddrInUse(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "address already in use") ||
		strings.Contains(err.Error(), "Only one usage of each socket address"))
}

// leafIPs are the certificate's IP SANs: every non-virtual candidate, the
// bound one, and 127.0.0.1.
func leafIPs(cands []share.Candidate, bound share.Candidate) []net.IP {
	out := []net.IP{bound.IP}
	for _, c := range cands {
		if !c.Virtual && !c.IP.Equal(bound.IP) {
			out = append(out, c.IP)
		}
	}
	return append(out, net.IPv4(127, 0, 0, 1).To4())
}

// leafDNS is the certificate's DNS SAN: <hostname>.local when valid.
func leafDNS(hostname string) []string {
	if n := share.LocalName(hostname); n != "" {
		return []string{n}
	}
	return nil
}

// closeServerLocked stops the HTTPS server (draining for shareDrain).
func (sh *sharing) closeServerLocked() {
	sh.host.Store(nil)
	if sh.httpSrv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), shareDrain)
	defer cancel()
	if err := sh.httpSrv.Shutdown(ctx); err != nil {
		sh.httpSrv.Close()
	}
	<-sh.served
	sh.httpSrv, sh.served = nil, nil
}

// startTimersLocked starts the network watcher and the auto-off timer.
func (sh *sharing) startTimersLocked(cfg *share.Config) {
	if sh.stop == nil {
		sh.stop = make(chan struct{})
		go sh.watch(sh.stop)
	}
	sh.setAutoOffLocked(cfg.AutoOff)
}

// autoOffAfter is how long sharing stays on per auto-off mode (tests
// shorten it).
var autoOffAfter = map[string]time.Duration{share.AutoOff1h: time.Hour, share.AutoOff4h: 4 * time.Hour}

// setAutoOffLocked (re)starts the auto-off timer for mode.
func (sh *sharing) setAutoOffLocked(mode string) {
	if sh.autoOff != nil {
		sh.autoOff.Stop()
		sh.autoOff = nil
	}
	sh.autoAt = time.Time{}
	d, ok := autoOffAfter[mode]
	if !ok {
		return // never, quit: on until turned off or picmover quits
	}
	sh.autoAt = sh.now().Add(d)
	sh.autoOff = time.AfterFunc(d, func() {
		sh.log.Info("Sharing on Wi-Fi turned off (auto-off)")
		sh.Stop()
	})
}

// Stop turns sharing off: the listener closes, pairings are dropped,
// device activity is saved and share.lock released.
func (sh *sharing) Stop() {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.stopLocked()
}

func (sh *sharing) stopLocked() {
	wasOn := sh.state == shareOn || sh.state == sharePaused
	sh.pairs.Reset() // first: a redeem waiting for approval ends now
	sh.closeServerLocked()
	if sh.stop != nil {
		close(sh.stop)
		sh.stop = nil
	}
	if sh.autoOff != nil {
		sh.autoOff.Stop()
		sh.autoOff = nil
	}
	sh.autoAt = time.Time{}
	if sh.mgr != nil {
		sh.mgr.Release()
	}
	sh.state, sh.reason = shareOff, ""
	if wasOn {
		sh.log.Info("Sharing on Wi-Fi is off")
	}
}

// restart re-listens with the current settings (address or port changed)
// when sharing is on.
func (sh *sharing) restart() error {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.state != shareOn {
		return nil
	}
	sh.closeServerLocked()
	if err := sh.listenLocked(sh.mgr, true); err != nil {
		sh.stopKeepState()
		return err
	}
	return nil
}

// stopKeepState releases what a failed restart holds, keeping state and
// reason (error).
func (sh *sharing) stopKeepState() {
	state, reason := sh.state, sh.reason
	sh.stopLocked()
	sh.state, sh.reason = state, reason
}

// watch checks the network every watchEvery and share expiry every
// minute until stop is closed.
func (sh *sharing) watch(stop chan struct{}) {
	t := time.NewTicker(sh.watchEvery)
	defer t.Stop()
	var lastExpiry time.Time
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		sh.checkNetwork()
		if now := sh.now(); now.Sub(lastExpiry) >= shareExpiryEvery {
			lastExpiry = now
			sh.checkExpiry()
		}
	}
}

// checkNetwork compares the network with the home network: the same
// address stays (the leaf is renewed when near expiry); a new address on
// the same subnet and gateway rebinds; anything else pauses sharing.
func (sh *sharing) checkNetwork() {
	cands, err := sh.netif.Candidates()
	if err != nil {
		cands = nil
	}
	gw := sh.netif.Gateway()
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.state != shareOn || sh.mgr == nil {
		return
	}
	cfg, err := sh.mgr.Snapshot()
	if err != nil || cfg.Home == nil {
		return
	}
	verdict, next := share.Compare(*cfg.Home, sh.bound.Iface, sh.bound.IP, cands, gw)
	switch verdict {
	case share.NetSame:
		if _, err := sh.certs.Ensure(leafIPs(cands, sh.bound), leafDNS(sh.hostname), sh.now(), false); err != nil {
			sh.log.Error("cannot renew the sharing certificate", "err", err)
		}
	case share.NetRebind:
		old := sh.bound.IP.String()
		sh.closeServerLocked()
		if err := sh.mgr.Update(func(c *share.Config, _ time.Time) error { c.Interface = next.Iface; return nil }); err != nil {
			sh.log.Error("cannot save the share settings", "err", err)
		}
		if err := sh.listenLocked(sh.mgr, false); err != nil {
			sh.pauseLocked("the address changed and sharing could not move to the new one: " + err.Error())
			return
		}
		sh.log.Info("Sharing moved from "+old+" to "+sh.host.Load().url()+"; phones must pair again at the new address",
			"address", sh.bound.IP.String())
	default:
		sh.pauseLocked("network changed")
	}
}

// pauseLocked closes the listener after a network change, keeping
// share.lock: the Share page resumes it.
func (sh *sharing) pauseLocked(reason string) {
	sh.pairs.Reset()
	sh.closeServerLocked()
	sh.state, sh.reason = sharePaused, reason
	sh.log.Info("Sharing on Wi-Fi paused: " + reason)
}

// checkExpiry logs shares that ended since the last check.
func (sh *sharing) checkExpiry() {
	m := sh.mgr
	if m == nil {
		return
	}
	cfg, err := m.Snapshot()
	if err != nil {
		return
	}
	now := sh.now()
	sh.mu.Lock()
	defer sh.mu.Unlock()
	for _, s := range cfg.Shares {
		if s.State(now) == share.StateExpired {
			if !sh.expired[s.ID] {
				sh.expired[s.ID] = true
				sh.pairs.DropShare(s.ID)
				sh.log.Info(fmt.Sprintf("Share %q has ended (expired)", s.Name))
			}
		} else {
			delete(sh.expired, s.ID)
		}
	}
}

// shutdown stops sharing for good (server shutdown).
func (sh *sharing) shutdown() { sh.Stop() }
