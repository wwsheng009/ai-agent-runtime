# aicli 流式管线性能审计与优化方案（2026-10-11）

> 审计对象：TUI 聊天会话中「LLM SSE → 事件总线 → 桥 → UI actor → 物理 writer → SQLite」全链路。
> 审计方法：三路并行代码审计（桥/actor 热路径、活动流渲染成本、事件发布与持久化），关键结论逐条到源码人工复核。
> 结论先行：**并发架构（LLM 请求不等 UI）形态正确，但默认配置下存在一处直接违反该不变式的 P0 缺口（每 delta 同步写 SQLite），以及若干随流长/会话长增长的 O(n²) 热点。**
>
> **实施状态（2026-10-11 收口）：批次 1 全部完成**，见文末 §9 实施记录。

## 1. 链路与不变式

生产链路（每级独立 goroutine）：

```
runChatLoop 主 goroutine ──阻塞──> actor.SubmitPrompt (reply channel)
    └─ actor run goroutine（agent loop，llmRuntime.Call 同步）
         └─ SSE 读流 goroutine ──Bus.Publish(同步分发)──> 订阅者们
              ├─ 宿主持久化 handler（Enqueue 或同步 AppendEvent）★P0
              ├─ chatRuntimeEventBridge.Handle（有界入队 + backlog 合并）
              └─ stall 检测 / 子代理镜像 / transcript 弹层
                   └─ bridge run goroutine → UI actor 邮箱（TryPost）
                        └─ UI actor 单 reducer goroutine（唯一渲染决策）
                             └─ FlushEffect → 物理 executor worker → os.Stdout
```

核心不变式（`chat_runtime_events.go:1467` 已成文）：**Streaming deltas must never block the LLM callback chain。** 本文所有 P0/P1 项都对照这条判据。

## 2. P0：违反「LLM 回调链零阻塞」的三处

### P0-1 默认配置下每个 delta 一次同步 SQLite 写事务

- 事实链：
  - `cmd/aicli/commands/chat_actor_host.go:1056` 注释自证：「runtimeEventPersistSettings 读取宿主的批量落盘配置（**默认关闭、逐条同步路径**）」。
  - batching 关闭时 handler 直接走 `h.EventStore.AppendEvent(context.Background(), mapped)`（`chat_actor_host.go:1135`），**执行 goroutine 是 LLM SSE 读流回调链**。
  - `internal/chat/session_runtime_store.go:2611-2698`：每次 AppendEvent = `BeginTx(LevelSerializable，即 IMMEDIATE 写锁)` + INSERT+RETURNING + 条件 prune + Commit。WAL+NORMAL 下无每事务 fsync，但每 delta 一个独立写事务的开销仍全额落在读流 goroutine 上。
  - 若开了 batching：溢出默认 `FailMode=block`（`internal/chat/event_persist_buffer.go:272-274`）+ `AsyncDispatch` 默认关（`internal/config/manager.go:333-334`）⇒ 溢出时走 `syncFallbackWrite`，**10s 超时的同步落盘**（`event_persist_buffer.go:300-316, 390-399`）。
- 影响：磁盘抖动直接反压 SSE 读流 ⇒ 首 token 延迟与吞吐随 I/O 波动；会话库单连接读排队时互相放大。`internal/chat/session_event_payload_cap.go:37` 现场数据：907MB 会话库中约 75% 字节是这类流式增量行，写频极高。
- 方案：
  1. aicli 本地宿主默认 `BatchingEnabled=true` + `AsyncDispatch=true`（batch=64/25ms 现成参数），逐条同步仅保留为显式回退。
  2. delta 类事件（`assistant_delta`、`assistant.reasoning`）溢出策略改 `FailMode=drop`：终态 `assistant_message` 已可完整重建正文，delta 行只影响 resume 重放细粒度；drop 计数进遥测。drop 仅作用于 delta，critical 类型维持 block。
  3. `AsyncDispatch requires batching` 的配置告警（`chat_actor_host.go:1096`）升级为自动满足前置，消除错误组合。
- 验收：新增 `session_runtime_store` 层 benchmark——1000 条 delta 流式 append 在默认配置下的 p99 耗时，目标「生产者侧每次 Enqueue ≤ 10µs、零同步写」；断言默认配置下 LLM 回调链上不存在任何 `AppendEvent` 直接调用（可用注入 store 的计数器测试钉住）。

