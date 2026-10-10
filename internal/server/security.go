package server

import (
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

// guard holds what the Host and Origin checks accept.
//
// DNS rebinding: a page on evil.example can make the browser resolve
// evil.example to 127.0.0.1 and then talk to this server as a same-origin
// page. The browser still sends "Host: evil.example:7439", so only the
// listening address and the loopback names are accepted (421 otherwise).
// Cross-site requests: a foreign page's requests carry its Origin, which
// must be this server's own origin or a --cors-origin (403 otherwise);
// mutations additionally need a JSON content type and X-Picmover: 1,
// which a cross-origin page cannot send without a CORS preflight that
// only --cors-origin origins pass. Requests without an Origin (img and
// video loads, navigations) are caught by the Sec-Fetch-Site rule
// (fetchSiteOK), and every response carries
// Cross-Origin-Resource-Policy: same-origin.
type guard struct {
	host     string // listening host, lower case, no brackets
	port     string
	wildcard bool            // listening on 0.0.0.0 / ::: any IP literal is a valid Host
	cors     map[string]bool // normalised --cors-origin values
}

func newGuard(addr string, corsOrigins []string) (*guard, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("server: listen address %q: %w", addr, err)
	}
	if port == "" || port == "0" {
		return nil, fmt.Errorf("server: listen address %q needs the actual port", addr)
	}
	g := &guard{host: strings.ToLower(host), port: port, cors: map[string]bool{}}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		g.wildcard = true
	}
	for _, o := range corsOrigins {
		n, err := NormalizeOrigin(o)
		if err != nil {
			return nil, err
		}
		g.cors[n] = true
	}
	return g, nil
}

// NormalizeOrigin checks that o is an origin (scheme://host[:port], no
// path, query or user info) and returns it in the form browsers send:
// lower-case scheme and host, without a trailing slash and without the
// scheme's default port (http://localhost:80 is sent as http://localhost).
func NormalizeOrigin(o string) (string, error) {
	u, err := url.Parse(strings.TrimSuffix(o, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" ||
		u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid origin %q: want scheme://host[:port], e.g. http://localhost:5173", o)
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	switch port := u.Port(); {
	case scheme == "http" && port == "80", scheme == "https" && port == "443":
		host = strings.TrimSuffix(host, ":"+port)
	case strings.HasSuffix(host, ":"):
		host = strings.TrimSuffix(host, ":") // "http://x:" is http://x
	}
	return scheme + "://" + host, nil
}

// fetchSiteOK applies the Sec-Fetch-Site rule to r. Browsers send the
// header on every request, including the ones without an Origin (img,
// video and script loads, top-level navigations), so it closes the gap the
// Origin check leaves: "cross-site" is refused; "same-site" (another port
// of localhost, e.g. the Vite dev server) only when the request's Origin
// is a --cors-origin; "same-origin", "none" (typed URL, bookmark, the
// browser opened by serve) and a missing header (old browsers, curl) pass.
// A top-level navigation (Sec-Fetch-Mode: navigate, Sec-Fetch-Dest:
// document when sent) to a path outside /api/ passes whatever the site:
// loading the app shell does nothing, and links to the app from other
// sites or chat apps keep working. /api/ paths stay strict.
func fetchSiteOK(r *http.Request, corsOrigin bool) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/") && strings.EqualFold(r.Header.Get("Sec-Fetch-Mode"), "navigate") {
		if dest := r.Header.Get("Sec-Fetch-Dest"); dest == "" || strings.EqualFold(dest, "document") {
			return true
		}
	}
	switch strings.ToLower(r.Header.Get("Sec-Fetch-Site")) {
	case "cross-site":
		return false
	case "same-site":
		return corsOrigin
	}
	return true
}

// hostOK reports whether a Host header (host[:port]) names this server.
func (g *guard) hostOK(hostport string) bool {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		// No port: only valid when we listen on the default port.
		host, port = hostport, "80"
	}
	if port != g.port {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
	switch host {
	case "localhost", "127.0.0.1", "::1", g.host:
		return host != ""
	}
	return g.wildcard && net.ParseIP(host) != nil
}

// originOK reports whether an Origin header is this server's own origin.
func (g *guard) originOK(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.User != nil || (u.Path != "" && u.Path != "/") ||
		u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return g.hostOK(u.Host)
}

// corsOK reports whether origin is one of the --cors-origin values.
func (g *guard) corsOK(origin string) bool {
	if len(g.cors) == 0 {
		return false
	}
	n, err := NormalizeOrigin(origin)
	return err == nil && g.cors[n]
}

// checkMutation returns why a non-GET request is refused, or "".
func checkMutation(r *http.Request) string {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return "mutating requests need Content-Type: application/json"
	}
	if r.Header.Get("X-Picmover") != "1" {
		return "mutating requests need the X-Picmover: 1 header"
	}
	return ""
}

// ValidSourcePath checks a path supplied by a request (the /api/fs path,
// an import job's source) before any filesystem call: it must be absolute
// and must not be a network (UNC) or device path, on every OS, and must
// not contain a NUL byte. Opening \\host\share on Windows connects to that
// host and sends the user's NTLM credentials, so such paths are refused
// lexically, never stat'ed. The check does not clean or resolve p.
func ValidSourcePath(p string) error {
	if p == "" {
		return errors.New("path must be absolute")
	}
	if strings.IndexByte(p, 0) >= 0 {
		return errors.New("path contains a NUL byte")
	}
	if isNetworkOrDevicePath(p) {
		return errors.New("network (UNC) and device paths are not supported")
	}
	if !filepath.IsAbs(p) {
		return errors.New("path must be absolute")
	}
	return nil
}

// isNetworkOrDevicePath reports UNC and device path spellings: two leading
// separators in any mix of / and \ (\\server\share, //server/share,
// \\?\..., \\.\...) and the NT object prefix \??\.
func isNetworkOrDevicePath(p string) bool {
	sep := func(c byte) bool { return c == '/' || c == '\\' }
	if len(p) >= 2 && sep(p[0]) && sep(p[1]) {
		return true
	}
	return len(p) >= 4 && sep(p[0]) && p[1] == '?' && p[2] == '?' && sep(p[3])
}

// IsLoopbackAddr reports whether the host of addr (host:port) is a
// loopback name or address. An empty host (all interfaces) is not.
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// htmlCSP is the Content-Security-Policy of the HTML page. Leaflet sets
// inline styles for positioning; scripts stay strict.
const htmlCSP = "default-src 'self'; img-src 'self' data: blob: https://tile.openstreetmap.org https://*.tile.openstreetmap.org; " +
	"media-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'none'"
