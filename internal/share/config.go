// Package share holds the security-critical parts of sharing on the home
// Wi-Fi (docs/design/lan-share.md): the per-user settings with the shares
// and their paired devices, the scope data, pairing tokens and codes,
// rate limits, the local CA and server certificate, LAN address
// detection and the connection filter, and QR codes. It does not depend
// on internal/server, which serves the share listener on top of it.
package share

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"picmover/internal/library"
)

// File names in the share directory.
const (
	ConfigName = "share.json"
	LockName   = "share.lock"
	damaged    = ".damaged"
)

// configVersion is share.json's format; it changes only for incompatible
// changes (a new scope kind is a new kind value, not a new version).
const configVersion = 1

// DefaultPort is the share listener's default port.
const DefaultPort = 7443

// Auto-off modes.
const (
	AutoOffNever = "never"
	AutoOff1h    = "1h"
	AutoOff4h    = "4h"
	AutoOffQuit  = "quit" // on until picmover quits (the default)
)

// ValidAutoOff reports whether s is an auto-off mode.
func ValidAutoOff(s string) bool {
	return s == AutoOffNever || s == AutoOff1h || s == AutoOff4h || s == AutoOffQuit
}

// Home is the network sharing was turned on in: the bound interface's
// subnet and the default gateway ("" when there is none).
type Home struct {
	Subnet  string `json:"subnet"`
	Gateway string `json:"gateway"`
}

// Config is share.json.
type Config struct {
	Version   int    `json:"version"`
	Port      int    `json:"port"`
	Interface string `json:"interface"`
	KeepOn    bool   `json:"keepOn"`
	AutoOff   string `json:"autoOff"`
	MDNS      bool   `json:"mdns"`
	Home      *Home  `json:"home"`
	// Shares: never nil after load.
	Shares []*Share `json:"shares"`
}

// defaultConfig is the configuration when there is no file.
func defaultConfig() *Config {
	return &Config{Version: configVersion, Port: DefaultPort, AutoOff: AutoOffQuit, Shares: []*Share{}}
}

// normalize replaces out-of-range settings with the defaults.
func (c *Config) normalize() {
	c.Version = configVersion
	if c.Port <= 0 || c.Port > 65535 {
		c.Port = DefaultPort
	}
	if !ValidAutoOff(c.AutoOff) {
		c.AutoOff = AutoOffQuit
	}
	if c.Shares == nil {
		c.Shares = []*Share{}
	}
	c.Shares = slices.DeleteFunc(c.Shares, func(s *Share) bool { return s == nil })
	for _, s := range c.Shares {
		if s.Devices == nil {
			s.Devices = []Device{}
		}
	}
}

// clone is a deep copy (through JSON, which is what the file holds).
func (c *Config) clone() *Config {
	b, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	out := &Config{}
	if err := json.Unmarshal(b, out); err != nil {
		panic(err)
	}
	return out
}

