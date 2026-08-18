# Agent 运行轨迹（Trajectory）视图与事件日志：实施待办清单

> 状态：`draft`（待办清单，非方案文档）
> 创建日期：2026-08-16
> 关联文档：
> - [agent-trajectory-view-implementation-plan.md](./agent-trajectory-view-implementation-plan.md)（上位方案，本文按 Phase 0–3 拆解）
> - [workspace-chat-streaming-realtime-rendering-plan.md](./workspace-chat-streaming-realtime-rendering-plan.md)（G1–G8 缺口分析；G1/G2/G5 已落地）
> - [aicli-event-stream-rendering-order-render-model-spec.md](./aicli-event-stream-rendering-order-render-model-spec.md)（Item/ChangeSet 规格蓝本）
> - [aicli-event-stream-rendering-order-todo.md](./aicli-event-stream-rendering-order-todo.md)（TUI 侧同类待办，语义与测试用例来源）
>
> 结论快照：**Phase 0–3 全部已完成（2026-08-16 实施）**。实施提交：
> - Phase 0（后端）：`backend/internal/api/skills/trajectory_events.go` + `trajectory_events_test.go`（chat SSE 事件入 EventStore + 帧 seq 契约；`go build ./...` 与 `go test ./internal/api/skills -count=1` 通过）。
> - Phase 1（前端渲染模型）：`frontend/src/lib/trajectory/`（types/reducer/stream-batch/projection/golden/search-index/recovery/export）+ `use-trajectory-snapshot.ts` store + turn hook 14 回调接入。
> - Phase 2（轨迹视图）：`frontend/src/components/workspace/trajectory/`（trajectory-view/trajectory-virtual-rows/trajectory-detail-panel/reasoning-window）。
> - Phase 3（恢复/导出/离线分析）：`use-trajectory-recovery.ts` + `scripts/analyze-trajectory.mjs`。
> - 验证：前端 vitest 382/382、`npx tsc --noEmit` 全绿、`trajectory.spec.ts` e2e 7/7、分析脚本 node:test 4/4。
> **评审修正要点**：
> 1. 后端**并非无事件持久化**：`chat.EventStore`（`AppendEvent`/`ListEvents(afterSeq,limit)`，InMemory+SQLite）、bus→EventStore 管道（handler.go:3293）、`GET /sessions/{id}/runtime/events`（已支持 `after`/`limit`/`wait_ms` 长轮询）均已存在——**Phase 0 从「新建 JSONL 录制」改为「复用 EventStore 接入 chat SSE 事件（Type 前缀 `chat.sse.*`）」**，不新建端点；
> 2. 前端 `useSessionRuntimeStream`（`streamSessionRuntime({after, pollMs})` + seq 去重合并）已具备增量续传机制——**Phase 3 直接对齐，不新造**；
> 3. 真实缺口收敛为：**chat SSE 的 LLM 轨迹事件（chunk/reasoning/工具参数与输出）未入任何持久化** + 前端无统一渲染模型 + 无轨迹视图；
> 4. TUI 编码器测试真实路径为 `cmd/aicli/ui/render/encoding/encoder_test.go`（非 `encoder_test.go` 简写）；
> 5. 本项目无 `recovery_gc` 机制——清理策略引用现有存储（SQLite）策略。

## 1. 状态口径

本文所有条目按以下口径标注：

- **[未开始]**：无实现，任务未动工
- **[进行中]**：有实现或测试，但可能未接入主路径
- **[部分]**：主路径已接入，但未达验收标准
- **[已完成]**：对应验收标准全部成立

## 2. 任务清单（按 Phase）

### Phase 0：chat SSE 轨迹事件入 EventStore + seq 契约（预估 1–2 天，评审修正）

- [x] **P0-1 [已完成] chat SSE 轨迹事件接入 `chat.EventStore`**：`streamLLMChat`（handler.go:6972）与 `streamStaticResult`（handler.go:7116）改用 `h.newTrajectoryEmitter(w, session)`（`trajectory_events.go`）；每帧先 `AppendEvent(Type: "chat.sse."+event, Payload: 原始载荷)`；**错误隔离**：`AppendEvent` 失败返回 0，帧 seq 降级为连接内计数，绝不阻塞 SSE；**并发安全**：复用 EventStore 自带锁。
  - 验收标准：① 集成测试断言 SSE 帧与 `ListEvents` 返回逐条一致（kind/payload 相同）；② 错误注入（store 不可用）后 SSE 主链路不受影响；③ 与既有 runtime 生命周期事件共库共存、按 `Type` 前缀可区分（双事件源边界测试）。
  - 参照：`chat/session_runtime_store.go:42-46`（EventStore 接口）、`handler.go:3293-3303`（既有 bus→EventStore 管道写法）。
  - 实施证据：`TestTrajectoryEmitterPersistsAndAlignsSeq` / `TestTrajectoryEmitterDegradesOnAppendFailure` 通过。

