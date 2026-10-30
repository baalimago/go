// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build (unix && !wasix) || (js && wasm) || wasip1

package poll

// waitReadable does nothing: a blocking read blocks only its own thread.
func waitReadable(fd int) {}
