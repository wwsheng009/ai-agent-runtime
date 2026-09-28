# 审查报告：《aicli TUI 运行中 slash 命令执行方案》（Draft v1 → v1.1）

> 审查对象：`docs/plan/aicli-tui-busy-slash-command-execution-plan-20260927.md`（Draft v1）
> 审查日期：2026-09-27
> 审查方式：作者自验（第一手代码证据）+ 一路只读独立审查子代理（跨「输入路由 / 命令分发 / 渲染并发 / 输入仲裁」四个子系统核验，全部结论附 `file:line`）
> 证据口径：本文所有结论可追溯到代码；未获证实项标注「待验证」，不作推断；本报告不含实现。
> 版本说明：方案已按本报告修订为 v1.1（§4 为落实对照表）；本报告保留 v1 问题记录，供实现期对照。

---

## §0 总体结论

**v1 稿方向成立（BusyPolicy + immediate 通道 + 忙时只读执行），但不能直接进入实现。** 共发现 **2 项阻断（B）**、**6 项重要（M）**、**5 项次要（N）**；其中 B1 必然导致 Web 侧命令丢失，B2 必然导致「命令触发的 turn」期间 G1 失效并堵死 capture 输入。v1.1 已全部修订或显式记录。

| 分级 | 数量 | 性质 |
| --- | --- | --- |
| B（阻断） | 2 | 事实/设计缺陷，直接导致输入丢失或目标失效 |
| M（重要） | 6 | 不修订则实现期返工或 `-race` 先红 |
| N（次要） | 5 | 锚点、一致性、范围声明 |

**修订后结论：建议按 v1.1 进入 P0（盘点与并发基线），P0 全部门禁（§6）通过后再进入 P1 实现。**

---

## §1 阻断问题（B）

### B1 Web/远程注入会被静默吞掉

- **问题**：v1 的 `immediate` 档「不入队」，但路由函数 `routeInputTextFromSource`（`chat_input_queue.go:498-509`）同时服务 TUI stdin 与 Web 注入（`web_handlers.go:990`、`web_invoke.go:345`）；Web 侧只在 `result.queued()` 时唤醒主循环（`web_handlers.go:991-999`），并固定回执 `{"status":"queued"}`（`:1024`）；`web_invoke.go:355-363` 只识别 `rejected()`。若 Web 注入的行被判为 immediate，则该命令既不执行（busy capture 只读 stdin）也不入队，调用方却收到「已排队」——静默丢失。
- **修订要求**：路由按来源分流。仅当 `chatInputSourceIsLocalTerminal(source)`（`chat_input_queue.go:482-489`）成立时才可能返回 immediate；web/远程/未知来源一律 deferred，保持「排队 + 唤醒 + queued 回执」语义不变；新增 Web 专项用例（T16）。
- **v1.1 落实**：§2.2 INV-8、§3.2 第 5 点、§3.5、§6 T16、§7 R9。

### B2 `commandMu` 跨整个嵌套 turn 持有，G1 失效且 capture 被堵死

- **问题**：v1 §3.3 要求 `chat.go:1714/1773` 在 `dispatchChatCommand` 外层持锁。而 `dispatchChatCommand` 的 Phase B 效应会同步开新 turn：`command.go:132-155` 的 `SendObjective`/`SendMessageAfterCommit`/`SendSkillTurn` → `sendGoalObjectiveRequest`/`sendChatMessageAfterCommit`/`sendSkillTurnRequest` → `chat_send.go:74-75,121` 的 `executor.Execute` 同步跑完整 turn。后果：`/goal`、`/shell`、`!cmd`、`/skill` 触发的 turn 运行期间，busy capture 的 immediate 执行会阻塞到该 turn 结束（G1 失效），且 capture 循环被堵死、无法继续读取输入或处理 Esc。
- **修订要求**：改为**两阶段锁**——`commandMu` 只覆盖 Phase A（解析 + 会话变更 + 渲染），Phase B 效应（send / picker / 屏幕）在释放锁后应用；busy 侧用 `TryLock`，失败即降级 deferred，capture 循环永不阻塞；新增「锁内无 send」静态检查/测试断言。
- **v1.1 落实**：§2.2 INV-1、§3.2 第 4/6 点、§3.3、§5 P1-4、§7 R10、§8 验收 8、附录 D。

