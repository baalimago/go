// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package linux

import "unsafe"

// On wasix/wasm the kernel is wasixv2: every syscall is its own typed wasm
// import in module wasixv2, numbered as on riscv32 (see zsyscall_wasix_wasm.go).
// Syscall6 keeps Linux's number-based entry point so that the syscall package,
// internal/syscall/unix and x/sys/unix work unchanged, and dispatches to the
// import.
//
// Go's wasm port is LP64 (8-byte int, uintptr and pointers) while the kernel
// sees an ILP32 guest. Structs with only fixed-width fields have the same
// layout in both; the few that hold pointers, longs or size_t are converted
// here (compat_* functions), in the opposite direction of Linux's own compat
// layer. The import calls do not yield to other goroutines (there are no
// threads on wasm), so the scratch buffers are not shared.

const (
	_ENOSYS = 38
	_EINVAL = 22
	_EFAULT = 14

	_UIO_MAXIOV = 1024
	_MSG_CTRUNC = 0x8
)

//go:nosplit
func Syscall6(num, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2, errno uintptr) {
	r1, errno = dispatch(num, a1, a2, a3, a4, a5, a6)
	if errno != 0 {
		return ^uintptr(0), 0, errno
	}
	return r1, 0, 0
}

//go:nosplit
func ret32(r int32) (uintptr, uintptr) {
	if r < 0 && r >= -4095 {
		return 0, uintptr(-r)
	}
	// Addresses (mmap) and other unsigned results are zero-extended.
	return uintptr(uint32(r)), 0
}

//go:nosplit
func ret64(r int64) (uintptr, uintptr) {
	if r < 0 && r >= -4095 {
		return 0, uintptr(-r)
	}
	return uintptr(r), 0
}

// iovec32 is the kernel's struct iovec; Go's is {*byte, uint64}.
type iovec32 struct {
	base uint32
	len  uint32
}

type iovec64 struct {
	base uint64
	len  uint64
}

// msghdr32 is the kernel's struct user_msghdr; Go's Msghdr is the LP64 one.
type msghdr32 struct {
	name       uint32
	namelen    uint32
	iov        uint32
	iovlen     uint32
	control    uint32
	controllen uint32
	flags      uint32
}

type msghdr64 struct {
	name       uint64
	namelen    uint32
	_          uint32
	iov        uint64
	iovlen     uint64
	control    uint64
	controllen uint64
	flags      int32
	_          int32
}

var (
	iovScratch     [_UIO_MAXIOV]iovec32
	msgScratch     msghdr32
	controlScratch [4096]byte
)

//go:nosplit
func packIovecs(p, n uintptr) (uintptr, bool) {
	if n > _UIO_MAXIOV {
		return 0, false
	}
	for i := uintptr(0); i < n; i++ {
		v := (*iovec64)(unsafe.Pointer(p + i*unsafe.Sizeof(iovec64{})))
		iovScratch[i] = iovec32{uint32(v.base), uint32(v.len)}
	}
	return uintptr(unsafe.Pointer(&iovScratch[0])), true
}

//go:nosplit
func compat_readv(fd, vec, vlen uintptr) (uintptr, uintptr) {
	p, ok := packIovecs(vec, vlen)
	if !ok {
		return 0, _EINVAL
	}
	return ret32(wasixv2_readv(int32(fd), p, uint32(vlen)))
}

//go:nosplit
func compat_writev(fd, vec, vlen uintptr) (uintptr, uintptr) {
	p, ok := packIovecs(vec, vlen)
	if !ok {
		return 0, _EINVAL
	}
	return ret32(wasixv2_writev(int32(fd), p, uint32(vlen)))
}

//go:nosplit
func compat_preadv(fd, vec, vlen, pos uintptr) (uintptr, uintptr) {
	p, ok := packIovecs(vec, vlen)
	if !ok {
		return 0, _EINVAL
	}
	return ret32(wasixv2_preadv(int32(fd), p, uint32(vlen), int64(pos)))
}

//go:nosplit
func compat_pwritev(fd, vec, vlen, pos uintptr) (uintptr, uintptr) {
	p, ok := packIovecs(vec, vlen)
	if !ok {
		return 0, _EINVAL
	}
	return ret32(wasixv2_pwritev(int32(fd), p, uint32(vlen), int64(pos)))
}

//go:nosplit
func compat_preadv2(fd, vec, vlen, pos, flags uintptr) (uintptr, uintptr) {
	p, ok := packIovecs(vec, vlen)
	if !ok {
		return 0, _EINVAL
	}
	return ret32(wasixv2_preadv2(int32(fd), p, uint32(vlen), int64(pos), uint32(flags)))
}

//go:nosplit
func compat_pwritev2(fd, vec, vlen, pos, flags uintptr) (uintptr, uintptr) {
	p, ok := packIovecs(vec, vlen)
	if !ok {
		return 0, _EINVAL
	}
	return ret32(wasixv2_pwritev2(int32(fd), p, uint32(vlen), int64(pos), uint32(flags)))
}

// Control messages: Go's cmsghdr is {uint64 len; int32 level; int32 type}
// with 8-byte alignment, the kernel's {uint32 len; int32 level; int32 type}
// with 4-byte alignment.
const (
	cmsgHdr64 = 16
	cmsgHdr32 = 12
)

//go:nosplit
func align(n, a uintptr) uintptr { return (n + a - 1) &^ (a - 1) }

