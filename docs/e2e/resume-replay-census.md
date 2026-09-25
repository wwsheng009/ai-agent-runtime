# Resume 重放必要性普查（L1.0）

- 计划：`docs/plan/resume-large-session-optimization-plan-20260924.md` §3.2（L1.0 普查）
- 样本：`<RuntimeEventsDir>/runtime-events.jsonl`，91 MB / 61,454 行（2026-09-24，本机真实长会话）
- 判据（计划 §3.2.3）：**只有「既不新增 cell、也不更新任何终态 cell」的类型才可裁**；
  识别不出类型 → 一律不裁（§3.2.4，宁可慢，不可丢）
- 方法：两路独立证据交叉验证
  1. **实测**：逐行字节级统计（`artifacts/.../eventlog_probe.py`）→ 类型 × 行数 × 字节
  2. **权威分类**：读 `backend/cmd/aicli/ui/render/encoding/encoder.go` 的 `classify`（`:767-860`）
     与各 `apply*` 实现，逐类型判定对最终 Scene 的影响

---

## 0. 结论摘要（TL;DR）

| 层级 | 含义 | 行数 | 占行 | 字节 | 占字节 |
|---|---|---|---|---|---|
| **Tier A** | 已证明零影响（`opNone`） | ~4,213 | 6.9% | ~1.93 MB | 2.1% |
| **Tier B** | 可裁，但需「配对/回落」证明或守卫 | ~27,997 | 45.5% | ~35.63 MB | 39.1% |
| **Tier C** | **不可裁** | ~29,244 | 47.6% | ~53.23 MB | 58.4% |

**对 L1.1 的直接含义**：免解析上限 ≈ **52% 行**（A+B），
`eventlog_parse` 预期 3.0s → ~1.4–1.6s，**达不到**计划 §3.3 写的「~59k 行免解析 / ~0.3s」。
计划 §3.3 的乐观估计隐含了「reasoning / tool.progress / llm.request.* 都不参与转录」这一**推断**，
本次普查证明该推断对 `tool.progress`（部分）与 `llm.request.finished`（完全不成立）都不准确。

**为什么 Tier A 单独没有价值**：只有 ~6.9% 行。想拿到 parse 的大头必须裁 Tier B，
而 Tier B 的正确性依赖两个必须显式处理的机制（见 §3 的坑 2/3）。

**最重要的单条发现**：`llm.request.finished`（13.2% 字节 / 2,561 行）**不是**可裁项，
它是「权威全文快照 + 排序闸状态」的载体，裁掉会让「长思考 / delta 被丢弃」的会话转录**永久截断**。

---

## 1. 表 1：样本日志逐类型实测（61,454 行 / 91 MB）

| # | type | 行数 | 行占比 | 字节 | 字节占比 |
|---|---|---|---|---|---|
| 1 | `assistant.reasoning` | 18,534 | 30.2% | 20.47 MB | 22.5% |
| 2 | `tool.completed` | 3,581 | 5.8% | 19.18 MB | 21.0% |
| 3 | `tool.progress` | 23,045 | 37.5% | 15.93 MB | 17.5% |
| 4 | `llm.request.started` | 2,561 | 4.2% | 12.04 MB | 13.2% |
| 5 | `llm.request.finished` | 2,560 | 4.2% | 10.32 MB | 11.3% |
| 6 | `tool_receipt_recorded` | 2,391 | 3.9% | 7.66 MB | 8.4% |
| 7 | `tool.requested` | 3,581 | 5.8% | 2.23 MB | 2.4% |
| 8 | `tool.reduced` | 3,581 | 5.8% | 1.93 MB | 2.1% |
| 9 | `assistant_delta` | 935 | 1.5% | 0.68 MB | 0.7% |
| 10 | `injection:command` | 34 | 0.1% | 0.26 MB | 0.3% |
| 11 | `assistant_message` | 19 | 0.0% | 0.09 MB | 0.1% |
| — | **上表合计** | **60,822** | **99.0%** | **90.79 MB** | **≈99.6%** |
| — | 其余未列类型 | 632 | 1.0% | ~0.4 MB | ~0.4% |

要点：

- 头部 6 类占 **94% 字节**，与计划 §3.2 的体积归因一致。
- **单行最大**：`llm.request.finished` 与 `assistant.reasoning` 的 P99 行（0.03–0.07 MB/行）——
  印证它们是「整段权威文本」的载体，不是纯遥测。
- 632 行未列类型（1.0%）是 §3.2.4「未知类型一律不裁」的实际适用面：**不能忽略**。

---

## 2. 表 2：类型 × 对最终 Scene 的影响（权威分类）

判定列含义：**新增 cell** = 会不会创造可见 item；**终态 cell** = 会不会改写已终态 item；
**mutable** = 只改运行中 item；**字节** = 表 1 实测。

| type | `classify` → op | 新增 cell | 更新终态 cell | 只更新 mutable | 其他副作用 | 层级 |
|---|---|---|---|---|---|---|
| `tool.reduced` | `opNone`（`isSilentSystemEventType` `:911-928`） | 否 | 否 | 否 | 无（**不产生任何东西**） | **A** |
| `llm.retry` | `opNone`（`:836-838`） | 否 | 否 | 否 | 无（重试由 bridge 状态区渲染） | **A** |
| `session_start` / `session_interrupted` / `session_compact_skipped` / `context.reconciled` / `planning.started` / `subagent.batch.started` / `subagent.started` / `task.started` / `team.task.started` / `context.tool_schema.frozen` | `opNone` | 否 | 否 | 否 | 无 | **A** |
| `session_end` | ⚠️ **`opSessionEnd`**（`:773` 先判，**不是** `opNone`） | 否 | 否 | 否 | **收尾所有未完成流式项**（终态化） | **C** |
| `tool.progress` | `opToolProgress`（`:2326-2355`） | ⚠️ **条件性**：identity 缺失或已终态 → `applySystem` **新增 system cell**（`:2329-2332`） | 否 | 是（累加 `Head` 细节行，`:2337-2351`） | 无 | **B** |
| `tool_receipt_recorded` / `tool_receipt_replayed` | `opToolReceipt`（`:2448-2467`） | ⚠️ **条件性**：cell 缺失时 `applyToolStarted + applyToolFinished` **重建一行**（`:2464-2466`） | 否（已终态 → 仅 `DuplicateCount++`） | 是（就地补完终态，`:2461`） | 无 | **B** |
| `llm.request.started` | `opLLMStarted`（`:1541-1547`） | 否（注释明示「不创建可见占位」） | 否 | 否 | 登记流身份 `beginAssistantRequest`（后续 delta/final 依赖它） | **B** |
| `llm.request.finished` | `opLLMFinished`（`:1906-1956`） | ⚠️ **失败分支新增 error cell**（`:1912-1919`） | 否 | 否 | ⚠️ **`requestFinished[key]=true` 排序闸状态**（`:1931-1936`）+ **reasoning/assistant 权威全文快照**（`:1937-1952`） | **C** |
| `tool.requested` | `opToolStarted`（`:2265`） | **是**（分配 CauseID 的 tool_call） | 否 | 否 | 无 | **C** |
| `tool.completed` / `tool.failed` / `tool.cancelled` | `opToolFinished`（`:2357`） | ⚠️ identity 缺失 → `applySystem` 新增 system cell（`:2360-2362`） | **是**（终态 + `tool_output`） | 否 | 无 | **C** |
| `assistant.reasoning` | `opReasoning`（`:1320`） | **是**（`KindReasoning` item） | 是 | — | 无 | **C** |
| `assistant_delta` | `opAssistantDelta` | 条件性 | — | 是 | 无 | **C** |
| `assistant_message` | `opAssistantFinal` | 条件性 | 是（提交终态） | — | 解除 native-history 阻塞 | **C** |
| `injection:*`（`Type==""` 行） | 注入路径（`:3391`） | — | — | — | historyReset / 命令 / 交互注入 | **C** |

