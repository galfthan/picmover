//go:build !linux

package importer

// isFUSE reports whether p lies on a FUSE file system; only detected on
// Linux.
func isFUSE(string) bool { return false }
