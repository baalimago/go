// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"os"
	"syscall"
)

// EnvVarHost selects the host's HTTP client, which carries a whole request with its own TLS, for
// a machine whose sockets carry no TLS of their own: wasixv2's http-open. Unset, or any other
// value, keeps the socket transport.
const EnvVarHost = "WASIX_HTTP"

// The value WASIX_HTTP must have to select the host's client.
const wasixHost = "host"

func init() {
	if os.Getenv(EnvVarHost) == wasixHost {
		DefaultTransport = &wasixTransport{open: wasixHostOpen, socket: DefaultTransport}
	}
}

// wasixHostOpen is http-open with its descriptor non-blocking. The response arrives on the
// descriptor after the call returns, and a goroutine reading it before it has parks in the
// netpoller (os.NewFile polls a non-blocking descriptor), so the program's other goroutines
// run and their requests are in flight at the same time. A blocking read would hold the whole
// program, which has no other thread, until each response came, one request after another.
func wasixHostOpen(request []byte) (int, error) {
	fd, err := syscall.HttpOpen(request)
	if err != nil {
		return fd, err
	}
	if err := syscall.SetNonblock(fd, true); err != nil {
		syscall.Close(fd)
		return -1, err
	}
	return fd, nil
}