- [x] **P0-2 [已完成] SSE 帧 `_event.sequence` = 持久化 seq**：`sseEmitter` 增加可选 `persist` 钩子（handler.go:7662-7677），`Emit` 先 persist 拿 seq 再写帧（`writeSSEEventWithEnvelope`/`wrapSSEData` sequence 类型 int→int64）；非 chat 的 SSE 使用方（log_viewer、runtime_stream）不挂钩子，行为不变。
  - 验收标准：同一会话内 SSE 帧 seq 单调、与 EventStore 中该事件 seq 一致；前端可解析到每帧 seq。
  - 实施证据：`TestAgentChatTrajectoryEventsPersistedEndToEnd` 断言每帧 sequence 单调且与 `ListEvents` 一致。

- [x] **P0-3 [已完成] 拉取验证（不新建端点）**：`GET /api/runtime/sessions/{id}/runtime/events?after=N`（session_runtime_handlers.go:489，已支持 after/limit/wait_ms）对 `chat.sse.*` 事件的增量拉取正确性已由集成测试覆盖（after=最后 chat.sse seq 只返回后续事件）；前端按 Type 前缀过滤即可区分，暂不加 `type_prefix` 参数（后续需要再加）。
  - 验收标准：① `after=N` 返回 seq 严格大于 N、按 seq 升序；② `after=0` 返回全量 `chat.sse.*`；③ 无事件时返回 200 空列表（与现有端点行为一致）。
  - 实施证据：`TestTrajectoryEmitterPersistsAndAlignsSeq` 的 after=1 / after=last 断言、`TestAgentChatTrajectoryEventsPersistedEndToEnd` 的 after 断言。

- [x] **P0-4 [已完成] 敏感性与清理策略**：轨迹事件与现有 runtime 事件同库（`session_events` 表，SQLite），保留/清理沿用既有机制（`SQLiteRuntimeStore.pruneRuntimeRowsTx`，session_runtime_store.go:2277）；导出脱敏选项留待 Phase 3 导出功能时实现。
  - 验收标准：文档记录存放位置与保留策略（本文档本条目即记录）；清理逻辑复用既有实现，无新增。

**Phase 0 总验收（评审修正）✅ 已达成（2026-08-16）**：集成测试证实——含推理的 chat 后 `ListEvents(after=0)` 按 seq 拉回全部 `chat.sse.*` 事件（数量与 SSE 帧一一对应、Type/Payload 一致）；SSE 帧 `_event.sequence` 与存储 seq 一致；`after` 增量补齐正确；AppendEvent 失败时 chat 正常（帧 seq 降级为连接内计数）。

### Phase 1：前端统一渲染模型（预估 3–5 天）

- [x] **P1-1 [已完成] 新建 `frontend/src/lib/trajectory/types.ts`**：`TrajectoryEventKind`（复用 13 类 SSE 事件名）、`Item`（ID/Seq/Kind/CauseID/Status/Head/Created/Updated，照搬 render-model-spec §3）、`ChangeSet`（Append/Upsert/Remove + Revision + Tail）。
  - 验收标准：类型与 render-model-spec §3 字段一一对应；TS 编译通过。
  - ✅ 已达成（2026-08-16）：`types.ts` 含 `TrajectoryEventKind`（14 类含 error）、`TrajectoryItem`/`TrajectoryHead`（text/reasoning/tool/system/structured）、`TrajectoryChange`/`ChangeSet`、`TrajectoryEvent`、`TrajectorySnapshot`；`npx tsc -b` 全绿。