//go:nosplit
func memmoveBytes(dst, src, n uintptr) {
	if dst < src {
		for i := uintptr(0); i < n; i++ {
			*(*byte)(unsafe.Pointer(dst + i)) = *(*byte)(unsafe.Pointer(src + i))
		}
	} else {
		for i := n; i > 0; i-- {
			*(*byte)(unsafe.Pointer(dst + i - 1)) = *(*byte)(unsafe.Pointer(src + i - 1))
		}
	}
}

// packMsghdr fills msgScratch from the Go Msghdr at m.
//
//go:nosplit
func packMsghdr(m uintptr, send bool) uintptr {
	h := (*msghdr64)(unsafe.Pointer(m))
	iov, ok := packIovecs(uintptr(h.iov), uintptr(h.iovlen))
	if !ok {
		return _EINVAL
	}
	msgScratch = msghdr32{
		name:    uint32(h.name),
		namelen: h.namelen,
		iov:     uint32(iov),
		iovlen:  uint32(h.iovlen),
		flags:   uint32(h.flags),
	}
	if h.control == 0 || h.controllen == 0 {
		return 0
	}
	ctl := uintptr(unsafe.Pointer(&controlScratch[0]))
	if !send {
		// Leave room for the kernel's smaller headers to grow back.
		n := uintptr(h.controllen)
		if n > uintptr(len(controlScratch)) {
			n = uintptr(len(controlScratch))
		}
		msgScratch.control = uint32(ctl)
		msgScratch.controllen = uint32(n)
		return 0
	}
	var in, out uintptr
	for in+cmsgHdr64 <= uintptr(h.controllen) {
		src := uintptr(h.control) + in
		clen := uintptr(*(*uint64)(unsafe.Pointer(src)))
		if clen < cmsgHdr64 || in+clen > uintptr(h.controllen) {
			return _EINVAL
		}
		data := clen - cmsgHdr64
		if out+align(cmsgHdr32+data, 4) > uintptr(len(controlScratch)) {
			return _EINVAL
		}
		dst := ctl + out
		*(*uint32)(unsafe.Pointer(dst)) = uint32(cmsgHdr32 + data)
		*(*int32)(unsafe.Pointer(dst + 4)) = *(*int32)(unsafe.Pointer(src + 8))
		*(*int32)(unsafe.Pointer(dst + 8)) = *(*int32)(unsafe.Pointer(src + 12))
		memmoveBytes(dst+cmsgHdr32, src+cmsgHdr64, data)
		out += align(cmsgHdr32+data, 4)
		in += align(clen, 8)
	}
	msgScratch.control = uint32(ctl)
	msgScratch.controllen = uint32(out)
	return 0
}

// unpackRecvMsghdr copies what recvmsg wrote back into the Go Msghdr at m.
//
//go:nosplit
func unpackRecvMsghdr(m uintptr) {
	h := (*msghdr64)(unsafe.Pointer(m))
	h.namelen = msgScratch.namelen
	flags := msgScratch.flags
	if msgScratch.control != 0 {
		var in, out uintptr
		for in+cmsgHdr32 <= uintptr(msgScratch.controllen) {
			src := uintptr(msgScratch.control) + in
			clen := uintptr(*(*uint32)(unsafe.Pointer(src)))
			if clen < cmsgHdr32 {
				break
			}
			data := clen - cmsgHdr32
			if out+cmsgHdr64+data > uintptr(h.controllen) {
				flags |= _MSG_CTRUNC
				break
			}
			dst := uintptr(h.control) + out
			*(*uint64)(unsafe.Pointer(dst)) = uint64(cmsgHdr64 + data)
			*(*int32)(unsafe.Pointer(dst + 8)) = *(*int32)(unsafe.Pointer(src + 4))
			*(*int32)(unsafe.Pointer(dst + 12)) = *(*int32)(unsafe.Pointer(src + 8))
			memmoveBytes(dst+cmsgHdr64, src+cmsgHdr32, data)
			out += align(cmsgHdr64+data, 8)
			in += align(clen, 4)
		}
		if out > uintptr(h.controllen) {
			out = uintptr(h.controllen)
		}
		h.controllen = uint64(out)
	}
	h.flags = int32(flags)
}

//go:nosplit
func compat_sendmsg(fd, msg, flags uintptr) (uintptr, uintptr) {
	if msg == 0 {
		return 0, _EFAULT
	}
	if e := packMsghdr(msg, true); e != 0 {
		return 0, e
	}
	return ret32(wasixv2_sendmsg(int32(fd), uintptr(unsafe.Pointer(&msgScratch)), uint32(flags)))
}

//go:nosplit
func compat_recvmsg(fd, msg, flags uintptr) (uintptr, uintptr) {
	if msg == 0 {
		return 0, _EFAULT
	}
	if e := packMsghdr(msg, false); e != 0 {
		return 0, e
	}
	r, e := ret32(wasixv2_recvmsg(int32(fd), uintptr(unsafe.Pointer(&msgScratch)), uint32(flags)))
	if e == 0 {
		unpackRecvMsghdr(msg)
	}
	return r, e
}

// RtSigaction32 calls rt_sigaction with structs already in the kernel's
// layout ({handler, flags uint32; mask uint64}), which Syscall6 refuses.
//
//go:nosplit
func RtSigaction32(sig, act, oact uintptr) uintptr {
	_, e := ret32(wasixv2_rt_sigaction(int32(sig), act, oact, 8))
	return e
}
