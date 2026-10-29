// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package runtime

import (
	"internal/runtime/syscall/linux"
	"unsafe"
)

// wasix/wasm runs on a wasixv2 kernel: Linux syscalls as wasm imports (see
// internal/runtime/syscall/linux). Go's wasm backend has no threads and no
// signal handlers, so the scheduler and locks are those of the other wasm
// ports (os_wasm.go, lock_wasix_wasm.go); this file is the Linux half:
// process startup, time, I/O and exit through syscalls.

// riscv32 syscall numbers.
const (
	_SYS_write           = 64
	_SYS_exit_group      = 94
	_SYS_sched_yield     = 124
	_SYS_getrandom       = 278
	_SYS_clock_gettime   = 403
	_SYS_clock_nanosleep = 407

	_CLOCK_REALTIME  = 0
	_CLOCK_MONOTONIC = 1

	_AT_NULL   = 0
	_AT_PAGESZ = 6
	_AT_SECURE = 23
	_AT_RANDOM = 25

	_SIGPIPE = 13
	_SIG_IGN = 1
)

type timespec64 struct {
	sec  int64
	nsec int64
}

//go:nosplit
func exit(code int32) {
	linux.Syscall6(_SYS_exit_group, uintptr(code), 0, 0, 0, 0, 0)
}

//go:nosplit
func write1(fd uintptr, p unsafe.Pointer, n int32) int32 {
	r, _, e := linux.Syscall6(_SYS_write, fd, uintptr(p), uintptr(n), 0, 0, 0)
	if e != 0 {
		return -int32(e)
	}
	return int32(r)
}

func usleep(usec uint32) {
	ts := timespec64{sec: int64(usec / 1e6), nsec: int64(usec%1e6) * 1e3}
	linux.Syscall6(_SYS_clock_nanosleep, _CLOCK_MONOTONIC, 0, uintptr(unsafe.Pointer(&ts)), 0, 0, 0)
}

func osSchedYield() {
	linux.Syscall6(_SYS_sched_yield, 0, 0, 0, 0, 0, 0)
}

func readRandom(r []byte) int {
	if len(r) == 0 {
		return 0
	}
	n, _, e := linux.Syscall6(_SYS_getrandom, uintptr(unsafe.Pointer(&r[0])), uintptr(len(r)), 0, 0, 0, 0)
	if e != 0 {
		return 0
	}
	return int(n)
}

func walltime() (sec int64, nsec int32) {
	var ts timespec64
	linux.Syscall6(_SYS_clock_gettime, _CLOCK_REALTIME, uintptr(unsafe.Pointer(&ts)), 0, 0, 0, 0)
	return ts.sec, int32(ts.nsec)
}

func nanotime1() int64 {
	var ts timespec64
	linux.Syscall6(_SYS_clock_gettime, _CLOCK_MONOTONIC, uintptr(unsafe.Pointer(&ts)), 0, 0, 0, 0)
	return ts.sec*1e9 + ts.nsec
}

// The startup block is what the kernel puts on a new process's stack: argc,
// argv, envp and auxv, in the guest's ILP32 layout. Go's pointers are 8
// bytes, so sysargs widens its vectors into startupVec and then reads them as
// any Linux port does.
var (
	startupBlock [256 << 10]byte
	startupVec   [8 << 10]uintptr
)