### P0-2 Bus.Publish 在独占锁内做留存深拷贝 + 尺寸估算

- 事实：`internal/events/bus.go:316-322` 每次发布取**独占写锁**；锁内 `appendRetainedEvent`（333 行 `ApproximateEventBytes` 递归遍历 payload + 342 行 `cloneEvent` 深拷贝整个 payload map）。`NewBus()` 默认留存 512 条 / 8MB（`bus.go:240-244`）⇒ 每个 delta chunk 在锁内付：递归尺寸估算 + 深 clone + 双订阅列表快照克隆。多 agent 并发流式时所有发布者在这把锁上串行化（子代理与父代理共用 EventBus，`chat_runtime_events.go:5495`）。
- 影响：每 chunk 固定分配 + 锁竞争面，随订阅者与并发 agent 数放大。
- 方案：
  1. 留存路径异步化：Publish 锁内只做 ring 槽位占位（或直接把 event 值拷贝——浅拷贝 event 结构，payload 深克隆移出锁外由 single janitor goroutine 补齐）；尺寸计算随事件携带一次（见 P1-3 记忆化），锁内只读。
  2. delta 类事件可免留存或留存「合并后句柄」：留存只服务 `Recent()/Query()` 诊断，delta 全文留存的诊断价值低于成本。
  3. 订阅列表改 copy-on-write + 原子指针，稳态 Publish 零分配。
- 验收：`internal/events` 增加并发 Publish benchmark（4 发布者 × 1000 chunk），对比改前改后的 allocs/op 与锁等待；Publish 稳态路径 allocs 目标 ≤1。

### P0-3 backlog 合并的全串重拼接 O(n²)

- 事实：UI 落后时相邻 delta 合并进 backlog 槽：`a.Payload["delta"] = streamEventText(a) + streamEventText(b)`（`chat_runtime_events.go:2754`；reasoning 分支 2723，legacy 2797-2808）。Go string 不可变 ⇒ 构造总长 L 的合并事件需 O(k×L) 拷贝；上限 1MB（`chatStreamCoalesceEventByteLimit`，387 行）。1MB 回复以 200 条×5KB 到达 ≈ 100MB 纯 memcpy + 200 次大临时串，**全部发生在持有 `streamMu` 的发布者 goroutine 上**（即 LLM 回调链）。另每 delta 2 次 `streamMergeKey` 字符串构造 + 两遍 payload 解析（1627/2401 行）。
- 方案：合并槽内的 delta 累积改 `[]byte`（或槽内 `strings.Builder`），出队时一次性 `String()`；merge key 改结构体字段比较（streamID+sequence+类型），不再字符串拼接。
- 验收：单测构造「UI 停滞 + 200 条 5KB delta」场景断言合并期分配总量（`testing.AllocsPerRun`）下降一个量级；发布者 goroutine 在 streamMu 内的最长持有时间（埋点）不随 backlog 深度增长。

## 3. P1：每 delta / 每帧热路径常数项

按「每 delta 固定成本」排序。S3/S4b 已把 unified 主路径的每帧成本与源长基本脱钩（240KB 源 p50 19.9ms → 0.213ms，见 `docs/plan/aicli-render-p3-performance-plan-20261007.md` §5），以下为残留项。

