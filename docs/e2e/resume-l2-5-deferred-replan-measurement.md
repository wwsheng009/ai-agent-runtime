# L2.5 实测：补齐较早页引发的第二次全量规划 — 2026-09-25

> **结论先行**
>
> 1. `resume_history_deferred`（首帧之后补齐较早页）**确实会再触发一次全量规划**：
>    插入较早页改变了 finalized-prefix 指纹 → memo 失效 → 整个 transcript 被重新 screening
>    与铸 commit（结构证据见 §3，非推断）。
> 2. 这次规划在 6,720 cells / 161k 行的合成语料上是 **602 ms（3 次运行中位数，562–657 ms）**，
>    首次规划是 284 ms；**布局只占其中的 1–2%**（整段较早页 1.7–4.4 ms、全 transcript 热态 2.5 ms）。
>    成本在**规划器**（screening + 铸 commit，`plan_only` = 104 ms @4,001 cells）与
>    **reduce 的计划集成路径**（首次规划 284 ms − plan 104 ms − install 0.45 ms ≈ 180 ms）上。
> 3. 计划 §4.6 给出的两条改法**都不能交付其验收**：
>    - 「把更老页排除在指纹之外让 memo 命中」**不成立**：较早页必须真的进入计划、且必须排在
>      最新页之前（§3 的断言），memo 命中会让补回的历史永远到不了 scrollback；
>    - 「把多页合并成一次发布」**已经是现状**：`startDeferredResumeHistoryLoad` 一次取回全部
>      较早页、一次前插、一次重放。
>
> 因此 L2.5 的验收口径（「启动后 60s 内 P12 冻结窗口数 = 1」）**按原设计不可达**；
> 真正能省掉这次重算的是**增量 screening**（把 L2.2 的「前缀复用」从布局层上移到计划层），
> 这是一个比 L2.5 更大的新工作项。详见 §5、§6。
>
> 4. **附带发现（§8，可能比 L2.5 本身更重要）已经实施修复**：`UIControllerState.Clone()` 的成本
>    **99.7% 在 history ledger 深拷贝**（1,500 条目基线约 159–180 ms，而同状态 transcript 只要
>    0.27–0.48 ms）。`/debug/chat/status` 现在使用 ledger-free 的 `DiagnosticState()` 加
>    `HistoryEffectDiagnostics()`，`HistoryCommitExecutor` 也使用 payload-free projection；因此
>    这条随 ledger 增长的锁内深拷贝地板已从这两个生产调用点移除。P12 端到端仍未重测，不能据此
>    宣称 harness 已达标。

## 1. 为什么要在单进程里量

harness 的 P12 是这件事的**最终**验收，但它的前置条件是空闲物理内存 ≥ `MinFreeMemoryMb`
（默认 6,000 MB，fail-closed）。本轮测量期间本机空闲内存为 **1.7–2.7 GB**、另有 5–6 个
`aicli` 长驻实例，因此：

- harness 数据**取不到**（低于门槛，按脚本设计本次数据作废）；
- 即使强行用 `-AllowLowMemory` 跑，P12 的「窗口数」也会被换页噪声污染（M1 记录的教训：
  同相位数字在 135 ms 与 4035 ms 之间摆动）。

于是把问题搬进单进程：用**生产同一份** reduce/规划代码路径（`reduceUIControllerState` →
`syncHistoryEffectsForTranscriptWithin` → `planEligibleHistoryCommitsWithin`），
在固定语料上做可重复测量与结构断言。它不能替代 harness 的端到端结论，
但足以判定「这次重规划存不存在、贵在哪、能不能省」。

## 2. 仪器

`backend/cmd/aicli/ui/history_prepend_replan_bench_test.go`（新增）

语料：最新页 4,000 cells + 较早页 2,719 cells = **6,720 cells × 24 行 ≈ 161k 行**，
每 cell 都是 committed/finalized，尾部一个 mutable cell 充当 frontier barrier
（与生产 resume 安装的形态一致）。

| 子基准 | 量的是什么 |
|--------|-----------|
| `layout_only` | `Transcript.LayoutRows`（4,001 cells，热态）—— 语义布局 |
| `install_only` | `ReplaceTranscriptAction` 的安装/克隆（不给几何 → 规划器入口即返回） |
| `plan_only` | `planEligibleHistoryCommitsWithin`（4,001 cells，热布局）—— 纯规划 |
| `older_page_layout_only` | 只布局较早页 2,719 cells |
| `first_plan` | 整条 reduce（安装 + 规划 + 计划集成），最新页 |
| `second_plan_prepend` | 较早页插入头部后的整条 reduce —— **L2.5 的目标** |

