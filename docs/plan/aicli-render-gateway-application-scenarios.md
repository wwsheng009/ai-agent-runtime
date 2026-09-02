# 中间渲染层（RenderOutputGateway）可应用场景方案

> 文档状态：已审查修订（2026-09-01）  
> 基于代码基线：`main` 2026-09-01（Phase 1-6 已交付）  
> 关联方案：`docs/plan/aicli-chat-render-intermediate-layer-design-plan.md`  
> 关联架构：`docs/architecture/aicli-chat-unified-renderer-architecture.md`  
> 主要范围：`backend/cmd/aicli/ui/render/output`、`backend/cmd/aicli/commands`、`backend/cmd/aicli/ui`

## 1. 背景与目的

`RenderOutputGateway` 中间渲染层已在 `aicli chat` 内部完成 Phase 0-6 的交付与门禁化。当前架构下：

- 交互输出经 `PhysicalSink→RenderOutputGateway` 提交，不再直写 `os.Stdout`；
- 每笔渲染批次有盖章的 `RenderBatch`、可验证的 `OutputReceipt/DeliveryRecord`；
- 支持 multi-target mirror（physical/capture/virtual/file/discard/fault）；
- 支持两阶段路由切换（`BeginReconfigure/Commit/Abort`）；
- 支持版本化回放（replay recorder/decoder + checksum fail-closed）；
- 支持 session 级隔离（`SessionBindingRef` + generation fence）。

这些能力在 `aicli chat` 内部已闭环验证。本文档的目的是**挖掘这套中间层在 aicli 产品内外的可应用场景**，为后续扩展提供方案参考。

---

## 2. 能力全景（已交付资产速览）

| 能力 | 实现位置 | 当前状态 |
| --- | --- | --- |
| 批次化交付 | `types.go`（RenderIntent/RenderBatch/OutputReceipt） | ✅ 已交付 |
| 单物理 owner | `physical_sink.go`（一次 batch 一次提交，禁止透明 retry） | ✅ 已交付 |
| 七类 sink | `physical_sink.go`、`capture_sink.go`、`virtual_terminal.go`、`file_sink.go`、`fault_sink.go`、`sink.go`（Discard、MemorySink） | ✅ 已交付 |
| 多目标 mirror | `mirror_scheduler.go`（outcome-aware 有界队列、per-mirror timeout） | ✅ 已交付 |
| 观测与审计 | `observer.go`、`parity.go`、`chat_debug_render_output_test.go`（/debug Render Output 节） | ✅ 已交付 |
| 版本化回放 | `replay.go`（ReplayEnvelope/ReplayProvenance、schema/checksum fail-closed） | ✅ 已交付 |
| 会话隔离 | `binding.go`（SessionBindingRef + generation fence + fencedPort） | ✅ 已交付 |
| 路由切换 | `state.go`（两阶段 barrier：Begin/Commit/Abort） | ✅ 已交付 |
| 确定性测试 | `testfixture_test.go`（RenderTestFixture）、`clock.go`（fake clock） | ✅ 已交付 |
| 安全/隐私 | `capture_sink.go`（metadata-only 默认、redaction、TTL、session-keyed hash） | ✅ 已交付 |
| 交付审计 | journal（DeliveryRecord） + snapshot 计数器 | ✅ 已交付 |
| 文件落盘 | `file_sink.go`（primary 或 mirror，SyncEveryWrite/SyncOnClose） | ✅ 已交付 |
| 控制面端口 | `gateway.go`（`RenderOutputPort`：Snapshot/WaitIdle/Drain/Close/BeginReconfigure/CommitReconfigure/AbortReconfigure） | ✅ 已交付 |
| 端口分离 | `binding.go`（`RenderSubmitPort` 只暴露 Submit；控制面不可达） | ✅ 已交付 |
| Abandoned 语义 | `state.go`（Open/Reconfiguring/Closing/Closed/Abandoned 状态机；阻塞 writer → Abandoned 安全退出） | ✅ 已交付 |
| 协议输出边界 | `commands/chat_command_text_writer.go`（Plain/JSON 非交互输出显式 writer，禁止隐式解析 os.Stdout） | ✅ 已交付 |

---

## 3. 场景分类

### A. 测试与质量保障（已复用，可扩展）

#### A1. 虚拟终端投影 golden 测试

**当前状态**：`VirtualTerminalSink` 已实现，`RenderTestFixture` 支持 physical/capture/virtual/discard/fault 组合。

**实施状态**：✅ **已实施**（2026-09-01）——`golden_projection_test.go` 6 个契约测试全过（frame/history/barrier/lease/resize/sequence）。双 golden 并存：wire 字节 golden（对比 `MemorySink.SnapshotBatches()`）+ 文本投影 golden（对比 `Projection().Rows`），全内联常量、无自动更新、变更需显式 review。关键接线经验：intent 必须显式携带 `RenderTerminalContext` geometry、断言前先 `Gateway.Drain()` 等齐异步 mirror、多帧 wire 需以 `\n` 开头避免行粘连。

**扩展方案**：将 unified/legacy 渲染器的主要 frame/history/lease/resize 测试固化为"不依赖 stdout replacement / pseudo-TTY"的契约测试。导出 `VirtualTerminalSink.Projection().Rows` 作为**可读文本 golden**，与 wire 字节 golden 并存。

**价值**：wire 字节对终端状态/尺寸/控制序列敏感，易碎；文本投影 golden 反映**用户实际看到的屏幕内容**，回归价值更高。两份 golden 互为补充。

**接线**：
- `RenderTestFixture` 提供 deterministic geometry/theme/profile/terminal epoch/history domain；
- 断言 `VirtualTerminalSink.Projection().Rows` 与 expected golden 一致；
- 使用 fake clock 替代固定 sleep。

**验收标准**：`TerminalSession` 核心测试（frame/history/lease/resize）中 ≥80% 改用 `VirtualTerminalSink.Projection().Rows` 断言；golden 变更必须显式 review，不允许自动更新静默通过。

#### A2. 故障注入与恢复演练

**当前状态**：`FaultSink` 已覆盖 zero/short/full+error/invalid count/block/cancel/timeout/abort。

