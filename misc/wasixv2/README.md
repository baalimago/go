# Go on wasixv2 (spike)

Status: spike. `GOOS=wasix GOARCH=wasm` builds the whole standard library and
unmodified [clai](https://github.com/baalimago/clai), and clai completes a
streamed LLM completion on wasmer's `feat/wasixv2` kernel.

## Goal

Run Go programs on the wasixv2 kernel, whose guest ABI is Linux's syscall ABI
for wasm32 guests: one typed wasm import per riscv32 syscall, in module
`wasixv2` ([wasixv2-abi](https://github.com/wasix-org/wasixv2-abi)). The port
is modelled on Go's Linux ports, not grown from `wasip1`: Linux code in the
standard library (`syscall`, `os`, `net`, `internal/poll`, epoll netpoll)
runs as it is, and only the syscall entry point and the runtime's OS glue are
new.

## Design

**Port identity.** The port is `GOOS=wasix GOARCH=wasm`. Like `android`,
`wasix` implies the `linux` build tag and file suffix, so code behind
`//go:build linux` or `unix` builds unchanged, and wasix-only code sits in
`_wasix_wasm` files. Teaching the toolchain the new GOOS touches
`internal/syslist`, `internal/goos`, the `linux` tag implication in
`go/build`, `cmd/go` and `cmd/dist`, and the linker, which writes a wasm
module (head type `Hwasip1`) entered at `_rt0_wasm_wasix`. Library code that
switches on `runtime.GOOS == "linux"` (dirent inode handling, `/proc/self/exe`,
sendfile, `os.Root` mkdir) also names `wasix`.

**One syscall entry point.** `internal/runtime/syscall/linux.Syscall6` stays
the only way into the kernel, as on every Linux port. On wasm it is Go code
that switches on the riscv32 syscall number and calls that syscall's typed
import (`zsyscall_wasix_wasm.go`). Results follow Linux: `-4095..-1` is
`-errno`; 32-bit results are zero-extended, so `mmap` addresses above 2 GiB
survive. `syscall`, `internal/syscall/unix` and, once ported, `x/sys/unix`
keep calling `Syscall(SYS_X, ...)`.

**Generated from the WIT.** wasixv2-codegen's Go backend (`wasixv2-codegen go`)
reads wasixv2-abi's `wit/` and writes the imports, the dispatch switch and
`syscall/zsysnum_wasix_wasm.go` (riscv32 numbers, plus Go's names for the
`*64`/`*_time64` calls, plus the asm-generic calls riscv32 leaves out, which
fail with `ENOSYS`), next to the backends for wasmer's Rust and libc2's C, so
an ABI bump regenerates all three from one definition.

**LP64 Go on an ILP32 kernel.** Go's wasm port has 8-byte `int`, `uintptr` and
pointers, while the kernel sees wasm32's ILP32 C layouts. The Go-visible types
are linux/loong64's (asm-generic, `statx` only, 64-bit `time_t`). Structs of
fixed-width fields have one layout either way (`timespec`, `statx`,
`epoll_event`, `sockaddr`, `pollfd`, `linux_dirent64`, `rlimit64`). Structs
holding pointers, `long` or `size_t` are converted inside `Syscall6`, the
opposite direction of Linux's own compat layer: `iovec` (readv, writev,
preadv(2), pwritev(2)) and `msghdr` with its `cmsghdr` chain (sendmsg,
recvmsg). The generator finds every other syscall taking such a struct (41
arguments in all: sigaction, sigaltstack, getrusage, sysinfo, mmsghdr,
sigevent, ...); those fail with `ENOSYS` when given a non-NULL pointer, never
with a misread struct. There are no threads, so the conversions use static
scratch buffers.

**syscall package.** linux/loong64's files with riscv32 numbers. riscv32 has
no 32-bit-time or legacy calls, so `Gettimeofday` uses `clock_gettime`, and
`wait4`, `nanosleep`, `getrlimit`/`setrlimit` (use `prlimit64`) and
`adjtimex` are `ENOSYS`. The socket timeout and timestamp options are the
64-bit-time `_NEW` values, which take Go's 16-byte `Timeval`.

**Runtime.** Go's wasm backend has no threads and cannot be a signal handler,
so the scheduler side is the other wasm ports' (`os_wasm.go`, flag locks), and
notes park the goroutine as on js instead of spinning: wasip1's busy-waiting
`notetsleepg` starves the netpoller once `os/signal` starts its loop, which
made clai's dial time out. The Linux side:

- `_start` calls `runtime.args`; `sysargs` fetches the startup block with
  `startup_get`, widens its 4-byte argv, envp and auxv words to `uintptr`, and
  hands them to Linux's own `goenvs_unix` and `sysauxv` (`AT_RANDOM`,
  `AT_SECURE`).
- The netpoller is `netpoll_epoll.go` unchanged; `epoll_pwait` with a timeout
  is where an idle program sleeps.
- `clock_gettime`, `clock_nanosleep`, `getrandom`, `write`, `exit_group`.
- The heap grows linear memory with `memory.grow` (`mem_wasm.go`), which the
  kernel counts as mapped.
- `SIGPIPE` is set to `SIG_IGN` at startup, so a write to a closed socket is
  `EPIPE`, as Go arranges on Linux with its handler.

**Toolchain.** The linker writes a wasm module (`Hwasip1` head type, not ELF)
that exports `_start` and `memory`, which is what wasixv2's exec layer enters
and binds.

**Standard library tags.** `os`, `os/exec` and `crypto/x509` stop using their
`wasm` files on linux (so x509 reads Linux's certificate paths and
`SSL_CERT_FILE`), `net/http` uses the real dialer, `internal/syscall/unix`
joins the loong64 (statx, renameat2, generic numbers) group, and the vendored
`x/sys/cpu` skips its Linux hwcap code on wasm.

## Results

Measured with wasmer `feat/wasixv2` (908423d), cranelift, on linux/amd64.

- `GOOS=wasix GOARCH=wasm go build std` passes. A module imports only
  `wasixv2` (all 315 syscalls, since the dispatch switch names them all) and
  exports `_start` and `memory`.
- clai builds unmodified (30 MB). Against a local mock that terminates TLS for
  `api.deepseek.com` behind an HTTPS proxy, `clai -cm deepseek-chat query`
  streams the reply token by token as the server sends it (60 ms apart),
  through `HTTPS_PROXY` (CONNECT via the gateway alias 10.0.2.2), TLS 1.3 and
  SSE. A cold run takes about 13 s, nearly all of it compiling 30 MB with
  cranelift and the kernel's preemption rewrite; a cached run takes 2 to 3 s.
- Real network: through the sandbox's egress proxy, `api.anthropic.com`
  answers 401 over TLS and HTTP/2 from inside the guest.
- `go test -short` with `go_wasix_wasm_exec`: strings, strconv, sort, bufio,
  encoding/json, io/fs, context, net/url, crypto/sha256, crypto/tls and
  net/http pass. os passes 131 tests, fails 4 and hangs in
  `TestRootMultiWriteFile`; net, time, path/filepath and sync fail on things
  the exec wrapper does not provide (parent directories of the package,
  `/proc/self/exe`, zoneinfo) plus `TestSplice` and a recovered-panic check in
  sync. The runtime's own tests do not compile yet (`export_linux_test.go`).

## Gaps

- **os/exec** runs, through wasixv2's `spawn` extension: `posix_spawn` in one
  call, with the child's file actions, `Dir` and `execve` taken by the kernel
  inside it (`syscall/exec_wasix_wasm.go`). There is no fork, since wasm cannot
  return twice and wasixv2's `vfork` resumes the parent with a `longjmp` on
  wasm exceptions, which Go's wasm backend does not emit; so `SysProcAttr`
  fields that need code of the child's own (`Credential`, `Setctty`,
  `Foreground`, `Ptrace`, `Pdeathsig`, clone and namespace flags) fail with
  `ENOSYS`, and `PidFD` is always -1. `Wait` blocks on a pidfd in the
  netpoller, so other goroutines run meanwhile.