---

## §2 重要问题（M）

### M1 仲裁快照 API 名错误

- **事实**：v1 引用的 `Snapshot()` 不存在；实际为 `chatInputArbitrationSnapshotOf(session) (snapshot, ok bool)`（`chat_input_arbitration.go:226-237`），`ok=false` 表示从未注册过仲裁器，调用方应回退旧位路径。modal 降级的可达窗口（priority 已翻转但 modal 未清理）需要显式 fail-closed：`ok=false` 时不得放行 immediate。
- **v1.1 落实**：INV-7、§3.2 第 3 点、§3.6。

### M2 未限定「统一渲染面」，immediate 可能落入 legacy 直写路径

- **事实**：`startBusyQueuedInputCapture` 的入口条件（`chat_busy_input.go:20`）只检查 `InputBox`/`Interaction`，不检查统一渲染面。若在非统一面执行 `dispatchChatCommand`，会落入 legacy `handleCommand`（`command.go:172-178`），包含 `fmt` 直写与 `/shell` 的 `sendMessage` 路径（`command.go:185-200`），INV-2/INV-4 的「仅结构化结果」断言失效。
- **修订要求**：immediate 仅在 `unifiedDirectInteractiveOutput(session)` 成立时放行；否则 deferred；并断言命令结果不得为 `CommandQuit`。新增 T17。
- **v1.1 落实**：INV-2、§3.2 第 4 点 2)、§3.3 契约表、T17。

### M3 命令历史 / echo / 状态行未定义

- **事实**：忙时输入的唯一历史入口在 `chat_busy_input.go:131-133`（仅 queued 且非 slash 才 `AddToHistory`）与 `chat_input_queue.go:1177-1182`；v1 的 immediate 分支会整体跳过，出现「执行过但历史里没有」的行为分叉。
- **修订要求**：显式定义——immediate 执行成功后经 `recordChatPromptHistory` 入历史；queued slash 不入历史的现状保持不变；不置 `queuedInputEchoed`（未入队）；状态行刷新行为明确并测试（T19）。
- **v1.1 落实**：§3.5、T19。

### M4 `/queue` 依赖的 `queuedInputDrain` 是裸读，`-race` 会先红

- **事实**：`queuedInteractiveInputState`（`chat_input_queue.go:1416-1421`）直接读 `session.queuedInputDrain`，写方在 `chat_team_drain.go:117-138` 无同步。`/queue` handler 经 `chat_command_result.go:943-954` 调用它，T9 并发用例会先于功能问题暴露为数据竞争。
- **修订要求**：把该字段收进 immediate 快照，或改原子读写；P0-3 的快照清单必须包含它。
- **v1.1 落实**：§1.2#5、§3.5、§4.1、§5 P0-3。

### M5 deferred 提示文案与「现有测试零改动」冲突

- **事实**：现状忙时 queued 不产生「已排队」文案（`chat_busy_input.go:125-133`）；v1 P3 直接改文案会让「既有白名单行为不变（T7）」自相矛盾，灰度期无法用现有测试锁基线。
- **修订要求**：文案变更归入 P3 独立切换；P1/P2 与现状逐字一致（D7）。
- **v1.1 落实**：§3.5、§5 P3、T7、§9 D7。

### M6 `/model`、`/provider`、`/status` 的分类依据与代码不符

- **事实**：`executeStructuredModelCommandVariant`（`chat_model_picker.go:443-461`）在无变更时：`canOpenChatModelPicker` 通过才返回 picker；忙时因 run 活跃不通过，退化为 `runtimeModelStateText` 状态文本（`:449-455`）。`/provider` 同链路。另外结构化 `/status`（`chat_command_result.go:598-607` → `chat_status.go:85-151`）**不**调用 `refreshChatConfigIfChanged`（对比 legacy `chat_status.go:61`）。v1 的「裸 `/model` 因 picker 保持 deferred」结论正确但依据错误。
- **修订要求**：所有分类依据统一改为「Ready 路径真实行为」，逐条附 handler 证据；§4.1 表格已重写。
- **v1.1 落实**：§4.1、§4.3、§3.1「效应枚举」约束。

