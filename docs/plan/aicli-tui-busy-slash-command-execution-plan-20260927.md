# aicli TUI 运行中 slash 命令执行方案（忙时命令通道 / BusyCommand）

> 状态：Draft v1.3.1（2026-09-27；v1.3 引入业务级分类 + 统一运行时交互机制（§3.8、附录 F）；**v1.3.1 已实现 `/todos` 首个命令**（inline 版：catalog + 结构化处理器 + 忙时排队 + 5 项测试全绿，见附录 F.7 落地状态）；审查报告见 `docs/plan/aicli-tui-busy-slash-command-execution-plan-20260927-review.md`）
> 关联文档：
> - `docs/plan/esc-interrupt-priority-and-loop-robustness-plan-20260918.md` §12（阶段 F 统一输入仲裁：P0~P2b 已实施，P3 待真实终端验证）
> - `docs/plan/aicli-chat-tui-slash-command-completion-plan.md`（slash 目录与补全）
> - `docs/analysis/aicli-tui-owned-render-simplification-plan-review.md`（渲染单写者边界）
> 证据口径：现状结论一律附 `file:line`；未经代码证实的事项标注「待验证」，不作推断。
> 本文档只描述方案与分期，不包含实现。

---

## §0 摘要

**问题**：turn 运行期间（`Interaction.IsReady() == false`），TUI 的 slash 命令要么被静默延迟到回合结束后执行（白名单），要么被直接拒绝（其余）。长回合中 `/status`、`/help` 等查询表现为「无输出、无反应」，用户无法在运行中查看状态。

**目标**：为 slash 命令引入**忙时策略元数据**（`immediate` / `deferred` / `reject`），让只读、无副作用、不需要独占屏幕的命令在 turn 运行中**立即执行并渲染**；状态变更与破坏性命令保持 deferred/reject，语义显式化。

**核心机制**：

1. 在 `chatSlashCommandSpec`（`backend/cmd/aicli/commands/chat_slash_command_catalog.go:19-31`）上新增 `BusyPolicy` 元数据，作为忙时行为的**唯一事实源**，替换现有硬编码白名单 `chatSlashCommandQueueSafe`（`chat_input_queue.go:1244-1284`）。
2. 输入路由结果新增 `chatInputRouteImmediate` 档位；busy capture 通道（`chat_busy_input.go:16-144`）对 immediate 命令不再入队，而是经统一命令互斥立即执行。
3. immediate 命令与在途 turn 的并发契约 = **只读快照 + 统一渲染面 + 效应枚举白名单**：以 P0-1 副作用矩阵（send / actor / 存储 / 屏幕租约 / 输入模式 / 阻塞 IO）判定，不以命令名判定；禁止 send / actor 变更 / 存储写 / 屏幕租约 / modal。
4. 命令互斥采用**两阶段锁**（§3.3）：锁只覆盖 Phase A（解析 + 会话变更 + 渲染），绝不跨 `sendMessage` 等 Phase B 效应；busy 侧 tryLock 失败即降级 deferred，capture 循环永不阻塞。
5. 执行位置固定在 busy capture goroutine（L1），纳入既有输入仲裁模型，不新增 stdin 所有者；**Web/远程来源一律不进入 immediate 通道**（B1），避免「回执 queued 但无人执行」的静默吞命令。

**非目标**：

- 不把主循环重构为异步事件循环（`sendMessage` 保持同步阻塞语义）。
- 不放行状态变更、破坏性、退出、需要 alternate screen / picker / 输入模式切换的命令。
- 不改变空闲（Ready）路径的任何行为。
- 不改变 turn 的中断、排队消息、actor 生命周期语义。
- ACP/stdio slash 路径（`agent_stdio_slash.go:94`）不在本方案范围：它没有 TUI busy capture 通道（N2）。

---

## §1 现状与证据

### 1.1 链路

| 环节 | 代码锚点 | 当前行为 |
| --- | --- | --- |
| 主循环读入 | `chat.go:1646` `chatInteractiveReadLine` | 单 goroutine 顺序执行 |
| slash 分支 | `chat.go:1762-1783` | 先过 `chatInputCommandAllowed`（要求 Ready），再 `dispatchChatCommand` |
| turn 入口 | `chat_send.go:74-75`、`chat_send.go:121` | turn 开始启动 busy capture；`executor.Execute` 同步阻塞至回合结束 |
| 忙时输入接管 | `chat_busy_input.go:16-144` | busy capture goroutine 独占 stdin，逐行经 `queue.routeInputText` 路由 |
| 命令门 | `chat_input_queue.go:1221-1229` | `chatInputCommandQueuable`：Ready 全放行；忙时仅白名单可排队 |
| 白名单 | `chat_input_queue.go:1244-1284` | `provider/model/help/?/status/session/history/h`、`/debug` 只读子命令、`/supervision` 只读子命令、`/routing show|doctor`、`/queue`（非 clear） |
| 拒绝反馈 | `chat_busy_input.go:146-160` | 「Agent 正在运行，slash 命令 %q 未执行，也未加入消息队列；请等待状态回到 Ready 后重试」 |
| 队列消费 | `chat_input_queue.go:1138-1146` | 仅当主循环回到读输入时消费——即 turn 结束后 |
| 路由实现 | `chat_input_queue.go:1673-1696` | 三态：`queued` / `priority` / `rejected`，无「立即执行」档 |
| 路由结果类型 | `chat_input_queue.go:35-53` | `chatInputRouteDisposition` 只有 `Queued/Priority/RejectedCommand` |
| 统一执行入口 | `command.go:24-179` | `tryExecuteStructuredChatCommand` → 渲染命令结果 → picker/overlay/send 等副作用 |
| 忙时渲染先例 | `chat_busy_input.go:128`（`RefreshStatus`）、`:153/:156`（`RenderLocalSupplement`） | busy capture goroutine 已在 turn 运行中直接渲染 |
| 仲裁器 | `chat_input_arbitration.go:20-27`、`esc-interrupt...plan-20260918.md` §12 | L0 modal > L1 busy capture > L2 ESC > L3 editor；P2b 单写者灰度开关 |

### 1.2 事实归纳

1. **主循环在 turn 期间完全不执行命令**：`sendMessage` 阻塞（`chat_send.go:121`），命令只能由 busy capture goroutine 旁路处理，或等回合结束后由主循环从队列取（`chat_input_queue.go:1141-1146`）。
2. **白名单命令是「延迟」而非「拒绝」**：语义上安全，但用户无即时反馈；长回合下体感等同失效。
3. **非白名单命令直接拒绝**：包括 `/clear`、`/exit`、`/new`、`/debug on|off|export` 等（由 `chat_debug_display_busy_repro_test.go:34-38` 锁定）。
4. **旁路渲染已有先例**：busy capture goroutine 已直接调用渲染协调器（`chat_busy_input.go:128,153-159`），说明渲染面允许 turn 期间的非主循环写入——具体锁粒度在 P0 核查（§5 P0-2）。
5. **会话字段无通用互斥**：`ChatSession`（`chat.go:121-254`）仅有少量专用锁（`skillInvocationSeenMu`、`actorWarmupMu`、`runtimeCtxMu`、仲裁器等），turn 期间被 executor goroutine 写入的字段（`Messages`、`TokenCount`、`ContextTokenCount`、`queuedInputDrain` 等）不与旁路读者互斥。这是 immediate 通道的**头号风险**，必须由只读快照契约解决（审查 M4 已实证 `/queue` 依赖的 `queuedInputDrain` 为裸读，写方在 `chat_team_drain.go:117-138`）。
6. **屏幕租约唯一**：picker / overlay / 备用屏路径集中在 `command.go:50-125` 的 `Open*Picker/OpenOverlay/Open*Screen` 效应后置处理，忙时并发执行会与在途 turn 的渲染争物理屏幕。

---

## §2 设计目标与不变式

### 2.1 设计目标

| 编号 | 目标 |
| --- | --- |
| G1 | 运行中（非 Ready）执行只读 slash 命令，命令结果立即可见，不等待回合结束 |
| G2 | 忙时行为由命令元数据单一事实源决定，新增命令必须显式声明策略（fail-closed） |
| G3 | immediate 命令不打断、不改变、不干扰在途 turn 的会话/渲染/actor 状态 |
| G4 | deferred 语义显式化：排队命令给出「已排队，将在当前回合结束后执行」反馈 |
| G5 | 空闲（Ready）路径与现有 deferred/reject 测试矩阵零行为变化 |
| G6 | 执行位置与既有输入仲裁模型（L0~L3）一致，不新增 stdin 所有者 |

### 2.2 不变式（实现与测试必须保持）

- **INV-1（两阶段单写者）**：任一时刻只有一个命令执行者处于 Phase A（解析+会话变更+渲染）；`commandMu` 只覆盖 Phase A，**禁止跨 Phase B 效应（`sendMessage`/交互等待）持锁**（§3.3）。busy 侧用 `TryLock`，失败即降级 deferred；capture 循环不得阻塞在锁上。
- **INV-2（生效域约束 + 统一面）**：运行时交互按**生效域**放行（§3.8.1）：`read` / `next-turn` / `next-call` 允许运行时执行；`session`（transcript/会话替换）只允许 `queue`；`process`（退出）只允许 `block`。仅当 `unifiedDirectInteractiveOutput(session)` 成立时放行；任何模式都不得调用 `sendMessage` 派生新 turn、不得触发 `CommandQuit`、不得落入 legacy `handleCommand`（`command.go:172-178`）。
- **INV-3（不打断）**：运行时交互失败/取消不得取消在途 turn；turn 结束与交互执行并发时，命令至多被执行一次（§3.4 交接协议）。
- **INV-4（渲染单数据面）**：运行时命令输出（含副屏交互结果）只能经统一命令结果/租约渲染入口提交；禁止 `fmt.Print`/直接 stdout（与统一渲染迁移约束一致）。
- **INV-5（上下文隔离）**：命令文本与输出不进入模型消息上下文（与现有 slash 命令语义一致）。
- **INV-6（fail-safe 分类）**：catalog 中未声明运行时策略的命令一律按 `queue`（排队、不丢输入）处理；任何解析失败都不得向上晋升为 inline/screen/prompt，也不得静默丢弃。
- **INV-7（仲裁优先级）**：L0 modal / priority prompt 活跃期间不执行 inline/screen/prompt 交互（一律降级 queue），避免与审批/提问的输入所有权冲突；判定用 `chatInputArbitrationSnapshotOf`（`chat_input_arbitration.go:226-237`），`ok=false` 时不得放行（fail-closed）。
- **INV-8（来源隔离）**：`routeInputTextFromSource`（`chat_input_queue.go:498-509`）的非本机终端来源（web/远程/未知）一律不进入 immediate：降级 deferred（保持排队与唤醒语义）；不得让调用方收到「已排队」而实际无人执行（B1 证据：`web_handlers.go:990-1024`、`web_invoke.go:345-363`）。
- **INV-9（单一注册与统一宿主）**：所有命令的运行时行为由唯一注册表声明（业务域/模式/生效域/确认/开关，§3.8.2），执行统一经 `runtimeCommandHost.Submit`；禁止在 busy capture 内散点特判命令名。未登记命令默认 `queue`（不丢输入）。
- **INV-10（三级开关与降级）**：全局/分类/命令三级开关可独立关闭运行时交互（§3.8.3）；关闭或任一安全门不通过时按 `inline→queue`、`screen→queue`、`prompt→queue` 降级（`session` 保持 queue、`process` 保持 block），并给出显式提示。

### 2.3 现状问题与新语义对照

| 场景 | 现状 | 目标 |
| --- | --- | --- |
| 运行中输入 `/status` | 入队，回合结束后执行 | immediate：立即渲染状态文档 |
| 运行中输入 `/help` | 入队 | immediate |
| 运行中输入 `/debug display` | 入队；若为 overlay 视图则回合结束后打开备用屏 | 首期保持 deferred；P2 视 inline 降级能力提为 immediate（D1） |
| 运行中输入 `/model status` | 入队 | immediate |
| 运行中输入 `/model <x>` | 拒绝 | deferred：排队并提示「将在当前回合结束后执行」（D2）或维持 reject |
| 运行中输入 `/clear`、`/exit`、`/new` | 拒绝 | reject（不变，提示保留） |
| 运行中输入 `/queue` | 入队（仅查看） | immediate（查看）；`/queue clear` 维持 reject |
| 运行中 Web/远程注入 `/status` | 入队并唤醒主循环 | 保持 deferred（INV-8 来源隔离；不允许 Web 侧出现「queued 但无人执行」） |

---

## §3 设计

### 3.1 BusyPolicy 元数据（单一事实源）

在 `chatSlashCommandSpec`（`chat_slash_command_catalog.go:19-31`）新增字段：

```go
type chatSlashCommandBusyPolicy uint8

const (
    // 未声明/未识别：忙时拒绝（fail-closed）
    chatBusyCommandReject chatSlashCommandBusyPolicy = iota
    // 忙时排队，回合结束后由主循环执行
    chatBusyCommandDeferred
    // 忙时立即执行（只读、无副作用、无屏幕租约）
    chatBusyCommandImmediate
    // 忙时在备用屏（副屏）执行只读交互（分页/查看器/选择浏览；§3.7）
    chatBusyCommandScreen
)

type chatSlashCommandSpec struct {
    // ... 现有字段不变
    BusyPolicy chatSlashCommandBusyPolicy
}
```

解析函数（替换 `chatSlashCommandQueueSafe` 的字符串 switch）：

```go
// 按 catalog（含别名）解析命令的忙时策略；未登记命令返回 reject。
func chatSlashCommandBusyPolicyFor(text string) chatSlashCommandBusyPolicy
```

约束：

- catalog 是命令名/别名的唯一解析入口；`/debug`、`/supervision`、`/routing` 的子命令策略在 spec 解析后按子命令细分（延续现有 `chatDebugSubcommandQueueSafe` 等函数的语义，但改为返回四态）。
- 单测锁定：catalog 中每个 spec 必须显式给出策略；任何未在 catalog 中登记但能被 `dispatchChatCommand` 识别的命令（需要盘点）默认 reject 并在测试中列为已知例外清单（防止「实现了命令但忘了分类」）。
- 帮助/补全渲染可展示忙时策略（可选，P3）。
- **判定依据是效应枚举而非命令名**：`BusyPolicy` 必须由 P0-1 副作用矩阵（send / actor / 存储 / 屏幕租约 / 输入模式 / 阻塞 IO）推导；即使命令名看起来只读，只要 Ready 路径存在任一被禁效应（如裸 `/model` 在可交互时会走 picker，见 `chat_model_picker.go:443-455`），就不得标 `immediate`。
- **四态解析（I/S/D/R）与旧白名单逐条等价**（灰度关闭时）：旧白名单只有 D/R 两态，映射到新 resolver 后逐条对照，用「resolver × `chatSlashCommandQueueSafe`」表驱动测试锁定（§6 T18）；`/queue` 未知参数的两处不一致（白名单放行 `chat_input_queue.go:1251-1252` vs handler 报错 `chat_command_result.go:947-961`）一并收敛（N3）。
- **S 档（screen）的边界**：只承载「只读交互」——查看/分页/浏览/选择后仅产生草稿或回执；任何确认后写会话状态、写存储、写配置、开新 turn 的屏幕交互一律仍为 D/R。S 档必须 fail-closed 降级（§3.7.3）：无备用屏能力/租约超时/缓冲超限 → I（内联快照）或 D（排队）。
- 四档解析与分类定稿见附录 E；`reject` 为所有未登记命令与未知子命令的默认值。

### 3.2 路由四档与执行位置

1. `chatInputRouteDisposition` 新增 `chatInputRouteImmediate` 与 `chatInputRouteScreen`（`chat_input_queue.go:35-41`），并加 `immediate()` / `screen()` 访问器。
2. `chatInputQueue.setCommandGate`（`chat_input_queue.go:780-787`）改为策略解析器 `setCommandPolicyResolver(func(string) chatSlashCommandBusyPolicy)`；`routeLineWithCommandGate`（`:1673-1696`）按策略分支：
   - `immediate`（且来源为本地终端，见第 5 点）→ 返回 `chatInputRouteImmediate`，原子标记该行已被 busy 通道占有，**不入队**；
   - `screen`（同样限本地终端）→ 返回 `chatInputRouteScreen`，原子占有后交由 BusyScreen 通道（§3.7）；
   - `deferred` → 入队（现行为）；`reject` → `chatInputRouteRejectedCommand`（现行为）。
   - `confirmDraft`/`rejectCommandInput`（`chat_input_queue.go:592,810-818`）同步迁移到 resolver；immediate 在草稿确认路径不得被当作 rejected（N1）。
