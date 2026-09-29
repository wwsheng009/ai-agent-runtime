# aicli 统一副屏框架 实施记录（批次 0–5 全部落地）

- 日期：2026-09-28
- 对应计划：`docs/plan/aicli-tui-alt-screen-framework-plan-20260927.md`
- 状态：批次 0 ✅ ｜ 批次 1 ✅ ｜ 批次 2 ✅（A 族 9 个 picker 调用点收编，A2/A5 守卫通过）｜ 批次 3 ✅（长文档 + 只读 list 变体收编，A3 直写守卫归零）｜ 批次 4 ✅（忙时白名单扩展）｜ 批次 5 ✅（旧 Open* 字段与 legacy 分支删除、BusyPolicy 单一事实源、T11 清单守卫、/help Esc 标注）
- 说明：工作区同时存在与计划无关的其它在途改动（plan-mode 审批等），本文只记录本计划范围。

## 1. 落地范围

### 1.1 批次 0（框架落地）

新增文件：

| 文件 | 行数 | 内容 |
| --- | --- | --- |
| `backend/cmd/aicli/commands/chat_screen_framework.go` | 886 | `chatScreenSpec{ID,Title,Kind,Doc,Rows,Stages,Esc}`、`openChatScreen` / `chatScreenOpen` / `chatScreenClose` / `chatScreenCloseLease`、`EscPolicy{TopLevelClose(默认),StepBack}`、能力门 + 2s 等待预算内聚（I7）、事件与计数器、降级矩阵、`ErrScreenNested`、panic 安全 close（defer） |
| `backend/cmd/aicli/commands/chat_screen_dispatch.go` | 234 | 副屏效应唯一派发入口 `dispatchChatScreenEffects`（批次 0 曾含兼容映射层 `chatScreenLegacySpecs` 与 `dispatchLegacyChatScreenOpeners` 一键回退，批次 5 已删除；B 族只读副屏 Spec 构建函数保留在本文件） |
| `backend/cmd/aicli/commands/chat_screen_framework_test.go` | 277 | 12 条框架测试（生命周期、Esc、降级、嵌套、legacy 开关、映射穷尽、计数器可见性） |
| `backend/cmd/aicli/commands/chat_screen_batch1_test.go` | 244 | 6 条批次 1 收编测试 |

修改（本计划相关）：

| 文件 | 改动 |
| --- | --- |
| `chat_command_result.go` | 新增 `CommandResult.Screen *chatScreenSpec`（与旧 `Open*` 字段并存，D-E；旧字段注释标注为批次 5 前兼容层） |
| `command.go` | 删除 6 处内联 opener 调用（transcript/debug/web/usage/account/accounts），统一 `dispatchChatScreenEffects`（`renderErr == nil` 时调用） |
| `chat_busy_screen_exec.go` | 忙时能力门 `chatBusyScreenCapability` 改为直接复用框架 `chatScreenCapability`（I7 单一实现，忙时/空闲同一 gate） |
| `ui/action.go` | 新增 `OpenScreenOverlay` / `CloseScreenOverlay` 两个显式生命周期屏障动作（ClassBarrier，lease id 为陈旧动作守卫） |
| `ui/app_state.go` | 新增 `ScreenOverlayState{Active,LeaseID,ScreenID}` |
| `ui/controller_state.go` / `ui/controller.go` | 归约 Open/Close 动作；close-all 路径同步清理；动作名诊断映射 |
| `chat_composer.go`、`chat_busy_input.go`、`chat_escape_interrupt_test.go` | D-C（独立提交）：忙时 Esc 中断保留草稿 —— `chatBusyComposerCapture.onCancel(snapshot)` 暂存快照，`PreserveDraft()` 先恢复草稿再清屏；审批/提问行语义不变 |

### 1.2 批次 1（只读页收编）

