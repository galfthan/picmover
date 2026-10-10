//go:build unix && !linux && !darwin

package library

// renameNoReplace renames by link and unlink: link refuses an existing
// target atomically.
func renameNoReplace(src, dst string) error { return linkRename(src, dst) }