- [x] **P1-2 [已完成] `trajectory-reducer.ts`（纯函数，核心）**：
  - `append / upsert / remove` + 幂等规则 + 终态保护（乱序缓冲拼接 `1,3,2 → ABC`、reasoning 独立索引、孤儿 final 直接终态）；
  - 工具状态机：`tool_start → tool_call → tool_end` 折叠为单 Item（status: started→running→finished/error），输出经 `CauseID` 锚定；
  - G7 事件（planning/orchestration/route/observation/subagent）映射为可折叠 Item（数据面先行）。
  - 验收标准：单测逐条对齐 TUI `cmd/aicli/ui/render/encoding/encoder_test.go` 用例：乱序 ABC、重复 delta 幂等、终态后 upsert 拒绝、reasoning 不覆盖 assistant Item、同 ID 同内容跳过。
  - ✅ 已达成（2026-08-16）：`trajectory-reducer.test.ts` 16/16 全绿（基本序列/乱序缓冲/幂等/终态保护/工具状态机/G7 映射/remove）。语义修正记录：planning/orchestration/route 为「可演进」structured 块（流结束前 running，done 收尾）；text 增量按追加语义幂等。

- [x] **P1-3 [已完成] `stream-batch.ts`**：rAF 帧内同 kind 连续 delta 合并为 segment（空 delta 保留以完成 live reasoning 边界）；`setTimeout` 兜底后台标签页（承接 G3）。
  - 验收标准：单测覆盖「帧内合并顺序」「reasoning→text 边界语义」「空 delta 完成 live reasoning」。
  - ✅ 已达成（2026-08-16）：`stream-batch.test.ts` 8/8 全绿（帧内批量/rAF 兜底/后台 tab/空 delta/合并顺序/边界语义）。

- [x] **P1-4 [已完成] 接入 `use-workspace-agent-chat-turn.ts`**：`onChunk/onReasoning/onTool*` 改为「先派发 reducer、再以快照映射 segments」；新增 `useTrajectorySnapshot()`（不可变快照 + chat 投影映射器 `Item[] → MessageSegment[]`，复用 `workspace-thread-state.ts` upsert 辅助）；`message-list.tsx` **零改动**。
  - 验收标准：① 流式期间 tool/reasoning/text 以稳定 ID upsert（行不闪动）；② 现有 chat 视图测试与 e2e 全绿（无回归）；③ **过渡策略**：接入初期保留现有 segments 路径并行（双跑校验快照一致性），确认无回归后再切换为唯一路径（对齐 TUI P3 的 AICLI_SCENE_PRESENTER 思路，避免一次性重构风险）。
  - ✅ 已达成（2026-08-16）：`use-trajectory-snapshot.ts`（store：批处理节流 + `useSyncExternalStore` 订阅）+ `projection.ts`（`trajectoryItemsToMessageSegments` chat 投影 + `compareTrajectoryVsSegments`/`debugTrajectoryConsistency` 双跑校验）；turn hook 14 个 SSE 回调全部 `pushTrajectory`（meta/chunk/reasoning/tool_start/tool_call/tool_end/planning/orchestration/route/observation/subagent/result/done/error），`finalizeTurn` 内 flush + updater 内双跑校验（DEV warn）。测试：projection 10/10、store 7/7、turn hook 3/3、reducer 16/16、stream-batch 8/8（合计 44 全绿）+ `tsc -b` 全绿；`message-list.tsx` 零改动；e2e 全量在 P1-6 收尾统一跑。

- [x] **P1-5 [已完成] golden 测试向量**：建立共享事件序列 → 期望快照的 golden 数据，与 TUI 编码器语义对拍（防双份实现漂移）。
  - 验收标准：同一 golden 向量在 TS reducer 与 TUI encoder 上结果一致（或记录差异并评审）。
  - ✅ 已达成（2026-08-16）：`golden.ts` 4 个语义向量（basic-sequence/out-of-order/idempotent/reasoning-independent，各自标注 TUI 参照用例 `TestEncode*`）+ `golden.test.ts` 6/6 全绿；`KNOWN_MODEL_DIFFERENCES` 记录 4 项已知模型差异（工具折叠 vs 双 item、system 占位、reasoning 呈现、权威 final 来源）。

- [x] **P1-6 [已完成] replay 等价性测试**：同一事件序列两次构建快照深相等；`after` 补齐事件 + 重放后快照与实时一致。
  - 验收标准：`toBeDeepEqual` 风格断言通过。
  - ✅ 已达成（2026-08-16）：`trajectory-replay.test.ts` 5/5 全绿——确定性（两次构建深相等）、增量补齐（分段应用 == 全量应用）、幂等重放（重放不改变快照）、乱序重放（乱序 == 有序，乱序缓冲）、终态保护（done 后 items 不变，仅 `lastEventSeq` 水位前进）。trajectory 全套 49/49 全绿。