func sysargs(_ int32, _ **byte) {
	size := linux.ExtStartupSize()
	buf := (uintptr(unsafe.Pointer(&startupBlock[0])) + 15) &^ 15
	if uintptr(size) > uintptr(len(startupBlock))-15 {
		throw("startup block too large")
	}
	if linux.ExtStartupGet(buf, uint32(size)) != 0 {
		throw("startup_get failed")
	}
	word := func(i uintptr) uintptr {
		return uintptr(*(*uint32)(unsafe.Pointer(buf + 4*i)))
	}
	n := word(0)
	nulls := 0
	var i uintptr
	for i = 1; ; i++ {
		if i >= uintptr(len(startupVec)) {
			throw("startup block too large")
		}
		w := word(i)
		startupVec[i-1] = w
		if w == 0 {
			nulls++
			if nulls == 2 {
				break
			}
		}
	}
	// auxv pairs follow the second NULL.
	auxStart := i
	for {
		i++
		if i+1 >= uintptr(len(startupVec)) {
			throw("startup block too large")
		}
		tag, val := word(i), word(i+1)
		startupVec[i-1], startupVec[i] = tag, val
		i++
		if tag == _AT_NULL {
			break
		}
	}
	argc = int32(n)
	argv = (**byte)(unsafe.Pointer(&startupVec[0]))
	auxvp := startupVec[auxStart : i-1 : i-1]
	pairs := sysauxv(auxvp)
	auxv = auxvp[: pairs*2 : pairs*2]

	// Go's wasm code cannot be a signal handler, so a write to a closed
	// socket or pipe must fail with EPIPE instead of killing the process.
	sigIgnore(_SIGPIPE)
}

func sysauxv(auxv []uintptr) (pairs int) {
	var i int
	for ; auxv[i] != _AT_NULL; i += 2 {
		tag, val := auxv[i], auxv[i+1]
		switch tag {
		case _AT_RANDOM:
			startupRand = (*[16]byte)(unsafe.Pointer(val))[:]
		case _AT_SECURE:
			secureMode = val == 1
		}
	}
	return i / 2
}

// secureMode holds the value of AT_SECURE passed in the auxiliary vector.
var secureMode bool

// sigaction32 is the kernel's struct sigaction on wasixv2 (riscv32 layout).
type sigaction32 struct {
	handler uint32
	flags   uint32
	mask    uint64
}

func sigIgnore(sig uintptr) {
	sa := sigaction32{handler: _SIG_IGN}
	linux.RtSigaction32(sig, uintptr(unsafe.Pointer(&sa)), 0)
}

func goenvs() {
	goenvs_unix()
}

func resetMemoryDataView() {}

// sbrk0 reports no brk: the heap grows linear memory directly (mem_wasm.go).
func sbrk0() uintptr { return 0 }

const (
	_SYS_fcntl = 25
	_SYS_pipe2 = 59
)

//go:nosplit
func fcntl(fd, cmd, arg int32) (ret int32, errno int32) {
	r, _, err := linux.Syscall6(_SYS_fcntl, uintptr(fd), uintptr(cmd), uintptr(arg), 0, 0, 0)
	return int32(r), int32(err)
}

func pipe2(flags int32) (r, w int32, errno int32) {
	var p [2]int32
	_, _, e := linux.Syscall6(_SYS_pipe2, uintptr(unsafe.Pointer(&p[0])), uintptr(flags), 0, 0, 0, 0)
	return p[0], p[1], int32(e)
}

const (
	_SYS_openat = 56
	_SYS_close  = 57
	_SYS_read   = 63

	_AT_FDCWD = -100
)

//go:nosplit
func open(name *byte, mode, perm int32) int32 {
	r, _, e := linux.Syscall6(_SYS_openat, atFDCWD, uintptr(unsafe.Pointer(name)), uintptr(mode), uintptr(perm), 0, 0)
	if e != 0 {
		return -int32(e)
	}
	return int32(r)
}

//go:nosplit
func closefd(fd int32) int32 {
	_, _, e := linux.Syscall6(_SYS_close, uintptr(fd), 0, 0, 0, 0, 0)
	return -int32(e)
}

//go:nosplit
func read(fd int32, p unsafe.Pointer, n int32) int32 {
	r, _, e := linux.Syscall6(_SYS_read, uintptr(fd), uintptr(p), uintptr(n), 0, 0, 0)
	if e != 0 {
		return -int32(e)
	}
	return int32(r)
}

var atFDCWD = ^uintptr(-_AT_FDCWD - 1)
