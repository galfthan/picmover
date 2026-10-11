//go:build windows

package shell

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// iconResourceID is the RT_GROUP_ICON id of the application icon in
// winres/winres.json ("#1"), used for the window's icon.
const iconResourceID = 1

var (
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procShowWindow          = user32.NewProc("ShowWindow")
	procIsIconic            = user32.NewProc("IsIconic")
	procIsZoomed            = user32.NewProc("IsZoomed")
	procIsWindowVisible     = user32.NewProc("IsWindowVisible")
	procGetWindowRect       = user32.NewProc("GetWindowRect")
	procSetWindowLongPtrW   = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW     = user32.NewProc("CallWindowProcW")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procFlashWindow         = user32.NewProc("FlashWindow")
	procMessageBoxW         = user32.NewProc("MessageBoxW")
	procBringWindowToTop    = user32.NewProc("BringWindowToTop")
)

const (
	wmClose       = 0x0010
	swHide        = 0
	swShow        = 5
	swRestore     = 9
	gwlpWndProc   = ^uintptr(3) // GWLP_WNDPROC = -4
	mbYesNo       = 0x00000004
	mbIconWarning = 0x00000030
	mbIconInfo    = 0x00000040
	mbSetFg       = 0x00010000
	idYes         = 6
)

// webView2ClientKey is the EdgeUpdate client id of the WebView2 runtime.
const webView2ClientKey = `Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`