---

## §3 次要问题（N）

| # | 问题 | 证据 | v1.1 落实 |
| --- | --- | --- | --- |
| N1 | `confirmDraft` → `rejectCommandInput` 的命令门需随 resolver 迁移；immediate 在草稿确认路径不得被当 rejected | `chat_input_queue.go:592,810-818` | §3.2 第 2 点 |
| N2 | `agent_stdio_slash.go:94` 是第三个 `dispatchChatCommand`/结构化执行入口，需声明范围 | `agent_stdio_slash.go:94` | §0 非目标、附录 D |
| N3 | `/queue` 未知参数：白名单放行但 handler 报错，档位解析（v1.2 四态 I/S/D/R）需同源收敛 | `chat_input_queue.go:1251-1252` vs `chat_command_result.go:947-961` | §3.1、T18 |
| N4 | `chat.go:1140-1143` 是会话信息打印路径，与 `/queue` handler 不同源 | `chat.go:1140-1143` | §5 P3 |
| N5 | `ChatSession` 还有 `runtimeCtxMu` 等专用锁，事实陈述需补全 | `chat.go:253-254` | §1.2#5 |

---

## §4 v1.1 修订落实对照表

| v1 问题 | 修订位置（v1.1） | 状态 |
| --- | --- | --- |
| B1 Web 静默吞命令 | INV-8；§3.2 第 5 点；T16；R9 | 已落实 |
| B2 锁跨嵌套 turn | INV-1；§3.2 第 4/6 点；§3.3 两阶段锁；P1-4；R10；验收 8 | 已落实 |
| M1 仲裁 API 名 | INV-7；§3.2 第 3 点；§3.6 | 已落实 |
| M2 统一渲染面 | INV-2；§3.2 第 4 点 2)；§3.3 契约表；T17 | 已落实 |
| M3 历史/echo/状态行 | §3.5；T19 | 已落实 |
| M4 `queuedInputDrain` 裸读 | §1.2#5；§3.5；§4.1；P0-3 | 已落实 |
| M5 文案版本化 | §3.5；§5 P3；T7；D7 | 已落实 |
| M6 分类依据 | §4.1；§4.3；§3.1 | 已落实 |
| N1~N5 | 见 §3 表右列 | 已落实 |
| 递归 dispatch 死锁 | 附录 D（生产调用点仅 5 处，picker 不回调）；§3.3 测试断言 | 已排除 |

---

## §5 最佳实践清单（实现期必须遵守）

1. **用效应枚举而非命令名做 immediate 白名单**：send / actor / 存储 / 屏幕租约 / 输入模式 / 阻塞 IO 六类，任一命中即不得 immediate；T13 防御断言。
2. **两阶段锁**：`commandMu` 只覆盖 Phase A（解析+变更+渲染）；锁内禁止 `sendMessage`/交互等待，配静态检查或「锁内无 send」测试。
3. **来源隔离**：Web/远程/未知来源默认「不丢输入优先」，immediate 一律降级 queued（B1 教训）。
4. **不可变快照**：immediate handler 只读快照；字段读写表进 `-race` 用例（含 `queuedInputDrain`）。
5. **灰度默认关 + 等价表**：resolver 与旧 `chatSlashCommandQueueSafe` 逐条对照（T18），关闭时零行为差异。
6. **原子占有**：在队列临界区内完成「判定+去向」，取消 `IsReady` 检查 + `requeueFront` 的两步交接，消除 TOCTOU。
7. **Scene 顺序/背压断言**：immediate 命令 cell 与流式 delta 并发写同一协调器，必须有顺序与背压测试（T9）。
8. **历史 / echo / 状态行显式定义**：并纳入 T19；不依赖 queued 分支的副作用。
9. **分类依据绑定 Ready 路径 handler 行为**：每条分类附 `file:line`，禁止用注释或直觉替代。
10. **文案阶段版本化**：P1/P2 零差异；「已排队」提示在 P3 独立评审后切换（D7）。
11. **先改造后放量**：P1 先完成 `dispatchChatCommand` 两阶段拆分与灰度开关，再接入首批 4 条 immediate 命令，逐条晋升。
12. **执行位置不扩散**：immediate 只存在于 busy capture（L1）；不新增 stdin owner、不改 KeyHandler 位、不碰 ACP 路径。

