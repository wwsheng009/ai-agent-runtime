# 工具是否都已转入 core：逐项核实

## 结论先说

**没有。** 现状是「remote 全部走 core，本地只有 grep 走 core」。

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

## 本地侧：只有 grep 走 core

`toolkit/tools` 里唯一的 core 调用点是 `grep_core_switch.go`：

```go
handler := searchcore.NewGrepHandler(...)
```

`view` / `ls` / `glob` / `write` / `edit` / `append_write` / `apply_patch`
在本地都还是各自的实现（`view.go`、`ls.go`、`glob.go`、`write.go`、`edit.go`、
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
| 本地工具面 | 只有 grep 走 core；其余仍是 legacy |
| 两套实现是否分叉 | 是。同一工具在两处各有一份实现，行为一致性靠测试而非共用代码 |

## 尚未完成的迁移（按缺口大小）

1. **`multiedit`**：core 完全没有，清单却写它是 core。
2. **`shell`**：core 没有 Handler；`shellexec` 只是执行原语，不是工具层。
3. **本地 `view` / `ls` / `glob`**：core 已有 handler，本地未接。
4. **本地 `write` / `edit` / `append_write` / `apply_patch`**：core 已有 handler，本地未接。
5. **本地 `artifact_read`**：本地是独立实现（`toolkit/tools/artifact_read.go`），
   core 的 `NewArtifactReadHandler` 只被 remote 用。

## 建议的收敛顺序

本地 grep 的翻转经验可以直接复用：先在 core 补齐差异 → 加开关按参数集分流 →
同源等价性测试 → 删 legacy 分支。read 系（view/ls/glob）比 mutate 系风险低，
应先做；`multiedit` 需要先决定它是否真的属于 core（或从清单里移除）。
