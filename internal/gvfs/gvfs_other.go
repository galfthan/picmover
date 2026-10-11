//go:build !linux

package gvfs

import "context"

// Mount reports false: gvfs mounts are only recognised on Linux.
func Mount(string) (scheme, host, rel string, ok bool) { return "", "", "", false }

// IsDevice reports false: gvfs mounts are only recognised on Linux.
func IsDevice(string) bool { return false }

// URI reports false: gvfs mounts are only recognised on Linux.
func URI(string) (string, bool) { return "", false }

// Available reports false: Pull is only implemented on Linux.
func Available() bool { return false }

// Pull returns ErrUnavailable: it is only implemented on Linux.
func Pull(context.Context, string, string, func(int64)) error { return ErrUnavailable }