// Share returns the share with id, or nil.
func (c *Config) Share(id string) *Share {
	for _, s := range c.Shares {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// Lookup returns the share named by ref: its id, or else its name
// (case-insensitively; names are unique).
func (c *Config) Lookup(ref string) *Share {
	if s := c.Share(ref); s != nil {
		return s
	}
	for _, s := range c.Shares {
		if strings.EqualFold(s.Name, strings.TrimSpace(ref)) {
			return s
		}
	}
	return nil
}

// Device returns the device with id and its share, or nils.
func (c *Config) Device(id string) (*Share, *Device) {
	for _, s := range c.Shares {
		if d := s.device(id); d != nil {
			return s, d
		}
	}
	return nil, nil
}

// Errors of the share operations.
var (
	ErrNotFound    = errors.New("no such share")
	ErrNoDevice    = errors.New("no such device")
	ErrNameTaken   = errors.New("a share with this name already exists")
	ErrTooMany     = fmt.Errorf("at most %d shares; delete one first", MaxShares)
	ErrTooManyDevs = fmt.Errorf("this share already has %d phones; revoke a phone first", MaxDevicesPerShare)
)

// nameTaken reports whether another share than except has name.
func (c *Config) nameTaken(name, except string) bool {
	for _, s := range c.Shares {
		if s.ID != except && strings.EqualFold(s.Name, name) {
			return true
		}
	}
	return false
}

// NewShare is what AddShare creates.
type NewShare struct {
	Name         string
	Scope        Scope
	Capabilities *Capabilities // nil: the kind's defaults
	ExpiresAt    *time.Time    // nil: never
	Approve      bool
}

// AddShare validates and adds a share; it returns the new share.
func (c *Config) AddShare(n NewShare, now time.Time) (*Share, error) {
	name := CleanName(n.Name)
	if name == "" {
		return nil, ErrInvalidName
	}
	if err := n.Scope.Validate(); err != nil {
		return nil, err
	}
	if c.nameTaken(name, "") {
		return nil, ErrNameTaken
	}
	if len(c.Shares) >= MaxShares {
		return nil, ErrTooMany
	}
	caps := DefaultCapabilities(n.Scope.Kind)
	if n.Capabilities != nil {
		caps = *n.Capabilities
	}
	s := &Share{ID: newID("s_"), Name: name, Scope: n.Scope, Capabilities: caps, ExpiresAt: n.ExpiresAt,
		Approve: n.Approve, CreatedAt: now.Truncate(time.Second), Devices: []Device{}}
	c.Shares = append(c.Shares, s)
	return s, nil
}

// Rename renames share s.
func (c *Config) Rename(s *Share, name string) error {
	name = CleanName(name)
	if name == "" {
		return ErrInvalidName
	}
	if c.nameTaken(name, s.ID) {
		return ErrNameTaken
	}
	s.Name = name
	return nil
}

// DeleteShare removes the share with id and its devices.
func (c *Config) DeleteShare(id string) error {
	n := len(c.Shares)
	c.Shares = slices.DeleteFunc(c.Shares, func(s *Share) bool { return s.ID == id })
	if len(c.Shares) == n {
		return ErrNotFound
	}
	return nil
}

// RemoveDevice removes the device with id.
func (c *Config) RemoveDevice(id string) error {
	s, _ := c.Device(id)
	if s == nil {
		return ErrNoDevice
	}
	s.Devices = slices.DeleteFunc(s.Devices, func(d Device) bool { return d.ID == id })
	return nil
}

// Sweep deletes shares expired for more than 30 days and devices unused
// for 400 days; it reports what it removed (for the log).
func (c *Config) Sweep(now time.Time) (removed []string) {
	c.Shares = slices.DeleteFunc(c.Shares, func(s *Share) bool {
		if s.ExpiresAt != nil && now.Sub(*s.ExpiresAt) > expiredKeep {
			removed = append(removed, fmt.Sprintf("share %q (expired %s)", s.Name, s.ExpiresAt.Format(time.DateOnly)))
			return true
		}
		return false
	})
	for _, s := range c.Shares {
		s.Devices = slices.DeleteFunc(s.Devices, func(d Device) bool {
			last := d.LastSeen
			if last.IsZero() {
				last = d.PairedAt
			}
			if now.Sub(last) > deviceIdle {
				removed = append(removed, fmt.Sprintf("device %q of share %q (unused since %s)", d.Name, s.Name, last.Format(time.DateOnly)))
				return true
			}
			return false
		})
	}
	return removed
}

// DefaultDir is os.UserConfigDir()/picmover/share.
func DefaultDir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "picmover", "share"), nil
}

// ErrManaged is returned when another process holds share.lock: it shares
// now, and only it changes share.json.
type ErrManaged struct {
	PID int
	Cmd string
}

func (e *ErrManaged) Error() string {
	who := "another picmover process"
	if e.PID > 0 {
		who = fmt.Sprintf("picmover pid %d", e.PID)
		if e.Cmd != "" {
			who += " (" + e.Cmd + ")"
		}
	}
	return "sharing is managed by " + who + "; use its Share page"
}

// Manager owns share.json for one process. While the process shares
// (Hold), it keeps share.lock and the settings in memory, and every
// change is saved at once; device activity (last seen) is saved at most
// every flushEvery and at Release. A process that does not share reads
// the file for every snapshot, and changes it only under a brief hold of
// share.lock, so it never writes underneath a process that shares.
type Manager struct {
	dir string
	log *slog.Logger
	now func() time.Time

	mu      sync.Mutex
	cfg     *Config
	unlock  func() // non-nil while this process holds share.lock
	dirty   bool   // last-seen updates not saved yet
	flushed time.Time
}

// flushEvery bounds how often device activity is written.
const flushEvery = 5 * time.Minute

// Open opens the share directory dir, creating it (0700) when missing,
// and reads share.json. A damaged file is kept as share.json.damaged and
// replaced by the defaults (no shares: phones pair again), logged at
// Error: failing closed. A file of a newer format is an error.
func Open(dir string, log *slog.Logger) (*Manager, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(dir); err == nil && fi.Mode().Perm()&0o077 != 0 {
			if err := os.Chmod(dir, 0o700); err != nil {
				return nil, fmt.Errorf("%s is accessible by other users and cannot be fixed: %w", dir, err)
			}
		}
	}
	m := &Manager{dir: dir, log: log, now: time.Now}
	cfg, err := m.read()
	if err != nil {
		return nil, err
	}
	m.cfg = cfg
	return m, nil
}

