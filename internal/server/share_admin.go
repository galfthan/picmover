package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"picmover/internal/share"
)

// The admin side of sharing: /api/share/* on the admin listener
// (docs/design/lan-share.md, "Admin API"). These routes manage the LAN
// exposure, so beyond the admin checks they answer 403 unless the admin
// listener and the request's peer are loopback.

// shareAdminRoutes registers the management routes on the admin mux.
func (s *Server) shareAdminRoutes(handle func(string, func(http.ResponseWriter, *http.Request))) {
	h := func(pattern string, f func(http.ResponseWriter, *http.Request)) { handle(pattern, s.shareLocal(f)) }
	h("GET /api/share", s.handleShareGet)
	h("GET /api/share/status", s.handleShareStatus)
	h("POST /api/share", s.handleShareSet)
	h("POST /api/share/cert/renew", s.handleShareRenew)
	h("POST /api/share/shares", s.handleShareCreate)
	h("POST /api/share/shares/{sid}", s.handleShareUpdate)
	h("POST /api/share/shares/{sid}/delete", s.handleShareDelete)
	h("POST /api/share/shares/{sid}/revoke-all", s.handleShareRevokeAll)
	h("POST /api/share/shares/{sid}/pairings", s.handlePairingIssue)
	h("POST /api/share/shares/{sid}/pairings/cancel", s.handlePairingCancel)
	h("GET /api/share/shares/{sid}/requests", s.handlePairingRequests)
	h("POST /api/share/shares/{sid}/requests/{rid}", s.handlePairingDecide)
	h("POST /api/share/devices/{did}", s.handleDeviceRename)
	h("POST /api/share/devices/{did}/revoke", s.handleDeviceRevoke)
}

// shareLocal answers 403 unless the admin listener and the request's peer
// are loopback.
func (s *Server) shareLocal(h func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := net.ParseIP(remoteHost(r))
		if !s.share.adminLoopback() || ip == nil || !ip.IsLoopback() {
			writeError(w, http.StatusForbidden, "sharing can be managed only from this computer, with picmover on its default (loopback) address")
			return
		}
		h(w, r)
	}
}

// adminShareJSON is a share on the Share page.
type adminShareJSON struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	Kind         string             `json:"kind"`
	Scope        adminScopeJSON     `json:"scope"`
	Capabilities share.Capabilities `json:"capabilities"`
	ExpiresAt    *string            `json:"expiresAt"`
	State        string             `json:"state"`
	Approve      bool               `json:"approve"`
	CreatedAt    string             `json:"createdAt"`
	Devices      []adminDeviceJSON  `json:"devices"`
	// Pairing is the open pairing (null: none); Notice says why pairings
	// were cancelled (too many wrong codes); Requests counts the pairing
	// requests waiting for approval.
	Pairing  *pairingOpenJSON `json:"pairing"`
	Notice   string           `json:"notice,omitempty"`
	Requests int              `json:"requests"`
}

type pairingOpenJSON struct {
	ExpiresAt string `json:"expiresAt"`
}

// adminScopeJSON is a share's scope by name: libraries for a library
// share; library and album for an album share.
type adminScopeJSON struct {
	Libraries []adminLibJSON  `json:"libraries,omitempty"`
	Library   *adminLibJSON   `json:"library,omitempty"`
	Album     *adminAlbumJSON `json:"album,omitempty"`
}

type adminLibJSON struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Missing bool   `json:"missing,omitempty"`
	// Busy: the library is locked by another process or being rebuilt
	// (phones get 503 for it).
	Busy bool `json:"busy,omitempty"`
}

type adminAlbumJSON struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Count   int64  `json:"count"` // members (including trashed ones)
	Missing bool   `json:"missing,omitempty"`
}

type adminDeviceJSON struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	PairedAt    string  `json:"pairedAt"`
	LastSeen    *string `json:"lastSeen"`
	LastAddress string  `json:"lastAddress"`
	Browser     string  `json:"browser"`
}

func newAdminDevice(d share.Device) adminDeviceJSON {
	return adminDeviceJSON{ID: d.ID, Name: d.Name, PairedAt: d.PairedAt.Local().Format(time.RFC3339),
		LastSeen: rfc3339Ptr(&d.LastSeen), LastAddress: d.LastAddress, Browser: d.Browser}
}