结构断言：`TestDeferredOlderPagePrependReplansAndCoversOlderCells`。

```pwsh
# 结构断言（秒级）
$env:GOMAXPROCS='2'
go test -p 1 -gcflags=-c=1 ./cmd/aicli/ui/ -run TestDeferredOlderPagePrepend -count=2 -v
# 耗时（本机约 20 s）
go test -p 1 -gcflags=-c=1 ./cmd/aicli/ui/ -run '^$' -bench BenchmarkDeferredOlderPageReplan -benchtime 3x -count=3
```

> `-gcflags=-c=1` 与 `GOMAXPROCS=2` 不是可选项：本机内存紧张时 `go test` 编译 `ui` 包会
> 直接 `fatal error: runtime: cannot allocate memory`（本轮实测两次），这两个开关把它压下去。

## 3. 结构证据（不是推断）

同一次运行内的断言输出：

```
first plan: 1500 commits covering the latest page; memoHit=true
second plan: 2500 minted commits (1000 older / 1500 latest) in plan order
             (firstOlder=0 firstNewer=1000); ledger holds 2500 entries (1000 older / 1500 latest)
```

被测断言（全部通过）：

1. **首轮规划被 memo 记住**（`memoHit=true`）—— 否则两轮不可比。
2. **插入较早页改变了 finalized-prefix 指纹**（`transcriptFinalizedPrefixFence` 前后不等）
   → memo 必然失效。这是「第二次全量重规划」的**直接机制**。
3. **失效的结果是整份 transcript 被重新规划**：memo 记录的 cell 数 61 → 101（= 新 transcript 全量）。
4. **较早页真的进入计划**（1,000 个 commit 覆盖较早页），且**排在最新页之前**
   （`firstOlder=0 < firstNewer=1000`）。顺序断言的对象是**这一轮新铸的计划**，
   不是 ledger 的存量条目：ledger 会保留上一轮已铸的 token，交付顺序由计划顺序决定
   （销毁式重放整体替换 ledger）。

第 4 条同时是**对「排除更老页」方案的否决证据**：任何让这次 reduce 命中 memo 的改法，
都会让较早页的这 1,000 个 commit 不被铸造，补回的历史只停留在 Scene 里。

## 4. 耗时归因

`-benchtime 3x -count=3`，取中位数（本机有负载，单次值波动 ±30%，故列中位数）：

| 子基准 | 中位数 | 分配 | allocs/op | 占 `second_plan_prepend` |
|--------|--------|------|-----------|--------------------------|
| `install_only` | **0.45 ms** | 0.87 MB | 4.0k | 0.1% |
| `layout_only`（4,001 cells 热） | **2.5 ms** | 5.3 MB | 19.5k | 0.4% |
| `older_page_layout_only`（2,719 cells） | **1.7 ms** | 3.6 MB | 13.6k | 0.3% |
| `plan_only`（4,001 cells） | **104 ms** | 108 MB | 167.6k | — |
| `first_plan`（reduce，4,001 cells） | **284 ms** | 230 MB | 669.5k | — |
| `second_plan_prepend`（reduce，6,720 cells） | **602 ms** | 319–361 MB | 824k–1.15M | 100% |

读法：

- **布局不是成本**。整段较早页 1.7 ms、全 transcript 热态 2.5 ms（M2 记录冷态 43 ms/6,719 cells），
  相对 602 ms 可以忽略。计划 §4.3 的 L2.3（「给 `LayoutTranscript` 加预算采样点」）
  在 L2.2 之后**已不是主要矛盾**：布局最多贡献个位数毫秒，加采样点省不到东西。
- **成本在规划器**：`plan_only` 对 4,001 cells 是 104 ms，随**行数**增长（screening 逐行走
  161k 行）；第二次规划把它对全量 6,720 cells 重跑一遍。
- **reduce 自身还有一大段计划集成开销**：`first_plan` 284 ms − `plan_only` 104 ms −
  `install_only` 0.45 ms ≈ **180 ms**（reconcile/ledger/义务登记），与布局无关。
  第二次规划是 6,720 cells → 602 ms，相对首次的 4,001 cells → 284 ms 呈超线性
  （cells 比 1.68，耗时比 2.12），与「全量重 screening + 全量集成」一致。
