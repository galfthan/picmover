package share

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"
)

// Pairing (docs/design/lan-share.md, "Pairing protocol"): per share, a
// 256-bit token for the QR code's URL fragment and an 8-character code
// to type, both single use, valid 2 minutes, kept in memory only and only
// as SHA-256 hashes.
const (
	PairingTTL       = 2 * time.Minute
	maxOpenPerShare  = 3
	maxOpenTotal     = 10
	maxWrongCodes    = 5
	tokenBytes       = 32
	codeLen          = 8
	codeAlphabet     = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
	maxPendingAsks   = 10
	TooManyWrongCode = "Too many wrong codes; show a new one."
)

// Errors of Redeem and Decide.
var (
	// ErrPairingGone: no open pairing has this token or code (expired,
	// used, cancelled, or never issued). 410 to the phone.
	ErrPairingGone = errors.New("This pairing code has expired or was already used. Show a new one on the computer.")
	// ErrMalformed: neither a well-formed token nor a code. 400.
	ErrMalformed = errors.New("invalid pairing token or code")
	// ErrNoRequest: no pending approval request with that id.
	ErrNoRequest = errors.New("no such pairing request")
)

// Ticket is an issued pairing: the token and the code in clear (shown
// once, never stored) and when they expire.
type Ticket struct {
	Token     string
	Code      string // "K7QM3XRD"; FormatCode adds the dash
	ExpiresAt time.Time
}

// FormatCode shows a code as "K7QM-3XRD".
func FormatCode(code string) string {
	if len(code) != codeLen {
		return code
	}
	return code[:4] + "-" + code[4:]
}

type pairing struct {
	shareID   string
	token     [32]byte // SHA-256 of the token
	code      [32]byte // SHA-256 of the code
	expiresAt time.Time
	used      bool
	wrong     int // wrong code attempts while this pairing was open
}

// Pairings are the open pairings and pending approval requests of a
// running server. Nothing here is persistent: a restart drops it all.
type Pairings struct {
	now func() time.Time

	mu      sync.Mutex
	open    []*pairing          // oldest first
	notices map[string]string   // share id -> message for the Share page
	asks    map[string]*Request // pending approval requests by id
}

// NewPairings returns an empty registry with the clock now (nil: time.Now).
func NewPairings(now func() time.Time) *Pairings {
	if now == nil {
		now = time.Now
	}
	return &Pairings{now: now, notices: map[string]string{}, asks: map[string]*Request{}}
}

// newToken is 32 random bytes, base64url (43 characters).
func newToken() string { return base64.RawURLEncoding.EncodeToString(randomBytes(tokenBytes)) }

// newCode is 8 characters drawn uniformly from codeAlphabet.
func newCode() string {
	b := make([]byte, codeLen)
	n := big.NewInt(int64(len(codeAlphabet)))
	for i := range b {
		k, err := rand.Int(rand.Reader, n)
		if err != nil {
			panic(err)
		}
		b[i] = codeAlphabet[k.Int64()]
	}
	return string(b)
}

// NormalizeCode upper-cases a typed code and drops dashes and spaces; ""
// when the result is not 8 characters of the alphabet.
func NormalizeCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch {
		case r == '-' || r == ' ':
		case r < 128 && strings.IndexByte(codeAlphabet, byte(r)) >= 0:
			b.WriteRune(r)
		default:
			return ""
		}
	}
	if b.Len() != codeLen {
		return ""
	}
	return b.String()
}

