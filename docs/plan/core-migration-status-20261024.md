# 工具是否都已转入 core：逐项核实

## 结论先说

**没有。** 现状是「remote 全部走 core，本地只有 grep / glob 走 core」。

这不是设计清单写错，而是**迁移只完成了一半**：core 里几乎每个工具都有 handler，
但本地工具大多仍执行自己的 legacy 实现。

## 核实方法

不看清单，看调用点：

1. `toolcore` 里存在哪些 `New*Handler` —— 定义了什么；
2. `internal/remotetools/registry.go` 用了哪些 —— remote 侧的真实绑定；
3. `internal/toolkit/tools/` 里是否构造了同样的 handler —— 本地侧的真实绑定。

## core 里的 handler（9 个）

| Handler | 包 | 文件 |
|---|---|---|
| `NewWriteHandler` | mutatecore | handler.go |
| `NewAppendHandler` | mutatecore | handler.go |
| `NewEditHandler` | mutatecore | handler.go |
| `NewApplyPatchHandler` | mutatecore | handler.go |
| `NewGlobHandler` | searchcore | handler.go |
| `NewGrepHandler` | searchcore | handler.go |
| `NewHandler`（view） | viewcore | handler.go |
| `NewHandler`（ls） | lscore | handler.go |
| `NewArtifactReadHandler` | toolcore | artifact_handler.go |

## remote 侧：全部使用 core

`remotetools/registry.go` 逐个绑定 core handler：

```
lscore.NewHandler        -> ls
viewcore.NewHandler      -> view
searchcore.NewGlobHandler-> glob
searchcore.NewGrepHandler-> grep
mutatecore.NewWrite/Append/Edit/ApplyPatchHandler -> write/append_write/edit/apply_patch
toolcore.NewArtifactReadHandler -> artifact_read
```

还有一处间接复用：`filetransport/service.go` 的写入走 `mutatecore.Write` /
`mutatecore.Append`。

## 本地侧：grep 与 glob 走 core

`toolkit/tools` 有两个 core 调用点，都收敛到同一个委托函数：

```go
// toolcore_runtime.go
func executeCoreSearchTool(ctx, tool, params) (*toolkit.ToolResult, error)
```

- `grep`：`grep_core_switch.go` 按参数集分流（`rg_args` 等 core 未覆盖的键仍走
  legacy），命中的调用进入 `executeCoreSearchTool`。
- `glob`：`glob.go` 的 `Execute` **只有一行委托**，本地 legacy 实现已删除。

`view` / `ls` / `write` / `edit` / `append_write` / `apply_patch`
在本地还是各自的实现（`view.go`、`ls.go`、`write.go`、`edit.go`、
`append_write.go`、`apply_patch.go`）。

## 一个直接矛盾

`toolcore.V1CoreInventory()` 把 `multiedit` 列为 Phase 3 的 core 工具，
但 **core 里没有 multiedit handler**，只有 `mutatecore` 的 write/append/edit/apply_patch。
`shell` 列为 Phase 4，core 里也没有 shell handler（只有 `shellexec` 这个执行原语，
不是 Handler）。

清单里还有 `LocalOnly: true` 的工具（aicli_exec / fetch / download / web_search /
sourcegraph / openai_image_generate / code_* / todos），这些明确设计为本地，不算缺口。

## 所以「主要工具」要看问的是哪一侧

| 视角 | 现状 |
|---|---|
| remote 工具面 | 已全部走 core（grep/glob/view/ls/write/append_write/edit/apply_patch/artifact_read） |
| 本地工具面 | grep / glob 走 core；其余仍是 legacy |
| 两套实现是否分叉 | glob 只有一套代码（本地 `Execute` 直接调用 core handler），`glob_local_remote_equivalence_test.go` 对拍 public 入口；grep 仍有 legacy 兜底分支 |

## 尚未完成的迁移（按缺口大小）

1. **`multiedit`**：core 完全没有，清单却写它是 core。
2. **`shell`**：core 没有 Handler；`shellexec` 只是执行原语，不是工具层。
3. **本地 `view` / `ls`**：core 已有 handler，本地未接。
4. **本地 `write` / `edit` / `append_write` / `apply_patch`**：core 已有 handler，本地未接。
5. **本地 `artifact_read`**：本地是独立实现（`toolkit/tools/artifact_read.go`），
   core 的 `NewArtifactReadHandler` 只被 remote 用。
6. **grep 的 legacy 兜底**：`rg_args` / `pattern_file` 之外的参数组合里仍有
   未进 core 的路径；glob 已经没有这一层。

## 建议的收敛顺序

本地 grep 的翻转经验可以直接复用：先在 core 补齐差异 → 加开关按参数集分流 →
同源等价性测试 → 删 legacy 分支。read 系（view/ls/glob）比 mutate 系风险低，
应先做；`multiedit` 需要先决定它是否真的属于 core（或从清单里移除）。

## glob 已收敛（2026-10-24，`Execute` 已翻转）

`glob` 现在只有一套实现：本地 `GlobTool.Execute` 与 remote 都执行
`searchcore` 的同一个 handler。原来在 `toolkit/tools/glob.go` 里的遍历、
排序、分页、brace 展开、字节窗口都已删除，语义搬进 core 并被测试锁定。

- 深度语义：本地/remote 都改成标准 glob（`*.go` 只匹配根层，`**/*.go` 递归），
  由 `MatchGlobRelative` 承担；`MatchGlobPath` 保留 basename 回退，专供
  grep 的 include/exclude。
- 排序分页：`SortByModTime` 先全量排序再分页，walker 与 rg 一致（旧实现边早停
  边排序，第 2 页会重复/丢项）。rg 分支曾**先按 walk 顺序截断再排序**，同样会
  重复，已改为排序后截断。
- 默认值：`DefaultGlobLimit` 500 → **100**（与本地一致）。
- 输出形态：去 header、空结果「未找到匹配项」、分页提示两种形态。
- 目录匹配：本地 glob 一直返回目录（`**/docs` 命中 `docs/`），core 的 walker
  曾经跳过所有目录。现在目录是正常匹配项；rg 只用于「最后一段像文件名」的
  模式，其余交给 walker，因为 `rg --files` 不列目录。
- 单文件根：`path` 指向文件时，模式命中该文件就返回它（本地旧行为），core
  之前会直接走目录遍历并返回空。
- 搜索预算：20s 覆盖 walker 与 rg 两条引擎；超时返回已收集结果并标记
  `timed_out`，不再是「有 rg 就报错、没 rg 就给部分结果」。
- 字节窗口：16 KiB 在渲染分页时按提示长度预留后裁剪，`files` / `count` /
  `next_offset` 描述的是**实际交付**的行，不再让 L4 二次折叠。
- schema：`searchcore.GlobParameters()` 是唯一来源，本地与 remote 不再各写一份。