| 命令 | 收编方式 | 行为差异 |
| --- | --- | --- |
| `/todos` | 原生 `CommandResult.Screen`（`ScreenDocument`，Doc 复用 `buildChatTodosDocument`） | unified 交互下由主屏内联 → 副屏；plain/非 TTY/JSON 输出不变（legacy 回退已随批次 5 删除） |
| `/history`、`Ctrl+T` | 框架 Spec `transcript.screen`（`RunDocument` 复用既有 transcript 分页器） | 键位与输出不变；租约/close 屏障由框架统一；首帧先发布 transcript 快照 + `OpenTranscriptOverlay` 再进分页器（不空屏）（legacy 回退已随批次 5 删除） |
| `/debug display`、`/web endpoints`、`/usage`、`/account`、`/accounts` | 兼容映射层 `chatScreenLegacySpecs` → `openChatScreen`（D-E：源命令代码不改） | 承载通道不变；快照捕获时机与旧实现一致；`/debug display` 能力不足时按旧行为静默降级（`SilentDegrade`，只记 degrade 事件与计数器）（legacy 回退已随批次 5 删除） |

- 说明：上表提到的 legacy 回退分支已随批次 5 删除；`AICLI_CHAT_SCREEN_FRAMEWORK` 的退休值（含 `legacy`）现在只记 `unknown_env` 警告并按 unified 运行（§1.6、§4）。

### 1.3 批次 2（交互页收编 / A 族）✅

新增 `chat_picker_screen.go`（统一 picker host，`runChatPickerScreen`）：

- unified（默认）：`openChatScreen`（Kind=list、`SilentDegrade`）持有租约，`RunScreen` 闭包在租约内运行各 picker 既有富交互（即时搜索、实时预览、多阶段 provider→model→reasoning、窗口化分页），close 序列（post Close → release → actor idle）、计数与降级全部由框架统一完成；选择结果仍在租约释放后由调用方应用（I3）。
- legacy（§6.1）：回退 `chatPickerOpen` / `chatPickerClose`（批次 0 前的内联实现）——该分支已在批次 5 删除；退休值只记 `unknown_env` 并按 unified 运行。

已收编 9 个调用点：

| 命令 | 调用点 | 说明 |
| --- | --- | --- |
| `/resume` | `chat_resume_command.go:212` | 分页由 picker window 自持；恢复动作在租约释放后 |
| `/backtrack` | `chat_backtrack_select.go:208` | 同上（截断动作在租约释放后） |
| `/model`、`/provider` | `chat_model_picker.go:72` | provider→model→reasoning 同一租约内推进；删除确认/重开不换租约 |
| `/theme` | `chat_theme_picker.go:59` | 实时预览选择仍在租约内；取消恢复原主题 |
| `/skills` | `chat_skill_picker.go:143` | 多选确认在租约释放后应用 |
| `/export` | `chat_export_picker.go:91` | 会话/格式两级选择同一租约 |
| `/mcp` | `chat_mcp_picker.go:271` | server 选择与来源复用 `/mcp list` 投影 |
| `/routing panel` | `chat_routing_panel.go:64` | 会话级路由覆盖在租约释放后应用 |
| `/login` | `chat_login_picker.go:83` | 与 `/model` 共用 picker 就绪门 |

A5（去重）：7 处内联租约实现（theme/skill/export/mcp picker、resume、theme 命令变体、backtrack）改走统一 host；批次 5 删除 legacy 后 `chatPickerOpen(` / `chatPickerClose(` 全包归零，由守卫 `TestChatScreenGuardA2PickerLeaseOnlyThroughFramework` 锁定。

A2（唯一取租约点）：取备用屏租约只允许发生在 `chat_screen_framework.go` 的 `chatScreenAcquireLease`；批次 5 后全包仅剩一处 `AcquireAlternateScreenWait` 调用，守卫 `TestChatScreenGuardA2AlternateScreenSitesAreWhitelisted` 要求命中集恰为 `{chat_screen_framework.go}`。

例外与偏差（显式说明）：

