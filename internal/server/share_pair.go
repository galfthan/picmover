package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"picmover/internal/share"
)

// approvalWait is how long a redeem waits for the computer's approval.
var approvalWait = 60 * time.Second

type pairBody struct {
	Token   string `json:"token"`
	Code    string `json:"code"`
	Name    string `json:"name"`
	Replace bool   `json:"replace"`
}

type pairedJSON struct {
	Device shareDeviceRefJSON `json:"device"`
	Share  shareJSON          `json:"share"`
}

// pairGoneJSON is the 410 of a redeem; state names why the share refuses
// pairing when it is not active.
type pairGoneJSON struct {
	Error string `json:"error"`
	State string `json:"state,omitempty"`
}

// alreadyPairedJSON is the 409 of a redeem from a browser that is paired
// already (without replace).
type alreadyPairedJSON struct {
	Error   string `json:"error"`
	Current struct {
		Share string `json:"share"`
	} `json:"current"`
}

// errPairBlocked is the 429 of an address refused after too many wrong
// codes.
var errPairBlocked = fmt.Sprintf("Too many wrong codes from this phone; try again in %d minutes.",
	int(share.CodeBlockFor/time.Minute))

// stateOff is the state of the 410 of a redeem whose approval wait ended
// because sharing was turned off, paused or picmover quit.
const stateOff = "off"

// handleSharePair is POST /api/pair {token | code, name, replace?}: it
// redeems a pairing (docs/design/lan-share.md, "Redeem"). 200 {device,
// share} with the device cookie; 400 malformed; 410 expired, used or
// unknown (or the share is not active, with its state; or sharing was
// turned off during the approval wait, state "off"); 409 already paired
// (the token is not used up) or the share is full (nor is it); 403
// denied on the computer; 429 rate limited, or the address typed too
// many wrong codes.
func (s *Server) handleSharePair(w http.ResponseWriter, r *http.Request) {
	sh := s.share
	ip := remoteHost(r)
	if sh.pairs.Blocked(ip) {
		writeError(w, http.StatusTooManyRequests, errPairBlocked)
		return
	}
	if !sh.pairIP.Allow(ip) {
		writeError(w, http.StatusTooManyRequests, "Too many pairing attempts; wait a minute and try again.")
		return
	}
	m := sh.mgr
	if m == nil {
		writeJSON(w, http.StatusGone, pairGoneJSON{Error: share.ErrPairingGone.Error()})
		return
	}
	var body pairBody
	if !decodeBody(w, r, &body) {
		return
	}
	// The global budget bounds code guessing from many addresses: only a
	// well-formed code takes from it (a token is 256 bits; a malformed
	// value is refused before any lookup).
	if body.Token == "" && share.NormalizeCode(body.Code) != "" && !sh.pairAll.Allow("") {
		writeError(w, http.StatusTooManyRequests, "Too many pairing attempts; wait a minute and try again.")
		return
	}
	claim, err := sh.pairs.Find(body.Token, body.Code, ip)
	switch {
	case errors.Is(err, share.ErrMalformed):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeJSON(w, http.StatusGone, pairGoneJSON{Error: share.ErrPairingGone.Error()})
		return
	}
	cfg, err := m.Snapshot()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	target := cfg.Share(claim.ShareID)
	if target == nil {
		writeJSON(w, http.StatusGone, pairGoneJSON{Error: share.ErrPairingGone.Error()})
		return
	}
	if st := s.shareState(r.Context(), target); st != share.StateActive {
		writeJSON(w, http.StatusGone, pairGoneJSON{Error: shareStateMsg[st], State: st})
		return
	}
	replace := ""
	if cur, _, ok, _ := s.verifyDevice(r); ok {
		if !body.Replace {
			var out alreadyPairedJSON
			out.Error = fmt.Sprintf("This phone is already paired with %q.", cur.Share.Name)
			out.Current.Share = cur.Share.Name
			writeJSON(w, http.StatusConflict, out)
			return
		}
		replace = cur.Device.ID
	}
	// A full share refuses before the pairing is used up, so that the
	// same code works once a phone is revoked.
	if len(target.Devices) >= share.MaxDevicesPerShare && !slices.ContainsFunc(target.Devices,
		func(d share.Device) bool { return d.ID == replace }) {
		writeError(w, http.StatusConflict, share.ErrTooManyDevs.Error())
		return
	}
	if err := sh.pairs.Use(claim); err != nil {
		writeJSON(w, http.StatusGone, pairGoneJSON{Error: share.ErrPairingGone.Error()})
		return
	}
	name := share.CleanName(body.Name)
	if name == "" {
		name = share.DefaultDeviceName
	}
	browser := share.BrowserLabel(r.UserAgent())
	if target.Approve {
		ask := sh.pairs.Ask(target.ID, name, browser, ip)
		sh.log.Info(fmt.Sprintf("Phone %q (%s, %s) asks to pair with share %q; approve it on the Share page",
			name, browser, ip, target.Name))
		switch err := sh.pairs.Wait(r.Context(), ask, approvalWait); {
		case errors.Is(err, share.ErrDenied):
			writeError(w, http.StatusForbidden, err.Error())
			return
		case errors.Is(err, share.ErrNotApproved):
			writeJSON(w, http.StatusGone, pairGoneJSON{Error: err.Error()})
			return
		case errors.Is(err, share.ErrSharingOff):
			writeJSON(w, http.StatusGone, pairGoneJSON{Error: err.Error(), State: stateOff})
			return
		case errors.Is(err, share.ErrShareDropped):
			s.writePairShareGone(w, r, target.ID)
			return
		case err != nil:
			return // the phone went away
		}
	}
	p, err := m.AddDevice(target.ID, name, browser, ip, replace)
	var se *share.StateError
	switch {
	case errors.Is(err, share.ErrTooManyDevs):
		writeError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, share.ErrNotFound), errors.As(err, &se):
		writeJSON(w, http.StatusGone, pairGoneJSON{Error: share.ErrPairingGone.Error()})
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	if replace != "" {
		sh.log.Info("Phone unpaired (paired with another share)", "device", replace)
	}
	sh.log.Info(fmt.Sprintf("Paired %q (%s, %s) with share %q", p.Device.Name, browser, ip, p.Share.Name))
	w.Header().Add("Set-Cookie", p.Cookie)
	req := &shareReq{dev: p.Device, sh: p.Share, auth: scopeFor(&p.Share)}
	info := shareJSON{Name: p.Share.Name, Kind: p.Share.Scope.Kind, Capabilities: p.Share.Capabilities,
		ExpiresAt: rfc3339Ptr(p.Share.ExpiresAt)}
	if req.auth != nil {
		// Best effort: the session route gives the same object.
		rec := &discardWriter{h: http.Header{}}
		if full, ok := s.shareInfo(rec, r, req); ok {
			info = full
		}
	}
	writeJSON(w, http.StatusOK, pairedJSON{Device: shareDeviceRefJSON{ID: p.Device.ID, Name: p.Device.Name}, Share: info})
}

