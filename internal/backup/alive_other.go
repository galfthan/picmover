//go:build !unix && !windows

package backup

// processAlive cannot tell on this system: a recorded holder counts as
// alive.
func processAlive(int) bool { return true }