// SetClock replaces the clock (tests).
func (m *Manager) SetClock(now func() time.Time) {
	m.mu.Lock()
	m.now = now
	m.mu.Unlock()
}

// Now is the manager's clock.
func (m *Manager) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now()
}

// Dir is the share directory.
func (m *Manager) Dir() string { return m.dir }

func (m *Manager) path() string { return filepath.Join(m.dir, ConfigName) }

// read loads share.json (defaults when absent or damaged).
func (m *Manager) read() (*Config, error) {
	p := m.path()
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return defaultConfig(), nil
	}
	if err != nil {
		return nil, err
	}
	var probe struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(b, &probe) == nil && probe.Version > configVersion {
		return nil, fmt.Errorf("%s was written by a newer picmover (format %d); upgrade picmover", p, probe.Version)
	}
	cfg := defaultConfig()
	if err := json.Unmarshal(b, cfg); err != nil {
		m.log.Error("share settings are damaged; sharing starts from no shares (phones must pair again)",
			"path", p, "kept", p+damaged, "err", err)
		if rerr := os.Rename(p, p+damaged); rerr != nil {
			return nil, fmt.Errorf("%s is damaged (%v) and cannot be set aside: %w", p, err, rerr)
		}
		return defaultConfig(), nil
	}
	cfg.normalize()
	return cfg, nil
}

// write saves cfg (0600, atomically).
func (m *Manager) write(cfg *Config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(m.path(), append(b, '\n'), 0o600)
}

// lock takes share.lock for cmd. Another holder is an *ErrManaged; a file
// system without locking goes on without it (a warning in the log).
func (m *Manager) lock(cmd string) (func(), error) {
	unlock, err := library.LockFile(filepath.Join(m.dir, LockName), cmd)
	var locked *library.ErrLocked
	switch {
	case errors.As(err, &locked):
		return nil, &ErrManaged{PID: locked.PID, Cmd: locked.Cmd}
	case errors.Is(err, library.ErrLockUnsupported):
		m.log.Warn("cannot lock the share settings; going on without the lock", "err", err)
		return unlock, nil
	}
	return unlock, err
}

// Hold takes share.lock for the sharing process (cmd names it to the
// others), reloads share.json and sweeps expired shares and idle devices.
// Holding twice is a no-op.
func (m *Manager) Hold(cmd string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unlock != nil {
		return nil
	}
	unlock, err := m.lock(cmd)
	if err != nil {
		return err
	}
	cfg, err := m.read()
	if err != nil {
		unlock()
		return err
	}
	m.cfg, m.unlock = cfg, unlock
	if removed := cfg.Sweep(m.now()); len(removed) > 0 {
		for _, r := range removed {
			m.log.Info("share settings: removed " + r)
		}
		if err := m.write(cfg); err != nil {
			m.log.Error("cannot save the share settings", "err", err)
		}
	}
	m.flushed = m.now()
	return nil
}

// Release saves pending device activity and releases share.lock.
func (m *Manager) Release() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unlock == nil {
		return
	}
	m.flushLocked()
	m.unlock()
	m.unlock = nil
}

// Holding reports whether this process holds share.lock.
func (m *Manager) Holding() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.unlock != nil
}

// Managed returns the other process holding share.lock, or nil when it is
// free or held by this process. It probes by taking the lock briefly.
func (m *Manager) Managed() *ErrManaged {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unlock != nil {
		return nil
	}
	unlock, err := m.lock("probe")
	var me *ErrManaged
	if errors.As(err, &me) {
		return me
	}
	if unlock != nil {
		unlock()
	}
	return nil
}

// Snapshot returns a copy of the settings: the in-memory ones while this
// process holds the lock, else freshly read from the file (another
// process may have changed it).
func (m *Manager) Snapshot() (*Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unlock == nil {
		cfg, err := m.read()
		if err != nil {
			return nil, err
		}
		m.cfg = cfg
	}
	return m.cfg.clone(), nil
}

// Update applies f to the settings and saves them. While this process
// holds the lock, f works on the in-memory settings; otherwise the lock is
// taken for the change (an *ErrManaged when another process holds it) and
// f works on the file as it is now. f gets the manager's clock reading
// (it must not call Now, which takes the same mutex). When f fails,
// nothing changes.
func (m *Manager) Update(f func(c *Config, now time.Time) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unlock == nil {
		unlock, err := m.lock("share settings")
		if err != nil {
			return err
		}
		defer unlock()
		cfg, err := m.read()
		if err != nil {
			return err
		}
		m.cfg = cfg
	}
	next := m.cfg.clone()
	if err := f(next, m.now()); err != nil {
		return err
	}
	next.normalize()
	if err := m.write(next); err != nil {
		return err
	}
	m.cfg, m.dirty = next, false
	return nil
}

