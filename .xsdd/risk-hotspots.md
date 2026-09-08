# 遗留代码风险热点（既有代码主动缺陷分析）

> 由 `xsdd-risk-hotspots.sh` 客观信号派生（churn=git 变更次数 / 复杂度=LOC+分支密度 / 债=TODO·FIXME 标记 / 测试=有无同名测试）。**这是历史项目缺陷分析不够的主动面**：不等 bug 爆，先按风险排出该【先加测试 / 先重构 / 先审查】的地方。

## 概览

| 指标 | 值 |
|---|---|
| 源文件数 | 33 |
| 巨型文件（>600 行）| 3 |
| 技术债标记（TODO/FIXME/HACK/XXX/deprecated）| 0 |
| ⚠ 高 churn 无测试危险区（改≥5 次 + 无同名测试）| 1 |

## Top 15 风险热点（churn × 复杂度，两高优先——缺陷高发地）

| # | 文件 | churn | LOC | 分支 | 债 | 测试 | 风险提示 |
|---|---|---|---|---|---|---|---|
| 1 | `engine.go` | 20 | 1617 | 305 | 0 | ✅ | 巨型文件(先拆) 高变更高复杂(重构候选)  |
| 2 | `examples/llm/proto_claude.go` | 3 | 1080 | 267 | 0 | ⬜ | 巨型文件(先拆)  |
| 3 | `examples/llm/proto_gemini.go` | 3 | 682 | 162 | 0 | ⬜ | 巨型文件(先拆)  |
| 4 | `jsonutil.go` | 4 | 556 | 103 | 0 | ⬜ | — |
| 5 | `examples/llm/proto_qwen.go` | 3 | 546 | 137 | 0 | ⬜ | — |
| 6 | `writer.go` | 7 | 274 | 43 | 0 | ⬜ | 改多无测试(先补测试)  |
| 7 | `examples/llm/proto_openai.go` | 3 | 295 | 73 | 0 | ⬜ | — |
| 8 | `examples/chatconv/conv/conv.go` | 2 | 439 | 110 | 0 | ✅ | — |
| 9 | `examples/llm/proto_openai_variants.go` | 3 | 257 | 53 | 0 | ⬜ | — |
| 10 | `action.go` | 4 | 158 | 17 | 0 | ⬜ | — |
| 11 | `examples/llm/hook_tools.go` | 3 | 111 | 31 | 0 | ⬜ | — |
| 12 | `validate.go` | 1 | 185 | 42 | 0 | ✅ | — |
| 13 | `examples/observe/main.go` | 3 | 86 | 11 | 0 | ⬜ | — |
| 14 | `examples/fallback/main.go` | 3 | 74 | 11 | 0 | ⬜ | — |
| 15 | `errors.go` | 3 | 71 | 7 | 0 | ✅ | — |

> ⚠ **另有 18 个文件同样命中热点判据，未列出**（按 churn × 复杂度降序取前 15 —— 清单长到没人读就等于没产出）。
> 要看全部：`--top 33` 重跑。**本表是抓重点用的，不是完整的遗留风险清单。**

## 建议处置顺序（风险驱动）

1. **改多无测试的热点先补【表征测试】**（钉住当前行为，再改才安全 —— 采纳 Path B 铁律）。
2. **高变更×高复杂**的重构候选：先补测试再 `/XSDD:code-simplify`（Chesterton's Fence：先懂为什么）。
3. **巨型文件**按业务边界拆（难懂难改是 bug 温床）。
4. **债标记**逐条评估：真债 → 建 `/XSDD:bug` 或排期；过时注释 → 清。

> 新增需求前，对照本清单看要碰的模块是否命中热点 —— 命中 = 该功能回归风险高，需求/设计的边界节声明 + `/XSDD:impact` 精确评估 + 先补测试。

## 变更耦合（总一起改、但代码上没有依赖）

**架构侵蚀最强的信号。** 两个文件在提交里总是成对出现，说明它们之间有一条【代码里看不见的依赖】——
改一个必须记得改另一个，靠的是人的记忆而不是编译器。这也是重构时真正的边界所在。

| 次数 | 文件 A | 文件 B |
|---|---|---|
| 6 | `engine.go` | `writer.go` |
| 6 | `engine.go` | `README.md` |
| 5 | `docs/OPTIMIZATION.md` | `README.md` |
| 4 | `engine.go` | `zerocopy_test.go` |
| 4 | `docs/OPTIMIZATION.md` | `engine.go` |
| 4 | `docs/DESIGN.md` | `README.md` |
| 4 | `docs/DESIGN.md` | `engine.go` |
| 3 | `README.md` | `writer.go` |
| 3 | `jsonutil.go` | `README.md` |
| 3 | `example_test.go` | `README.md` |

> 共现 ≥3 次才列。没有行 = 本仓历史里没有明显的隐藏耦合（提交粒度很细时也会这样）。

## 代码年龄（老而不动 = 资产；老而常改 = 债）

| 最后改动 | 累计改动次数 | 文件 |
|---|---|---|
| 2026-09-06 | 1 | `examples/llm/README.md` |
| 2026-09-06 | 1 | `examples/llm/testdata/claude.jsonl.gz` |
| 2026-09-06 | 1 | `examples/llm/testdata/gemini.jsonl.gz` |
| 2026-09-06 | 1 | `examples/llm/testdata/openai.jsonl.gz` |
| 2026-09-06 | 1 | `examples/llm/testdata/openrouter.jsonl.gz` |
| 2026-09-06 | 1 | `examples/llm/testdata/qwen_compat.jsonl.gz` |
| 2026-09-06 | 1 | `examples/llm/testdata/qwen_native.jsonl.gz` |
| 2026-09-06 | 1 | `examples/llm/testdata/zhipu.jsonl.gz` |

> 最久没动的几个。**改动次数少 = 稳定资产，动它要格外小心（没人记得它为什么这么写）；
> 改动次数多却很久没动 = 曾经翻腾过的老债，现在没人敢碰。**

## 知识分布（巴士因子）

| 提交数 | 作者 |
|---|---|
| 32 | axfor |

> 提交高度集中在一两个人身上 = **离职风险**，也是评审人选择的依据。

### 声明的 owner vs 实际作者

**单仓模式，没有逐仓的 owner 声明可对照。**

> 这里不留空 —— 留空和「对照过且一致」在产物里长得一模一样。

> **没覆盖的部分**：业务模块表的「负责人」列没有参与对照 ——
> 模块到代码路径没有可靠映射，硬编一套猜测就成了「看起来在对照、其实在猜」。

## 缺陷密度（commit message 里的 fix/bug 落到哪些文件）

| 被修复次数 | 文件 |
|---|---|

> **客观的「哪里最容易出错」**——不是靠印象，是数出来的。这几个文件改动前应先补测试。
