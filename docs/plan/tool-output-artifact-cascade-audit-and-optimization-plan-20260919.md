# Tool Output Artifact 链路审计与二次读取优化方案

- 日期：2026-09-19
- 状态：分析完成，待实施
- 关联修复：本轮已完成的指针级联修复（`internal/output/gateway.go`、`internal/output/tool_result_content.go`）
- 关联文档：`docs/aicli/tool_output_contract.md`

---

## 1. 背景：artifact_read 为何被频繁调用

### 1.1 观测到的现象

在一个真实的开发会话（本仓库工作会话）中观测到以下模式：

| 序号 | 调用模式 | 结果特征 |
|---|---|---|
| 1 | `grep` → 指针行 → `artifact_read` → 指针行 → `artifact_read`… | 小结果（24~1085 字节）也被归档，解引用输出尾部再次出现指针行，形成递归级联 |
| 2 | `view` 单文件 743 行（29162 字节）→ 被截断 → `artifact_read` → 输出又贴近预算上界 | view 的行数窗口与模型字节预算错位，折叠发生在错误的层 |
| 3 | `shell` 输出 TUI 转义序列（16~57 KB）→ 截断 → `artifact_read` 分页 2~3 次才能读完全部 | 单次解引用无法覆盖完整原始输出 |
| 4 | 每次 `artifact_read` 的结果又被 gateway 归档成新 artifact | 存储膨胀，指针指向指针 |

### 1.2 本轮已修复的部分（指针级联）

修复前 `renderToolTextForModelHistory` 的行为：

```go
// 修复前
withNotice := appendToolArtifactNotice(full, notice)
if len(withNotice) <= modelToolTextByteBudget {
    return withNotice   // ← 小结果也无条件带指针行
}
```

只要 metadata 里有 `artifact_id`（gateway 对所有非空输出无条件注入），就追加指针行。后果是：
`artifact_read` 解引用一个 artifact → 输出本身又被归档为新 artifact 并追加新指针 →
模型看到的指针指向的内容又以指针结尾 → 递归级联，每次解引用膨胀 ~160 字节。

修复后语义（`tool_result_content.go` `renderToolTextForModelHistory`）：

- 完整文本 ≤ `modelToolTextByteBudget`（默认 12 KiB）且未截断时：
  - **成功的普通结果**：不再追加 record-id 指针行（消除级联的燃料）
  - **失败结果**：保留指针（兼作恢复提示，`tool_error_output_test.go` 契约）
  - **artifact_read 窗口**（metadata 带 `artifact_source_id`）：防御性保留指针，且 gateway 层跳过重复归档（`gateway.go` `isArtifactReadWindow`）
  - **path 型通知**（`raw_output_artifact_path`）：无条件保留（磁盘 artifact 可发现性）
- 超预算截断时：指针行照旧保留（这是指针存在的本意）

修复后该目标成立：**解引用一个指针永远不会产生新的指针级联**（`artifact_read.go:19-22` 注释的设计意图）。

但级联只是表层症状。审计发现 artifact_read 高频调用的根源是**三个结构性问题**，详见第 4 节。

---

## 2. Artifact 产生链路全景

### 2.1 架构总览

