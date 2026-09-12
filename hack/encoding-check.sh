#!/bin/sh
# Fail if any of this package's amd64 assembly carries an AVX-512 encoding.
#
# The runtime detection promises AVX2 and nothing more, so an instruction the assembler encodes with an EVEX prefix
# (a leading 0x62) is illegal on a machine that has AVX2 without AVX-512 -- which is how VPBROADCASTB from a general
# register, an AVX-512BW instruction, produced a SIGILL on the very first instruction of the wide scan.
#
# This runs anywhere, including the arm64 machine this is developed on, which does not compile that file at all: the
# binary is cross-compiled and read, not executed. The symbols are discovered rather than listed, so assembly added
# later is covered without anyone remembering to update this script.
set -e
out=$(mktemp /tmp/ason-enc-XXXXXX.test)
trap 'rm -f "$out"' EXIT
GOOS=linux GOARCH=amd64 go test -c -o "$out" . >/dev/null

syms=$(go tool nm "$out" | awk '$2 == "T" || $2 == "t" {print $3}' | grep '^github\.com/axfor/ason\.' | grep '\.abi0$' | sed 's#^github\.com/axfor/ason\.##; s#\.abi0$##' | sort -u)
if [ -z "$syms" ]; then
  echo "!! found no assembly symbols to check -- the discovery in this script has gone stale"
  exit 1
fi

bad=0
n=0
for f in $syms; do
  n=$((n + 1))
  # Two things this line got wrong once each, both of which made the check pass silently rather than fail loudly:
  # `go tool objdump -s` takes no anchors, so a pattern like ^name$ matches nothing at all; and the encoding bytes
  # are the third field (file:line, address, bytes, mnemonic), not the fourth.
  if go tool objdump -s "$f" "$out" 2>/dev/null | awk '$3 ~ /^62/ {found = 1} END {exit !found}'; then
    echo "!! $f contains an EVEX (AVX-512) encoding; the detection only guarantees AVX2"
    bad=1
  fi
done

[ "$bad" = 0 ] && echo "no AVX-512 encodings in the amd64 assembly ($n symbols checked: $(echo $syms | tr '\n' ' '))"
exit $bad