字节（按层级汇总）：A ≈ 1.93 MB（2.1%）｜B ≈ 35.63 MB（39.1%，= `tool.progress` + `tool_receipt_recorded` + `llm.request.started`）｜C ≈ 53.23 MB（58.4%）。

---

## 3. 四个必须显式处理的坑（普查的主要产出）

### 坑 1：`session_end` 在 silent 名单里，但不是 no-op

`isSilentSystemEventType`（`:911-928`）列出 `EventSessionEnd`，但 `classify` 在 `:773` **先**匹配
`EventSessionEnd` 并返回 `opSessionEnd`（行为：收尾所有未完成流式项），`:911` 那段永远走不到。
→ **任何「用 silent 名单当裁剪依据」的实现都是错的**；裁剪白名单必须是显式常量，不能由 `isSilentSystemEventType` 派生。

### 坑 2：`tool.progress` 裁掉会「少内容」，配对缺失时还会「多一行」

- identity 在场且未终态：只 upsert 同一 mutable tool cell（`:2337-2351`），裁掉 = 丢 `Head` 里累积的细节行。
  多数情况下 `tool.completed` 会用 `display_head` 覆写 `Head`（`:2370-2371`）→ 细节行本来也会被替换，
  但**无 `display_head` 且无 title 的调用**会保留细节行（`:2376-2380` 的 `title + t.Head[newline:]` 分支）→ 可见差异。
- identity 缺失或已终态：**回落成 `applySystem` 新增可见 system cell**（`:2329-2332`）。
  裁掉它反而**消除了**这类 system 行 → 与实时路径**结构不等价**。

**约束**：`tool.progress` 只有当「同 callID 的 `tool.requested` 未被裁」时才可裁；
且必须接受「细节行丢失」这一内容级差异（须由等价性单测钉住，见 §5）。

### 坑 3：`tool_receipt_recorded` 是崩溃恢复的**唯一**重建来源

`:2464-2466` 显式处理「原始 started/finished 缺失」的恢复路径：先用回执 `applyToolStarted` 建身份，
再 `applyToolFinished` 落终态。这是设计意图（`:813-819` 注释：回执是崩溃恢复账本）。
→ 裁掉回执 = 丢掉「以回执为唯一记录的那一行工具调用」。
**约束**：只能裁「已证明存在同 identity 的 started/finished」的那部分；配对证明要便宜（见 §5）。

### 坑 4：`llm.request.finished` 承载权威全文，裁掉 = 永久截断

成功分支（`:1921-1952`）不产生 cell，看似可裁，但它做了三件事：

1. `e.requestFinished[key] = true`（`:1907-1910`）——**排序闸状态**，`:1931-1936` 用它决定是否
   在 assistant 前插空的 reasoning 占位 cell（native-history ordering fence）。裁掉 → 迟到 reasoning 的排布不同。
2. `reasoningSnapshotKey` / `assistantSnapshotKey` 的**整段权威全文收敛**（`:1937-1943`、`:1947-1952`）。
   注释写明动因：**coalesce 预算溢出会丢弃 reasoning delta**，「仅靠增量拼装无法复原」。
   裁掉 → 被丢弃 delta 的会话在 resume 后**永久截断**，且实时路径与重放路径**不一致**。
3. 失败分支（`:1912-1919`）新增 error cell。

→ `llm.request.finished` 归 **Tier C**，不可裁。这一条把计划 §3.3 的「`llm.request.*` 只裁普查证明无影响的那部分」
从「13.2%+11.3% 字节可期」压缩到「`llm.request.started`（13.2%）待证明，`finished` 全部保留」。

---

## 4. 对 L1.1（便宜解析）的施工约束

1. **白名单是显式常量、单一事实源**，不得从 `isSilentSystemEventType` 派生（坑 1）。
   建议与 L1.2 共用同一常量，放在 `cmd/aicli/commands/` 下新文件（例如 `chat_eventlog_trim.go`），
   含两个集合：`skipAlways`（Tier A）、`skipGuarded`（Tier B，需守卫条件）。
2. **Tier B 的守卫条件必须便宜**：字节级前缀扫描只能看到「当前行的 type」，看不到配对。
   因此 Tier B 的可行做法是**两遍扫描**：第一遍只做前缀扫描，收集在场 identity 集合
   （`tool.requested` 的 callID、`llm.request.started` 的 stream_id）；
   第二遍才决定哪些 Tier B 行可以跳过解析。两遍前缀扫描仍远便宜于 60k 次 `json.Unmarshal`，
   但**必须实测**：若两遍扫描的净收益不明显，宁可只上 Tier A + 把目标压到 L1.2（写侧分流）。
3. **`Type==""` 的注入行永远走原路径**（计划 §3.3 已定），无论白名单如何。
4. **计数要可见**：跳过数、守卫拒绝数、未知类型数都要进 debug display，回归时可对比
   （`eventlog_replayed/recorded/bytes` 语义不变，跳过 ≠ 失败 → 不动 `eventLogFailures`）。
5. **预期修正**：parse 3.0s → **1.4–1.6s**（52% 行免解析），不是 0.3s；
   `apply` 侧只有 Tier A（2.1% 字节）是无条件受益，Tier B 还需要「跳过 apply」的同身份证明。

---

## 5. 验证要求（L1.0 → L1.1 的放行条件）

等价性基座已存在：`backend/cmd/aicli/commands/chat_runtime_events_replay_test.go`。
在动裁剪代码**之前**，先补以下样本并断言「重放后 Scene ≡ 实时路径构造的 Scene」：

1. **正常会话**：started → progress×N → completed → receipt，全类型齐备（golden）。
2. **悬空尾部**（崩溃中）：`tool.requested` 在场但**无** `tool.completed`；`llm.request.started` 在场但无 `finished`。
   → 断言裁 `tool.progress` / `llm.request.started` 后 Scene 仍等价（这是坑 2/3 的直接反例场景）。