- 语料保真度：生产样本是 6,714 cells / **300k 行**（本语料 161k 行）、且含富 presentation
  （markdown/chroma）。因此这里的毫秒数是**下界**，用于归因与比较，不用于宣称端到端达标。

## 5. 对计划 §4.6 的判定

| §4.6 的改法 | 判定 | 依据 |
|-------------|------|------|
| ① 把多页合并成一次发布 | **已是现状**，无收益 | `startDeferredResumeHistoryLoad` 一次 `fetchOlderResumeHistoryPagesWithRetry` 取回全部较早页 → 一次 `prependResumeHistoryPages` → 一次 `printVisibleSessionLoadHistory`（chat_session.go:692–716） |
| ② 把更老页排除在指纹之外让 memo 命中 | **不成立（正确性）** | §3 断言 4：较早页必须进入计划且排在最新页之前；memo 命中会跳过整个规划，1,000 个 commit 不会被铸造 |

验收口径本身也要改：P12 的预算是「单次 ≤2 s 且累计 ≤4 s」，而 L2.5 写的是
「冻结窗口数 = 1」。按本轮的 602 ms（生产约 2× → ~1.2 s）估计，**两个窗口也可能落在预算内**；
「窗口数 = 1」比 harness 的预算更严，不应作为 L2.5 的验收线，除非确实要做增量 screening。

## 6. 下一步（建议）

**增量 screening**（新工作项，暂称 L2.6）：把 L2.2 在布局层做成的「前缀复用」上移到计划层 ——
按 cell 身份/revision 缓存已 screening 的屏幕行，较早页插入时只 screening 新增前缀，
复用后缀。它直接消掉 602 ms 里的大头（`plan_only` 的全量重跑 + 集成路径的全量重做），
且**不触碰**销毁式重放/授权语义。

在此之前，L2.5 应记为「**经测量否决**」而不是「待做」，M2 的验收仍以 harness P12 为准
（需安静机器：空闲内存 ≥6 GB、无其它 `aicli` 实例）。

## 7. 本轮未验证 / 已知限制

- **harness P12 仍未取数**：本轮空闲内存 1.7–2.7 GB，低于脚本的 fail-closed 门槛
  （6,000 MB）。本文件的所有数字都是**单进程合成语料**口径，不得表述为 P12 已通过。
- 合成语料的 presentation 是 plain（无 markdown/chroma），行数约为生产的一半 ⇒ 绝对耗时是下界。
- 单次值波动 ±30%（本机负载），故一律取 3 次运行的中位数；跨机比较无意义，只看同机比值。
- `install_only` 用「不给几何」把规划器短路（`history_effect_planner.go:46`），
  它量到的是安装/克隆，不含 ledger 对账；后者被算在 §4 的「计划集成开销」里。

## 8. 附带发现：`State()` 的深拷贝地板与 ledger-free 修复

为了回答 §4.5 的前置问题（「reduce 移出锁时每次动作深拷贝状态有多贵」），本轮给仪器加了
`state_clone` / `clone_transcript_only` / `clone_history_effects_only` 三个子基准，随后又加入
`history_effects_diagnostics_projection` 验证 ledger-free 诊断投影。结果把问题指到了另一个方向，
并促成了两个生产调用点的修复：诊断端点不再复制 ledger，history executor 也不再通过完整
`State()` 读取单个 commit 的门控。

### 8.1 实测

| 子基准 | 观测区间（多轮） | 分配 | allocs/op |
|--------|------------------|------|-----------|
| `clone_transcript_only`（4,001 cells） | **0.27–0.48 ms** | 0.87 MB | 4.0k |
| `clone_history_effects_only`（1,500 ledger 条目） | **157–180 ms** | 96.5 MB | 497.6k |
| `state_clone`（4,001 cells + 1,500 条目） | **145–186 ms** | 97.4 MB | 501.6k |
| `state_clone_after_second_plan`（6,720 cells + 2,500 条目） | **305–345 ms** | 177.9 MB | 843.0k |

修复后的投影另做了一次验证性样本（同一 benchmark、同一 1,500 条目状态，
`-benchtime=1x`，不是稳定中位数）：

| 子基准 | 单次样本 | 分配 | allocs/op |
|--------|----------|------|-----------|
| `clone_history_effects_only` | **159.36 ms** | 96.54 MB | 497,600 |
| `history_effects_diagnostics_projection` | **6.14 ms** | 0 B | 0 |

