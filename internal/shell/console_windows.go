//go:build windows

package shell

import "os"

var procAttachConsole = kernel32.NewProc("AttachConsole")

const attachParentProcess = ^uintptr(0) // ATTACH_PARENT_PROCESS = (DWORD)-1

// ShowText shows the --help or --version text. picmover-app.exe is a GUI
// program without a console of its own: when started from a console
// (cmd, PowerShell) it attaches to it and prints there (the prompt has
// already returned, so the text appears after it); otherwise it shows a
// message box.
func ShowText(title, text string) {
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r != 0 {
		if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
			f.WriteString("\r\n" + text)
			f.Close()
			return
		}
	}
	messageBox(0, title, text, mbIconInfo|mbSetFg)
}
