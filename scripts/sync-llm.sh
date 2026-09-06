#!/bin/sh
# Snapshot the Higress streamxform protocol hooks into examples/llm (package renamed to llm).
# Usage: scripts/sync-llm.sh /path/to/higress
set -e
H="${1:?path to the higress repository}/plugins/wasm-go/pkg/streamxform"
D="$(dirname "$0")/../examples/llm"
for f in proto_claude.go proto_gemini.go proto_qwen.go proto_openai.go proto_openai_variants.go hook_tools.go literals.go prelude.go jsonutil_llm.go proto_probe.go ason.go \
         bench_test.go commit_test.go detect_test.go holdbail_test.go inventory_test.go memorder_test.go openai_shape_test.go order_test.go probe_test.go robust_test.go shuffle_test.go stop_test.go streamxform_test.go; do
  sed 's/^package streamxform$/package llm/' "$H/$f" > "$D/$f"
done
echo "synced from $H"