区间 = 多轮运行的观测极值（同机 `-benchtime 3x`，含最后一次「10 个子基准放进同一轮」的复跑）；
分配与 allocs/op 在轮次间稳定（±2%），故列单值。新增的两行是单次验证样本，不能与上面的
多轮区间混读，也不应被表述为稳定中位数。**§8 的结论只依赖量级与线性关系，不依赖点值。**

即 **`UIControllerState.Clone()` 的成本 99.7% 在 ledger，不在 transcript**：
每条约 **120 µs / 64 KB**（`HistoryCommit.Lines []render.Line` 的深拷贝），随条目数线性增长。

### 8.2 为什么这是结构性的（代码证据）

- `UIControllerState.Clone()` → `AppState.Clone()` → `HistoryEffectQueueState.Clone()` →
  `ledger.Clone()`（`controller_state.go:27`、`app_state.go:47`、`history_effect_queue.go:128`）。
- `HistoryCommitLedger.Clone()` **逐条** `entry.Clone()` → `Commit.Clone()` → `cloneRenderLines`，
  注释明说「AppState snapshots must never retain actor-owned maps or render-line slices」
  （`history_commit.go:607-619`、`:82-85`）。
- `byToken` **没有淘汰**：全文件唯一的 `delete` 在 `byRange` 的 re-mint 路径（`history_commit.go:335-341`）。
  终态记录必须保留（否则 `hasTerminalRecordForSource` 会永久阻止重新铸造），
  所以 ledger 规模 = 本次会话**曾铸出的 commit 总数**，只增不减。

### 8.3 两个生产调用点（修复已落地，P12 尚未重测）

1. **`/debug/chat/status` —— 就是 P12 的探针**：
   `chat_debug_display_http.go:772` 现在调用 `DiagnosticState()`，并单独调用
   `HistoryEffectDiagnostics()`；前者通过 `controller.go:960-966` 在锁内复制
   transcript/scalar，但明确丢弃 commit ledger，后者在 `controller.go:974-981`
   下只做 ledger-free 的状态计数。harness 每 200 ms 打这个端点
   （`aicli-e2e-harness.ps1:83`、`debug-aicli-resume-status-latency.ps1:87`），且
   **`?fast=1` 仍不跳过 `app_state` 区块**（契约测试
   `chat_debug_bounded_contract_test.go:96-102` 要求 fast 快照仍输出它）。
   因此原先随 ledger 增长的 `c.state.Clone()` 地板已从探针路径移除；但 P12
   的冻结窗口、端到端延迟和总预算尚未重新取数。
2. **`HistoryCommitExecutor.runOne` —— 生产投递路径**：现在使用
   `PendingHistoryCommit()` 和 `historyCommitGateOf()`（`history_commit_executor.go:141-193`），
   只复制当前要投递的一个 commit payload，并以 payload-free gate 做 claim/ack 确认，
   不再每步通过 `State()` 深拷贝整个 ledger。该修复保留了 `State()` 供需要完整 entry
   的测试/调用方使用；`history_effect_queue.go:139-142` 的旧反模式注释仍是其动机记录。

### 8.4 未测 / 不可推断的部分

- **生产 ledger 的 live 条目数未测**。真实会话 artifact 只给计数
  （`acked=149327`、`next=299953`、`epoch=2`，见
  `artifacts/aicli-resume-startup-perf-e2e/20260924-234117/tmp-http-body.json`），
  不给 `len(byToken)`。因此「生产完整 `State()` 到底多少毫秒」**没有测到**，只能由
  §8.1 的线性系数（~120 µs/条目）推算，而条目数未知；本轮也没有把生产端点改造后的
  P12 端到端收益量出来。
- 也不能据此断言 P12 的全部冻结都由探针造成：L2.2 之前的 goroutine dump 明确显示
  `LayoutTranscript` 持锁（那是当时的真凶）。本节只能证明并移除了一个随 ledger 增长的
  克隆地板，不能替代 P12 重测或宣称累计预算已达标。
- 诊断投影仍需在 actor 锁内扫描 ledger 的状态计数（O(entries)，但 0 allocs），所以它
  不是把诊断变成 O(1)；它去掉的是逐 entry 的 map/slice/payload 深拷贝。若后续需要继续
  降低这段锁内时间，应另行缓存计数并增加一致性测试，不能把本轮单次 benchmark 外推成
  生产端到端结果。