3. **回执独存**（崩溃恢复）：只有 `tool_receipt_recorded`，无 started/finished → **必须保留该行**（坑 3）。
4. **delta 被丢弃**：仅有 `llm.request.finished` 的 `assistantSnapshotKey` 含完整全文，
   delta 流为空 → 断言裁掉 `finished` 会失败（即该测试**保护** Tier C）。

这 4 个用例同时也是 L1.1/L1.2 的回归网；用例 2/3/4 是本普查新识别出的风险面。

---

## 6. 未决问题（留给 L1.1 实测回答）

- Q1：两遍前缀扫描的净收益 vs 只裁 Tier A？→ 实测 `eventlog_parse` 分段耗时后再定。
- Q2：`llm.request.started` 是否可裁——依赖 `beginAssistantRequest` 是否被后续事件按 key 反查
  （若 delta/final 自带 stream_id 且不依赖预登记，则可裁；需单测用例 2 覆盖）。
  → **已由 §7.12 回答**：payload 不可"掏空"、行不可整丢，但**只需解 6 个 identity 键**。
- Q3：把 Tier B 的「细节行丢失」纳入可接受的可见差异，还是要求逐字节等价？
  （建议：Scene 级等价，允许 `tool.progress` 累积的中间细节行差异，但需在 doc 中标注为**已知差异**。）

---

## 7. L1.1 实施记录（2026-09-25）

**改动足迹（6 个文件）**

| 文件 | 角色 |
|---|---|
| `commands/chat_eventlog_trim.go` | **新增**：白名单 / 前缀扫描 / 零拷贝切分 / 便宜解码 / kill-switch |
| `commands/chat_eventlog_trim_test.go` | **新增**：等价性门禁 + 14 个不变量测试（§7.9） |
| `commands/chat_eventlog_started_dependency_test.go` | **新增**：Q2 决策钉（§7.12）——`llm.request.started` 的读取面等价性 + "掏空 payload 不安全" |
| `commands/chat_runtime_events.go` | 接入：重放分派处 + 3 个计数 + `eventLogTrimStats()`（**该文件同时被其它 agent 改动**） |
| `commands/chat_debug_document.go` | 只加一段 `Event Log Trim:` 条件 meta（§7.5-4） |
| `commands/chat_debug_display_http.go` | 只在 `eventLog` 结构上加 3 个 `omitempty` 字段（§7.5-4） |

接入方式：重放分派处「**命中便宜路径则用、否则走原路径**」——不新增第二条 apply 代码路径。

**状态（放行条件三项）**

| 放行条件 | 状态 |
|---|---|
| 1. 等价性单测绿 | ✅ 14 个测试（§7.9） |
| 2. 全包基线测试绿 | ✅ `go test ./cmd/aicli/commands/ -count=1` → `ok … 155.650s`（§7.10 #3）；含 §7.12 新增门禁后复跑 `ok … 137.585s`（§7.10 #5） |
| 3. harness A/B 无回归 | ⛔ **阻塞**：preflight 不满足（空闲 2,314 MB < 6,000 MB；8 个 `aicli*` 实例），见 §7.10 |

→ **本节的性能数字全部是行内微基准，不是端到端回归结论**；端到端相位数字（P2/P9/P10）仍待安静机器复跑。
→ 在条件 3 完成前，本实施**不得**被表述为"已改善启动耗时"。

### 7.1 与 §4.2 施工约束的偏离：**没有做两遍扫描**

§4.2 建议 Tier B 用「两遍前缀扫描」收集 identity 集合。实测后**放弃该设计**，因为
它要解决的问题被一个更便宜的机制替代了：

| 方案 | 对 `tool.progress`（23,045 行 / 37.5%）的处理 | 实测成本 |
|---|---|---|
| 全量 `json.Unmarshal(Event)` | 解到 `map[string]interface{}` + 逐值 `interface{}` 装箱 | 186 ms（该切片） |
| 零拷贝 + 全量解码（M1 已落地） | 同上，仅省 `Text()`/`[]byte()` 两次拷贝 | 965 ms / 215 MB（**全日志**） |
| 两遍扫描 + **跳过**（§4.2 原建议） | 第一遍收 identity，第二遍跳过该行 | 需第二遍 91 MB 扫描，且**丢内容**（坑 2） |
| **紧结构体便宜解码**（本实施） | 解到 5 字段 struct，**仍然 apply** | **113 ms / 7.4 MB** |

结论：**「便宜解码」比「跳过」更划算，而且没有语义代价。**

1. 紧结构体只解 5 个键，**对同一批行**（23,045 行 `tool.progress`）比全量解码便宜 **39%**
   （113 vs 186 ms，省 ~73 ms/日志）；作为量级参照，零拷贝 + 全量解码**整份日志**是 965 ms / 215 MB，
   而整份日志只取 type 前缀只要 6.5 ms；
   （注：不能把 113 ms 与 965 ms 直接相除当加速比——前者只覆盖 37.5% 的行，后者是全日志。）
2. **跳过需要证明配对**（§4.2 的两遍扫描就是在买这个证明）；**便宜解码不需要证明任何东西** ——
   行照常 apply，走向由编码器真实状态决定；
3. 跳过会落到坑 2 的两种不等价（丢细节行 / 配对缺失时多出一行 system cell），便宜解码两者都不会发生。

因此 Q1 的答案不是「两遍扫描值不值」，而是：**这个问题不需要两遍扫描来回答**。
唯一必须便宜获取的信息是「当前行的 type」，而那只是一个前缀——全日志 **6.5 ms / 1.2 MB**。

### 7.2 三条便宜路径

1. **零拷贝行切分**（`eventLogNextLine`）：行切片直接指向整块 raw，不再经
   `bufio.Scanner.Text()` + `[]byte()` 的两次拷贝。
   → **顺带止血一个潜在致命缺陷**：`bufio.Scanner` 默认 token 上限 64 KB，
   而本机真实长会话的**最长行 65,516 B** —— 只差 20 字节，再长一点整个 resume 就会报
   `token too long` 而**直接失败**。这不是优化项。
2. **Tier A 白名单**（`eventLogTrimAlwaysSkip`，11 个类型）：`classify` 恒为 `opNone`、`apply` 空实现
   → 跳过 `json.Unmarshal`。**但仍对每行调用 `Encode`**（理由见 §7.3）。
3. **`tool.progress` 紧结构体解码**（`decodeEventLogProgressFast`）：只解
   `tool_call_id` 与 `message`/`progress`/`detail`/`status`，与 `encoder.toolProgressText`
   的优先级序列逐键对应（该键集合是**契约**，改 `applyToolProgress` 的读取面必须同步）。