```
┌──────────────────────────────────────────────────────────────────┐
│                        工具执行层                                  │
│  view / grep / glob / ls / shell / write / edit / web_search …    │
└──────────────┬───────────────────────────────┬───────────────────┘
               │ 输出文本                        │ shell 类额外产物
               ▼                               ▼
┌──────────────────────────┐   ┌─────────────────────────────────┐
│ 链路 A：Gateway 会话归档   │   │ 链路 B：磁盘 shell artifact       │
│ gateway.go:130-147        │   │ bash.go:1409 ensureLargeHistory… │
│ store.Put(所有非空文本)     │   │ 阈值: ModelToolTextByteBudget()  │
│ → metadata["artifact_id"] │   │ = 12 KiB (bash.go:71-77)         │
│ → ArtifactIDs[]           │   │ → raw_output_artifact_path       │
└──────────────┬───────────┘   └────────────────┬────────────────┘
               │                               │
               ▼                               ▼
┌──────────────────────────────────────────────────────────────────┐
│ 回显层 renderToolTextForModelHistory (tool_result_content.go)      │
│  ≤ 12 KiB 未截断: 成功→无指针 / 失败→保留 / path→保留              │
│  > 12 KiB: head/tail + "Full raw output artifact_id: art_…"       │
└──────────────┬───────────────────────────────────────────────────┘
               │ 模型看到指针行
               ▼
┌──────────────────────────────────────────────────────────────────┐
│ artifact_read（toolkit/tools/artifact_read.go）                   │
│ 会话隔离校验 → 字节窗口 [offset, offset+limit)                    │
│ 默认窗口 8 KiB，上界 budget−512（artifactReadMaxLimitBytes）       │
│ eof/next_offset 分页协议                                          │
└──────────────────────────────────────────────────────────────────┘
```

### 2.2 链路 A：Gateway 会话级归档（所有工具）

**位置**：`backend/internal/output/gateway.go:130-147`

**逻辑**：

```go
if g.store != nil && strings.TrimSpace(text) != "" && !isArtifactReadWindow(result.Metadata) {
    artifactID, err := g.store.Put(ctx, artifact.Record{...})
    envelope.ArtifactIDs = append(envelope.ArtifactIDs, artifactID)
    envelope.Metadata["artifact_id"] = artifactID
}
```

**触发条件**：`store` 存在 + 输出非空 + 非 artifact_read 窗口（最后一个条件是本轮修复新增）。

**覆盖工具**：**全部工具**。`view`、`grep`、`glob`、`ls`、`shell`、`write`、`edit`、
`multiedit`、`web_search`、`web_fetch`、`todo`、`spawn_agent`、MCP 工具……所有经过
`Gateway.Process`（`agent/loop.go` 共 5 处调用点：正常执行 L2471/L2542/L2702/L2861 +
denied/soft-empty 终态 L2929/L2950）的非空文本输出。

**关键语义**：归档无条件，但**指针行只在模型可见文本被截断时**才出现在模型视野。
因此"产生 artifact" ≠ "触发 artifact_read"。小结果归档只造成存储/索引噪声，不直接
引发解引用。

### 2.3 链路 B：Shell 类磁盘 artifact（阈值触发）

**位置与逻辑**：

| 工具 | 位置 | 阈值 | 说明 |
|---|---|---|---|
| `bash` | `bash.go:862-911` + `ensureLargeHistoryOutputArtifact` (L1409-1421) | `capture.TotalBytes > ModelToolTextByteBudget()` | capture 未截断（Truncated=false）但总量超阈值时落盘 |
| `execute_shell_command` | 同 bash 底层 | 同上 | 复用同一执行器 |
| `aicli_exec` | `aicli_exec.go:245` | 同上 | 子 CLI 输出同样落盘 |
| `cmd/aicli/functions/shell.go:393` | CLI 函数层 | **硬编码 12 KiB**（L24） | 与 budget 常量重复定义，见 §5.4 |
| bash 批量模式 | `bash.go:710` | `len(batchOutput) > threshold` | 批量输出整体超阈值时归档合并输出 |

**落盘位置**：`toolctx.ShellOutputArtifactDir(ctx)` 下的 scope 目录（如
`shell-output/toolkit/git_<hash>.txt`），记录写入 `metadata["raw_output_artifact_path"]`。

**渲染语义**：path 型通知 `Full raw output artifact: <path> kind=raw_output`，
无条件追加（不受本轮"未截断则省略"规则影响——磁盘文件不在 artifact store 里，
除了这个 path 没有别的发现渠道）。