---

## §6 进入实现（P0）的门禁

- [ ] §4 全量命令（catalog 58 条 ∪ 结构化注册表 ∪ `handleCommand` 分支，附录 C）完成「handler → 副作用矩阵 → BusyPolicy」填写，未填写默认 reject。
- [ ] 首批 4 条命令的只读快照接口设计完成（含字段读写表与 `queuedInputDrain` 原子化）。
- [ ] `dispatchChatCommand` 两阶段拆分的接口草案与「锁内无 send」断言方案评审通过（B2 的根治措施）。
- [ ] 来源分流（INV-8）与 Web 回执语义的测试方案确认（T16）。
- [ ] 灰度开关与 resolver 等价表方案确认（T18）。
- [ ] 现有基线测试确认清单：`chat_input_queue_test.go`、`chat_debug_display_busy_repro_test.go`、`chat_composer_test.go` 等（P1 零改动目标）。

---

## §7 残留决策与未覆盖项

| # | 项 | 说明 |
| --- | --- | --- |
| D1 | `/debug display` 是否 P2 提升 immediate | 需先做 viewer 的 inline 降级；建议维持 deferred |
| D2 | `/model <x>`、`/permission-mode` 忙时 deferred vs reject | 建议首期维持 reject，独立决策 |
| D7 | deferred 文案切换时机 | 建议 P3 |
| U1 | 流式 delta 与命令 cell 的背压量化门槛 | P0-2 给出具体断言指标 |
| U2 | Windows/winpty/SSH 真实终端行为 | 与阶段 F P3 验证合并（T15） |
| U3 | immediate 最坏耗时门槛（多长算「阻塞」） | P0 定义（建议 >50ms 的本地读取维持 deferred） |

---

## 附录：审查证据索引

| 主题 | 锚点 |
| --- | --- |
| Web 注入与回执 | `web_handlers.go:990-1024`、`web_invoke.go:345-363`、`chat_input_queue.go:498-509,482-489` |
| 嵌套 turn 效应 | `command.go:132-155`、`chat_send.go:74-75,121` |
| 渲染锁 | `chat_interaction.go:4612-4648`、`chat_busy_input.go:128,153` |
| 快照 API | `chat_input_arbitration.go:226-237`、`:155-160` |
| 历史入口 | `chat_busy_input.go:131-133`、`chat_input_queue.go:1177-1182` |
| 队列 drain | `chat_input_queue.go:1416-1421`、`chat_team_drain.go:117-138` |
| 模型命令分支 | `chat_model_picker.go:443-461` |
| legacy 门 | `command.go:172-178` |
| ACP 入口 | `agent_stdio_slash.go:94` |

---

---

## 附录 R2：第二轮审查（v1.2，2026-09-27）——副屏交互与全量命令分类

> 审查方式：一路只读子代理专查备用屏/租约机制 + 三路只读子代理按 58 条命令逐条盘点（全部附 file:line）+ 主线程复核与汇总。
> 结论：**S 档（副屏只读交互）可行（中高）**；全量分类定稿见方案附录 E；新增 4 项高危风险（R11~R14）与 8 项待 P0 复核项（附录 E.4）。

### R2.1 副屏机制结论（关键证据链）

