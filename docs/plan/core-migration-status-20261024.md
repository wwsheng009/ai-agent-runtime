# 工具是否都已转入 core：逐项核实

## 结论先说

**没有。** 现状是「remote 全部走 core，本地只有 grep / glob / ls / write /
append_write 走 core」。

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
- `ls`：同样只有一行委托；本地 legacy 的遍历、渲染与字节裁剪已删除。
- `write`：只有一行委托；本地 legacy 的编码探测、原子替换、文档拒绝、
  内联载荷闸门与 `expected_sha256` 前置条件都搬进了 core。
- `append_write`：只有一行委托；`create_if_missing` 默认值修正为 true（该工具
  的存在意义就是首块创建），本地 `filetransport` 包装层已删除。
- `edit`：只有一行委托；本地 1078 行匹配层（六层阶梯、closest 片段搜索、
  歧义保护、诊断文案）整段平移进 `mutatecore/edit_match.go`。
- `apply_patch`：只有一行委托；解析器、应用器与诊断渲染层全部搬进 core，
  本地只剩 schema、freeform 语法元数据与参数归一化。

`view` 在本地还是自己的实现（`view.go`）。

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
| 本地工具面 | grep / glob / ls / write / append_write / edit / apply_patch 走 core；其余仍是 legacy |
| 两套实现是否分叉 | 上述工具都只有一套代码（本地 `Execute` 直接调用 core handler），各自有对拍 public 入口的等价性测试；grep 仍有 legacy 兜底分支 |

## 尚未完成的迁移（按缺口大小）

1. **`multiedit`**：core 完全没有，清单却写它是 core。
2. **`shell`**：core 没有 Handler；`shellexec` 只是执行原语，不是工具层。
3. **本地 `view`**：core handler 只覆盖纯文本窗口；本地的图片直通、文档转换、
   notebook 渲染、符号解析、去重与读取台账都还没有对应实现。这是最大的一块，
   需要先决定哪些属于工具能力（应进 core）、哪些属于宿主能力（应留在本地）。
4. **本地 `artifact_read`**：本地是独立实现（`toolkit/tools/artifact_read.go`），
   core 的 `NewArtifactReadHandler` 只被 remote 用。
5. **grep 的 legacy 兜底**：`rg_args` / `pattern_file` 之外的参数组合里仍有
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

## ls 已收敛（2026-10-24，`Execute` 已翻转）

`ls` 也只有一套实现。此前面向模型的两面行为不同：本地是带 `depth` 的树形
列表（emoji 标记 + 统计行），remote 是单层 `d name/` 列表。按「以本地为准」，
树形语义进 core，`depth` 默认 1 正好等于 remote 原来的行为。

- `lscore.Parameters()` 是唯一 schema；remote 不再单独声明 `depth` 缺失的版本。
- 深度上限 `MaxDepth=10`，超出则 clamp（调用方要的是「尽可能深」而不是报错）；
  显式 `depth: 0` 才报 `INVALID_ARGUMENT`。
- 单层默认 200 条、多层默认 1000 条：同一个数字在两种遍历下含义差太多。
- `path` 指向文件时返回大小与权限位（本地旧行为），不再报错。
- 空目录是 `empty_result`/`outcome=empty` 的成功结果，不是失败。
- 16 KiB 窗口在渲染时按 header + trailer 预留后裁剪，`returned_count` 是实际
  交付行数。
- 路径不存在时带候选提示；本地由 sandbox 计算，remote 由 workspace 计算，
  通过 `PathHinter` 接进同一个 handler。
- 删掉了 `lscore.Read` / `Render` / `Metadata` / `Listing` / `Entry`：handler
  改用 `ReadTree` 后，这些只剩自己的测试在引用，属于死代码。

## write 已收敛（2026-10-24，`Execute` 已翻转）

`write` 现在只有一套实现。翻之前它是「core 有引擎、本地有一套策略」的分裂状态，
而且 split 的方向是错的：`expected_sha256` 只在本地 schema 里、core handler 根本
不读它——同一个工具在两个面上确实是两种能力。

搬进 core 的（都是工具能力，远程同样需要）：

- `expected_sha256`：并发前置条件，含 `absent`；回放（内容已相同）不再算改动。
- 文档容器拒绝（pdf/docx/xlsx/pptx/epub/rtf…）：纯文本写进这些格式会毁掉原文档。
  先于写目标校验执行，否则 `report.pptx.` 会先报「父目录不存在」。
- 内联载荷上限（单段 64 KiB / 总量 128 KiB）：超过后传输层可能截断参数，
  静默截断的写入比拒绝更糟。
- `patch` 元数据（unified diff）与 `idempotent_replay` / `mutated_paths` 语义：
  回放时 `mutated_paths` 必须为空，否则调用方会以为改动落地了。

留在宿主、通过可选接口注入的（远程没有对应状态）：

- `toolcore.MutationGuard`：读后写账本。写入前否决，并把 pre-write 快照的
  读状态（fresh/partial/stale/unread/unverified）作为注解返回。
- `toolcore.MutationObserver`：落盘后刷新账本、通知知识层。
- `toolcore.BackupPolicy`：非 git 工作树才落 `.backups/` 快照。
- `toolcore.KindHinter`：目录当成文件时给近似名字候选。

这一轮修出的真 bug：

1. `ValidateWriteTarget` 用 `Lstat`，把符号链接一律当成特殊文件——链接到普通
   文本的别名写不进去，而链接到只读路径的写入反而绕过了检查。
2. 本地 adapter 的 `Resolve` 只做 `OpRead` 准入、`Permissions()` 返回 nil：
   翻转后**只读路径保护完全失效**。现在按真实操作查权限。
3. `staleWriteFailure` 广告的是 raw 字节哈希，而 `expected_sha256` 比较的是
   解码文本——BOM/UTF-16 文件照提示重试必然再失败。
4. 回放结果仍把路径列进 `mutated_paths`。
5. `ResultToToolkit` 丢掉了 precondition 的错误身份，`errors.Is(...ErrWritePrecondition)`
   失效；现在在边界处翻译。
## edit 已收敛（2026-10-24，`Execute` 已翻转）

`edit` 只有一套实现。翻之前 core 的匹配只有三种 CRLF 拼写变体，本地有完整的
六层阶梯；差一层就意味着「同一个 old_string，本地命中、远程报 stale」。这直接
决定编辑能不能落地，属于工具能力，所以整段平移而不是在 core 重写。

搬到 `mutatecore/edit_match.go`（≈1100 行）的：

- 六层匹配阶梯：精确 → CRLF → 尾随空白 → 缩进对齐 → 空行窗口漂移 → token 回退，
  加上替换重建时未触及行保持原字节。
- 最近片段搜索：token 回退、窗口收紧、相似度排序（`FindClosestEditSnippet*`、
  `scoreEditClosestWindow`），与 apply_patch 共用 `PatchLineSimilarity` 的排序口径。
- 歧义保护：命中数 + 行号；`next_action` 与诊断文案。
- 共享文案：`FormatEditSuccessContent`（`…\n\n文件差异:\n```diff`）、
  `FormatEditClosestLines`（`%6d|` 行号前缀）、`TruncateDiagnosticText`。

同时补齐了 core handler 与本地等价的周边语义：

- 文档容器拒绝、缺失目标报候选路径（而不是「先建父目录」）、文件大小上限
  （`failure_class` / `file_size` / `max_file_bytes`）。
- 成功路径发布 `occurrences` / `replacement_count` / unified diff / `edited_snippet` 行号。
- **只有 `write` 在 stale 拒绝中广告 `expected_sha256`**（`supportsExplicitRevision := tool == "write"`）。
- `EditTool.insideGitWorkTree` 原先没有被 runtime 使用（测试假绿），现在
  `CoreRuntime()` 真的把一个缝传下去，并有反向探测的测试锁住。

`multiedit` 也改调 core 的同一批函数，`apply_patch.go` 共用
`mutatecore.FormatEditSuccessContent`：一工具一文案。

## apply_patch 已收敛（2026-10-24，`Execute` 已翻转）

`apply_patch` 现在只有一套实现。本地文件从 1106 行降到 98 行，只剩 schema、
freeform lark 语法元数据、`patch` / `_raw` 参数归一化，以及一行委托。

搬进 core 的：

- `patch_parse.go`：解析器（Add/Delete/Update/Move、`*** End of File`、
  bare `+` 行、折叠行展开后的边界识别）。
- `patch.go` 的 `ApplyPatch`：事务式应用（先全部 resolve/authorize，再逐个落盘，
  失败按 undo 栈回滚）、`CreateParents`、`Move` 语义、CRLF/BOM 保持。
- `patch_render.go`：`PatchSummaryMessage`（`补丁已应用：…；影响 N 个路径`）、
  `PatchHunkDiagnostic`（`无法定位 hunk: … 期望内容: … 最接近的当前内容（第 N 行附近）`）、
  `PatchHunkErrorMetadata`、`IsPatchInvalidSyntaxError`、`PatchFailureNextAction`、
  `ExtractPatchSuggestedViewLine`、`PatchNotFoundMessage`。
- `diff.go` 的 `UnifiedDiffStates`：带 `/dev/null` 一侧的 diff（新增/删除不会被
  渲染成「空文件」），`multiedit` 的 `patch` 元数据也改用它，本地 diff 渲染器删除。

修掉的等价缺口（翻转后由本地测试逐个暴露并补齐）：

1. **`CreateParents`**：补丁会创建它命名的目录（`moved/b.txt`）。core 之前用
   与 write 相同的 `ValidateWriteTarget` 先拒绝「父目录不存在」，等于把 patch
   降级成「目标目录必须已存在」。现在 `authorizeWriteTarget(..., createsParents)`
   区分两者：普通 write 仍然拒绝，patch 不拒绝。
2. **缺失目标的候选提示**：update / delete 找不到文件时，core 只报
   `cannot update missing file`；本地一直给候选路径。新增 `PatchBindings.PathHint`
   接上同一个 `toolcore.PathHinter`（remote 侧由 workspace 实现，本地由 sandbox 实现），
   文案统一为 `文件不存在，无法更新: <path>\n<候选>`。
3. **hunk 诊断**：core 原来只回一句 `hunk 1 does not match the file`。现在
   `ApplyHunks` 直接产出带期望行、最近窗口与 `next_action` 的完整诊断，并由
   `PatchHunkError.Path` 标注是哪个文件；`mutationFailure` 认识 `*PatchHunkError`，
   把 `failure_class` / `suggested_view_offset` / `suggested_view_limit` /
   `current_snippet` / `current_snippet_start_line` / `file_path` 一起发布。
4. **非法语法 = TOOL_INVALID_ARGS**：解析失败原先落到 `INTERNAL_ERROR` +
   通用 next_action。现在 `IsPatchInvalidSyntaxError` 分类，code 走
   `INVALID_ARGUMENT`，`failure_class=invalid_patch_syntax`，并给出针对语法的
   next_action。本地面在 `executeCoreSearchTool` 里把 `INVALID_ARGUMENT`
   本地化为既有的 `TOOL_INVALID_ARGS`（remote 面保持 core 的拼写，
   `ResultToToolkit` 不做这层翻译）。
5. **成功文案与 diff**：`Applied patch to N file(s):` 改成与 edit 同源的
   `补丁已应用：… 文件差异: ```diff …`，`PatchResult.Diff` 逐路径累积
   （move 会出两条：源端 `/dev/null`、目标端新建）。