3. busy capture 在收到 `immediate` 后调用新入口（同 goroutine），并显式补齐现有 queued 分支负责的副作用（历史策略见 §3.5、echo/状态行见 M3 修订）：

```go
// chat_busy_input.go 捕获循环内
if result.immediate() {
    executeBusySlashCommand(session, line) // Phase A 持锁 + TryLock 失败降级 + 统一渲染
    continue
}
if result.screen() {
    openBusyScreenCommand(session, line) // §3.7：租约 + 输入移交 + 只读交互 + 恢复
    continue
}
```

4. 执行函数骨架：

```go
func executeBusySlashCommand(session *ChatSession, line string) {
    // 1) 再校验：策略仍为 immediate（防御 catalog 竞态）
    // 2) 统一面门：!unifiedDirectInteractiveOutput → deferred（INV-2 / M2）
    // 3) L0 门：chatInputArbitrationSnapshotOf 的 ModalDepth>0 或 ok=false → deferred（INV-7）
    // 4) Phase A：commandMu.TryLock；失败 → 降级 deferred 并提示，capture 不阻塞
    // 5) 忙时安全断言：命令结果不得含 send/picker/overlay/screen/quit 效应，否则降级并记录诊断
    // 6) dispatchChatCommand 的 Phase A（解析 + 渲染）；锁由内部获取/释放，不跨 Phase B
    // 7) 执行成功后补记输入历史（§3.5）
}
```

5. **来源分流（INV-8）**：仅当 `chatInputSourceIsLocalTerminal(source)`（`chat_input_queue.go:482-489`）成立时才可能返回 immediate；web/远程/未知来源一律 deferred，保持 `web_handlers.go:990-1024` 的「排队 + 唤醒 + `{"status":"queued"}` 回执」语义不变（B1）。
6. 主循环不新增外层锁（`chat.go:1714/1773` 保持原调用）：两阶段锁由 `dispatchChatCommand` 内部实现（§3.3），避免跨 `sendMessage` 持锁（B2）。

### 3.3 命令互斥与并发契约（两阶段锁）

**新增 `ChatSession.commandMu sync.Mutex`**（命名已核查：全仓无 `commandMu`/`cmdMu`/`dispatchMu` 冲突）：

- **Phase A（持锁）**：结构化命令解析、会话/配置变更、命令结果渲染（`resolve + mutate + render`）。锁在该阶段内获取，**在应用 Phase B 效应之前释放**。
- **Phase B（不持锁）**：turn 触发类效应（`SendObjective`/`SendMessageAfterCommit`/`SendSkillTurn`，`command.go:132-155` → `sendGoalObjectiveRequest`/`sendChatMessageAfterCommit`/`sendSkillTurnRequest` → `chat_send.go:74-75,121` 同步跑完整 turn）与屏幕/交互类效应（picker/overlay/screen）。这些是「主循环在 Ready 下正常开新 turn」的既有语义，**必须允许 busy capture 在其运行期间继续执行 immediate 命令**。
- **实现方式**：把 `dispatchChatCommand` 收敛为「Phase A + Phase B 效应应用」两段（B2 修订）；不接受在 `chat.go:1714/1773` 外层持锁——那样 `/goal`、`/shell`、`!cmd`、`/skill` 触发的新 turn 会把 busy capture 的 immediate 执行阻塞到 turn 结束（G1 失效），并堵死 capture 的输入读取与 Esc 处理。
- **busy 侧**：`executeBusySlashCommand` 对 Phase A 用 `TryLock`；失败（主循环正在 Phase A/B）→ 入队降级 deferred 并给出提示；**capture 循环永不阻塞在锁上**。
- **禁止锁内重入**：生产调用点已核实仅 `command.go:29`、`command.go:178`（`handleCommand`）、`chat.go:1714/1773`、`agent_stdio_slash.go:94`（ACP，范围外）；picker/overlay 不回调 `dispatchChatCommand`（附录 D）。仍需测试断言防回归。

**immediate handler 并发契约（强制）**：

| 规则 | 说明 |
| --- | --- |
| 只读快照 | handler 依赖的会话语义字段必须来自快照函数（§5 P0-2），不得直接读 turn 期间会被写入的字段 |
| 无发送 | 不调用 `sendMessage`、`sendGoalObjectiveRequest`、`sendSkillTurnRequest`、`sendChatMessageAfterCommit` |
| 无状态变更 | 不修改 `PermissionMode`、`Config`、actor、profile、team、`Messages` |
| 无屏幕租约 | 不去 `Open*Picker/OpenOverlay/Open*Screen`；渲染结果中若检测到这些效应 → 视为分类错误，降级为 deferred 并记录诊断 |
| 无阻塞等待 | 不做长时间阻塞 IO / 子进程 / 网络等待（`/debug`、`/usage` 等读取本地状态；确需 IO 的命令维持 deferred） |
| 幂等渲染 | 重复读取不产生会话日志副作用（与只读语义一致） |
| 统一渲染面 | 仅 `unifiedDirectInteractiveOutput` 会话放行；否则 deferred，禁止落入 legacy `handleCommand` 直写 stdout（M2） |
| 有界本地读取 | 允许 `/status` 的有界本地 `os.Stat`（`chat_status.go:831`）等即时读取；网络/子进程/全量磁盘扫描禁止 |
| 屏幕租约（仅 S 档） | 仅 S 档可经 BusyScreen 生命周期获取备用屏租约（§3.7）；I 档仍严格禁止；S 档交互期间**不持有 `commandMu`**，只在打开/关闭的短临界区持锁 |

### 3.4 回合结束交接协议（INV-3，原子占有）

窗口：immediate 命令执行期间 turn 结束（`sendMessage` 返回 → `IsReady()` 变 true → capture 即将退出）。

处理（消除 `IsReady()` 检查与 `requeueFront` 之间的 TOCTOU）：

1. **原子占有**：路由在 `routeLineWithCommandGate` 的 `routeMu` 临界区内完成「判定策略 + 决定去向」，immediate 结果即代表该行已被 busy 通道占有；未占有的行走队列。
2. **占有后必达**：已占有的命令即使执行前 `IsReady()` 翻转为 true，也照常在 busy 通道完成（只读命令在 Ready 下执行同样安全），保证「至多一次、且一定可见」；不做「先检查再回填」的两步操作。
3. **未占有回执**：`TryLock` 失败或安全门（统一面/仲裁/来源）不满足时，在入队前后给出唯一去向（deferred），不产生重复执行。
4. 执行失败不产生 turn 错误；以 `RenderLocalSupplement` 输出失败原因（如「命令在忙时不可用，已排队等回合结束」）。

### 3.5 渲染、历史与队列状态

- **渲染**：走 `renderChatCommandResult`（`command.go:34`）→ `RenderCommandDocument`（`chat_interaction.go:4612-4648`，`c.mu` + `beginMessageLocked` 串行提交）；渲染可用性已获证，**P0-2 只缩窄为「与流式 delta 的 Scene 顺序/背压」验证**（审查 c）。渲染失败降级为 `RenderLocalSupplement`，不 panic、不影响 turn。
- **输入历史（M3）**：immediate 命令执行成功后经 `recordChatPromptHistory`（`chat_input_queue.go:1177-1182`）写入历史（与 Ready 下已执行命令的语义对齐）；现有 queued slash 命令不写历史的行为（`chat_busy_input.go:131-133` 仅非 slash 入历史）保持不变，并在 T1/T10/T19 中显式断言。
- **echo 与状态行（M3）**：immediate 分支需显式定义 `queuedInputEchoed` / `RefreshStatus` 行为——建议执行前刷新状态行以呈现命令输出，不置 queued echo（未入队）。
- **队列状态（M4）**： `/queue` 读取的 `queuedInputDrain`（`chat_input_queue.go:1420`，写方 `chat_team_drain.go:117-138`）必须收进快照或改原子读写，否则 T9 `-race` 先红。
- **提示文案版本化（M5）**：deferred 排队提示属于 P3 变更；P1/P2 灰度关闭与打开时均保持现有文案，T7 断言「零行为差异」。
- 忙时提示补充：命令执行时状态行可短暂标注 `[busy-command]`（复用 `RefreshStatus`，`chat_busy_input.go:128` 先例），P3 可选。

### 3.6 与统一输入仲裁（阶段 F）的关系

- immediate 执行发生在 L1 busy capture 内部，**不注册新 owner**；不改变 `chatInputArbitrator`（`chat_input_arbitration.go:20-118`）层级，也不触碰 KeyHandler 位（`chatInputArbitrationDesiredKeyHandlerState:155-160`）。
- L0（modal/priority prompt）活跃时由队列的 priority 模式接管（`routeLineWithCommandGate:1684-1687` 已先于命令门），immediate 自然不可达；「priority 已翻转但 modal 未清理」的窄窗口可达，需在 `executeBusySlashCommand` 用 `chatInputArbitrationSnapshotOf`（`chat_input_arbitration.go:226-237`）二次确认：`ModalDepth > 0` **或 `ok=false`** → 降级 deferred（INV-7，fail-closed）。
- 待阶段 F P3 删除散点路径后，本方案的 immediate 通道不依赖 `KeyHandler` 位，只依赖 capture 循环本身，兼容 P2b 单写者模式。

### 3.7 副屏命令交互（BusyScreen，S 档）

> 依据（独立调查，2026-09-27）：`ScreenLease` + DEC 1049 + presenter recovery 已闭环（`ui/screen_lease.go:41-236`、`ui/terminal_session.go:536-578/870-878`、`ui/controller_state.go:125-164`）。租约活动期间主屏事务被物理 `Deferred`、`HistoryEffects` 冻结；释放后 `ExitAlternateScreen` → `LeaseReleased` → `RequestPrimaryRecovery` 触发全量重绘。**可行性：中高**；前置改造 4 项（租约等待、显式暂停/恢复、流式缓冲、输入所有权移交）。

#### 3.7.1 生命周期

1. **请求**：busy capture 收到 `chatInputRouteScreen` → 构造 `BusyScreenRequest`（命令、来源、预算）。
2. **租约获取**：新增 `AcquireAlternateScreenWait(ctx, budget≤2s)`（现有 `AcquireAlternateScreen` 撞租约立即返回 `ErrScreenLeaseBusy`，`screen_lease.go:163-167`）；超时/失败 → 降级 I 或 D。
3. **输入所有权移交**：登记 `chatInputOwnerModal`（仲裁 L0）；让出 busy capture 的 stdin 读取（复用 priority prompt 的「取消当前读 → 切换 prompt」路径，`chat_busy_input.go:52-96`），capture 在屏幕关闭前不得消费按键；KeyHandler 保持 Suspend。
4. **运行只读交互**：复用 `SelectFullScreenListWithLease` / `RunTranscriptPagerWithLease` / `RunDebugOverlayWithLease`；所有写入必须经受租 transport，禁止 raw stdout（`screen_lease.go:271-276` fail-closed）。
5. **关闭与恢复**：post Close barrier → `lease.Release` → `LeaseReleased` → `RequestPrimaryRecovery`；capture 重新接管 stdin；释放部分失败（`terminal_session.go:641-648` 置 projection unknown）走 reset+replay 兜底（`terminal_session_executor.go:1023-1030`）。
6. **跨 turn 边界**：屏幕允许跨回合结束继续交互；主循环恢复读输入前必须等待屏幕关闭（新增会话级 `busyScreenActive` 门 + 仲裁 L0 检查），避免主循环 composer 与副屏争 stdin。

#### 3.7.2 流式缓冲契约（新增）

- 租约期间流式 delta 仍写 AppState/active 状态，主屏帧被 `Deferred`（`terminal_session.go:792-800/870-878`）；**租约期 delta 缓冲是否有界未证实**。
- 新增 `StreamHold(leaseID)` / `StreamFlush(leaseID)` 契约：租约内只累积**有界** delta 索引（上限触发「恢复后全量重绘」，不保留增量）；释放后统一 Flush，默认走全量重绘（screen Invalidate → `projectionKnown=false`，`terminal_session.go:897-907`）。
- 量化门槛：缓冲上限与 Flush 时限进入 P0-2 验证清单（U1）。

#### 3.7.3 降级矩阵（fail-closed）

| 条件 | 降级 |
| --- | --- |
| 非交互 TTY / `CanUseFullScreenList=false`（`fullscreen_list.go:144-150`） | I（内联文档快照）或 D |
| 无 `OwnedViewport` / 非统一渲染面 | D |
| 租约获取超时或被占用 | D（提示「回合结束后查看」） |
| 流式缓冲超限 / 恢复失败 | 关闭副屏 + 全量重绘；结果降级为内联文档 |
| winpty / MobaXterm / SSH 管道 | D（fail-closed；T26） |

#### 3.7.4 首批 S 档白名单（只读交互，P2 落地）

`/history`（分页器）、`/usage`（viewer）、`/debug display`（overlay）、`/web endpoints`（overlay）、`/account --no-refresh|show`（缓存快照屏）、`/todos`（任务面板，支持视图切换，见附录 F.7）。

其余屏幕命令在 **v1.3 起按 §3.8 升级**：`session` 生效域（裸 `/resume`、`/backtrack picker|apply`）仍为 `queue`；`next-turn`/`next-call` 生效域的写选择（裸 `/model`、`/provider`、`/theme select`、`/skills enable|disable`、`/routing` 面板、`/profile pick`）走 `screen+confirm`，确认后按生效域登记/应用；网络与子进程类（`/login`、`/mcp reload|auth`、`/image`、`/web open`、`/call`、`/shell`）保持 `queue`。任何写选择必须显示生效时机并写审计事件。

#### 3.7.5 与 priority prompt / approval 的关系

审批/提问已是「忙时模态抢占」（`chat_surface_output.go:215,239-251`），但呈现为底栏 popup 且语义必须即时；BusyScreen 不替代它，仅服务用户主动发起的 S 档命令。两者共用仲裁 L0 与 priority 读行通道，互斥由仲裁器保证；L0 活跃期间 S 档请求降级 D（INV-7）。

### 3.8 统一运行时交互机制（RuntimeInteractionHost，v1.3）

> 设计立场：**默认允许运行时交互**（不丢输入、不阻塞 capture）；只对「破坏/替换当前会话状态或派生新 turn」的命令收敛为排队，对 `/exit` 收敛为阻断。命令差异不散点判断，由注册表声明、统一宿主执行。本节取代 §3.7.4 的「S 档只读」限制。

#### 3.8.1 两个正交维度

**交互模式（怎么交互）**

| 模式 | 说明 | 载体 |
| --- | --- | --- |
| `inline` | 主屏内联命令 cell（只读快照/轻量计算） | `RenderCommandDocument` |
| `screen` | 备用屏交互（查看器/分页器/picker/表单/预览） | BusyScreen（§3.7） |
| `prompt` | 优先行确认/单选/简短输入 | priority prompt（复用审批/提问通道） |
| `queue` | 排队到 Ready 执行（不丢输入） | `InputQueue`（现行为） |
| `block` | 忙时拒绝（最小集） | 拒绝 + 提示 |

**生效域（何时生效）**

