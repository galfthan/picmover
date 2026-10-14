package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"picmover/internal/share"
)

// Sharing on the home Wi-Fi for the commands and the desktop app: serve
// --share and --pair, the tray item and "Keep sharing on".

// SharingStatus is sharing's state for serve's output and the tray.
type SharingStatus struct {
	State       string // "off", "on", "paused", "error"
	Reason      string
	URL         string // https://<ip>:<port>/ while on
	Address     string // the bound IP while on or paused
	Fingerprint string // the leaf's SHA-256, AB:CD:...
	// Shares are the active shares of the served workspace.
	Shares []SharingShare
}

// SharingShare describes an active share in one line.
type SharingShare struct {
	Name   string
	Scope  string // "libraries peter, penny" or "album Summer in peter"
	Phones int
}

// Line is "<name>: <scope>; N phones".
func (s SharingShare) Line() string {
	phones := "phones"
	if s.Phones == 1 {
		phones = "phone"
	}
	return fmt.Sprintf("%s: %s; %d %s", s.Name, s.Scope, s.Phones, phones)
}

// StartSharing turns sharing on with the existing shares (serve --share,
// the tray, "Keep sharing on"). port, when not 0, is saved as the share
// port first. It fails when sharing could not start; Status says why.
func (r *Running) StartSharing(port int) error {
	sh := r.srv.share
	if port != 0 {
		m, err := sh.manager()
		if err != nil {
			return err
		}
		if err := m.Update(func(c *share.Config, _ time.Time) error { c.Port = port; return nil }); err != nil {
			return err
		}
	}
	return sh.Start()
}

// StopSharing turns sharing off.
func (r *Running) StopSharing() { r.srv.share.Stop() }

// KeepSharingOn reports the app's "Keep sharing on" setting.
func (r *Running) KeepSharingOn() bool {
	m, err := r.srv.share.manager()
	if err != nil {
		return false
	}
	cfg, err := m.Snapshot()
	return err == nil && cfg.KeepOn
}

// SharingStatus reports sharing's state and the active shares of the
// served workspace.
func (r *Running) SharingStatus() SharingStatus {
	s := r.srv
	snap := s.share.snapshot()
	out := SharingStatus{State: snap.state, Reason: snap.reason, URL: s.share.host.Load().url()}
	if snap.bound.IP != nil && snap.state != shareOff && snap.state != shareError {
		out.Address = snap.bound.IP.String()
	}
	if c := s.share.certsLoaded(); c != nil {
		out.Fingerprint, _ = c.Fingerprints()
	}
	m, err := s.share.manager()
	if err != nil {
		return out
	}
	cfg, err := m.Snapshot()
	if err != nil {
		return out
	}
	ctx := context.Background()
	for _, x := range cfg.Shares {
		if s.shareState(ctx, x) != share.StateActive {
			continue
		}
		out.Shares = append(out.Shares, SharingShare{Name: x.Name, Scope: scopeLine(s.adminShare(ctx, x)),
			Phones: len(x.Devices)})
	}
	return out
}

// scopeLine is a share's scope in words.
func scopeLine(a adminShareJSON) string {
	if a.Scope.Album != nil && a.Scope.Library != nil {
		return fmt.Sprintf("album %s in %s", a.Scope.Album.Name, a.Scope.Library.ID)
	}
	ids := make([]string, 0, len(a.Scope.Libraries))
	for _, l := range a.Scope.Libraries {
		ids = append(ids, l.ID)
	}
	return "libraries " + strings.Join(ids, ", ")
}

// SharesHere is the number of shares of the served workspace (of any
// state).
func (r *Running) SharesHere() (int, error) {
	m, err := r.srv.share.manager()
	if err != nil {
		return 0, err
	}
	cfg, err := m.Snapshot()
	if err != nil {
		return 0, err
	}
	root, n := r.srv.ws.root(), 0
	for _, x := range cfg.Shares {
		if root != "" && x.Scope.Known() && sameDir(x.Scope.Workspace, root) {
			n++
		}
	}
	return n, nil
}

// Pairing is an issued pairing for the terminal (serve --pair).
type Pairing struct {
	Share     string // the share's name
	URL       string
	Code      string // "K7QM-3XRD"
	ExpiresAt time.Time
	QR        string // the URL as a QR code in Unicode half blocks
}

// ErrNoSuchShare: --pair names no share.
var ErrNoSuchShare = errors.New("no such share")

// Pair issues a pairing for the share named by ref (its name or id)
// while sharing is on.
func (r *Running) Pair(ref string) (Pairing, error) {
	m, err := r.srv.share.manager()
	if err != nil {
		return Pairing{}, err
	}
	cfg, err := m.Snapshot()
	if err != nil {
		return Pairing{}, err
	}
	x := cfg.Lookup(ref)
	if x == nil {
		return Pairing{}, fmt.Errorf("%w %q; 'picmover share list' shows them", ErrNoSuchShare, ref)
	}
	p, err := r.srv.share.issuePairing(context.Background(), x)
	if err != nil {
		return Pairing{}, err
	}
	qr, err := share.QRTerminal(p.URL)
	if err != nil {
		return Pairing{}, err
	}
	exp, _ := time.Parse(time.RFC3339, p.ExpiresAt)
	return Pairing{Share: x.Name, URL: p.URL, Code: p.Code, ExpiresAt: exp, QR: qr}, nil
}