// writePairShareGone answers a redeem whose share was paused, ended or
// deleted during the approval wait: 410 with the share's state, or
// without one when the share is gone.
func (s *Server) writePairShareGone(w http.ResponseWriter, r *http.Request, sid string) {
	if cfg, err := s.share.mgr.Snapshot(); err == nil {
		if x := cfg.Share(sid); x != nil {
			if st := s.shareState(r.Context(), x); st != share.StateActive {
				writeJSON(w, http.StatusGone, pairGoneJSON{Error: shareStateMsg[st], State: st})
				return
			}
		}
	}
	writeJSON(w, http.StatusGone, pairGoneJSON{Error: share.ErrPairingGone.Error()})
}

// discardWriter swallows a response (shareInfo's refusals after a
// successful pairing).
type discardWriter struct{ h http.Header }

func (d *discardWriter) Header() http.Header         { return d.h }
func (d *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardWriter) WriteHeader(int)             {}

// shareState is a share's full state for pairing and the Share page:
// baseState, then gone for an album share whose library or album is not
// the one shared. It never opens a library.
func (s *Server) shareState(ctx context.Context, sh *share.Share) string {
	if st := s.baseState(sh, s.share.now()); st != share.StateActive {
		return st
	}
	if sh.Scope.Kind != share.KindAlbum {
		return share.StateActive
	}
	ref := libRef{id: sh.Scope.Library.ID, libraryID: sh.Scope.Library.LibraryID}
	li, ok := s.sharedLibrary(ctx, ref)
	if !ok {
		return stateGone
	}
	a, ok, err := s.albumOf(ctx, li, sh.Scope.Album.ID)
	if err != nil || !ok || a.CreatedAt.Unix() != sh.Scope.Album.CreatedAt {
		return stateGone
	}
	return share.StateActive
}