**注意**：`CaptureCombinedOutputGuarded` 自身有 256 KB capture limit（可被
`output_bytes_cap`/`disable_output_cap` 参数覆盖）；被 capture 截断的输出
（`Truncated=true`）**不会**再走 `ensureLargeHistoryOutputArtifact`（L1410 提前返回），
因为 capture 已经尽力保留了全部输出。

### 2.4 其他 artifact 相关工具

| 工具 | 与 artifact 的关系 |
|---|---|
| `artifact_read` | 消费方。按字节窗口读取 store 中的记录，带会话隔离、分页协议、防级联窗口上界 |
| `openai_image_generate` | 图片写入会话 artifact 目录（`openai_image_generate.go:26`），走磁盘目录而非 store 记录 |
| `task_output` | 后台任务输出，独立于 artifact store（指针行的 "never pass this id to task_output" 警示即源于模型混淆这两者） |
| 前端 `artifact-output-link` | 消费指针行渲染"查看完整原始输出"按钮（`frontend/src/components/workspace/message-markdown/`） |

---

## 3. 折叠发生的四个层次（现状盘点)

工具输出从产生到进入模型历史，要经过四道尺寸限制，彼此独立、互不感知：

| 层 | 限制 | 单位 | 默认值 | 截断后模型恢复路径 |
|---|---|---|---|---|
| L1 工具内部 | view 的 `viewDefaultLimit` | **行** | 2000 行 | `suggested_next_offset` + `offset/limit` 重调 view |
| L2 shell capture | `CaptureCombinedOutputGuarded.MaxBytes` | 字节 | 256 KB | 无恢复（head/middle/tail 保真压缩） |
| L3 shell 落盘 | `ensureLargeHistoryOutputArtifact` | 字节 | 12 KiB | `raw_output_artifact_path` → 模型需自行读文件（或 artifact_read 不了——这是 path 不是 store id） |
| L4 回显层 | `modelToolTextByteBudget` | 字节 | 12 KiB | `artifact_read(artifact_id=…, offset, limit)` 分页 |

**核心错位**：L1 用行、L2-L4 用字节，且 L1 的 2000 行远超 L4 的 12 KiB
（12 KiB ≈ 150~300 行代码 / 300~600 行日志）。L1 层返回
`is_truncated=false` 的"完整窗口"，在 L4 层必然被截断。

### 3.1 各工具对 L4 预算的实际触碰概率

| 工具 | 典型输出尺寸 | 触碰 12 KiB 预算概率 | 备注 |
|---|---|---|---|
| `view`（无 limit） | 高达数百 KB | **极高** | 2000 行默认窗口是最大来源 |
| `view`（files 批量） | 受 `viewBatchDefaultLimit=200` 行/项 | 中 | 多文件合并后仍可能超 |
| `grep` | 一般小 | 低 | 大 codebase + 宽 pattern 时会超 |
| `shell` | 不定 | 中高 | TUI 程序输出转义序列时异常膨胀 |
| `go test` / 构建输出 | 中大 | 高 | 经 shell 链路 |
| `write`/`edit`/`multiedit` | 小（diff/确认信息） | 极低 | |
| `todo`/`get_goal` | 小 | 极低 | |
| MCP 外部工具 | 不定 | 中 | full-content 优先策略，见 `tool_output_contract.md` |

---

## 4. 结构性根因分析

### 根因 1（主因）：view 的行数窗口与模型字节预算脱节

`view.go:34-36`：

```go
// viewDefaultLimit is the default window size when callers omit limit.
// Large files should still be segmented with explicit offset/limit.
const viewDefaultLimit = 2000
```

问题链条：

1. 模型 `view` 一个 743 行 Go 文件，不带 limit
2. view 返回 29162 字节，`is_truncated=false`（view 认为读完了）
3. L4 回显层把它截成 head/tail + 指针行，**中间内容丢失**
4. 模型此时有两条恢复路径：
   - view 的协议：`offset=200 limit=400` 续读（但 head/tail 视图里没有"当前窗口"概念，
     模型不知道该从哪行继续，只能猜测或重读全文件）
   - artifact_read 的协议：`artifact_read(artifact_id=…, offset, limit)` 按字节分页