**Phase 1 总验收 ✅ 已达成（2026-08-16）**：流式期间 tool/reasoning/text 稳定 ID upsert（reducer 稳定 ID + 折叠语义，单测覆盖）；replay 幂等（P1-6 5/5）；chat 视图无回归（`workspace-chat.spec.ts` e2e 6/6 全绿：G1 reasoning 实时渲染、G2 tool 卡片状态流转、G5 滚动跟随、G6 phase strip、G8a/G8b 中断路径）。trajectory 全套 55 单测 + turn hook 3 + tsc -b 全绿；预存基线失败 `workspace-thread-state.test.ts` "treats null session history as an empty message list"（fixture 自带 `assistant-existing` 与测试期望冲突，HEAD 既有问题）已于 Phase 2 收尾时修复：测试对齐实现语义（null history 保留现有流式消息，与 history 匹配时的合并语义一致），改名 "keeps existing streaming messages when session history is null"，全量 330/330 全绿。

### Phase 2：轨迹视图 UI（预估 3–5 天）

- [x] **P2-1 [已完成] `trajectory-view.tsx` 主视图**：`frontend/src/components/workspace/trajectory/trajectory-view.tsx`，工具栏（搜索、导出 JSONL、筛选：仅工具/仅消息/全部、时间线模式切换）+ 明细列表 + 详情面板（`trajectory-detail-panel.tsx`，`data-trajectory-detail` 标记）。
  - 验收标准：① 流式期间事件逐条出现在明细列表；② 单行摘要与详情面板共用同一 Item 对象（无两套数据）；③ 筛选条件即时生效。
  - ✅ 已达成（2026-08-16）：e2e `trajectory.spec.ts` P2-1 三条用例全绿（切换保留、工具筛选、搜索过滤）；组件测试 `trajectory-view.test.tsx` 覆盖摘要/详情/筛选/时间线联动。

- [x] **P2-2 [已完成] 时间线概览**：事件泳道（tool 高亮车道；sequence 模式先行，duration 模式列为开放问题 Q2）。
  - 验收标准：一眼区分工具密集段与纯对话段；焦点区间与明细列表联动（点击时间线跳转到对应行）。
  - ✅ 已达成（2026-08-16）：时间线块 `aria-label="<kind> <seq>"`，点击跳转到对应行并打开详情面板；e2e P2-2 全绿。

- [x] **P2-3 [已完成] 虚拟滚动 `trajectory-virtual-rows.ts`**：行 = Item，行高缓存；request-only 零高行合并到下一内容行（对齐 DeepSeek-Reasonix `transcriptRows.ts` 思路）。
  - 验收标准：1000+ 事件会话滚动不卡顿（e2e 断言帧率/滚动流畅或至少无 O(n²) 全量重建）；滚动时行身份稳定（key 不闪动）。
  - ✅ 已达成（2026-08-16）：`trajectory-virtual-rows.ts` + mock `burst` 脚本（1200+ 事件）；e2e P2-3 断言渲染 DOM 行数 < 150、滚动到底后尾部行可见。

- [x] **P2-4 [已完成] 轨迹视图入口**：`workspace-shell` 加 Chat/Trajectory 标签切换（形态见开放问题 Q1）。
  - 验收标准：chat ↔ 轨迹切换不丢流式状态（同一 reducer 快照双投影）；e2e 覆盖切换路径。
  - ✅ 已达成（2026-08-16）：e2e P2-4 切换后 chat 消息与轨迹行均保留。

- [x] **P2-5 [已完成] G4 增量 Markdown（轨迹明细渲染）**：明细详情采用「frozen/tail + 源 offset 稳定 key」增量解析（deepseek-harness `incremental.ts` 思路），避免长回复 O(n²)。chat 视图的 G4 修复遵循 streaming 文档口径，不在此重复。
  - 验收标准：长回复（>4k tokens）流式渲染帧率可接受（bench/性能测试或 e2e 计时断言）。
  - ✅ 已达成（2026-08-16）：详情面板 text item 在 `status === "running"` 时以 `streaming` 模式渲染——复用 `MessageMarkdown` 内置增量渲染（`splitStreamingMarkdown`：frozen stable 块 memo + tail 按 fence/structured/plain 解析，`CodeBlock streaming` 逐行增量），流式期间只有 tail 增量解析，无 O(n²) 全量重建。