6. **变更通知**：`apply_patch` 原先不调用 `MutationObserver`，改完之后
   「解析成功但不通知」会静默丢掉知识层增量。现在每个被改的路径都上报
   （move 报两条，源端 post-image 为 nil），`PatchFileResult.NewBytes` / `MovePath`
   是为这条通道新增的字段。

## 一个曾经静默失效的 bug（本轮）

`mutationFailure` 会重新给消息加 `code: ` 前缀，而 patch resolver 里
`resultAsError` 已经把渲染好的文案塞进 `Error.Message`，于是同一个失败被拼成
`NOT_FOUND: NOT_FOUND: parent directory does not exist: … (next: …) (next: …)`。
现在 `Error.Rendered` 记住「这个失败已经渲染过」，`mutationFailure` 直接原样返回，
不再二次加工。

## 下一轮：view（最大的剩余缺口）

量级：本地 `toolkit/tools/view.go` 1606 行 / 56 KB，core 的
`viewcore/{view,handler,schema}.go` 合计约 620 行 / 17 KB。差的不是参数，
是一整层读取能力，因此和其它工具一样必须平移，不能在 core 重写一遍。

本地有、core 没有的（按依赖顺序）：

1. **读取形态**：`readTailLines` / `orderedTailLines` / `trimTailToByteBudget`
   的负向窗口（「最后 N 行」）、`viewLineRead` 的行截断记账
   （`accountViewLineTruncation`、`clampLinePrefix`）、`truncateLongLine`。
   core 的 `Read` 目前只有正向窗口 + 字节上限。
2. **目录预览**：`listDirectoryPreview`（`view` 一个目录时列条目）。
3. **非文本结果**：`binaryNoteResult`（二进制只给摘要而不是硬失败）、
   `unsupportedPathResult`（设备/保留名拒绝，带 `path_refused` 元数据）。
4. **文档与图片**：文档容器渲染（docx/pdf/…，`recordDerivedRender` 记录派生
   产物）、图片直通与 `SetImageAttachmentTokenLimit` 的 token 预算。
   这一层要区分「工具能力」（渲染-> core）与「宿主能力」（附件投递-> 本地）。
5. **符号解析**：`CodeIndexResolver` + `resolveViewSymbol` + `annotateViewSymbolNote`
   （`view --symbol`）。属于工具能力，依赖注入的索引实现留在宿主。
6. **路径拼写治愈**：`healSpellingPath`（拼错的文件名给候选并继续读）。
   注意与 `PathHinter` 的关系：hinter 只解释失败，治愈要真的读成功。
7. **读取台账与效率提示**：`annotateIgnoredBatchWindow`、
   `attachViewEfficiencyHints`（告诉调用方这次窗口够不够、要不要续读）。
8. **批处理**：`executeBatch` 的分节预算（`viewBatchSectionBudgetBytes`、
   `attachBatchWindowMetadata`、`trimBatchTail`、`compactViewSummary`）。
   core 的 `readBatch` 只有逐项聚合，没有分节预算与尾裁剪。

建议顺序：先 1+3（纯读窗口语义，风险低、测试最密），再 7+8（元数据与预算），
然后 2+6（目录与拼写），最后 4+5（文档/图片/符号，需要先定宿主/工具边界）。
每一步都按 apply_patch 那轮的做法：整段平移 -> 翻转 `Execute` -> 用本地测试
逐个暴露等价缺口 -> 补 core -> 全量回归。
## view 第一段已收敛（2026-10-25）：读取窗口层

按上一节的顺序先做第 1 项（纯读窗口语义）＋第 2/7 项里不依赖宿主的那几块。
做法和 edit/apply_patch 那两轮一样：整段平移，不是重写。

量级变化（同一把尺子）：

| 文件 | 之前 | 现在 |
|---|---|---|
| `toolkit/tools/view.go`（本地） | 1606 | **1040** |
| `toolcore/viewcore/read.go`（新） | — | 688 |
| `toolcore/viewcore/view.go` | 328（含 `Read`/`Render`/`Metadata`） | 188（只剩 `LineReader`） |
| `toolcore/textcodec/encoding.go`（新） | — | 171 |

平移进 core 的（逐段对应本地实现）：

- `ReadText` / `ReadWindow`：编码感知读取（BOM/UTF-16 先解码）、正向窗口、
  负向窗口（`-N` 读最后 N 行，环形缓冲 + `orderedTailLines` + `trimTailToByteBudget`）、
  字节预算、行数上限、`readLine` 的 rune 截断与 `accountLineTruncation` 的
  逐行丢失记账。
- `Stats`：本地 `viewReadResult` 的全部字段（tail 系列、reader clamp 计数、
  长行丢失、original bytes、空文件标记）。宿主侧的台账/去重不需要重新推导窗口语义。
- `WindowMetadata` / `BuildResult` / `AttachEfficiencyHints`：**纯文本与派生渲染
  （文档/notebook）共用同一份窗口元数据**，本地 `view_window_meta.go` 随之删除。
- `FormatContent`、`IsBinaryContent`、`DirectoryPreview`、`ClampLinePrefix`。

编码编解码器挪到 `toolcore/textcodec`：读取路径不必再 import 变更路径（原来的
`viewcore -> mutatecore` 依赖是这次拆包的原因），`mutatecore` 保留同名薄别名，
写入侧所有调用点零改动。

## 这一轮定下的三处「以本地为准」

1. **一种渲染**：`N: 文本`，没有头部。core 原来的
   `=== path (offset N, limit M) ===` 头部本地从来没有过，已删除；批处理的每节
   也统一成本地的 `===== path =====`（原来是 `=== path ===`）。
2. **一种行截断**：2000 rune + 诚实标记（`…[line truncated: N more chars]`）。
   core 原来的 4096 字节上限被替换——两者同时存在时，同一个文件在两侧会被截到
   不同的位置。
3. **一种字节预算**：工具自己声明的 32 KiB 窗口，所有面共用
   （`byteBudget` 依次取 surface `CaptureLimit` → `Definition.DefaultOutputBudget`
   → `DefaultByteBudget`）。

顺带修掉一个陷阱：`ByteBudget == 0` 在本地语义里不是「不限量」而是「每窗口一行」。
`ReadWindow` 现在把非正值当默认预算，免得某个面漏传参数就悄悄退化成一行窗口。

## 证据

- `TestViewLocalAndCoreRenderTheSameWindow`（新，`toolkit/tools`）：同一个请求分别
  走本地工具和 core handler（remote 执行的那条路），断言 **content 逐字节相同**
  且 17 个窗口元数据字段相同，覆盖 8 种窗口形态（整文件/窗口/tail/tail+limit/
  越界/空文件/UTF-16/长行）。这是「一套实现、两个位置」第一次可被测试直接验证，
  而不是靠人工比对。
- `viewcore` 的读取测试按新 API 重写（窗口、tail、越界与空文件提示、字节预算、
  长行记账、UTF-16 解码、目录预览）。
- 本地 `view_read_hardening_test.go` 一行断言改为引用 `viewcore` 常量后原样通过，
  说明本地测试仍然守着同一套契约。
- `go build ./...`、`go vet ./...` 干净；`./internal/...` 全绿。

**本机预存在失败（与本次改动无关，已单独验证）**：
`internal/api/runtimeapi` 的 `TestSessionMCPAddCreatesWorkspaceFileWhenMissing`
断言「首次 add 前应回落到 workspace 作用域」，但本机存在
`~/.aicli/mcp.yaml`（user 作用域，`Exists:true`），于是前置条件不成立。
用 `HOME=/tmp/emptyhome` 重跑同一个测试即通过——失败来自开发机 home 目录，
不是代码回归。

## 下一轮：view 剩下的四块

1. **非文本结果**（上一节第 3 项）：`binaryNoteResult`（二进制给 MIME+大小+恢复
   路线而不是硬失败）、`unsupportedPathResult`（设备/FIFO/保留名在 open 之前拒绝，
   带 `path_refused`/`refusal_reason`）。纯策略，进 core 没有障碍。
2. **批处理预算**（第 8 项）：`viewBatchSectionBudgetBytes`、尾块预留
   `trimBatchTail`、`attachBatchWindowMetadata`、失败明细上限
   `viewBatchFailureDetailLimit`、`compactViewSummary`。core 的 `readBatch` 目前
   只有逐项聚合，没有分节预算——这是 remote 侧现在最大的一处能力缺口。
3. **路径拼写治愈**（第 6 项）：`healSpellingPath` 要真的读成功，不能只像
   `PathHinter` 那样解释失败。需要决定：宿主提供候选（拼写枚举依赖文件系统），
   core 决定「唯一候选就采用、多个候选就报错」的策略。
4. **宿主钩子 + 翻转 `Execute`**（第 4/5/7 项）：符号索引、读台账、去重、
   图片附件、文档/notebook 渲染器、`annotateIgnoredBatchWindow`。
   这些是宿主状态，需要像 `MutationObserver`/`MutationGuard` 那样定成可选接口，
   定完才能把本地 `Execute` 翻到 core handler 上。

顺序建议：1 → 2 → 3 → 4。前两块纯工具能力，风险低；第 3 块要先定宿主边界；
第 4 块是翻转前的最后一步，必须等接口定义清楚。## view 第二段已收敛（2026-10-25）：非文本结果 + 特殊路径拒绝

按上一节剩余清单的第 1 项。这一段的产出不只是「搬过去」，还把一处**三方重复**
收敛成一句话——这正是靠「两侧对拍」才暴露出来的。

### 搬运

| 原来在哪 | 现在在哪 |
|---|---|
| `toolkit/tools/path_guard.go`（279 行规则） | `toolcore/pathguard/pathguard.go`（`NameReason` / `ModeReason` / `RefusalMessage` / `RefusalNextAction`） |
| `view.go` 的 `unsupportedPathResult` | `viewcore.RefusalResult` |
| `view.go` 的 `binaryNoteResult` | `viewcore.BinaryNote` |
| `view.go` 的目录自动列目录分支 | `viewcore.DirectoryListing` |
| `view_image.go` 的 `readViewFileHead` | `viewcore.ReadFileHead`（本地留一个薄别名，4 个调用点零改动） |

