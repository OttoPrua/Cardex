//go:build !windows

package host

// joinGroup is a no-op on Unix: the executor leads its own process group.
func joinGroup(int) error { return nil }
