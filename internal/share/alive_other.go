//go:build !unix && !windows

package share

// processAlive cannot tell on this system: a recorded holder counts as
// alive.
func processAlive(int) bool { return true }