| # | 位置 | 问题 | 影响 | 方案 |
| --- | --- | --- | --- | --- |
| P1-1 | `chat_ui_actor.go:840` → `scene/scene.go:225-237` → `app_state.go:390-400` | 每 delta `sceneSnapshot()` 重建整个 cell 指针切片（O(cells) 分配）+ `ActiveCellFromSnapshot` 从头正扫找 mutable cell；全程持 coordinator `c.mu`。长会话（万级 cell）每 delta 万次指针拷贝 | 高 | mutable cell id 缓存增量推进；`Cells()` 切片复用（contentVersion 递增但 cell 集合多数帧不变） |
| P1-2 | `chat_interaction.go:4127/4131` | 每 delta 2 次 `c.streamBuffer.String()` 全量拷贝；4131 的 `previousContent` 在 unified 分支完全未用 | 中-高 | 4131 直接删；normalize 改按已消费前缀偏移（`streamRenderedPrefixLen` 已存在）做零拷贝切片 |
| P1-3 | `bus.go:333`、`chat_runtime_events.go:1463`、`event_persist_buffer.go:297/506/459`、`orderAssistantDelta` | `ApproximateEventBytes` 同一事件最多独立计算 4 次，第 1 次还在总线锁内 | 中 | 尺寸在事件结构旁路携带一次（Event 加 size 字段或伴生缓存），下游全读缓存值 |
| P1-4 | `loop.go:2382-2393` + `session_runtime_store.go:2629` | payload `content`/`delta`/`text` 三键同串 ⇒ json.Marshal 把同一正文写 3 遍进 SQLite（行字节 ×3，也 ×3 放大 ApproximateEventBytes） | 中 | 序列化层折叠为单键；读侧（`session_runtime_stream_coalesce.go:96-103` 等）做键回退兼容，旧行不动 |
| P1-5 | `active_cell_projection.go:125`、`active_cell_range.go:78` | `markdown.LooksLikeMarkdown(source)` 每帧/每 update 全源线性扫描，一个 delta ≥2 次 | 中 | 结果按 (cellID, sourceLen, 前 1KB hash) 记忆化；或判定改增量（首帧判定后随 delta 只看新段） |
| P1-6 | `renderengine/cache.go:277/95/198-253` | RenderCache 命中路径每次调用：全源 `[]byte` 拷贝算 FNV + 全源 string 比较 + O(maxDocs=256) LRU 线性 touch | 中（legacy band 每帧、Formatter.Format 每调用） | docKey 改「长度 + 首尾采样 hash」两级判等；LRU 改 container/list O(1) touch |
| P1-7 | `chat_interaction.go:6560-6604` | 每个 stable commit 对两个前缀各做一次全量 markdown Format（自注释承认与 history replay 同价），长输出累计 O(n²) | 中-高 | 前缀 Format 结果按 sourceEnd 游标增量复用（与 StreamCollector 同思路），或降频到 commit 节流 |
| P1-8 | `chat_runtime_events.go:3023` + `controller.go:866-899` | 满邮箱重试循环每轮 `actor.Stats()`：两份 256 元素样本环拷贝 + 两次 sort.Slice | 中 | 判断存活/水位用专用原子 bool；Stats 环拷贝改为惰性统计 |
| P1-9 | `chat_runtime_events.go:2431/3077/7115/7866/4858` | `renderMu` 是全局渲染串行点：生产者读 epoch 也要排队等消费者长临界区（encodeRenderModelEvent 持锁做编码 + Scene apply） | 中 | epoch 改 `atomic.Uint64`；编码临界区缩窄到编码器自身 |
| P1-10 | `chat_runtime_events.go:5426-5445` | 被 ownership 拒绝的事件逐条 `json.Marshal` + **同步 debug 文件写**，无限流；stale-epoch 路径同样逐条 | 中 | 限频（复用 `lastPersistWarnAt` 模式）+ 只记 payload 摘要；洪峰时直接计数 |

## 4. P2：turn 粒度与低频路径