// webView2Version returns the installed WebView2 runtime version ("" when
// absent): pv under HKLM\SOFTWARE\WOW6432Node\..., then HKCU, then the
// paths without WOW6432Node; present when not empty and not 0.0.0.0.
func webView2Version() string {
	for _, k := range []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\` + webView2ClientKey},
		{registry.CURRENT_USER, `SOFTWARE\WOW6432Node\` + webView2ClientKey},
		{registry.LOCAL_MACHINE, `SOFTWARE\` + webView2ClientKey},
		{registry.CURRENT_USER, `SOFTWARE\` + webView2ClientKey},
	} {
		key, err := registry.OpenKey(k.root, k.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		pv, _, err := key.GetStringValue("pv")
		key.Close()
		if err == nil && pv != "" && pv != "0.0.0.0" {
			return pv
		}
	}
	return ""
}

// webviewWindow is the WebView2 window. There is at most one per process:
// closing it hides it (the page keeps running), Open shows it again; it
// is destroyed only at quit.
type webviewWindow struct {
	cfg windowConfig

	mu       sync.Mutex
	wv       webview2.WebView
	hwnd     uintptr
	starting bool
	quitting bool          // Close was called: WM_CLOSE destroys the window
	ended    chan struct{} // closed when the window's thread has ended
}

// theWindow is the window the subclass procedure serves.
var (
	theWindow    *webviewWindow
	origWndProc  uintptr
	subclassProc = windows.NewCallback(windowProc)
)

func newNativeWindow(cfg windowConfig) (nativeWindow, bool, string) {
	v := webView2Version()
	if v == "" {
		return nil, false, "the WebView2 runtime is not installed (https://go.microsoft.com/fwlink/p/?LinkId=2124703)"
	}
	cfg.log.Info("WebView2 runtime found", "version", v)
	w := &webviewWindow{cfg: cfg}
	theWindow = w
	return w, true, ""
}

func (w *webviewWindow) IsOpen() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.wv == nil {
		return false
	}
	r, _, _ := procIsWindowVisible.Call(w.hwnd)
	return r != 0
}

func (w *webviewWindow) Handle() uintptr {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hwnd
}

// Open shows the window, creating it on its own locked OS thread the
// first time.
func (w *webviewWindow) Open(url, nav string) error {
	w.mu.Lock()
	switch {
	case w.quitting:
		w.mu.Unlock()
		return nil
	case w.wv != nil:
		wv, hwnd := w.wv, w.hwnd
		w.mu.Unlock()
		if nav != "" {
			wv.Dispatch(func() { wv.Navigate(nav) })
		}
		focusWindow(hwnd)
		return nil
	case w.starting:
		w.mu.Unlock()
		return nil
	}
	w.starting = true
	w.ended = make(chan struct{})
	ended := w.ended
	w.mu.Unlock()
	errc := make(chan error, 1)
	go w.loop(url, errc, ended)
	return <-errc
}

// loop creates the window and runs its message loop until it is
// destroyed. COM is initialised on this thread (the WebView2 package
// initialises only the main thread, which the tray uses).
func (w *webviewWindow) loop(url string, errc chan<- error, ended chan struct{}) {
	runtime.LockOSThread() // never unlocked: the thread ends with the goroutine
	defer close(ended)
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil {
		w.cfg.log.Warn("CoInitializeEx", "err", err)
	}
	size := w.cfg.size()
	wv := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  w.cfg.dataPath,
		WindowOptions: webview2.WindowOptions{
			Title: "picmover", Width: uint(size.Width), Height: uint(size.Height),
			IconId: iconResourceID, Center: true,
		},
	})
	if wv == nil {
		w.mu.Lock()
		w.starting = false
		w.mu.Unlock()
		errc <- errors.New("the WebView2 window could not be created")
		return
	}
	hwnd := uintptr(wv.Window())
	// Ctrl+W closes the window, as in a browser.
	_ = wv.Bind("picmoverCloseWindow", func() {
		procPostMessageW.Call(hwnd, wmClose, 0, 0)
	})
	wv.Init(`document.addEventListener("keydown", function (e) {
  if (e.ctrlKey && !e.altKey && !e.shiftKey && (e.key === "w" || e.key === "W")) {
    e.preventDefault();
    window.picmoverCloseWindow();
  }
}, true);`)
	origWndProc, _, _ = procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, subclassProc)
	w.mu.Lock()
	w.wv, w.hwnd, w.starting = wv, hwnd, false
	w.mu.Unlock()
	wv.Navigate(url)
	focusWindow(hwnd)
	errc <- nil
	wv.Run()
	w.mu.Lock()
	w.wv, w.hwnd = nil, 0
	w.mu.Unlock()
	windows.CoUninitialize()
}

// windowProc subclasses the WebView2 window: a close by the user (the
// close button, Alt+F4, Ctrl+W) hides it and tells the app, which keeps
// running in the tray or quits; at quit the close goes through and
// destroys the window.
func windowProc(hwnd, msg, wp, lp uintptr) uintptr {
	if w := theWindow; msg == wmClose && w != nil {
		w.mu.Lock()
		quitting := w.quitting
		w.mu.Unlock()
		if !quitting {
			size := windowSize(hwnd)
			procShowWindow.Call(hwnd, swHide)
			go w.cfg.onClosed(size)
			return 0
		}
	}
	r, _, _ := procCallWindowProcW.Call(origWndProc, hwnd, msg, wp, lp)
	return r
}

// windowSize is the window's outer size, or zero when it is minimised or
// maximised (the saved size is the normal one).
func windowSize(hwnd uintptr) WindowSize {
	if r, _, _ := procIsIconic.Call(hwnd); r != 0 {
		return WindowSize{}
	}
	if r, _, _ := procIsZoomed.Call(hwnd); r != 0 {
		return WindowSize{}
	}
	var rect struct{ Left, Top, Right, Bottom int32 }
	if r, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect))); r == 0 {
		return WindowSize{}
	}
	return WindowSize{Width: int(rect.Right - rect.Left), Height: int(rect.Bottom - rect.Top)}
}

// Close destroys the window (at quit) and waits up to 5 s for its thread.
func (w *webviewWindow) Close() {
	w.mu.Lock()
	w.quitting = true
	hwnd, ended := w.hwnd, w.ended
	w.mu.Unlock()
	if hwnd == 0 || ended == nil {
		return
	}
	if size := windowSize(hwnd); size.Width > 0 {
		w.cfg.onSize(size)
	}
	procPostMessageW.Call(hwnd, wmClose, 0, 0)
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		w.cfg.log.Warn("the window did not close")
	}
}

// focusWindow shows, restores and raises hwnd. Windows lets a process
// take the foreground only in some cases (it was given the right by the
// process the user acted in, see allowForeground); otherwise the taskbar
// button flashes.
func focusWindow(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	if r, _, _ := procIsWindowVisible.Call(hwnd); r == 0 {
		procShowWindow.Call(hwnd, swShow)
	}
	if r, _, _ := procIsIconic.Call(hwnd); r != 0 {
		procShowWindow.Call(hwnd, swRestore)
	}
	procBringWindowToTop.Call(hwnd)
	if r, _, _ := procSetForegroundWindow.Call(hwnd); r == 0 {
		procFlashWindow.Call(hwnd, 1)
	}
}

// confirmQuit asks whether to quit while jobs run (a message box owned by
// the window).
func confirmQuit(owner uintptr, jobs int) bool {
	what := "A job is running"
	if jobs > 1 {
		what = fmt.Sprintf("%d jobs are running", jobs)
	}
	text := what + ". Quit anyway? The job is cancelled safely; an import can be resumed by starting it again."
	r := messageBox(owner, "picmover", text, mbYesNo|mbIconWarning|mbSetFg)
	return r == idYes
}

func messageBox(owner uintptr, title, text string, flags uintptr) uintptr {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	r, _, _ := procMessageBoxW.Call(owner, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), flags)
	return r
}
