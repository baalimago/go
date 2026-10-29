// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package syscall

import (
	"internal/runtime/syscall/linux"
	"runtime"
	"unsafe"
)

// spawnAttr is wasixv2's spawn-attr, the posix_spawnattr_t fields the kernel acts on.
type spawnAttr struct {
	flags      uint32
	pgroup     int32
	sigmask    [2]uint32
	sigdefault [2]uint32
}

// spawnFileAction is wasixv2's spawn-file-action, one posix_spawn file action.
type spawnFileAction struct {
	cmd   uint32
	fd    int32
	srcFd int32
	oflag uint32
	mode  uint32
	path  uint32
}

const (
	_POSIX_SPAWN_SETPGROUP = 0x2
	_POSIX_SPAWN_SETSIGDEF = 0x4
	_POSIX_SPAWN_SETSID    = 0x80

	_SPAWN_FDOP_CLOSE = 1
	_SPAWN_FDOP_DUP2  = 2
	_SPAWN_FDOP_CHDIR = 4
)

// forkAndExecInChild starts the child with wasixv2's spawn import, posix_spawn in one call:
// wasm cannot return twice, and wasixv2's vfork resumes the parent by unwinding with wasm
// exceptions, which Go's wasm backend does not emit. The kernel runs the child's steps and
// its execve inside the call and returns the errno of the step that failed, so nothing is
// written to pipe. SysProcAttr fields that need code of the child's own fail with ENOSYS.
func forkAndExecInChild(argv0 *byte, argv, envv []*byte, chroot, dir *byte, attr *ProcAttr, sys *SysProcAttr, pipe int) (pid int, err Errno) {
	if chroot != nil || sys.Credential != nil || sys.Ptrace || sys.Setctty || sys.Noctty ||
		sys.Foreground || sys.Pdeathsig != 0 || sys.Cloneflags != 0 || sys.Unshareflags != 0 ||
		sys.UidMappings != nil || sys.GidMappings != nil || len(sys.AmbientCaps) != 0 ||
		sys.UseCgroupFD {
		return 0, ENOSYS
	}

	// The runtime ignores SIGPIPE so that writes to closed sockets fail with EPIPE; the child
	// gets the default action, as it does on Linux, where Go handles SIGPIPE instead.
	sp := spawnAttr{flags: _POSIX_SPAWN_SETSIGDEF}
	sp.sigdefault[(SIGPIPE-1)/32] |= 1 << ((SIGPIPE - 1) % 32)
	if sys.Setsid {
		sp.flags |= _POSIX_SPAWN_SETSID
	}
	if sys.Setpgid {
		sp.flags |= _POSIX_SPAWN_SETPGROUP
		sp.pgroup = int32(sys.Pgid)
	}

	var actions []spawnFileAction
	if dir != nil {
		actions = append(actions, spawnFileAction{cmd: _SPAWN_FDOP_CHDIR, path: ptr32(unsafe.Pointer(dir))})
	}
	actions = appendFdActions(actions, attr.Files)

	argv32, envv32 := cstrArray(argv), cstrArray(envv)
	var pid32 int32
	ret := linux.ExtSpawn(
		uintptr(unsafe.Pointer(&pid32)),
		uintptr(unsafe.Pointer(argv0)),
		uintptr(unsafe.Pointer(unsafe.SliceData(actions))),
		uint32(len(actions)),
		uintptr(unsafe.Pointer(&sp)),
		uintptr(unsafe.Pointer(&argv32[0])),
		uintptr(unsafe.Pointer(&envv32[0])),
	)
	runtime.KeepAlive(dir)
	runtime.KeepAlive(argv)
	runtime.KeepAlive(envv)
	if ret < 0 {
		return 0, Errno(-ret)
	}
	if sys.PidFD != nil {
		*sys.PidFD = -1
	}
	return int(pid32), 0
}

// appendFdActions moves files[i] onto descriptor i, as forkAndExecInChild1 does on Linux. A
// descriptor that a later move would overwrite first moves above every descriptor in files;
// those copies are closed at the end, since dup2 cannot set FD_CLOEXEC. Descriptors are
// close-on-exec otherwise, so the child keeps only these.
func appendFdActions(actions []spawnFileAction, files []uintptr) []spawnFileAction {
	fd := make([]int, len(files))
	nextfd := len(files)
	for i, ufd := range files {
		if nextfd < int(ufd) {
			nextfd = int(ufd)
		}
		fd[i] = int(ufd)
	}
	nextfd++

	var moved []int
	for i := range fd {
		if fd[i] >= 0 && fd[i] < i {
			actions = append(actions, spawnFileAction{cmd: _SPAWN_FDOP_DUP2, fd: int32(nextfd), srcFd: int32(fd[i])})
			moved = append(moved, nextfd)
			fd[i] = nextfd
			nextfd++
		}
	}
	for i := range fd {
		if fd[i] == -1 {
			actions = append(actions, spawnFileAction{cmd: _SPAWN_FDOP_CLOSE, fd: int32(i)})
			continue
		}
		// A dup2 onto itself clears FD_CLOEXEC.
		actions = append(actions, spawnFileAction{cmd: _SPAWN_FDOP_DUP2, fd: int32(i), srcFd: int32(fd[i])})
	}
	for _, fd := range moved {
		actions = append(actions, spawnFileAction{cmd: _SPAWN_FDOP_CLOSE, fd: int32(fd)})
	}
	return actions
}

// cstrArray is the NULL-terminated array of 32-bit string pointers the kernel reads from p,
// which ends in nil.
func cstrArray(p []*byte) []uint32 {
	a := make([]uint32, len(p))
	for i, s := range p {
		a[i] = ptr32(unsafe.Pointer(s))
	}
	return a
}

func ptr32(p unsafe.Pointer) uint32 {
	return uint32(uintptr(p))
}
