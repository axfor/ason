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
syms=$(go tool nm "$out" | awk '$2 == "T" || $2 == "t" {print $3}' | grep -E "$want" | sort -u)
if [ -z "$syms" ]; then
  echo "!! no symbols matched $want in $pkg -- the discovery in this script has gone stale"
  exit 1
fi

printf '%-52s %7s %7s %7s %7s\n' "symbol" "insns" "loads" "bounds" "branch"
for s in $syms; do
  esc=$(printf '%s' "$s" | sed 's/[().*+?^$|\\]/\\&/g')
  asm=$(go tool objdump -s "$esc" "$out" 2>/dev/null)
  n=$(printf '%s\n' "$asm" | grep -c '')
  ld=$(printf '%s\n' "$asm" | grep -cE '\b(MOVBU|MOVWU|MOVD|MOVB|MOVW)\b.*\(R[0-9]+\)' || true)
  bc=$(printf '%s\n' "$asm" | grep -cE 'panicIndex|panicSlice|panicBounds' || true)
  br=$(printf '%s\n' "$asm" | grep -cE '\b(B(EQ|NE|LT|LE|GT|GE|HI|HS|LO|LS|CC|CS))\b' || true)
  short=$(printf '%s' "$s" | sed 's|github.com/axfor/ason/||')
  printf '%-52s %7s %7s %7s %7s\n' "$short" "$n" "$ld" "$bc" "$br"
done