- **Signals.** Delivery needs a `__stack_pointer` export, an exported
  `__indirect_function_table` and a `(i32, i32, i32) -> ()` trampoline. Go
  could export its SP global as `__stack_pointer`, use `sigaltstack` so the
  frame never lands on a goroutine stack, and have the linker synthesize a
  trampoline that only queues the signal for `os/signal` to pick up at the next
  scheduling point. Synchronous handlers (`SIGSEGV` to panic) stay impossible.
- **Blocking syscalls stop every goroutine.** There is one thread, so a read
  on a blocking fd that is not in the netpoller (a pipe or terminal left in
  blocking mode, a slow file) stalls the whole program, as on wasip1. Sockets
  are fine.
- **Imports.** Every module imports all 315 syscalls. That is fine while the
  kernel exports the whole table, but defeats the ABI's link-time version
  check; direct import calls from the generated `zsyscall` wrappers would
  import only what a program uses.
- **x/sys/unix** needs wasix/wasm files (types, errors, numbers) before
  programs that use it build.
- The browser (wasmer.sh) is untested; the same module should run once
  wasmer-js has the wasixv2 kernel.

## Trying it

You need wasmer built from wasmerio/wasmer's `feat/wasixv2` branch
(`cargo build --release -p wasmer-cli --features cranelift`). Then, from this
checkout:

```sh
WASMER=<wasmer>/target/release/wasmer misc/wasixv2/e2e/run.sh
```

[`e2e/run.sh`](e2e/run.sh) builds the toolchain if needed, runs a hello
world, builds the latest clai (or `CLAI=<checkout>`), and has it stream a
completion from [`mockllm`](e2e/mockllm), a local stand-in provider behind an
HTTPS proxy, so it needs no API key. `run.sh --live <model>` streams from the
real provider instead, with the `*_API_KEY` variables of the environment.

By hand:

```sh
GOOS=wasix GOARCH=wasm go build -o hello.wasm hello.go
wasmer run hello.wasm
PATH=$PATH:$(go env GOROOT)/misc/wasixv2 GOOS=wasix GOARCH=wasm go test -short strings
```

Regenerate the syscall glue after an ABI change, from a
[wasixv2-abi](https://github.com/wasix-org/wasixv2-abi) checkout:

```sh
cargo run -- go --wit wit --goroot <this checkout>
```