**实施状态**：✅ **已实施**（2026-09-01）——`fault_matrix_test.go` 新增 `TestA2FaultInjectionMatrix`（7 类 fault 矩阵：none/reject/zero/partial/error_committed/panic + block 单独用例）与 `TestE2UnknownPartialRecovery`。矩阵中每个 `UnknownPartial` 用例（partial/error_committed/panic）都断言恢复批次 `BatchID` 为新值、`ParentBatchID`/`Cause` 正确指向原始批次、journal 中原始记录保持 non-committed；`DrainCalls()` 断言一次 Submit 恰好一次 sink 调用（无静默 retry）；`FaultBlock` 用例验证阻塞期间无额外调用、Release 后按 zero-proof 收敛、gateway 继续可用。`go test -race ./cmd/aicli/ui/render/output -count=1` 通过。

**扩展方案**：构建 `UnknownPartial` 恢复路径的故障注入测试矩阵：
- 当 `FaultSink` 返回 `UnknownPartial` 时，断言恢复使用**新 intent + 新 BatchID + parent/cause 追踪**；
- 断言**没有静默 retry**（计划 §6.2 强制要求）；
- 用 fake clock 验证超时/abandon 边界，替代固定 sleep。

**价值**：这是渲染层最关键的失败恢复语义——不静默 retry 是避免数据损坏的基石。矩阵化测试确保每次代码变更不退化。

**验收标准**：故障注入矩阵覆盖 zero/short/full+error/invalid/block/cancel/timeout/abort 全部类别；每个 `UnknownPartial` 用例断言恢复批次 `BatchID` 为新值、`ParentBatchID`/`Cause` 正确指向原始批次，且 journal 中无静默 retry 记录。

#### A3. 渲染器 parity 自动化

**当前状态**：`parity_test.go` 已做 physical+capture 双跑一致性。

**扩展方案**：同一批 `RenderIntent` 同时送 unified 与 legacy 两条渲染链（各挂独立 sink），断言**虚拟终端投影一致**，而不是只比 ANSI 字节。

**价值**：在统一渲染器全量替换 legacy 之前，parity 测试是"两套渲染器输出一致"的客观证据。

**验收标准**：unified 与 legacy 两条渲染链对同一批代表性 intent 的 `Projection().Rows` 完全一致；不一致时测试失败并输出差异 diff。

---

### B. 可观测性与诊断（生产价值高）

#### B1. 交付审计日志

**当前状态**：每笔 batch 的 `DeliveryRecord` 已封存入 journal，含 `{session, sequence, target class, committed/unknown/partial, bytes/hash}`。metadata-only 默认、TTL、redaction 已内置。

**应用场景**：将 journal 作为**不可变审计链**对外暴露：

- **合规**："这个 prompt/tool 输出确实显示在了用户的终端上"——primary receipt 是唯一物理事实；
- **事故追溯**：按 session + sequence 回放精确时刻的屏幕状态；
- **容量**：TTL 有界、redaction 处理敏感内容、payload 不落入 journal。

**接线建议**：
- 提供 `RenderOutputGateway.Snapshot()` 或 `JournalReader()` 导出入口；
- CLI 工具 `aicli debug render-output <session>` 查询最近 journal；
- 默认 metadata-only，full capture 需显式 opt-in。**注意**：gateway 不存在 `AttachCaptureSink` 方法——full capture sink 只能在**构造时**通过 `RenderRouteConfig` 配置，或运行时通过 `BeginReconfigure` + `SetPendingRoute` 两阶段切换（`gateway.go` 提供 `BeginReconfigure/CommitReconfigure/AbortReconfigure/SetPendingRoute` 控制面，见 `RenderOutputPort` 接口）。

**验收标准**：`aicli debug render-output <session>` 可导出最近 N 笔 `DeliveryRecord`（含 committed/unknown/partial 分类与 bytes/hash）；默认导出不含 payload，只含 metadata/hash；TTL 淘汰后记录不可再查。

#### B2. 会话录屏 + 离线回放

**当前状态**：`FileSink` 支持 primary/mirror 落盘（`--render-output-file` 已接线）；`ReplayEnvelopeFromArchive` 带 schema/checksum fail-closed 校验。

**实施状态**：✅ **已实施**（2026-09-01）——`aicli replay <file>` 子命令（`replay_command.go`，含 `--replay-verify` 只校验模式，校验失败返回非零退出码）+ archive 序列化层（`replay_archive.go`：容器 schema 版本 + entry 级 checksum fail-closed，原子写盘）。测试覆盖：`replay_command_test.go`（CLI 收口）、`replay_archive_test.go`（容器/校验链）、`replay_test.go`（回放语义）。

**应用场景**：用户报告渲染 bug 后：

1. 取回 `--render-output-file` 录制的文件；
2. `ReplayEnvelopeFromArchive` 校验 schema/checksum；
3. **不触达 console** 回放到 `VirtualTerminalSink`，复现 bug 到具体 batch；
4. 导出 virtual projection 的 `Rows` 作为回归 golden。

**关键保障**：
- replay 永不触达 physical（`ReplayAttemptedIntent` 明确 `NonAuthoritative`）；损坏/篡改文件拒绝执行。
- replay 时校验录制时的 geometry（terminal width/height）与当前 geometry 一致，不匹配时警告但允许继续（投影结果可能不同，用户需自行判断）；
- replay 按 `ReplayAttemptedIntent` 的 `NonAuthoritative` 标记禁止触达 physical/process writer。

**接线建议**：
- 增加 `aicli replay <file>` 命令，接收录屏文件 → 回放到 virtual terminal → 输出文本投影；
- 增加 `--replay-verify` 模式，只校验 checksum 和 schema，不执行回放。

**验收标准**：`aicli replay <file>` 可将录屏回放到 `VirtualTerminalSink` 并输出 `Rows` 文本投影；`--replay-verify` 对损坏/篡改文件返回非零退出码且不执行回放；replay 进程不打开任何 physical/process writer。

#### B3. `/debug` 实时诊断增强

**当前状态**：`chat_debug_render_output_test.go` 验证 `/debug Render Output` 节存在。

