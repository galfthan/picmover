package share

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Limits (docs/design/lan-share.md, "Expiry, pause, revoke all").
const (
	MaxShares          = 50
	MaxDevicesPerShare = 20
	maxNameRunes       = 60
)

// Share states as the share package knows them; the server adds "gone"
// (album or library missing) and "unavailable" (another workspace).
const (
	StateActive  = "active"
	StatePaused  = "paused"
	StateExpired = "expired"
	StateUnknown = "unknown" // a scope kind this picmover does not know
)

const (
	// expiredKeep is how long an expired share stays listed (with
	// Extend) before it is deleted.
	expiredKeep = 30 * 24 * time.Hour
	// deviceIdle removes a device unused this long (its cookie has
	// expired anyway).
	deviceIdle = 400 * 24 * time.Hour
	// cookieRenew re-sends a device's cookie once it is this old.
	cookieRenew = 7 * 24 * time.Hour
)

// Device is a paired phone. Its credential is stored only as the SHA-256
// of the cookie secret.
type Device struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	SecretSHA256   string    `json:"secretSha256"`
	PairedAt       time.Time `json:"pairedAt"`
	LastSeen       time.Time `json:"lastSeen,omitzero"`
	LastAddress    string    `json:"lastAddress,omitempty"`
	Browser        string    `json:"browser,omitempty"`
	CookieIssuedAt time.Time `json:"cookieIssuedAt"`

	extra extra // members of a newer picmover, kept on rewrite
}

// Share is a named, scoped grant with its own devices.
type Share struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Scope        Scope        `json:"scope"`
	Capabilities Capabilities `json:"capabilities"`
	ExpiresAt    *time.Time   `json:"expiresAt"` // nil: never
	Paused       bool         `json:"paused"`
	Approve      bool         `json:"approve"`
	CreatedAt    time.Time    `json:"createdAt"`
	Devices      []Device     `json:"devices"`

	extra extra // members of a newer picmover, kept on rewrite
}

// State is the share's state from its own fields at now: unknown for a
// kind this picmover does not know, else paused, expired or active. The
// server refines active into gone or unavailable.
func (s *Share) State(now time.Time) string {
	switch {
	case !s.Scope.Known():
		return StateUnknown
	case s.Paused:
		return StatePaused
	case s.ExpiresAt != nil && !now.Before(*s.ExpiresAt):
		return StateExpired
	}
	return StateActive
}

// device returns the device with id, or nil.
func (s *Share) device(id string) *Device {
	for i := range s.Devices {
		if s.Devices[i].ID == id {
			return &s.Devices[i]
		}
	}
	return nil
}

// newID is prefix + 8 random bytes in hex.
func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return prefix + hex.EncodeToString(b[:])
}

// randomBytes returns n bytes from crypto/rand.
func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// secretHash is the stored form of a device secret.
func secretHash(secret []byte) string {
	sum := sha256.Sum256(secret)
	return hex.EncodeToString(sum[:])
}

// secretMatches compares a presented secret with the stored hash in
// constant time.
func secretMatches(secret []byte, stored string) bool {
	want, err := hex.DecodeString(stored)
	if err != nil || len(want) != sha256.Size {
		return false
	}
	got := sha256.Sum256(secret)
	return subtle.ConstantTimeCompare(got[:], want) == 1
}

// CookieName is the device cookie. The __Host- prefix makes the browser
// insist on Secure, Path=/ and no Domain, binding it to this host and port.
const CookieName = "__Host-picmover"

// CookieMaxAge is the cookie lifetime in seconds: 400 days, Chrome's cap.
const CookieMaxAge = 34560000

// secretLen is the device secret's length in bytes (256 bits).
const secretLen = 32

// CookieHeader is the Set-Cookie value for a device credential.
func CookieHeader(value string) string {
	return CookieName + "=" + value + "; Path=/; Secure; HttpOnly; SameSite=Strict; Max-Age=" + fmt.Sprint(CookieMaxAge)
}

// cookieValue is "<device id>.<base64url secret>".
func cookieValue(id string, secret []byte) string {
	return id + "." + base64.RawURLEncoding.EncodeToString(secret)
}

// parseCookie splits a cookie value into the device id and the secret;
// ok is false for anything malformed.
func parseCookie(v string) (id string, secret []byte, ok bool) {
	id, enc, found := strings.Cut(v, ".")
	if !found || !validDeviceID(id) || len(enc) != base64.RawURLEncoding.EncodedLen(secretLen) {
		return "", nil, false
	}
	secret, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || len(secret) != secretLen {
		return "", nil, false
	}
	return id, secret, true
}

// validDeviceID reports the shape "d_" + 16 lower-case hex digits.
func validDeviceID(id string) bool { return validID(id, "d_") }

// ValidShareID reports the shape "s_" + 16 lower-case hex digits.
func ValidShareID(id string) bool { return validID(id, "s_") }

func validID(id, prefix string) bool {
	if len(id) != len(prefix)+16 || !strings.HasPrefix(id, prefix) {
		return false
	}
	for _, c := range id[len(prefix):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ErrInvalidName: a share or device name that is empty after cleaning.
var ErrInvalidName = errors.New("invalid name: want 1-60 characters")

// CleanName trims s, drops control and format characters and cuts it to
// 60 characters; "" when nothing is left.
func CleanName(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r == utf8.RuneError || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if n == maxNameRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// BrowserLabel is a coarse label from a User-Agent ("Safari on iPhone",
// "Chrome on Android", "Sailfish Browser"), for recognising a device in
// the list. It is never used for a decision.
func BrowserLabel(ua string) string {
	has := func(s string) bool { return strings.Contains(ua, s) }
	var device string
	switch {
	case has("Sailfish"):
		return "Sailfish Browser"
	case has("iPhone"):
		device = "iPhone"
	case has("iPad"):
		device = "iPad"
	case has("Android"):
		device = "Android"
	case has("Windows"):
		device = "Windows"
	case has("Macintosh"):
		device = "Mac"
	case has("Linux"):
		device = "Linux"
	}
	var browser string
	switch {
	case has("Edg/"):
		browser = "Edge"
	case has("SamsungBrowser"):
		browser = "Samsung Internet"
	case has("Firefox/"), has("FxiOS"):
		browser = "Firefox"
	case has("Chrome/"), has("CriOS"):
		browser = "Chrome"
	case has("Safari/"):
		browser = "Safari"
	}
	switch {
	case browser != "" && device != "":
		return browser + " on " + device
	case browser != "":
		return browser
	case device != "":
		return device
	}
	return "Browser"
}

// DefaultDeviceName is the name suggested for a device with no name.
const DefaultDeviceName = "Phone"
