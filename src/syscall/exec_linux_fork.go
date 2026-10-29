// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux && !wasix

package syscall

import "unsafe"

// Fork, dup fd onto 0..len(fd), and exec(argv0, argvv, envv) in child.
// If a dup or exec fails, write the errno error to pipe.
// (Pipe is close-on-exec so if exec succeeds, it will be closed.)
// In the child, this function must not acquire any locks, because
// they might have been locked at the time of the fork. This means
// no rescheduling, no malloc calls, and no new stack segments.
// For the same reason compiler does not race instrument it.
// The calls to RawSyscall are okay because they are assembly
// functions that do not grow the stack.
//
//go:norace
func forkAndExecInChild(argv0 *byte, argv, envv []*byte, chroot, dir *byte, attr *ProcAttr, sys *SysProcAttr, pipe int) (pid int, err Errno) {
	// Set up and fork. This returns immediately in the parent or
	// if there's an error.
	upid, pidfd, err, mapPipe, locked := forkAndExecInChild1(argv0, argv, envv, chroot, dir, attr, sys, pipe)
	if locked {
		runtime_AfterFork()
	}
	if err != 0 {
		return 0, err
	}

	// parent; return PID
	pid = int(upid)
	if sys.PidFD != nil {
		*sys.PidFD = int(pidfd)
	}

	if sys.UidMappings != nil || sys.GidMappings != nil {
		Close(mapPipe[0])
		var err2 Errno
		// uid/gid mappings will be written after fork and unshare(2) for user
		// namespaces.
		if sys.Unshareflags&CLONE_NEWUSER == 0 {
			if err := writeUidGidMappings(pid, sys); err != nil {
				err2 = err.(Errno)
			}
		}
		RawSyscall(SYS_WRITE, uintptr(mapPipe[1]), uintptr(unsafe.Pointer(&err2)), unsafe.Sizeof(err2))
		Close(mapPipe[1])
	}

	return pid, 0
}
