package importer

import "syscall"

// fuseSuperMagic is FUSE_SUPER_MAGIC, the statfs f_type of FUSE mounts
// (gvfs MTP and gphoto2 phone mounts among them).
const fuseSuperMagic = 0x65735546

// isFUSE reports whether p lies on a FUSE file system.
func isFUSE(p string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(p, &st); err != nil {
		return false
	}
	return uint32(st.Type) == fuseSuperMagic
}
