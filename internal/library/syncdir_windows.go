//go:build windows

package library

// syncDir is a no-op on Windows, where directory handles cannot be flushed
// the same way and NTFS journals metadata itself.
func syncDir(string) {}