| 生效域 | 语义 | 例子 | 运行时要求 |
| --- | --- | --- | --- |
| `read` | 无任何写入 | `/status`、`/history` | 只读快照 |
| `live` | 当前 turn 即时生效（显示面/热刷新/投递/控制面写，不改写 transcript） | `/stream`、`/fast`、`/reasoning`、`/theme`、`/skills enable`、`/mcp reload`、`/supervision ack`、`/agents send` | 提示生效范围；热刷新类注明「对后续请求生效」 |
| `next-turn` | 登记变更，回合边界后生效（actor 重建/策略面） | `/model`、`/provider`、`/reasoning_effort set`、`/profile use`、`/routing on`、`/add-dir` | 复用 `actorRebuildPending` + `refreshLocalRuntimeAfterSelection`（第三轮核实）；提示「下一回合生效」 |
| `next-call` | 对后续工具调用生效（权限/授权判定通道） | `/permission-mode`、`/yolo`、`/trust grant`、`/grants revoke`、`/approval-reuse` | 复用 `withLivePermissionModeSource`（第三轮核实）；必须 `prompt` 确认 + 审计 + 提示「对后续工具调用生效」 |
| `session` | 替换/破坏会话或 transcript | `/clear`、`/new`、`/load`、`/resume`、`/compact`、`/backtrack apply` | 只允许 `queue` |
| `process` | 进程级动作 | `/exit` | 只允许 `block`（v1.3 唯一硬阻断；是否改为「回合后退出」见 D11） |

不变式：**任何运行模式都不得在 `session`/`process` 生效域上执行**；宿主层拒绝（不可达），handler 层再做防御断言（T28 扩展）。

#### 3.8.2 注册表（单一事实源）

```go
type runtimeInteractionMode uint8 // inline | screen | prompt | queue | block
type runtimeEffectScope uint8     // read | next-turn | next-call | session | process
type commandCategory uint8        // C1..C12 业务域（§3.8.5）

type runtimeCommandSpec struct {
    Command   string                 // 主命令名（别名归并）
    Category  commandCategory
    Mode      runtimeInteractionMode
    Effect    runtimeEffectScope
    Confirm   bool                   // prompt/screen 是否需要显式确认
    Notice    string                 // 生效域提示模板（如「下一回合生效」）
    SwitchKey string                 // 分类/命令级开关键
}
```

- 注册入口：catalog `chatSlashCommandSpec` 扩展 + 复合命令子命令解析器（`/debug`、`/supervision`、`/agents`、`/routing`、`/plan`、`/model` 等按变体返回多份 spec）。
- 未登记命令/未知子命令 → `queue`（fail-safe，不丢输入）；`block` 仅出现在显式注册。
- 单测锁定注册表全覆盖 + 与 catalog 全集差集检查（§6 T29）。

#### 3.8.3 三级开关与灰度

| 层级 | 开关 | 语义 |
| --- | --- | --- |
| 全局 | `chat.runtime_interaction.mode`（env `AICLI_CHAT_RUNTIME_INTERACTION`） | `auto`（默认，按注册表）/ `readonly`（仅 `read` 可运行时执行，其余 queue）/ `off`（全部 queue，等价 v1.0 行为） |
| 分类 | `chat.runtime_interaction.categories.<C#>` | 单业务域关闭 → 该域全部降级 queue |
| 命令 | `chat.runtime_interaction.commands."/model"` | 单命令（含子命令变体）覆盖 |

解析优先级：命令级 > 分类级 > 全局；任一级关闭按 INV-10 降级并记录诊断事件。`block` 集只增不减需评审。

#### 3.8.4 宿主执行流程

```go
func (h *runtimeCommandHost) Submit(req runtimeRequest) runtimeOutcome {
    spec, ok := resolveRuntimeSpec(req.Line)      // 注册表 + 别名/子命令
    if !ok { return h.enqueue(req, spec) }        // 未登记 → queue
    if !h.switchEnabled(spec) { return h.degrade(req, spec) }
    if !h.gatesPass(req, spec) { return h.degrade(req, spec) } // 来源/统一面/仲裁/预算
    switch spec.Mode {
    case modeInline: return h.runInline(req, spec)   // Phase A（两阶段锁）+ 内联渲染
    case modeScreen: return h.runScreen(req, spec)   // BusyScreen 租约 + 查看器/picker（§3.7）
    case modePrompt: return h.runPrompt(req, spec)   // priority line 确认/选择
    case modeQueue:  return h.enqueue(req, spec)
    case modeBlock:  return h.reject(req, spec.Notice)
    }
}
```

统一职责：① `applyRuntimeEffect` 按生效域执行/拒绝（`session`/`process` 不可达）；② 按 `Notice` 模板提示生效时机；③ 每次交互发布统一审计事件（命令/模式/生效域/结果/耗时）；④ screen/prompt 的用户交互阶段不持有 `commandMu`（仅在 Phase A 短临界区加锁）；⑤ 任一安全门失败 → queue + 提示。

**适配器清单（第三轮核实，2026-09-27）**

| 生效域 | 适配机制 | 代码锚点 |
| --- | --- | --- |
| `live` | 输出/显示直接写；技能与 MCP 热刷新；actor mailbox 投递；监督 CAS 写 | `refreshSkillsRuntimeBinding`（skills_integration.go:1232）、`refreshChatMCPTools`（chat_mcp_command.go:61）、mailbox 投递（chat_debug.go:571-573）、`GrantTrust`（chat_folder_trust.go:224） |
| `next-turn` | **actor 延迟重建**：登记 pending → 回合边界 reconcile；含 submit-in-flight 兜底 | `actorRebuildPending`（chat.go:216-223）、`markPendingChatActorRebuild`/`reconcilePendingChatActorRebuild`（chat_profile_switch.go:311-367）、`refreshLocalRuntimeAfterSelection`（chat_actor_host.go:1477-1504） |
| `next-call` | **实时权限源**：每次工具评估读取 live 值，无需回合边界 | `withLivePermissionModeSource`（chat_actor_executor.go:569-581） |
| `session` / `process` | 宿主拒绝（不可达） | §3.8.1 不变式 + T32 |

**prompt 载体契约（第三轮核实）**：priority prompt 单行文本通道可复用（`showChatRuntimePriorityPrompt`，`chat_surface_output.go:209-267`；读行 `chat_input_queue.go:1334-1352`），yes/no 与枚举由调用方解析文本（复用 `parseApprovalPromptDecisionInput`/`mapQuestionSuggestionAnswer`）。约束：① 必须由读方 goroutine（busy capture）发起，popup begin/cleanup 与仲裁登记须同 goroutine；② 读取期间持有 `priorityPromptMu`（不可重入），host 必须串行化同类请求；③ 通道无内建超时，host 必须自加预算与取消路径；④ `JSONOutput/NoInteractive` 下输出层 no-op 但读行仍可能发生，host 必须在调用前预检并给默认值/错误；⑤ 通道当前无统一审计事件，host 自行发布诊断事件。

**screen 载体契约（第三轮核实）**：`SelectFullScreenListWithLease`（含 `FreeTextMode` 单值输入）即通用「单选/短输入」宿；`RunTranscriptPagerWithLease`、`RunDebugOverlayWithLease` 为查看器宿；租约三步协议（Acquire → Open barrier → Run → Close barrier → Release）已由 `chatPickerOpen/Close` 提供（`chat_picker_common.go:174-204`），host 直接封装复用。

#### 3.8.5 业务域（C1~C12）与默认策略

| 域 | 业务域 | 默认模式 | 默认生效域 | 覆盖命令（摘要） |
| --- | --- | --- | --- | --- |
| C1 | 会话生命周期与破坏类 | `queue`（`/exit`=`block`） | `session`（`/exit`=`process`） | `/clear`、`/new`、`/load`、`/resume`、`/compact`、`/backtrack apply`、`/exit` |
| C2 | 回合、目标与计划 | 只读 `inline`；变更 `prompt`；派生 turn `queue` | `next-turn` / `session` | `/goal`、`/plan`、`/plans`、`/retry` |
| C3 | 会话元数据与历史 | `inline` / `screen` | `read` | `/session`、`/sessions`、`/title`、`/export`、`/history` |
| C4 | 模型与路由 | status `inline`；切换/面板 `screen`+confirm | `next-turn` | `/model`、`/provider`、`/routing`、`/profile` |
| C5 | 权限与安全 | status `inline`；变更 `prompt`+confirm | `next-call` | `/permission-mode`、`/yolo`、`/trust`、`/grants`、`/approval-reuse` |
| C6 | 沙箱与工作区 | list `inline`；变更 `prompt` | `next-call` | `/add-dir` |
| C7 | 上下文与输入 | list `inline`；写入 `prompt` | `next-turn` | `/memory`、`/attach`、`/queue clear` |
| C8 | 技能与工具 | list `inline`；选择 `screen`；启停 `prompt`；直执/发 turn `queue` | `next-call` / `session` | `/skill(s)`、`/function(s)`、`/call`、`/mcp` |
| C9 | 诊断与状态 | 只读 `inline`；viewer `screen`；控制动作 `prompt`；投递 `queue` | `read` / `next-turn` | `/status`、`/debug`、`/supervision`、`/agents`、`/timeline`、`/collab`、`/usage`、`/hotkeys`、`/help` |
| C10 | 输出与外观 | status `inline`；切换 `prompt` | `next-turn` | `/stream`、`/fast`、`/reasoning`、`/reasoning_effort`、`/theme` |
| C11 | 网络与长任务 | `queue`（P3 可选异步 + 进度副屏） | `session`/外部 | `/account refresh`、`/accounts refresh`、`/login`、`/image`、`/web open`、`/mcp reload|auth` |
| C12 | 任务与待办（新增） | 默认 `screen`（可切换视图）；无副屏 `inline` | `read` | `/todos`（新增命令，§附录 F.7） |

> 逐命令定稿见**附录 F**。C1「破坏类」是**排队执行**（不丢输入），不是拒绝；仅 `/exit` 阻断。

---

## §4 命令分类基线

> 基线依据：现有 `chatSlashCommandQueueSafe` 白名单（`chat_input_queue.go:1244-1284`）与 `command.go` 结构化命令结果效应。
> **分类定稿以附录 F 为准**（v1.3：业务级分类 + 运行模式 + 生效域）；附录 E（v1.2 四档 I/S/D/R）保留为保守基线。本节保留 P1/P2 推进集合。

### 4.1 首批 immediate 候选（P1，零副作用文档型）

| 命令 | 依据（Ready 路径 handler） | 说明 |
| --- | --- | --- |
| `/help`、`/?` | `chat_command_result.go:444-451`（`buildChatSlashHelpDocument`） | 零会话读取，无风险 |
| `/status` | `chat_command_result.go:598-607` → `chat_status.go:85-151` | 读 15 类字段（含 turn 期写入的 `TokenCount`/`ContextTokenCount`）→ 必须走快照；结构化路径**不**调用 `refreshChatConfigIfChanged`（对比 legacy `chat_status.go:61`）；允许有界本地 `os.Stat`（`chat_status.go:831`） |
| `/session` | `chat_command_result.go:548-553` → `chat_load_document.go:94-149` | 读 `RuntimeSession.BuildPreview()` 与 HTTP capture 快照 → 纳入快照并 `-race` 验证 |
| `/queue`（无参数/非 clear） | `chat_command_result.go:943-954` → `chat_input_queue.go:1416-1421` | 队列计数走锁；`queuedInputDrain` 为裸读（M4），须先收进快照/原子 |

### 4.2 分批提升（P2，子命令级）

| 命令/子命令 | 目标策略 | 前置条件 |
| --- | --- | --- |
| `/model status`（含别名 status 子命令） | immediate | 确认不触发 picker 效应；裸 `/model` 维持 deferred |
| `/provider` 只读子命令 | immediate | 盘点项 A3：确认不触发登录/picker/网络写 |
| `/supervision status` 等只读子命令 | immediate | 读取监督状态；确认不唤醒/不写 CAS（现有 `chatSupervisionSubcommandQueueSafe` 语义） |
| `/routing show|doctor` | immediate | 读取路由快照；确认不写会话路由（现有语义） |
| `/debug status|routing|state` | immediate | 读取诊断快照；`/debug display` 见 D1 |
| `/debug display` | **S（副屏只读 overlay）** | P2-4 落地；租约失败按 §3.7.3 降级 I/D |
| `/usage` | **S（副屏 viewer）** | P2-4 落地；无副屏能力时降级 I/D |
| `/web endpoints` | **S（副屏 overlay）** | P2-4 落地；`/web open` 起子进程维持 R |
| `/agents`（只读变体） | immediate | 附录 E.4：只读视图 I；pick/panel 待确认 S；send/followup/cleanup 维持 R |

### 4.3 保守基线：queue 集（v1.3 定稿见附录 F）

- 裸 `/model`、`/provider`：Ready 路径在可交互时进入 picker（`chat_model_picker.go:443-461`）；忙时若走 immediate，`canOpenChatModelPicker` 会因 run 活跃退化为 status 文本（`:449-455`），语义与用户预期分叉，因此保持 deferred（`/model status` 单独提升）；`/provider` 依据同理（M6 修正）。
- `/history`（含 `/h`）：Ready 路径打开分页器（`command.go:44-49`）→ v1.2 起进入 **S（副屏只读）**（P2-4）；无副屏能力时降级 I/D。
- 其余保持 deferred 的命令以附录 E 为准（裸 `/model`、`/provider`、`/resume`、`/export`、`/retry`、`/skill` 默认路径等）。
- 语义上「和当前回合相关、延后仍有意义」的只读观察命令。

### 4.4 保守基线：reject 集（v1.3 定稿见附录 F）

- 退出/会话级破坏：`/exit`、`/quit`、`/clear`、`/new`、`/queue clear`。
- 状态变更（next-turn 语义）：`/permission-mode`、`/profile switch`、`/model <provider> <model>`（若 D2 决策为 deferred 则移入 4.3）。
- 需要独占屏幕但「选择即写」的交互：`/resume`、`/export`、`/backtrack`、`/theme select`、`/login`、`/mcp` 选择动作、`/skills enable|disable`、`/profile pick`（v1.2 起这些为 D/R，S 档只收只读查看器，见 §3.7.4 与附录 E）。
- 触发新 turn 或子进程：`/shell`、`/cmd`、`/goal`（`SendObjective`）、`/skill`、`/retry`、`/compact`。

### 4.5 分类元数据落地形态

- catalog spec 显式声明 `BusyPolicy`；
- 子命令级策略用少量解析函数（沿用现有 `chatDebugSubcommandQueueSafe` 等模式，改为四态 I/S/D/R）；
- 测试锁定「catalog 全覆盖 + 4.3/4.4 行为不变」。

---

## §5 分期实施（每步可独立回滚）

### P0：盘点与并发基线（无行为变化）

| 步骤 | 内容 | 产出 |
| --- | --- | --- |
| P0-1 | 盘点 `dispatchChatCommand` 可识别的全部命令及真实副作用（send / actor / 存储 / 屏幕租约 / 输入模式） | 命令-副作用矩阵，落到本文 §4 |
| P0-2 | 复核渲染协调器锁边界（已获证：`RenderCommandDocument` → `chat_interaction.go:4612-4648` 的 `c.mu` + `beginMessageLocked`；busy capture 已有旁路写先例 `chat_busy_input.go:128,153`） | 收敛为「与流式 delta 的 Scene 顺序/背压」验证清单，不再是渲染可用性前置 |
| P0-3 | 定义 immediate 命令的只读快照接口（`chatBusyCommandSnapshot`），列明每个字段的读写方；首批覆盖 `/status`/`/session`/`/queue`（含 `queuedInputDrain` 原子化） | 快照函数 + 字段读写表 + `-race` 用例 |
| P0-4 | 单测：忙时 `/help` 等仍走 deferred（基线） | 基线测试 |

**退出条件**：§4 全部分类有依据；快照设计覆盖 P1 命令所需的全部字段。

### P1：BusyPolicy 元数据 + immediate 通道骨架（默认关闭）