本地 `path_guard.go` 只剩别名 + 本地错误词汇（`devicePathRefusalError`、
`errDevicePath`），`multiedit`、`sandbox_support`、`toolcore_runtime` 和三个
guard 测试文件全部不动。

### 顺带修掉的三方重复（对拍发现的）

同一条 `/dev/null` 在三个地方被拒绝，用了三种说法：

1. `viewcore` 的新 handler（结构化拒绝：`path_refused` / `refusal_reason` / `next_action`）；
2. 本地 core runtime 的 `Resolve()`，它自己先拒了，用的是短句
   `不支持的特殊文件（reason）: path`；
3. view 自己的 open 前守卫，又是另一份文案。

于是「同一个调用」会因为「谁先跑」而得到两句话——`TestViewLocalAndCoreRenderTheSameWindow`
的 `refused_device` 用例第一次跑就红在这里。现在三处都产出
`pathguard.RefusalMessage`，并且 handler 把守卫提到 `Resolve()` **之前**（按请求的
原样拼写判定），这样即使某个面的 resolver 自己也会拒绝设备路径，也不会换一种形状。
守卫排在权限钩子前面是刻意的。

### 同时收敛的行为（仍以本地为准）

- **目录**：core 以前回 `use ls for directories`，本地是自动列目录。现在统一为
  自动列出（`is_directory` / `auto_listed` / `depth=1`）。
- **二进制**：core 以前给正文盖一个 `binary: true` 元数据却照样把 NUL 字节交给
  模型；现在统一为一行 MIME + 大小 + 恢复路径。
- 删掉随调用点一起消失的死代码：`viewcore.ValidUTF8`、本地 `viewDirPreviewLimit`。

### 证据

- 对拍测试从 8 个用例扩到 **11 个**（新增二进制、目录、被拒设备），全部
  content 逐字节相同 + 17 个窗口元数据字段相同；
- `viewcore/refusals_test.go`（新）：在**没有 sandbox 守卫的 runtime** 上钉住
  拒绝、自动列目录、二进制摘要三个形状——这是 remote 侧的形态，本地 runtime 会
  在 Resolve 阶段先拒，测不到 handler 自己那道门；
- 本地 guard 套件（`device_path_deny_test`、`path_guard_test`、
  `path_guard_review_fixes_test`、`view_binary_note_test`、`view_test` 的目录断言）
  原样通过；
- `go build` / `go vet` 干净；`./internal/...` 全绿（除上一节记录的本机预存在失败）。

提交：`596541b6`。

## 下一轮：批处理预算（剩余第 2 项）

`executeBatch` 是 remote 侧现在最大的能力缺口，也是唯一还整段留在本地的读逻辑。
要搬的东西：分节预算（`viewBatchSectionBudgetBytes`）、跳过项记账
（`batch_skipped_files` / `batch_bytes_emitted`）、尾块预留与裁剪
（`viewBatchTailReserveBytes`、`trimBatchTail`）、失败明细上限
（`viewBatchFailureDetailLimit = 20` 的 `===== errors =====` 块）、全部批处理元数据
（`request_count` / `succeeded_count` / `failed_count` / `partial_failure` / `items` /
`batch_default_limit_applied`）、以及全失败时的 `批量读取失败` 形状。

**为什么这一块必须先定接口**：本地的去重/读台账是通过
`ViewFileRequest.dedupDefer / ledgerDefer / dedupRecordDefer` 三个回调实现的，语义是
「分节真的被交付了才提交」。这三个回调之所以存在，正是因为**本地 batch 才是知道
「这一节有没有被聚合上限丢掉」的地方**。一旦 core 拥有 batch 循环，这个「何时提交」
的判断就归 core，宿主只需要一个直白的钩子：

- `ReadServed(tool, absPath, window)`：宿主记台账/窗口缓存（对应 `MutationObserver` 的读侧）；
- `DedupStub(tool, absPath, window) (stub, commit, hit)`：宿主可以拿一个未变的重复窗口
  换掉这次读取，core 在**真的把这一节写入正文之后**才调用 `commit`。

接口定完，本地 `ViewFileRequest` 上那三个 `*Defer` 字段就可以删掉，
`recordDerivedRender` 也跟着简化。这一步做完，view 只剩符号解析、图片/文档渲染
和 `annotateIgnoredBatchWindow`，然后就能翻转 `Execute`。## view 第三段已收敛（2026-10-25）：批处理聚合窗口

上一节说「批处理必须先定接口」，定完就做了。这是 remote 侧最大的一处能力缺口，
也是唯一还整段留在本地的读逻辑。

### 搬进 `viewcore/batch.go`

- **逐项默认**：`BatchDefaultLimit = 200`、compact 的 10 行头窗口、`MaxLimit` 截断。
- **聚合窗口**：`BatchSectionBudgetBytes()` = 声明的 64 KiB 减去 4 KiB 尾块预留
  （下限是单窗口的 32 KiB）；两道准入检查（读之前一次、分节拼出来之后一次）；
  `===== batch window =====` 块公布「已读取 X/N，跳过 K 个」。
- **有界错误块**：`BatchFailureDetailLimit = 20` 条明细 + 省略数量的披露。
- `trimBatchTail`（按 UTF-8 边界裁剪并说明「尾部细节已按聚合预算截断」）、失败行、
  以及全部批处理元数据词汇（`request_count` / `succeeded_count` / `failed_count` /
  `partial_failure` / `items` / `batch_default_limit_applied` / `batch_skipped_*`）。
- 全失败时的 `批量读取失败: …` 形状。

**逐项读取是注入的**（`viewcore.ReadItem`）：某个面独有的逐项能力（宿主渲染器、
宿主的读状态）保留在自己手里，而**不该漂移的策略**由 core 独占。它还顺带返回这次
读取欠宿主的账（`viewcore.DeliveredRead`）——batch 只在**这一节真的被写进正文之后**
才 commit。这部分是整块里最微妙的：被聚合上限丢掉的那一节既不能进读台账，也不能
烧掉去重条目（review m13/H4）。

### 新接口：`toolcore.ReadObserver` + `toolcore.ReadWindow`

```
ReadDelivered / ReadRemembered / RememberedStub
```

和 `MutationObserver` 同一个理由：会话读台账与「已交付窗口」缓存是**宿主状态**
（remote 主机没服务过这次读取，台账在那里没有意义），而**什么时候可以写条目**的
策略不能每个面各写一遍。所以「什么算一个完整窗口」由共享 handler 判定
（`viewcore.DeliveredWindow` 挨着 `WindowMetadata`），宿主只知道写到哪去；本地
runtime 用已有的 ledger / dedup 助手实现这三个方法，没有这个扩展的 runtime 就是
无状态的——remote 一直如此。

### 对拍又逼出两处收敛

1. **缺失路径文案带候选**：本地说 `路径不存在: a.txt\n<候选>`，core 原来只回一句
   stat 错误。现在 core 接上了早就存在的 `toolcore.PathHinter`（本地 sandbox 与
   remote workspace 各自实现），两边同一句话。
2. **core 的批处理词汇退场**：`count` / `files` / `failed` 与 `[compact: …]` 摘要
   换成上表那套本地词汇与 `... lines_read=…, is_truncated=…` 摘要。

### 证据

- `TestViewLocalAndCoreRenderTheSameBatch`（新）：三个批次——带一个失败项的混合批、
  compact 扫描、以及**超出分节预算**的批——分别走本地工具与 core handler，断言正文
  **逐字节相同**且 16 个批处理元数据字段相同。
- 每个用例还钉住自己的结果（`wantFailed` / `wantSkipped`）：这一条不是形式主义——
  第一版的「超预算」用例只放了两个请求，两个 ~28 KiB 的分节刚好塞进 61 KiB 预算，
  于是它在比较「两个空跳过记录」，什么也没验证；断言把它抓住了，改成三个请求后
  `batch_skipped_count == 1` 才真正跑在跳过路径上。
- `viewcore` 的批处理测试按本地契约更新；`go build` / `go vet` 干净；
  `./internal/...` 全绿（163 个包），除上一节记录的本机预存在失败。

提交：`1ac9ac51`。本地 `view.go` 现在 **702 行**，`executeBatch` 只剩 30 行适配器。

## 下一轮：view 只剩三块

1. **路径拼写治愈**（原清单第 6 项）：`healSpellingPath` 要真的读成功，不只是
   `PathHinter` 那样解释失败。宿主提供候选（拼写枚举依赖文件系统枚举），core 决定
   「唯一候选就采用、多个候选就报错」的策略与文案。注意它必须发生在 `os.Stat`
   失败之后、真正读取之前，和本地同序。
2. **符号解析**（第 5 项）：`CodeIndexResolver` + `resolveViewSymbol` +
   `annotateViewSymbolNote`。工具能力进 core，注入的索引实现留宿主；remote 侧已经
   用 `FilterSchemaParams(ParamSymbol)` 把参数摘掉了，本地翻过去之后这条路径才真正
   一分为二。
3. **宿主渲染器 + 翻转 `Execute`**（第 4 项）：图片直通（含
   `SetImageAttachmentTokenLimit` 的 token 闸门）、文档/notebook 渲染与
   `recordDerivedRender`、`annotateIgnoredBatchWindow`。做完这些，本地就不再有
   `executeSingle`，`ViewFileRequest` 上那三个 `*Defer` 字段和
   `viewMetadataInt` 也一起消失。

顺序建议：1（纯工具能力，形最小）→ 2 → 3（翻转）。第 3 块之前要先确认一件事：
文档/notebook 渲染依赖宿主的转换器（`docread`、`ipynb`），按「工具能力进 core、
宿主能力留宿主」的划分，渲染本身是纯函数（输入路径与 head，输出 markdown），
因此可以整体进 core，真正留宿主的只有「附件投递」和「台账写入」。## view 第四段已收敛（2026-10-25）：拼写治愈

原清单第 6 项。本地会治愈「只在大小写/不可见 Unicode 码点上不同」的文件名，remote
完全没有这层——请求一个磁盘上真实存在、但拼写不同的路径，存在性检查会永远失败，
模型只能反复重试死路径（analysis §3.7）。

### 搬运

- `viewcore.HealSpelling(checker, absPath)`：唯一候选被采用并**真的读成功**；
  多个候选走「选一个」的失败形状。**每个候选都要过一遍与原始请求同一道
  `toolcore.PermissionChecker`**——治愈不能变成越界逃逸。
- `viewcore.AmbiguousSpellingResult`：带 `path_candidates` 与 pick-one next action
  的失败结果。
- 治愈成功后发布 `path_auto_healed` / `original_path` / `resolved_path`，并追加
  「按磁盘实际拼写解析为 X，后续请用解析后的路径」的说明。
