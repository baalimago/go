// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unix

// The wasix/wasm port is LP64 on an ILP32 kernel, whose siginfo has the 32-bit layout.
const is64bit = 0
