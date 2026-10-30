// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build wasix

package poll

import (
	"syscall"
	"unsafe"
)

// The wasix port runs every goroutine on one thread, so a read that blocks in the kernel
// stops them all: a program waiting for a line on its terminal would stop its timers and
// its other readers with it. A descriptor outside the netpoller (a terminal, a pipe from
// the shell) is polled instead until it is readable, sleeping the goroutine in between,
// and only then read.

// readableBackoff is how long a goroutine sleeps between polls of a descriptor that is not
// readable yet.
const readableBackoff = 10e6 // 10ms

//go:linkname sleep time.Sleep
func sleep(ns int64)

type pollFd struct {
	fd      int32
	events  int16
	revents int16
}

const pollIn = 0x1

// waitReadable returns once fd has data, has hung up or is in error, or polling it fails,
// so the read that follows does not block.
func waitReadable(fd int) {
	for {
		pfd := pollFd{fd: int32(fd), events: pollIn}
		var zero syscall.Timespec
		n, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&pfd)), 1, uintptr(unsafe.Pointer(&zero)), 0, 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 || n > 0 {
			return
		}
		sleep(readableBackoff)
	}
}
