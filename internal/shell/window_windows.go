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

	"picmover/internal/server"
)

// iconResourceID is the RT_GROUP_ICON id of the application icon in
// winres/winres.json ("#1"), used for the window's icon.
const iconResourceID = 1

var (
	procSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	procShowWindow            = user32.NewProc("ShowWindow")
	procIsIconic              = user32.NewProc("IsIconic")
	procIsZoomed              = user32.NewProc("IsZoomed")
	procIsWindowVisible       = user32.NewProc("IsWindowVisible")
	procGetWindowRect         = user32.NewProc("GetWindowRect")
	procSetWindowLongPtrW     = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW       = user32.NewProc("CallWindowProcW")
	procPostMessageW          = user32.NewProc("PostMessageW")
	procFlashWindow           = user32.NewProc("FlashWindow")
	procMessageBoxW           = user32.NewProc("MessageBoxW")
	procBringWindowToTop      = user32.NewProc("BringWindowToTop")
	procDestroyWindow         = user32.NewProc("DestroyWindow")
	procSystemParametersInfoW = user32.NewProc("SystemParametersInfoW")
	procGetSystemMetrics      = user32.NewProc("GetSystemMetrics")
	procSetWindowsHookExW     = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx   = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx        = user32.NewProc("CallNextHookEx")
)