- 折叠规则本来就在 `internal/pathrepair`（`toolkit/tools/path_spelling.go` 只是个
  三行包装），删掉包装，它的测试改为直接调 `pathrepair`。

本地 `view.go` 改用同一个 `HealSpelling`，`healSpellingPath` 删除。

### 分层说明（重要，避免误解为「还有第二套实现」）

`internal/toolexec/preflight.go` 已经在**派发之前**为本地所有工具做过一次同类修复
（`decision.Preflight = "path_auto_heal"`，重写 args 后继续执行）。也就是说本地面的
工具级治愈平时是被它抢先的。工具级治愈仍然必须存在且必须进 core，因为 remote/MCP/
直接调用 handler 的路径上没有 preflight——remote 之前根本无法从这种拼写差异里恢复。

「preflight 的重写」与「handler 的 stat 失败后治愈」要不要合并成一层，属于更上层的
问题（它影响所有文件工具，不只 view），本方案不改，但记录在此以免下次重复发现。

### 证据

- 对拍测试再增两个用例（`spelling healed` / `spelling ambiguous`），并给单文件表加上
  **成功/失败一致性**断言；现在共 **13 个用例**，全部 content 逐字节相同 + 元数据相同
  （含 `file_path` / `path_auto_healed` / `original_path` / `resolved_path` 这些路径
  身份字段——治愈后模型需要照着解析结果继续，两侧必须一致）。
- 本地 `view_spelling_heal_test`、`path_spelling_test` 通过。
- `go build` / `go vet` 干净；`./internal/...` 全绿，除上一节记录的本机预存在失败。

**一处诚实记录**：本轮全量扫描中有一次 `internal/events` 的
`TestMainAgentEventFamilyIsNotEmittedAsBareLiteral` 失败（那是全仓 `.go` AST 门禁，
耗时 35s），随后单独跑（11s）与下一次全量扫描都通过。按证据判断是**该门禁在高并发
负载下的偶发失败**，与本轮改动无关；既不是本提交引入，也没有被本提交修掉。

提交：`00adbcb3`。

## 下一轮：view 只剩两块

1. **符号解析**（原清单第 5 项）：`CodeIndexResolver` + `resolveViewSymbol` +
   `annotateViewSymbolNote`（`view --symbol`）。工具能力进 core，注入的索引实现留宿主；
   remote 侧已用 `FilterSchemaParams(ParamSymbol)` 把参数摘掉，本地翻过去之后这条
   路径才真正一分为二。
2. **宿主渲染器 + 翻转 `Execute`**（第 4 项）：图片直通（含
   `SetImageAttachmentTokenLimit` 的 token 闸门）、文档/notebook 渲染与
   `recordDerivedRender`、`annotateIgnoredBatchWindow`。做完这些，本地不再有
   `executeSingle`，`ViewFileRequest` 上那三个 `*Defer` 字段与 `viewMetadataInt`
   一起消失，`Execute` 翻到 core handler，view 收敛完成。## view 第五段已收敛（2026-10-25）：符号解析

原清单第 5 项。本地用注入的代码索引解析 `symbol`；core handler 之前**完全忽略**这个
参数——remote 的 schema 声明了它然后再用 `FilterSchemaParams(ParamSymbol)` 摘掉，而
真有人传进来时，只是静默按 file_path 读，连一句说明都没有。

### 搬运

- `toolcore.SymbolTarget` + `toolcore.SymbolResolver`：面把符号名解析成一个普通的
  「路径 + offset/limit」，于是符号请求复用同一个读取器，而不是变成第二条读取路径。
  **索引本身、它的 shadow 档规则、file-id→路径 的映射全部留在宿主。**
- **策略归 core**：符号优先于同一次调用里的 file_path 窗口；解析不了但给了 file_path
  就降级读取并在正文里附上说明（`SymbolFallbackNote`）；解析不了又没有可读窗口就按
  同一句话失败（`SymbolFailureMessage`）。三处文案都在 `viewcore/symbol.go`。
- 本地 runtime 用已有的 `resolveViewSymbol` 实现这个 seam——**解析逻辑本身只有一份**，
  两条路径共用。

### 顺带修掉的一个门禁盲点（这是真收获，不是为了让测试变绿）

`internal/remotetools` 的 schema 门禁双向断言「schema 声明的参数必须被 handler 读到」
与「handler 读的参数必须在 schema 里声明」。handler 现在读 `symbol`，而 remote 的
schema **故意不声明**它（没有索引，声明的代价是「收了再拒绝」）。门禁没有
「按能力裁掉的参数」这个概念，于是它红在了正确的地方。

处理方式不是放宽门禁，而是让 `schemaAudit` 学会 `filtered`：**「canonical schema 声明、
本面用 `FilterSchemaParams` 摘掉」**，并要求写明理由。这样意图被固定下来，而不是被
静默豁免。

### 证据

- 新增 `TestViewLocalAndCoreResolveTheSameSymbol`：**6 个用例**（解析命中、命中并带
  limit、命中且同时给了 file_path、未命中但有回退、未命中且无窗口、未命中且只有
  files）走同一份代码索引分别跑两条路径，比较正文与 10 个元数据字段，并断言命中时
  读到的**确实是符号的行区间**（第 5-7 行，不是第 1 行）。
- 本地原有的 view --symbol 套件（`code_tools_test.go`）原样通过。
- `go build` / `go vet` 干净；`./internal/{toolcore,toolkit,remotetools,tools,agent,toolexec}`
  全绿。

**一处流程失误（已修正）**：我对 `internal/remotetools/` 整个目录跑了 `gofmt -l -w`，
它顺手重排了该目录里 7 个**本来就未格式化**的文件（`audit.go`、`metrics.go`、
`platform.go`、`roots.go`、几个 `_test.go`）。这些都不是本次改动范围，已用
`git checkout --` 全部还原，提交里只包含我真正改过的文件。教训：`gofmt -w` 只列
具体文件，不要给目录。

提交：`684290f5`。

## 下一轮：最后一块（宿主渲染器 + 翻转 `Execute`）

只剩这一块，做完 view 就收敛完成：

1. **图片直通**：`viewImageResult` + `sniffImageFormat` + `SetImageAttachmentTokenLimit`
   的 token 闸门。附件投递是「元数据驱动」的（`image_passthrough` / `image_paths` 等
   键），所以渲染判定可以进 core，真正留在宿主的是「把图片写进内容寻址目录」和
   「下一轮把附件塞进 provider 请求」。
2. **文档 / notebook 渲染**：`viewDocumentResult` / `viewNotebookResult` +
   `recordDerivedRender`（派生渲染也要走同一套台账/去重，且只在真的交付后才提交）。
   渲染本身是纯函数（输入绝对路径与 head，输出 markdown），可以整体进 core；
   `docread` / `ipynb` 是本地进程，属于宿主能力，用注入实现。
3. **`annotateIgnoredBatchWindow`**：files-only 调用带了顶层 offset/limit 时明说
   它们没生效。
4. **翻转 `Execute`**：本地 `Execute` 变成「归一化参数 → 组装 core Call → 调 core
   handler → 转回 toolkit 结果」，`executeSingle` 删除，`ViewFileRequest` 上
   `dedupDefer` / `dedupRecordDefer` / `ledgerDefer` 三个字段与 `viewMetadataInt`
   一起消失；`annotateViewSymbolNote` 与 `annotateIgnoredBatchWindow` 的文字进 core。

翻完之后，对拍测试可以进一步扩到「图片 / 文档 / notebook / files-only 带顶层窗口」
这几类，届时两侧应该是同一段代码，测试的意义从「对拍」变成「回归锁定」。## view 已完成收敛（2026-10-25）：本地 `Execute` 翻到共享 handler

最后一块做完，view 不再有自己的读取路径。

### 翻转后的形态

`Execute` 现在只做两件事：把参数归一化（模型常把数字写成字符串），以及套上本地渲染层
的契约标记（`skip_render_truncation` + 声明窗口，按结果上的 `batch` 标记选 32 KiB /
64 KiB）。读取、窗口、批处理、特殊路径拒绝、拼写治愈、符号解析、派生渲染的记账全部
走共享 handler。

**删掉的东西**：`executeSingle`、`executeBatch`、`recordDerivedRender`、
`annotateIgnoredBatchWindow`、`annotateViewSymbolNote`、`viewCoreResult` 之外的本地结果
构造、以及 `ViewFileRequest` 上 `dedupDefer` / `dedupRecordDefer` / `ledgerDefer` 三个
字段。「分节真的交付了才提交」这个顺序现在由 core 拥有，本地不再需要回调机制。
`view.go` 从本轮开始时的 **1606 行降到 379 行**，而且它不再读文件。

### 新接口：`toolcore.ContentRenderer` + `RenderRequest`

面回答文本读取器读不了的东西（图片附件、notebook、文档容器），并声明自己是否处理了；
**共享 handler 保留交付窗口的记账**（读台账 + 已交付窗口缓存）。`viewcore` 从渲染结果的
元数据里还原窗口，所以「图片附件不是窗口」（它不声明 `lines_read`）这一条**由数据本身
决定**，而不是让每个渲染器各自声明——附件因此完全不会在台账里留下「这个文件我看过」的
记录，也就不会让后续写操作误判为安全。

本地剩下的全是**能力**而非行为：沙箱与权限、代码索引、三个渲染器、会话读台账与窗口缓存。

### 翻转后本地套件抓出的两个真 bug（都在新代码里）

1. **派生渲染的台账条目凭空造出 `total_lines`**：窗口描述无条件带上了 `TotalLines`，
   于是第二次读取的 stub 会宣称一个第一次窗口从未公布过的行数（`recordViewWindowRead`
   是按「未知就记 0」设计的，stub 也只在 >0 时才公布）。现在 `DeliveredWindow` 只在实际
   扫到文件末尾时才给出行数。这个 bug 从第二段（引入 `ReadObserver`）就存在，只是因为
   当时本地读取还走自己的路径（记 0）而没被触发，翻转后才暴露。
2. **读台账的 source 用了注册名而不是能力名**：`read` 别名调用会被记成 `Source:"read"`，
   后续写入就不再认为它是「刚读过」。测试 `TestReadAliasRecordsViewLedgerSource` 的注释
   说得很清楚：「ledger source 是稳定的能力字符串，永远不是注册的工具名」。现在
   `coreDefinition()` 固定用 `"view"`。

### 证据

- **本地整套 view 测试现在都跑在共享 handler 上**：图片、文档、notebook、去重 stub、
  读台账与别名、批处理上限、compact 扫描、符号区间、拼写治愈、特殊路径拒绝、
  ignored-window 提示——`internal/toolkit/tools` 40 秒全绿。
- 新增 `viewcore/render_test.go`：把派生渲染的记账规则搬到它现在所在的地方，并且双向
  覆盖——完整的渲染窗口「记一次台账 + 记一次缓存」，被行截断或提前停止的只记台账，
  附件两者都不记。