**实施状态**：✅ **已实施**（2026-09-01）——`chat_debug_document.go` 的 `appendChatDebugRenderOutputLines` 在既有 primary/mirror 指标基础上新增「Recent Deliveries:」节：调用 `TerminalSession.RecentDeliveries()` 拉取最近 N 笔（cap=8）封存 `DeliveryRecord` 摘要，逐行展示 `record/seq/batch/kind/primary/payload mode/bytes/hash`。`gateway.go` 的 delivery journal 由 `metadata_only` 升级为 `hash_only`（`SanitizedBatch(..., RecordedHashOnly, defaultKeyedHash)`），journal 仍不保留明文 bytes，但 `BytesHash` 填充 SHA-256 摘要，`/debug` 据此以 hash 呈现 payload。`TerminalSession` 新增 `RecentDeliveries(limit)` 委托 gateway。`chat_debug_render_output_test.go` 新增 `TestDebugRenderOutputRecentDeliveries`（3 笔含敏感明文 payload，断言 `Recent Deliveries:`/`payload=hash_only`/`primary=committed`/`kind=frame` 出现且明文绝不泄漏）与 `TestDebugRenderOutputRecentDeliveriesCap`（12 笔只展示 cap 条数、明文不出现）。全量 `./cmd/...` 25 包与 `./internal/...` 回归通过。

**扩展方案**：在 `/debug` 页增加：
- 每 session 的 `RenderOutputGateway.Snapshot()`（pending/in-flight/sealed counters）；
- 最近 N 笔 `DeliveryRecord` 摘要；
- mirror 队列状态（high-water/drop/timeout）；
- 敏感信息 hash 替代 payload。

**价值**：生产排查时，不依赖日志就能看到"当前渲染层是否健康"。

**验收标准**：`/debug Render Output` 节展示每 session 的 pending/in-flight/sealed 计数、最近 N 笔 DeliveryRecord 摘要、mirror 队列 high-water/drop/timeout；所有 payload 以 hash 呈现，无明文敏感内容。

#### B4. stdout/stderr 边界验证

**当前状态**：交互模式的所有 terminal effects 走 stdout（经 gateway）；诊断/日志/panic 走 stderr（不经 gateway）。原设计计划 §8.5 强制要求"stderr 不包含渲染字节"。

**实施状态**：✅ **已实施**（2026-09-01）——`chat_stderr_stdout_boundary_test.go` 4 个集成测试全过：① 交互错误 stderr 与渲染字节零交集；② fence 错误渲染到 stdout 代理而非 stderr；③ 管道/NoInteractive 模式 stdout 不含 ANSI/诊断前缀；④ 正常轮次 stderr 无渲染泄漏。

**应用场景**：管道/嵌入方（CI 日志、IDE 集成、命令替换）消费 aicli 输出时，必须保证：
- stderr 只含诊断（log/panic/错误提示），不含任何渲染字节；
- stdout 只有经 gateway 盖章的渲染输出或 `CommandTextWriter` 的协议输出；
- 第三方包装器可以安全地把 stderr 当诊断通道、把 stdout 当数据通道。

**价值**：这是"可被程序安全消费"的基础契约——渲染字节与诊断字节若不分离，任何管道消费者都会收到污染的 payload。

**接线建议**：
- 增加集成测试：在 interactive session 中触发已知错误，断言 stderr 字节集合与渲染输出零交集；
- 将 §8.5 边界纳入 inventory 静态审计范围（stderr 直写点同样必须登记）。

**验收标准**：在 interactive session 中触发已知错误（如工具失败、panic 路径），断言 stderr 字节集合与渲染输出零交集；管道模式（`|` 重定向）消费 stdout 时不含任何诊断字节。

#### B5. 协议输出边界（CommandTextWriter）

**当前状态**：`commands/chat_command_text_writer.go` 是 structured command 协议输出的显式类型边界。`Plain/JSON/noninteractive` 输出**不走 gateway**，通过 `NewCommandTextWriter(os.Stdout, mode)` 直接写 stdout；interactive owned 输出经 `RenderCommandDocument` 提交到 gateway，两路**互斥路由**。`internal/acp` 的 ACP/stdio 协议由独立模块管理，也不在此边界内。

**应用场景**：
- 非交互模式（`aicli chat -p`、JSON/plain 投影）需要稳定的协议输出——不经过 gateway 的批次化/镜像/回放，避免引入非协议字节；
- 协议输出不允许隐式回落 `os.Stdout`（`NewCommandTextWriter` 对 nil writer fail-fast），调用方必须显式声明目标；
- 测试/嵌入方可注入自定义 writer，验证协议输出与交互渲染字节不串扰。

**价值**：把"JSON/plain 协议分离"从分支约定升级为类型契约，杜绝渲染代码隐式解析 `os.Stdout` 的旁路。

**接线建议**：
- 为每个非交互命令的协议输出建立 golden 断言（Plain/JSON 各一组）；
- 增加静态检查：协议路径只允许经 `NewCommandTextWriter`/`NewStdoutCommandTextWriter` 写 stdout，禁止直接 `os.Stdout.Write`。

**验收标准**：每个非交互命令的 Plain/JSON 输出各有 golden 断言；对 nil writer 调用 `NewCommandTextWriter` 返回错误（fail-fast）；协议输出与交互渲染字节无串扰（并发场景断言无交叉）。

---

### C. 多目标分发（mirror 架构的核心价值）

#### C1. Console + 文件 + 远程 viewer 三路并行

**当前状态**：`MirrorScheduler` 支持多 target，`FileSink` 已实现。

**应用场景**：同一批渲染字节同时送达：
- **primary** = console（物理投影事实）；
- **mirror 1** = FileSink（录屏/审计，开源码）；
- **mirror 2** = WebSocket/SSH 转发（远程观看、共享大屏、会议投屏）。

**关键保障**：
- mirror 失败只进诊断，不伪造物理失败；
- mirror 慢/挂死只影响自身队列（有界、drop、timeout），physical primary receipt 不受影响；
- mirror 不创建第二物理 writer。