// validToken reports the shape of a token: 43 base64url characters.
func validToken(t string) bool {
	if len(t) != base64.RawURLEncoding.EncodedLen(tokenBytes) {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(t)
	return err == nil
}

// pruneLocked drops expired and used pairings.
func (p *Pairings) pruneLocked(now time.Time) {
	p.open = slices.DeleteFunc(p.open, func(x *pairing) bool { return x.used || !now.Before(x.expiresAt) })
}

// Issue opens a pairing for share shareID. At most 3 are open per share
// and 10 in total; issuing one more drops the oldest.
func (p *Pairings) Issue(shareID string) Ticket {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	p.pruneLocked(now)
	delete(p.notices, shareID)
	count := func() (n int) {
		for _, x := range p.open {
			if x.shareID == shareID {
				n++
			}
		}
		return n
	}
	for count() >= maxOpenPerShare {
		i := slices.IndexFunc(p.open, func(x *pairing) bool { return x.shareID == shareID })
		p.open = slices.Delete(p.open, i, i+1)
	}
	for len(p.open) >= maxOpenTotal {
		p.open = p.open[1:]
	}
	t := Ticket{Token: newToken(), Code: newCode(), ExpiresAt: now.Add(PairingTTL)}
	p.open = append(p.open, &pairing{shareID: shareID, token: sha256.Sum256([]byte(t.Token)),
		code: sha256.Sum256([]byte(t.Code)), expiresAt: t.ExpiresAt})
	return t
}

// Cancel closes every open pairing of share shareID.
func (p *Pairings) Cancel(shareID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.open = slices.DeleteFunc(p.open, func(x *pairing) bool { return x.shareID == shareID })
	delete(p.notices, shareID)
}

// Open reports when the newest open pairing of shareID expires.
func (p *Pairings) Open(shareID string) (expiresAt time.Time, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(p.now())
	for _, x := range p.open {
		if x.shareID == shareID && x.expiresAt.After(expiresAt) {
			expiresAt, ok = x.expiresAt, true
		}
	}
	return expiresAt, ok
}

// Notice is a message about shareID's pairings for the Share page (too
// many wrong codes), or "".
func (p *Pairings) Notice(shareID string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.notices[shareID]
}

// Claim is an open pairing found by Find, not yet used.
type Claim struct {
	ShareID string
	p       *pairing
}

// Find looks up the open pairing of a token or a code without using it
// up. A wrong code counts against every open pairing; after 5 wrong codes
// all open pairings are cancelled. A malformed value is ErrMalformed, an
// unknown or expired one ErrPairingGone.
func (p *Pairings) Find(token, code string) (Claim, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	p.pruneLocked(now)
	switch {
	case token != "":
		if !validToken(token) {
			return Claim{}, ErrMalformed
		}
		h := sha256.Sum256([]byte(token))
		for _, x := range p.open {
			if x.token == h {
				return Claim{ShareID: x.shareID, p: x}, nil
			}
		}
		return Claim{}, ErrPairingGone
	case code != "":
		c := NormalizeCode(code)
		if c == "" {
			return Claim{}, ErrMalformed
		}
		h := sha256.Sum256([]byte(c))
		for _, x := range p.open {
			if x.code == h {
				return Claim{ShareID: x.shareID, p: x}, nil
			}
		}
		cancel := false
		for _, x := range p.open {
			x.wrong++
			cancel = cancel || x.wrong >= maxWrongCodes
		}
		if cancel {
			for _, x := range p.open {
				p.notices[x.shareID] = TooManyWrongCode
			}
			p.open = nil
		}
		return Claim{}, ErrPairingGone
	}
	return Claim{}, ErrMalformed
}

// Use marks a claimed pairing used: an atomic swap, so of two concurrent
// redeems exactly one wins (the other gets ErrPairingGone).
func (p *Pairings) Use(c Claim) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c.p == nil || c.p.used || !p.now().Before(c.p.expiresAt) || !slices.Contains(p.open, c.p) {
		return ErrPairingGone
	}
	c.p.used = true
	p.pruneLocked(p.now())
	return nil
}

// Request is a redeem waiting for approval on the computer (a share with
// approve on).
type Request struct {
	ID      string    `json:"id"`
	ShareID string    `json:"-"`
	Name    string    `json:"name"`
	Browser string    `json:"browser"`
	Address string    `json:"address"`
	At      time.Time `json:"at"`

	decided chan bool // buffered: one decision
}

// ErrNotApproved: the computer did not decide in time.
var ErrNotApproved = errors.New("Not approved in time. Show a new code on the computer and try again.")

// ErrDenied: the computer denied the pairing.
var ErrDenied = errors.New("The computer did not allow this phone.")

// Ask records a pending approval request (its pairing is already used).
func (p *Pairings) Ask(shareID, name, browser, addr string) *Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Bounded: each request used up a pairing, but keep the map small
	// even so.
	for len(p.asks) >= maxPendingAsks {
		var oldest *Request
		for _, r := range p.asks {
			if oldest == nil || r.At.Before(oldest.At) {
				oldest = r
			}
		}
		delete(p.asks, oldest.ID)
		oldest.decided <- false
	}
	r := &Request{ID: newID("r_"), ShareID: shareID, Name: name, Browser: browser, Address: addr,
		At: p.now().Truncate(time.Second), decided: make(chan bool, 1)}
	p.asks[r.ID] = r
	return r
}

// Wait waits for the decision on r, at most timeout or until ctx is done:
// nil when approved, ErrDenied, ErrNotApproved (timeout), or ctx's error.
// The request is gone afterwards in every case.
func (p *Pairings) Wait(ctx context.Context, r *Request, timeout time.Duration) error {
	t := time.NewTimer(timeout)
	defer t.Stop()
	defer func() {
		p.mu.Lock()
		delete(p.asks, r.ID)
		p.mu.Unlock()
	}()
	select {
	case ok := <-r.decided:
		if ok {
			return nil
		}
		return ErrDenied
	case <-t.C:
		return ErrNotApproved
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Requests lists the pending requests of shareID, oldest first.
func (p *Pairings) Requests(shareID string) []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []Request{}
	for _, r := range p.asks {
		if r.ShareID == shareID {
			out = append(out, *r)
		}
	}
	slices.SortFunc(out, func(a, b Request) int { return a.At.Compare(b.At) })
	return out
}

// Decide approves or denies a pending request of shareID.
func (p *Pairings) Decide(shareID, id string, approve bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.asks[id]
	if r == nil || r.ShareID != shareID {
		return ErrNoRequest
	}
	delete(p.asks, id)
	r.decided <- approve
	return nil
}

// DropShare cancels the pairings and denies the pending requests of a
// share (deleted, paused, revoked).
func (p *Pairings) DropShare(shareID string) {
	p.Cancel(shareID)
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, r := range p.asks {
		if r.ShareID == shareID {
			delete(p.asks, id)
			r.decided <- false
		}
	}
}

// Reset drops everything (sharing turned off).
func (p *Pairings) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.open = nil
	p.notices = map[string]string{}
	for id, r := range p.asks {
		delete(p.asks, id)
		r.decided <- false
	}
}