- `./internal/...` 全绿，除 `internal/api/runtimeapi`（下文）。

### 翻转后的行为差异（有意为之，已记录）

1. **错误结果的信封**：本地自己构造的失败结果历来是 `Error` 有文案、`Content` 为空，
   而共享 handler 两处都有（remote 消费方读的就是 `Content`）。翻转后本地的失败结果也
   统一成后者——这是收敛的副作用，方向是「信息更全」。
2. **remote 侧没有渲染器**：remote 的 runtime 不实现 `ContentRenderer`，所以文档/notebook
   在 remote 上仍走文本路径（二进制摘要）。这是**能力差异**（remote 主机没有转换器），
   不是实现分叉；如果将来 remote 有转换器，接上同一个 seam 就自动获得渲染。
3. **「可缓存窗口」的两条规则**：文本框口（停在 limit 也算完整窗口）与派生渲染（必须
   扫到 EOF）在本地从来就不同，本次**保留原样**而没有顺手统一——统一是行为变更，应该在
   单独一轮里带着理由做。差异写在 `viewcore/render.go` 的注释里。
4. **`internal/toolexec/preflight` 的治愈**：见上一节记录，工具级治愈与 preflight 重写
   是否合并成一层属于上层问题，本方案不动。

### 本轮外部失败（与本改动无关）

`./internal/...` 扫描里 `internal/api/runtimeapi` 有两个失败：
`TestSessionMCPAddCreatesWorkspaceFileWhenMissing`（本机 `~/.aicli/mcp.yaml` 导致，空
`HOME` 下通过）与 `TestAgentRegistryReconcilerConvergesMissingAgentSession`
（单独跑 0.07s 通过，高并发负载下的偶发）。两者都与 view 无关。

提交：`34e318ca`。

## 工具层收敛现状总览（2026-10-25）

| 工具 | 本地 | remote | 状态 |
|---|---|---|---|
| `grep` / `glob` / `ls` | 共享 handler | 共享 handler | 已收敛（grep 仍留 legacy 兜底分支，优先级低） |
| `write` / `append_write` | 共享 handler | 共享 handler | 已收敛 |
| `edit` / `apply_patch` | 共享 handler | 共享 handler | 已收敛 |
| `view` | 共享 handler | 共享 handler | **已收敛（本轮）** |
| `artifact_read` | 独立实现 | 共享 handler | **待收敛** |

view 是最后一块「大」的重复实现。剩下的 `artifact_read` 是本地一份、core 一份
（`NewArtifactReadHandler` 只被 remote 用），量级小得多，做法相同：平移 + 翻转 + 对拍。
另外 `grep` 的 legacy 兜底分支（`rg_args` / `pattern_file` 之外的参数组合）仍未收敛或删除。## artifact_read 已收敛（2026-10-25）：本地 `Execute` 翻到共享 handler

最后一个重复实现。本地工具自己读会话 artifact store（scope、哨兵错误、解引用指标、
按 rune 对齐的分页），core handler 走 store 无关的 sink 并渲染另一种页面。

### 这是一次「定契约」而不是「猜契约」

两边的**页面形状**是真实的契约冲突，各自都有测试钉住：

- **本地**：`artifact art_… | tool=shell | total_bytes=N | window=[a,b) | eof=… | next_offset=N | tool_call_id=…` + 空行 + 窗口字节。续读靠头部写明的 `next_offset`。
- **remote**：**纯窗口字节**，续读靠元数据 `suggested_next_offset`；`artifact_page_test.go` 的注释明确写着「响应大小必须等于请求大小，这正是 next_offset 算术可信的原因」，并用 `offset += len(chunk)` 逐页重组。

决定性理由是「以本地为准」之外还有一条：**成功的工具结果到达模型的是正文，remote 的元数据模型根本看不到**。纯字节方案下模型无法知道下一段该从哪里读（它没法自己数字节）；头部方案把 `next_offset` 直接写在正文里。代价已记录进测试：remote 的分页测试现在按页面写明的 offset 走，而不是按自己量出来的长度。

### 搬运

- **core**：`artifact_handler.go` 渲染本地那套头部与元数据
  （`artifact_source_id` / `tool_name` / `total_bytes` / `window_offset` / `window_bytes` /
  `eof` / `has_full_bytes` / `next_offset` / `source_tool_call_id`），窗口起点取自
  `ArtifactChunk.Offset`（sink 对齐过窗口时，页面如实报告真实起点），并接受粘贴的
  指针尾巴（从任意字符串里提取 `art_<32hex>`）。
- `toolcore.ArtifactChunk` 新增 `SourceTool` / `SourceToolCallID`：advisory，让头部能写出
  捕获来源；sink 报不出来就留空，头部写 `unknown` 而不是编一个名字。
- **本地**：`localArtifactSink` 从会话 store 服务共享 handler，并保住本地历来强制执行的全部
  规则——读取范围（本会话或直接父会话）、无归属/越界的 fail-closed 答案、store 损坏的修复
  路线、解引用指标、以及「一页不切开 rune、且至少推进一个 rune」。store 的哨兵错误以
  `toolcore.CodedError` 跨边界，失败类别不会被压成统一的 INVALID_ARGUMENT。
- 本地 `artifact_read.go` 现在 **172 行**：schema、一条「完全没给 id」的 miss 指标、以及委托。

### 新 core 测试抓到的一处分歧

core 原来把**调用方明确给的小 limit 抬到 1024**，而本地是原样尊重（下限只作用于
由预算推出的最大值）。「故意只读 40 字节」现在真的返回 40 字节。

### 证据

- **本地 artifact 套件一行未改、全部通过**——这就是「页面形状没有搬家」的证据；
- **remote artifact 套件按共享形状更新后全绿**，含逐字节重组的分页遍历；
- 新增 `toolcore/artifact_handler_test.go`：用假 sink 钉住页面契约——头部字段、
  EOF 页不给续读承诺、粘贴的指针尾巴能解析、sink 的 `CodedError` 保留类别与恢复路线；
- `go build` / `go vet` 干净；`toolcore`、`toolkit`、`remotetools`、`tools`、`agent`、
  `toolexec`、`artifact` 全绿。

提交：`926ca74b`。

## 工具层收敛现状（2026-10-25，最终）

| 工具 | 本地 | remote | 状态 |
|---|---|---|---|
| `grep` / `glob` / `ls` | 共享 handler | 共享 handler | 已收敛（grep 仍留 legacy 兜底分支，见下） |
| `write` / `append_write` | 共享 handler | 共享 handler | 已收敛 |
| `edit` / `apply_patch` | 共享 handler | 共享 handler | 已收敛 |
| `view` | 共享 handler | 共享 handler | 已收敛 |
| `artifact_read` | 共享 handler | 共享 handler | **已收敛（本轮）** |

**所有文件/搜索/归档工具现在都是「一套实现、两个执行位置」。** 剩下的两处已知遗留：

1. **`grep` 的 legacy 兜底分支**：`rg_args` / `pattern_file` 之外的参数组合仍走本地旧实现。
   要么搬进 `searchcore`，要么删掉、让 schema 只声明真正实现的那部分（按用户约束应搬进 core）。
2. **`internal/toolexec/preflight` 的路径治愈**与工具级治愈是两层（见 view 第四节），
   是否合并属于跨工具的上层问题。

本轮（view + artifact_read）新增的 core 扩展接口一共四个：`SymbolResolver`、
`ContentRenderer`、`ReadObserver`（`ReadWindow`）、`ArtifactOriginProvider` 之外的
`ArtifactChunk.SourceTool*` 字段。它们全部遵循同一条分工：**策略在 core，宿主状态/能力在面**。## 最后一处债务：`grep` 的 legacy 路径（已量化、已登记，待分批搬完）

`artifact_read` 收敛后，`grep` 是**唯一还有两套实现**的工具。这一轮没有再动行为，而是把债务
变成可数的东西——因为「还有多少」之前只存在于一堵 schema 声明和那个静默回退的路由开关里。

### 现在的确切形状

本地 `GrepTool.Execute` 先问 `executeCoreSearch`：调用里**每一个参数**都在
`coreSearchParams`（53 个）里，且不是 `only_matching` + context 这个组合，就跑共享 handler；
否则回退到本地旧实现。

本地 schema 声明 **96 个参数**，其中 **41 个只有 legacy 路径服务**。这一轮新增
`internal/toolkit/tools/grep_legacy_register_test.go` 把它冻成登记表，**双向**卡住：

- 新声明一个 core 还不支持的参数 → 测试红（债务不能悄悄变大）；
- 共享 handler 开始支持某个参数（或路由集合加进去）→ 测试也红（登记表必须跟着改，
  否则两张表会各说各话）。

同一个文件还单独钉住了**唯一一条不是参数的例外**：`only_matching` + context
——searchcore 拒绝这个组合，本地历来接受它，所以在共享 handler 实现这个组合之前，
该调用必须留在 legacy 路径。它是「一套实现」上的一个洞，单独写出来免得被当成参数问题。

### 41 个参数分三组（决定移植顺序）

1. **表现层 / 空操作标记**（约 18 个）：`color`、`heading`/`no_heading`、
   `line_number`/`no_line_number`、`with_filename`/`no_filename`、`null`/`null_data`、
   `pretty`、`text`、`binary`、`line_buffered` 系列、`field_context_separator`、
   `path_separator`、`context_separator`/`no_context_separator`。
   本地旧实现把它们**消费掉并记为 ignored**（`isNoOpRGFlag` / `consumeNoOpRGFlag`），
   因为工具自己的输出形状已经满足它们（永远是 `path:line:content`）。移植成本最低：
   共享 handler 只需接受并如实报告为 ignored（元数据 `ignored_rg_args`）。
2. **rg 引擎/输出语义标记**（约 18 个）：`pcre2`、`engine`、`auto_hybrid_regex`、
   `multiline`、`multiline_dotall`、`replace`、`json`、`json_output`、`follow`、`crlf`、
   `sort`/`sortr`/`sort_reverse`/`sort_files`/`no_sort_files`、`head_limit`、`no_config`、
   `type_add`/`type_clear`。
   这些需要 runtime 已经暴露的 ripgrep 通道（`searchcore.RipgrepStream`）以及各自的输出语义与
   测试，逐个搬。
3. **逃生口**：`rg_args`、`passthru`。它们的语义就是「镜像 rg 自己的解析器」，本地那套
   解析器（`parseRGCompatArgs` 及其 5 个 helper）是唯一实现，应当整体平移进 core，
   而不是重写。

顺序：**1 → 3 → 2**。第 1 组是纯接受/忽略，风险最低；第 3 组是整段平移（和
`edit`/`apply_patch` 同一种做法）；第 2 组每个标记都要自己的语义与测试，所以放最后。