- `/profile pick` 走 priority-line selection popup（`useRuntimeSelectionPopup`，`chat_model_switch.go:783`），从不取备用屏租约，因此不在本框架的租约生命周期内，保持原交互（未迁 ScreenList）。注册表当前仍声明 `runtimeModeScreen`（`chat_runtime_command_registry.go:292`），与实现不符，列入批次 5 单一事实源收敛项。
- `/agents panel` 的 live modal 不复活：unified 下 `full`/`follow` 提交有限快照 `ScreenDocument`（批次 3 `agents.panel`）；`summary`/导航类子命令保持主屏内联。
- `/theme list`、`/skills list`、`/mcp list`、`/profile list` 的只读列表出口属批次 3 长文档范畴，本批不动（收尾中）。

### 1.4 批次 3（长文档迁入）✅

新增 `chat_screen_batch3.go`：`chatScreenDocumentSpec` / `chatScreenDocResult` / `chatScreenTextDoc` 与各命令 Spec。

| 命令 | 收编方式 | 行为差异 |
| --- | --- | --- |
| `/help`、`/status`、`/sessions`、`/functions`、`/hotkeys` | `ScreenDocument`（正文复用既有文档/文本构建函数） | unified 交互出口由主屏内联改为副屏；plain/JSON/legacy 不变 |
| `/plans detail`、`/timeline`、`/collab` | `ScreenDocument` | 同上（列表/对比等短变体保持内联） |
| `/agents panel`（full/follow）、`/agent transcript` | `ScreenDocument`（提交有限快照） | live modal 不复活；导航类子命令保持内联 |
| `/model status`、`/provider status`、`/theme status\|list\|preview`、`/skills list`、`/mcp list\|status`、`/profile status\|list\|show\|diff` | `ScreenDocument` 只读变体 | 消除「只读内联 / 交互副屏」分裂；写入类回执仍内联 |

- A3 守卫：`TestChatScreenBatch3UnifiedPathHasNoDirectCommandWrites` 经 `chatCommandOutputObserver` 断言 14 条统一出口命令零 legacy stdout 直写。
- 只读变体矩阵：`TestChatScreenBatch3ReadOnlyVariantsUseScreenDocument` 覆盖 14 条（含 `/profile show|diff`），断言 `Kind==document`、正文非空、且与 plain 出口逐行一致（同一构建函数的两条投影）。

### 1.5 批次 4（忙时闸门扩展）✅

- `chatBusyScreenDocumentCommands` 由首批 5 条扩展为 15 条只读副屏命令（`/todos`、`/history`、`/usage`、`/debug`、`/web`、`/account`、`/accounts`、`/help`、`/status`、`/sessions`、`/functions`、`/plans`、`/timeline`、`/collab`、`/hotkeys`）。
- 判定函数 `busyScreenCommandReadOnlyDocument`：注册表 `Mode==screen && Effect==read` + 白名单命中（fail-closed）；picker/写入类 screen 命令继续 Deferred 入队。
- 忙时/空闲共用 `chatScreenCapability`（I7 单实现）；等待预算 `chatBusyScreenWaitBudget` 不变。
- 审计测试：`TestRuntimeCommandRegistryBusyAdmissionAudit` 遍历白名单，逐条要求注册表存在 `screen+read` 声明；`TestChatBusyPolicyScreenFirstBatch` 锁定策略映射与总闸回退。

### 1.6 批次 5（清理与守卫）✅

- 单一事实源：`catalog.BusyPolicy` 已删除，忙时策略统一由 `runtimeCommandRegistry` 的
  `Mode`/`Effect` 派生（`chatSlashCommandBusyPolicyFor` → `chatBusyPolicyFromRuntimeSpec`）；
  catalog 只保留别名归并与帮助文案（`chat_slash_command_catalog.go`），
  总闸 `AICLI_CHAT_BUSY_COMMAND` 显式关闭时仍退回 P1 白名单派生（T18 等价）。
