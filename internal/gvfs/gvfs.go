// Package gvfs maps the FUSE paths of gvfs mounts on Linux
// ($XDG_RUNTIME_DIR/gvfs/<scheme>:host=<host>/...) to GIO URIs and pulls
// files from phones through the gvfs daemon with `gio copy`.
//
// Reading a phone's files through the FUSE mount is slow: the gvfs MTP
// backend turns every FUSE read into a separate MTP partial-object request
// with a USB round trip each. `gio copy` asks the daemon to pull the whole
// object (the backend's pull operation), at USB speed. Only mtp (Android)
// and gphoto2 (PTP cameras and iPhones) mounts count as devices (IsDevice).
//
// On other operating systems IsDevice, Mount and URI report false and Pull
// returns ErrUnavailable.
package gvfs

import (
	"errors"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrUnavailable is returned by Pull when the gio tool is not installed
// (or the OS has no gvfs).
var ErrUnavailable = errors.New("gio is not available")

// Dir returns where gvfs mounts its FUSE file systems:
// $XDG_RUNTIME_DIR/gvfs, else /run/user/<uid>/gvfs.
func Dir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "gvfs")
	}
	return filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "gvfs")
}

// parseMount is Mount for the gvfs directory dir; it works on slash paths
// only, so it is tested on every OS. p must be absolute and clean. The
// mount directory name is "<scheme>:host=<host>" possibly followed by
// ",key=value" parts; host is returned as it appears there (gvfs
// percent-encodes reserved characters in it, as in its URIs). rel is the
// slash-separated path below the mount directory, "" for the mount
// directory itself.
func parseMount(dir, p string) (scheme, host, rel string, ok bool) {
	if dir == "" || !strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return "", "", "", false
	}
	rest, found := strings.CutPrefix(p, strings.TrimSuffix(dir, "/")+"/")
	if !found || rest == "" {
		return "", "", "", false
	}
	name, rel, _ := strings.Cut(rest, "/")
	scheme, spec, found := strings.Cut(name, ":")
	if !found || scheme == "" {
		return "", "", "", false
	}
	for _, kv := range strings.Split(spec, ",") {
		if v, isHost := strings.CutPrefix(kv, "host="); isHost {
			host = v
			break
		}
	}
	if host == "" {
		return "", "", "", false
	}
	return scheme, host, rel, true
}

// isDeviceScheme reports the gvfs backends of phones and cameras.
func isDeviceScheme(scheme string) bool { return scheme == "mtp" || scheme == "gphoto2" }

// uriFor returns the GIO URI <scheme>://<host>/<rel>, each segment of rel
// percent-encoded (url.PathEscape); GIO unescapes the path of a URI, so
// this names the same file as the FUSE path.
func uriFor(scheme, host, rel string) string {
	segs := strings.Split(rel, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return scheme + "://" + host + "/" + strings.Join(segs, "/")
}