**接线建议**：
- 实现 `RenderMirrorSink` 的 WebSocket/pipe 版本；
- 运行时 mirror 通过 `BeginReconfigure` + `SetPendingRoute` 两阶段切换——gateway 不存在 `AttachMirror` 方法；mirror 只能在构造时通过 `RenderRouteConfig` 配置，或运行时通过 reconfiguration 变更。

**验收标准**：同一批渲染字节可同时到达 primary + FileSink + WebSocket/pipe 三路；任一路 mirror 失败/挂死不影响 primary receipt；mirror 不创建第二物理 writer（inventory 无新增 stdout 直写点）。

#### C2. 慢消费者隔离

**当前状态**：mirror 队列有界，`MirrorScheduler` 在 admission 时决定 enqueue/drop，不等待 mirror I/O。

**应用场景**：第三方 viewer 慢/挂死不阻塞 console 写入。即使 mirror 队列满，physical primary 的 `Committed/Full` receipt 不受影响；drop 计数进入 snapshot 可观测。

**价值**：这是"附加上消费者"的正确方式——不强制生产者承担消费者速度。

**验收标准**：集成测试中挂死 mirror 时，physical primary 的 `Committed/Full` receipt 及时返回；`queue-full → drop` 不阻塞 primary；drop 计数在 snapshot 可观测。

---

### D. 安全与隐私（已内置，可推广）

#### D1. 敏感内容防护默认策略

**当前状态**：production 默认 metadata/hash-only；full capture 需 session-scoped opt-in + TTL；redaction + session-keyed hash 已实现。

**应用场景**：任何需要"既要诊断能力、又不准泄露数据"的场景可直接复用：
- secret prompt 值永不进入 intent/capture（`ReadTransientSecretPrompt` 不使用 gateway）；
- journal 不含任意 error object 或默认 full payload；
- payload store 只能通过显式 handle 访问，过期/越权/截断 fail closed。

**推广**：这可以作为**终端渲染层的隐私红线**——谁要开启 full capture 必须显式、有 TTL、有权限。

**验收标准**：production 默认 journal 不含 payload 明文；full capture 必须显式 opt-in + session-scoped TTL；redaction 后敏感值不落入 journal/capture；越权/过期访问 fail closed。

#### D2. 不可信回放文件 fail-closed

**当前状态**：`ReplayEnvelopeFromArchive` 按顺序校验 schema major / minor、payload bound、checksum。任何失败返回 `ReplayValidationError`。

**应用场景**：处理外部用户提供的录屏文件、跨团队共享的故障归档：
- 无需信任文件来源，校验链条保证解码安全；
- 跳过完整校验的可选 `--force` 模式必须显式确认。

**验收标准**：对 schema major/minor 不匹配、payload 越界、checksum 不符的文件均返回 `ReplayValidationError` 且不执行回放；`--force` 仅在显式确认后生效并记录警告。

---

### E. 会话生命周期与一致性（架构基础）

#### E1. 迟到 goroutine 隔离

**当前状态**：`SessionBindingRef` + generation fence 保证 unbind/close 后 `Submit` 返回 pre-admission error。

**实施状态**：✅ **已实施**（2026-09-01）——`binding_fence_test.go` 新增 6 个测试覆盖完整 E1 验收矩阵：`TestE1UnbindRejectsOldFacadeWithoutTouchingPort`（unbind 后旧 facade 返回 pre-admission rejected + `Primary=nil`/`Sequence=0` + 底层 port 零触达）；`TestE1RebindOldGenerationRejectedWithPrimaryNil`（rebind 后旧 generation 被 fence + 新 generation 递增 + 旧 facade 拒绝）；`TestE1UnbindFenceAllRejectsEveryFacade`（shutdown 时所有 session 旧 facade 全部被 fence）；`TestE1UnbindOneSessionKeepsOthersActive`（session 隔离性：unbind A 不影响 B）；`TestE1LateBytesNeverReachNextSessionGateway`（端到端：真实 gateway + MemorySink，迟到字节不进入旧 session sink 或新 session gateway sink）；`TestE1RebindLateSubmitIsPreAdmissionRejected`（既有线性化测试补强：rebind 后 old facade 提交完整满足 pre-admission 契约）。

**应用场景**：多 session 并行、频繁 resume/load 的场景下，旧会话的迟到渲染字节绝不串入新会话。具体的：
- 上一个会话的异步 spinner 在 unbind 后写 gateway → 得到稳定 rejection，不写入下一会话或 process compatibility writer；
- 旧 binding 的 `fencedPort.Submit` 在校验 generation 时发现不匹配，返回 pre-admission `Primary=nil, Sequence=0`。

**接线建议**：已作为 `chat_setup.go` 的默认行为，无需额外接线。新建 session 时自动绑定新 gateway、新 generation。

**验收标准**：unbind/close 后旧 binding 的 `fencedPort.Submit` 返回 pre-admission error（`Primary=nil, Sequence=0`）；迟到渲染字节不写入新 session 或 process compatibility writer。

#### E2. Unknown Partial 恢复语义

**当前状态**：`PhysicalSink` 返回 `UnknownPartial` 时，gateway 不自动 retry。恢复是**新 intent + 新 BatchID + parent/cause 追踪**。

**实施状态**：✅ **已实施**（2026-09-01）——`fault_matrix_test.go` 的 `TestE2UnknownPartialRecovery` 完整演练恢复链：原始批次 `UnknownPartial` 且 `DrainCalls()==1`（无静默 retry）；恢复批次 `BatchID` 为新值、`ParentBatchID`/`Cause` 指向原始批次、`TargetClass` 仍为 `Physical` 不降级；原始 partial 的 bytes 不进入 `Committed`；journal 可完整还原 `orig → recov → 独立后续批次` 三段链。与 A2 矩阵（partial/error_committed/panic 三个 UnknownPartial 入口）共用 `assertRecoveryChain` 辅助。

**应用场景**：终端输出被中断/部分写入后，恢复过程可追溯：
- 在 journal 中通过 `ParentBatchID` / `Cause` 追踪原始 partial 批次；
- 原始 partial 的 bytes 不进入 `Committed` 事实；
- 新 batch 的 `TargetClass` 仍然是 `Physical`，不降级。

