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
