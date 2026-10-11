//go:build windows

package shell

import (
	"log/slog"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
)

const (
	wmQueryEndSession = 0x0011
	wmEndSession      = 0x0016
	wsPopup           = 0x80000000
	wsExToolWindow    = 0x00000080
)

// wndClassEx is WNDCLASSEXW.
type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   windows.Handle
	icon       windows.Handle
	cursor     windows.Handle
	background windows.Handle
	menuName   *uint16
	className  *uint16
	iconSm     windows.Handle
}

// winMsg is MSG.
type winMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
	private uint32
}

var (
	sessionEndFn   func()
	sessionWndProc = windows.NewCallback(sessionProc)
)

// watchSessionEnd runs onEnd (synchronously, before Windows goes on)
// when the user logs off or Windows shuts down or restarts. A GUI
// process gets no console control events, so it listens for
// WM_QUERYENDSESSION / WM_ENDSESSION with a hidden window of its own on
// a locked thread, present in every mode (with or without the WebView2
// window, --browser included). It is a hidden top-level window, not a
// message-only one: message-only windows (HWND_MESSAGE) do not receive
// these broadcasts. WS_EX_TOOLWINDOW keeps it off the taskbar and
// Alt+Tab; it is never shown.
func watchSessionEnd(log *slog.Logger, onEnd func()) {
	sessionEndFn = onEnd
	ready := make(chan struct{})
	go func() {
		runtime.LockOSThread() // never unlocked: the thread serves the window until the process ends
		defer close(ready)
		var inst windows.Handle
		_ = windows.GetModuleHandleEx(0, nil, &inst)
		cls, _ := windows.UTF16PtrFromString("picmoverSessionWatcher")
		wc := wndClassEx{wndProc: sessionWndProc, instance: inst, className: cls}
		wc.size = uint32(unsafe.Sizeof(wc))
		if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
			log.Warn("cannot watch for logoff and shutdown", "err", err)
			return
		}
		title, _ := windows.UTF16PtrFromString("picmover")
		hwnd, _, err := procCreateWindowExW.Call(wsExToolWindow, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
			wsPopup, 0, 0, 0, 0, 0, 0, uintptr(inst), 0)
		if hwnd == 0 {
			log.Warn("cannot watch for logoff and shutdown", "err", err)
			return
		}
		ready <- struct{}{}
		var m winMsg
		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 {
				return
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
	<-ready
}

// sessionProc answers WM_QUERYENDSESSION with TRUE (the app does not
// block a logoff) and quits on WM_ENDSESSION when the session really
// ends (wParam != 0), before returning: Windows may end the process
// right after.
func sessionProc(hwnd, msg, wp, lp uintptr) uintptr {
	switch msg {
	case wmQueryEndSession:
		return 1
	case wmEndSession:
		if wp != 0 && sessionEndFn != nil {
			sessionEndFn()
		}
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}