| 步骤 | 内容 |
| --- | --- |
| P1-1 ✅ | `chatSlashCommandSpec` 增加 `BusyPolicy`；catalog 全量标注（首批 4 条已标注，其余 inherit） |
| P1-2 ✅ | `chatSlashCommandBusyPolicyFor` 解析函数 + 单测（含别名、子命令、fail-closed、T18 等价） |
| P1-3 ✅ | 路由新增 `chatInputRouteImmediate`/`chatInputRouteScreen`；`setCommandGate` → `setCommandPolicyResolver`（旧布尔门保留兼容包装）；`routeLineWithCommandGate` 四档分支 + INV-8 来源分流 + 消费方缺失回退排队 |
| P1-4 ✅ | `ChatSession.commandMu` + `lockChatCommandPhaseA`；`dispatchChatCommand` Phase A（解析/变更/渲染）持锁、Phase B 效应在 `unlockCommand()` 后锁外执行；`TestDispatchChatCommandReleasesCommandPhaseALock` 覆盖结构化/legacy 两条出口 |
| P1-5 ✅ | `chat_busy_command_exec.go`：`executeBusySlashCommand`（策略再校验 → 统一面门 → 仲裁 L0 门 → TryLock → 解析 → 忙时安全断言 → 渲染 → 历史）；`consumeBusyCommand` 交接 + 捕获循环未占有回退入队 |
| P1-6 ✅ | 来源分流随 P1-3 落地（`chatInputSourceIsLocalTerminal`，INV-8）；Web 回执/唤醒语义回归 `-run Web` 全绿 |
| P1-7 ✅ | 灰度开关 `AICLI_CHAT_BUSY_COMMAND` 默认关；T18 等价在策略层与路由层双重断言 |
| P1-8 ✅ | 首批 §4.1 四条（`/help` `/status` `/session` `/queue`）；M4 原子化完成，`/queue` 忙时读取无竞态前置 |

**退出条件**：开关关闭 = 现行为逐条等价（现有测试零改动）；开关打开 = 首批命令忙时立即可见。

### P2：统一交互机制落地（注册表 / 宿主 / 模式与生效域）

- **P2-1 ✅ 注册表与解析器**：`runtimeCommandSpec`（Mode/Effect/Category/Confirm/Notice/SwitchKey）+ 附录 F.1 全量 59 条 + F.2 别名归并 + 子命令变体解析；未登记命令/未知子命令 → queue（INV-6）；T28 不变式与 T29 catalog 覆盖单测已绿（`chat_runtime_command_registry.go`，尚未被路由消费——按 P2-2/P2-3 接入）。
- **P2-2 ✅ 三级开关**：`AICLI_CHAT_RUNTIME_INTERACTION`（auto/readonly/off）+ `..._CATEGORIES`（C#=mode）+ `..._COMMANDS`（命令=mode，键先别名归并）；优先级命令级 > 分类级 > 全局；block 不可被放宽；T31 单测已绿（`chat_runtime_command_switch.go`，消费接入见 P2-3）。
- **P2-3 ✅ 宿主与生效域执行器**：`runtimeCommandHost.SubmitBusy`（注册表→三级开关→安全门→模式分发）+ 生效域守卫（session/process 不可达，block+process 例外只拒绝）+ 非 read 生效域执行后的 Notice 提示 + 统一审计事件 `aicli.chat.runtime_interaction`（命令/模式/生效域/结果/耗时，T32/T33）；路由由注册表接管（**双开关显式启用**才生效，P2 环境变量未设置时保持 P1 首批白名单行为）；screen/prompt 载体仍按 P2-4 降级。
- **P2-4 🚧 副屏与 prompt 载体**：① 租约等待 ✅ `AcquireAlternateScreenWait` + surface 级预算 `SetAlternateScreenWaitBudget`（既有 ~20 处 handler 调用点零改动继承等待能力；含超时/取消/致命错误/复位共 7 项单测，见 G.9）；② 流式缓冲 ✅ 核验（租约期 frame `Invalidate`+`Deferred`、释放后 `FullRepaint` 已由 `TestTerminalSessionLeaseReleaseForcesPrimaryRecovery` 锁定，**不新增冗余 StreamHold/StreamFlush API**；长租约 ledger 增长量化归 U1/P0-2）；P2-4b-2 待办（设计见 G.9）：③ modal 影子登记与恢复、④ `prepareInteractiveRead` 跨回合门、⑤ 白名单 S 档接入 host（先 `/todos` 单条打通，再放量 `/history` `/usage` `/debug display` `/web endpoints` `/account`）+ T20~T26/T34。
- **P2-5 生效域适配**：`live`（显示/热刷新/投递/控制面写）直接接入；`next-turn` 复用 `actorRebuildPending`/`reconcilePendingChatActorRebuild`/`refreshLocalRuntimeAfterSelection`（模型/Provider/reasoning_effort/profile/routing/add-dir）；`next-call` 复用 `withLivePermissionModeSource`（permission-mode/yolo/trust/grants/approval-reuse）；缺适配器或未决项（V1/V2a/V10）先降 queue 兜底（D12）。
- **P2-6 网络长任务**：C11 保持 queue；P3 评估「异步任务 + 进度副屏」。
- 每个晋升命令必须附快照依赖清单 + 忙时专项测试。

### P3：deferred 反馈显式化与体验收敛（文案版本化，M5）

- 排队命令提示改为「已排队，将在当前回合结束后执行」；该文案变更只在 P3 落地，P1/P2 保持与现状零差异（T7）；
- 状态行/队列计数联动（`chat.go:1140-1143` 属会话信息打印路径，与 `/queue` handler 不同源，N4）；
- 可选：catalog 帮助标注忙时策略。

### P4：仲裁器 P3 对齐（依赖阶段 F）

- 若阶段 F P3 删除散点 `Arm/Suspend`，复核本方案在纯仲裁路径下的行为（`chatBusyCapture` 仍为 L1，不受影响）；
- 补 Windows/winpty/SSH 真实终端回归。

---

## §6 测试矩阵

| # | 场景 | 预期 | 层级 |
| --- | --- | --- | --- |
| T1 | 忙时输入 `/help` | 立即渲染帮助；turn 继续；命令不入队、不入模型上下文 | 单测 + TTY 集成 |
| T2 | 忙时输入 `/status` | 立即渲染状态文档；turn 继续 | 单测 + TTY 集成 |
| T3 | 忙时输入 `/session`、`/queue` | 同上 | 单测 |
| T4 | 忙时输入 `/clear`、`/exit`、`/new`、`/queue clear` | 维持拒绝 + 现有提示；无状态变化 | 单测（回归） |
| T5 | 忙时输入 `/model status`（P2 后） | immediate；裸 `/model` 仍 deferred | 单测 |
| T6 | 忙时输入 `/model <x>`（P2/D2 决策后） | deferred + 「回合结束后执行」提示 | 单测 |
| T7 | 既有白名单命令（`/history`、`/debug display` 等） | 行为与现状一致（deferred）；提示文案与现状逐字一致（M5） | 现有测试回归 |
| T8 | 开启/关闭灰度开关 | 关闭 = 现行为等价；打开 = T1~T3 生效 | 单测 |
| T9 | turn 运行中执行 immediate 命令（并发） | `-race` 无竞态；命令 cell 与流式 delta 的 Scene 顺序/背压正确、无交错损坏 | `-race` 并发测试 |
| T10 | immediate 判定瞬间与 turn 结束重叠 | 原子占有后必达（至多执行一次且一定渲染）；未占有行由主循环执行 | 单测（交接协议） |
| T11 | modal/priority prompt 活跃时输入 immediate 命令 | 降级 deferred，不抢占 L0 | 单测 |
| T12 | 渲染失败 / 命令内部错误 | 本地补充提示；turn 不受影响；无 panic | 单测 |
| T13 | immediate 命令结果携带被禁止效应（防御） | 降级 deferred + 诊断日志；不执行副作用 | 单测（安全断言） |
| T14 | 非 TTY / capture 不可用（`SupportsCancelableInteractiveInputRead=false`） | 回退现状（deferred/reject） | 单测 + 平台矩阵 |
| T15 | Windows/winpty/SSH 真实终端 | P1/P2 命令忙时可用且无渲染错位 | 真实终端回归（与阶段 F P3 合并） |
| T16 | 运行中 Web/远程注入 immediate 类命令 | 保持 queued + 唤醒主循环，回执 `{"status":"queued"}`；不出现静默吞命令（B1） | 单测（`web_handlers.go`/`web_invoke.go` 路径） |
| T17 | 非统一渲染面（`!unifiedDirectInteractiveOutput`）忙时输入 immediate 类命令 | 降级 deferred，不落 legacy `handleCommand`、无 stdout 直写（M2） | 单测 |
| T18 | resolver × 旧 `chatSlashCommandQueueSafe` 全命令对照（灰度关闭） | 逐条等价；例外清单显式登记 | 表驱动单测 |
| T19 | immediate 命令的历史/echo/状态行行为 | 执行成功后入历史；不置 queued echo；状态行行为符合 §3.5 | 单测 |
| T20 | 忙时 `/history` 打开副屏分页器 | 租约获取成功；turn 继续流式（主屏帧 Deferred）；退出后全量重绘、内容无丢失 | TTY 集成 + `-race` |
| T21 | 忙时 `/usage`、`/debug display`、`/web endpoints` | 同 T20；关闭后 composer 与流式内容恢复 | TTY 集成 |
| T22 | 副屏打开期间 turn 结束 | 屏幕继续可用；主循环等待屏幕关闭后才读输入（无 stdin 争用） | 单测 + TTY |
| T23 | 副屏打开期间输入 Esc/q | 正常关闭；capture 恢复；Esc 由 L0 消费，不冒泡为 turn 中断 | 单测 |
| T24 | L0 modal（审批/提问）活跃时请求 S 档 | 降级 D；不发生租约获取 | 单测 |
| T25 | 无备用屏能力 / 租约超时 | fail-closed 降级 I（内联快照）或 D；有明确提示 | 单测 |
| T26 | winpty / SSH 管道 / 非 TTY | 一律降级 D；无 escape 泄漏、无画面损坏 | 平台矩阵 |
| T27 | 租约期流式缓冲超限 | 触发全量重绘策略；退出后内容完整、无重复 | `-race` 单测 |
| T28 | S 档命令结果试图携带写效应（防御） | 拒绝并降级；记录诊断 | 单测 |
| T29 | 注册表 × catalog 差集 | 每条 catalog 命令（含别名/子命令变体）都有 `runtimeCommandSpec`；无未登记项 | 表驱动单测 |
| T30 | 未登记命令/未知子命令 | 默认 queue（不丢输入）；不发生晋升 | 单测 |
| T31 | 三级开关（全局 readonly/off、分类 off、命令 off） | 逐级降级到 queue；`block` 不受影响；诊断事件记录 | 单测 |
| T32 | 生效域执行器 | `session`/`process` 在宿主层不可达；next-turn/next-call 走适配器并提示 Notice | 单测 |
| T33 | 统一审计事件 | 每次运行时交互产生事件（命令/模式/生效域/结果/耗时） | 单测 |
| T34 | prompt 模式确认/取消 | 确认后应用；取消不产生任何变更；capture 恢复读 | 单测 + TTY |
| T35 | next-turn 切换类忙时执行（`/model`、`/stream`、`/theme`） | 确认后提示「下一回合生效」；当前 turn 不受影响、无 race | `-race` 单测 + TTY |
| T36 | next-call 安全类忙时执行（`/permission-mode`、`/trust`、`/add-dir`） | 确认后对后续工具调用生效；审计记录；当前 turn 无错乱 | `-race` 单测 |
| T37 | 忙时 `/todos`（默认/`active`/`done`） | **inline 部分已实现**（参数直达/过滤，单测通过）；screen 面板待 P2-4 | TTY 集成 |
| T38 | 面板打开期间 todos 工具更新 | 面板刷新到最新快照（轮询/事件驱动）；无脏读、无重复渲染 | `-race` + TTY |
| T39 | 无待办数据 | **已实现**：显示「当前会话暂无待办」；不渲染过期数据（单测通过） | 单测 |
| T40 | 无副屏能力/租约超时 | inline 降级路径已实现；screen 阶段复核与 screen 视图一致性 | 单测 |
| T41 | 快照并发（turn 写 transcript / 面板读） | `-race` 无竞态；实现为 tool_end 缓存 + 无缓存回退扫描（V12） | `-race` 单测 |

---

## §7 风险与对策

| # | 风险 | 等级 | 对策 |
| --- | --- | --- | --- |
| R1 | immediate handler 直读 turn 期间可变字段 → data race | 高 | P0-3 快照契约；每个提升命令附字段读写表；`-race` 门禁 |
| R2 | 命令渲染与流式输出并发导致 Scene/scrollback 顺序损坏 | 高 | 只走统一命令结果入口；T9 顺序断言；复用协调器现有锁（P0-2 结论） |
| R3 | 分类遗漏导致副作用命令被错误 immediate | 高 | `BusyPolicy` fail-closed；安全断言（T13）；catalog 全覆盖测试 |
| R4 | 回合结束交接窗口导致重复执行/丢失 | 中 | `IsReady` 二次检查 + `requeueFront`；T10 |
| R5 | immediate 长耗时命令阻塞 capture 读取（如慢 IO） | 中 | 契约禁止阻塞 IO；超时/降级为 deferred；必要时 P3 抽独立命令 worker |
| R6 | 与阶段 F P3 单写者清理冲突 | 中 | §3.6 依赖关系；P4 对齐复核；不依赖 KeyHandler 位 |
| R7 | 用户对「为什么这条命令还要等」产生困惑 | 低 | P3 显式 deferred 提示 + 帮助标注策略 |
| R8 | 命令输出被误当成模型输入 / 会话日志污染 | 中 | INV-5；不加消息、不写会话日志（只读命令的渲染走临时/命令 cell 语义） |
| R9 | Web/远程来源被误放 immediate → 回执 queued 但无人执行（静默吞命令） | 高 | INV-8 来源隔离 + T16；路由按 `chatInputSourceIsLocalTerminal` 分流，fail-closed |
| R10 | `dispatchChatCommand` 两阶段改造影响全部命令（含 Ready 路径） | 高 | Phase A/B 拆分保持效应顺序与语义不变；现有命令测试全量回归；灰度开关默认关；「锁内无 send」断言 |
| R11 | 副屏租约与流式/recovery 窗口竞争导致画面损坏 | 高 | `AcquireAlternateScreenWait` 有界等待 + fail-closed；复用 UI-actor barrier（`chat_picker_common.go:174-204`）；T20/T25 |
| R12 | 租约期流式缓冲无界（长 turn 内存增长 / 恢复卡顿） | 高 | `StreamHold/StreamFlush` 有界契约；超限转全量重绘；T27 |
| R13 | 输入所有权移交竞态：capture 与副屏同时消费按键 | 高 | L0 modal 登记 + capture 暂停读；跨 turn 边界 `busyScreenActive` 门；T22/T23 |
| R14 | S 档被误用于写状态命令（如 `/theme select`） | 中 | 只读契约 + 安全断言（T28）；附录 E 子命令级分类 |

---

## §8 验收标准

1. 灰度关闭时：`chat_input_queue_test.go`、`chat_debug_display_busy_repro_test.go` 等现有测试**零改动**通过。
2. 灰度打开时：T1~T3 命令在 300s 长回合运行中 1s 内可见输出（TTY 实测）。
3. `-race` 全量通过，含 T9 并发用例。
4. 代码中不存在绕过 `commandMu` 的 `dispatchChatCommand` 调用点（含测试辅助入口白名单说明）。
5. catalog 每个 spec 均声明 `BusyPolicy`，未声明默认 reject 的单测成立。
6. INV-1~INV-7 每条有对应测试或静态断言。
7. Web/远程注入路径行为与现状逐条一致（T16），不存在 immediate 导致的输入丢失。
8. 「锁内无 send」静态检查/测试成立：`commandMu` 覆盖区内不存在 `sendMessage`/`send*` 调用（R10）。
9. resolver 与旧白名单的逐条等价表（T18）通过，灰度关闭时零行为差异。
10. S 档命令在租约失败/非 TTY 场景 100% fail-closed 降级，无 stdin 争用与画面损坏（T22~T26）。
11. 租约期流式缓冲有界（T27），退出后全量重绘内容与 Scene 一致。
12. 附录 E 的 58 条命令 + legacy 入口全部显式定级；`screen` 档只出现在只读交互白名单（§3.7.4）。
13. 注册表覆盖 100%（T29），未登记默认 queue（T30）。
14. 三级开关关闭时行为与 v1.0 逐条等价（T31）。
15. 生效域执行器保证 `session`/`process` 在运行时不可达（T32 + 静态断言）。
16. next-turn/next-call 命令的生效提示与审计事件可见（T33/T35/T36）。

---

## §9 开放问题与决策点