**验收标准**：`UnknownPartial` 后恢复批次 `BatchID` 为新值、`ParentBatchID`/`Cause` 指向原始批次；原始 partial 的 bytes 不进入 `Committed`；journal 可完整还原恢复链。

#### E3. 运行时路由切换

**当前状态**：`BeginReconfigure/CommitReconfigure/AbortReconfigure` 两阶段 barrier 已实现，`ReconfigureTimeout` 已定义。

**应用场景**：
- 从 console 切换到 capture/virtual 模式（进入诊断），不丧失性能；
- 从 capture/virtual 切回 physical（触发 source-backed 完整恢复绘制）；
- 切换同一物理目标不复用旧 history proof、不重复 native scrollback。

**当前限制**：§9.5 定义 active alternate lease 第一版明确拒绝切换。生产只配置过 stdout primary。

**验收标准**：`BeginReconfigure → SetPendingRoute → CommitReconfigure` 切换后，新 route 生效且旧 route 干净收尾；`AbortReconfigure` 回滚后旧 route 继续工作，token 不复用；切换期间提交得到 `DeliveryErrorReconfiguring`。

#### E4. 阻塞 Writer 关闭与 Abandoned 安全退出

**当前状态**：gateway 状态机含 `Abandoned` 终态（`state.go`）：Open/Reconfiguring/Closing/Closed/Abandoned。`Close` 超时或 primary callback 终止无法证明时，gateway 自动固定为 `Abandoned`（`finalizeAbandonedPrimaryAttempts` + `completeAbandonedPrimaryAttempt`）；被放弃的排队批次以 `DeliveryErrorAbandoned` 快速收敛，不执行 sink；`AbandonedReason` 进入快照可观测。`Close` 超时返回 `DeliveryErrorTimeout`，无法证明 primary 终止时返回 `DeliveryErrorAbandoned`。

**实施状态**：✅ **已实施**（2026-09-01）——`observer.go` 的 `RenderOutputSnapshot` 新增 `AbandonedReason` 字段；`gateway.go` 的 `Close` deviate 路径与 `reconfigureTimeoutHandler` 填充 `abandonReason`（`"primary callback termination could not be proven after close deadline"` 或 `"reconfigure operation deadline exceeded after plan handoff"`），`Snapshot()` 暴露该字段；`fault_matrix_test.go` 新增 `TestE4BlockedWriterAbandonedSafeExit` 用 `FaultSink`+`FaultBlock` 模拟永不返回的阻塞 writer，断言 `Close` 超时后返回 `DeliveryErrorAbandoned`、gateway 进入 `GatewayAbandoned` 且快照 `AbandonedReason` 非空、排队批次得到 `DeliveryErrorAbandoned` 且不执行 sink（`DrainCalls==1`）。`go test -race ./cmd/aicli/ui/render/output -count=1` 通过。

**应用场景**：
- 物理 sink 的 writer 阻塞（如 PTY 挂死、管道消费者退出但 fd 未关）时，`Close` 不能无限等待——`Abandoned` 提供"放弃 + 记录已提交事务"的安全退出路径；
- 会话强制终止（用户 Ctrl-C 两次、调试器中断）后，journal 中仍保留到 cutoff 为止的已接受记录，晚到的 primary callback 只发晚期诊断，不伪造 `Committed`；
- 上层可以区分"正常关闭"（`Closed`）与"异常放弃"（`Abandoned` + `AbandonedReason`），决定是否告警/重试。

**价值**：这是"物理事实唯一性"在异常路径上的兜底——即使 writer 挂死，也绝不静默伪造提交，且放弃原因可审计。

**接线建议**：
- 故障注入矩阵扩展：模拟阻塞 writer（永不返回的 `io.Writer`），断言 `Close` 在超时后进入 `Abandoned`、`AbandonedReason` 非空、排队批次得到 `DeliveryErrorAbandoned`；
- `/debug` 页面展示 gateway 状态与 `AbandonedReason`，异常放弃可告警。

**验收标准**：阻塞 writer 场景下 `Close` 在超时后返回 `DeliveryErrorTimeout` 或 `DeliveryErrorAbandoned`；gateway 进入 `Abandoned` 且 `AbandonedReason` 非空；排队批次得到 `DeliveryErrorAbandoned`，不执行 sink、不伪造 `Committed`。

---

### F. 多代理协作（面向未来）

#### F1. 多代理共享 UI 的原子批次

**当前状态**：gateway 是 session 串行边界，每个 `Submit` 产生独立 `RenderBatch`。端口分离已就绪：`RenderSubmitPort` 只暴露 `Submit`；`RenderOutputPort` 才含控制面（Snapshot/WaitIdle/Drain/Close/BeginReconfigure/CommitReconfigure/AbortReconfigure）。`binding.go` 的 `SessionBindingRegistry.Bind` 只返回 `RenderSubmitPort` 的不可变 facade。

**应用场景**：多个 agent（或工具、后台任务）并发写同一终端 UI 时：
- 每个 producer 提交 `RenderIntent`，gateway 按提交顺序串行化，保证**批次原子性**；
- 两笔 intent 的字节不会在同一批次中交错；
- 每笔有 `OutputReceipt` 可追踪谁提交了什么、交付状态如何。

**权限模型**：
- 只有 **owner**（持有 `RenderOutputPort` 控制面）能切换路由/关闭/排空；
- 非 owner 代理只拿到 `RenderSubmitPort`，只能提交批次，**不能**改路由、不能关闭网关；
- 这保证多代理场景中"谁能改路由"有明确边界，不会出现某个后台代理悄悄把输出切走。

**价值**：这是"多 agent 协作 UI"的事务级基础设施，不只靠"加锁写 stdout"。

**验收标准**：多 producer 并发提交时批次原子（无交错）；每笔有独立 `OutputReceipt` 可溯源；非 owner 代理只拿到 `RenderSubmitPort`，调用控制面（reconfigure/close）编译失败或运行时被拒。

#### F2. 多 session 并行隔离

**当前状态**：每个 chat session 一个 gateway，`SessionBindingRef` 生成独立 `{SessionID, BindingGeneration}`。

