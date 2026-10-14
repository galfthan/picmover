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
	sharePaused = "paused" // another network (resumed on the Share page), or waiting for the home network
	shareError  = "error"  // could not start (busy port, no LAN address)
)

// Reasons of the paused state.
const (
	// reasonNetChanged: the computer is on another network; only the
	// user resumes ("Resume on this network" records it as home).
	reasonNetChanged = "network changed"
	// reasonWaiting: no LAN address (Wi-Fi down, just woken up); sharing
	// resumes by itself when the home network is back.
	reasonWaiting = "waiting for the home network"
	// reasonNoHome: "Keep sharing on" at launch, but sharing was never
	// turned on by hand, so no home network is known.
	reasonNoHome = "turn sharing on once on the Share page to record the home network"
)

const (
	// shareWatchEvery is how often the network is checked.
	shareWatchEvery = 15 * time.Second
	// shareExpiryEvery is how often share expiry is checked for the log.
	shareExpiryEvery = time.Minute
	// shareDrain bounds the share listener's shutdown.
	shareDrain = 5 * time.Second
)

// shareConnLimits are the share listener's connection limits (tests
// change them).
var shareConnLimits = share.DefaultConnLimits

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
	waiting bool // paused, and resumes by itself when the home network is back
	bound   share.Candidate
	port    int
	// portOnce is serve --share-port: the port of this run, never saved
	// (0: the saved port). A port set on the Share page replaces it.
	portOnce int
	httpSrv  *http.Server
	ln       net.Listener  // httpSrv's listener
	served   chan struct{} // closed when httpSrv's Serve returned
	closing  *atomic.Bool  // set when httpSrv's listener is closed on purpose
	stop     chan struct{} // ends the watcher
	autoOff  *time.Timer
	autoAt   time.Time // when auto-off turns sharing off; zero: never
	// gen changes whenever sharing stops or the auto-off timer is set: a
	// timer that fired for an earlier session does nothing.
	gen     uint64
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
	waiting       bool
	bound         share.Candidate
	port          int
	autoAt        time.Time
}

func (sh *sharing) snapshot() shareSnapshot {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return shareSnapshot{state: sh.state, reason: sh.reason, waiting: sh.waiting, bound: sh.bound, port: sh.port,
		autoAt: sh.autoAt}
}

// url is the phone's URL of the bound address ("" while not listening).
func (h *shareHost) url() string {
	if h == nil {
		return ""
	}
	return "https://" + net.JoinHostPort(h.ip, h.port) + "/"
}

// unlockDrain unlocks sh.mu, then waits for the listener closed under it
// to drain (drain may be nil): status polls, the tray and the auto-off
// timer never wait for a phone's download to end.
func (sh *sharing) unlockDrain(drain func()) {
	sh.mu.Unlock()
	if drain != nil {
		drain()
	}
}

// Start turns sharing on by the user's hand (the Share page, the tray,
// serve --share), or resumes it after a network change: it takes
// share.lock, picks the LAN address, records the current network as the
// home network, makes sure the certificate fits, and listens. A busy port
// or a missing LAN address leave sharing in state error (returned as the
// error too); another process sharing is an *share.ErrManaged.
func (sh *sharing) Start() error { return sh.start(true) }

// StartKept turns sharing on at the app's launch ("Keep sharing on"),
// never recording a home network: on the saved home network it listens;
// on another network it is paused until the user resumes it; with no LAN
// address yet (Wi-Fi not up) it waits, and listens once the home network
// is there. Without a saved home network it is paused.
func (sh *sharing) StartKept() error { return sh.start(false) }

