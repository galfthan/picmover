package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ConfigName is the app's settings file in the config directory.
const ConfigName = "app.json"

// DefaultPort is the port the app tries first (serve uses 7439, so both
// can run side by side). The port actually used is remembered.
const DefaultPort = 7440

// Default window size (outer size, physical pixels).
const (
	DefaultWidth  = 1280
	DefaultHeight = 800
	minWidth      = 480
	minHeight     = 360
)

// WindowSize is the native window's saved size.
type WindowSize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Config is app.json.
type Config struct {
	// Workspace is the folder served (a workspace of libraries or one
	// library); "" before the user chose one.
	Workspace string `json:"workspace"`
	// Port is the loopback port, remembered after the first run so
	// bookmarks keep working; 0 = DefaultPort.
	Port int `json:"port"`
	// CloseToTray keeps the app running in the tray when the window is
	// closed (default true); false quits.
	CloseToTray bool       `json:"closeToTray"`
	Window      WindowSize `json:"window"`
	// LastOpened is when the app last started (RFC 3339).
	LastOpened string `json:"lastOpened"`
}

// DefaultConfig is the configuration of a first run.
func DefaultConfig() Config {
	return Config{CloseToTray: true, Window: WindowSize{Width: DefaultWidth, Height: DefaultHeight}}
}

// normalize replaces out-of-range values with the defaults.
func (c *Config) normalize() {
	if c.Port < 0 || c.Port > 65535 {
		c.Port = 0
	}
	if c.Window.Width < minWidth || c.Window.Height < minHeight || c.Window.Width > 16384 || c.Window.Height > 16384 {
		c.Window = WindowSize{Width: DefaultWidth, Height: DefaultHeight}
	}
}

// LoadConfig reads dir/app.json. A missing file gives the defaults; so
// does a damaged one, with the error (the caller logs it and goes on).
// Absent fields keep their defaults.
func LoadConfig(dir string) (Config, error) {
	c := DefaultConfig()
	b, err := os.ReadFile(filepath.Join(dir, ConfigName))
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return DefaultConfig(), fmt.Errorf("%s: %w", ConfigName, err)
	}
	c.normalize()
	return c, nil
}

// SaveConfig writes dir/app.json atomically (a temporary file renamed
// over it).
func SaveConfig(dir string, c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, ConfigName), append(b, '\n'), 0o644)
}

// writeFileAtomic writes data to a temporary file next to p and renames
// it over p.
func writeFileAtomic(p string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	_ = f.Chmod(perm) // best effort: Windows ignores most modes
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	ok = true
	return nil
}
