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
	wsChanged                                <-chan struct{}
	stop                                     <-chan struct{} // the app quit
}

func (a *app) trayHooks() trayHooks {
	return trayHooks{
		open: func() { a.open("") }, openBrowser: a.openInBrowser, changeWorkspace: a.changeWorkspace,
		quit: a.trayQuit, jobCount: a.run.JobCount, workspace: a.run.Workspace,
		wsChanged: a.wsChanged, stop: a.done,
	}
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
	mQuit := systray.AddMenuItem(quitLabel(h.jobCount()), "Quit picmover (running jobs are cancelled safely)")
	systray.SetOnTapped(func() { go h.open() }) // Linux: a left click on the icon opens the window

	go func() {
		tick := time.NewTicker(trayRefresh)
		defer tick.Stop()
		jobs := h.jobCount()
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
			case <-h.wsChanged:
				title, tip := workspaceLabel(h.workspace())
				mWS.SetTitle(title)
				mWS.SetTooltip(tip)
			case <-tick.C:
				if n := h.jobCount(); n != jobs {
					jobs = n
					mQuit.SetTitle(quitLabel(n))
				}
			}
		}
	}()
}
