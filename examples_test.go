package ason_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 每个示例程序都编译并用固定输入运行一遍，stdout 与 examples/testdata/<name>.golden 逐字节比对。
// 更新黄金文件：UPDATE_GOLDEN=1 go test -run TestExamplesGolden .
func TestExamplesGolden(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	inputs := map[string]string{
		"passthrough":  "{\n  \"id\" : 1 ,\n  \"items\" : [ { \"k\" : \"v\" } ]\n}\n",
		"rename":       `{"id":"m","count":10,"n":{"count":1}}`,
		"rewrite":      `{"items":[{"k":"v"}],"owner":"team/42"}`,
		"redact":       `{"api_key":"sk-1234567890abcdef","user":"u"}`,
		"restructure":  `{"id":"m","items":[{"a":1},{"b":2}],"flag":true}`,
		"defer-replay": `{"body":"be brief","kind":"note"}`,
		"subhook":      `{"meta":{"a":1,"b":{"c":2}},"d":3}`,
		"attachment":   `{"image":"data:image/png;base64,iVBORw0KGgo=","n":1}`,
		"observe":      `{"owner":"team/42","items":[{"a":1},{"b":2},3]}`,
		"fallback":     `{"a":1,"b":tru}`,
		"chatconv": `{"model":"demo/large","messages":[{"role":"system","content":"be brief"},{"content":"hi","role":"user"},` +
			`{"role":"user","content":[{"text":"look","type":"text"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]}],` +
			`"stop":["END"],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"temperature":0.5}`,
	}
	dirs, err := filepath.Glob("examples/*/main.go")
	if err != nil || len(dirs) == 0 {
		t.Fatalf("找不到示例: %v", err)
	}
	bin := t.TempDir()
	for _, mainGo := range dirs {
		name := filepath.Base(filepath.Dir(mainGo))
		in, ok := inputs[name]
		if !ok {
			t.Fatalf("示例 %s 没有登记输入（examples_test.go 的 inputs）", name)
		}
		exe := filepath.Join(bin, name)
		build := exec.Command("go", "build", "-o", exe, "./examples/"+name)
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("编译 %s 失败: %v\n%s", name, err, out)
		}
		for _, chunk := range []string{"1", "3", "4096"} {
			cmd := exec.Command(exe, "-chunk", chunk)
			cmd.Stdin = strings.NewReader(in)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			_ = cmd.Run() // fallback 示例以 1 退出属于预期
			golden := filepath.Join("examples", "testdata", name+".golden")
			if os.Getenv("UPDATE_GOLDEN") != "" && chunk == "1" {
				if err := os.WriteFile(golden, stdout.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%s: 缺少黄金文件 %s（UPDATE_GOLDEN=1 生成）", name, golden)
			}
			if !bytes.Equal(stdout.Bytes(), want) {
				t.Fatalf("%s chunk=%s 输出与黄金文件不同:\n got  %q\n want %q\n stderr %s", name, chunk, stdout.String(), want, stderr.String())
			}
		}
	}
}
