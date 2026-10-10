//go:build darwin

package server

func browserCommand(url string) (string, []string) {
	return "open", []string{url}
}