- **可行性闭环已存在**：`ScreenLease`（`ui/screen_lease.go:41-236`）→ DEC 1049 进入/退出（`ui/terminal_session.go:536-578`）→ 租约期主屏事务物理 `Deferred`（`:792-800`、`:870-878`）与 `HistoryEffects.Frozen`（`ui/controller_state.go:125-164`）→ 释放后 `ExitAlternateScreen`/`LeaseReleased`/`RequestPrimaryRecovery` 全量重绘（`ui/terminal_session.go:897-907`、`ui/terminal_session_executor.go:1023-1030`）。
- **现状缺口 4 项**：① 租约无等待（撞租约立即 `ErrScreenLeaseBusy`，`screen_lease.go:163-167`）；② presenter 暂停为隐式（无显式 Suspend/Resume API）；③ 租约期流式缓冲是否有界**未证实**；④ 输入所有权移交缺失——busy capture 独占 stdin，而现有 approval/question 只是底栏 popup（`chat_surface_output.go:239-251`），不是备用屏。
- **落地前置**：`AcquireAlternateScreenWait`、显式 presenter 暂停/恢复、`StreamHold/StreamFlush` 有界缓冲、capture→L0 输入移交；S 档必须 fail-closed 降级（§3.7.3）。
- **反例约束（重要）**：`/model` 裸、`/resume` 裸、`/theme select`、`/login` 等「选择即写」命令**不得因为能开屏就升 S**；S 档仅承载只读交互（方案 §3.7.4）。

### R2.2 分类结果摘要（定稿：方案附录 E）

| 档位 | 粗略计数（按变体） | 代表 |
| --- | --- | --- |
| I（内联只读） | ~30 | `/help`、`/status`、`/session`、`/agents` 只读、`/plans` 浏览、`/model status` 等 |
| S（副屏只读） | 首批 5 条白名单 | `/history`、`/usage`、`/debug display`、`/web endpoints`、`/account show|--no-refresh` |
| D（排队） | ~10 | 裸 `/model`、`/provider`、`/resume`、`/export`、`/retry`、`/skill` 默认路径等 |
| R（拒绝） | ~25 | 写状态/写配置/网络/子进程/发 turn：`/clear`、`/permission-mode`、`/login`、`/compact`、`/call`、`/shell`、`/image`、`/yolo` 等 |

与旧白名单的关键差异：`/status`、`/session`、`/help` 由 D 升 I；`/history`、`/usage`、`/debug display`、`/web endpoints` 进入 S；`/model status` 等只读子命令升 I；裸 `/model`、`/provider`、`/resume`、`/export` 等保持 D；写状态/网络/子进程保持 R。

### R2.3 新增风险（详见方案 §7 R11~R14）

租约与流式/recovery 竞争；租约期流式缓冲无界；输入所有权移交竞态（capture 与副屏同抢按键）；S 档被误用于写状态命令。

### R2.4 P0 门禁增补

- 复核附录 E.4 的 8 项不确定点（`/routing` 面板与 `/agents` pick/panel 只读性、approve/deny/answer 是否 I 特例、`/mcp list|status` 与 `/account show` 是否触网、`/skills` 草稿选择等）。
- 副屏改造四项前置的技术评审（`AcquireAlternateScreenWait`、presenter 暂停协议、StreamHold/Flush 上限、L0 移交）。
- 测试 T20~T28 与降级矩阵（§3.7.3）纳入 P0 验收。

---

---

## 附录 R3：第三轮设计升级（v1.3）——业务级分类与统一运行时交互机制

> 动因（用户反馈）：不必过于保守——绝大多数命令可以支持运行时交互，只排除「会误中断/破坏会话」的特例；要求按**业务级**分类，并通过**统一注册/开关机制**收敛为一个通用交互机制。
> 落实位置：方案 §3.8（机制）、附录 F（58 条 + legacy 定稿）；本报告记录设计取舍与待核实项。

### R3.1 相对 v1.2 的变化

| 维度 | v1.2（保守） | v1.3（业务级 + 统一机制） |
| --- | --- | --- |
| 交互档位 | I/S/D/R 四档，S 仅只读 | 模式（inline/screen/prompt/queue/block）× 生效域（read/next-turn/next-call/session/process）两个正交维度 |
| 写命令 | 一律 D/R（忙时不可写） | `next-turn`/`next-call` 允许运行时执行：screen/prompt + 确认 + 生效提示 + 审计 |
| 破坏类 | R（拒绝，输入丢失） | C1 全部 **queue**（回合结束后执行，不丢输入）；仅 `/exit` block |
| 机制 | 散点白名单 + 四档解析 | 唯一注册表 `runtimeCommandSpec` + 统一宿主 `runtimeCommandHost.Submit` + 三级开关（全局/分类/命令） |
| 灰度 | 单开关 | `auto/readonly/off` 全局 + 分类/命令级覆盖，默认 `auto` |