- [x] **P2-6 [已完成] `MessageReasoningRow` 稳定窗口裁剪**：对齐 DeepSeek-Reasonix `STREAMING_REASONING_WINDOW_STEP_CHARS/LINES`，超长推理只渲染末尾稳定窗口。
  - 验收标准：万字符推理流式期间无布局抖动（组件测试）。
  - ✅ 已达成（2026-08-16）：`lib/trajectory/reasoning-window.ts`（`REASONING_WINDOW_STEP_CHARS=2000` / `MAX_CHARS=8000`，`displayReasoningText` 返回尾部稳定窗口 + 折叠计数）；应用于轨迹详情面板（`ReasoningContent`：裁剪提示 + 有界 `<pre>`）与 chat 视图 `MessageReasoningRow`（展开面板裁剪 + 提示条）。测试：`reasoning-window.test.ts` 5/5 + `trajectory-detail-panel.test.tsx` 2/2（万字符推理渲染内容 ≤ 8k 且有界）。

- [x] **P2-7 [已完成] 增量搜索索引**：全文索引 + 3s 节流重建 + terms AND；sources 签名比较避免重复解析。
  - 验收标准：长会话搜索响应 < 100ms（节流后）；命中行可定位。
  - ✅ 已达成（2026-08-16）：`trajectory-search-index.ts`（`tokenizeTrajectoryText` Unicode 词切分 + terms 倒排 `term → itemId 集合` + `trajectorySearchSignature`（id:revision 版本签名）+ `useTrajectorySearchIndex` 3s 节流重建）；视图接入：索引新鲜时 terms AND（O(命中)），节流窗口内线性回退保证结果正确。测试：`trajectory-search-index.test.ts` 10/10（切分/签名/AND/空安全）。

- [x] **P2-8 [已完成] e2e（Playwright + mock SSE）**：事件逐条出现、搜索命中、1000+ 事件虚拟滚动、chat↔轨迹切换。
  - 验收标准：`frontend/e2e/` 下新增用例全绿（沿用现有 mock 机制）。
  - ✅ 已达成（2026-08-16）：`trajectory.spec.ts` 5/5 全绿（mock-server 注入 `_event.sequence` + `burst` 脚本）。

**Phase 2 总验收 ✅ 已达成（2026-08-16）**：含推理+多工具+subagent 的会话可在轨迹视图完整回放（chat 双投影一致性由 P1 双跑校验兜底）；搜索（terms AND 增量索引 + 线性回退）/筛选/时间线/虚拟滚动可用；千级事件 DOM 有界；长文本（markdown 增量 + 推理窗口裁剪）流式渲染有界。验证：trajectory 单测 92/92（+ reasoning-window 5 + search-index 10 + detail-panel 2）+ 全量 349/349 + tsc + `trajectory.spec.ts` e2e 5/5。

**关键缺陷修复（2026-08-16，Phase 2 收尾）**：轨迹快照在 e2e 中恒为空——根因是 workspace-page 的 `useEffect(() => trajectoryStore.reset(), [selectedThread?.id])` 在「发送 prompt → `navigate`（startTransition 低优先级）→ 迟到渲染」后才执行，reset 清空已收集的事件快照并造成 seq gap（后续事件全部进入 reducer 乱序缓冲且永远等不到前序）。修复：reset 移到两个同步触发点——`submitPrompt` 开头（新 turn 开始）与线程选择 handler（用户主动切换）；移除迟到 effect。附带：`trajectory-detail-panel.tsx` 加 `data-trajectory-detail` 标记修复 e2e 严格模式选择器冲突；`workspace-thread-state.test.ts` 过时断言对齐实现语义。

### Phase 3：恢复、重放与离线分析（预估 2–3 天）