- T11 输出类别清单守卫：注册表全部 screen 声明补 `rtOutput(...)`（`chatOutputScreenDocument`
  23 处 / `chatOutputScreenInteractive` 14 处，含 bare/variant/wildcard）；新增
  `chat_command_output_category.go`（类别判定 `chatCommandOutputCategoryFor` + 一致性检查）
  与 `chat_command_output_category_test.go`：
  1. 目录全集（含别名归并）必须命中且仅命中一个类别；
  2. screen 档必须显式声明 `Output`（未声明即失败）；
  3. 类别与 `Mode`/`Confirm`/`Effect` 语义一致（screen-document ⇒ read 且无确认门）。
- `/help` 文案：副屏命令统一标注「（副屏，Esc 返回）」；
  `TestChatSlashHelpMarksScreenCommandsWithEscHint` 锁定「标注数 == 主入口为 screen 的可见命令数」，
  且断言 `/todos`、`/history`、`/help` 有标注、`/debug`、`/theme`、`/profile`、`/agent` 无标注。
- 旧 `Open*` 字段与 legacy 分支删除（5A）：已落地（工作区核验，2026-09-28 12:2x）——
  `CommandResult` 不再含任何 `Open*` 字段（grep 归零）；`chat_screen_dispatch.go`
  只保留统一派发（`dispatchChatScreenEffects` / `chatScreenEffectSpec`）与 B 族
  Spec 构建函数，`chatScreenLegacySpecs` 映射层与 legacy 分支已删除；
  `AICLI_CHAT_SCREEN_FRAMEWORK` 仅接受空/`unified`，其他值记 `unknown_env`。
  A2 复核：全 `commands` 包仅剩一处取租约调用点
  （`chat_screen_framework.go:382` `AcquireAlternateScreenWait`），其余命中均为注释。
- T11 守卫首跑（2026-09-28 12:22）发现 1 条违规：裸 `/account` 未在运行时注册表
  登记（该条目只有 Variants，缺 Bare；裸形式语义 = refresh 网络长任务，P1 队列
  契约不变）。修复：补 `Bare`（queue + `rtNotice("已排队，回合结束后执行")`，与
  注册表兜底 `runtimeQueueFallbackSpec` 逐字一致，忙时行为不变）；
  `go test -run 'TestChatCommandOutputCategory|TestChatSlashHelp|TestChatScreenGuard|TestChatPickerScreen'`
  复跑通过（2026-09-28 12:4x，ok 1.240s）。
- 验证口径：`go build ./cmd/aicli/...` + `go test ./cmd/aicli/... -count=1` + `go test -race ./cmd/aicli/commands/ -count=1`；除三条预存在失败外全绿（证据见 §5）。

## 2. 不变量与验收对照