### R3.2 安全性依据（为什么放宽后仍成立）

1. **生效域硬约束**：`session`/`process` 在宿主层不可达（`applyRuntimeEffect` 直接拒绝 + T32 静态断言），所以放宽的只是 `read/next-turn/next-call`，不会在回合中途替换 transcript、换会话或退出进程。
2. **写操作三重护栏**：确认（prompt/screen confirm）+ 生效时机提示（Notice）+ 审计事件；`next-call` 类（权限/沙箱/信任）默认建议先 `off`/next-turn 灰度（D10/V7）。
3. **不阻塞 capture**：screen/prompt 的用户交互阶段不持有 `commandMu`；C11 网络长任务保持 queue（P3 才评估异步+进度副屏）。
4. **不丢输入**：未登记命令、开关关闭、任一安全门失败均降级 queue；唯一 `block` 只有 `/exit`。
5. **既有门禁不变**：来源（本机终端）/统一渲染面/仲裁 L0/租约预算四道门对所有模式生效。

### R3.3 第三轮核实结论（已回填方案 §3.8.4、附录 F.5—F.6）

1. **生效域机制已具备（V5/V7 已核）**：`next-turn` 可复用 `actorRebuildPending`（chat.go:216-223）+ `markPendingChatActorRebuild`/`reconcilePendingChatActorRebuild`（chat_profile_switch.go:311-367）+ `refreshLocalRuntimeAfterSelection`（chat_actor_host.go:1477-1504）；`next-call` 可复用 `withLivePermissionModeSource`（chat_actor_executor.go:569-581）。`/add-dir` 的策略面属 `next-turn`（旧 actor 工具策略留到本轮结束，不漂移）。
2. **prompt 通道可复用（新增契约）**：priority prompt 由读方 goroutine（busy capture）发起、经 `priorityLines` 通道回传，跨 goroutine 安全；单行文本 + 调用方解析；**无内建超时、不可重入**、`JSONOutput/NoInteractive` 需调用前预检（方案 §3.8.4）。
3. **破坏类边界维持「queue 兜底 + 唯一 block=/exit」**：调查列出的高危命令（`/new`、`/load`、`/resume`、`/clear`、`/compact`、`/backtrack --apply`、`/plans reopen`、`/goal set`、`/skill --direct`、`/call`、`/shell`、`/login`、`/mcp auth|reload`、`/account(s) refresh|--save`）若**忙时执行**确实会破坏在途状态或派生并发 turn；本方案一律 queue（回合后执行、不丢输入），故不升 block。仅 `/exit` 为 block（D11 可复盘为「回合后退出」）。
4. **新增闸门完整性项 V11**：忙时拦截依赖 `setCommandGate` 安装（chat_busy_input.go:183）；agent-stdio/ACP/headless 未安装时会绕过（本方案范围限 TUI，P0 复核）。
5. **未决项**：V1（routing 面板动作）、V2a（agents approve/deny/answer）、V4（`/normal` 迁移）、V6（account show 网络）、V8（skills 草稿）、V9（mcp list/status 网络）、V10（`/plan` TUI 忙时安全），均已列入 P0 门禁，不阻塞 P1。

### R3.4 回归门禁增补

- T29~T36（注册表覆盖、默认 queue、三级开关、生效域执行器、审计、prompt 确认/取消、next-turn/next-call 行为）。
- 附录 F.5 的 V1~V9 全部有结论后方可进入 P2-5（写类运行时交互）。
- `chat.runtime_interaction.mode=off` 必须与 v1.0 行为逐条等价（T31）。

---

---

## 附录 R4：`/todos` 运行时查看可行性（v1.3.1，2026-09-27）

**问题**：turn 运行中能否用 `/todos` 查看待办，并在不同视图间切换？

**核查结论**