5. **模型几乎总是选 artifact_read**：因为 head/tail 视图已经把中间内容剪掉了，
   字节分页能精确到达丢失区间，而行数续读需要多次盲试

后果：view 精心设计的 `is_truncated` / `suggested_next_offset` / `efficiency_advisory`
（view.go:280-291）协议被架空；`view`→截断→`artifact_read` 成为高频路径。

更深一层：view 的注释（L36）说 "Large files should still be segmented with explicit
offset/limit"，但**没有机制强制或引导模型这样做**——efficiency_advisory 只在
`request.Limit >= viewEfficiencyAdvisoryThreshold`(2000) 时软提示，而模型的默认调用
恰恰不带 limit。

### 根因 2：Gateway 归档粒度与解引用需求不匹配

Gateway 把 24 字节的"未找到匹配的内容"与 57 KB 的测试输出同等对待：

- 小结果归档：永不解引用，纯存储与 Search 索引噪声
- `artifact_read` 结果归档：指针指向指针（已修复，跳过）
- 中等结果（1~12 KiB）：归档了但模型看得到全文，解引用概率低

归档本身有正当用途（前端回放、审计、Search），不应取消；问题是**归档策略与
指针策略没有对齐**——没有"多大以上的 artifact 才值得可发现"的分级。

### 根因 3：artifact_read 默认窗口偏保守，分页次数偏多

`artifact_read.go:22`：

```go
artifactReadDefaultLimitBytes = 8 * 1024
```

而单次窗口的硬上界是 `ModelToolTextByteBudget() - 512` ≈ 11.5 KiB
（`artifactReadMaxLimitBytes`，L206-213）。对于 12 KiB 上下的 artifact（shell 落盘
阈值恰好也是 12 KiB），模型用默认 8 KiB 窗口需要 2 次 `artifact_read` 才能读完全部；
第二次调用又经历一次完整的 gateway 流程（归档跳过已修复，但仍有工具往返、
contract JSON 包裹等开销）。

默认值与上界之间 3.5 KiB 的差距没有任何注释说明取舍原因；窗口协议本身
（`eof`/`next_offset`）是健壮的，问题纯粹在默认参数。

### 根因 4（放大器）：TUI 测试输出等异常输入膨胀

`cmd/aicli/commands` 的测试在非交互 shell 下输出大量 ANSI 转义序列
（观测到单条 `go test` 输出 16~57 KB），这类输出：

- 经链路 B 落盘 + 链路 A 归档，双份存储
- 解引用时按字节分页，转义序列没有语义分页点，模型需要读 2~3 页才能确认
  "只是 TUI 噪声"

这类输入是训练/评测环境的固有噪声，无法在协议层消除，但可以通过 L1 层的
字节感知（方案 P0-2）把折叠提前到 view/grep 内部，避免它们进入 L4。

---

## 5. 优化方案

### P0-1：view 默认窗口字节感知（收益最大）

**改动点**：`backend/internal/toolkit/tools/view.go`

方案 A（推荐，最小改动）：

- `viewDefaultLimit` 从 2000 行降到 400 行
- `readFile` 读满 limit 行后检查累计字节数，若已超
  `output.ModelToolTextByteBudget()` 的 70%（约 8.4 KiB，为 head/tail + 元数据留余量）
  则提前停止并把 `is_truncated=true`、`suggested_next_offset` 设为实际停止行
- 元数据新增 `byte_budget_applied: true`，与行数截断区分

方案 B（更彻底）：`readFile` 直接以字节窗口为主协议，行数仅作为可选上限。
改动面大（`viewReadResult` 结构、批量模式、目录预览都要跟着改），收益与 A 相同，
不建议第一轮做。

**效果**：view 输出几乎不再触碰 L4 截断；模型按 view 自己的
`offset/limit` 协议续读，artifact_read 调用量预期下降 60%+（按本会话观测
view→artifact_read 占解引用来源的比例估算）。