func (sh *sharing) start(byHand bool) error {
	sh.mu.Lock()
	var drain func()
	defer func() { sh.unlockDrain(drain) }()
	if sh.state == shareOn || (!byHand && sh.state == sharePaused) {
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
	cfg, err := m.Snapshot()
	if err != nil {
		m.Release()
		return sh.failLocked(err)
	}
	if byHand {
		err = sh.listenHereLocked(m, cfg)
	} else {
		err = sh.resumeLocked(m, cfg)
	}
	if err != nil {
		m.Release()
		return err
	}
	sh.startTimersLocked(cfg)
	return nil
}

// failLocked records state error with err as the reason.
func (sh *sharing) failLocked(err error) error {
	sh.state, sh.reason, sh.waiting = shareError, err.Error(), false
	sh.log.Warn("sharing on Wi-Fi could not start", "err", err)
	return err
}

// errPortBusy is the reason for a port in use.
type errPortBusy struct{ port int }

func (e *errPortBusy) Error() string {
	return fmt.Sprintf("Port %d is in use by another program; choose another port on the Share page", e.port)
}

// errNoAddress: no LAN address to share on.
var errNoAddress = errors.New("no Wi-Fi or LAN address found; connect this computer to the home network")

// listenHereLocked is the user's start: the chosen address, the current
// network recorded as home, then listen. On failure the state is error.
func (sh *sharing) listenHereLocked(m *share.Manager, cfg *share.Config) error {
	cands, err := sh.netif.Candidates()
	if err != nil {
		return sh.failLocked(err)
	}
	cand, ok := share.Choose(cands, cfg.Interface, sh.netif.DefaultIP())
	if !ok {
		return sh.failLocked(errNoAddress)
	}
	h := share.Home{Subnet: cand.Subnet.String()}
	if gw := sh.netif.Gateway(); gw != nil {
		h.Gateway = gw.String()
	}
	if err := m.Update(func(c *share.Config, _ time.Time) error { c.Home = &h; return nil }); err != nil {
		return sh.failLocked(err)
	}
	return sh.listenOnLocked(m, cfg, cands, cand)
}

// resumeLocked starts without the user: listen on the saved home network,
// else paused (another network, or no home network known) or waiting (no
// LAN address). Only a failure to listen is an error.
func (sh *sharing) resumeLocked(m *share.Manager, cfg *share.Config) error {
	if cfg.Home == nil {
		sh.pauseLocked(reasonNoHome, false)
		return nil
	}
	cands, _ := sh.netif.Candidates()
	switch where, cand := share.Locate(*cfg.Home, cfg.Interface, cands, sh.netif.Gateway()); where {
	case share.NetHome:
		return sh.listenOnLocked(m, cfg, cands, cand)
	case share.NetAway:
		sh.pauseLocked(reasonNetChanged, false)
	default:
		sh.pauseLocked(reasonWaiting, true)
	}
	return nil
}

// listenOnLocked ensures the leaf fits cand and starts the HTTPS server
// on it. On failure the state is error.
func (sh *sharing) listenOnLocked(m *share.Manager, cfg *share.Config, cands []share.Candidate, cand share.Candidate) error {
	certs, err := sh.loadCerts(m)
	if err != nil {
		return sh.failLocked(err)
	}
	if _, err := certs.EnsureFor(leafNeed(cand), leafIPs(cands, cand), leafDNS(sh.hostname), sh.now()); err != nil {
		return sh.failLocked(err)
	}
	want := cfg.Port
	if sh.portOnce != 0 {
		want = sh.portOnce
	}
	ln, err := sh.listen(net.JoinHostPort(cand.IP.String(), strconv.Itoa(want)))
	if err != nil {
		if isAddrInUse(err) {
			return sh.failLocked(&errPortBusy{port: want})
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
	fl := share.FilterListener(ln, share.AllowFrom(cand.Subnet), shareConnLimits)
	closing := &atomic.Bool{}
	go func() {
		defer close(done)
		// ServeTLS sets up HTTP/2 on the TLS config (GetCertificate
		// supplies the leaf, so no files are named).
		if err := srv.ServeTLS(fl, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) && !closing.Load() {
			sh.log.Error("share listener stopped", "err", err)
		}
	}()
	sh.httpSrv, sh.ln, sh.served, sh.closing = srv, fl, done, closing
	sh.state, sh.reason, sh.waiting = shareOn, "", false
	leaf, _ := certs.Fingerprints()
	sh.log.Info("Sharing on Wi-Fi at "+sh.host.Load().url(), "certificate", leaf)
	return nil
}

// isAddrInUse reports a listen error for a busy port.
func isAddrInUse(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "address already in use") ||
		strings.Contains(err.Error(), "Only one usage of each socket address"))
}

// leafIPs are the IP SANs of a new leaf: the bound address, every
// non-virtual candidate, and 127.0.0.1.
func leafIPs(cands []share.Candidate, bound share.Candidate) []net.IP {
	out := []net.IP{bound.IP}
	for _, c := range cands {
		if !c.Virtual && !c.IP.Equal(bound.IP) {
			out = append(out, c.IP)
		}
	}
	return append(out, net.IPv4(127, 0, 0, 1).To4())
}

// leafNeed are the IP SANs the current leaf must have to stay: the bound
// address and 127.0.0.1 (another interface coming up does not reissue).
func leafNeed(bound share.Candidate) []net.IP {
	return []net.IP{bound.IP, net.IPv4(127, 0, 0, 1).To4()}
}

// leafDNS is the certificate's DNS SAN: <hostname>.local when valid.
func leafDNS(hostname string) []string {
	if n := share.LocalName(hostname); n != "" {
		return []string{n}
	}
	return nil
}

// closeServerLocked stops the HTTPS server: the listener closes at once
// (the port is free for a new one), and the returned drain waits up to
// shareDrain for the requests in flight. Callers run drain after
// unlocking sh.mu (unlockDrain).
func (sh *sharing) closeServerLocked() (drain func()) {
	sh.host.Store(nil)
	srv, served, ln, closing := sh.httpSrv, sh.served, sh.ln, sh.closing
	sh.httpSrv, sh.served, sh.ln, sh.closing = nil, nil, nil, nil
	if srv == nil {
		return nil
	}
	closing.Store(true)
	ln.Close()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), shareDrain)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			srv.Close()
		}
		<-served
	}
}

