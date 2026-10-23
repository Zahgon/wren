// Copyright (c) 2013-2021 Robert Nystrom and Wren Contributors
//
// Distributed under the MIT licence; see LICENSE in the repository root.

package wren

import "syscall"

// processClock returns the processor time this process has used, in seconds,
// which is what the original's `System.clock` reports through C's clock().
func processClock() float64 {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0
	}
	seconds := float64(usage.Utime.Sec) + float64(usage.Utime.Usec)/1e6
	seconds += float64(usage.Stime.Sec) + float64(usage.Stime.Usec)/1e6
	return seconds
}