提交：`5d353b4e`（仅登记，无行为变更）。

## 现状（2026-10-25）

| 工具 | 状态 |
|---|---|
| `write` / `append_write` / `edit` / `apply_patch` / `view` / `artifact_read` / `glob` / `ls` | 一套实现、两个执行位置 |
| `grep` | **核心参数集（53 个）已收敛**；41 个参数 + `only_matching`+context 组合仍在本地旧实现上，已登记、已排序 |

`grep` 的债清完之后，「工具层一套实现」这件事就没有已知例外了；剩下的只有
`toolexec/preflight` 的路径治愈与工具级治愈是否合并这一条**跨工具的分层问题**
（见 view 第四节），它不影响「同一个工具只有一套实现」。## grep legacy 第一批（第 1 组）的作业单

分析已完成，下一轮可以直接照单执行。

### 要搬的 18 个参数

它们全是「接受并忽略」，因为工具自己稳定的输出骨架已经满足它们：

```
line_number, no_line_number, with_filename, no_filename,
heading, no_heading, pretty, text, binary,
line_buffered, no_line_buffered, block_buffered, no_block_buffered,
null, null_data,
field_context_separator（字符串）, path_separator（字符串）, color（字符串）
```

本地把这件事集中在 `grep.go:3121 collectIgnoredPresentationParams(params)`：布尔为真时记 `name`，
字符串非空时记 `name=value`，去重后放进 `opts.ignoredPresentation`，最终以元数据键
**`ignored_presentation`**（`grep.go:6160`）出现在结果里。注意它是**无条件发布**的（没有忽略项时是空列表）。

### 三个（其实是四个）改动点

1. **`searchcore`**：`GrepOptions` 加 `IgnoredPresentation []string`；参数解析处调用一个
   **原样平移**的 `collectIgnoredPresentationParams`（与本地同一份拼写规则）；
   结果元数据加 `ignored_presentation`。平移而不是重写——它是一张拼写表，重写必然漏项。
2. **remote 的 grep schema**（`remotetools/registry.go`）：补上这 18 个属性的声明，否则
   `tool_schema_audit_test` 的「handler 读了但 schema 没声明」会红（这些不是按能力裁掉的参数，
   不能用 `filtered` 豁免——remote 完全能兑现它们，因为它们是空操作）。
3. **本地路由集合**（`grep_core_switch.go` 的 `coreSearchParams`）：加这 18 个键。
4. **登记表**（`grep_legacy_register_test.go` 的 `grepLegacyOnlyParams`）：删掉这 18 项。
   `TestGrepCoreSwitchCoversEveryRegisteredParam` 会在两处不同步时直接报出参数名。

验收：`TestGrepLegacyOnlyParamsAreRegistered` 自动核对新集合；`searchcore` 新增一个测试，
断言「bool 真值记 `name`、空字符串不记、`color=never` 记 `color=never`、无忽略项时发布空列表」。

### 顺带记下另一条同轴债务：remote 的 grep schema 是本地 schema 的平行副本

remote 的 grep 参数（`remotetools/registry.go:176` 起约 400 行）与本地 `grep.go` 的声明是
**两份手写文本**，只是被 `tool_schema_audit_test` 从行为上对齐。这与「一套代码」是同一个病，
只是发生在 schema 轴：view 已经用 `viewcore.Schema()` 做到了「一份声明」，grep 还没有。

等 41 个参数搬完（第 1、3、2 组都结束），这 400 行副本自然会消失——那时本地与 remote 应当
像 view 一样共享**同一份** grep schema（`searchcore.GrepParameters()`），而不是各写一份。
在那之前不值得先动它：参数集正在变，先抽公共声明会把搬运动作放大一倍。

提交：`5d353b4e`（登记）、`5b5ead65`（文档）。## grep legacy 第 1 组已完成（2026-10-25）：表现层空操作标记

按上一节的作业单执行完毕。**legacy-only 参数从 41 变成 23**，而且这个数字现在是测试算出来的
（登记表 + 路由集合双向核对），不是手数的。

### 搬了什么

- **`searchcore`** 读取这 18 个拼写，并**无条件**发布 `metadata.ignored_presentation`——
  空列表本身就是一句话：「你要的输出形状本来就是现在的形状」。
- **`collectIgnoredPresentationParams` 是原样平移**，不是重写。两个细节重写必然会漏：
  `""`/`''` 占位符表示「没传」，以及只有布尔**真值**才算请求。
- 它还刻意**逐条字面读取**（`args["line_number"]` …）而不是走一张键表：schema 审计走的是
  真实的参数读取，用豁免表把读不到的键登记成「间接读取」恰好会盖住它要抓的东西。
- **remote schema** 声明这 18 个（它完全能兑现——它们本来就是空操作），审计的扫描清单加上
  这个新读取函数。
- 本地路由集合加 18 个键；登记表删 18 项。

### 证据

- `searchcore` 新增测试双向钉住契约：没设任何标记时是空列表；设了一整族时**逐项、按既有顺序**
  都在；并且**设与不设标记的页面逐字节相同**（这就是「接受但不改变输出」的证据）。
- 登记测试确认路由集合与登记表一致（`TestGrepCoreSwitchCoversEveryRegisteredParam` 抓到了我
  第一次漏删的 3 个键：`block_buffered` / `no_block_buffered` / `pretty`）。
- remote schema 审计两个方向重新全绿（第一版 `indirect` 豁免被我自己否掉了，改成字面读取）。
- `go build` / `go vet` 干净；`toolcore`、`toolkit`、`remotetools`、`tools`、`agent`、
  `toolexec`、`toolbroker` 全绿。

提交：`601b30ab`。

### 剩下的 23 个 + 1 个组合

下一个批次是**第 3 组（逃生口）**：`rg_args` + `passthru`。它们的语义就是镜像 rg 自己的解析器，
本地那套（`parseRGCompatArgs` + `consumeNoOpRGFlag` + `applyRGBooleanFlag` + `parseRGFlagWithValue`
+ `applyRGFlagValue` + `parseRGShortFlagCluster`，约 600 行）是唯一实现，应当**整段平移**进
`searchcore`——和 `edit`/`apply_patch` 一样，不重写。

然后是第 2 组（19 个 rg 引擎/输出语义标记：`pcre2`、`engine`、`auto_hybrid_regex`、`multiline`、
`multiline_dotall`、`replace`、`json`、`json_output`、`follow`、`crlf`、`sort*`、`head_limit`、
`no_config`、`type_add`、`type_clear`、`context_separator`、`no_context_separator`），
以及 `only_matching` + context 那个组合。这些需要 ripgrep 通道与各自的输出语义，逐个搬。## grep legacy 第 2 批（引擎选择）已完成（2026-10-25）

`pcre2`、`engine`、`auto_hybrid_regex`、`multiline`、`multiline_dotall` 五个参数进共享 handler。
**legacy-only 从 23 降到 18。**

### 顺带修掉一个真缺陷

**「显式要求 ripgrep 的模式先被 Go regexp 拒了」**：core 原先的顺序是「先跑 walker，再让引擎覆盖」
——而 `pcre2: true` + lookaround 模式（`foo(?!bar)`，PCRE 合法、Go regexp 非法）会在 walker 编译处
直接失败，引擎根本轮不到。本地一直是对的（要求 rg 时先交给 rg，否则报错），并在注释里写明了原因。
现在 core 也是：`RequiresRipgrep()` 为真时**先问引擎**；引擎跑不了（主机没 rg）才像本地一样退回 walker。

顺带把「要不要 rg」变成**派生**而不是并列字段：`RequiresRipgrep()` 由五个标记算出来，
`engine=default`/留空明确不算（这也和本地的 `requiresRipgrepForEngine` 一致），避免两个真相源。
`requires_ripgrep` 元数据由 handler 发布——因为它就是做决定的那一方。

### 一次必要的词汇收敛（有先例可依，不是我拍的）

改路由后立刻暴露：同一个 `engine` 元数据，本地旧路径说 `builtin`/`rg`，core 说
`walker`/`ripgrep`。两边都有测试钉住。判断依据是**已经收敛的兄弟工具**：本地 `glob`
（早就跑在共享 handler 上）测试里断言的就是 `walker`/`ripgrep`。
也就是说本地面自己就不一致，而收敛后的那个工具用的是共享词汇——所以退役的是 grep 旧路径的
`builtin`/`rg`，三处本地断言随之更新，每处都写了理由。

### 证据

新增 searchcore 套件用假 searcher 驱动，钉住三件事：① 引擎被先问、且收到了 flags；
② **Go regexp 拒绝的模式在引擎可用时仍被回答**（就是上面那个缺陷）；③ 没有 ripgrep 的主机
退回 walker。`RequiresRipgrep()` 另有表驱动测试，包含「`engine=default` 不算要求 rg」。

`go build` / `go vet` 干净；`toolcore`、`toolkit`、`remotetools`、`tools`、`agent`、`toolexec` 全绿。
提交：`a9e7593f`。

### 剩下的 18 个 + 1 个组合

`rg_args`、`passthru`、`replace`、`json`、`json_output`、`follow`、`crlf`、`no_config`、
`sort`/`sortr`/`sort_reverse`/`sort_files`/`no_sort_files`、`head_limit`、
`context_separator`/`no_context_separator`、`type_add`/`type_clear`，以及
`only_matching` + context。

**排序修正**：之前写「1 → 3 → 2」，把 `rg_args` 排在第二。这一轮证明那是错的——
`rg_args` 把 raw 参数解析成**选项字段**（`-P` → PCRE2、`-U` → multiline、`--engine` → engine…），
而那些字段现在才刚有 5 个；`-r/--replace`、`--json`、`-L/--follow` 的目标字段还不存在。
所以正确顺序是：**先把第 2 组剩下的语义字段补齐，最后再平移 `rg_args` 解析器**（约 600 行，
整段平移）。剩下的 18 个里，`no_config` 值得单独记一笔：共享引擎的 base args 里**本来就有**
`--no-config`，所以 `no_config: true` 在共享路径上已经是既有行为，但 `false`（要求读配置文件）
在共享路径上无法兑现——这类「本地能、共享不能」的语义差异必须在搬之前定清楚，不能顺手当空操作。## grep legacy 第 3 批（crlf）已完成（2026-10-25）

**legacy-only：41 → 23 → 18 → 17。**

### 这一批的推理比代码重要

`crlf` 要求 ripgrep 把 CRLF 当成行结束符。**内置 walker 本来就是这么做的**（它的读取器会去掉 CR），
所以这个开关的存在意义是「让两个引擎一致」：不传时 ripgrep 会把 CR 留在行内容里，同一个文件在
两个引擎下渲染结果不同。因此它属于 `RequiresRipgrep`（与本地旧实现的判定一致），引擎侧追加 `--crlf`。

