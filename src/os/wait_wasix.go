// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package os

import (
	"internal/poll"
	"internal/syscall/unix"
	"runtime"
	"syscall"
)

// blockUntilWaitable waits in the netpoller on a pidfd, which is readable once the
// process exited, and reports whether it has done so. There is one thread, so a
// blocking waitid, as on Linux, would stop every goroutine, among them those that
// feed the child's stdin and drain its output.
func (p *Process) blockUntilWaitable() (bool, error) {
	h, err := unix.PidFDOpen(p.Pid, syscall.O_NONBLOCK)
	if err != nil {
		if err == syscall.ENOSYS {
			return false, nil
		}
		return false, NewSyscallError("pidfd_open", err)
	}
	pfd := poll.FD{Sysfd: int(h)}
	if err := pfd.Init("pidfd", true); err != nil {
		syscall.Close(int(h))
		return false, nil
	}
	defer pfd.Close()
	var waitErr error
	err = pfd.RawRead(func(uintptr) bool {
		var info unix.SiginfoChild
		waitErr = unix.Waitid(unix.P_PID, p.Pid, &info, syscall.WEXITED|syscall.WNOWAIT|syscall.WNOHANG, nil)
		return waitErr != syscall.EINTR && (waitErr != nil || info.Pid != 0)
	})
	runtime.KeepAlive(p)
	if err == nil {
		err = waitErr
	}
	if err != nil {
		return false, NewSyscallError("waitid", err)
	}
	return true, nil
}