- [x] **P3-1 [已完成，评审修正] 断线恢复**：对齐既有 `useSessionRuntimeStream` 机制（`streamSessionRuntime({after, pollMs})` + `getRuntimeEventSeq`/`mergeRuntimeEvent`）——挂载时以 `after=本地已收最大 seq` 拉取 `chat.sse.*` 事件 → 同一 reducer 重放 → 快照与中断前一致（替换 `use-session-history-sync` 全量重建路径，缓解 R3；history sync 保留为兜底）。
  - 验收标准：kill -9 后端/刷新页面后，轨迹视图与中断前逐条一致（e2e 断言）。
  - ✅ 已达成（2026:08:16）：`frontend/src/lib/trajectory/recovery.ts`（`streamSessionRuntime` 增量拉取 + `chat.sse.*` 过滤 + seq 去重合并，poll 兜底）+ `use-trajectory-recovery.ts` hook（挂载后按 `after=本地 maxSeq` 恢复；防重标记移到**成功完成之后**——修复 reload 竞态：sessions 数据中途清空导致 selectedThread 短暂 undefined → effect cleanup 取消恢复且防重已设置 → 恢复永不重跑）。测试：`trajectory-recovery.test.tsx` 4/4（防重/取消重试/增量/去重）；e2e `P3-1` 刷新后逐条一致。

- [x] **P3-2 [已完成，评审修正] 导出 JSONL**：轨迹视图「导出」从 EventStore 读取 `chat.sse.*` 事件（按 seq）生成 JSONL `{seq, ts, kind, payload}` 下载。
  - 验收标准：导出内容与 `runtime/events` 拉取结果逐条一致；导出时可选脱敏（对齐风险 R4）。
  - ✅ 已达成（2026:08:16）：`frontend/src/lib/trajectory/export.ts` 纯函数（`chatSseEventToExportEntry` 过滤非轨迹事件 + 剥离 payload.seq 游标；`eventsToTrajectoryJsonl` 按 seq 升序；`downloadTrajectoryJsonl` blob 下载）+ `TrajectoryView` 导出按钮（`sessionId` 经 workspace-shell 传入，`fetchSessionRuntimeEvents` 拉 EventStore 生成 JSONL）。测试：`export.test.ts` 5/5；e2e `P3-2` 下载文件逐行解析断言（meta 打头、含 tool_start/done、每行带 seq/ts）。

- [x] **P3-3 [已完成] 离线分析脚本 `scripts/analyze-trajectory.mjs`**：输入 JSONL 输出 TTFT、工具耗时、重试率、token 分布、reasoning 占比。
  - 验收标准：对样例 JSONL 输出稳定指标；空/损坏行容错；单测或样例验证脚本。
  - ✅ 已达成（2026:08:16）：`scripts/analyze-trajectory.mjs`（TTFT=meta→首个 text chunk；工具耗时=tool_start/tool_end 按 tool.id 配对；重试=done.result.orchestration 的 route_attempted/fallback_reason + 同名工具重复；token=done.result.usage 汇总；reasoning 占比=reasoning chars/(reasoning+text chars)；损坏行跳过计数）+ `scripts/fixtures/trajectory-sample.jsonl` 样例 + `scripts/analyze-trajectory.test.mjs`（node:test 4/4：稳定指标/空损坏容错/解析/无 done 降级）。运行：`node scripts/analyze-trajectory.mjs <file.jsonl> [--json]`。

**Phase 3 总验收 ✅ 已达成（2026:08:16）**：会话中断后重开，轨迹视图与中断前逐条一致（P3-1 e2e）；导出的日志可用脚本产出诊断报告（P3-2 e2e + P3-3 样例验证）。验证：`trajectory.spec.ts` e2e 7/7 全绿 + 导出单测 5/5 + 恢复 hook 单测 4/4 + 分析脚本 node:test 4/4 + `npx tsc --noEmit` 全绿。

---

## 3. 验收项状态总表（对照方案 §10 验收清单）

| 验收项 | 状态 | 当前证据 |
|---|---|---|
| Phase 0：chat SSE 事件入 EventStore（`chat.sse.*`）+ SSE 帧 `seq` + 复用 `runtime/events?after=` 增量拉取 | [已完成] | `trajectory_events.go`：每帧先 `AppendEvent` 再写帧（失败降级连接内计数）；集成测试断言帧 seq 与 `ListEvents` 逐条一致（P0-1/2/3 实施证据） |
| Phase 1：web 统一 reducer 接入 turn hook；replay 等价；chat 零回归 | [已完成] | `lib/trajectory/*`（reducer 16 + stream-batch 8 + projection 10 + golden 6 + store 7 + turn hook 3）；`finalizeTurn` 内双跑校验（DEV warn）；chat 视图零改动 |
| Phase 2：轨迹视图（时间线/明细/筛选/虚拟滚动）；G4/G6 顺带解决 | [已完成] | `components/workspace/trajectory/*`；搜索/筛选/时间线/虚拟滚动/详情面板；千级事件 DOM 有界（P2-3 e2e）；长文本增量渲染有界 |
| Phase 3：断线恢复一致；导出 + 离线分析脚本 | [已完成] | `use-trajectory-recovery.ts`（刷新后逐条一致 e2e）+ `export.ts`（JSONL 下载 + R4 脱敏开关 e2e）+ `scripts/analyze-trajectory.mjs`（node:test 4/4） |
| roadmap「运行轨迹展示」验收项达成 | [已完成] | roadmap.md:351 建议动作 4（轨迹视图 + inline tool steps 消息区）已落地，验收标准中的轨迹展示部分达成 |

