//go:build !unix && !windows

package library

// renameNoReplace has no atomic form on this platform.
func renameNoReplace(src, dst string) error { return checkedRename(src, dst) }