| # | 问题 | 选项 | 建议 |
| --- | --- | --- | --- |
| D1 | `/debug display` 是否 P2 提为 immediate | (a) 维持 deferred；(b) 实现 inline 降级后 immediate | (a) 稳妥：viewer 背屏复杂，先不动；有明确需求再做 (b) |
| D2 | 状态类命令（`/model <x>`、`/permission-mode`）忙时 | (a) 维持 reject；(b) deferred + 明示「回合结束后执行」 | (b) 更符合直觉，但需确认「延后切换」不会让用户误以为已生效（提示必须显式）；首期建议维持 (a)，独立决策 |
| D3 | 是否首期即引入独立命令 worker goroutine | (a) capture goroutine + 两阶段 `commandMu`（TryLock）；(b) worker + mailbox | (a) 已按审查结论定为 v1.1 方案（capture 永不阻塞）；R5 出现实测问题再演进 (b) |
| D4 | immediate 命令输出是否进入 scrollback 常驻 | (a) 常驻命令 cell；(b) 瞬时提示 | 跟随现有命令渲染语义（与 Ready 路径一致），首期 (a) |
| D5 | `/queue` 的 immediate 范围 | 仅无参数查看；clear 仍 reject | 按 §4.1 落地 |
| D6 | 是否需要 `/cancel`、`/interrupt` 忙时控制通道 | 独立于本方案的后续项 | 建议单独立项（与 Esc/Ctrl+C 语义对齐） |
| D7 | deferred 文案（「已排队…」）何时切换 | (a) P1 即改；(b) P3 独立评审后切换 | (b) 保证 P1/P2 灰度期零行为差异（M5） |
| D8 | S 档是否允许「选择后产生草稿」（如 `/skills` pick 只填草稿不落状态） | (a) 首期不允许（纯查看）；(b) 允许草稿类选择 | (a) 首期最小面；草稿类选择 P3 评估 |
| D9 | 副屏跨 turn 结束后是否自动关闭 | (a) 保持打开直到用户退出（主循环等待）；(b) turn 结束时提示并自动关闭 | 建议 (a)（用户主动发起）；若出现「忘记退出」再切 (b) |
| D10 | C5/C6（权限/沙箱/信任）运行时生效边界 | (a) next-call（对后续工具调用立即生效，prompt+审计）；(b) 收敛为 next-turn | 首轮灰度建议先 `off`/next-turn，稳定后按 (a) 打开（V7） |
| D11 | `/exit` 是否改为「回合后退出」 | (a) 保持 block；(b) queue 后退出 | (a) v1.3 默认；若需求明确再切 (b)，需显式提示 |
| D12 | 缺少 next-turn/next-call 适配器的命令 | (a) 先降 queue 兜底；(b) 阻塞实现适配器 | (a) 保证 P2 可分期；适配器按命令优先级补齐 |
| D13 | `/todos` 裸命令语义 | (a) 固定默认视图（全部）；(b) 每次调用循环切换（all→active→done） | 建议 (a) + 屏内按键切换；若偏好循环再开 (b) |

---

## 附录 A：关键代码锚点速查

| 主题 | 锚点 |
| --- | --- |
| 主循环 slash 分支 | `backend/cmd/aicli/commands/chat.go:1762-1783` |
| turn 入口 / busy capture 启停 | `backend/cmd/aicli/commands/chat_send.go:74-75,121` |
| busy capture 循环与路由 | `backend/cmd/aicli/commands/chat_busy_input.go:16-160` |
| 拒绝反馈文案 | `backend/cmd/aicli/commands/chat_busy_input.go:146-160` |
| 路由四档与队列 gate | `backend/cmd/aicli/commands/chat_input_queue.go:35-53,1673-1696` |
| 命令门与白名单 | `backend/cmd/aicli/commands/chat_input_queue.go:1221-1284` |
| 队列消费（Ready） | `backend/cmd/aicli/commands/chat_input_queue.go:1138-1146` |
| 统一命令执行与效应 | `backend/cmd/aicli/commands/command.go:24-179` |
| 命令目录 spec | `backend/cmd/aicli/commands/chat_slash_command_catalog.go:19-31` |
| 忙时白名单回归测试 | `backend/cmd/aicli/commands/chat_debug_display_busy_repro_test.go:9-38` |
| 输入仲裁器 | `backend/cmd/aicli/commands/chat_input_arbitration.go:20-118` |
| 仲裁阶段 F 规划 | `docs/plan/esc-interrupt-priority-and-loop-robustness-plan-20260918.md` §12 |

---

## 附录 B：被否决的替代方案

| 方案 | 否决理由 |
| --- | --- |
| 主循环异步化（sendMessage 移入 goroutine，主循环变事件循环） | 会话状态、actor 生命周期、渲染顺序、中断语义需整体重新证明；超出本问题范围，属长期架构方向 |
| 在 ReAct loop 步骤边界注入 immediate 命令 | 安全点粒度粗：长流式输出/长工具执行期间仍不可用，恰是目标场景；可作为 deferred 命令的更早执行点后续考虑 |
| 扩大现有白名单为「全部命令排队」 | 状态变更命令延后生效语义不明确；破坏性命令不可延后；且无法解决即时可见性问题 |
| 在 busy capture 中逐个命令特判（不建元数据） | 与 catalog 不同源，必然出现「新命令忘分类」；违反 G2/INV-6 |

---

## 附录 C：现状基线盘点（catalog 全量 58 条）

> 本附录保留「现状忙时行为」作为 P0 对照基线；**分类定稿以附录 E 为准**（v1.2，四档 I/S/D/R）。
> 来源：`chat_slash_command_catalog.go` 全量 `Name`（2026-09-27 实测 58 条；别名 `/ ? /q /quit /cls /h /s /normal` 等随主命令一起定级）。
> 下表「现状忙时」由 `chatSlashCommandQueueSafe`（`chat_input_queue.go:1244-1284`）当前实现推导。

### C.1 首批 immediate 候选（P1 落地）

| 命令 | 现状忙时 | P0 目标 |
| --- | --- | --- |
| `/help`（含 `/ ?`） | deferred | immediate |
| `/status` | deferred | immediate |
| `/session` | deferred | immediate |
| `/queue`（无参数/非 clear） | deferred | immediate |

### C.2 现状 deferred，P2 按子命令/副作用评估提升

| 命令 | 现状忙时 | P2 目标 |
| --- | --- | --- |
| `/debug` | 只读子命令 deferred；on/off/export reject | status/routing/state 提升；display 待 D1 |
| `/supervision` | 只读子命令 deferred | status 类提升；wake/ack/defer/resolve/control 保持 reject |
| `/routing` | show/doctor deferred | show/doctor 提升；写入类保持 reject |
| `/provider` | deferred | 只读子命令提升（盘点 A3） |
| `/model` | deferred（整条） | `status` 子命令提升；裸命令（picker）保持 deferred |
| `/history`（含 `/h`） | deferred | 保持 deferred（分页器接管屏幕） |
| `/usage` | reject | P2 评估：inline 降级可用则提升 |

### C.3 现状 reject，保持基线（P0 逐条确认无遗漏）

`/hotkeys`、`/exit`、`/clear`、`/new`、`/profile`、`/agents`、`/agent`、`/timeline`、`/collab`、`/sessions`、`/load`、`/resume`、`/export`、`/title`、`/goal`、`/stream`、`/fast`、`/theme`、`/reasoning`、`/reasoning_effort`、`/s`、`/normal`、`/account`、`/accounts`、`/login`、`/compact`、`/backtrack`、`/memory`、`/attach`、`/image`、`/retry`、`/queue clear`、`/permission-mode`、`/trust`、`/add-dir`、`/plan`、`/plans`、`/approval-reuse`、`/grants`、`/yolo`、`/functions`、`/function`、`/call`、`/skill`、`/skills`、`/mcp`、`/web`、`/shell`

> 注：C.3 中若有命令实际是「只读且无屏幕租约」（例如 `/grants`、`/plans` 查询态），P0 盘点后可按晋升流程移入 C.2/C.1；晋升必须附 handler 证据与专项测试。

### C.4 非 catalog 入口排查

`dispatchChatCommand` 还识别别名与非 catalog 命令（如 `/cmd` 与 `/shell` 同路径、`!` 前缀 shell、`/new` 类会话操作）。P0-1 必须用「catalog ∪ 结构化命令注册表 ∪ handleCommand 分支」的并集建立清单，并为差集命令指定默认 reject 与例外登记。

---

## 附录 D：`dispatchChatCommand` 调用点与效应证据（2026-09-27 独立审查核查）

| 类型 | 位置 | 结论 |
| --- | --- | --- |
| 生产调用点（TUI 主循环） | `chat.go:1714`（`!` 转 `/shell`）、`chat.go:1773`（slash） | 仅有的 TUI 调用点；不由外层持锁，锁在 `dispatchChatCommand` 内部实现 |
| 结构化执行 | `command.go:29` `tryExecuteStructuredChatCommand` | Phase A：解析 + 渲染；效应随后应用 |
| legacy 执行 | `command.go:178` `handleCommand` | 统一渲染面下被 `command.go:172-178` fail-closed；immediate 禁止进入（INV-2） |
| ACP/stdio | `agent_stdio_slash.go:94` | 范围外（N2）；无 TUI busy capture 通道 |
| Phase B：屏幕/交互效应 | `command.go:44-125`（`Open*Picker`/`Open*Overlay`/`Open*Screen`） | 不持锁；immediate 结果出现这些效应即视为分类错误并降级 |
| Phase B：turn 触发效应 | `command.go:132-155`（`SendObjective`/`SendMessageAfterCommit`/`SendSkillTurn`） | 不持锁；会经 `chat_send.go:74-75,121` 同步跑完整嵌套 turn（B2 根因） |
| 重入核查 | picker/overlay 实现（如 `chat_model_picker.go`、`chat_mcp_picker.go`） | 未发现回调 `dispatchChatCommand` 的路径；仍需测试断言防回归 |

---

## 附录 E：全量命令分类定稿（v1.2，四档 I/S/D/R）

> 定级规则（§3.1、§3.7）：**I**=主屏内联即时（只读快照）；**S**=副屏只读交互（仅 §3.7.4 白名单）；**D**=排队到 Ready 执行；**R**=忙时拒绝。
> 依据：三路只读子代理逐命令盘点（2026-09-27，handler 均定位到结构化注册表 `tryExecuteStructuredChatCommand` 命中处）+ 主线程复核。
> 复合命令按「变体」分列；**未列出的子命令与未知参数一律 R（fail-closed）**。handler 行号以盘点时工作区为准。

### E.1 catalog 58 条

| # | 命令 | handler（file:line） | 主要效应 | Ready 行为 | 策略 |
| --- | --- | --- | --- | --- | --- |
| 1 | `/help` | chat_command_result.go:444 | 无 | 帮助文档 | I |
| 2 | `/hotkeys` | command.go:378 → chat_hotkeys_command.go:31 | 读 keymap；reload 写配置 | 文本矩阵 | I（reload→R） |
| 3 | `/exit` | command.go:423；chat_unified_command_gate.go:21 | 终止进程 | 退出 | R |
| 4 | `/clear` | chat_command_result.go:354 | 清历史（破坏性） | 确认+清空 | R |
| 5 | `/new` | chat_command_result.go:555 | 新会话 | 确认 | R |
| 6 | `/session` | chat_command_result.go:548 | 只读 | 文档 | I |
| 7 | `/status` | chat_command_result.go:598 → chat_status.go:85 | 只读（有界 stat） | 文档 | I |
| 8 | `/usage` | chat_command_result.go:610 → chat_usage_screen.go:73 | 备用屏 viewer | 分页屏 | S |
| 9 | `/debug` | chat_command_result.go:798 → chat_debug.go:828 | status/routing 只读；display 屏；on/off/export 写 | 混合 | I（status/routing/state）· S（display）· R（on/off/export） |
| 10 | `/supervision` | chat_command_result.go:402 → chat_supervision.go:155 | 只读查询；wake/ack/resolve/control 写 CAS | 文本/动作 | I（只读）· R（动作） |
| 11 | `/routing` | chat_command_result.go:409 → chat_routing_command.go:66 | show/doctor 只读；面板；写入类改覆盖 | 文本/面板 | I（show/doctor）· S（只读面板，待确认）· R（写入类） |
| 12 | `/profile` | chat_command_result.go:417 → chat_profile_command.go:54 | status/list/show/diff 只读；pick/use/reload/save/import 写 | 文本/弹层 | I（只读）· D（pick/切换，next-turn）· R（写文件类） |
| 13 | `/agents` | chat_command_result.go:378 → chat_debug.go:275 | 只读视图；pick/panel；send/followup/cleanup；approve/deny/answer | 文本/交互 | I（只读）· S（pick/panel，待确认）· R（send/followup/cleanup）· I 特例（approve/deny/answer，待评审） |
| 14 | `/agent` | chat_command_result.go:381 → chat_agent_transcript.go:110 | 只读 transcript | 文本 | I |
| 15 | `/timeline` | chat_command_result.go:482 | 只读 TeamStore | 文本 | I |
| 16 | `/collab` | chat_command_result.go:486 | 只读 mailbox 快照 | 文本 | I |
| 17 | `/sessions` | chat_command_result.go:532 | 只读列表 | 文档 | I |
| 18 | `/load` | chat_command_result.go:627 | 会话变更+replay | 确认+回放 | R |
| 19 | `/resume` | chat_command_result.go:345 | 裸 picker；显式恢复会话 | 屏/确认 | D（选择即恢复=写，不满足 S 只读契约） |
| 20 | `/export` | chat_command_result.go:317 | 裸 picker；导出写文件 | 屏/文档 | D（写文件，非只读） |
| 21 | `/title` | chat_command_result.go:648 | 写标题+storage | 确认 | R（备选 D） |
| 22 | `/goal` | chat_command_result.go:665 | status 只读；clear/pause/resume/complete 写；set 发新 turn | 文档/确认 | I（status）· R（set/变更） |
| 23 | `/history` | chat_command_result.go:573 | 备用屏分页器 | 分页屏 | S |
| 24 | `/stream` | chat_command_result.go:777 | 改 Stream+persist | 文档 | I（status）· R（toggle） |
| 25 | `/fast` | chat_command_result.go:786 | 改 FastMode+persist | 文档 | I（status）· R（toggle） |
| 26 | `/theme` | chat_theme_picker.go:162 | status/list/preview 只读；select/set 改全局主题+写 config | 文档/屏 | I（只读）· R（select/set，写全局） |
| 27 | `/reasoning` | chat_command_result.go:790 | 改输出开关 | 文档 | I（status）· R（toggle） |
| 28 | `/reasoning_effort` | chat_command_result.go:794 | status 只读；select 阻塞读 stdin；set 改 actor | 文档/行内选择 | I（status）· R（select/set） |
| 29 | `/s` | chat_command_result.go:781 | 改 Stream=true | 文档 | R |
| 30 | `/normal` | command.go:464（统一 TTY 已禁用） | 改 Stream=false | 文档 | R |
| 31 | `/provider` | chat_model_picker.go:432 | status 只读；裸 picker（选中即切换）；显式切换 | 文档/屏 | I（status）· D（裸/切换） |
| 32 | `/model` | chat_model_picker.go:425 | status 只读；裸 picker；显式切换 | 文档/屏 | I（status）· D（裸/切换） |
| 33 | `/account` | chat_account_command.go:124 | show/--no-refresh 缓存只读；refresh 同步网络+屏 | 屏 | S（show）· R（refresh，网络） |
| 34 | `/accounts` | chat_account_command.go:636 | 默认后台刷新+缓存屏；display 零网络 | 屏 | D（后台任务）· S（display） |
| 35 | `/login` | chat_login_picker.go:113 | 阻塞 OAuth+网络+写配置 | 交互 | R |
| 36 | `/compact` | chat_command_result.go:1036 | 同步 compact 重写 transcript | 文档 | R |
| 37 | `/backtrack` | chat_backtrack_command.go:204 | list/audit 只读；裸 picker；apply 破坏性 | 文档/屏 | I（list/audit）· R（裸/select/apply） |
| 38 | `/memory` | chat_command_result.go:704 | 读 store；add/note 写盘 | 文档 | I（status/list/search）· D（add/note/flush） |
| 39 | `/attach` | chat_command_result.go:964 | 裸列表只读；path/paste/clear/remove 改附件 | 文档 | I（裸）· R（变更；paste 含阻塞 IO） |
| 40 | `/image` | chat_command_unified_migration.go:83 | 网络生成+写文件 | 文档 | R |
| 41 | `/retry` | chat_retry_command.go:150 | 写 Composer 草稿 | 草稿回填 | D |
| 42 | `/queue` | chat_command_result.go:943 | status 只读；clear 丢弃队列 | 文档 | I（status）· R（clear） |
| 43 | `/permission-mode` | chat_command_result.go:1060 | 改模式+actor；bypass 阻塞确认 | 文档/确认 | I（status）· R（变更） |
| 44 | `/trust` | chat_folder_trust.go:211 | status 只读；grant 写 trust store | 文档 | I（status）· R（grant） |
| 45 | `/add-dir` | chat_add_dir.go:283 | list 只读；add/remove 改 roots+重建 runtime | 文档 | I（list）· R（add/remove） |
| 46 | `/plan` | chat_plan_command.go:123 | status/review/comment 只读；enter/exit/approve 改 mode；request_changes 派生新 turn | 文档/确认 | I（status/review/comment）· R（其余） |
| 47 | `/plans` | chat_plans_command.go:39 | list/detail/diff 只读；reopen 写文件 | 文档 | I（只读）· R（reopen） |
| 48 | `/approval-reuse` | chat_command_result.go:1094 | status/list 只读；set 改模式+清 grants | 文档 | I（status/list）· R（set） |
| 49 | `/grants` | chat_grants_command.go:113 | list 只读；revoke 写 store | 文档 | I（list）· R（revoke） |
| 50 | `/yolo` | chat_command_unified_migration.go:61 | 阻塞确认+提权 | 确认 | R |
| 51 | `/functions` | chat_command_result.go:512 | 只读目录/暴露预览 | 文档 | I |
| 52 | `/function` | chat_command_result.go:498 | 只读描述符 | 文档 | I |
| 53 | `/call` | command_invoke.go:79 | 直接执行工具（可阻塞审批/网络/子进程） | 执行+结果 | R |
| 54 | `/skill` | chat_skill_picker.go:183 | 默认 SendSkillTurn（新 turn）；--direct 直执 | 发送/执行 | D（默认）· R（--direct） |
| 55 | `/skills` | chat_skill_picker.go:243 | list 只读；裸/select picker（填草稿）；enable/disable 写配置 | 文档/屏 | I（list）· S（裸/select 草稿，D8）· R（enable/disable） |
| 56 | `/mcp` | chat_mcp_command.go:534 | 裸/select picker（选择执行动作）；list/status 可能网络；写/重载/auth 长 IO | 屏/文本 | D（裸/select）· R（写/重载/auth）· I（list/status，待确认网络） |
| 57 | `/web` | chat_web_command.go:21 | status/token 只读；endpoints overlay；open 起子进程 | 文本/屏 | I（status/token）· S（endpoints）· R（open） |
| 58 | `/shell` | chat_shell_command.go:16 | 子进程+SendMessageAfterCommit | 执行+新 turn | R |

