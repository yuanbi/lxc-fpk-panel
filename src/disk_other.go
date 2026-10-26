//go:build !linux

package main

// diskUsage is a stub for non-Linux hosts; it is only used so the package can
// be built/tested on a development machine.
func diskUsage(path string) (total, free uint64) {
	return 0, 0
}
