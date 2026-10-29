// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !wasix

package unix

// 0 for 32-bit kernels, 1 for 64-bit ones.
const is64bit = ^uint(0) >> 63