### 第 2 组剩下的**不能**这样成批搬（分头记下，免得下次重新发现）

| 参数 | 为什么不能当「加个 flag」处理 |
|---|---|
| `sort` / `sortr` / `sort_files` / `no_sort_files` | 与**契约**冲突而不是与 flag 冲突：引擎强制 `--sort path` 且**每个 root 起一个进程**，才让分页与 walker 的逐 root 遍历完全一致。用户选的排序键是跨 root 的全局序，兑现它就得放弃「确定性分页」或「每 root 一进程」二者之一。 |
| `json` / `json_output` | 是**输出形状模式**（透传 rg 的原始事件流），不是又一个开关。 |
| `context_separator` / `no_context_separator` | 改变上下文分组的**渲染**方式。 |
| `follow` | 需要 walker 真的跟随符号链接目录，**并且**带上本地那套「Windows junction 默认跟随」的宿主策略。 |
| `no_config` | **不对称**：`true` 已经是引擎的既有行为（base args 里就有 `--no-config`），而 `false` 要求读配置文件，正是共享引擎刻意不做的事。搬它需要先决定「确定性」怎么办，不是映射一个 flag。 |
| `replace` / `head_limit` / `type_add` / `type_clear` | 各自带语义（替换输出、页上限、类型表增删），需要逐个设计。 |
| `only_matching` + context | core 自己的拒绝文案写着「not combined by the shared core yet」——这是最后一个非参数形态的洞，需要两个引擎 + 渲染器一起改。 |

顺序建议：`only_matching`+context（一个行为，跨引擎+渲染器）→ `replace` / `json`（引擎输出模式）
→ `sort*`（要先定分页契约）→ `follow`（先定宿主策略）→ `no_config`（先定确定性）→
`type_add`/`type_clear` / `head_limit` / `context_separator*` → 最后整段平移 `rg_args` 解析器。

### 工作区状态提醒

本轮提交时发现工作区里出现了**不是本方案产生的改动**（`cmd/gateway/*`、`internal/notify/*`、
新增 `apps/`、`docs/gateway-app/`）——有另一个执行者正在同一个工作区上工作。本次提交只包含本方案
的 7 个文件（显式列路径，没有用 `git add -A`）；那些改动留在工作区未动。

提交：`3615a97c`。## grep 的最后一个非参数洞：`only_matching` + context 已收敛（2026-10-25）

提交 `caed6a36`。**「一套实现」在 grep 上不再有行为形态的洞**——剩下的全是参数。

### 为什么它是个「洞」而不是参数问题

共享 handler 原先直接拒绝这个组合（文案是「not combined by the shared core yet」），
于是本地把**这一个组合**路由到 legacy 路径。也就是说：同一个调用，两个面给出不同答案。

### 为什么修起来比预想的小

context 的实现方式本来就选对了：`ExpandGrepContext` **通过共享行读取器重读被匹配的文件**
来生成上下文行（特意不让 ripgrep 传 `--context`，这样两个引擎不可能渲染出不同窗口）。
所以冲突点只有一处——**被匹配的那一行**：它原先从文件重读出来按整行渲染，而
`only_matching` 要求那一行显示**片段**。这正是 `rg -o -C` 的行为。

改动：
- `ExpandGrepContext` 收 `onlyMatching`：命中行按**每个片段一条**输出（并且要**吃光同一行的所有匹配**，
  否则游标会落后），窗口内其余行仍整行输出。片段文本与列号来自引擎已经产出的 match，
  所以两个引擎天然一致。
- `startsGrepContextGroup` 把**同一行的两条**视为同一个块：否则同一行的第二个片段会被标成新分组，
  渲染时会在同一行的两半之间打印 `--` 分隔符——凭空造出一个并不存在的间隔。
- 删掉 handler 的拒绝分支与本地路由里的那条分支；原先「钉住 legacy 路由」的登记测试，
  现在变成「钉住共享路径」的测试。

### 证据

- 纯函数级：逐条钉住 `ExpandGrepContext` 的输出（命中行是片段、周围是整行、`GroupStart` 只在第一条为真）；
- handler 级：断言渲染出的 `path>line: 片段` / `path line: 整行` 两种形状；
- remote MCP 对拍测试：原先断言「必须失败」，现在断言「两种输出单位同页共存」；
- `go build` / `go vet` 干净；`toolcore`、`toolkit`、`remotetools`、`tools`、`agent`、`toolexec` 全绿。

### 剩下的（全部是参数，17 个）

`rg_args`、`passthru`、`replace`、`json`、`json_output`、`follow`、`no_config`、
`sort`/`sortr`/`sort_reverse`/`sort_files`/`no_sort_files`、`head_limit`、
`context_separator`/`no_context_separator`、`type_add`/`type_clear`。
按上一节的分头结论逐个来：`replace` / `json`（引擎输出模式）→ `sort*`（先定分页契约）→
`follow`（先定宿主策略）→ `no_config`（先定确定性）→ 其余 → 最后整段平移 `rg_args` 解析器。

**本轮工作区又一次出现非本方案的改动**（`cmd/gateway/*`、`internal/notify/*`、`docs/gateway/*`、
`docs/README.md`、新增 `apps/`、`docs/gateway-app/`）。两次提交都只显式列了本方案的文件；
另外 `gofmt -l -w <目录>` 又一次顺手重排了 `internal/remotetools/` 里 7 个**本来就未格式化**的文件
（`audit.go`、`metrics.go`、`platform.go`、`roots.go`、几个 `_test.go`），已用 `git checkout --` 全部还原。
教训重复了一次：**`gofmt -w` 只列具体文件，永远不要给目录**。## 关于剩下 17 个参数的**实测发现**：它们都撞在同一堵墙上

提交 `b59d9bbb`（本次先落一个「把结构约束钉住」的测试，下面说为什么）。

### 实测（`rg` 14.1.0 就在本机，所以这次不是推测）

```
$ rg --json foo a.txt
{"type":"match","data":{...,"lines":{"text":"foo bar foo\n"},"submatches":[{"match":{"text":"foo"},...}]}}

$ rg --json -r BAR foo a.txt      # ← 完全一样
{"type":"match","data":{...,"lines":{"text":"foo bar foo\n"},"submatches":[{"match":{"text":"foo"},...}]}}
```

**`-r` 对 `--json` 输出没有任何影响**。而内置年仅的实现默认走**纯文本**输出（`--json` 只在
`json: true` 时才加），所以 `replace` 在那里能用；共享引擎**始终**用 `--json` 解析，所以在 core 里
`-r` 等于无效。

### 三次撞墙，同一堵墙

我们连续三次发现「这个参数搬不动」，而原因每次都一样——**内置旧的 rg 使用模型与共享引擎的模型
在结构上不同**：

| 参数 | 与哪条结构冲突 |
|---|---|
| `sort*` | 引擎**每个 root 起一个进程**且强制 `--sort path`（为了让分页与 walker 的逐 root 遍历一致） |
| `replace` | 引擎**始终** `--json`（为了拿结构化事件），而 `-r` 在 json 模式下不可见 |
| `json` / `json_output` | 反过来：它要的就是**单进程原始事件流透传**，与「每 root 一进程 + 解析后渲染」冲突 |
| `no_config` | 引擎**始终** `--no-config`（确定性），而 `false` 要求读配置 |

这四条不是懒，是**结构**：共享引擎为了「两个引擎渲染同一页 + 分页确定」才这么设计的，而这些
flag 想改的恰恰是那两条保证。

### 因此我把结构约束钉成了测试（`b59d9bbb`）

- `ripgrepBaseArgs()` = `--no-config --threads 1 --sort path`，逐字节断言；
- **每个 root 一个进程**，用一个记录调用的注入 stream 断言；
- 顺带补上一个**真实的覆盖缺口**：此前引擎选择类 flag 只测到「searcher 收到了选项」，
  从没测到「ripgrep 真的被要求了」——现在直接断言 argv（`--pcre2` / `--engine pcre2` /
  `--auto-hybrid-regex` / `--multiline` / `--multiline-dotall` / `--crlf`），
  以及引擎仍然在要 `--json`（它结构化契约的基础）。

有了这些断言，任何「为了搬某个 flag 而改动引擎模型」的尝试都会立刻红，并指出它要放弃的是哪条保证。

### 剩下的选择（需要你定）

17 个参数现在只有两条路，**没有第三条**：

**A．按「一个实现只能声明它能兑现的东西」把它们从本地 schema 撤下**（保留已经收敛的 77 个参数）。
- 优点：与用户既定原则一致（「如果远程不支持，不是应该不声明这个参数吗」）；端口面立刻变成
  「所有工具一套实现、一套 schema」；这 17 个拼写本来就是「给熟悉 rg 的模型用的兼容糖」，
  真正被依赖的（`type`/`glob`/`include`/`context`/`files_with_matches`/`stats`/`column`/
  `only_matching`/`max_count`/`max_depth`/`max_filesize`/`ignore_file*`/`hidden`/`no_ignore*`）
  都已经在共享实现里。
- 代价：本地模型不能再写 `rg_args: ["-P"]` 这类调用（会得到「参数未知」）；`pcre2` 有等价替代
  （已收敛的 `pcre2` / `engine` / `multiline*` 在共享路径上可用 ✓ 所以这部分不受影响）。

**B．为它们各建一条「旁路」**（例如给引擎加一个纯文本输出模式、一个单进程原始透传模式）。
- 代价：正是我们花了一整轮移除的那种「第二条路径」；而且要放弃或参数化「每 root 一进程」/
  「始终 --json」这两条现有保证，需要重做它们的测试。

**我建议 A**，理由：这 17 个里真正有语义价值的（`pcre2`/`engine`/`multiline*`/`crlf`）**已经搬完了**；
剩下的大多是纯 CLI 外观（buffering 已按空操作搬完，`sort*`/`json*`/`replace`/`head_limit`/
`context_separator*`/`type_add`/`type_clear`/`no_config`/`follow`/`passthru`/`rg_args`）。
而 `rg_args`/`passthru` 本质是「我要用 rg 的原生参数」——那与「一套实现、两个执行位置」在概念上
就是冲突的：**它们要求的是「在本地跑 rg 的另一个前端」**。

如果选 A，剩下 17 个参数从本地 schema 删除、登记表清零、文档收尾，这轮收敛就结束了；
如果选 B，我会先针对 `sort*` 或 `json*` 中的**一个**做设计稿（说清要放弃哪条保证、测试怎么改），
再动手。## 更正：不是「搬不动就删」，是「核心没实现就补」（2026-10-25）

上面那节 A/B 选择写错了框架，用户否掉了它。历史保留只为对照：

**错误的框架**：把 17 个参数当成「本地多余的能力」，于是把「删本地 schema」当成选项 A。
**正确的框架**：核心是共享的那一份实现；核心缺能力 = 实现缺陷，要补。补完核心，
本地与 remote **同时**获得同一功能——不是为 remote 牺牲本地，因为两边跑的是同一份代码。

