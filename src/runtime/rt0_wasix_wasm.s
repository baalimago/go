// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include "go_asm.h"
#include "textflag.h"

// The kernel does not pass argc and argv: sysargs fetches the startup block
// with the startup_get import.
TEXT _rt0_wasm_wasix(SB),NOSPLIT,$0
	MOVD $runtime·wasmStack+(m0Stack__size-16)(SB), SP

	Get SP
	I64Const $0 // argc
	I64Store $0

	Get SP
	I64Const $0 // argv
	I64Store $8

	I32Const $0 // entry PC_B
	Call runtime·rt0_go(SB)
	Drop
	Call wasm_pc_f_loop(SB)

	Return

TEXT _rt0_wasm_wasix_lib(SB),NOSPLIT,$0
	Call _rt0_wasm_wasix(SB)
	Return