| 项 | 落地位置 | 测试 |
| --- | --- | --- |
| I1 单所有者 | `chatScreenOpen` 全程唯一租约；`ScreenLease`/lease id 守卫 | `TestChatScreenDocumentLifecycleReleasesLeaseOnEsc` |
| I2 close 屏障顺序 | `chatScreenCloseLease`：post Close → release → `waitUIActorIdleBounded` | `TestChatScreenDocumentLifecycleReleasesLeaseOnEsc`、`TestChatScreenBatch1TodosLifecycleEscReleasesLease` |
| I3 结果应用在 release 后 | picker/list 结果由框架在释放后交给调用方 | `TestChatScreenListConfirmReturnsIndexAfterLeaseRelease` |
| I4 Esc 默认一次回主屏 | `EscPolicy` 默认 `TopLevelClose`；`StepBack` 拒绝 | `TestChatScreenListCancelIsEsc`、`TestChatScreenEscPolicyStepBackRejected` |
| I6 降级不落 legacy、不静默 | 降级为主屏内联文档单元格 + degrade 事件/计数 | `TestChatScreenDegradesInlineWhenCapabilityMissing`、`TestChatScreenAccountSpecDegradeDocumentMatchesPlain`、`TestChatScreenWebEndpointsSpecDegradesInline` |
| I7 能力门/预算单一实现 | 框架 `chatScreenCapability`；忙时 gate 已改为复用 | `TestChatScreenDegradesInlineWhenCapabilityMissing` |
| I9 禁止嵌套 | `openChatScreen` 拒绝重入（`ErrScreenNested`，不排队） | `TestChatScreenNestedOpenIsFailClosed` |
| 退休开关值（批次 5） | `AICLI_CHAT_SCREEN_FRAMEWORK` 非空/unified 值只记 `unknown_env` 并按 unified 运行（legacy 分支已删） | `TestChatScreenRetiredEnvValueRunsUnified`、`TestChatScreenBatch1RetiredEnvValueBehavesAsUnified`、`TestChatPickerScreenRetiredEnvValueRunsUnified` |
| 未知开关值 | 未知值按 unified 运行并计数 | `TestChatScreenUnknownEnvCountsAndRunsUnified` |
| 可观测性 | 事件 `aicli.chat.screen.{open,close,degrade,error,violation}` + 计数器 | `TestChatScreenCountersVisibleInDebugDisplay` |
| A1 统一入口 | unified 路径唯一取租约点 `chatScreenAcquireLease`；A 族经 `runChatPickerScreen` | `TestChatScreenGuardA2AlternateScreenSitesAreWhitelisted`、`TestChatScreenGuardA2PickerLeaseOnlyThroughFramework` |
| A5 去重 | 7 处内联租约实现改走统一 host `runChatPickerScreen`（内部即框架租约）；9 个 picker 调用点经统一 host；批次 5 删除 legacy 后全包 `chatPickerOpen/Close` 归零 | `TestChatScreenGuardA2PickerLeaseOnlyThroughFramework`、`TestChatPickerScreenUnifiedRunsInsideFrameworkLease` |
| A3 统一出口无 legacy 直写 | `chatCommandOutputObserver` 观测点 + 结构化出口 | `TestChatScreenBatch3UnifiedPathHasNoDirectCommandWrites` |
| T10 忙时白名单扩展 | `chatBusyScreenDocumentCommands`（15 条）+ 注册表 `screen+read` 审计 | `TestRuntimeCommandRegistryBusyAdmissionAudit`、`TestChatBusyPolicyScreenFirstBatch` |
| T11 输出类别清单守卫 | 注册表 `Output` 声明 + `chatCommandOutputCategoryFor` 判定（单一事实源） | `TestChatCommandOutputCategoryCatalogGuard` |
| 批次 5 帮助文案 | `chatSlashHelpPrimaryScreen` + `chatSlashHelpScreenMarker` | `TestChatSlashHelpMarksScreenCommandsWithEscHint` |

## 3. 行为差异清单（A4）

1. `/todos`：unified 交互出口由主屏内联单元格改为副屏只读页；plain/非 TTY/JSON 仍输出同一文档（`TestChatScreenBatch1TodosPlainKeepsInlineDocument`）。批次 5 起退休开关值（`legacy` 等）不再回退内联，只记 `unknown_env`（`TestChatScreenBatch1RetiredEnvValueBehavesAsUnified`）。
2. `/history` 与 `Ctrl+T`：通道由"命令内自行取租约"改为"框架租约 + 统一 close 序列"；分页器渲染与键位不变；退出后 primary repaint 由框架完成。
3. B 族 5 条只读命令：输出/键位/降级行为逐条保持不变（复用旧渲染函数与快照时机），仅生命周期与派发归一。
4. 忙时执行闸门：能力判定从"各自维护一份"改为复用框架 gate；判定条件等价（统一渲染面 + surface enabled/owned + 无租约/弹层 + 终端支持全屏），无行为差异。
5. D-C：忙时 Esc 中断后草稿保留（bug 修复，独立提交可单独 revert）；审批/提问行不变。
6. A 族 picker（批次 2）：通道由「各自取租约 + 各自 close」改为「框架租约 + 统一 close 序列」；列表内容、键位、实时预览与多阶段顺序不变。三阶段失败映射回各 picker 既有文案与哨兵（open/run/close → `chatPickerScreenErrorText`、`chatPickerMapFrameworkErr`），调用方 `errors.Is` 判定不变。
7. 能力不足 / 租约忙 / 嵌套时 picker 静默降级（不进副屏、不打印内联文档），与迁移前 opener 门禁失败行为一致（`SilentDegrade`）。
8. 退休开关值（`legacy` 等）下 picker 与文档页一律走统一框架并正常计数，只记 `unknown_env` 警告；批次 5 起不存在"不触碰框架计数器"的旁路（`TestChatPickerScreenRetiredEnvValueRunsUnified`、`TestChatScreenRetiredEnvValueRunsUnified`）。
9. `/profile pick`（无租约，priority-line popup）与 `/agents panel`（批次 3 快照）不迁本框架，理由见 §1.3。
10. 未解释差异：0 条。