**风险**：已有测试依赖 `viewDefaultLimit=2000`（`view_test.go` 需要同步调整）；
依赖"一次 view 读整个文件"的工作流会多一次往返——但换来的是每次往返都在
预算内、无需解引用，净 token 成本下降。

### P0-2：grep/工具层字节预算硬上限

**改动点**：`backend/internal/toolkit/tools/grep.go`（及其他高方差输出工具）

- 匹配行输出累计超过 `ModelToolTextByteBudget()` 的 80% 时停止收集，
  标记 `results_truncated=true` + `next_step: "narrow the pattern or add paths/glob"`
- 与 L4 的 head/tail 截断不同，L1 截断保留**完整的前 N 条匹配**（不剪中间），
  语义上对"找东西"更有用

**效果**：消除 grep→artifact_read 路径（本会话观测中 grep 是第二大解引用来源）。

### P1-1：Gateway 归档分级

**改动点**：`backend/internal/output/gateway.go`

- 文本 < 1 KiB 且无 reducer 摘要需求：跳过 `store.Put`，metadata 记
  `artifact_skipped: "below_threshold"`
- **前提确认**：`store.Search`（gateway_test.go:79 用于全文检索）是否依赖小结果——
  审计结论是 Search 主要服务大输出的关键词回捞，小结果通常模型直接可见，
  跳过归档不损失可检索性；但需在实现时验证 `internal/artifact` 的 Search
  实现与前端回放对缺失记录的容忍度
- 阈值做成与 `modelToolTextByteBudget` 联动的常量（如 budget/12）

**效果**：存储与索引噪声显著下降；对解引用频率无直接影响（小结果本来
不会被解引用）。

### P1-2：artifact_read 默认窗口对齐上界

**改动点**：`backend/internal/toolkit/tools/artifact_read.go:22`

- `artifactReadDefaultLimitBytes` 从 8 KiB 提到 `artifactReadMaxLimitBytes()`
  （即 budget−512 ≈ 11.5 KiB）
- 保留显式 `limit` 参数覆盖能力；`artifactReadMinLimitBytes=1024` 下界不变
- 更新 `TestArtifactReadWindowStaysUnderModelBudget` 边界断言

**效果**：12 KiB 上下的 artifact 一次解引用即可读完，分页调用次数约减半。

**风险**：极低——窗口上界本来就有防级联保证（窗口 + header ≤ budget），
默认值只是从"保守"改为"贴上界"。

### P2：截断视图内的显式续读指引

**改动点**：`backend/internal/output/tool_result_content.go` `formatTruncatedToolTextForModel`

- head/tail 之间的 `[... truncated N bytes ...]` 区域改为携带解引用指引：
  `[... truncated N bytes; read via artifact_read(artifact_id=<id>, offset=<bytes>, limit=<bytes>) ...]`
- 消除"模型看到 head/tail 后走弯路（如重调同参数工具）"的路径

**依赖**：`formatTruncatedToolTextForModel` 目前不知道 artifact id（它只拿文本和
预算），需要把 id 从 `renderToolTextForModelHistory` 传下来——小规模签名调整。

### P3：shell 落盘阈值常量去重

**改动点**：`backend/cmd/aicli/functions/shell.go:24`

- 硬编码 `modelHistoryArtifactThresholdBytes = 12 * 1024` 改为引用
  `output.ModelToolTextByteBudget()`（与 toolkit 侧 `bash.go:75-77` 对齐）
- 消除"调整 budget 后 CLI 函数层不同步"的隐患

### 方案优先级与依赖关系

```
P0-1 (view) ─┐
P0-2 (grep) ─┼─ 独立，可并行，直接削减 artifact_read 调用量
P1-2 (窗口) ─┘
P1-1 (归档分级) ── 独立，存储侧收益
P2 (续读指引) ─── 依赖小重构（id 传递），收益中等
P3 (常量去重) ─── 独立，一行改动
```