| # | 位置 | 问题 | 方案 |
| --- | --- | --- | --- |
| P2-1 | `chat_actor_executor.go:333-341` → `syncRuntimeSessionBackIntoCLI`（654-691） | 每 turn：1 次全量历史 Load（`manager.go:104` 自证整段投影反序列化）+ 2-3 个写事务 + 2 次全量内存复制 + O(history) token 估算 + reconciler 对全部 tool 消息逐条 `json.Unmarshal`（`goal_actor_reconcile.go:11-52`） | 合并三次 sync 为一次；reconciler 只扫本 turn 新增消息（turn 边界有消息游标）；Load 改增量/只读快照 |
| P2-2 | `chat_interaction.go:5159-5180/5218-5250` | 每次按键快照在生产端与消费端各做 2 次全量 `strings.ReplaceAll` 规整，O(n²)/打字会话 | 规整结果随 snapshot 携带一次（生产端规整后置 dirty 标记），消费端跳过 |
| P2-3 | `chat_agent_transcript.go:577` | transcript follow 弹层在总线 goroutine 上同步 append + 重算 `Lines()`——总线订阅者中唯一同步渲染者 | 改投 UI actor 邮箱（与其余订阅者同原则） |
| P2-4 | `markdown/stream.go:193-263` | 开放栅栏/表格期间每个 structural delta 重扫整个 holdback 尾部 + `strings.Split` 全尾行分配（尾部=整个未闭合块，累计 O(n²)） | 扫描改从上次 open 位置增量推进；Split 改 offset 扫描 |
| P2-5 | `bus.go:386-396` | `Recent()/Query()` 每次调用深拷贝最多 512 条留存事件（含 delta 全文）；web 轮询路径每次全量克隆 | 返回只读视图/增量快照（水位号 + 拉取增量） |
| P2-6 | `markdown/renderer.go:23`、`ast_cache.go:93/142-146` | 每次 markdown 渲染 3 份全源 `[]byte` 拷贝（AST 命中仍付） | 渲染器复用 caller 的 []byte；hash 用 P1-6 的两级判等 |
| P2-7 | `chat_runtime_events.go:4798-4844` + `controller.go:806-826` | EndRun 排水 5ms 轮询 + UI actor idle 等 1ms 忙轮询（上限 5s） | 改事件驱动（ticket/applied 水位唤醒），仅发生在 turn 尾，优先级低 |
| P2-8 | `chat_interaction.go:4245-4265` | `EstimateStreamFlushTimeout` 对全文做 rune 扫描，结果 <8s 一律被抬到 8s、>10s 被截断，基本白算 | 直接钳制：`min(max(len/常数, 8s), 10s)`，删 rune 扫描 |
| P2-9 | `active_stream.go:310/465-476/496` | legacy band：plain 路径 `Body += delta` O(n²)；`Buffer.Render` join 结果被丢弃；Diff+LinesEqual 双重全帧比较；FPS 未发射 poll 全源 Split | legacy 为回退路径，按改动成本顺带修 |
| P2-10 | `event_persist_buffer.go:504`、`chat_runtime_events.go:1627/7500` | takeBatch O(n) memmove；每 delta 2 次合并键字符串构造；`rendered` map 只增不删 | 环形化；结构体 key；按 timeline 截断清理 |

## 5. 设计缺口（非单点性能，防回归）

1. **「订阅者必须非阻塞」靠约定维持**：`Bus.Publish` 同步分发，任何未来订阅者在回调里做重活（序列化/渲染/同步 I/O）都会直接反压 SSE 读流。建议给 Publish 加发布耗时 watchdog（单 handler >5ms 限频告警 + 计数暴露到 `/debug/chat/status`），把约定变成可回归检测的不变式。
2. **bridge 单消费者 + critical 无限重排**（`chat_runtime_events.go:973/3037-3043`）：reducer 停摆时 bridge 完全单点，deferred 积压 512 条后按计数丢弃。这是文档化的 liveness 换正确性选择，建议只加遥测（等待时长直方图 + deferred 丢弃计数），不改语义。
3. **transcript 弹层偏离「只入队」原则**（见 P2-3），是唯一例外，收编。

## 6. 实施批次与验收

### 批次 1：保不变式（P0-1/2/3）
收益最大、互相独立、都可单独回滚。预期效果：LLM 回调链上彻底消除同步磁盘 I/O 与锁内深拷贝；UI 停滞场景发布者侧开销从 O(n²) 降到 O(n)。

- 验收基准（新增/扩展 benchmark，进 CI 门禁）：
  - `internal/events`：并发 Publish（4×1000 chunks）allocs/op、锁等待时长；
  - `internal/chat`：EventPersistBuffer 溢出路径——默认配置下生产者 Enqueue p99 ≤10µs、零同步 AppendEvent；
  - `cmd/aicli/commands`：backlog 合并 200×5KB 场景分配量对比。
- 回归保护：现有 `event_persist_buffer_*_test.go`、`runtime_store_append_hotpath_test.go`、`chat_single_writer_fence_test.go` 必须全绿；resume 重放测试确认 delta drop 不影响终态正文重建。

### 批次 2：每 delta 常数项（P1-1～P1-10）
依赖基准挂具：`ui/frame_latency_bench_test.go`（p95<16ms 门禁、历史规模矩阵、分配断言）继续作为门槛；P1-1/P1-2 完成后追加「6719 cells × 500 delta」分配数下降 ≥30% 的门禁。

### 批次 3：turn 粒度与收尾（P2 全部 + §5 缺口加固）
P2-1 收益随会话长度增长（长会话 turn 收尾从 O(history)×N 降为 O(turn)），建议在批次 1/2 稳定后做；P2-3 与 §5.1/5.2 的遥测可先行。

## 7. 明确不做的事