### E.2 legacy / 别名入口（非 catalog 或别名）

| 入口 | 归并 | 策略 |
| --- | --- | --- |
| `/ ?` | `/help` | I |
| `/h` | `/history` | S |
| `/q`、`/quit` | `/exit` | R |
| `/cls` | `/clear` | R |
| `/n` | `/normal` | R |
| `/cmd` | `/shell` | R |
| `/mode`、`/mode:<name>` | `/permission-mode` | R |
| `/rewind` | `/backtrack` | R |
| `/tool` | `/call` | R |
| `/describe` | `/function` | I |
| `/catalog` | `/functions` | I |
| `/rename` | `/title` | R |
| `/reasoning-effort` | `/reasoning_effort` | 随主命令 |
| `!` 前缀 | `/shell` | R |
| `/hotkeys`（legacy only） | — | I（reload→R） |
| `/exit`（unified gate only） | — | R |
| `/normal`（legacy only，统一 TTY 已禁用） | — | R |

### E.3 统计与推进顺序

| 档位 | 粗略计数（按变体） | 推进 |
| --- | --- | --- |
| I | ~30 个只读变体 | P1 首批 4 条 → P2-1 批量提升 |
| S | 首批 5 条白名单（§3.7.4）+ 待确认 4 项 | P2-2~P2-4 落地 |
| D | ~10 个变体 | 保持排队；P3 文案显式化 |
| R | ~25 个变体 | 保持拒绝；不进入本方案 |

### E.4 待 P0 复核的不确定项

1. `/routing` 面板与 `/agents` pick/panel 是否真正只读（若面板内含写操作 → 降为 D/R）。
2. `/agents approve|deny|answer` 是否列为 I 特例（忙时解除子代理 pending 审批的刚需；需评审其写效应与 actor 竞态）。
3. `/mcp list|status` 是否触达 MCP server（网络）→ 决定 I 或 D。
4. `/account show`、`/accounts display` 是否零网络（决定 S 或 D）。
5. `/usage`、`/debug display`、`/history`、`/web endpoints` 的 S 档依赖「忙时租约门禁放行」改造（`chatPickerSurfaceReady`/`isRunActive` 的旁路，§3.7）。
6. `/skills` 裸/select 草稿类选择（D8 决策后定 S 或 I）。
7. `/profile pick` 的 next-turn 生效语义（D 或 R）。
8. `/functions <prompt>` 暴露预览需确认纯本地计算。

### E.5 晋升规则（防回归）

1. 新命令默认 **R**；要提升 I/S 必须同时提交：handler 证据（file:line）、效应矩阵、只读快照依赖、忙时专项测试。
2. S 档只允许出现在 §3.7.4 只读白名单；选择后写状态/写盘/发 turn 的命令一律 D/R。
3. `immediate` 结果若出现 send/picker/overlay/screen/quit 任一效应 → 运行期安全断言降级并记录（T13/T28）。

---

## 附录 F：业务级分类定稿（v1.3：业务域 × 交互模式 × 生效域）

> 维度说明见 §3.8。列含义：**业务域**＝C1~C11；**模式**＝inline/screen/prompt/queue/block；**生效域**＝read/next-turn/next-call/session/process；**确认**＝是否需要用户显式确认。
> 复合命令按变体列出；未列出的子命令与未知参数 → `queue`（INV-6）。本附录取代附录 E（v1.2 保守基线）。
> 「V」列标注需 P0 复核的存疑项（对应附录 F.5 与第二轮/第三轮审查证据）。

### F.1 catalog 58 条

| # | 命令 | 业务域 | 模式 | 生效域 | 确认 | 备注 | V |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | `/help` | C9 诊断 | inline | read | — | 纯文档 | |
| 2 | `/hotkeys` | C9 诊断 | inline（reload→prompt） | read（reload→next-turn） | reload 需确认 | reload 写 keymap 配置 | |
| 3 | `/exit` | C1 生命周期 | **block** | process | — | v1.3 唯一硬阻断；D11 讨论「回合后退出」 | |
| 4 | `/clear` | C1 生命周期 | queue | session | — | 排队执行，不丢输入；破坏性 | |
| 5 | `/new` | C1 生命周期 | queue | session | — | 同上 | |
| 6 | `/session` | C3 元数据 | inline | read | — | 当前会话摘要 | |
| 7 | `/status` | C9 诊断 | inline | read | — | 快照（含 `os.Stat` 有界读） | |
| 8 | `/usage` | C9 诊断 | screen | read | — | 备用屏 viewer；降级 inline | |
| 9 | `/debug` | C9 诊断 | status/routing/state=inline；display=screen；on/off=prompt；export=queue | read / next-turn | on/off 确认 | export 写文件故 queue | |
| 10 | `/supervision` | C9 诊断 | 只读=inline；ack/resolve/control=prompt；wake/deliver=queue | read / live | 动作类确认 | 控制面 CAS 写立即持久化（chat_debug_supervision.go:330-336）；现被白名单拒绝，v1.3 有意放开为 prompt+审计 | |
| 11 | `/routing` | C4 模型路由 | show/doctor=inline；面板=screen；on/off/reset/save=prompt | read / next-turn | 面板/写入确认 | 面板内写动作需逐项确认 | V1 |
| 12 | `/profile` | C4 模型路由 | status/list/show/diff=inline；pick=screen+confirm；reload/save/import=queue | read / next-turn | pick 确认 | 文件写类 queue | |
| 13 | `/agents` | C9 诊断 | 只读=inline；pick/panel=screen；approve/deny/answer=prompt；send/followup/cleanup=prompt | read / live | 控制动作确认 | mailbox 投递/registry 回收与当前 turn 无耦合（chat_debug.go:571-573）；V2 部分核实（approve/deny/answer 见 V2a） | V2 |
| 14 | `/agent` | C9 诊断 | inline | read | — | 子会话 transcript 快照 | |
| 15 | `/timeline` | C9 诊断 | inline | read | — | TeamStore 只读 | |
| 16 | `/collab` | C9 诊断 | inline | read | — | mailbox 快照 | |
| 17 | `/sessions` | C3 元数据 | inline | read | — | 会话列表 | |
| 18 | `/load` | C1 生命周期 | queue | session | — | 会话替换不可运行时执行 | |
| 19 | `/resume` | C1 生命周期 | queue（裸 picker 亦 queue） | session | — | 选择即恢复会话；不做运行时 screen | |
| 20 | `/export` | C3 元数据 | screen+confirm | read（写外部文件） | 确认 | 不写会话；文件 IO 在确认后执行 | |
| 21 | `/title` | C3 元数据 | 有参数=inline；裸=prompt | next-turn | 裸命令需输入 | storage 写，不影响运行中 turn | |
| 22 | `/goal` | C2 回合/目标 | status=inline；clear/pause/resume/complete=prompt；set=queue | read / next-turn / session | 变更确认 | `set` 派生新 turn → queue | |
| 23 | `/history` | C3 元数据 | screen | read | — | 分页器；降级 inline/queue | |
| 24 | `/stream` | C10 输出 | status=inline；toggle=prompt | read / live | toggle 确认 | 显示面立即生效（chat_command_result.go:1136） | |
| 25 | `/fast` | C10 输出 | status=inline；toggle=prompt | read / live | toggle 确认 | 立即 + persist（chat_command_result.go:1175） | |
| 26 | `/theme` | C10 输出 | status/list/preview=inline；select/set=screen+confirm | read / live | 确认 | 视觉立即、全局 UI 状态（chat_theme_picker.go:214-255）；无 turn 数据面耦合 | V3 已核 |
| 27 | `/reasoning` | C10 输出 | status=inline；toggle=prompt | read / live | 确认 | 仅显示开关 | |
| 28 | `/reasoning_effort` | C10 输出 | status=inline；select=screen+confirm；set=prompt | read / next-turn | 确认 | actor 延迟重建（markPendingChatActorRebuild）；select 不再忙时阻塞读 stdin | |
| 29 | `/s` | C10 输出 | prompt | live | 确认 | `/stream on` 快捷 | |
| 30 | `/normal` | C10 输出 | prompt | live | 确认 | 需先完成统一 TTY 迁移 | V4 |
| 31 | `/provider` | C4 模型路由 | status=inline；裸/切换=screen+confirm | read / next-turn | 确认 | 复用 actor 延迟重建（chat_profile_switch.go:311-367） | V5 已核 |
| 32 | `/model` | C4 模型路由 | status=inline；裸/切换=screen+confirm | read / next-turn | 确认 | 同上（chat_model_switch.go:202-303） | V5 已核 |
| 33 | `/account` | C11 网络 | show/--no-refresh=inline或screen；refresh=queue | read / 外部 | — | refresh 同步网络，不可运行时阻塞 | V6 |
| 34 | `/accounts` | C11 网络 | display=inline/screen；默认=queue | read / 外部 | — | 后台刷新任务 | |
| 35 | `/login` | C11 网络 | queue | external（写配置） | — | OAuth 长交互，绝不忙时执行 | |
| 36 | `/compact` | C1 生命周期 | queue | session | — | 重写 transcript | |
| 37 | `/backtrack` | C2 回合 | list/audit=inline；裸 picker=queue；apply=queue | read / session | — | apply 破坏性 | |
| 38 | `/memory` | C7 上下文 | status/list/search=inline；add/note/flush=prompt | read / next-turn | 写确认 | 写盘但只影响后续记忆读取 | |
| 39 | `/attach` | C7 上下文 | 裸列表=inline；add/clear/remove=prompt；paste=queue | read / next-call | 变更确认 | paste 含剪贴板子进程 | |
| 40 | `/image` | C11 网络 | queue | external（写文件） | — | 网络生成 | |
| 41 | `/retry` | C2 回合 | prompt | read（仅回填草稿） | 确认 | 防止覆盖已输入草稿 | |
| 42 | `/queue` | C7 上下文 | status=inline；clear=prompt | read / next-turn | clear 确认 | 丢弃待提交输入需确认 | |
| 43 | `/permission-mode` | C5 权限 | status=inline；set=prompt+confirm | read / next-call | 确认 | live 权限源（chat_actor_executor.go:569-581）；bypass 二次确认 + 审计 | V7 已核 |
| 44 | `/trust` | C5 权限 | status=inline；grant=prompt+confirm | read / next-call | 确认 | 进程全局 trust gate（chat_folder_trust.go:224-241） | V7 已核 |
| 45 | `/add-dir` | C6 沙箱 | list=inline；add/remove=prompt+confirm | read / **next-turn** | 确认 | 准入集合立即写；工具策略留到 actor 重建（chat_add_dir.go:259-264）→ 按策略面取 next-turn | V7 已核 |
| 46 | `/plan` | C2 计划 | status/review/comment=inline；enter/exit/approve=prompt；request_changes=queue | read / next-call / session | 变更确认 | 权限值下一次工具评估可见；durable plan 在 turn 起钉住；ACP 在途显式拒绝（agent_stdio_mode.go:214）→ TUI 需专项测试 | V10 |
| 47 | `/plans` | C2 计划 | list/detail/diff=inline；reopen=queue | read / session | — | reopen 写工作区文件 | |
| 48 | `/approval-reuse` | C5 权限 | status/list=inline；set=prompt+confirm | read / next-call | 确认 | live 权限源；清 grants 属安全边界变更 | V7 已核 |
| 49 | `/grants` | C5 权限 | list=inline；revoke=prompt+confirm | read / next-call | 确认 | 仅收窄权限、立即持久撤销 | V7 已核 |
| 50 | `/yolo` | C5 权限 | prompt+confirm | next-call | 强确认 | 提权，附审计与生效提示 | V7 已核 |
| 51 | `/functions` | C8 技能工具 | inline | read | — | 目录/暴露预览 | |
| 52 | `/function` | C8 技能工具 | inline | read | — | 描述符 | |
| 53 | `/call` | C8 技能工具 | queue | session（直执工具） | — | 可阻塞审批/网络/子进程 | |
| 54 | `/skill` | C8 技能工具 | queue | session（新 turn / 直执） | — | 默认发 turn；--direct 同 queue | |
| 55 | `/skills` | C8 技能工具 | list=inline；裸/select=screen；enable/disable=prompt | read / live | 确认 | 热刷新（skills_integration.go:1232）；select 仅填草稿（D8） | V8 |
| 56 | `/mcp` | C8 技能工具 | list/status=inline（若触网→queue）；裸/select=screen；add/remove/enable/disable/reload/auth=queue | read / next-call / external | 动作确认 | 写/重载含网络与子进程 | V9 |
| 57 | `/web` | C9 诊断 | status/token=inline；endpoints=screen；open=queue | read / external | — | open 起子进程 | |
| 58 | `/shell` | C2 回合 | queue | session（子进程+新 turn） | — | 含 send，不可运行时执行 | |
| 59 | `/todos`（**新增**） | **C12 任务与待办** | 默认 `screen`（可切换视图）；无副屏 `inline` | read | — | 复用 web 待办快照双通道；面板实时刷新；详见 F.7 | V12 |

### F.2 legacy / 别名（归并到主命令后不变）

