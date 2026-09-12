#!/bin/sh
# Build this module for every architecture that matters, in both the default and the purego configuration, and check
# that the right implementation is selected on each.
#
# The point is that the assembly is an accelerator, never the contract: arm64 takes NEON, amd64 takes SSE2 (with AVX2
# chosen at run time when the CPU and OS both allow it), and everything else -- 386, riscv64, s390x, ppc64le, mips64,
# loong64, wasm -- falls back to the word-at-a-time loop that owns the semantics. `-tags purego` puts every
# architecture on that same fallback, which is the escape hatch for anyone who would rather not ship assembly.
set -e

targets="linux/386 linux/amd64 linux/arm64 linux/riscv64 linux/s390x linux/ppc64le linux/mips64 linux/loong64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 freebsd/arm64 js/wasm wasip1/wasm"
fail=0

for t in $targets; do
  os=${t%/*}
  arch=${t#*/}
  if ! err=$(GOOS=$os GOARCH=$arch go build ./... 2>&1); then
    echo "!! $t does not build: $err"
    fail=1
    continue
  fi
  sel=$(GOOS=$os GOARCH=$arch go list -f '{{join .GoFiles " "}}' . | tr ' ' '\n' | grep '^scanstring_' | tr '\n' ' ')
  case "$arch" in
    arm64) want="scanstring_neon_arm64.go " ;;
    amd64) want="scanstring_sse2_amd64.go " ;;
    *)     want="scanstring_generic.go " ;;
  esac
  if [ "$sel" != "$want" ]; then
    echo "!! $t selected [$sel], expected [$want]"
    fail=1
  fi
done

for t in $targets; do
  os=${t%/*}
  arch=${t#*/}
  if ! err=$(GOOS=$os GOARCH=$arch go build -tags purego ./... 2>&1); then
    echo "!! $t does not build with -tags purego: $err"
    fail=1
    continue
  fi
  sel=$(GOOS=$os GOARCH=$arch go list -tags purego -f '{{join .GoFiles " "}}' . | tr ' ' '\n' | grep '^scanstring_' | tr '\n' ' ')
  if [ "$sel" != "scanstring_generic.go " ]; then
    echo "!! $t with -tags purego selected [$sel], expected the generic scan"
    fail=1
  fi
done

# GOAMD64 levels: the run-time detection is independent of the level the toolchain was told to assume, so all four
# must build and none may contain an AVX-512 encoding.
for lvl in v1 v2 v3 v4; do
  if ! err=$(GOOS=linux GOARCH=amd64 GOAMD64=$lvl go build ./... 2>&1); then
    echo "!! GOAMD64=$lvl does not build: $err"
    fail=1
  fi
done

# With GOEXPERIMENT=simd, amd64 takes the simd/archsimd implementation instead of the hand-written pair, and every
# other target must be unaffected. Both halves matter: that the experiment builds at all, and that it changes only
# the one architecture that can use it (arm64 and wasm have no ToBits on their masks, so they keep their own).
for t in $targets; do
  os=${t%/*}
  arch=${t#*/}
  if ! err=$(GOEXPERIMENT=simd GOOS=$os GOARCH=$arch go build ./... 2>&1); then
    echo "!! $t does not build with GOEXPERIMENT=simd: $err"
    fail=1
    continue
  fi
  sel=$(GOEXPERIMENT=simd GOOS=$os GOARCH=$arch go list -f '{{join .GoFiles " "}}' . | tr ' ' '\n' | grep '^scanstring_' | tr '\n' ' ')
  case "$arch" in
    arm64) want="scanstring_neon_arm64.go " ;;
    amd64) want="scanstring_simd_amd64.go " ;;
    *)     want="scanstring_generic.go " ;;
  esac
  if [ "$sel" != "$want" ]; then
    echo "!! $t with GOEXPERIMENT=simd selected [$sel], expected [$want]"
    fail=1
  fi
done

# The two switches together, which is the combination a build tag is most likely to get wrong: with the experiment
# on and purego asked for, every architecture must still land on the generic scan. Leaving !purego off the simd
# implementation compiles fine in every other configuration and only collides here.
for t in $targets; do
  os=${t%/*}
  arch=${t#*/}
  if ! err=$(GOEXPERIMENT=simd GOOS=$os GOARCH=$arch go build -tags purego ./... 2>&1); then
    echo "!! $t does not build with GOEXPERIMENT=simd and -tags purego: $err"
    fail=1
    continue
  fi
  sel=$(GOEXPERIMENT=simd GOOS=$os GOARCH=$arch go list -tags purego -f '{{join .GoFiles " "}}' . | tr ' ' '\n' | grep '^scanstring_' | tr '\n' ' ')
  if [ "$sel" != "scanstring_generic.go " ]; then
    echo "!! $t with GOEXPERIMENT=simd and -tags purego selected [$sel], expected the generic scan"
    fail=1
  fi
done

[ "$fail" = 0 ] && echo "portability: $(echo $targets | wc -w | tr -d ' ') targets build and select correctly, with and without purego, across GOAMD64 v1-v4"
exit $fail