**应用场景**：团队协作场景中，多个 session 同时运行（每个 session 有独立渲染上下文），中间层保证：
- 各自的 projection/history domain 不交叉；
- 各自的 `RenderReceipt` 不混淆；
- 各自的 capture/journal 有界隔离。

**验收标准**：并行 session 的 projection/history 互不交叉；各自 `RenderReceipt` 的 session/sequence 不混淆；各自 capture/journal 容量独立有界，不互相挤占。

---

### G. 平台与后端抽象（v2 方向）

#### G1. 可替换 Primary Sink

**当前状态**：非 stdout primary 属 v2 能力（架构文档 §16.1 声明）。

**应用场景**：
- **PTY 终端**：`PhysicalSink` 包装 PTY fd，aicli 通过 SSH 或 tmux 连接；
- **WebSocket 终端**：Web 终端的 primary sink，渲染逻辑在服务端，渲染字节推送到浏览器；
- **文件 primary**：headless 录制，事后通过 virtual terminal 回放查看。

**价值**：**渲染逻辑与输出目标完全解耦**。新平台 bring-up 只需实现一个 `RenderOutputSink`，全部渲染逻辑、测试、审计、回放能力复用。

**验收标准**：为 PTY/WebSocket/文件各实现一个 `RenderOutputSink` 后，同一套渲染逻辑/测试/审计/回放无改动通过；替代 sink 均满足"一次 batch 一次提交"约束。

#### G2. 平台差异封装

**当前状态**：Windows/Unix 终端差异被封装在 `PhysicalSink` 和 `ui/terminal.go` 中，渲染逻辑层不感知平台。Win7 不支持 VT 序列，`PhysicalSink` 需 fallback 到 Win32 API（`platformEnsureConsoleUTF8Output(os.Stdout)`）。

**应用场景**：新平台（如 Windows 非 ANSI 模式、SSH 远程终端）只需替换 `PhysicalSink` 实现，所有 unified/legacy 渲染器、测试、回放、观测不变。Win7/legacy console 场景需验证：
- VT 序列不适用时，`PhysicalSink` 的 Win32 API fallback 不丢批次、不伪造 `Committed`；
- `PhysicalSink` 的 fallback 路径同样满足"一次 batch 一次提交、禁止透明 retry"约束；
- Windows legacy console 的 smoke 测试纳入发布门禁（CI 增加 Windows 作业）。

**验收标准**：Windows（含 legacy console/Win7）作业跑通 render output smoke：渲染字节正确落盘/显示、批次不丢、`Committed` 不被伪造；fallback 路径不违反"禁止透明 retry"。

#### G3. 进程级兼容 Fallback 边界

**当前状态**：gateway 构造/初始化失败时当前实现是 **fail-closed**（终止会话，不回退直写 stdout）。但启动前、非交互、初始化失败等场景仍存在"无 gateway 时的进程级输出"路径（`TerminalOutput()` 兼容投影）。

**应用场景**：
- 启动早期（gateway 尚未就绪）或构造失败时，进程级兼容 fallback 提供最小可用的 stdout 投影，保证程序不静默吞输出；
- 该 fallback 是**显式声明**的降级路径，不是隐式回落——与 `CommandTextWriter` 的 fail-fast 契约一致（nil writer 拒绝隐式回落）；
- 诊断信息仍走 stderr，fallback 只覆盖 stdout 数据通道。

**价值**：明确"gateway 与进程级 fallback 的边界"，避免实现者误把 fallback 当正常路径、把正常路径当 fallback。

**接线建议**：
- 文档化 fallback 触发条件（构造失败/启动前/非交互）；
- 增加测试：gateway 构造失败时，fallback 输出仍满足 §8.5 stdout/stderr 边界，且不产生第二物理 writer。

**验收标准**：gateway 构造失败/启动前路径输出仍满足 stdout/stderr 边界；fallback 为显式声明降级（不静默隐式回落）；无第二物理 writer（inventory 校验通过）。

---

### H. CI/发布质量门禁

#### H1. 终端效果静态审计

**当前状态**：inventory 已有 1203 条记录覆盖 90 个源文件，`release-aicli.yml` 的 `Verify terminal output inventory` step 强制指纹比对。

**实施状态**：✅ **已实施**（此前轮次）——`tools/terminal-inventory`（`main.go`）扫描终端直写点生成 `terminal-output-inventory.json`；CI verify step（`go run ./tools/terminal-inventory -verify`）强制指纹比对，新增未登记直写点或删除已登记点均 fail。

**应用场景**：任何新增/删除/修改终端直写点，发布前必须通过 inventory 比对：
- 新增直写点未登记 → fail，避免第二 physical writer 悄悄引入；
- 已有直写点被删除但仍在 inventory baseline → fail，避免误删。

**验收标准**：新增未登记直写点 → `Verify terminal output inventory` 步骤 fail；删除已登记直写点 → fail；inventory 指纹比对在 `release-aicli.yml` 中强制执行。

#### H2. 性能门禁

**当前状态**：Phase 4 固化 benchmark baseline（ns/op、allocs/op、retained bytes）。

**应用场景**：CI 中 `go test -bench=. ./ui/render/output/` 与 baseline 比较，超过阈值（+5% / +5µs）阻断发布。

**验收标准**：CI 集成 benchmark 对比步骤；任一指标超过阈值（+5% / +5µs）阻断发布；阈值变更需显式 review。

---

## 4. 场景优先级矩阵