- 不改并发架构本身（actor/邮箱/单 reducer/物理单写端）——该架构是 S3/S4b/P3 多轮收敛的结果，本次审计确认其形态正确。
- 不动 `assistant_message` 等 critical 事件的 block 语义与「liveness 换正确性」的 deferred 丢弃设计。
- 不把 Bus.Publish 改成异步总线——同步分发 + 非阻塞订阅者是当前最简正确解，问题在订阅者与留存成本，不在分发模型。

## 8. 附：审计方法与证据强度

- 三路并行审计（bridge/UI actor、活动流渲染、发布/持久化），关键结论（P0-1 batching 默认关、P0-2 锁内深拷贝、P0-3 合并拼接、merge key 构造、retention 默认 512/8MB）已逐条人工读源复核。
- 量级数据引用自仓库既有实测记录：`docs/plan/aicli-render-p3-performance-plan-20261007.md` §5（S4b 前后对比）、`session_event_payload_cap.go:37`（907MB 会话库 75% 为 delta 字节）。
- 未做项：运行时 profile 实测（pprof 采样佐证各热点占比）。批次 1 动工前建议先跑一轮 `aicli chat --pprof` 长流式会话采样，确认 P0 三项的实际占比排序。

## 9. 实施记录（批次 1 收口，2026-10-11）

### 9.1 提交清单

| 项 | 提交 | 内容 |
| --- | --- | --- |
| P0-1 | `492cf038` | aicli 本地宿主 eventPersist 默认启用 batching+AsyncDispatch；新增 `disable: true` 回滚开关（runtime-server 仍按 `batchingEnabled`）；契约登记 `PersistStreamDelta`（assistant_delta / assistant_reasoning / assistant.reasoning），EventPersistBuffer 溢出按 drop 处理，不再 block 背压 |
| P0-2 | `990d7094` | Bus.Publish 留存条目（ApproximateEventBytes + cloneEvent 深拷贝）移出独占锁；流式 delta 免留存 |
| P0-2 收口 | 本提交 | 订阅列表 copy-on-write（Subscribe/Unsubscribe 重建切片，Publish 锁外直接迭代已发布切片），稳态发布零快照分配 |
| P0-3 | `e346d6e6` | chatStreamAccumulator 缓冲累积合并；成功投递物化进独立 payload 副本（失败尝试零拷贝、与槽位无别名）；mergeStreamEvents 重构为累积器纯函数包装；streamMergeKey 字符串键改结构化比较 |

### 9.2 验收基准实测（对照 §6 批次 1）

| 验收项 | 目标 | 实测 |
| --- | --- | --- |
| Bus delta 发布热路径 | allocs ≤1 | **59ns/op，0 allocs**（单发布者）；**并发 4 发布者 111ns/op，0 allocs** |
| Bus 诊断事件发布 | — | 456ns/op，2 allocs（留存深拷贝，设计内） |
| EventPersistBuffer 溢出 drop 热路径 | p99 ≤10µs，零同步 AppendEvent | **141.9ns/op，2 allocs**，基准断言 singleCalls==0 |
| EventPersistBuffer 非溢出快路径 | 无磁盘 I/O | 390ns/op，0 allocs |
| backlog 合并 200×5KB | 分配较 O(n²) 降一个量级 | 钉子测试上界 ≤20MB（原实现约 100MB 纯 memcpy），单槽折叠 + 投递副本完整文本/无别名断言 |

### 9.3 回归与测试

- `go build ./...` 全仓干净；`internal/events`、`internal/chat`、`internal/config` 全量测试通过；`cmd/aicli/commands` 全量套件（387+ 测试）219s 通过；合并/锁路径 `-race` 通过。
- delta drop 不影响终态正文：drop 仅发生在队列溢出（4096 条/16MB）时，终态 `assistant_message` 承载完整正文，resume 重放测试全绿。
- 测试适配：`chat_actor_host_persist_test.go` 翻转 `Disable` 语义；`TestStreamMergeKey…` 重写为 `streamMergeIdentityEqual`；reasoning 合并测试断言从槽位 payload 迁移到累积器 + 投递副本。

### 9.4 遗留（批次 2+ 承接）

- §3 P1 全部 10 项（每 delta 常数项）与 §4 P2、§5 缺口加固未动工。
- 动工批次 2 前建议先跑 `aicli chat --pprof` 长流式采样确认热点占比排序（§8 未做项）。