- remote 不是「另一套功能」，只是同一套功能运行在另一个环境；
- 「远程不支持就不声明参数」指的是 **Runtime 能力**（例如远程机器上没有 rg 二进制），
  不是「共享引擎自己做不到」；共享引擎做不到只能补实现。

据此重新排队：`replace` / `json*`（引擎输出模式）→ `sort*` → `follow` → 其余 →
最后整段平移 `rg_args` 解析器。

## `no_config` 已收敛（三态）（2026-10-25）

`no_config` 是引擎模型洞的一个，已按「核心必须实现」落地：

- `GrepOptions.NoConfig` + `NoConfigSet`：新增 `argPresent` 让布尔拥有第三态「未设置」；
- `ripgrepSearchArgs()`：未设置 → 仍传 `--no-config`（确定性默认，跨机器同一答案，这是共享引擎的既有保证）；
  `false` → **不传**（调用方明确要求继承本机 rg 配置）；`true` → 传；
- metadata 发布**生效值**而不是原请求：未设置时发布 `true`，因为引擎确实没让 rg 读配置；
- 本地 schema 与 remote schema 用**同一段描述**声明三态语义；
- `coreSearchParams` 收录 `no_config`，登记表 41 → 16。

一个必须说清的点：本地旧实现在未设置时**不传** `--no-config`，而共享引擎未设置时传。
这不是「新实现偷偷改语义」，而是两条路径的默认值本来就不同（旧实现是「除非显式要求否则不传」，
共享引擎是「除非显式反对否则传」）——收敛后取共享引擎的确定性默认，因为
「同一台机器上的同一调用不该因为 `~/.ripgreprc` 而给出不同答案」。
`no_config=false` 保留了继承能力，所以能力没有丢，只是默认值选定了。

### 证据

- `TestRipgrepNoConfigIsThreeState`：三态直接断 argv，并确认去掉 `--no-config` 时
  `--threads 1` / `--sort path` 仍在；
- `TestGrepNoConfigRequestReachesTheEngineAndTheMetadata`：请求 → 引擎选项 → metadata 生效值
  三个环节全过一遍（「未设置」时若发布原请求，会描述出与实跑相反的事实）；
- 定向构建 + `go vet` 干净；`toolcore/...`、`remotetools`、`toolkit/tools` 整包回归全绿
  （`toolkit/tools` 69s，含登记表两个方向的断言）。## `replace` 已收敛：两个引擎都实现替换（2026-10-25）

### 为什么不能沿用 JSON 模式（实测，rg 14.1.0）

- `rg --json -r X pat` 与 `rg --json pat` 输出**完全相同**；
- `--json` 的每个 match 事件只带整段匹配的 `start/end`，**没有捕获组偏移**；
- 因此共享引擎无法从 JSON 里重算 `$1`/`${name}`——真想重算就是自造第二套替换语义，
  而且 `$1Y` 按「最长名字」解析这类细节必然走偏（rg 与 Go 在这点上恰好一致，但那是巧合不是契约）。

结论：替换必须由**执行替换的一方**完成——这正是本地参考实现的做法（本地把 `--replace` 交给 rg）。

### 落地

- `GrepOptions.Replace`；`mode == content` 时 ripgrep 路径切到「替换输出模式」：
  `--null --line-number --with-filename --color=never --no-heading --replace <tpl>`（不再 `--json`），
  基准 flag（`--no-config --threads 1 --sort path`）与每 root 一进程照旧；
- `--null` 让路径以 NUL 结束，`path\0line[:col]:text` 的切分因此**无歧义**（Windows 路径自带冒号）；
- walker **同样实现**替换（Go `ExpandString` / `ReplaceAllString`），没有 rg 的机器不丢能力——
  这与「核心必须完整实现」一致，也就不再需要「缺 rg 就报错」的分支；
- 列语义对齐：`only_matching` 一律向 rg 要 `--column`（JSON 模式在片段上总是给列号），
  整行匹配只按调用方要求取列；
- 上下文渲染：`ExpandGrepContext` 新增「命中行以引擎文本为准」。带替换时命中行为替换后文本、
  上下文行仍来自文件——与 `rg -r -C` 一致（只有命中行被替换）；
- 顺带把 ripgrep 两种输出模式的文本整形抽成 `normalizeGrepEngineText` / `appendRipgrepMatch`：
  路径解析、glob 过滤、文本整形、收满页即停只写一份。

### 证据

- walker 替换（整行 / 片段）；
- **两引擎一致性**：同一 fixture、同一次调用，用真实 rg 跑 ripgrep 路径，逐字段比较 walker 与
  ripgrep 的行号 / 列号 / 文本（`TestReplaceAgreesBetweenEngines`）——这类测试正是「一套功能、
  两个执行位置」的守门人；
- argv 契约：替换模式必须出现 `--null` / `--replace`，必须**没有** `--json`，基准 flag 必须还在；
- 解析器：NUL 切分、含冒号的 Windows 路径、`line:col:` 前缀、文本自身形如前缀的情形；
- handler 路由 + metadata + 上下文渲染（命中行为替换文本、上下文行为原文，且原文不得残留）；
- remote MCP 对拍（不依赖目标机是否装了 rg）。

登记表：41 → 15。剩余 15 个：`rg_args`、`passthru`、`json`、`json_output`、`follow`、
`sort`/`sortr`/`sort_reverse`/`sort_files`/`no_sort_files`、`head_limit`、
`context_separator`/`no_context_separator`、`type_add`/`type_clear`。## `json` / `json_output` 已收敛（2026-10-25）

### 契约（以本地为准，已用测试钉住）
- 输出 = **ripgrep 原始事件行**（begin/match/context/end/summary），逐字节透传；
- 分页单位是**事件行**（不是匹配）：`limit` 是这一页给多少行事件，`offset` 从第几行起；
- 输出必须始终是可解析的 JSON Lines，因此**不追加任何提示文本**（旧实现同样在 json 下关掉分页提示）；
- metadata：`json`、`normalized_output=false`、`output_format="rg_passthrough"`、`json_output_requested=true`，
  以及 rg 汇总事件里的计数。

### 落地
- `GrepOptions.JSONOutput`；引擎在 json 模式下把**已经解析过的那些事件行原样收集**
  （`GrepResult.JSONEvents` + 每行的绝对路径），收满 `offset+limit+1` 行即停（多收一行作为 has_more 证据），
  并累计汇总计数；
- handler 负责**路径改写**：事件里的 path 换成该面统一的显示路径（单文件=文件名、多根=根标签前缀、
  否则工作区相对/必要时绝对），其余字节一律不动——于是 json 与普通渲染对同一个文件给出同一条路径；
- `json` 进入 `RequiresRipgrep()`：要「rg 的事件流」就得先问引擎；引擎不在时**明确报错**（指名 ripgrep 与 json），
  绝不降级成别的文本。旧实现此时同样是报错，这里只是把措辞换成「缺什么 + 怎么办」。

### 顺带修掉一个真实字段错配
rg 的汇总事件报的是 `searches` / `searches_with_match`，**从来没有 `files_searched` 字段**；
而核心一直按 `files_searched` 读，于是真实 rg 下 `files_scanned` 恒为 0。
现在按 rg 的真实字段读（`searches`→files_searched、`searches_with_match`→files_with_matches），
并沿用本地一直发布的字段名。这是 json 收敛过程中被测试抓出来的既有缺陷，不是新引入的分歧。

### 证据
- searchcore：json 页保持 JSON Lines（无提示）、按行分页、has_more/next_offset 正确、path 改写为显示路径、
  改写器的四类用例（普通相对路径 / 含转义的 Windows 路径 / 已是显示路径 / 无 path 的 summary）、
  引擎侧收集原始行 + argv 仍含 `--json` + 汇总计数、**无 rg 时 fail-closed**（错误指名引擎，
  且绝不把别的东西当 JSON 流返回）；
- remote MCP 对拍：装了 rg 时客户端拿到合法事件流且无提示；没装时跳过（fail-closed 路径在 searchcore 钉住）；
- 本地包此前钉住 legacy 行为的 5 个测试按核心契约更新（错误措辞、stub 改为按 argv 里的 root 区分进程），
  其余断言（metadata 键、stats 字段）**未改测试即通过**——说明核心确实复现了本地契约。

登记表：15 → 13。剩余：`rg_args`、`passthru`、`follow`、`sort`/`sortr`/`sort_reverse`/`sort_files`/`no_sort_files`、
`head_limit`、`context_separator`/`no_context_separator`、`type_add`/`type_clear`。## `context_separator` / `no_context_separator` / `head_limit` 已收敛（2026-10-25）

### 分隔符改由共享渲染器输出
本地把 `--context-separator` 交给 rg；walker 根本没有对应旗标，所以「交给 rg」就等于「两个引擎不一致」。
现在分隔符由**唯一的渲染器**打印（`grepContextSeparator`）：

- 默认 `--`（rg 的默认值）；
- `context_separator` 给字符串 → 用它；空/纯空白视为未设置（与本地一致：那时本地什么都不传给 rg，rg 用默认）；
- `no_context_separator` → 不打印，且**优先于**给定的字符串（本地也是这个优先级）；
- metadata 发布**生效值**（默认 `--` / 自定义串 / 关闭时为空串），而不是原请求。

### 页大小的两个拼写合成一个概念
`head_limit` 是本地拼写、`limit` 是 core/remote 拼写，本质都是「本页给多少条」：

- 同时出现时 `head_limit` 优先（只有它在本地面一直表示页大小）；
- `head_limit: 0` = 不限，解析为安全上限 5000（本地一直如此，不是「回落到默认页」）；
- 该上限对 `limit` 同样生效，因此两个拼写不可能描述两个不同的最大值；
- metadata：`limit` 与 `head_limit` 同为生效页大小，`head_limit_explicit` 说明用的是哪个拼写。

### 证据
- searchcore：默认 / 自定义 / 空白 / 关闭四种分隔符用例（断言分隔符**独占一行**、两个文件都仍在页里、metadata 是生效值）；
- `head_limit` 的页大小、与 `limit` 同时出现时的优先级、`0` 不回落到默认页、两个拼写同受 5000 上限；
- `toolcore/...`、`remotetools`、`toolkit/tools`（29.6s）全绿；本地那条
  `TestGrepTool_ContextSeparatorBuiltin`（无 rg 场景）**未改即通过**——它现在跑的就是共享渲染器。

登记表：13 → 10。剩余：`follow`（需 walker 也实现链接跟随 + 环保护，独立一批）、
`sort`/`sortr`/`sort_reverse`/`sort_files`/`no_sort_files`、`passthru`、`type_add`/`type_clear`、`rg_args`。