| 优先级 | 编号 | 场景 | 依赖 | 预估工作量 | 价值 |
| --- | --- | --- | --- | --- | --- |
| **P0** | A1 | 虚拟投影 golden 测试 | 已有 `VirtualTerminalSink` + `RenderTestFixture` | ✅ 已交付（golden_projection_test.go） | 回归质量提升，wire 和文本双保险 |
| **P0** | B2 | 会话录屏 + 离线回放命令 | `FileSink` + `replay.go` 已就绪，缺 CLI 收口 | ✅ 已交付（aicli replay + --replay-verify） | 用户反馈复现、安全跨进程回放 |
| **P0** | B4 | stdout/stderr 边界验证 | 已有边界约定（§8.5），缺集成测试 | ✅ 已交付（chat_stderr_stdout_boundary_test.go） | 管道消费安全基础契约 |
| **P0** | H1 | inventory 静态审计门禁 | 已有 baseline + verify step | ✅ 已交付（terminal-inventory 工具 + CI 门禁） | 防止新增旁路点的最后防线 |
| **P1** | A2 | 故障注入与恢复矩阵 | `FaultSink` + `clock` 已就绪，扩展测试 | ✅ 已交付（fault_matrix_test.go） | 不静默 retry 的基石保障 |
| **P1** | A3 | 渲染器 parity 自动化 | `parity_test.go` 已有，扩展覆盖范围 | 中（1w） | 统一渲染器替换的安全网 |
| **P1** | B1 | 交付审计日志导出 | journal 已封存，缺导出入口 | 中（1w） | 合规与事故追溯 |
| **P1** | B3 | /debug 实时诊断增强 | observer + Snapshot 已就绪，缺页面 | ✅ 已交付（chat_debug_render_output_test.go，DeliveryRecord 摘要 + hash_only journal） | 生产排查不求人 |
| **P1** | B5 | 协议输出边界验证 | `CommandTextWriter` 已就绪，缺 golden 断言 | 小（3-5d） | 非交互输出与交互渲染不串扰 |
| **P1** | C1 | 远程 viewer mirror | 新增 `RenderMirrorSink` 实现 | 中（1-2w） | 多终端投射、协作调试 |
| **P1** | C2 | 慢消费者隔离验证 | mirror 有界队列已实现，缺集成测试 | 小（1w） | 生产者不背消费者速度 |
| **P1** | D1 | 敏感内容防护策略文档化 | capture_sink redaction/TTL/hash 已实现 | 小（3-5d） | 终端渲染层隐私红线 |
| **P1** | E1 | 迟到 goroutine 隔离验证 | `SessionBindingRef` + `fencedPort` 已实现 | ✅ 已交付（binding_fence_test.go，6 测试矩阵） | 多 session 安全隔离 |
| **P1** | E2 | Unknown Partial 恢复测试 | `PhysicalSink` + journal ParentBatchID 已就绪 | ✅ 已交付（fault_matrix_test.go） | 中间层最关键的失败恢复语义 |
| **P1** | E3 | 运行时路由切换产品化 | 两阶段 barrier 已实现，需生产验证 | 中（1w） | 诊断/调试模式切换 |
| **P1** | E4 | 阻塞 Writer 关闭与 Abandoned | Abandoned 状态机已实现，缺故障注入测试 | ✅ 已交付（fault_matrix_test.go + AbandonedReason 快照） | 物理事实唯一性在异常路径的兜底 |
| **P2** | D2 | 不可信回放文件 fail-closed 强化 | `ReplayEnvelopeFromArchive` 校验链已就绪 | 小（1w） | 外部故障文件安全处理 |
| **P2** | F1 | 多代理共享 UI 原子批次 | 基础设施已完备，需新 producer 接入 | 中（2w） | 多 agent 协作基础 |
| **P2** | F2 | 多 session 并行隔离验证 | 每个 session 独立 gateway，已就绪 | 小（1w） | 团队协作场景 |
| **P2** | G1 | 非 stdout primary sink | 架构文档 v2，需真实替代 sink | 大（2-4w） | 平台扩展（PTY/Web/远端） |
| **P2** | G2 | 平台差异封装（Win7 等） | `PhysicalSink` 封装已就绪，缺 Windows smoke | 中（1-2w） | 跨平台交付 |
| **P2** | G3 | 进程级兼容 Fallback 边界 | 启动前/非交互 fallback 已存在，需文档化 | 小（1w） | 明确 gateway 与 fallback 边界 |
| **P2** | H2 | 性能门禁 CI 集成 | benchmark baseline 已固化，缺 CI 对接 | 小（1w） | 性能退化不溜进发布 |

---

## 5. 实施建议

### 5.1 近期（P0，1-2 周内）

> ✅ **已全部交付**（2026-09-01）：
> 1. **录屏回放命令**：`aicli replay <file>`（`replay_command.go`），支持 `--replay-verify` 只校验模式；archive 层（`replay_archive.go`）容器 schema + entry checksum fail-closed。
> 2. **虚拟投影 golden**：`golden_projection_test.go` 为 frame/history/barrier/lease/resize/sequence 建立 wire + `Projection().Rows` 双 golden，全内联、无自动更新。
> 3. **stdout/stderr 边界验证**：`chat_stderr_stdout_boundary_test.go` 4 个集成测试（交互错误零交集/fence 走 stdout/管道模式无诊断/正常轮次无泄漏）。
> 4. **inventory 静态审计门禁**：`tools/terminal-inventory` + `release-aicli.yml` `Verify terminal output inventory` step 强制指纹比对。

**后续可扩展（P1+）**：

### 5.2 中期（P1，1-2 月）

3. **交付审计导出**：`RenderOutputGateway.Snapshot()` 的 journal 导出为 JSON 或 framed binary，供外部审计系统消费。
4. **远程 viewer mirror sink**：实现 WebSocket 或 pipe 版 `RenderMirrorSink`，验证 mirror 架构在真实远距离场景下的行为。

### 5.3 远期（P2，2-4 月）

5. **多代理协作 UI 原型**：基于 `RenderSubmitPort` 的 facade，构建多 agent 共享同一 session gateway 的 demo。
6. **非 stdout primary sink 验证**：选择一种真实替代 sink（PTY/文件/WebSocket），验证 reconfigure barrier 与 mirror 一致性。

---

## 6. 风险与缓解

| 风险 | 表现 | 缓解 |
| --- | --- | --- |
| journal 导出泄露敏感信息 | 审计日志包含 prompt/tool 参数 | 默认 metadata-only + redaction；full 导出需显式 opt-in 并验证 TTL |
| 远程 viewer mirror 阻塞 gateway | mirror 慢拖慢 physical primary | mirror 有界队列 + timeout 已实现；需在集成测试中验证 `queue-full → drop` 不阻塞 primary |
| 非 stdout primary 的 continuity 证明 | 重新连接后无法证明是同一终端 | §9.5 定义 `ProjectionTargetID` + `ContinuityID` 生成方式，需针对每个替代 sink 类型验证 |
| replay 成第二物理 writer | replay 意外触达 console | `ReplayTarget` 禁止 physical/process writer；`NonAuthoritative` 标记；校验/验证模式执行 |

