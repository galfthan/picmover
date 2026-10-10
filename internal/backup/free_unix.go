//go:build linux || darwin || freebsd

package backup

import "golang.org/x/sys/unix"

// diskFree returns the bytes available to an unprivileged user on dir's
// file system, -1 when unknown.
func diskFree(dir string) int64 {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return -1
	}
	return int64(uint64(st.Bavail) * uint64(st.Bsize))
}