**外加 apply 期守卫**（`EventEncoder.ToolProgressAttachable`）：用编码器**已有的** `toolByID` 状态
确认该 callID 存在且未终态 ⇒ 该行「一定走 upsert 分支」。
守卫为假 → 该行回落全量解码 —— 坑 2 的第二种不等价（多出一行 system cell）由此被**结构性排除**，
而不是靠约定排除。

### 7.3 一个反直觉的实测发现：Tier A 不能跳过 `Encode`

`TestEventLogTrimAlwaysSkipIsExactlyOpNone` 起初红了，报
`tail mismatch: want=<nil> got=&{ItemID: Seq:0}`：**`Encode(opNone)` 虽不改任何 Item，
却仍会把 `model.Tail` 从 nil 置为非 nil**（编码器既有行为，与本改动无关，两臂同样受影响）。

→ 这**反向证明**了「Tier A = 跳过整行」是错的：跳过 `Encode` 会让 Tail/clock 与实时路径分叉。
本实施因此只跳过「读 payload」，**不跳过 Encode**。单测用 `assertRenderItemsEquivalent`
（比 Item、忽略 Tail）钉住「Tier A 对渲染产物零影响」，另有测试钉住「Encode 仍被逐行调用」。

### 7.4 回答 §6 的三个未决问题

- **Q1**（两遍扫描 vs 只裁 Tier A）→ 见 §7.1：**未采纳两遍扫描**。
  实测守卫**拒绝数 = 0 / 23,045**（每一行 tool.progress 的 callID 都 attachable ⇒ 全部走便宜解码，
  无一回落）；也就是说「跳过」能省下的内容，便宜解码**本来就没读**。前缀扫描本身只要 6.5 ms，
  再补一遍 91 MB 扫描去买一个「连 unread 键也要省」的收益，性价比为负。
- **Q2**（`llm.request.started` 是否可裁）→ **已决，见 §7.12**。L1.1 本身不触及 `llm.request.*`，
  但该项已用"真实日志差分重放 + 合成语料机制探针"独立回答：
  **payload 不可掏空、行不可整丢，正确形态是"只解 6 个 identity 键"**（= §7.2-3 的模式）；
  门禁 `chat_eventlog_started_dependency_test.go` 已入库。
- **Q3**（是否把 Tier B 的细节行丢失纳入可接受的可见差异）→ **不需要任何差异**，这是本实施
  相对计划/普查的一个改进。普查预设「裁 Tier B 必然丢内容，故须在『接受差异』与『逐字节等价』间二选一」；
  而本实施**不裁任何行**，只是用更便宜的方式解码同一行：Tier A 的 11 个类型是 `opNone`（对 Item 零影响），
  `tool.progress` 全行走 upsert 分支且读取面被逐键复刻。
  → 当前结论是**Scene 级逐项等价**，**不引入已知差异**。（将来若守卫出现拒绝（> 0），
  那些行自动回落全量解码——仍然等价，只是便宜路径的收益随之下降。）

### 7.5 §4 其余约束的落实

| §4 约束 | 落实 |
|---|---|
| 1. 白名单是显式常量、不派生自 `isSilentSystemEventType` | ✅ `eventLogTrimAlwaysSkip`；文件头注释写明差集是**故意的**（`session_end` / `session_interrupted` 必须留在名单外，坑 1），并注明「新增静默类型时保守起见不加」 |
| 2. Tier B 守卫条件必须便宜 | ✅ 守卫读编码器**已有的** `toolByID` 状态，零额外扫描；前缀扫描 6.5 ms / 1.2 MB |
| 3. `Type==""` 的注入行永远走原路径 | ✅ 前缀 `{"type":"` 匹配失败即回落（`eventLogInjection` 无 type 字段） |
| 4. 计数可见、跳过 ≠ 失败 | ✅ `eventLogTrimStats()` → (trimmed, cheapProgress, cheapStarted, cheapFallback)（L1.1 落地时为 **3 值**，L1.2 扩为 **4 值**，见 §7.13.3；与 `eventLogStats` 分开，以保住后者的四值签名不动）；已接入**两处** /debug：文本态 `Event Log Trim: trimmed=… cheap_progress=… cheap_started=… cheap_fallback=…`（全 0 时不显示）与 `GET /debug/chat/status` 的 `event_log.{trimmed,cheap_progress,cheap_started,cheap_fallback}`（`omitempty`）。**不动** `eventLogFailures` 语义（跳过 ≠ 失败） |
| 5. 预期修正（parse → 1.4–1.6 s） | ❌ **本实施达不到该目标，且这是刻意取舍** —— 见 §7.8 |
| B 不变量：任何识别/解码失败一律回落 | ✅ 类型前缀取不到、超长（>64）、含转义、JSON 损坏、字段类型不符 → 全部走原路径 |

### 7.6 kill-switch 与回滚

`AICLI_RESUME_REPLAY_TRIM`：默认开启；**只有** `0`/`false`/`no`/`off`（trim + lower）才关闭，
与既有 `AICLI_SQLITE_WARMUP` 同约定。关闭后逐字节回到 M1 之前的解析路径，
用途是 A/B 归因与快速回滚（每次重放只判定一次）。

### 7.7 下一步

1. **harness A/B 复跑**（L1.1 放行的第三项条件）：须先满足 §4 前置条件（空闲内存 ≥ 6 GB、
   无其它 aicli 实例）；A = trim 开 / B = `AICLI_RESUME_REPLAY_TRIM=0`，关注
   `eventlog_parse` 与 **P10 acked 回退次数**（P10 是 M1 复跑时唯一「基线绿 → 变更后红」的未决项）。
2. ~~**Q2** 随 L1.2（写侧分流 / `llm.request.*`）一并回答~~ ✅ **已独立回答，见 §7.12**；
   L1.2 落地时按"只解 6 个 identity 键"实现，不要掏空 payload。
3. ~~`eventLogTrimStats` 的展示端~~ ✅ 已接入两处 /debug（见 §7.5-4）；后续复跑直接读文本态
   `Event Log Trim:` 行或 `status.event_log.*` 即可做 A/B 归因。
### 7.8 对 §4.5 目标的影响（诚实评估）

§4.5 的预期是「parse 3.0 s → **1.4–1.6 s**，52% 行免解析」。**本实施达不到，而且是刻意不追求它。**

原因：那个 52% 的前提是「A+B 都可免解析」。但普查自己的 §3 已经证明：
**Tier B（`tool.progress` 等）免解析会改变场景**（坑 2：丢细节行 / 配对缺失时多出一行）。
§4.2 提出用两遍扫描去买这个配对证明，§7.1 已说明为何不采纳；于是本实施的取向改成：

> **不裁行、只裁"读进去的字段"** —— 语义上逐项等价，性能上只拿"便宜解码"这一档。

按已实测的切片数字，收益可以这样估（**全部是行内微基准，非端到端**）：

