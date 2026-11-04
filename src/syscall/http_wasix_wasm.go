// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package syscall

import (
	"internal/runtime/syscall/linux"
	"unsafe"
)

// HttpOpen asks the host to carry one HTTP request, encoded as wasixv2's http-open describes, with
// its own HTTP client, and returns the descriptor its response is read from: the response head,
// then its body, then 0. The host closes it in Close, as a socket is.
func HttpOpen(request []byte) (int, error) {
	var addr uintptr
	if len(request) > 0 {
		addr = uintptr(unsafe.Pointer(unsafe.SliceData(request)))
	}
	fd := linux.ExtHttpOpen(addr, uint32(len(request)))
	if fd < 0 {
		return -1, Errno(-fd)
	}
	return int(fd), nil
}