| 入口 | 归并 | 结果 |
| --- | --- | --- |
| `/ ?` | `/help` | inline/read |
| `/h` | `/history` | screen/read |
| `/q`、`/quit` | `/exit` | block/process |
| `/cls` | `/clear` | queue/session |
| `/n` | `/normal` | prompt/next-turn |
| `/cmd` | `/shell` | queue/session |
| `/mode`、`/mode:<name>` | `/permission-mode` | status=inline；set=prompt+confirm |
| `/rewind` | `/backtrack` | 同主命令 |
| `/tool` | `/call` | queue |
| `/describe` | `/function` | inline |
| `/catalog` | `/functions` | inline |
| `/rename` | `/title` | 同主命令 |
| `/reasoning-effort` | `/reasoning_effort` | 同主命令 |
| `!` 前缀 | `/shell` | queue/session |

### F.3 边界说明

1. **block 最小集 = {`/exit`}**：唯二另需评审的候选是「回合后退出」（D11）与「强制中断当前 turn」类命令（D6，独立立项）。
2. **queue 不是拒绝**：C1 全部命令排队到 Ready 执行并给出「已排队，回合结束后执行」提示（P3），用户输入不丢失。
3. **next-call 生效域必须 prompt+confirm+审计**：权限/沙箱/信任类变更对运行中 turn 的后续工具调用立即生效，风险由「显式确认 + 生效提示 + 审计事件」承担（V7 复核是否改为 next-turn）。
4. **C11 长任务不进 capture 路径**：网络/OAuth/图像生成保持 queue；P3 可演进为「异步任务 + 进度副屏」（模式仍为 screen，但执行在 worker）。
5. **`session`/`process` 不可达断言**：宿主 `applyRuntimeEffect` 直接拒绝；T28 扩展到全部登记项（静态表断言）。

### F.4 开关建议默认值（灰度）

| 开关 | 默认 | 说明 |
| --- | --- | --- |
| `chat.runtime_interaction.mode` | `auto` | 按附录 F 执行 |
| `chat.runtime_interaction.categories.C5/C6`（权限/沙箱） | `auto` | 首轮灰度建议先置 `off` 观察（只读+queue），稳定后打开 |
| `chat.runtime_interaction.categories.C11` | `off` | 长任务固定 queue |
| `chat.runtime_interaction.commands."/exit"` | n/a | block 不可被开关放宽 |

### F.5 复核清单与第三轮核实状态（V1~V11）

| # | 项 | 关联 | 状态（第三轮核实后） |
| --- | --- | --- | --- |
| V1 | `/routing` 面板内是否含写入动作（整体 screen 还是逐动作 prompt） | 第二轮 E.4-1 | 未决；兜底逐动作 confirm |
| V2 | `/agents send\|followup\|cleanup` 的投递/回收安全性 | 第二轮 E.4-2 | **已核**：mailbox/registry 立即操作、与当前 turn 无耦合 → prompt 可行 |
| V2a | `/agents approve\|deny\|answer` 的写效应与 actor 竞态 | 第二轮 E.4-2 | 未决；暂 prompt+确认并专项测试 |
| V3 | `/theme` 全局态运行时切换安全性 | 第三轮 A | **已核**：只改全局 UI 状态与配置，无 turn 数据面耦合 → live |
| V4 | `/normal` 统一 TTY 迁移 | 第二轮 M6/N | 未决；迁移完成前保持 queue |
| V5 | `/model`、`/provider` 回合边界重建机制 | 第三轮 A | **已核**：`actorRebuildPending` + `reconcilePendingChatActorRebuild` + `refreshLocalRuntimeAfterSelection` → next-turn |
| V6 | `/account show`、`/accounts display` 是否零网络 | 第二轮 E.4-4 | 未决；暂 queue/screen 兜底 |
| V7 | C5/C6 生效边界 | 第三轮 A；D12 | **已核**：权限/信任/授权复用为 next-call（live 权限源）；`/add-dir` 策略面为 next-turn |
| V8 | `/skills` 选择仅填草稿（D8） | 第二轮 D8 | 未决；首期按草稿选择实现（D8 建议 (a)→(b) 过渡） |
| V9 | `/mcp list\|status` 是否触网 | 第二轮 E.4-3 | 未决；暂 queue |
| V10 | `/plan enter\|exit\|approve` 在 TUI 忙时的安全性 | 第三轮 A（新增） | 未决；TUI 无 `isRunActive` 门禁，需专项测试（ACP 已显式拒绝） |
| V11 | 闸门安装完整性（未安装 `commandGate` 的入口会放行） | 第三轮 C（新增） | 未决；P0 复核 agent-stdio/headless 入口（本方案范围限 TUI） |
| V12 | `/todos` 快照的 transcript 并发读安全性 | 第四轮（新增） | 未决；实现为 tool_end 缓存快照（加锁）+ 无缓存回退扫描；T41 `-race` 锁定 |

### F.6 第三轮核实摘要（2026-09-27）

1. **生效域机制已具备**：`actorRebuildPending`（chat.go:216-223）+ `markPendingChatActorRebuild`/`reconcilePendingChatActorRebuild`（chat_profile_switch.go:311-367）+ `refreshLocalRuntimeAfterSelection`（chat_actor_host.go:1477-1504）支撑 `next-turn`；`withLivePermissionModeSource`（chat_actor_executor.go:569-581）支撑 `next-call`；技能/MCP 热刷新与 actor mailbox 投递支撑 `live`（证据见 §3.8.4）。
2. **prompt 通道可复用**：priority prompt 由 busy capture（读方 goroutine）发起、经 `priorityLines` 通道回传，跨 goroutine 安全；单行文本 + 调用方解析；**无内建超时、不可重入**、`JSONOutput/NoInteractive` 需调用前预检（契约见 §3.8.4）。
3. **破坏类边界**：`/exit`、`/new`、`/load`、`/resume`、`/clear`、`/compact`、`/backtrack --apply`、`/plans reopen`、`/goal set`、`/skill --direct`、`/call`、`/shell`、`/login`、`/mcp auth|reload`、`/account(s) refresh|--save` 若**忙时执行**会破坏在途状态或派生并发 turn；本方案统一由 `queue` 承载（回合结束后执行、不丢输入），仅 `/exit` 为 `block`（D11 复盘）。
4. **闸门完整性**：忙时拦截依赖 `setCommandGate` 被安装（chat_busy_input.go:183）；agent-stdio/ACP/headless 入口未安装时 `routeLineWithCommandGate(enforce=false)` 会放行 → P0 复核项 V11（本方案范围限 TUI）。
5. **现状差异提醒**：`chatSlashCommandQueueSafe` 目前对 `/provider`、`/model` 按名无条件放行（未区分只读/变更）；v1.3 改为按变体分流（status=inline、切换=screen+confirm）。

---

### F.7 新增命令建议：`/todos`（任务面板，可切换查看）

> 用户场景：turn 运行中想查看当前待办，并能在"全部 / 进行中 / 待办 / 已完成"等视图间切换。

**现状核查（2026-09-27）**

- catalog 与结构化注册表**均无** `/todos`（grep 确认）；TUI 只在工具 cell 里按大预览渲染 `todos` 工具结果（`chat_tool_rendering.go:359`、`chat_runtime_events.go:9828-9844`，32 行/240 字符摘要）。
- 数据面已就绪且形状统一：`{items:[{content,status,active_form}], session_id, goal_id}`。
  - 实时通道：`tool_end.payload.protocol_result.metadata.todo_snapshot`（`web_schema.go:286-290`）。
  - 回放通道：扫描 transcript 最近一次 todos 结果（`chatWebTodoSnapshotFromMessages`，`web_todo_snapshot.go:87`）。
  - 会话级入口：`chatWebTodoSnapshotForSession`（`web_todo_snapshot.go:189-196`）。
- 结论：**不需要新协议**，`/todos` 直接复用既有快照；是统一运行时交互机制（§3.8）的理想示范命令。

**注册（§3.8.2）**

```go
runtimeCommandSpec{
    Command:  "/todos",
    Category: C12,                 // 任务与待办
    Mode:     screen,              // 无副屏/租约失败 → inline（T40）
    Effect:   read,                // 只读快照；不写会话/不发 turn
    Confirm:  false,
    Notice:   "实时快照",           // 无写语义，无需生效域提示
}
```

**交互设计（"切换查看"）**

1. **参数直达**：`/todos`（默认全部）、`/todos active`（进行中+待办）、`/todos done`、`/todos all`、`/todos brief`（单行摘要，适合 inline）。
2. **屏内切换**：↑/↓ 选择、`Tab` 或数字键切过滤（全部/进行中/待办/已完成）、`g` 按 goal 分组、`b` 返回 brief；视图切换不重建 ScreenLease。
3. **实时刷新**：面板每 1s 或订阅 `tool_end` 事件刷新（Refresh 回调须非阻塞，沿用 debug overlay 契约，`ui/debug_overlay.go:37-52`）；主屏流式帧在租约期间被 `Deferred`（§3.7.2），互不干扰。
4. **降级**：无副屏能力/租约超时 → inline 快照（同一过滤视图）；无数据 → 「当前会话暂无待办」；非统一渲染面/开关关闭 → queue。
5. **等价入口**：Web 侧任务列表面板与 ACP plan entries 已存在，`/todos` 只补 TUI 的"按需查看"入口，不改变既有通道。

**实现要点与并发（V12）**

- 快照优先读 `tool_end` 缓存（每会话一份，带锁更新）；无缓存时回退 `chatWebTodoSnapshotForSession()` 扫描 transcript。
- 运行中 turn 并发写 messages：回退扫描路径必须走 `-race` 用例（T41）；若确认无锁不安全，则改为"仅读缓存 + 启动时预热一次回放快照"。
- 注册表差集测试（T29）自动要求 `/todos` 有 spec；降级矩阵沿用 §3.7.3。

**落地状态（2026-09-27，v1.3.1）**

- ✅ **inline 版已实现**：
  - catalog 注册：`chat_slash_command_catalog.go`（`/todos [all|active|done|brief]`，Session 组，补全可用）；
  - 结构化处理器：`chat_todos_command.go`（`executeStructuredTodosCommand` / `parseChatTodosFilter` / `buildChatTodosDocument` / `formatChatTodoLine`），数据面复用 `chatWebTodoSnapshotFromMessages`；
  - 接入：`chat_command_result.go` 结构化注册 + legacy 围栏白名单；忙时排队过渡：`chat_input_queue.go` `chatSlashCommandQueueSafe`（宿主落地后迁入注册表 runtime spec）；
  - legacy/plain 出口：`command.go` 的 `handleCommand` 新增 `/todos` 路由（`handleTodosCommand` 共享同一快照与视图逻辑）——`handleCommand` 是路由表的权威来源，缺它会导致 catalog/路由一致性测试失败（首轮回归实测 72 vs 73，已修复）；
  - 测试：`chat_todos_command_test.go` 5 项全绿（catalog 注册、忙时可排队、快照渲染与 all/active/done/brief 过滤、空数据/未知参数/nil 会话、结构化分发识别）。
  - **验证**：`go build ./cmd/aicli/...` ✅；`gofmt -l` ✅；`-race`（ChatTodos）✅；整包回归无新增失败——仅 2 个**基线既有**的环境相关失败（`TestHumanizeActorExecutorError_AppendsRuntimeHTTPPreview`、`TestChatDebugDisplayShowsStorageSection`），已用 `git stash` 基线对照确认与本改动无关。
- ⏳ **待实现（P2-4）**：screen 任务面板（BusyScreen + 屏内切换/实时刷新）、`tool_end` 快照缓存（V12）、T38/T41。
- 备注：当前忙时语义为「排队到回合结束」（不丢输入）；inline 运行时直读（`read` 生效域）随 `runtimeCommandHost` 一并开放（§3.8.4）。

---

## 附录 G：实施记录（v1.3.2，2026-09-27/28）

### G.1 P1-1 ~ P1-3（已完成；灰度默认关闭 = 零行为差异）

| 项 | 落地 |
| --- | --- |
| P1-1 | `chatSlashCommandSpec.BusyPolicy`（`chat_slash_command_catalog.go`）；首批 §4.1 四条（`/help`、`/status`、`/session`、`/queue`）标注 `chatBusyPolicyImmediate`，其余零值 inherit |
| P1-2 | `chat_busy_command_policy.go`：`chatBusyCommandPolicy`（inherit/I/S/D/R）、`chatBusyCommandEnabled`（`AICLI_CHAT_BUSY_COMMAND`，默认关）、`chatSlashCommandBusyPolicyFor`（别名归一、参数守卫、`/queue clear` 子命令覆盖、fail-closed）、`chatInputCommandBusyPolicy`（Ready 语义等价包装） |
| P1-3 | `chat_input_queue.go`：`chatInputRouteImmediate`/`chatInputRouteScreen` + 访问器；字段 `commandGate`→`commandPolicy`；新增 `setCommandPolicyResolver` / `setBusyCommandExecutor`（`setCommandGate` 保留兼容包装，既有测试零改动）；`routeLineWithCommandGate` 四档分支 + INV-8 来源分流 + 消费方未注册时回退排队；`chat_busy_input.go` 捕获循环对 I/S 兜底 requeue（绝不丢输入） |

**测试**：`chat_busy_command_policy_test.go` 6 项（含 T18 关闭等价）＋ `chat_busy_command_routing_test.go` 5 项（I/S 路由、INV-8、无消费方回退、关闭等价、旧门包装）全绿；`-run 'ChatBusyCommand|ChatInputQueue'` 全量通过；`go build`/`gofmt` 干净。

### G.2 独立勘查发现与方案修正（第三轮，2026-09-27）

1. **§3.3 调用点偏差**：`agent_stdio_slash.go:94` 实际调用 `tryExecuteStructuredChatCommand`（`:93-104`），**不是** `dispatchChatCommand`；ACP 忙时命令不经本通道。`dispatchChatCommand` 生产调用点仅 `chat.go:1714/1773`（均不持会话锁）。
2. **M4 范围扩大**：除 `queuedInputDrain`（`chat_input_queue.go:1425` 裸读，写方 `chat_team_drain.go:117-138`）外，`session.queuedInputEchoed` 亦为无锁读写（`chat.go:300`、`chat_busy_input.go:126/136`）。**P1-5 启用 `/queue` immediate 前必须先完成 M4**（否则 T9 `-race` 必红）；本轮不动（保持既有测试零改动），登记为 P1-5 前置阻断项。
3. **行号漂移**：`routeLineWithCommandGate` 实际 `:1678-1701`；`queuedInputDrain` 读取在 `:1425`。
4. `chatInputCommandQueuable`（忙时队列门）与 `chatInputCommandAllowed`（Ready 门，`chat.go:1763`）是两条语义；P1-3 只迁移前者，后者未动。
5. 包内暂无队列/忙时 `-race` 用例（T9/T20/T27/T35 需新建；`-race ChatTodos` 已有）。

### G.3 下一步（P1-4 → P1-5）

- **P1-4**：新增 `ChatSession.commandMu`（建议置于 `chat.go:250-256` 邻近）+ `dispatchChatCommand` 两阶段切分（Phase A 以 `command.go:34` 渲染完成为界；pickers `:50-131`、sends `:132-163` 留在 Phase B 锁外）。
- **P1-5**：`executeBusySlashCommand` 插入 `chat_busy_input.go:120-124`；三道门（`unifiedDirectInteractiveOutput` / `chatInputArbitrationSnapshotOf` 的 `ModalDepth` / 来源）+ TryLock 降级 + 忙时安全断言 + 历史记录；并完成 M4 原子化后方可让 `/queue` 在灰度打开时实际立即执行。
- 验证：T18（关闭等价）已由单测覆盖；T9/T16 待 P1-5/P1-6。

---

### G.4 P1-4 ~ P1-5 与 M4（已完成，2026-09-28）

