#!/bin/sh
# Count what a hot function actually compiles to, for changes whose value can be judged without a throughput
# benchmark: memory loads, bounds checks, branch count, instruction count.
#
# This exists because this machine cannot measure small effects. Three qemu VMs (the ACG cluster nodes) sit on the
# same CPU and keep the load between 7 and 14; the same code measured three times in a row varies by 1.7%, and a
# run polluted by one profiling pass showed 20% spread on a single shape. Counting instructions is not a substitute
# for measuring -- it says nothing about cache locality or branch prediction, which is exactly where the cache-line
# experiment in OPTIMIZATION.md went wrong -- but for "does this remove work per byte" it answers exactly.
#
# Usage: sh hack/asm-profile.sh [package] [symbol-regexp-on-nm-output]
#
# The default pattern depends on the package, because the two have different shapes worth counting: engine has
# three hot functions among a hundred methods, and simd has a handful of symbols of which the interesting one is
# hand-written assembly (it appears as .abi0). Two ways to get a pattern wrong, both of which produce output that
# looks fine: '\.Write$' matches every Write the test binary links in (bufio, bytes, gzip, tabwriter), and a
# pattern anchored only on the import path lists every method in the package. Pass a second argument to override.
set -e

pkg=${1:-./engine}
if [ -n "${2:-}" ]; then
  want=$2
else
  case "$pkg" in
    *simd) want='^github\.com/axfor/ason/simd\.[a-zA-Z]' ;;
    *)     want='^github\.com/axfor/ason/engine\.(\(\*Transformer\)\.(scan|Write)|scanStringBody)$' ;;
  esac
fi
out=$(mktemp /tmp/ason-asm-XXXXXX.test)
trap 'rm -f "$out"' EXIT

go test -c -o "$out" "$pkg" >/dev/null

# Take symbol names from nm rather than guessing a pattern for objdump: `objdump -s` is a regexp over the whole
# name, and getting it wrong reports zero everywhere rather than failing -- which is how this was first written.
#
# The regexp is unanchored, which is the second way to get it wrong and the one that survived the first fix: a
# function's own closures are named after it, so `(*Transformer).scan` also matches `(*Transformer).scan.func1`
# and `.func2`, and the counts printed were their sum. That is where the 2793/315/26/181 recorded for scan in
# commit 8026704 came from: unanchored, the pattern also pulled in `(*Transformer).scanStrUTF8`, whose name scan
# is a prefix of, and `(*Transformer).scan.func1`. Anchored, and with reads separated from writes below, scan on
# that same code is 2596 insns / 708 loads / 242 stores / 20 bounds / 325 branches on arm64, and 2498 / 695 / 310
# / 20 / 377 on amd64. Every symbol is therefore anchored below, and the number of TEXT headers in the
# disassembly is asserted to be exactly one -- a sum is the failure mode here, and a sum looks like an answer.
#
# GOOS/GOARCH are honoured through `go test -c`, so `GOARCH=amd64 sh hack/asm-profile.sh` counts the other
# architecture; worth doing for any change that claims to remove work, since bounds checks and branches compile
# differently on the two.
syms=$(go tool nm "$out" | awk '$2 == "T" || $2 == "t" {print $3}' | grep -E "$want" | sort -u)
if [ -z "$syms" ]; then
  echo "!! no symbols matched $want in $pkg -- the discovery in this script has gone stale"
  exit 1
fi

printf '%-52s %7s %7s %7s %7s %7s\n' "symbol" "insns" "loads" "stores" "bounds" "branch"
for s in $syms; do
  esc=$(printf '%s' "$s" | sed 's/[().*+?^$|\\]/\\&/g')
  asm=$(go tool objdump -s "^${esc}$" "$out" 2>/dev/null)
  ntext=$(printf '%s\n' "$asm" | grep -c '^TEXT ' || true)
  if [ "$ntext" != "1" ]; then
    echo "!! $s: objdump matched $ntext functions, not 1 -- these counts would be a sum"
    printf '%s\n' "$asm" | grep '^TEXT ' | sed 's/^/!!   /'
    exit 1
  fi
  asm=$(printf '%s\n' "$asm" | grep -v '^TEXT ')
  n=$(printf '%s\n' "$asm" | grep -c '' || true)
  # Go assembly is src, dst: a memory operand before a comma is a read, one at the end of the line is a write.
  # The regexp this replaced ('MOV.*\(R[0-9]+\)') counted both as "loads", which is where the 315 recorded for
  # scan in commit 8026704 came from -- on top of that figure being three functions summed.
  ld=$(printf '%s\n' "$asm" | grep -cE '\bMOV[A-Z]*\b[^,]*\([A-Z0-9]+\)[^,]*,' || true)
  st=$(printf '%s\n' "$asm" | grep -cE '\bMOV[A-Z]*\b.*\([A-Z0-9]+\)[[:space:]]*$' || true)
  bc=$(printf '%s\n' "$asm" | grep -cE 'panicIndex|panicSlice|panicBounds' || true)
  br=$(printf '%s\n' "$asm" | grep -cE '\b(J[A-Z]{1,3}|B(EQ|NE|LT|LE|GT|GE|HI|HS|LO|LS|CC|CS|MI|PL)|CBN?Z|TBN?Z)\b' || true)
  short=$(printf '%s' "$s" | sed 's|github.com/axfor/ason/||')
  printf '%-52s %7s %7s %7s %7s %7s\n' "$short" "$n" "$ld" "$st" "$bc" "$br"
done
