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
		DefaultTransport = &wasixTransport{open: syscall.HttpOpen, socket: DefaultTransport}
	}
}