// startTimersLocked starts the network watcher and the auto-off timer.
func (sh *sharing) startTimersLocked(cfg *share.Config) {
	if sh.stop == nil {
		sh.stop = make(chan struct{})
		go sh.watch(sh.stop, sh.watchEvery)
	}
	sh.setAutoOffLocked(cfg.AutoOff)
}

// autoOffAfter is how long sharing stays on per auto-off mode (tests
// shorten it).
var autoOffAfter = map[string]time.Duration{share.AutoOff1h: time.Hour, share.AutoOff4h: 4 * time.Hour}

// setAutoOffLocked (re)starts the auto-off timer for mode. A timer of an
// earlier setting or session that fires late does nothing (gen).
func (sh *sharing) setAutoOffLocked(mode string) {
	if sh.autoOff != nil {
		sh.autoOff.Stop()
		sh.autoOff = nil
	}
	sh.gen++
	sh.autoAt = time.Time{}
	d, ok := autoOffAfter[mode]
	if !ok {
		return // never, quit: on until turned off or picmover quits
	}
	gen := sh.gen
	sh.autoAt = sh.now().Add(d)
	sh.autoOff = time.AfterFunc(d, func() {
		sh.mu.Lock()
		var drain func()
		defer func() { sh.unlockDrain(drain) }()
		if sh.gen != gen {
			return
		}
		sh.log.Info("Sharing on Wi-Fi turned off (auto-off)")
		drain = sh.stopLocked()
	})
}

// Stop turns sharing off: the listener closes, pairings are dropped,
// device activity is saved and share.lock released.
func (sh *sharing) Stop() {
	sh.mu.Lock()
	sh.unlockDrain(sh.stopLocked())
}

// stopLocked turns sharing off; the caller runs the returned drain after
// unlocking.
func (sh *sharing) stopLocked() (drain func()) {
	wasOn := sh.state == shareOn || sh.state == sharePaused
	sh.pairs.Reset() // first: a redeem waiting for approval ends now
	drain = sh.closeServerLocked()
	if sh.stop != nil {
		close(sh.stop)
		sh.stop = nil
	}
	if sh.autoOff != nil {
		sh.autoOff.Stop()
		sh.autoOff = nil
	}
	sh.gen++
	sh.autoAt = time.Time{}
	if sh.mgr != nil {
		sh.mgr.Release()
	}
	sh.state, sh.reason, sh.waiting = shareOff, "", false
	if wasOn {
		sh.log.Info("Sharing on Wi-Fi is off")
	}
	return drain
}

// restart re-listens with the current settings (address or port changed)
// when sharing is on. The user changed them, so the current network is
// recorded as home.
func (sh *sharing) restart() error {
	sh.mu.Lock()
	var drain func()
	defer func() { sh.unlockDrain(drain) }()
	if sh.state != shareOn {
		return nil
	}
	drain = sh.closeServerLocked()
	cfg, err := sh.mgr.Snapshot()
	if err == nil {
		err = sh.listenHereLocked(sh.mgr, cfg)
	} else {
		sh.failLocked(err)
	}
	if err != nil {
		state, reason := sh.state, sh.reason
		sh.stopLocked() // the server is closed already: nothing to drain
		sh.state, sh.reason = state, reason
		return err
	}
	return nil
}

