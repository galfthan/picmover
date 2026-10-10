package server

import (
	"fmt"
	"net/url"
	"os/exec"
)

// OpenBrowser opens url (http or https) in the user's default browser:
// xdg-open on Linux and BSD, open on macOS, rundll32
// url.dll,FileProtocolHandler on Windows. The URL is passed as a single
// argument, never through a shell. It does not wait for the browser.
func OpenBrowser(u string) error {
	parsed, err := url.Parse(u)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("open browser: not an http URL: %q", u)
	}
	name, args := browserCommand(u)
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	go cmd.Wait() // reap; the opener exits quickly
	return nil
}