| 项 | 落地 |
| --- | --- |
| P1-4 | `ChatSession.commandMu`（`chat.go:256` 邻近）+ `lockChatCommandPhaseA`（`command.go`）；`dispatchChatCommand` 的 Phase A（解析 + 会话/配置变更 + 渲染）持锁，Phase B 效应块在 `unlockCommand()` 之后锁外执行（send/picker/screen 不再被锁跨住） |
| P1-5 | `chat_busy_command_exec.go`：`executeBusySlashCommand`（策略再校验 → 统一渲染面门 → 仲裁 L0 门 → `TryLock` → 解析 → 忙时安全断言 → 渲染 → 历史记录）；降级路径输出可见提示；`consumeBusyCommand` 交接 + 捕获循环「未占有 → requeue」兜底 |
| M4 | `queuedInputDrain` / `queuedInputEchoed` 改为 `atomic.Bool` + 会话访问器；全部读写点（含 6 处既有测试字面量，机械迁移）同步；`TestChatSessionQueuedInputFlagsConcurrentAccess` 供 `-race` 验证 |
| P1-6 | INV-8 来源分流已在 P1-3 落地；Web 回执/唤醒语义回归（`-run Web`，15.3s）全绿 |
| P1-7 | 灰度默认关；T18 等价在策略层（`TestChatBusyCommandPolicyGateOffMatchesLegacyWhitelist`）与路由层（`TestChatInputQueuePolicyRoutingGateOffMatchesLegacyGate`）双重断言 |
| P1-8 | 首批 §4.1 四条；M4 完成后 `/queue` 忙时读取已无竞态前置 |

**新增测试（8 项）**：`TestChatBusyCommandUnsafeEffect`、`TestLockChatCommandPhaseA`、`TestDispatchChatCommandReleasesCommandPhaseALock`、`TestChatBusyCommandArbitrationAllowsFailClosed`、`TestExecuteBusySlashCommandGuards`、`TestExecuteBusySlashCommandUnifiedSession`（统一渲染面 happy path，断言结果到达 presenter）、`TestChatSessionQueuedInputFlagsConcurrentAccess`、`TestChatInputQueueConsumeBusyCommand`。

**验证**：`go build ./cmd/aicli/...` ✅；`gofmt` 干净；`-race`（ChatBusyCommand/ChatInputQueue/ChatTodos/执行器/锁语义）✅；整包回归仅剩 4 个已知基线失败（`TestRunChatLoopInteractiveInitialPromptSubmitsOnceAndStaysInteractive`、`TestRunChatLoop_DrainsQueuedLinesAfterTeamSettlesBeforePrompt`、`TestBuildChatSurfaceStatusLine_DedupesProjectWhenSameAsDirectory`、`TestComposeLocalChatSystemPrompt_IncludesWorkspaceGuidance`，均已用 `git stash` 基线对照确认为既有环境问题），无新增失败。

> 说明：「现有测试零改动」的 P1-7 退出条件针对**行为语义**；M4 因字段类型迁移对 6 处测试字面量做了机械改写（`queuedInputDrain: true` → `setQueuedInputDrainActive(true)`），不改变任何断言意图。
> 灰度打开后的行为：`/help`、`/status`、`/session`、`/queue`（非 clear）在 turn 运行期间立即可见；`/queue clear` 保持拒绝；其余命令保持现状（queue/reject）。

**P1 阶段收口结论**：P0（分类基线/快照设计）+ P1（元数据/解析器/四档路由/两阶段锁/执行器/M4/灰度）全部落地，下一步进入 **P2**（统一交互机制：注册表与解析器 P2-1 → 三级开关 P2-2 → 宿主与生效域执行器 P2-3 → 副屏与 prompt 载体 P2-4 → 生效域适配 P2-5）。

### G.5 P2-1 注册表与解析器（已完成，2026-09-28）

| 项 | 落地 |
| --- | --- |
| 类型 | `runtimeInteractionMode`（inline/screen/prompt/queue/block）、`runtimeEffectScope`（read/live/next-turn/next-call/session/process）、`commandCategory`（C1~C12）、`runtimeCommandSpec`（Command/Category/Mode/Effect/Confirm/Notice/SwitchKey） |
| 数据 | `runtimeCommandRegistry`：附录 F.1 全量 **59 条**（58 catalog + `/todos`）逐条代码化；复合命令按变体表声明（`/debug`、`/supervision`、`/agents`、`/goal`、`/model`、`/provider`、`/mcp`、`/skills`、`/account` 等）；`/exit` 唯一 `block` |
| 归并 | `canonicalRuntimeCommandName` + `runtimeLegacyAliasCommands`：catalog 别名索引优先，F.2 legacy 入口补齐（`/?`、`/h`、`/q`、`/quit`、`/cls`、`/n`、`/cmd`、`/rewind`、`/tool`、`/describe`、`/catalog`、`/rename`、`/reasoning-effort`）；`/mode:<name>` 冒号简写归并到 `/permission-mode` |
| 解析 | `resolveRuntimeCommandSpec`：Bare → Variants[首 token] → Wildcard → **queue 兜底（ok=false，INV-6）**；返回时自动补 `SwitchKey` |
| 校验 | `runtimeCommandRegistryViolations`：`session`/`process` 生效域只允许 queue/block（T28 静态断言） |

**测试（4 项）**：`TestRuntimeCommandRegistryCoversCatalog`（T29：catalog 主命令与别名全覆盖 + 非空条目）、`TestRuntimeCommandRegistryInvariants`（T28）、`TestResolveRuntimeCommandSpecVariants`（22 组变体/别名/未知兜底）、`TestResolveRuntimeCommandSpecSwitchKey`。

**边界**：本步骤只做声明与解析，**尚未被路由消费**——消费路径将在 P2-2（三级开关）与 P2-3（宿主 + 生效域执行器）接入；接入前运行时行为仍由 P1 的 BusyPolicy 通道决定。

### G.6 P2-2 三级开关（已完成，2026-09-28）

| 项 | 落地 |
| --- | --- |
| 全局 | `AICLI_CHAT_RUNTIME_INTERACTION`：`auto`（默认）/`readonly`（仅 `effect=read` 可运行时执行）/`off`（除 block 外全部降级 queue） |
| 分类级 | `AICLI_CHAT_RUNTIME_INTERACTION_CATEGORIES`，逗号分隔 `C5=off,C11=readonly`（键大小写不敏感） |
| 命令级 | `AICLI_CHAT_RUNTIME_INTERACTION_COMMANDS`，`/model=off,/mode:yolo=readonly`（键先 `canonicalRuntimeCommandName` 别名归并；非法项忽略） |
| 优先级 | 命令级 > 分类级 > 全局；`block` 只增不减、任何层级不可放宽 |
| 降级语义 | 仅把 `Mode` 收敛为 `queue`，`Effect`/`Category`/`Notice` 保持声明值（宿主按 Notice 提示） |

**测试（4 项）**：`TestRuntimeCommandSwitchOffDegradesAllToQueue`（T31：遍历注册表全集断言 off 降级且 block 不变）、`TestRuntimeCommandSwitchReadonlyKeepsReadEffects`、`TestRuntimeCommandSwitchPrecedence`（含命令级覆盖分类级与 read 变体保留）、`TestRuntimeSwitchTableFromEnv`（解析/别名归并/非法项）。

**边界**：与 P2-1 相同，本步骤只提供解析与降级函数，**尚未被路由或宿主消费**；运行时行为在 P2-3 接入前仍由 P1 的 BusyPolicy 通道决定。

### G.7 P2-3 宿主与生效域执行器（已完成，2026-09-28）

| 项 | 落地 |
| --- | --- |
| 宿主 | `runtimeCommandHost.SubmitBusy`（`chat_runtime_command_host.go`）：注册表解析 → 三级开关降级 → P1 总闸（未开一律 queue）→ 生效域守卫 → 模式分发（inline/queue/block 已落地；screen/prompt 降级，待 P2-4 载体）→ 审计发布 |
| 执行原语 | 从 P1 执行器抽出 `runBusyInlineCommand`（统一面门 → 仲裁 L0 门 → Phase A TryLock → 解析 → 忙时安全断言 → 渲染 → 历史）；`executeBusySlashCommand` 保留为「P1 策略 + 原语」兼容入口 |
| 生效域守卫（T32） | `runtimeHostEffectReachable`：`session`/`process` 在宿主层不可达；唯一合法组合是 `block+process`（`/exit`，只拒绝不执行）——该例外在守卫判定中显式放行 |
| Notice（T32） | inline 且生效域非 `read` 的命令，执行后按 `spec.Notice` 给出生效提示（如 `/title <text>` → 「下一回合生效」） |
| 审计（T33） | 事件 `aicli.chat.runtime_interaction`（payload：command/registered/mode/effect/result/occupied/duration_ms）；加入渲染数据面抑制清单（写入 eventLog/timeline/SSE，不产生 Scene 系统消息噪声） |
| 路由接管 | `chatSlashCommandBusyPolicyFor` 在**双开关**下改由注册表映射：`block→R`、`inline→I`、`screen/prompt/queue→D`、未登记→D（T30）；映射发生在既有 catalog/子命令覆盖之后，保证 P1 已定级命令不回退 |
| 灰度门（关键） | `chatRuntimeInteractionRegistryActive` = `AICLI_CHAT_BUSY_COMMAND` 打开 **且** `AICLI_CHAT_RUNTIME_INTERACTION` 被显式设置；P2 变量未设置时行为与 P1 首批白名单逐条一致（T18/T31 等价性保持） |
| 接线 | capture 循环的 busy 消费方由 `executeBusySlashCommand` 改为 `runtimeCommandHost.SubmitBusy`（未占有一律回退入队，不丢输入） |

**新增测试（7 项）**：`TestRuntimeCommandHostInlinesRegisteredReadCommand`（非首批 read 命令立即执行 + 审计内容 + 锁释放）、`TestRuntimeCommandHostDegradesScreenMode`（T6）、`TestRuntimeCommandHostQueuesUnknownCommand`（T30）、`TestRuntimeCommandHostRejectsBlockCommand`（block 拒绝 + 提示）、`TestRuntimeCommandHostEmitsNoticeForNextTurnEffect`（T32 Notice）、`TestRuntimeCommandHostEffectReachability`（T32）、`TestChatBusyPolicyRegistryMappingWhenP2Enabled`（auto/readonly/off/未启用 四态路由映射）。

**验证**：`go build`/`gofmt` 干净；`-race`（host/registry/switch/queue/busy/executor 选择集）✅；整包回归仅剩 2 个 `RunChatLoop` 已知基线项（此前 4 项中 2 项已由并行工作流修复），无新增失败。

**偏差与边界**：① `applyRuntimeEffect` 的 next-turn/next-call **适配器本体**属 P2-5，本步骤只做「生效域可达性守卫 + Notice + 审计」；当前 inline 集合中仅 `/title <text>` 为非 read 生效域，其写盘由既有 handler 完成。② P2 路由接管要求 P2 变量显式设置（而非 F.4 的默认 auto），作为分阶段灰度的过渡规则；P2-5 收口后可按 F.4 将默认切为 auto。③ `/clear` 等 C1 命令在 P2 显式启用后由「拒绝」变为「排队」，文案变更仍按 M5 留到 P3。

### G.8 P2-4a 副屏前置①租约等待 + ②流式缓冲核验（部分完成，2026-09-28）

| 项 | 结论 |
| --- | --- |
| ① 租约等待 | 新增 `FixedBottomSurface.AcquireAlternateScreenWait(ctx, req, budget)`（`ui/screen_lease.go`）：预算内轮询等待在途租约释放（默认 2s，10ms 间隔）；`budget<=0` 取默认；ctx 取消用 `errors.Join` 保留 `ErrScreenLeaseBusy`+`context.Canceled` 双语义；非租约类错误（surface 未启用/无统一 transport）立即返回不等待。 |
| ① 测试 | `ui/screen_lease_wait_test.go` 5 项：空闲立即获取、等在途释放后获取（校验确实等待且 ID 更新）、预算耗尽（校验耗时下界与不返回 lease）、ctx 取消（双 `errors.Is` + 及时性）、致命错误不等待（含 nil surface）。 |
| ② 流式缓冲 | **核验结论：无需新增 `StreamHold/StreamFlush` 契约**。租约期间主屏 frame 在 `terminal_session.go:904-906` 强制 `Invalidate()` + `projectionKnown=false`，`Flush` 返回 `Deferred` 且零字节写入；释放后首帧 `FullRepaint` 且 projection 确认。既有 `TestTerminalSessionLeaseReleaseForcesPrimaryRecovery` 已逐条锁定这些断言（本步骤复跑通过）。 |
| ② 余量 | 「长租约期间 history-effect ledger 条目增长是否有界」属**量化问题**，计划本身已挂在 U1/P0-2 验证清单（`Summary().LedgerEntries`/`PlanCount`/`MaxPlanMs` 为现成读数），不在本步骤补 API。 |
| 未完成 | ③ 输入所有权移交、④ `busyScreenActive` 门、⑤ 首批 S 档命令接入 host——三者耦合（只有真正打开副屏才需要 ③④），建议作为同一增量落地：先接一个白名单命令（推荐 `/todos` 或多页 `/history`）打通「租约→L0 移交→交互→关闭→恢复」全链路，再批量放量其余 5 条。 |

**验证**：`go build`/`gofmt` 干净；`ui` 包租约相关回归子集 + 新增 5 项测试全绿。

### G.9 P2-4b-1 租约等待预算接入 surface（2026-09-28）

| 项 | 结论 |
| --- | --- |
| API | `FixedBottomSurface.SetAlternateScreenWaitBudget(budget)` / `AlternateScreenWaitBudget()`：0（默认）保持历史语义（撞租约立即 `ErrScreenLeaseBusy`）；>0 时**既有** `AcquireAlternateScreen` 调用点在预算内轮询等待（10ms 间隔）。内部重构为 `acquireAlternateScreenOnce`（单次尝试）+ `acquireAlternateScreenWithBudget`（等待语义），`AcquireAlternateScreenWait(ctx,req,budget)` 显式预算优先。 |
| 为什么这样接 | 各 S 档 handler（`/resume` `/history` `/todos` `/backtrack` `/export` `/mcp` `/model` 面板 `openChatLeaseBoundTextViewer` 等 ~20 处）都直接调用 `session.Surface.AcquireAlternateScreen`；surface 级预算让忙时宿主只需在执行 S 档前 `SetAlternateScreenWaitBudget(2s)`，无需改任何 handler。 |
| 测试 | 新增 2 项：默认 0 撞租约立即失败（耗时上界）→ 设 1s 后同一调用点等待释放并成功（耗时下界）→ 预算可复位；与既有 5 项等待测试共同覆盖「立即/等待/超时/取消/致命错误/复位」。 |
| 当前状态 | 预算 API 已就绪但**尚无生产调用方**（宿主 `runScreen` 属下一增量），属分阶段基础设施；默认 0 保证零行为变化。 |

**下一增量（P2-4b-2）设计已定（本步骤勘察结论）**：

1. **stdin 所有权**：`startBusyQueuedInputCapture`（`chat_busy_input.go:16-51`）在**单个 goroutine** 内串行执行「`capture.ReadLine` → 路由 →（回调）执行命令」，busy 命令消费回调在同一 goroutine 内同步执行。因此在其中运行 S 档 handler 时，**该 goroutine 不会再有并行 `ReadLine`**，副屏的 `os.Stdin` 读者无竞争——③ 的实质要求由结构保证；仍需 `chatInputOwnerModal` 影子登记（L0 对外可见 modal）+ 关闭后恢复 `chatInputOwnerBusyCapture`。
2. **④ 跨回合门**：`prepareInteractiveRead(session)`（`chat_team_drain.go:106`）是主循环读输入前的唯一入口，应在此加 `busyScreenActive` 等待（屏幕跨回合未关闭时不得开读）。
3. **能力门（fail-closed）**：统一渲染面 + `session.Surface.Enabled()` + 全屏能力（`CanUseFullScreenList` / `canOpenChatDebugOverlay` 同款判定）+ 非 JSONOutput/NoInteractive + L0 无 priority prompt 待答；任一不满足 → 降级 queue（保持现状行为）。
4. **放量顺序**：先 `/todos`（面板，§3.7.4 白名单）单条打通「租约（预算）→ modal 登记 → 只读交互 → Close → `Release` → `RequestPrimaryRecovery` → 恢复 capture」，再依次放 `/history`、`/usage`、`/debug display`、`/web endpoints`、`/account --no-refresh|show`；T20~T26 的 TTY 行为验证随后补齐。
5. **灰度**：screen 档仅在 `AICLI_CHAT_RUNTIME_INTERACTION` 显式启用（auto）时生效；文档提示 T20~T26 未过前建议保持 `off`/`readonly`。