const (
	wmClose        = 0x0010
	swHide         = 0
	swShow         = 5
	swRestore      = 9
	gwlpWndProc    = ^uintptr(3) // GWLP_WNDPROC = -4
	mbYesNo        = 0x00000004
	mbIconWarning  = 0x00000030
	mbIconInfo     = 0x00000040
	mbSetFg        = 0x00010000
	idYes          = 6
	spiGetWorkArea = 0x0030
	smCxScreen     = 0
	smCyScreen     = 1
	whCBT          = 5
	hcbtCreateWnd  = 3
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
// is destroyed only at quit. When its creation fails (Embed false, or no
// answer within windowCreateTimeout) it stays failed: Open returns that
// error and the app uses the browser for the rest of the run.
//
// go-webview2 still calls log.Fatal in a few of its own paths (a failed
// environment or controller creation callback, settings it cannot set);
// that text goes to the log file (the standard logger writes there) and
// the process ends. The data folder is checked first (probeDataDir), the
// most likely cause; the library is not forked for the rest because the
// remaining causes (a broken runtime, policy blocks) are rare and a fork
// would have to be kept up to date with its COM bindings.
type webviewWindow struct {
	cfg windowConfig

	mu       sync.Mutex
	wv       webview2.WebView
	hwnd     uintptr
	starting bool
	quitting bool          // Close was called: WM_CLOSE destroys the window
	failed   error         // creation failed: never tried again
	ended    chan struct{} // closed when the window's thread has ended
}

// theWindow is the window the subclass procedure serves.
var (
	theWindow    *webviewWindow
	origWndProc  uintptr
	subclassProc = windows.NewCallback(windowProc)
)

func newNativeWindow(cfg windowConfig) (nativeWindow, string, string) {
	v := webView2Version()
	if v == "" {
		return nil, server.WindowNoRuntime, "the WebView2 runtime is not installed (https://go.microsoft.com/fwlink/p/?LinkId=2124703)"
	}
	cfg.log.Info("WebView2 runtime found", "version", v)
	if err := probeDataDir(cfg.dataPath); err != nil {
		return nil, server.WindowFailed, err.Error()
	}
	w := &webviewWindow{cfg: cfg}
	theWindow = w
	return w, server.WindowOK, ""
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
// first time; it waits at most windowCreateTimeout for the creation.
func (w *webviewWindow) Open(url, nav string) error {
	w.mu.Lock()
	switch {
	case w.failed != nil:
		err := w.failed
		w.mu.Unlock()
		return err
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
	select {
	case err := <-errc:
		return err
	case <-time.After(windowCreateTimeout):
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.wv != nil {
		return nil // ready just now
	}
	// The loop may still finish later: it then sees failed and destroys
	// the window.
	w.failed = fmt.Errorf("the WebView2 window was not ready within %v", windowCreateTimeout)
	return w.failed
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
	defer windows.CoUninitialize()
	x, y, cw, ch := placeWindow(w.cfg.size(), workArea())
	placed := placeNextWindow(x, y, cw, ch)
	wv := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  w.cfg.dataPath,
		WindowOptions: webview2.WindowOptions{
			// Center is the fallback when the placement hook is missing:
			// the size is clamped either way.
			Title: "picmover", Width: uint(cw), Height: uint(ch),
			IconId: iconResourceID, Center: true,
		},
	})
	created := placed()
	if wv == nil {
		// CreateWithOptions showed the window before Embed failed: do not
		// leave it blank on the screen.
		if created != 0 {
			procDestroyWindow.Call(created)
		}
		err := errors.New("the WebView2 window could not be created")
		w.mu.Lock()
		w.starting = false
		if w.failed == nil {
			w.failed = err
		}
		w.mu.Unlock()
		errc <- err
		return
	}
	hwnd := uintptr(wv.Window())
	w.mu.Lock()
	if w.failed != nil || w.quitting {
		// Open gave up waiting, or the app quit meanwhile.
		w.starting = false
		w.mu.Unlock()
		wv.Destroy()
		wv.Run() // until the window is destroyed
		return
	}
	w.mu.Unlock()
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
}

// workArea is the primary monitor's work area (the screen without the
// taskbar), where the window is placed (go-webview2 centres on the
// primary monitor too).
func workArea() rect {
	var r rect
	if ok, _, _ := procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0); ok != 0 && r.Right > r.Left && r.Bottom > r.Top {
		return r
	}
	cx, _, _ := procGetSystemMetrics.Call(smCxScreen)
	cy, _, _ := procGetSystemMetrics.Call(smCyScreen)
	return rect{Right: int32(cx), Bottom: int32(cy)}
}

// cbtCreateWnd and createStruct are CBT_CREATEWNDW and CREATESTRUCTW.
type cbtCreateWnd struct {
	cs          *createStruct
	insertAfter uintptr
}

type createStruct struct {
	createParams, instance, menu, parent uintptr
	cy, cx, y, x                         int32
	style                                int32
	name, class                          *uint16
	exStyle                              uint32
}

// The placement of the next top-level window created on the window
// thread (placeNextWindow); only that thread touches these.
var (
	placeHook    uintptr
	placeAt      [4]int32 // x, y, width, height
	placeCreated uintptr
	placeProc    = windows.NewCallback(cbtProc)
)

// placeNextWindow sets the position and size of the next top-level
// window this thread creates (go-webview2's, which can only centre on
// the whole screen), through a thread-local CBT hook that edits its
// CREATESTRUCT before it appears. The returned function removes the hook
// and returns that window's handle (0 when none was created).
func placeNextWindow(x, y, w, h int32) (done func() uintptr) {
	placeAt, placeCreated = [4]int32{x, y, w, h}, 0
	placeHook, _, _ = procSetWindowsHookExW.Call(whCBT, placeProc, 0, uintptr(windows.GetCurrentThreadId()))
	return func() uintptr {
		if placeHook != 0 {
			procUnhookWindowsHookEx.Call(placeHook)
			placeHook = 0
		}
		return placeCreated
	}
}

func cbtProc(code, wp, lp uintptr) uintptr {
	if int32(code) == hcbtCreateWnd && placeCreated == 0 {
		if c := (*cbtCreateWnd)(pointerOf(lp)); c != nil && c.cs != nil && c.cs.parent == 0 {
			c.cs.x, c.cs.y, c.cs.cx, c.cs.cy = placeAt[0], placeAt[1], placeAt[2], placeAt[3]
			placeCreated = wp
		}
	}
	r, _, _ := procCallNextHookEx.Call(placeHook, code, wp, lp)
	return r
}

// pointerOf turns an address Windows passed in a message parameter into
// a pointer.
func pointerOf(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

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
	var r rect
	if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return WindowSize{}
	}
	return WindowSize{Width: int(r.Right - r.Left), Height: int(r.Bottom - r.Top)}
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

// confirmQuit asks whether to quit while jobs run, in a message box owned
// by the window. The window is restored and raised first: a box owned by
// a minimised window would be hidden with it.
func confirmQuit(win nativeWindow, jobs int) bool {
	owner := win.Handle()
	focusWindow(owner)
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