// libBusy reports whether a listed library's entry is busy or rebuilding.
func (s *Server) libBusy(id string) bool {
	e := s.ws.entry(id)
	if e == nil {
		return false
	}
	st, _ := e.snapshot()
	return st == libBusy || st == libRebuilding
}

// adminShare describes sh for the Share page. Libraries and albums are
// checked read-only (never opened).
func (s *Server) adminShare(ctx context.Context, sh *share.Share) adminShareJSON {
	out := adminShareJSON{ID: sh.ID, Name: sh.Name, Kind: sh.Scope.Kind, Capabilities: sh.Capabilities,
		ExpiresAt: rfc3339Ptr(sh.ExpiresAt), Approve: sh.Approve, CreatedAt: sh.CreatedAt.Local().Format(time.RFC3339),
		Devices: make([]adminDeviceJSON, 0, len(sh.Devices))}
	for _, d := range sh.Devices {
		out.Devices = append(out.Devices, newAdminDevice(d))
	}
	out.State = s.baseState(sh, s.share.now())
	root := s.ws.root()
	here := sh.Scope.Known() && root != "" && sameDir(sh.Scope.Workspace, root)
	lib := func(k share.LibraryKey) (adminLibJSON, listed, bool) {
		j := adminLibJSON{ID: k.ID, Name: k.ID}
		if !here {
			return j, listed{}, false
		}
		li, ok := s.sharedLibrary(ctx, libRef{id: k.ID, libraryID: k.LibraryID})
		j.Missing, j.Busy = !ok, ok && s.libBusy(k.ID)
		return j, li, ok
	}
	switch sh.Scope.Kind {
	case share.KindLibraries:
		for _, k := range sh.Scope.Libraries {
			j, _, _ := lib(k)
			out.Scope.Libraries = append(out.Scope.Libraries, j)
		}
	case share.KindAlbum:
		j, li, ok := lib(*sh.Scope.Library)
		out.Scope.Library = &j
		al := &adminAlbumJSON{ID: sh.Scope.Album.ID}
		out.Scope.Album = al
		if ok {
			a, found, err := s.albumOf(ctx, li, sh.Scope.Album.ID)
			if err == nil && found && a.CreatedAt.Unix() == sh.Scope.Album.CreatedAt {
				al.Name, al.Count = a.Name, a.Count
			} else {
				al.Missing = true
			}
		}
		if out.State == share.StateActive && (j.Missing || al.Missing) {
			out.State = stateGone
		}
	}
	if t, ok := s.share.pairs.Open(sh.ID); ok {
		out.Pairing = &pairingOpenJSON{ExpiresAt: t.Local().Format(time.RFC3339)}
	}
	out.Notice = s.share.pairs.Notice(sh.ID)
	out.Requests = len(s.share.pairs.Requests(sh.ID))
	return out
}

// shareStatusJSON is GET /api/share.
type shareStatusJSON struct {
	State            string            `json:"state"`
	Reason           string            `json:"reason,omitempty"`
	URL              string            `json:"url,omitempty"`
	MDNSURL          string            `json:"mdnsUrl,omitempty"`
	Port             int               `json:"port"`
	Address          string            `json:"address"`
	Addresses        []shareAddrJSON   `json:"addresses"`
	Fingerprint      shareFingerprints `json:"fingerprint"`
	KeepOn           bool              `json:"keepOn"`
	MDNS             bool              `json:"mdns"`
	AutoOff          autoOffJSON       `json:"autoOff"`
	ManagedElsewhere *managedJSON      `json:"managedElsewhere,omitempty"`
	FirewallHint     string            `json:"firewallHint"`
	Shares           []adminShareJSON  `json:"shares"`
}

type shareAddrJSON struct {
	IP      string `json:"ip"`
	Iface   string `json:"iface"`
	Subnet  string `json:"subnet"`
	Virtual bool   `json:"virtual,omitempty"`
}

type shareFingerprints struct {
	Leaf string `json:"leaf"` // "" before the first certificate
	CA   string `json:"ca"`
}

type autoOffJSON struct {
	Mode  string  `json:"mode"`
	Until *string `json:"until,omitempty"`
}

type managedJSON struct {
	PID int    `json:"pid"`
	Cmd string `json:"cmd"`
}