| 项 | 收益 | 性质 |
|---|---|---|
| 零拷贝行切分（M1 已落地） | 1010 → 965 ms | 已计入基线 |
| `tool.progress` 便宜解码 | 186 → 113 ms（**省 ~73 ms**） | 本实施 |
| Tier A 白名单（3,614 行 / 5.88% 行 / 2.1% 字节） | 未单独实测；**上界 ≈ 解码耗时的 5.88%**（若按全日志 965 ms 线性摊） | 本实施 |
| **合计（本实施新增）** | **≈ 75–130 ms，即 parse 的 ~8–13%** | — |

→ 换句话说：**本实施把 parse 从 ~1.0 s 降到 ~0.87–0.93 s（微基准口径），不是降到 0.5 s。**
计划里 `eventlog_parse` 3.0 s（端到端口径）若同比例，约 → 2.7 s，**远达不到 1.4–1.6 s**。

**要把 1.4–1.6 s 真正拿到手，必须换路线**（留待 L1.2 决策）：

1. **按类型做紧结构体解码**（本实施对 `tool.progress` 做法的推广）：收益与"该类型占的字节比"成正比，
   且**不改变语义**。这是唯一一条已知能不丢内容的路线，代价是要为每个类型维护一份
   「apply 读取面」契约 + oracle 测试（本实施已建立该模式）。
2. **写侧分流**（计划 L1.2 原意）：让重放根本不必读那些行（例如按类型分文件/分段落），
   而不是在读侧裁。这绕开了坑 2/坑 4 的全部证明负担。

**本实施的真实价值**（不应以 parse 数字衡量）：

1. **止血**：最长行 65,516 B 距 `bufio.Scanner` 上限仅 20 B —— 修掉的是一个**会让 resume 直接失败**的缺陷；
2. **建立了可复制的安全模式**：前缀识别 + 便宜解码 + apply 期守卫 + oracle 等价性单测 + kill-switch
   —— L1.2 的每种新类型都照这个模子做，等价性由测试钉住而不是靠推理；
3. **零风险**：不裁行 ⇒ 不引入已知差异；kill-switch 可逐字节回到旧路径。
### 7.9 §5 用例覆盖对照

`chat_eventlog_trim_test.go` 的 A/B 门禁 `TestEventLogReplayTrimABEquivalence` 用同一份合成日志跑两臂
（`AICLI_RESUME_REPLAY_TRIM=1` vs `=0`），断言渲染模型逐项等价 + 便宜路径计数只在 A 臂增长。

| §5 用例 | 对应语料/测试 | 状态 |
|---|---|---|
| 1 正常会话（started → progress×N → completed → receipt） | `trimCorpusClosedCall` 段 + 收尾后晚到 progress | ✅ |
| 2 悬空尾部（`tool.requested` 在场、无 `completed`） | `trimCorpusOpenCall`：8 条 progress 全走便宜分支，文本留在最终快照可断言 | ✅ |
| 3 回执独存（崩溃恢复） | `trimCorpusReceiptCall`：无 started/finished，只有回执 → 必须重建一行 | ✅ |
| 2-llm 悬空（`llm.request.started` 无 `finished`） | **依赖面已刻画**：`chat_eventlog_started_dependency_test.go`（§7.12，非重言式） | ⏸ A/B 门禁仍推后到 L1.2 |
| 4 delta 被丢弃（仅 `finished` 的 snapshot 含全文） | — | ⏸ 推后到 L1.2 |

**为什么 2-llm / 4 不是 L1.1 的门禁**：L1.1 **不触碰 `llm.request.*`**，两臂对这些行走**完全相同**的原始路径
→ 此刻写这种 A/B 断言是**重言式**（恒真、无鉴别力），只能防"将来有人误把 llm.* 加进白名单"。
它们真正具备门禁效力，是在 L1.2 把 `llm.request.*` 纳入便宜路径之后。

→ **登记为 L1.2 的前置条件**：动 `llm.request.*` 之前，必须先补上这两个用例（§5 已经把它们定义为 Tier C 的保护网）。

> **2026-09-25 更新**：2-llm 用例的**依赖面**已提前回答（§7.12）——它不需要等 L1.2 才有鉴别力：
> "掏空 payload 是否安全"可以**直接**用合成语料证伪（已证伪）。上面这段关于"重言式"的判断只对
> **trim 开/关的 A/B** 成立（对 `llm.request.*` 两臂同路径），对**改写同一行的前后对比**不成立。
> 用例 4（delta 被丢弃）仍按原计划留到 L1.2。

**其余覆盖**（本实施新增的等价性边界，超出 §5 要求）：

| 测试 | 钉住的不变量 |
|---|---|
| `TestEventLogNextLineMatchesBufioScanner` / `...ConsumesEveryPhysicalLine` / `...BeyondBufioLimit` | 零拷贝切分与 `bufio.Scanner` 语义等价；**且 > 64 KB 的行不再中断重放**（§7.2-1 的止血点） |
| `TestEventLogTypePrefixIsFailSafeOracle` | 前缀扫描两个方向：合法行必识别；截断/转义/超长 → 必须回落 |
| `TestEventLogProgressFastMatchesFullDecode` | 便宜解码结果与全量解码在 apply 读取面上逐键一致（oracle） |
| `TestEventLogProgressFastDecodeFailsClosed` | 非字符串键 / 形状不符 → `ok=false` |
| `TestEventLogProgressFastEmptyCallIDCaughtByGuard` | 「无 payload」边界由 **apply 期守卫**兜住（不在解码器里） |
| `TestEventLogProgressEventKeepsUpsertResultStable` | 最小事件的键集合（`tool_call_id` + `message`）足以复现 upsert 结果 |
| `TestReplayTrimToolProgressGuards` | 守卫与 `applyToolProgress` 落点**同构**：`attachable == 编码后 item 数不变` |
| `TestEventLogTrimAlwaysSkipIsExactlyOpNone` | Tier A 的 11 类型对**渲染 Item** 零影响（Tail 另计，见 §7.3） |
| `TestEventLogTrimExcludesTerminalSessionEvents` | 故意差集：`session_end` / `session_interrupted` **不得**进白名单（坑 1） |
| `TestEventLogReplayTrimKillSwitch` | kill-switch 取值语义（只有明确关闭值才关闭） |
| `TestEventLogReplayTrimMatchesLegacyOnCorruptLog` | 损坏日志下两臂行为一致（都回落、都不 panic） |

### 7.10 验证记录（可复现）

全部在 `backend/`（模块根）下执行：

