//go:build !windows && !darwin

package server

func browserCommand(url string) (string, []string) {
	return "xdg-open", []string{url}
}