// shareSummaryStatusJSON is GET /api/share/status, for the top-bar pill.
type shareSummaryStatusJSON struct {
	State            string       `json:"state"`
	Reason           string       `json:"reason,omitempty"`
	URL              string       `json:"url,omitempty"`
	Shares           int          `json:"shares"` // active shares
	Phones           int          `json:"phones"` // devices of active shares
	AutoOff          autoOffJSON  `json:"autoOff"`
	ManagedElsewhere *managedJSON `json:"managedElsewhere,omitempty"`
}

// writeShareErr answers an error of the share settings: 409 with the
// holder for another sharing process, 500 otherwise.
func (s *Server) writeShareErr(w http.ResponseWriter, r *http.Request, err error) {
	var me *share.ErrManaged
	if errors.As(err, &me) {
		writeJSON(w, http.StatusConflict, struct {
			Error            string      `json:"error"`
			ManagedElsewhere managedJSON `json:"managedElsewhere"`
		}{me.Error(), managedJSON{PID: me.PID, Cmd: me.Cmd}})
		return
	}
	s.internalError(w, r, err)
}

// shareConfig returns the manager and a snapshot of the settings,
// answering itself on failure.
func (s *Server) shareConfig(w http.ResponseWriter, r *http.Request) (*share.Manager, *share.Config, bool) {
	m, err := s.share.manager()
	if err != nil {
		s.internalError(w, r, err)
		return nil, nil, false
	}
	cfg, err := m.Snapshot()
	if err != nil {
		s.internalError(w, r, err)
		return nil, nil, false
	}
	return m, cfg, true
}

// autoOff is the auto-off JSON of mode with the time it fires.
func autoOff(mode string, at time.Time) autoOffJSON {
	out := autoOffJSON{Mode: mode}
	if !at.IsZero() {
		out.Until = rfc3339Ptr(&at)
	}
	return out
}

// managedBy is the other process sharing, or nil.
func (s *Server) managedBy(m *share.Manager) *managedJSON {
	if me := m.Managed(); me != nil {
		return &managedJSON{PID: me.PID, Cmd: me.Cmd}
	}
	return nil
}

// shareStatus builds GET /api/share.
func (s *Server) shareStatus(ctx context.Context, m *share.Manager, cfg *share.Config) shareStatusJSON {
	sh := s.share
	snap := sh.snapshot()
	out := shareStatusJSON{State: snap.state, Reason: snap.reason, Port: cfg.Port, KeepOn: cfg.KeepOn, MDNS: cfg.MDNS,
		AutoOff: autoOff(cfg.AutoOff, snap.autoAt), Addresses: []shareAddrJSON{}, Shares: []adminShareJSON{},
		ManagedElsewhere: s.managedBy(m)}
	cands, _ := sh.netif.Candidates()
	for _, c := range cands {
		out.Addresses = append(out.Addresses, shareAddrJSON{IP: c.IP.String(), Iface: c.Iface, Subnet: c.Subnet.String(),
			Virtual: c.Virtual})
	}
	subnet := ""
	if snap.state == shareOn || snap.state == sharePaused {
		out.Address, out.Port = snap.bound.IP.String(), snap.port
		subnet = snap.bound.Subnet.String()
	} else if c, ok := share.Choose(cands, cfg.Interface, sh.netif.DefaultIP()); ok {
		out.Address, subnet = c.IP.String(), c.Subnet.String()
	}
	if h := sh.host.Load(); h != nil {
		out.URL = h.url()
		if cfg.MDNS {
			if n := share.LocalName(sh.hostname); n != "" {
				out.MDNSURL = "https://" + net.JoinHostPort(n, h.port) + "/"
			}
		}
	}
	if c := sh.certsLoaded(); c != nil {
		out.Fingerprint.Leaf, out.Fingerprint.CA = c.Fingerprints()
	}
	out.FirewallHint = firewallHint(subnet, out.Port)
	for _, x := range cfg.Shares {
		out.Shares = append(out.Shares, s.adminShare(ctx, x))
	}
	return out
}

// certsLoaded returns the certificates when sharing has loaded them.
func (sh *sharing) certsLoaded() *share.Certs {
	sh.initMu.Lock()
	defer sh.initMu.Unlock()
	return sh.certs
}

