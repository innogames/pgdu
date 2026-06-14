//go:build !linux

package tui

// sampleAllPids is a no-op on non-Linux platforms where /proc is unavailable.
// All proc columns in the activity table will show — on these hosts.
func sampleAllPids(_ []int32) []procRaw { return nil }
