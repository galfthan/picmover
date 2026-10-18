//go:build linux || darwin || freebsd

package server

import "golang.org/x/sys/unix"

// diskSpace returns the bytes available to an unprivileged user on dir's
// file system and its size; -1, -1 when unknown.
func diskSpace(dir string) (free, total int64) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return -1, -1
	}
	return int64(uint64(st.Bavail) * uint64(st.Bsize)), int64(uint64(st.Blocks) * uint64(st.Bsize))
}