| # | 命令 | 结果 |
|---|---|---|
| 1 | `gofmt -l` 上述 6 个文件 | ✅ 无输出（全部已格式化；`chat_eventlog_started_dependency_test.go` 首次被列出后已 `gofmt -w`） |
| 2 | `go test ./cmd/aicli/commands/ -count=1`（改动足迹完成前） | ✅ `ok … 138.041s`，`FULL_EXIT=0` |
| 3 | `go build ./cmd/aicli/commands/` → `go test ./cmd/aicli/commands/ -count=1`（**最终状态**，含两处 /debug 改动） | ✅ `BUILD_OK` + `ok … 155.650s`，`FULL_TEST_EXIT=0` |
| 4 | `go test -run 'TestStartedIdentityOnlyPayloadIsReplayEquivalent\|TestStartedEmptyPayloadIsNotUniversallySafe' -count=1 -v ./cmd/aicli/commands/`（§7.12 的决策钉） | ✅ 2/2 PASS，`ok … 0.272s` |
| 5 | `go test ./cmd/aicli/commands/ -count=1`（**最终状态**，含 §7.12 新增门禁文件后） | ⚠️ 第 1 次 `FAIL … 134.838s`（`FULL_TEST_EXIT=1`）→ 第 2 次 ✅ `ok … 137.585s`，`FULL_TEST_EXIT=0`（同码、间隔约 30 s、期间无改动；归因见下「环境现象」第 3 条） |

→ **结论：全包测试在最终改动状态下为绿**（#3 = 改动足迹完成时；#5 = 追加 §7.12 门禁后的复跑），这是 L1.1 放行条件第 2 项的证据。
第 3 项（harness A/B）见下。

**必须记录的环境现象（不是本实施的问题，但会污染验证信号）**：
验证期间该 repo 存在**其它 agent 的并发未提交改动**，其中

- `internal/usageledger/service.go:142,146` → `undefined: strings`（import 被删）
- `internal/api/skills/handler.go:1785` → `undefined: ledgerProfileName`

在约 06:57–06:59（本地 CST，下同）正处于**半成品状态**，使 `go build ./cmd/aicli/commands/` 因**依赖包**
编译失败而整体失败（`BUILD_EXIT=1` / `[build failed]`）。
→ 判定：**与本实施无关**（这两个文件不在改动足迹内，报错只指向它们）。
→ 处置：改成**轮询等待依赖树恢复**再取门禁结果——第一次 `go build` 仍失败、
**第二次（约 45 s 后）成功**（`BUILD_OK_AFTER_ATTEMPT=2`），随后 #3 全包绿。
这同时说明该失败是**短暂且可自愈**的（对端 agent 很快补齐了自己的 import）。
→ 教训：**任何基于「构建/测试通过」的结论，都必须先确认失败信息指向的包是不是自己的改动**，
否则会把并发 agent 的半成品误判成本实施的问题。

**环境现象 3（07:41–07:47，对应 §7.10 #5）：同一份代码复跑出现 1 次 FAIL / 1 次 PASS。**

- 第 1 次 `FAIL … 134.838s`（`FULL_TEST_EXIT=1`），第 2 次 `ok … 137.585s`（`FULL_TEST_EXIT=0`）；
  两次之间**没有任何代码改动**（间隔约 30 s）。两次都跑满全程（~135 s），不是早期崩溃。
- 该时段实测同机有**其它 agent 的 4 个并发 `go` 进程 / 2 个 `commands.test` 二进制**
  （同一包的并发测试运行），空闲物理内存 2,626–2,836 MB。
- → 判定：**瞬态/争用型失败**（同码复跑即绿），**不是本实施引入的确定性失败**；
  放行条件第 2 项以第 2 次的绿为准。
- → **但失败用例名未定位**：非 `-v` 的 `go test` 把 `--- FAIL:` 块混在 TUI 转义日志（约 312 KB）里，本次未捞出。
- → 待办：安静窗口用 `go test … -json`（或 `*>` 落盘日志）复跑一次即可定名归档；
  在定名之前**不得**把该包表述为"100% 稳定绿"。

**端到端相位（P2 / P9 / P10）**：**未测**。归因见 `resume-m1-ab-measurement.md` §4 ——
复跑前置条件（空闲内存 ≥ 6 GB、无其它 aicli 实例）当前不满足，**不得**在污染机器上取数。

**当时的 preflight 实测**（07:01）：空闲物理内存 **2,314 MB**（门槛 6,000 MB），
同机 `aicli*` 进程 **8 个**（另一个 agent 正在该 repo 上并发工作，见上文的依赖包半成品）。
→ 两项前置条件**均不满足**，A/B 复跑**明确阻塞**；这也解释了为何本轮只做行内微基准。

### 7.11 已知缺口（本实施**未**做，登记以免被误读为已完成）

1. **「未知类型数」的*归因*计数未实现**（原表述"未实现"不准确，2026-09-25 更正）：
   §4.4 要求三个计数（跳过数 / 守卫拒绝数 / **未知类型数**），本实施只加了与前三者对应的
   `trimmed` / `cheap_progress` / `cheap_started` / `cheap_fallback`。
   **但"出现了没见过的类型"这一告警今天已经存在**：编码器自带 `Stats.UnknownCount`
   （`encoding/model.go:188`；classify 的 default 分支自增，`encoder.go:857`），且**已经在**
   /debug 两处显示（`chat_debug_document.go:590` 的 `Unknown Types:`、
   `chat_debug_display_http.go:591` 的 `unknown_count`）；默认分支还会 append 成 system 块，**信息不丢**。
   → **残余缺口（比原表述窄）**：「已知静默（opNone）但不在白名单」的类型不可见。
   它们只是"白名单可以再扩一条"的**机会成本**，不是正确性风险（仍走全量解析）。
   检测它们需要在 encoding 包加一个"类型 → 是否静默"的导出钩子（`classify` 目前不导出），
   属跨包 API 变更，登记为后续项，不在 L1.2 范围内。
2. **端到端相位未测**：P2 / P9 / P10 全部未取数（环境阻塞，见 §7.10）。
   本实施**不得**被表述为"已改善启动耗时"。
3. ~~**§5 用例 4（delta 被丢弃）未补**~~ ✅ **已随 L1.2 落地**（§7.13.2）：
   用例 2-llm 的**依赖面**由 §7.12 的 `chat_eventlog_started_dependency_test.go` 覆盖（非重言式，
   已入库并验证），其 trim A/B 门禁已由 `TestEventLogStartedFastMatchesFullDecode`
   （危险排序 + 两臂逐字节比较 + 命中/未命中双向断言）补上。
4. **全包复跑的那 1 次瞬态 FAIL 未定名**：§7.10 #5 的第 1 次 `FAIL … 134.838s` 只留下退出码，
   失败用例名被 TUI 转义日志淹没（归因见 §7.10「环境现象 3」）。
   → 影响：**不改变**放行条件第 2 项的结论（同码第 2 次全绿），但"该包在争用机器上的稳定性"仍无数据。
   → 待办：安静窗口用 `go test … -json` 复跑一次定名归档。

### 7.12 Q2 已决：`llm.request.started` 的 payload 不可"掏空"，只可"只读 6 个键"（2026-09-25）

§6 把 Q2 描述为"依赖 `beginAssistantRequest` 是否被后续事件按 key 反查"。实测给出的答案比这个
二分更细一层：

