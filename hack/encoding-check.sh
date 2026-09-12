#!/bin/sh
# 在 amd64 目标上编出测试二进制，反汇编四个汇编函数，若出现 EVEX（62 开头）编码即失败。
# 这次 CI 的 SIGILL 就是 VPBROADCASTB 从通用寄存器广播被编成 AVX-512BW 造成的，
# 而探测只保证 AVX2，本机 arm64 又不编译这份汇编——所以需要一道能在任何机器上跑的静态门禁。
set -e
out=$(mktemp /tmp/ason-enc-XXXX.test)
GOOS=linux GOARCH=amd64 go test -c -o "$out" . >/dev/null
bad=0
for f in scanStringBodyAVX2 scanStringBodySSE2 cpuid xgetbv; do
  if go tool objdump -s "$f" "$out" 2>/dev/null | awk '{print $3}' | grep -qE '^62'; then
    echo "!! $f contains an EVEX (AVX-512) encoding; the detection only guarantees AVX2"
    bad=1
  fi
done
rm -f "$out"
[ "$bad" = 0 ] && echo "no AVX-512 encodings in the amd64 assembly"
exit $bad