1. **命令不存在**：catalog 与结构化注册表均无 `/todos`（grep 全包无命中）；TUI 目前只在工具 cell 中以大预览渲染 `todos` 工具结果（`chat_tool_rendering.go:359`、`chat_runtime_events.go:9828-9844`）。
2. **数据面已就绪**：web「任务列表」双通道形状统一 `{items:[{content,status,active_form}],session_id,goal_id}`——实时 `metadata.todo_snapshot`（`web_schema.go:286-290`）、回放 transcript 扫描（`web_todo_snapshot.go:87`）、会话级入口（`web_todo_snapshot.go:189-196`）。无需新协议。
3. **设计定位**：`/todos` = 业务域 **C12**、模式 **screen（可切换视图）/inline 降级**、生效域 **read**、零确认。是统一运行时交互机制（§3.8）的示范命令：只读、可实时刷新、主屏流式不受影响（租约期帧 Deferred）。
4. **"切换查看"两形态**：参数直达（`/todos [all|active|done|brief]`）+ 屏内按键切换（过滤/分组/brief）；重复调用是否循环视图留作 D13。
5. **唯一技术风险（V12）**：回放扫描 `chatWebTodoSnapshotForSession()` 读 transcript，而运行中 turn 并发写 messages。建议实现为 **tool_end 缓存快照（加锁）+ 无缓存回退扫描**，并用 `-race` 用例（T41）锁定；若回退路径确认不安全，则仅读缓存（启动时预热一次）。
6. **降级链**：无副屏/租约超时 → inline 快照；无数据 → 「当前会话暂无待办」；非统一面/开关关闭 → queue（沿用 §3.7.3）。

**文档落实**：方案 §3.7.4（S 白名单加入 `/todos`）、§3.8.5（新增 C12）、附录 F 第 59 行 + F.7（完整设计）、测试 T37~T41、决策 D13、V12；本报告 R4。

**建议落地顺序**：P1 先做 inline 版（`/todos` 参数直达，复用 `chatWebTodoSnapshotForSession`），P2-4 再开 screen 面板与实时刷新——两步都不依赖外部改动。

---

### R4.1 落地记录（v1.3.1，2026-09-27）

`/todos` inline 版已按 R4 设计落地，新增/修改：

| 文件 | 变更 |
| --- | --- |
| `cmd/aicli/commands/chat_todos_command.go`（新增） | `executeStructuredTodosCommand` / `parseChatTodosFilter` / `buildChatTodosDocument` / `chatTodosSnapshotForSession`；过滤视图 all/active/done/brief；空数据与用法错误处理；顶部注释声明 runtime 注册（C12 / screen\|inline / read） |
| `cmd/aicli/commands/chat_todos_command_test.go`（新增） | 5 项单测：catalog 注册、忙时可排队、快照渲染与过滤、空/非法/nil 会话、结构化分发识别 |
| `cmd/aicli/commands/chat_slash_command_catalog.go` | 新增 `/todos [all\|active\|done\|brief]`（Session 组，支持参数补全） |
| `cmd/aicli/commands/chat_command_result.go` | 结构化注册 + legacy 围栏白名单（防止落回旧终端直写） |
| `cmd/aicli/commands/chat_input_queue.go` | `chatSlashCommandQueueSafe` 增加 `todos`（忙时排队过渡；宿主落地后迁入 runtime spec） |
| `cmd/aicli/commands/command.go` | legacy/plain 出口：`handleCommand` 新增 `/todos` 路由（`handleTodosCommand`）；同时是 catalog↔路由一致性测试的权威来源 |
| `cmd/aicli/commands/chat_slash_completion_test.go` | 期望清单补 `/todos` 条目（首轮整包回归 72 vs 73 的一致性失败已修复） |

验证：`gofmt -l` 无输出；`go build ./cmd/aicli/...` 通过；`go test ./cmd/aicli/commands -run ChatTodos -count=1 -v` 5/5 PASS；`-race`（ChatTodos）PASS；catalog↔handleCommand 路由一致性测试（73/73）PASS。整包回归无新增失败：仅 `TestHumanizeActorExecutorError_AppendsRuntimeHTTPPreview`、`TestChatDebugDisplayShowsStorageSection` 两个**基线既有**环境相关失败，已用 `git stash` 基线对照复现确认与本改动无关。T38/T41 与 screen 面板留待 P2-4。

---
