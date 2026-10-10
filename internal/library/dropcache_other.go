//go:build !linux

package library

import "os"

// dropCache is a no-op here: macOS, the BSDs and Windows have no
// portable way to drop one file's cached pages, so the read-back may be
// served from the cache (see readBackUncached).
func dropCache(*os.File) {}
