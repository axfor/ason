# examples

每个目录一个可运行程序：从 stdin 按块读 JSON（`-chunk` 控制块大小，默认故意很小），边读边转换，写到 stdout；
stdin 是终端时用程序自带的样例输入。

| 目录 | 演示 |
|---|---|
| `passthrough` | `BaseProtocol`：没动的字节一个不改 |
| `rename` | `Pass().As`：只改顶层 key 的名字 |
| `rewrite` | `KeyProbe`：捕获顶层 key 并原位替换，与 sjson 一致 |
| `redact` | `Prefix`：只看前几个字节，其余 Skip——长字符串不进内存 |
| `restructure` | `PushObj / PushArr / Enter().Flat()`：一个输入容器落到多层嵌套输出 |
| `defer-replay` | `Defer / Release`：值先于决定其形状的字段到达 |
| `subhook` | `Enter().Via`：整棵子树交给另一个 Protocol |
| `attachment` | `Prefix + Pass().Wrap`：data URL 拆前缀，主体直通到另一个形状 |
| `observe` | 只看不改：Skip 一切、数元素，输出扔掉 |
| `fallback` | 提交点：不支持发生在 64KB 之前时调用方换路 |

```
go run ./examples/rename
echo '{"items":[{"k":"v"}],"owner":"team/42"}' | go run ./examples/rewrite -chunk 3
for d in examples/*/; do [ -f $d/main.go ] && go run ./$d < /dev/null; done
```
