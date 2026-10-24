# 远程输出裁剪与 artifact_read 的分页契约

## 远程确实有输出裁剪，而且是三层

| 层 | 位置 | 作用 |
|---|---|---|
| 抓取上限 | `Limits.MaxBytesPerCall` / `CaptureLimit` | 单次调用的**内存**闸门（不决定模型看到多少） |
| 工具预算 | `toolcore/output.ToolBudget(tool)` | 每个工具声明的模型可见窗口（view 32K / grep 32K / glob 16K / ls 16K / shell 32K / artifact_read 32K） |
| 远程硬顶 | `Limits.ModelVisibleCap`（默认 64K，硬上限 `BudgetCeiling`） | 信任边界，只能**收窄**不能放宽 |

`ResolveCap` 的算法：

```
hardCap = min(toolWindow, remoteHardCap)
cap     = declared > 0 ? min(declared, hardCap) : hardCap
```

shell 走的是特化的 `ShapeShellWindow`：head+tail 折叠，**保尾部**（测试结论、
最终报错都在尾部），中间的省略量、字节区间、页数全部写进 metadata，
并提示用 `artifact_read` 按 `offset`/`limit` 取回中段。

## 发现的缺陷：artifact_read 被二次折叠

`remoteOutput.Shape` 对所有 `KindText` 结果无条件调用 `ShapeText`，
**包括 `artifact_read`**。而 `artifact_read` 本身就是分页工具。

实测（`ModelVisibleCap=1024`）：

```
requested=4096    returned=1024   <-- FOLDED BY SHAPER
requested=32768   returned=1024   <-- FOLDED BY SHAPER
requested=61440   returned=1024   <-- FOLDED BY SHAPER
requested=65536   returned=1024   <-- FOLDED BY SHAPER
```

一个请求都拿不到请求的字节数。连带三个后果：

1. **请求与响应不一致**：要 4 KB 回 1 KB；
2. **内容丢失**：head+tail 折叠丢弃页面中段——正是调用方要读的字节；
3. **续读指针失真**：`suggested_next_offset` 按 `len(返回)` 计算，
   而正文已被折叠，指针描述的窗口与实际内容不再对应。

换句话说：**一个分页工具被折叠，等于它自己把自己要读的数据吃掉。**

## 修复

三处，缺一不可：

### 1. 分页工具不参与折叠（`remotetools/runtime.go`）

新增 `toolcoreoutput.IsSelfSizedWindowTool(tool)`，`artifact_read` 命中时
只标记 `skip_render_truncation` 与 `model_visible_budget_bytes`，直接返回原文。

### 2. 页大小由工具自己定（`toolcore/artifact_handler.go`）

原来默认 `64*1024`，比工具自身 32 KiB 预算还大——请求上限和声明上限自相矛盾。现在：

- 默认 = 自身窗口 − 头部预留（512 B）；
- 调用方传更大的 `limit` 会被**收敛**到该上限，而不是被折叠；
- 下限 1024 B，避免极端配置下返回空页。

### 3. 发布自己的续读契约

新增 `artifact_window_bytes` / `artifact_window_limit` / `artifact_eof` /
`artifact_next_offset`，与本地 `artifact_read` 的契约对齐；同时保留原有的
`eof` / `is_truncated` / `suggested_next_offset` 以免破坏既有调用方。

### 一个 import cycle 的处理

`toolcore/output` 导入 `toolcore`，所以 `toolcore` 不能反向导入 `output`。
预算常量与 metadata key 因此定义在 `artifact_handler.go` 内，
由 `output.ToolBudget("artifact_read")` 保持同值（注释里写明这层关系）。

## 修复后实测

`ModelVisibleCap=8192`（窗口 8 KiB）：

```
requested=4096    returned=4096     ← 完全遵守
requested=32768   returned=7680     ← 收敛到窗口 8192-512
requested=61440   returned=7680     ← 同上，不折叠
requested=65536   returned=7680     ← 同上，不折叠
```

**整份抓取逐页读回校验**：抓取 108894 字节，按 4096 逐页读回
**恰好 108894 字节**——无缺口、无重复。

## 固化为回归测试（`remotetools/artifact_page_test.go`）

- `TestRemoteArtifactReadPageIsNotFolded` —— 要 4096 必须回 4096，且正文不能出现折叠标记。
- `TestRemoteArtifactReadHonoursItsOwnWindow` —— 超限请求收敛到窗口，而不是折叠。
- `TestRemoteArtifactReadPagesCoverEveryByte` —— 逐页读完整份抓取，字节数必须相等。
- `TestRemoteShellFoldedOutputPointsAtArtifact` —— 折叠**只有**在省略中段可取回时才允许。

最后一条是前三者的前提：如果 shell 折叠后不再给 artifact 指针，折叠就是静默丢数据。

## artifact_read 是否在默认 MCP 列表里

**不在**。它由 `Artifacts.Root` 控制，与 profile 无关：

```go
// tools.go, enabledTools()
if c.Artifacts.Root != "" {
    base = append(base, artifactTools...)
}
```

实测各场景（`remote_dev__artifact_read` 是否出现）：

| 场景 | 出现 |
|---|---|
| 默认配置（未设 artifact root） | ❌ |
| 设置 `Artifacts.Root` | ✅ |
| readonly profile + `Artifacts.Root` | ✅ |
| `ReadOnly=true` + `Artifacts.Root` | ✅ |
| custom profile，`Tools=[grep]` + `Artifacts.Root` | ❌ |
| custom profile，`Tools=[grep, artifact_read]`，**无** store | ✅（但调用必失败） |

三个要点：

1. **门槛是 store，不是 profile。** 没有 artifact store 时列出这个工具，
   每次调用都会返回「artifact store is not configured」——声明了一个必然失败的能力。
2. **read-only 不会砍掉它。** 解引用 artifact 是读操作，只读目标必须保留，
   否则 agent 生成的每个折叠输出都无法取回。`ReadOnly` 的硬过滤白名单里
   明确包含 `ToolArtifactRead`。
3. **custom profile 是唯一例外**：它以 `Tools` 为完整清单，所以显式写
   `artifact_read` 会被列出，即使没配 store。这是调用方明确的越权选择，
   失败信息是可行动的 `UNSUPPORTED`（不是 panic、不是空读取）。

## 固化为（`remotetools/artifact_listing_test.go`）

- `TestRemoteArtifactReadIsNotInTheDefaultSurface`
- `TestRemoteArtifactReadAppearsWhenStoreIsConfigured`
- `TestRemoteArtifactReadSurvivesReadOnlyGating`
- `TestRemoteArtifactReadWithoutStoreFailsCleanly`
- `TestRemoteArtifactReadIsNarrowableByToolList`