---

## 6. 验证计划

### 6.1 单元测试

| 目标 | 测试 |
|---|---|
| P0-1 | `view_test.go`：新增"800 行文件默认窗口在预算内且 is_truncated=true + suggested_next_offset 正确"；调整依赖 2000 行默认值的既有断言 |
| P0-2 | `grep` 新增匹配数超预算截断测试（截断保留前缀完整匹配 + results_truncated 标记） |
| P1-1 | `gateway_test.go`：小结果不产生 ArtifactIDs；`artifact_read_no_cascade_test.go` 回归保持 |
| P1-2 | `artifact_read_test.go`：默认窗口 = maxLimit；`TestArtifactReadWindowStaysUnderModelBudget` 保持通过 |
| P2 | `tool_result_content_test.go`：截断视图中包含带 id 的续读指引 |
| P3 | `cmd/aicli/functions` 既有测试回归 |

### 6.2 集成验证（观测指标）

在真实工作会话中统计一轮开发任务的：

- `artifact_read` 调用次数 / 总工具调用次数（基线：本会话观测约 15%+）
- 因截断产生的解引用链平均长度（基线：1~3 跳）
- 单 turn 工具输出总字节数（含解引用重复内容）

目标：artifact_read 占比降到 5% 以下；解引用链长度 ≤ 1 跳
（P1-2 后 12 KiB 级 artifact 一次读完）。

### 6.3 回归守卫

- 本轮已加的 `TestGateway_SkipsReArchivingArtifactReadWindows` /
  `TestRenderToolResultContentForModel_NoIDNoticeOnUntruncatedSuccess` 必须持续通过
- `tool_error_output_test.go` 的失败结果指针契约必须保持
- 前端 `artifact-output-patterns.ts` 对指针行的解析不受 P2 措辞调整影响
  （P2 若实施，需同步更新前端匹配 pattern 与 `splitTrailingArtifactNotice`）

---

## 7. 明确不做的事

1. **不取消 Gateway 全量归档**：前端回放、审计、Search 都依赖完整记录；
   只做小结果分级跳过（P1-1）。
2. **不把 L1 全部改成字节协议**（§5 方案 B）：改动面与收益不成比例，
   行协议对"读代码文件"场景语义更自然，字节感知只作为停止条件。
3. **不提高 L4 budget 本身**：12 KiB 是 token 成本与信息密度的平衡点，
   有独立配置入口（`SetModelToolTextByteBudget`），按会话调整即可。
4. **不在协议层处理 TUI 转义噪声**（根因 4）：属于输入侧问题，
   L1 字节感知（P0）能顺带缓解。

---

## 8. 遗留问题（超出本方案范围，需单独跟踪）

1. **permission mode 恢复优先级 bug**（本会话最初发现）：
   `cmd/aicli/commands` `TestRestoreChatStateFromRuntimeSessionRestoresRouteTransparency`
   失败——`ctx.PermissionMode` 的零值 "default" 越过了 session metadata 里持久化的
   "plan"。与 artifact 链路无关，修复前即存在。
2. **`cmd/aicli/commands` 测试在非交互 shell 的转义序列噪声**：建议给 TUI
   交互测试加非 TTY 环境守卫或输出重定向。
3. **`internal/chat` `TestSessionActorApproveToolResumesWithoutInMemoryWaiter`**
   全量跑偶发 5s 超时抖动，单跑稳定，疑似测试间资源竞争。

---

## 9. 实施顺序建议

1. **P1-2 + P3**（半小时级）：参数与常量对齐，零风险，立即减少分页次数
2. **P0-1**（view 字节感知）：最大收益项，测试调整面集中在 view_test.go
3. **P0-2**（grep 截断）：与 P0-1 同型，复制模式即可
4. **P1-1**（归档分级）：先验证 Search/回放对缺失小记录的容忍度再合入
5. **P2**（续读指引）：最后做，需要前端联动验证