> **payload 里的 identity 字段确实参与登记（⇒ 不可整体丢弃），但登记之外的 12 MB 内容零贡献
> （⇒ 不必全量解码）。**

#### 7.12.1 取证一：真实长会话差分重放

语料 = §1 表 1 的同一份日志（61,468 行 / 91.16 MB），其中 `llm.request.started`
**2,561 行 / 12.04 MB / 13.2% 字节**。

做法：把同一份日志按 4 种改写喂给**同一条生产重放路径**（写 JSONL → `replayEventLog`），
导出渲染模型 JSON 逐字节比较；不等价时再比"剔除 `Created`/`Updated` 后的归一化 JSON"，
以把 **clock 位移**与**语义差异**分开（理由见 §7.3：`Item.Created/Updated` 取自 `e.clock`，
而 `Encode` 每次调用自增 clock）。

| 臂 | 改写 | 原始 JSON vs full | 归一化 vs full | 判读 |
|---|---|---|---|---|
| `full` | 不改 | — | — | 基线：10,305 items / 23,603,603 B / tail `item-10305 seq=10305` |
| `typeonly` | 整行 → `{"type":"llm.request.started"}`（= Tier A 现行形态） | **IDENTICAL** | IDENTICAL | 连 envelope 都可去 |
| `emptypayload` | 保留 envelope，只删 `payload` 键 | **IDENTICAL** | IDENTICAL | 连 clock 都不动 |
| `nostarted` | 整行丢弃 | DIVERGENT（首差 @8188：`"Created":8,"Updated":30` vs `7`/`29`） | **IDENTICAL** | **纯 clock 位移** |

四臂 item 数全部为 10,305。

#### 7.12.2 取证二：合成语料机制探针（决定性）

**取证一只能证明"这份语料里没有行依赖 started 的登记"，不能证明"机制安全"。**
要回答"依赖能不能被构造出来"必须用合成语料。两种排序，差别只在 fallback 行与
identifying 行的先后（其余事件完全相同）：

| 语料 | `full` | 掏空 payload | 整行丢弃 |
|---|---|---|---|
| **安全排序**（identifying 行先到） | 2 items | **等价**（2 items） | **等价**（2 items） |
| **危险排序**（无 step 的 fallback 行先到） | **1 item**（fallback 行落进 step cell） | **发散 → 3 items** | **发散 → 3 items** |

两条读法：

1. **危险排序下"掏空 payload"与"整行丢弃"的发散形态完全相同** ⇒ payload 里的
   `step` / `turn_id` 等字段**正是**登记的依据：掏空 payload ≡ 取消登记；
2. ⇒ **"真实日志上逐字节相同"只证明该语料无依赖**。这正是 §7.9 把「2-llm 悬空」
   列为 L1.2 前置条件的原因，现在它有了实测依据。

#### 7.12.3 结论：完整读取面 = 6 个键 + envelope

| 来源 | 键 |
|---|---|
| payload | `step`、`stream_id`、`llm_request_id`、`turn_id`、`logical_turn_id`、`trace_id` |
| envelope | `TraceID` |

依据（代码级，非推测）：

- `applyLLMStarted`（`encoder.go:1541-1547`）只调用 `beginAssistantRequest`，**不产生任何 Item**；
- `assistantRequestIdentityFromEvent`（`encoder.go:2651-2666`）只读上述 6 个 payload 键 + `ev.TraceID`；
- 登记写入的 `latestRequestByScope` / `requestAliases`（`encoder.go:108-109`）**全仓库只在 encoder.go 内被读写**；
- 旁证：前端事件契约把 `llm.request.started` 的 payload 声明为**空**
  （`frontend/src/types/runtime/event-contract.ts:159`）。

⇒ **L1.2 的正确形态是 §7.2-3 的紧结构体便宜解码**（只解这 6 个键），而不是"掏空 payload"或"整行丢弃"：

| 形态 | 等价性 | 结论 |
|---|---|---|
| **只解 6 个 identity 键** | **按构造等价**（读取面被逐键复刻） | ✅ 推荐 |
| 掏空 payload | 只在安全排序下等价 | ❌ 守卫无法预知"后续是否出现 fallback 行" |
| 整行丢弃 | 语义等价但 clock 位移；危险排序下发散 | ❌ |

**实现约束（留给 L1.2）**：紧结构体必须逐键复刻 `payloadString` 的转换语义
（`encoder.go:3140-3161`：`string` / `json.Number` / `float64` / `bool`，其余走 `json.Marshal`），
**不能**假设"非字符串即空"——`step` 是数字时它仍会被转成字符串参与登记。
oracle 测试照 `TestEventLogProgressFastMatchesFullDecode` 的模子做（§7.9）。

#### 7.12.4 口径边界（避免被误读）

1. 观测面是**渲染模型 JSON**（resume 重建的那一份）。TUI timeline / SSE / 前端 `turn_start` 不在口径内；
2. `llm.request.started` 的 payload 在**实时路径**确实被消费：`chat_runtime_events.go:5408`
   读 `message_count` / `context_prompt_tokens`（但受 `isRunActive()` 门控，重放时恒假），
   `internal/cacheanalytics/collector.go:13` 用它算 TTFT。这些消费者走**总线**；
   且 `runtime-events.jsonl` 的生产读取点**只有** `chat_runtime_events.go` 一条（已 grep 核对），
   所以**读侧**裁剪不影响它们；**写侧**裁剪（L1.2 原意）必须先复核这些消费者不会改从文件读；
3. 合成语料是**人工构造**的：它证明"依赖可构造"，不证明"生产会这样发"。危险排序在真实长会话
   （61,468 行）中**未出现**——但这不构成对其它会话的保证，故 L1.2 必须按"只解 6 键"实现，
   而不是按"本语料上掏空也等价"实现。

#### 7.12.5 门禁

`chat_eventlog_started_dependency_test.go`（新增）把上述两条钉住：

| 测试 | 钉住的不变量 |
|---|---|
| `TestStartedIdentityOnlyPayloadIsReplayEquivalent` | 安全排序下，"只解 6 个 identity 键"与全量解码的渲染模型**逐字节相同**（含 clock 派生字段） |
| `TestStartedEmptyPayloadIsNotUniversallySafe` | 危险排序下，"掏空 payload"**必须发散**，且与"整行丢弃"**同形** |

**它不是重言式 A/B**：A/B（trim 开 / trim 关）对 `llm.request.*` 两臂走同一条路径、恒真无鉴别力；
本测试比较的是**同一语料在 started 被改写前后**的渲染模型，有真实鉴别力——这正是 §7.9
所说"2-llm 用例要等 L1.2 才有门禁效力"的例外：它的**依赖面**现在就能测，而且已经测出结论。

**回归面**：加入本文件后全包复跑 `ok … 137.585s`（§7.10 #5），即该门禁**未破坏包内其它测试**。