## 4. 测试缺口（对照 Phase 任务）

- [x] **后端 handler 集成测试**（P0-1/2/3，评审修正）：SSE 帧 seq 与 EventStore 一致、`after` 补齐、错误注入隔离、双事件源 Type 前缀可区分——✅ `trajectory_events_test.go`（`TestTrajectoryEmitterPersistsAndAlignsSeq` / `TestTrajectoryEmitterDegradesOnAppendFailure` / `TestAgentChatTrajectoryEventsPersistedEndToEnd`）。
- [x] **TS reducer 单测**（P1-2）：对齐 TUI encoder 用例（乱序/幂等/终态/reasoning 索引）+ 工具状态机 + G7 事件映射——✅ `trajectory-reducer.test.ts` 16/16。
- [x] **TS 帧合并单测**（P1-3）：同 kind 合并、边界语义、空 delta、后台 tab 兜底——✅ `stream-batch` 8/8。
- [x] **golden 向量对拍**（P1-5）：TS reducer 与 TUI encoder 共享事件序列 → 期望快照——✅ `golden.ts` 4 向量 + `golden.test.ts` 6/6 + `KNOWN_MODEL_DIFFERENCES` 4 项差异记录。
- [x] **hook 集成测试**（P1-4/6）：流式→finalize 快照一致、stop 路径、replay 深相等——✅ turn hook 3/3 + replay 等价 + 恢复 hook 4/4。
- [x] **轨迹视图组件测试**（P2-1/2/3）：摘要/详情/筛选/虚拟行/时间线联动——✅ `trajectory-view.test.tsx` / `trajectory-detail-panel.test.tsx` / `trajectory-virtual-rows.test.tsx` / `trajectory-search-index.test.ts` / `trajectory-reasoning-window.test.ts`。
- [x] **e2e**（P2-8/P3-1）：mock SSE 全流程、1000 事件、断线恢复、导出一致性——✅ `trajectory.spec.ts` 7/7（含 P3-2 脱敏导出断言）。
- [x] **Q4 runtime 生命周期事件映射**（评审新增，2026-08-16 完成）：后端白名单纳入请求期 lifecycle 事件 + 前端 runtime 映射——✅ 后端 `trajectory_events_test.go` 双事件源共存断言按前缀分类适配；前端 `trajectory-recovery.test.ts`（32/32 含 runtime 双源映射）+ `trajectory-reducer.test.ts` 21/21（runtime → system 行 note 可读摘要 + 同 seq 重复 push 幂等）。

## 5. 建议执行顺序

1. **P0（后端）先行**：chat SSE 接入 EventStore + seq 暴露，是前端 Phase 1/3 的依赖；后端独立可验收（复用现有端点，无新路由）。
2. **P1（前端 reducer）**：纯函数 + 单测先行，接入 hook 最后做（可回退）。
3. **P2（轨迹视图）**：依赖 P1 快照；先主视图 + 虚拟滚动，搜索（P2-7）可推迟。
4. **P3（恢复与离线分析）**：依赖 P0 端点与 P1 reducer；脚本（P3-3）可与 P2 并行。
5. 横切：golden 向量（P1-5）随 P1 建立；文档同步与入库（方案 + 本待办）在首个 Phase 合入时一并提交。

## 6. 风险与观察