// firewallHint is the "If the phone cannot connect" text for this OS.
func firewallHint(subnet string, port int) string {
	if subnet == "" {
		subnet = "192.168.1.0/24"
	}
	switch runtime.GOOS {
	case "windows":
		return "When Windows Defender Firewall asks, allow picmover on Private networks only, and set the Wi-Fi " +
			"network's profile to Private (Settings > Network > Wi-Fi > the network > Network profile type). " +
			"A Public profile blocks the phone even when Private was allowed."
	case "darwin":
		return "If the macOS firewall is on, answer Allow when it asks whether picmover may accept incoming " +
			"network connections (System Settings > Network > Firewall)."
	}
	return fmt.Sprintf("If the phone cannot connect, allow the port in the firewall (and undo it when done):\n"+
		"ufw:       sudo ufw allow from %s to any port %d proto tcp\n"+
		"firewalld: sudo firewall-cmd --zone=home --add-port=%d/tcp --permanent && sudo firewall-cmd --reload\n"+
		"(firewall-cmd --get-active-zones shows the Wi-Fi connection's zone)", subnet, port, port)
}

func (s *Server) handleShareGet(w http.ResponseWriter, r *http.Request) {
	m, cfg, ok := s.shareConfig(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.shareStatus(r.Context(), m, cfg))
}

