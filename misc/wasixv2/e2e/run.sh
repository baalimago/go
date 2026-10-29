#!/usr/bin/env bash
# Copyright 2026 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

# End-to-end check of the wasix/wasm port: builds a Go program and clai
# (github.com/baalimago/clai) with this toolchain, runs them on wasmer's
# wasixv2 kernel, and checks that clai streams an LLM completion.
#
# Usage:
#
#	run.sh                        # against mockllm, a local stand-in provider
#	run.sh --live <model> [query] # against the real provider of <model>
#
# Environment:
#
#	WASMER    wasmer built from wasmerio/wasmer's feat/wasixv2 branch
#	          (default: wasmer on PATH)
#	CLAI      clai checkout to build (default: github.com/baalimago/clai@$CLAI_VERSION)
#	CLAI_VERSION  module version when CLAI is unset (default: latest)
#	*_API_KEY     passed to clai in --live mode
#
# The toolchain is built with make.bash first if bin/go is missing.

set -euo pipefail

e2e=$(cd "$(dirname "$0")" && pwd)
goroot=$(cd "$e2e/../../.." && pwd)
go=$goroot/bin/go
wasmer=${WASMER:-wasmer}
work=$(mktemp -d)
mock_pid=

cleanup() {
	if [[ -n $mock_pid ]]; then kill "$mock_pid" 2>/dev/null || true; fi
	rm -rf "$work"
}
trap cleanup EXIT

step() { printf '\n==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

live_model=
query="Say hello"
if [[ ${1:-} == --live ]]; then
	live_model=${2:?--live needs a model, for example claude-sonnet-4-5}
	query=${3:-$query}
fi

command -v "$wasmer" >/dev/null || fail "no wasmer: set WASMER to a build of wasmer's feat/wasixv2"
if [[ ! -x $go ]]; then
	step "Building the toolchain"
	(cd "$goroot/src" && ./make.bash)
fi

wasix_build() { GOOS=wasix GOARCH=wasm "$go" build "$@"; }

step "hello: a Go program on wasixv2"
cat >"$work/hello.go" <<'EOF'
package main

import (
	"fmt"
	"os"
	"runtime"
)

func main() { fmt.Println("hello from", runtime.GOOS+"/"+runtime.GOARCH, os.Args[1:]) }
EOF
wasix_build -o "$work/hello.wasm" "$work/hello.go"
got=$("$wasmer" run -q "$work/hello.wasm" -- a b)
echo "$got"
[[ $got == "hello from wasix/wasm [a b]" ]] || fail "hello printed: $got"

step "clai: building for wasix/wasm"
if [[ -n ${CLAI:-} ]]; then
	(cd "$CLAI" && wasix_build -o "$work/clai.wasm" .)
else
	mkdir "$work/clai-mod"
	(
		cd "$work/clai-mod"
		"$go" mod init e2e >/dev/null 2>&1
		"$go" get "github.com/baalimago/clai@${CLAI_VERSION:-latest}"
		wasix_build -o "$work/clai.wasm" github.com/baalimago/clai
	)
fi
ls -lh "$work/clai.wasm" | awk '{print $5, $NF}'

mkdir "$work/home"
run_args=(run -q --volume "$work/home:/home/user" --env HOME=/home/user --env NO_COLOR=1)

if [[ -n $live_model ]]; then
	step "clai: streaming from $live_model"
	for key in $(compgen -e | grep '_API_KEY$' || true); do
		run_args+=(--env "$key=${!key}")
	done
	"$wasmer" "${run_args[@]}" "$work/clai.wasm" -- -r -cm "$live_model" query "$query" </dev/null
	echo
	exit 0
fi

step "mockllm: starting a stand-in provider"
(cd "$goroot/misc" && "$go" build -o "$work/mockllm" ./wasixv2/e2e/mockllm)
"$work/mockllm" -ca "$work/ca.pem" -addr-file "$work/addr" &
mock_pid=$!
for _ in $(seq 50); do [[ -s $work/addr ]] && break; sleep 0.1; done
[[ -s $work/addr ]] || fail "mockllm did not start"
addr=$(<"$work/addr")
port=${addr##*:}

step "clai: streaming a completion through HTTPS_PROXY and TLS"
# 10.0.2.2 is the host's loopback as the guest sees it. deepseek-chat speaks
# the chat completions API, which mockllm imitates for any host.
mkdir "$work/ca"
cp "$work/ca.pem" "$work/ca/"
run_args+=(--volume "$work/ca:/ca" --env SSL_CERT_FILE=/ca/ca.pem
	--env "HTTPS_PROXY=http://10.0.2.2:$port" --env DEEPSEEK_API_KEY=mockllm)
start=$(date +%s)
"$wasmer" "${run_args[@]}" "$work/clai.wasm" -- -r -cm deepseek-chat query "$query" </dev/null | tee "$work/clai.out"
echo
got=$(<"$work/clai.out")
echo "($(($(date +%s) - start))s, including compiling the module)"
[[ $got == *"Hello from Go on wasixv2!"* ]] || fail "clai printed: $got"

step "PASS"