// setPortOnce sets the port of this run (serve --share-port), not saved.
func (sh *sharing) setPortOnce(port int) {
	sh.mu.Lock()
	sh.portOnce = port
	sh.mu.Unlock()
}

// setMDNS applies the .local name setting to the running listener: the
// name is accepted (or no longer) at once, without listening again (the
// leaf always names it).
func (sh *sharing) setMDNS(on bool) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	h := sh.host.Load()
	if h == nil {
		return
	}
	next := *h
	next.local = ""
	if on {
		next.local = share.LocalName(sh.hostname)
	}
	sh.host.Store(&next)
}

// watch checks the network every watchEvery and share expiry every
// minute until stop is closed.
func (sh *sharing) watch(stop chan struct{}, every time.Duration) {
	t := time.NewTicker(every)
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

// checkNetwork compares the network with the home network. While on: the
// same address stays (the leaf is renewed when near expiry or when it no
// longer names the address); another address on the home network
// rebinds; no LAN address at all waits (paused, resuming by itself);
// another network pauses until the user resumes. While waiting: the home
// network back listens again; another network pauses for good.
func (sh *sharing) checkNetwork() {
	cands, err := sh.netif.Candidates()
	if err != nil {
		cands = nil
	}
	gw := sh.netif.Gateway()
	sh.mu.Lock()
	var drain func()
	defer func() { sh.unlockDrain(drain) }()
	on := sh.state == shareOn
	if (!on && !(sh.state == sharePaused && sh.waiting)) || sh.mgr == nil {
		return
	}
	cfg, err := sh.mgr.Snapshot()
	if err != nil || cfg.Home == nil {
		return
	}
	if on {
		if verdict, _ := share.Compare(*cfg.Home, sh.bound.Iface, sh.bound.IP, cands, gw); verdict == share.NetSame {
			if _, err := sh.certs.EnsureFor(leafNeed(sh.bound), leafIPs(cands, sh.bound), leafDNS(sh.hostname), sh.now()); err != nil {
				sh.log.Error("cannot renew the sharing certificate", "err", err)
			}
			return
		}
	}
	iface := sh.bound.Iface
	if iface == "" {
		iface = cfg.Interface
	}
	where, next := share.Locate(*cfg.Home, iface, cands, gw)
	switch where {
	case share.NetHome:
		old := ""
		if on {
			old = sh.bound.IP.String()
			drain = sh.closeServerLocked()
		}
		if err := sh.listenOnLocked(sh.mgr, cfg, cands, next); err != nil {
			sh.pauseLocked("sharing could not listen on "+next.IP.String()+": "+err.Error(), true)
			return
		}
		switch {
		case old == "":
			sh.log.Info("Sharing on Wi-Fi resumed: the home network is back")
		case old != sh.bound.IP.String():
			sh.log.Warn("Sharing moved from "+old+" to "+sh.host.Load().url()+"; phones must pair again at the new address",
				"address", sh.bound.IP.String())
		}
	case share.NetAway:
		if on {
			drain = sh.closeServerLocked()
		}
		sh.pauseLocked(reasonNetChanged, false)
	default:
		if on {
			drain = sh.closeServerLocked()
			sh.pauseLocked(reasonWaiting, true)
		}
	}
}

// pauseLocked records the paused state (the listener must be closed
// already), keeping share.lock: waiting resumes by itself on the home
// network, otherwise the Share page resumes it.
func (sh *sharing) pauseLocked(reason string, waiting bool) {
	sh.pairs.Reset()
	if sh.state == sharePaused && sh.reason == reason && sh.waiting == waiting {
		return
	}
	sh.state, sh.reason, sh.waiting = sharePaused, reason, waiting
	sh.log.Info("Sharing on Wi-Fi paused: " + reason)
}

// checkExpiry logs shares that ended since the last check, and sweeps
// the settings (share.Manager.Sweep).
func (sh *sharing) checkExpiry() {
	m := sh.mgr
	if m == nil {
		return
	}
	m.Sweep() // shares ended 30 days ago, phones unused for 400 days
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