## 4. 开关与回退（§6.1 / §6.2）

- `AICLI_CHAT_SCREEN_FRAMEWORK`：`unified`（默认）；读取集中在框架入口一处。批次 0–4 期间 `legacy` 为全通道回退值，批次 5 删除分支后成为退休值：非空且非 `unified` 的取值只记 `unknown_env` 警告并按 `unified` 运行（`chatScreenFrameworkEnvLookup` 保留，便于观测配置漂移）。
- 批次回退手段：批次 0–4 可经 `legacy` 开关一键回退；批次 5 的删除提交按计划不可经开关回退（开关删除即终态），回退以 revert 本批提交为手段；退休值等价性由 §2 表格退休开关行的三条测试锁定（行为 == unified）。

## 5. 验证证据

> 批次 5 收尾后统一执行（2026-09-28）；下方为最新终验结果。

- `go build ./cmd/aicli/...`：通过（exit=0）。
- `go vet ./cmd/aicli/...`：通过（exit=0）。
- `go test ./cmd/aicli/... -count=1`（全量终验，日志 `/tmp/aicli_full_final2.log`）：19 个包 `ok`；仅剩 3 条既有失败（下述），`full_exit=1` 仅由既有失败引起，无本计划引入的新失败。
- 既有失败（与本次改动无关，干净 HEAD worktree `/tmp/aar-head-baseline` 复现）：
  - `TestRunChatLoopInteractiveInitialPromptSubmitsOnceAndStaysInteractive`（`chat_initial_prompt_test.go:32`：submitted prompts 少一条 follow up）；
  - `TestRunChatLoop_DrainsQueuedLinesAfterTeamSettlesBeforePrompt`（`chat_interaction_test.go:1621`：queued hello 未在退出前送出）；
  - `TestReadInteractiveLineForcedReadWhenPeekAlwaysEmpty`（`cmd/aicli/ui`，`inputbox_editor_test.go:1898`，5s 超时判定 editor starved）。
- 批次 5 定向：`go test ./cmd/aicli/commands/ -run 'TestChatCommandOutputCategory|TestChatBusyPolicy|TestRuntimeCommandHost|TestRuntimeCommandRegistry|TestRuntimeSwitch|TestChatScreen|TestChatPickerScreen|TestChatSlashHelp|TestRuntimeCommandSpec' -count=1`：ok（1.819s；T11 输出类别矩阵、忙时策略、运行时注册表/切换、A2/A3/A5 守卫、picker host、`/help` 文案全绿）。
- `go test -race ./cmd/aicli/ui/ -count=1`（日志 `/tmp/aicli_race_ui_b5.log`）：除 ui 既有失败外全绿（`race_ui_exit=1` 仅由既有失败引起）。
- `go test -race ./cmd/aicli/commands/ -count=1`（日志 `/tmp/aicli_race_cmd_final2.log`）：包在 600.8s 撞 10m 默认超时中止（累计耗时环境问题，非死锁：中止时仅 `TestHandleChatWebAPIAnalysis_RoutingUIScopeToggle` 在跑，耗时 1s）；中止前 7 条失败，逐条归因：
  - 6 条在干净 HEAD 定向 `-race` 复现（同命令同失败，日志 `/tmp/race_targeted_head.log`）：`TestDetachedMeshNodeStdinEOFParkKeepsQueueServing`、`TestDiagnosticNoticeTimerExpiryClearsTheRow`、`TestAICLIChatActorExecutor_DocsPromptRegression_CoversWorkspaceToolPriorityBlockedReplanAndStreamFallback`、`TestPrintResumeSuccessUnifiedOmitsSessionMetaBlock` + 上述 2 条既有失败；
  - `TestACPSessionMCPStdioEndToEnd`：DATA RACE 位于未触碰的 `internal/mcp/client`（`client.go:447/:257`；本计划 diff 对该目录为 0 行），定向 `-race -count=3` 两树均通过（0 竞态），判定为间歇性既有竞态。
  - 结论：7 条均非本计划引入；全量 `-race` 受 10m 默认超时限制未跑完，不作为放行依据（DoD 不要求 -race）。