// Verified is a device and its share as Verify found them (copies).
type Verified struct {
	Device Device
	Share  Share
}

// Verify resolves a device cookie value to its device and share, comparing
// the secret's SHA-256 in constant time; ok is false for a malformed
// value, an unknown device or a wrong secret. It reads the in-memory
// settings (the sharing process's).
func (m *Manager) Verify(cookie string) (Verified, bool) {
	id, secret, ok := parseCookie(cookie)
	if !ok {
		return Verified{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, d := m.cfg.Device(id)
	if d == nil || !secretMatches(secret, d.SecretSHA256) {
		return Verified{}, false
	}
	return Verified{Device: *d, Share: *s.clone()}, true
}

// clone deep-copies a share.
func (s *Share) clone() *Share {
	c := *s
	c.Devices = slices.Clone(s.Devices)
	c.Scope.Libraries = slices.Clone(s.Scope.Libraries)
	if s.ExpiresAt != nil {
		t := *s.ExpiresAt
		c.ExpiresAt = &t
	}
	return &c
}

// Seen records a device's request from addr. It reports whether the
// device's cookie is due for renewal (older than 7 days), in which case
// the caller re-sends it and cookieIssuedAt is now. Activity is saved at
// most every 5 minutes (and at Release). first reports the device's first
// request of the day, which the caller logs.
func (m *Manager) Seen(deviceID, addr string) (renew, first bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, d := m.cfg.Device(deviceID)
	if d == nil {
		return false, false
	}
	now := m.now()
	y1, m1, d1 := d.LastSeen.Date()
	y2, m2, d2 := now.Date()
	first = d.LastSeen.IsZero() || y1 != y2 || m1 != m2 || d1 != d2
	d.LastSeen, d.LastAddress = now.Truncate(time.Second), addr
	if now.Sub(d.CookieIssuedAt) > cookieRenew {
		d.CookieIssuedAt = now.Truncate(time.Second)
		renew = true
	}
	m.dirty = true
	if m.unlock != nil && now.Sub(m.flushed) >= flushEvery {
		m.flushLocked()
	}
	return renew, first
}

// Flush saves pending device activity (while holding the lock).
func (m *Manager) Flush() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushLocked()
}

func (m *Manager) flushLocked() {
	if !m.dirty || m.unlock == nil {
		return
	}
	if err := m.write(m.cfg); err != nil {
		m.log.Error("cannot save the share settings", "err", err)
		return
	}
	m.dirty, m.flushed = false, m.now()
}

// Paired is a device created by AddDevice, with its cookie value (the
// only time the secret exists outside the phone).
type Paired struct {
	Device Device
	Share  Share
	Cookie string // the Set-Cookie value (CookieHeader)
}

// AddDevice pairs a new device with share shareID and saves: name is
// cleaned ("Phone" when empty), the secret is 256 random bits stored only
// as its SHA-256. replace, when not "", is a device (of any share)
// removed in the same save. Fails with ErrNotFound, ErrTooManyDevs, or a
// share state error when the share is not active.
func (m *Manager) AddDevice(shareID, name, browser, addr, replace string) (Paired, error) {
	var out Paired
	err := m.Update(func(c *Config, now time.Time) error {
		s := c.Share(shareID)
		if s == nil {
			return ErrNotFound
		}
		if st := s.State(now); st != StateActive {
			return &StateError{State: st}
		}
		if replace != "" {
			_ = c.RemoveDevice(replace)
		}
		if len(s.Devices) >= MaxDevicesPerShare {
			return ErrTooManyDevs
		}
		if name = CleanName(name); name == "" {
			name = DefaultDeviceName
		}
		secret := randomBytes(secretLen)
		now = now.Truncate(time.Second)
		d := Device{ID: newID("d_"), Name: name, SecretSHA256: secretHash(secret), PairedAt: now,
			LastSeen: now, LastAddress: addr, Browser: browser, CookieIssuedAt: now}
		s.Devices = append(s.Devices, d)
		out = Paired{Device: d, Share: *s.clone(), Cookie: CookieHeader(cookieValue(d.ID, secret))}
		return nil
	})
	return out, err
}

// CookieFor is the Set-Cookie value that renews a device's cookie: the
// value the phone sent, with a fresh Max-Age.
func CookieFor(value string) string { return CookieHeader(value) }

// StateError is a share that is not active (paused, expired, unknown).
type StateError struct{ State string }

func (e *StateError) Error() string { return "the share is " + e.State }