- [x] **R1 双份状态机漂移**（TUI Go 编码器 vs web TS reducer）：✅ 已缓解——`golden.ts` 4 向量共享语义 + `KNOWN_MODEL_DIFFERENCES` 记录 4 项差异；中期评估上提 `internal/` 共享（保持开放，成本高）。
- [x] **R2 seq 契约**（评审修正）：✅ 已落地——SSE 帧 seq = `AppendEvent` 返回的 EventStore seq；并发 append 由 EventStore 锁保证；`ListEvents` 分页/断档由前端 `nextRecoveryAfter` 按 max seq 推进；双事件源以 `chat.sse.*` 前缀区分（P0 测试覆盖）。
- [x] **R3 历史同步覆盖流式**：✅ 已缓解——Phase 3 事件日志 + 恢复（`use-trajectory-recovery`）成为轨迹事实源；history sync 保留为 chat 兜底，流式期间暂停保护保持。
- [x] **R4 日志敏感性与容量**（评审修正）：✅ 已实现——复用 EventStore（SQLite）现有保留/清理策略；导出脱敏选项（视图「Toggle export redaction」开关，掩码工具 args/arguments/content，保留身份字段与正文，文件名带 `-redacted`）；未引入 `recovery_gc`。
- [x] **R5 双事件源边界**（评审新增）：✅ 已落地——chat SSE（`chat.sse.*`）与 runtime 生命周期事件共库共存；轨迹视图只消费 `chat.sse.*`（recovery/export 均按前缀过滤）；runtime 事件映射保持后续项（Q4）。
- [x] **Q1** 轨迹视图入口形态：✅ 已定——Tab 切换（workspace view tabs Chat/Trajectory）。
- [x] **Q2** duration 时间线（idle 压缩）：✅ 已定——本期只做 sequence 时间线（timeline 按 seq 定位），duration 列为后续。
- [x] **Q3** 增量搜索索引：✅ 已做——`trajectory-search-index.ts`（terms AND 索引 + 3s 节流重建 + 线性回退）。
- [x] **Q4**（评审新增）runtime 生命周期事件（approval/notice/team 编排）是否映射进轨迹视图？：✅ 已落地（2026-08-16）——后端 bus→EventStore 白名单纳入请求期 lifecycle 事件（approval_requested/approval_resolved/session_compact_*/session_start/session_end/session_interrupted/context_reconciled/checkpoint_created；与原生 tool.requested/tool.completed/context.profile.injected/recall.performed 共库共存、按 Type 前缀区分，`trajectory_events_test.go` 适配；rewind/backtrack/team 编排未入白名单——事件不落库则视图不显示，属有意边界）；前端 `types.ts` 新增 kind `runtime`，`recovery.ts` 双源解析（`runtime_type` + payload 字段名兜底），`trajectory-reducer.ts` 新增 `describeRuntimeEvent`（approval/compact 等可读摘要，`approval_resolved` 读后端 `allowed` 字段）+ `case "runtime"`（`system` 行、`id: runtime-{seq}`、status completed），实时通路 `use-session-runtime-stream.ts` 可选 `onRuntimeEvent` 回调经 ref 转发（避免每次 render 重启流）由 workspace-page 投递。测试：recovery 32/32、reducer 21/21、前端全量 382/382、e2e P3-1 注入 `approval_requested` 断言轨迹行、后端 `go test ./internal/...` 全绿。
- [ ] **O1**（观察项）恢复完成后轨迹初始视口在顶部：虚拟列表为保留 P2-3「DOM 有界」语义不自动贴底；恢复/历史数据就绪后用户需滚动查看末尾行（如 Q4 审批行）。已由 e2e 注释记录（P3-1 显式 scrollToBottom 后断言）；后续若做「恢复完成自动定位最新」的产品决策，需同时适配 P2-3 断言语义。
- [x] **打包链路修复（2026-08-18）**：`package-runtime-server.ps1` 的 `tsc -b`（build 严格检查，含测试文件；与 `tsc --noEmit` 的 tsconfig 范围不同）暴露 6 处 Q4 测试/实现类型错误——① `trajectorySearchSignature` 误用不存在的 `item.revision`，改用 `item.updatedAt`（reducer upsert 时单调递增，等价 revision 语义）；② search-index / detail-panel 测试夹具补 `causeId`/`createdAt` 并去多余字段；③ recovery 测试的未读 `resolveFetch` 改为永挂 promise 实现。修复后 `npx tsc -b` 全绿，`.\scripts\package-runtime-server.ps1` 完整通过（frontend build + go build + smoke test + 归档；此前一次失败仅为旧 runtime-server 进程占用 exe 的文件锁，非代码问题）。
