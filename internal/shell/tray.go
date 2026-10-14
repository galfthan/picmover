//go:build windows || linux

package shell

import (
	"fmt"
	"path/filepath"
	"time"

	"fyne.io/systray"
)

// trayRefresh is how often the Quit label follows the job count.
const trayRefresh = 5 * time.Second

// trayHooks are what the tray menu does.
type trayHooks struct {
	open, openBrowser, changeWorkspace, quit func()
	jobCount                                 func() int
	workspace                                func() string
	// sharing reports sharing on the home Wi-Fi (state, address);
	// setSharing turns it on or off.
	sharing    func() (state, address string)
	setSharing func(on bool)
	wsChanged  <-chan struct{}
	stop       <-chan struct{} // the app quit
}

func (a *app) trayHooks() trayHooks {
	return trayHooks{
		open: func() { a.open("") }, openBrowser: a.openInBrowser, changeWorkspace: a.changeWorkspace,
		quit: a.trayQuit, jobCount: a.run.JobCount, workspace: a.run.Workspace,
		wsChanged: a.wsChanged, stop: a.done,
		sharing: func() (string, string) {
			st := a.run.SharingStatus()
			return st.State, st.Address
		},
		setSharing: a.setSharing,
	}
}

// setSharing is the tray's "Share on Wi-Fi": on starts sharing with the
// existing shares, or opens the Share page when there are none or
// sharing cannot start (the page says why); off stops it at once.
func (a *app) setSharing(on bool) {
	if !on {
		a.run.StopSharing()
		return
	}
	if n, err := a.run.SharesHere(); err != nil || n == 0 {
		a.open("#/share")
		return
	}
	if err := a.run.StartSharing(0); err != nil {
		a.log.Warn("cannot share on Wi-Fi", "err", err)
		a.open("#/share")
	}
}

// shareLabel is the "Share on Wi-Fi" item's title and whether it is
// checked.
func shareLabel(state, address string) (title string, checked bool) {
	switch state {
	case "on":
		return "Share on Wi-Fi (" + address + ")", true
	case "paused":
		return "Share on Wi-Fi (paused: network changed)", true
	}
	return "Share on Wi-Fi", false
}

// quitLabel is the Quit item's title for n running jobs.
func quitLabel(n int) string {
	switch n {
	case 0:
		return "Quit"
	case 1:
		return "Quit (1 job running)"
	}
	return fmt.Sprintf("Quit (%d jobs running)", n)
}

// workspaceLabel is the disabled item naming the workspace folder.
func workspaceLabel(ws string) (title, tooltip string) {
	if ws == "" {
		return "No workspace chosen", "Choose the folder that holds your libraries with Change workspace…"
	}
	name := filepath.Base(ws)
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = ws
	}
	return "Workspace: " + name, ws
}

// runTray shows the notification-area icon and its menu and blocks until
// stopTray. On Windows it must run on the main goroutine (systray locks
// it to the main thread in its init).
func runTray(h trayHooks) {
	systray.Run(func() { trayReady(h) }, nil)
}

// stopTray ends runTray.
func stopTray() { systray.Quit() }

func trayReady(h trayHooks) {
	systray.SetIcon(trayIcon())
	systray.SetTooltip("picmover")
	mOpen := systray.AddMenuItem("Open", "Open the picmover window")
	mBrowser := systray.AddMenuItem("Open in browser", "Open picmover in the default browser")
	systray.AddSeparator()
	title, tip := workspaceLabel(h.workspace())
	mWS := systray.AddMenuItem(title, tip)
	mWS.Disable()
	mChange := systray.AddMenuItem("Change workspace…", "Choose another folder of libraries")
	systray.AddSeparator()
	shareTitle, shared := shareLabel(h.sharing())
	mShare := systray.AddMenuItemCheckbox(shareTitle, "Let paired phones on the home Wi-Fi view the shares", shared)
	systray.AddSeparator()
	mQuit := systray.AddMenuItem(quitLabel(h.jobCount()), "Quit picmover (running jobs are cancelled safely)")
	// A left click on the icon opens the window (Windows and Linux); the
	// menu is on the right click.
	systray.SetOnTapped(func() { go h.open() })

	go func() {
		tick := time.NewTicker(trayRefresh)
		defer tick.Stop()
		jobs := h.jobCount()
		shareChanged := make(chan struct{}, 1)
		refreshShare := func() {
			title, checked := shareLabel(h.sharing())
			if title != shareTitle || checked != mShare.Checked() {
				shareTitle = title
				mShare.SetTitle(title)
				setChecked(mShare, checked)
			}
		}
		for {
			select {
			case <-h.stop:
				return
			case <-mOpen.ClickedCh:
				go h.open()
			case <-mBrowser.ClickedCh:
				go h.openBrowser()
			case <-mChange.ClickedCh:
				go h.changeWorkspace()
			case <-mQuit.ClickedCh:
				go h.quit()
			case <-mShare.ClickedCh:
				on := !mShare.Checked()
				go func() {
					h.setSharing(on)
					select {
					case shareChanged <- struct{}{}:
					default:
					}
				}()
			case <-shareChanged:
				shareTitle = ""
				refreshShare()
			case <-h.wsChanged:
				title, tip := workspaceLabel(h.workspace())
				mWS.SetTitle(title)
				mWS.SetTooltip(tip)
			case <-tick.C:
				if n := h.jobCount(); n != jobs {
					jobs = n
					mQuit.SetTitle(quitLabel(n))
				}
				// Sharing also changes on the Share page, by auto-off and
				// by a network change.
				refreshShare()
			}
		}
	}()
}

// setChecked checks or unchecks a checkbox item.
func setChecked(m *systray.MenuItem, on bool) {
	if on {
		m.Check()
	} else {
		m.Uncheck()
	}
}