### 7.13 L1.2 已落地：`llm.request.started` 紧结构体便宜解码（2026-09-25）

按 §7.12.3 的结论实现，形态 =「**只解 6 个 identity 键**」（既不掏空 payload，也不整行丢弃）。

> **命名澄清（避免与计划混淆）**：本节的「L1.2」指 §7.8 路线 1（**读侧**按类型紧结构体解码），
> 是 §7.9 登记的「动 `llm.request.*` 之前必须先补用例」的兑现。
> 计划 §3.4 / 里程碑 M4 的「L1.2」是**另一件事**（写侧双日志分流），**仍未做**；
> 两者同名但不同物，引用时请带上出处。

实现落点：`chat_eventlog_trim.go`（紧结构体 + 解码 + 最小事件）、`chat_runtime_events.go`
（重放循环分支 + apply 分支 + 计数）、`chat_debug_document.go` / `chat_debug_display_http.go`
（两处 /debug）、`chat_eventlog_started_dependency_test.go`（oracle / fail-closed / 真实形状）、
`chat_eventlog_trim_test.go`（A/B 语料 + 微基准）。

#### 7.13.1 与 §7.12.3 实现约束的一处**刻意偏离**（更省、也更安全）

§7.12.3 留的约束是「紧结构体必须逐键复刻 `payloadString` 的转换语义」。落地时改成：

> 6 个 identity 键在紧结构体里**全部声明为 `interface{}`**，解出的值原样透传给
> `runtimeevents.Event.Payload`；`payloadString` 仍**只由编码器实现一份**、在 apply 期执行。

依据：`interface{}` 解出的值与全量解码 `map[string]interface{}` 的值**逐字相同**
（同一 decoder、同一 JSON token：字符串→`string`、数字→`float64`、布尔→`bool`、
对象/数组→`map`/`slice`、`null`/缺失→`nil`），因此**不需要第二份转换实现**，
也就不存在"两份实现漂移"的风险 —— 比"复刻"更强。
`TestEventLogStartedFastValuesMatchFullDecode` 把这条逐键钉住（cheap 值必须与 full 值 `reflect.DeepEqual`）。

**类型坑（必须记住）**：写侧 `internal/agent/loop.go:2038` 把 `step` 写成**数字**
（`for step := 1; …` 的 int）。若紧结构体按直觉把 `step` 声明成 `string`，
`json.Unmarshal` 会因类型不符失败 → 便宜路径在真实日志上**永不命中**，
而线上只表现为"回落率 100%"，计数器上看不出异常。
`TestEventLogStartedFastHitsRealShape` 专钉这条（断言解出的 `step` 是 `float64`）。

envelope 的 `trace_id` 反向处理：**声明为 `string`**。若日志里它是非字符串，便宜解码失败回落，
而全量解码对同一行**同样失败**（`Event.TraceID` 是 string）⇒ 错误语义与行号逐字保持
（`TestEventLogStartedFastFailsClosed`，5 个子用例）。

#### 7.13.2 没有 apply 期守卫（与 tool.progress 的差异）

`applyLLMStarted` → `beginAssistantRequest` 按构造只有**一个无条件路径**，
不存在"守卫为假时读更多键"的第二分支 ⇒ 不需要 `ToolProgressAttachable` 那样的运行时守卫
（`chat_eventlog_trim.go` 的不变量 C 已按此修订）。等价性改由 **oracle 测试**保证：

`TestEventLogStartedFastMatchesFullDecode` 在**危险排序**（fallback 行紧跟 started，
其落点只能由登记决定）上比较两臂（trim 开 / trim 关）的渲染模型 JSON，要求**逐字节相同**；
同时断言 A 臂 `cheap_started > 0`（防空跑）与 B 臂为 0。语料含 4 种 payload 形状：
数字 `step` / 字符串 `step` / 空 payload / 带非 identity 大键（`model`/`messages`/`tools`）。

§5 用例 2-llm 的 trim A/B 门禁**就此补齐**（§7.11-3 关闭）。

#### 7.13.3 计数与观测

`eventLogTrimStats()` 由 3 值扩为 **4 值**（新增 `cheapStarted`），两处 /debug 同步：
文本态 `Event Log Trim: … cheap_started=…`（全 0 时不显示）与
`GET /debug/chat/status` 的 `event_log.cheap_started`（`omitempty`）。
§7.5-4 的表述以本节为准。

#### 7.13.4 门禁与实测

- 定向：`go vet ./cmd/aicli/commands/` OK；
  `go test -count=1 -v -run 'TestEventLogStartedFast|TestEventLogReplayTrimABEquivalence|TestStarted'`
  → **全 PASS**（4 个 oracle 子用例 + 5 个 fail-closed 子用例 + A/B 门禁）；
- 全包（最终树，含本节微基准）：`ok … 151.406s`，`-v` 输出**无 `--- FAIL` 行**
  （日志留在 `$TEMP\trim-full-final.log`）⇒ 放行条件②对 L1.2 成立；
- 单行微基准 `BenchmarkEventLogStartedFastDecode`（4.7 KB/行 = §1 表 1 实测均值
  12.04 MB / 2,561 行；本机争用中，`-benchtime 2000x -count=3`）：

  | 子基准 | ns/op（3 次） | B/op | allocs/op |
  |---|---|---|---|
  | `full`（全量解码） | 54,380 / 52,929 / 54,994 | **24,240** | **381** |
  | `cheap`（紧结构体） | 45,777 / 51,469 / 43,266 | **648** | **21** |

  **分配量是硬数字**（三次完全一致）：**24,240 → 648 B/行（37×）**、
  **381 → 21 allocs/行（18×）** ⇒ 按 2,561 行折算，每次恢复少产生约 **60 MB** 垃圾。
  墙钟**不可当硬数字**：争用机器上三次波动 43–51 µs，两次独立测量给出的加速比在
  **1.2×–1.7×** 之间，折算到 2,561 行约 **20–80 ms**。
  → 口径：本节只主张"分配量大幅下降、墙钟有正收益"，**不主张**任何具体耗时数字。

#### 7.13.5 未做（登记以免被误读为已完成）

- **未知类型**的*归因*计数仍未实现 —— 但缺口比 §7.11-1 原表述窄得多（已更正）：
  "出现了没见过的类型"这个告警**本来就有**（编码器 `Stats.UnknownCount` → 两处 /debug），
  真正缺的只是"已知静默但未列入白名单"这一**机会成本**信号，且它需要 encoding 包的导出钩子，
  不在 L1.2 范围内；
- **端到端相位（P2/P9/P10）仍未取数**：前置条件（空闲内存 ≥ 6 GB、无其它 aicli 实例）
  依旧不满足（本轮 preflight：**2,281 MB / 8 个 aicli 实例**），A/B 复跑继续阻塞（§7.7-1）；
- 因此**不得**把 L1.1+L1.2 表述为"已改善恢复耗时"——只有单行级证据，端到端相位为空。