- `gofmt -l cmd/aicli/commands/ cmd/aicli/ui/`：无输出（0 文件）。

## 6. 遗留与后续批次

- 批次 0–5：全部完成。批次 5 交付物：`CommandResult` 不再有 `Open*` 字段；legacy 分支物理删除（`AICLI_CHAT_SCREEN_FRAMEWORK=legacy` 退休为警告值，只记 `unknown_env` 并按 `unified` 运行）；`catalog.BusyPolicy` 与 `runtimeRegistry.Mode` 合并为单一事实源；T11 输出类别清单守卫（每条命令必须声明输出类别）；`/help` 标注「Esc 返回」。
- 计划内保留例外：`/profile pick`（无租约 priority-line popup）与 `/agents panel`（仅快照）不迁本框架，理由见 §1.3。
- 既有失败（非本计划引入）：`TestReadInteractiveLineForcedReadWhenPeekAlwaysEmpty`（`cmd/aicli/ui`）；`cmd/aicli/commands` 另余 1 条在研渲染用例 `TestPrintVisibleChatHistory_UnifiedPrimaryViewportRetainsHistoryTailAlongsideActiveReasoning`（溢出交接事务把 active cell 前缀插入 primary 滚动区 `1;OutputBottomRowr` 后，后继帧未重绘尾部，`terminal_session.go:1192`；断言见 672ccdc2）。原 3 条中 2 条循环用例（`TestRunChatLoopInteractiveInitialPromptSubmitsOnceAndStaysInteractive`、`TestRunChatLoop_DrainsQueuedLinesAfterTeamSettlesBeforePrompt`）已由后续基线清理批次经 host 生命周期测试缝（`chatPipeLineEditorPreferredFn`）转绿：`cmd/aicli/commands` 包全量 12 红 → 1 红。放行口径为「不新增失败」。
- 计划 §11 待复核项收口：
  - 第 1 条：已关闭（搜索态 Esc 例外固定进 T5b）。
  - 第 2 条：键位映射未改动，仍由既有 `ui.RunTranscriptPagerWithLease` 原语承载（`chat_transcript_pager.go:60`），批次 1 只替换打开/关闭与租约编排；未新增键位回归。
  - 第 3/4 条：随批次 5 删除 `Open*` 字段与旧调度函数自动关闭。
  - 第 5 条：已关闭——框架文档页编排原语定为 `ui.RunDebugOverlayWithLease`（`chatScreenDocumentRunner`，`chat_screen_framework.go:707-709`）；transcript 页经 `chatScreenSpec.RunDocument` 注入口（`chat_transcript_pager.go:37` → `runChatTranscriptScreen` → `ui.RunTranscriptPagerWithLease`）。
  - 第 6 条：tmux/winpty 真机未实测；能力门 fail-closed 降级路径有测试覆盖（`TestChatScreenDegradesInlineWhenCapabilityMissing`），列为已知未实测项。
  - 第 7 条：已关闭——降级投影一致性由 `TestChatScreenAccountSpecDegradeDocumentMatchesPlain`、`TestChatScreenWebEndpointsSpecDegradesInline`（`chat_screen_framework_test.go:235/:250`）覆盖主路径。
