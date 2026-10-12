package shell

import (
	"fmt"
	"os"
	"time"
)

// The native window's smallest size when it is placed at start (outer
// size, physical pixels); the work area wins over the saved size, this
// minimum over the work area.
const (
	placeMinWidth  = 640
	placeMinHeight = 480
)

// windowCreateTimeout bounds the wait for the native window: WebView2
// reports through callbacks that might never come.
const windowCreateTimeout = 20 * time.Second

// rect is a screen rectangle (left, top inclusive; right, bottom
// exclusive), as Win32's RECT.
type rect struct{ Left, Top, Right, Bottom int32 }

// placeWindow fits the saved size into the work area (a window saved on a
// bigger monitor would otherwise start partly off screen, its title bar
// out of reach), with a minimum of 640×480, and centres it on the area.
func placeWindow(saved WindowSize, area rect) (x, y, w, h int32) {
	aw, ah := area.Right-area.Left, area.Bottom-area.Top
	w, h = int32(saved.Width), int32(saved.Height)
	if aw > 0 && w > aw {
		w = aw
	}
	if ah > 0 && h > ah {
		h = ah
	}
	w, h = max(w, placeMinWidth), max(h, placeMinHeight)
	x = area.Left + max((aw-w)/2, 0)
	y = area.Top + max((ah-h)/2, 0)
	return x, y, w, h
}

// probeDataDir makes sure the WebView2 user data folder can be created
// and written: WebView2 ends the process (go-webview2 calls log.Fatal)
// when it cannot use it, so the app checks first and uses the browser
// instead.
func probeDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("WebView2 data folder: %w", err)
	}
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return fmt.Errorf("WebView2 data folder not writable: %w", err)
	}
	name := f.Name()
	_, werr := f.Write([]byte("picmover\n"))
	cerr := f.Close()
	os.Remove(name)
	if werr != nil {
		return fmt.Errorf("WebView2 data folder not writable: %w", werr)
	}
	if cerr != nil {
		return fmt.Errorf("WebView2 data folder not writable: %w", cerr)
	}
	return nil
}