---

## 7. 实施后收益（P0 场景已交付）

| 场景 | 关键产出 | 直接收益 | 量化效果 |
| --- | --- | --- | --- |
| **A1 虚拟投影 golden** | `golden_projection_test.go`（6 个契约测试） | 回归质量从"wire 字节单保险"升级为"wire + 文本投影双保险"；文本投影反映用户实际看到的屏幕内容，比 ANSI 控制序列更稳定、更可读 | 核心测试（frame/history/lease/resize）均以 `Projection().Rows` 断言，无需依赖 stdout replacement / pseudo-TTY；golden 变更显式 review，防止静默退化 |
| **B2 会话录屏 + 离线回放** | `aicli replay <file>` 子命令 + `--replay-verify` 模式；archive 层（容器 schema + entry checksum fail-closed） | 用户报告渲染 bug 后，取回录屏文件 → 回放到 `VirtualTerminalSink` → 复现到具体 batch → 导出 golden 作为回归基线；`--replay-verify` 确保损坏/篡改文件 fail-closed | 回放永不触达 console（`NonAuthoritative` 标记），安全跨进程；校验失败返回非零退出码，不执行回放 |
| **B4 stdout/stderr 边界验证** | `chat_stderr_stdout_boundary_test.go`（4 个集成测试） | 管道/嵌入方可安全消费 stdout 作为数据通道、stderr 作为诊断通道——渲染字节与诊断字节零交叉 | 交互错误、fence 错误、管道模式、正常轮次四类场景全部覆盖，可作为 CI 门禁防止回归 |
| **H1 inventory 静态审计门禁** | `tools/terminal-inventory` + `release-aicli.yml` `Verify terminal output inventory` step | 防止新增/删除终端直写点悄悄引入第二 physical writer 或误删必要直写点 | 1203 条记录覆盖 90 个源文件，每次发布前强制指纹比对；新增未登记点或删除已登记点均 fail |

### 7.1 间接收益

1. **实施中发现的架构缺陷已修复**：A1 实现中暴露了 `RenderIntent.Terminal` 必须显式设置 geometry 的约束（原 fixture 默认不填），以及异步 mirror 断言前必须 `Drain()` 的时序契约——这些发现使渲染层接线契约更加明确。
2. **测试基础设施可复用**：`golden_projection_test.go` 的 `goldenIntent()`/`drainGolden()` 辅助函数可直接用于其他场景（A2 故障注入、A3 parity、E2 Unknown Partial 恢复）的测试。
3. **CI 门禁链完整**：P0 四场景的测试已全部集成到 CI 流程（`go test` + `Verify terminal output inventory`），确保 P0 契约不会因后续代码变更退化。

---

## 8. 附录：场景与能力映射矩阵

| 场景 | 依赖的核心能力 | 新接线量 |
| --- | --- | --- |
| A1 虚拟投影 golden | VirtualTerminalSink, RenderTestFixture, Clock | 测试断言，无新代码 |
| A2 故障注入矩阵 | FaultSink, clock, testfixture | 测试用例，无新代码 |
| A3 渲染器 parity | parity_test.go, VirtualTerminalSink | 测试用例扩展 |
| B1 交付审计日志 | journal, DeliveryRecord, Snapshot | 导出 CLI 入口 |
| B2 会话录屏回放 | FileSink, ReplayEnvelopeFromArchive | CLI `replay` 子命令 |
| B3 /debug 增强 | observer, Snapshot, RecentDeliveries | ✅ 已实施（chat_debug_render_output_test.go，DeliveryRecord 摘要 + hash_only journal） |
| B4 stdout/stderr 边界 | 管道/嵌入方输出契约（§8.5） | 集成测试 |
| B5 协议输出边界 | CommandTextWriter, NewCommandTextWriter | golden 断言 + 静态检查 |
| C1 远程 viewer mirror | MirrorScheduler, RenderMirrorSink | 新 `RenderMirrorSink` 实现 |
| C2 慢消费者隔离 | MirrorScheduler 有界队列 + timeout | 已就绪，集成测试 |
| D1 敏感内容防护 | capture_sink redaction/TTL/hash | 策略文档化 |
| D2 不可信回放 fail-closed | ReplayEnvelopeFromArchive 校验链 | 已就绪，强化测试 |
| E1 迟到 goroutine 隔离 | SessionBindingRef, fencedPort | ✅ 已实施（binding_fence_test.go） |
| E2 Unknown Partial 恢复 | PhysicalSink, journal ParentBatchID | ✅ 已实施（fault_matrix_test.go） |
| E3 运行时路由切换 | state.go Begin/Commit/Abort | 生产接线验证 |
| E4 Abandoned 安全退出 | Abandoned 状态机, finalizeAbandonedPrimaryAttempts | ✅ 已实施（fault_matrix_test.go + AbandonedReason 快照） |
| F1 多代理 UI 原子批次 | RenderSubmitPort, gateway 串行化 | 新 producer 接入 |
| F2 多 session 并行隔离 | 每个 session 独立 gateway + BindingGeneration | 已就绪，无需新接线 |
| G1 可替换 primary sink | RenderOutputSink 接口 | 新 sink 实现 |
| G2 平台差异封装 | PhysicalSink, ui/terminal.go | Windows smoke 测试 |
| G3 进程级 fallback 边界 | TerminalOutput() 兼容投影 | 文档化 + 测试 |
| H1 静态审计门禁 | inventory tool, verify step | 维护成本 |
| H2 性能门禁 CI 集成 | benchmark baseline, go test -bench | CI 对接 |

---

> **文档状态**：初稿，2026-09-01  
> **下一步**：讨论优先实施的 P0 场景（录屏回放 CLI + 虚拟投影 golden），确认方案后进入实现阶段。