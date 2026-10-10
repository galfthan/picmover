//go:build windows

package server

func browserCommand(url string) (string, []string) {
	return "rundll32", []string{"url.dll,FileProtocolHandler", url}
}
