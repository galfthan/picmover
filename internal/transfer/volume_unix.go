//go:build unix

package transfer

import "syscall"

// sameVolume reports whether the directories a and b are on one file
// system (st_dev). On any error it reports true: the move then tries a
// rename and copies on EXDEV.
func sameVolume(a, b string) bool {
	var sa, sb syscall.Stat_t
	if syscall.Stat(a, &sa) != nil || syscall.Stat(b, &sb) != nil {
		return true
	}
	return sa.Dev == sb.Dev
}