// handleShareStatus is the light status for the top-bar pill: the state,
// the URL and the counts of active shares and their phones.
func (s *Server) handleShareStatus(w http.ResponseWriter, r *http.Request) {
	m, cfg, ok := s.shareConfig(w, r)
	if !ok {
		return
	}
	snap := s.share.snapshot()
	out := shareSummaryStatusJSON{State: snap.state, Reason: snap.reason, URL: s.share.host.Load().url(),
		AutoOff: autoOff(cfg.AutoOff, snap.autoAt), ManagedElsewhere: s.managedBy(m)}
	for _, x := range cfg.Shares {
		if s.baseState(x, s.share.now()) == share.StateActive {
			out.Shares++
			out.Phones += len(x.Devices)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type shareSettingsBody struct {
	Enabled *bool   `json:"enabled"`
	Address *string `json:"address"`
	Port    *int    `json:"port"`
	KeepOn  *bool   `json:"keepOn"`
	AutoOff *string `json:"autoOff"`
	MDNS    *bool   `json:"mdns"`
}

// handleShareSet is POST /api/share: settings, then the switch. Turning
// sharing on that fails (busy port, no address) answers 200 with state
// error and the reason; another sharing process is 409.
func (s *Server) handleShareSet(w http.ResponseWriter, r *http.Request) {
	var body shareSettingsBody
	if !decodeBody(w, r, &body) {
		return
	}
	sh := s.share
	m, err := sh.manager()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var iface string
	if body.Address != nil {
		cands, _ := sh.netif.Candidates()
		ip := net.ParseIP(*body.Address)
		for _, c := range cands {
			if ip != nil && c.IP.Equal(ip) {
				iface = c.Iface
			}
		}
		if iface == "" {
			writeError(w, http.StatusBadRequest, "address: not a LAN address of this computer")
			return
		}
	}
	if body.Port != nil && (*body.Port < 1 || *body.Port > 65535) {
		writeError(w, http.StatusBadRequest, "port: want 1-65535")
		return
	}
	if body.AutoOff != nil && !share.ValidAutoOff(*body.AutoOff) {
		writeError(w, http.StatusBadRequest, `autoOff: want "never", "1h", "4h" or "quit"`)
		return
	}
	relisten := false
	if body.Address != nil || body.Port != nil || body.KeepOn != nil || body.AutoOff != nil || body.MDNS != nil {
		err := m.Update(func(c *share.Config, _ time.Time) error {
			if body.Address != nil && c.Interface != iface {
				c.Interface, relisten = iface, true
			}
			if body.Port != nil && c.Port != *body.Port {
				c.Port, relisten = *body.Port, true
			}
			if body.MDNS != nil && c.MDNS != *body.MDNS {
				c.MDNS, relisten = *body.MDNS, true
			}
			if body.KeepOn != nil {
				c.KeepOn = *body.KeepOn
			}
			if body.AutoOff != nil {
				c.AutoOff = *body.AutoOff
			}
			return nil
		})
		if err != nil {
			s.writeShareErr(w, r, err)
			return
		}
	}
	if body.AutoOff != nil {
		sh.mu.Lock()
		if sh.state == shareOn {
			sh.setAutoOffLocked(*body.AutoOff)
		}
		sh.mu.Unlock()
	}
	switch {
	case body.Enabled != nil && !*body.Enabled:
		sh.Stop()
	case body.Enabled != nil && *body.Enabled:
		if err := sh.Start(); err != nil {
			var me *share.ErrManaged
			if errors.As(err, &me) || errors.Is(err, errAdminNotLoopback) {
				s.writeShareErr(w, r, err)
				return
			}
			// state error: the body says why.
		}
		if relisten {
			_ = sh.restart()
		}
	case relisten:
		_ = sh.restart()
	}
	s.handleShareGet(w, r)
}

func (s *Server) handleShareRenew(w http.ResponseWriter, r *http.Request) {
	sh := s.share
	m, err := sh.manager()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if me := m.Managed(); me != nil {
		s.writeShareErr(w, r, me)
		return
	}
	certs, err := sh.loadCerts(m)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	cands, _ := sh.netif.Candidates()
	sh.mu.Lock()
	bound := sh.bound
	if bound.IP == nil {
		bound, _ = share.Choose(cands, "", sh.netif.DefaultIP())
	}
	var ips = leafIPs(cands, bound)
	if bound.IP == nil {
		ips = ips[1:]
	}
	_, err = certs.Ensure(ips, leafDNS(sh.hostname), sh.now(), true)
	sh.mu.Unlock()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	sh.log.Info("Sharing certificate renewed; phones will see the certificate warning once more")
	s.handleShareGet(w, r)
}

// optTime is an optional JSON time that tells absent (set false) from
// null (set, t nil).
type optTime struct {
	set bool
	t   *time.Time
}

func (o *optTime) UnmarshalJSON(b []byte) error {
	o.set = true
	if bytes.Equal(b, []byte("null")) {
		o.t = nil
		return nil
	}
	var t time.Time
	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}
	o.t = &t
	return nil
}

type shareCreateBody struct {
	Name  string `json:"name"`
	Scope struct {
		Kind      string   `json:"kind"`
		Libraries []string `json:"libraries"`
		Library   string   `json:"library"`
		Album     int64    `json:"album"`
	} `json:"scope"`
	Capabilities *share.Capabilities `json:"capabilities"`
	ExpiresIn    *int64              `json:"expiresIn"`
	ExpiresAt    optTime             `json:"expiresAt"`
	Approve      bool                `json:"approve"`
}

// defaultAlbumExpiry is the expiry of a new album share when none is
// given.
const defaultAlbumExpiry = 30 * 24 * time.Hour

// errShareInput is a 400 of the share management.
type errShareInput struct{ msg string }

func (e *errShareInput) Error() string { return e.msg }

// resolveLibraryKey identifies a library of the served workspace for a
// new scope: its LibraryID, read from the open user.db or the file; a
// library that has none yet (never opened) is opened once.
func (s *Server) resolveLibraryKey(ctx context.Context, id string) (share.LibraryKey, listed, error) {
	li, err := s.ws.find(id)
	if err != nil || !li.library {
		return share.LibraryKey{}, li, &errShareInput{"unknown library " + strconv.Quote(id)}
	}
	lid, err := s.libraryIDOf(ctx, li)
	if err == nil && lid == "" {
		var l *libHandles
		if l, err = s.shareAcquire(ctx, id); err == nil {
			lid, err = l.user.LibraryID(ctx)
			l.entry.release()
		}
	}
	if err != nil {
		s.log.Info("share: cannot identify library", "id", id, "err", err)
		return share.LibraryKey{}, li, &errShareInput{"library " + strconv.Quote(id) + " cannot be read now (is it busy?)"}
	}
	return share.LibraryKey{ID: id, LibraryID: lid}, li, nil
}

// handleShareCreate is POST /api/share/shares: 201 share.
func (s *Server) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	var body shareCreateBody
	if !decodeBody(w, r, &body) {
		return
	}
	ctx := r.Context()
	root := s.ws.root()
	if root == "" {
		writeError(w, http.StatusConflict, "no workspace is served")
		return
	}
	scope := share.Scope{Kind: body.Scope.Kind, Workspace: root}
	bad := func(err error) {
		var in *errShareInput
		if errors.As(err, &in) || errors.Is(err, share.ErrInvalidScope) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.internalError(w, r, err)
	}
	switch body.Scope.Kind {
	case share.KindLibraries:
		if len(body.Scope.Libraries) == 0 {
			writeError(w, http.StatusBadRequest, "scope: no libraries")
			return
		}
		for _, id := range body.Scope.Libraries {
			k, _, err := s.resolveLibraryKey(ctx, id)
			if err != nil {
				bad(err)
				return
			}
			scope.Libraries = append(scope.Libraries, k)
		}
	case share.KindAlbum:
		k, li, err := s.resolveLibraryKey(ctx, body.Scope.Library)
		if err != nil {
			bad(err)
			return
		}
		a, found, err := s.albumOf(ctx, li, body.Scope.Album)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if !found {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown album %d in library %q", body.Scope.Album, k.ID))
			return
		}
		scope.Library, scope.Album = &k, &share.AlbumKey{ID: a.ID, CreatedAt: a.CreatedAt.Unix()}
	default:
		writeError(w, http.StatusBadRequest, `scope: kind must be "libraries" or "album"`)
		return
	}
	now := s.share.now()
	var expires *time.Time
	switch {
	case body.ExpiresIn != nil:
		if *body.ExpiresIn <= 0 {
			writeError(w, http.StatusBadRequest, "expiresIn: want seconds > 0")
			return
		}
		t := now.Add(time.Duration(*body.ExpiresIn) * time.Second).Truncate(time.Second)
		expires = &t
	case body.ExpiresAt.set:
		expires = body.ExpiresAt.t
	case scope.Kind == share.KindAlbum:
		t := now.Add(defaultAlbumExpiry).Truncate(time.Second)
		expires = &t
	}
	m, err := s.share.manager()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var created *share.Share
	err = m.Update(func(c *share.Config, now time.Time) error {
		x, err := c.AddShare(share.NewShare{Name: body.Name, Scope: scope, Capabilities: body.Capabilities,
			ExpiresAt: expires, Approve: body.Approve}, now)
		created = x
		return err
	})
	if !s.shareUpdateErr(w, r, err) {
		return
	}
	s.share.log.Info(fmt.Sprintf("Share %q created (%s)", created.Name, created.Scope.Kind))
	writeJSON(w, http.StatusCreated, s.adminShare(ctx, created))
}

// shareUpdateErr answers a share settings error: 400 for invalid input,
// 404 for an unknown share or device, 409 for a taken name, the limits
// and another sharing process. It returns true when err is nil.
func (s *Server) shareUpdateErr(w http.ResponseWriter, r *http.Request, err error) bool {
	var me *share.ErrManaged
	var in *errShareInput
	switch {
	case err == nil:
		return true
	case errors.As(err, &me):
		s.writeShareErr(w, r, err)
	case errors.As(err, &in), errors.Is(err, share.ErrInvalidName), errors.Is(err, share.ErrInvalidScope):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, share.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such share")
	case errors.Is(err, share.ErrNoDevice):
		writeError(w, http.StatusNotFound, "no such phone")
	case errors.Is(err, share.ErrNameTaken), errors.Is(err, share.ErrTooMany), errors.Is(err, share.ErrTooManyDevs):
		writeError(w, http.StatusConflict, err.Error())
	default:
		s.internalError(w, r, err)
	}
	return false
}

type shareUpdateBody struct {
	Name         *string             `json:"name"`
	Libraries    []string            `json:"libraries"`
	Capabilities *share.Capabilities `json:"capabilities"`
	ExpiresAt    optTime             `json:"expiresAt"`
	Paused       *bool               `json:"paused"`
	Approve      *bool               `json:"approve"`
	// Refused: the kind and an album share's album never change.
	Kind  json.RawMessage `json:"kind"`
	Scope json.RawMessage `json:"scope"`
	Album json.RawMessage `json:"album"`
}

// handleShareUpdate is POST /api/share/shares/{sid}: rename, the
// libraries of a library share, capabilities, expiry, pause, approval.
func (s *Server) handleShareUpdate(w http.ResponseWriter, r *http.Request) {
	var body shareUpdateBody
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Kind != nil || body.Scope != nil || body.Album != nil {
		writeError(w, http.StatusBadRequest, "the kind and an album share's album cannot change; create a new share")
		return
	}
	sid := r.PathValue("sid")
	m, cfg, ok := s.shareConfig(w, r)
	if !ok {
		return
	}
	cur := cfg.Share(sid)
	if cur == nil {
		writeError(w, http.StatusNotFound, "no such share")
		return
	}
	var libs []share.LibraryKey
	if body.Libraries != nil {
		if cur.Scope.Kind != share.KindLibraries {
			writeError(w, http.StatusBadRequest, "libraries: only a library share has libraries")
			return
		}
		if len(body.Libraries) == 0 {
			writeError(w, http.StatusBadRequest, "libraries: at least one")
			return
		}
		for _, id := range body.Libraries {
			// A library already shared keeps its key (it may be closed or
			// busy now); a new one is identified.
			if i := indexLib(cur.Scope.Libraries, id); i >= 0 {
				libs = append(libs, cur.Scope.Libraries[i])
				continue
			}
			k, _, err := s.resolveLibraryKey(r.Context(), id)
			if !s.shareUpdateErr(w, r, err) {
				return
			}
			libs = append(libs, k)
		}
	}
	var updated *share.Share
	var changes []string
	err := m.Update(func(c *share.Config, _ time.Time) error {
		x := c.Share(sid)
		if x == nil {
			return share.ErrNotFound
		}
		if body.Name != nil && *body.Name != x.Name {
			if err := c.Rename(x, *body.Name); err != nil {
				return err
			}
			changes = append(changes, "renamed")
		}
		if libs != nil {
			sc := x.Scope
			sc.Libraries = libs
			if err := sc.Validate(); err != nil {
				return err
			}
			x.Scope = sc
			changes = append(changes, "libraries changed")
		}
		if body.Capabilities != nil {
			x.Capabilities = *body.Capabilities
			changes = append(changes, "capabilities changed")
		}
		if body.ExpiresAt.set {
			x.ExpiresAt = body.ExpiresAt.t
			changes = append(changes, "expiry changed")
		}
		if body.Paused != nil && *body.Paused != x.Paused {
			x.Paused = *body.Paused
			changes = append(changes, map[bool]string{true: "paused", false: "resumed"}[x.Paused])
		}
		if body.Approve != nil {
			x.Approve = *body.Approve
		}
		updated = x
		return nil
	})
	if !s.shareUpdateErr(w, r, err) {
		return
	}
	if updated.Paused {
		s.share.pairs.DropShare(sid)
	}
	for _, c := range changes {
		s.share.log.Info(fmt.Sprintf("Share %q %s", updated.Name, c))
	}
	writeJSON(w, http.StatusOK, s.adminShare(r.Context(), updated))
}

func indexLib(libs []share.LibraryKey, id string) int {
	for i, l := range libs {
		if l.ID == id {
			return i
		}
	}
	return -1
}

// handleShareDelete is POST /api/share/shares/{sid}/delete: 204.
func (s *Server) handleShareDelete(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("sid")
	m, err := s.share.manager()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var name string
	err = m.Update(func(c *share.Config, _ time.Time) error {
		if x := c.Share(sid); x != nil {
			name = x.Name
		}
		return c.DeleteShare(sid)
	})
	if !s.shareUpdateErr(w, r, err) {
		return
	}
	s.share.pairs.DropShare(sid)
	s.share.log.Info(fmt.Sprintf("Share %q deleted with its phones", name))
	w.WriteHeader(http.StatusNoContent)
}

// handleShareRevokeAll is POST /api/share/shares/{sid}/revoke-all: every
// phone of the share is removed; the share stays.
func (s *Server) handleShareRevokeAll(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("sid")
	m, err := s.share.manager()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var updated *share.Share
	err = m.Update(func(c *share.Config, _ time.Time) error {
		x := c.Share(sid)
		if x == nil {
			return share.ErrNotFound
		}
		x.Devices = []share.Device{}
		updated = x
		return nil
	})
	if !s.shareUpdateErr(w, r, err) {
		return
	}
	s.share.log.Info(fmt.Sprintf("Every phone of share %q revoked", updated.Name))
	writeJSON(w, http.StatusOK, s.adminShare(r.Context(), updated))
}

// pairingJSON is an issued pairing for the QR dialog.
type pairingJSON struct {
	URL       string `json:"url"`
	MDNSURL   string `json:"mdnsUrl,omitempty"`
	Code      string `json:"code"`
	QRSVG     string `json:"qrSvg"`
	ExpiresAt string `json:"expiresAt"`
}

// handlePairingIssue is POST /api/share/shares/{sid}/pairings: 409 when
// sharing is off or the share is not active.
func (s *Server) handlePairingIssue(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("sid")
	_, cfg, ok := s.shareConfig(w, r)
	if !ok {
		return
	}
	x := cfg.Share(sid)
	if x == nil {
		writeError(w, http.StatusNotFound, "no such share")
		return
	}
	p, err := s.share.issuePairing(r.Context(), x)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// issuePairing opens a pairing for share x, which must be active, while
// sharing is on.
func (sh *sharing) issuePairing(ctx context.Context, x *share.Share) (pairingJSON, error) {
	host := sh.host.Load()
	if host == nil {
		if snap := sh.snapshot(); snap.state == sharePaused {
			return pairingJSON{}, fmt.Errorf("sharing on Wi-Fi is paused (%s); resume it first", snap.reason)
		}
		return pairingJSON{}, errors.New("sharing on Wi-Fi is off; turn it on first")
	}
	if st := sh.s.shareState(ctx, x); st != share.StateActive {
		return pairingJSON{}, fmt.Errorf("the share is %s; phones cannot pair with it now", st)
	}
	t := sh.pairs.Issue(x.ID)
	out := pairingJSON{URL: host.url() + "#/pair/" + t.Token, Code: share.FormatCode(t.Code),
		ExpiresAt: t.ExpiresAt.Local().Format(time.RFC3339)}
	if host.local != "" {
		out.MDNSURL = "https://" + net.JoinHostPort(host.local, host.port) + "/#/pair/" + t.Token
	}
	svg, err := share.QRSVG(out.URL)
	if err != nil {
		return pairingJSON{}, err
	}
	out.QRSVG = svg
	sh.log.Info(fmt.Sprintf("Pairing code for share %q issued (valid 2 minutes)", x.Name))
	return out, nil
}

// handlePairingCancel is POST /api/share/shares/{sid}/pairings/cancel.
func (s *Server) handlePairingCancel(w http.ResponseWriter, r *http.Request) {
	s.share.pairs.Cancel(r.PathValue("sid"))
	w.WriteHeader(http.StatusNoContent)
}

// handlePairingRequests is GET /api/share/shares/{sid}/requests.
func (s *Server) handlePairingRequests(w http.ResponseWriter, r *http.Request) {
	reqs := s.share.pairs.Requests(r.PathValue("sid"))
	type reqJSON struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Browser string `json:"browser"`
		Address string `json:"address"`
		At      string `json:"at"`
	}
	out := make([]reqJSON, 0, len(reqs))
	for _, q := range reqs {
		out = append(out, reqJSON{ID: q.ID, Name: q.Name, Browser: q.Browser, Address: q.Address,
			At: q.At.Local().Format(time.RFC3339)})
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePairingDecide is POST /api/share/shares/{sid}/requests/{rid}
// {approve}: 204, 404 for an unknown request.
func (s *Server) handlePairingDecide(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Approve *bool `json:"approve"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Approve == nil {
		writeError(w, http.StatusBadRequest, "approve: want true or false")
		return
	}
	if err := s.share.pairs.Decide(r.PathValue("sid"), r.PathValue("rid"), *body.Approve); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeviceRename is POST /api/share/devices/{did} {name}: device.
func (s *Server) handleDeviceRename(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	name := share.CleanName(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, share.ErrInvalidName.Error())
		return
	}
	did := r.PathValue("did")
	m, err := s.share.manager()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var dev share.Device
	err = m.Update(func(c *share.Config, _ time.Time) error {
		_, d := c.Device(did)
		if d == nil {
			return share.ErrNoDevice
		}
		d.Name = name
		dev = *d
		return nil
	})
	if !s.shareUpdateErr(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, newAdminDevice(dev))
}

// handleDeviceRevoke is POST /api/share/devices/{did}/revoke: 204; the
// phone's next request is 401.
func (s *Server) handleDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	did := r.PathValue("did")
	m, err := s.share.manager()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var name, shareName string
	err = m.Update(func(c *share.Config, _ time.Time) error {
		x, d := c.Device(did)
		if d != nil {
			name, shareName = d.Name, x.Name
		}
		return c.RemoveDevice(did)
	})
	if !s.shareUpdateErr(w, r, err) {
		return
	}
	s.share.log.Info(fmt.Sprintf("Phone %q of share %q revoked", name, shareName))
	w.WriteHeader(http.StatusNoContent)
}
