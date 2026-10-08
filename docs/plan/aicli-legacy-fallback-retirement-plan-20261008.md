# aicli legacy fallback 链退役：分析报告与实施方案（2026-10-08）

> 性质：只读引用侦察（三路并行）+ 生产可达性判定 + 分批退役方案。
> 基线：`feat/render-p0-writer-unification` @ `d0d3da80`（P0 写端归一完成；ui 直写债务基线 41 项）。
> 上游：`docs/plan/aicli-render-p0-writer-unification-ledger.md`、`docs/architecture/aicli-tui-renderer-architecture-design.md`、
> `docs/architecture/aicli-chat-unified-renderer-architecture.md`、`docs/aicli/windows7.md`。
> 方法：本地锚点（装配时序/使能点/门禁基线）+ 三路只读侦察（ui surface 链 / 输入编辑器链 / commands 入口模式），
> 全部结论带 file:line 证据；不确定处显式标注。

---

## 1. 结论摘要（TL;DR）

1. **不支持「legacy fallback 链整体退役」**：当前生产装配存在**产品承诺的降级模式**——
   `--compat-mode`（Win7/无 ANSI 终端，`docs/aicli/windows7.md` 明文承诺）与
   surface 能力探测失败时的 plain 交互降级（`chat_setup.go:89-122`）。这两条路径
   必然使用 console line editor + stdout 直写打印链，删除即破坏对外承诺。
2. **支持「死码退役 + 降级路径正规化」两段式**：
   - **死码退役**：统一会话中物理不可达的 legacy 实现（`FixedBottomSurface` 物理绘制族、
     no-lease raw 分支、DEC2026 follower、默认 stdout sink、ui hookless 编辑器等）——
     逐链证明「三条生产路径均不可达」后分批删除。
   - **正规化**：把 compat/plain 降级链从「legacy 债务」语义中剥离，改称 console mode，
     纳入正式验收矩阵；门禁基线将其登记为**受认可 writer**而非迁移债务。
3. 判定分四类（详见 §4）：
   - **A 保留（产品承诺）**：console 输入/输出链、启动期清理、stderr 诊断。
   - **B 可删（生产死码）**：仅测试/防御可达的 legacy 渲染与直写实现。
   - **C 改造后删（半死）**：被 facade/状态机复用、但物理侧可剥离的组件
     （`FixedBottomSurface` 拆分为「状态 facade」与「物理绘制」两半）。
   - **D 观察（跨平台/低频）**：Unix keyhandler、fullscreen/pager 在无 lease 环境的降级。
4. 关键前提（已在代码锚定，无需新决策）：
   - 统一交互路径 **无直写回退**：gateway 工厂失败即 fail-closed 终止
     （`chat_setup.go:231-244`）；`enableUnifiedRendererWithWriter` 注释明确 test-only
     （`chat_ui_actor.go:261-264`）。
   - `FixedBottomSurface` 物理写默认开启、但生产交互在 `Enable()` **之前**即
     `SetPhysicalWritesEnabled(false)`（`chat_setup.go:84`），`Enable()` 内物理绘制受
     `physicalWritesEnabledLocked()` 门控（`fixed_bottom_surface.go:429-445`）——
     即「生产交互期 surface 从不落字节」是结构性事实，不是时序巧合。
   - `FencePhysicalWrites` 为**单向栅栏**（`fixed_bottom_surface.go:279-291`），
      presenter attach 后永久关闭，不存在复活窗口。

---

## 2. 生产装配与输出权威矩阵（锚点事实）

### 2.1 会话初始化决策树（`backend/cmd/aicli/commands/chat_setup.go`）

```
buildChatSession
├─ compatMode (--compat-mode)            → 跳过 TUI/keyHandler；surface/layout/inputBox = nil
│                                          → setChatConsoleInputMode(opts.InputMode)   [:64-66,124-135]
├─ interactiveUI = 交互 TTY && !compatMode
│   ├─ layout/inputBox/keyHandler/surface 全建
│   ├─ surface.SetPhysicalWritesEnabled(false)      ← 先栅栏         [:84]
│   ├─ surface.Enable()
│   │   ├─ true  → 继续统一路径
│   │   └─ false → 全部拆掉（surface=nil, keyHandler.Stop, layout.Disable）
│   │              → 打印 "using plain interactive mode"（stderr）
│   │              → setChatConsoleInputMode(opts.InputMode)          [:89-122]
│   └─ unified gateway：EnableUnifiedRendererGateway
│       ├─ 成功 → presenter attach（唯一物理写端），MCP 状态改语义 sink  [:231,249]
│       └─ 失败 → fail-closed：禁用全部组件 + 返回错误（无直写回退）   [:231-244]
└─ NoInteractive / JSON                  → 非交互输出路径              [:136-140]
```

`surface.Enable()` 的能力判据（`fixed_bottom_surface.go:3687-3699`）：
`Interactive && ANSI && ScrollRegion`，且 **zellij 复用器被显式排除**，且高度 > 底部保留行数。
即：**能力不足 → plain 降级**，这是「legacy 输入/打印链」仍然存活的根因。

### 2.2 三条输出权威路径

| 路径 | 触发 | 物理写端 | 输入 | 输出打印 |
|---|---|---|---|---|
| **unified**（主路径） | 交互 TTY + ANSI/ScrollRegion + 非 zellij + gateway 工厂成功 | `TerminalSessionPresenter`（经 session-scoped gateway；surface 永久栅栏） | `InputBox` hooks 版（`chat_composer.go` 各 `...WithHooksContext`） | 语义 Scene/AppState 提交（`submitUnifiedDirectInteractiveOutput`） |
| **console/plain**（降级，产品承诺） | `--compat-mode`；或 surface 探测失败（Win7 conhost、无 ANSI、zellij）；或交互 TTY 之外的交互 stdin | 无 surface（`nil`）→ 进程 stdout 直写 | `chat_legacy_console_line.go` / `chat_legacy_console_editor_windows.go` / `chat_system_console_editor_windows.go` / `chat_pipe_console_line.go` | `printDirectInteractiveOutput` 的 `fmt.Print` 兜底（`chat_surface_output.go:572-582`）及各 legacy printer |
| **非交互/JSON** | `--no-interactive` / `--output json` | stdout/JSON 编码器 | 管道 | 结构化输出（不走交互 printer 族） |

### 2.3 语义判定函数（输出归属的关键口径）

- `unifiedInteractiveOutputMustFailClosed(session)` = `unifiedDirectInteractiveOutput(session) && session.Interaction == nil`
  （`chat_surface_output.go:437-443`）：**不是**「是否统一渲染」，而是「统一权威存在但协调器已消失」的
  teardown/race 状态；生产代码里大量 `} else if !unifiedInteractiveOutputMustFailClosed(session) {`
  分支 = 「非统一路径继续用 legacy 打印」。
- `writeDirectInteractiveOutput`（`chat_surface_output.go:470-482`）：统一路径 → 语义提交；
  非统一且 surface 可用 → surface 写；否则返回 false，由调用方走 stdout。
- `printDirectInteractiveOutput`（`chat_surface_output.go:572-582`）：统一 → 语义提交；
  否则 surface 或 `fmt.Print`。**这是 console 模式的输出主通道，属于保留面。**

### 2.4 启动期/诊断类（保留面，非 fallback）

| 项 | 位置 | 触发 |
|---|---|---|
| `EnsureConsoleUTF8Output` | `main.go:57` → `terminal_driver.go:47-53` | 进程启动期一次 |
| `ui.NewTerminal().ClearIfSupported()` | `chat_setup.go:347` | 启动清屏（非 TUI 模式） |
| `LiveOSCProbe` | `terminal_driver.go:134`、`render_bridge.go:24` | 主题探测，进程一次 |
| `ui.PrintErrorTo(os.Stderr, ...)` | `chat.go:1080` | provider 缺失等启动错误 |
| `NotifyChatDiagnostic` / `aicliDiag*` | 多处 | 诊断通道（非交互输出） |

---

## 3. 引用与可达性矩阵（三路侦察结果）

> 待三路只读侦察（lf-ui-surface / lf-input-editor / lf-cmd-modes）返回后填入。
> 输出格式：`符号 → 定义 → 生产调用点 → 触发条件 → 统一会话可达 → 其他模式 → 测试依赖 → 判定`。

### 3.1 矩阵一：ui surface/renderer 链（侦察：lf-ui-surface）

| 符号/链 | 定义 | 生产调用点 | 统一会话可达 | 其它模式 | 判定 |
|---|---|---|---|---|---|
| `NewFixedBottomSurface` | `fixed_bottom_surface.go:232` | `chat_setup.go:80`（全仓唯一生产构造） | 可达（兼容支撑壳） | Enable 失败即丢弃（:104-118） | 保留 |
| `SetPhysicalWritesEnabled(false)` | `:265` | `chat_setup.go:84`；`chat_interaction.go:634`；`chat_ui_actor.go:229,290` | 可达（置 false） | — | 保留（fence 契约） |
| `FencePhysicalWrites` | `:282` | `chat_interaction.go:632,732` | 可达（presenter 上线后单向锁死） | — | 保留 |
| `Enable` 首帧块 | `:429-445` | `chat_setup.go:85` | **不可达**（先 fence 再 Enable） | 仅测试 | 改造后删（paint 块） |
| `writeOutput` 物理分支 | `:974-996` | `chat_interaction.go:1932`（被 `:1925 unifiedRenderer` 提前 return） | **不可达** | 仅测试/legacy | 改造后删 |
| `appendOwnedDirectPaintLocked` | `:1139` | 唯一调用 `writeOutput:1031`（已被 fence） | **不可达** | 仅测试 | 可删 |
| `insertHistoryLinesInRegionLocked` | `:5206-5213` | `:1178`、`:5196`（fence :5210） | **不可达** | 仅测试 | 可删 |
| `renderOwnedViewportLocked` | `snapshot.go:72-80` | `:694,:1033,:1408,:2177,:3833,:3853,:4153` 等（fence :76） | **不可达** | 仅测试 | 可删 |
| `flushHoldingLock`/`flushHandoffHoldingLock` | `:372/:392`（fence :376/:396） | `:1228`、`snapshot.go:89`、`:5225` | **不可达** | 仅测试 | 可删 |
| `clearActiveBand` paint 分支 | `:2253-2256` | facade/内部（unified 走 state-only） | 可达（仅 state） | legacy 可达 | 改造后删（paint 分支） |
| `withTerminalWriteLock`（DEC2026） | `renderengine/terminal_lock.go:71/:22-24` | `renderengine/presenter.go:40`（unified 合法）；true 开关仅 `fixed_bottom_surface.go:437`（fence 内） | 锁=可达；DEC2026 true=**不可达** | legacy 测试 | 锁保留；true 分支改造后删 |
| `processTerminalOutput`/`TerminalOutput()` | `terminal_output.go:18/:49` | 无生产注入；启动 `chat_setup.go:347` 经默认 stdout | 交互期不可达；启动期可达 | legacy/降级可达 | 保留（改造后可去默认 stdout） |
| `Terminal.PrintAt` | `terminal.go:345` | **无生产调用者** | 不可达 | 不可达 | **可删** |
| `Status.PrintTo`/`PrintXxxTo` | `status.go:165/:205-221` | **无生产调用者** | 不可达 | 不可达 | **可删** |
| `Status.Print`/`PrintXxx` | `status.go:121/:180-201` | `chat.go:1818,1946,1964,2039` 等（非统一分支） | 条件可达 | 降级/命令路径可达 | 保留 |
| `screen_lease` raw 直写分支 | `screen_lease.go:286-330,:503-527` | 仅 fence-on/测试 | **不可达** | 仅 legacy/测试 | **可删** |
| `screen_lease` transport 分支 | `:263-284,:364-370,:463-490` | `chat_screen_framework.go:387` | 可达（presenter=transport） | — | 保留 |
| fullscreen/pager/debug 的 raw/no-lease 分支 | `fullscreen_list.go:423-455`、`transcript_pager.go:582-598`、`debug_overlay.go:114-121` | 启动期 picker `chat.go:1021,1214`、`login.go:278`；会话内带 lease | 带 lease 不可达 | 启动期/非租约可达 | 保留（依赖启动期租约改造） |
| `surface.Apply` | `:1765` | `chat_ui_actor.go:1139`（仅 `!UnifiedRendererEnabled()`） | **不可达** | 降级可达 | 改造后删 |

### 3.2 矩阵二：输入/编辑器/平台链（侦察：lf-input-editor）

| 符号/链 | 定义 | 生产调用点 | 统一会话可达 | 判定 |
|---|---|---|---|---|
| `readPrompt`（无 hooks） | `inputbox_editor.go:170` | **无生产调用者**（仅 ui 内 3 个无 hooks 包装） | 否 | **可删** |
| 无 hooks 包装 `ReadWithHistoryPrompt`/`ReadTransientPrompt`/`ReadTransientLine` | `inputbox_editor.go:130/138/166` | 无生产；`ReadWithHistory` 自身亦无调用者 | 否 | **可删** |
| `writeEditorControlSequence` `hooks==nil` 分支 | `:289-297`（nil 传参仅 :208/:214） | 随 `readPrompt` 死链 | 否 | 改造后删（简化签名） |
| `readPromptWithHooksContext` | `:234` | `chat_composer.go:96,480,641,822,934,956,1065`；`chat_runtime_selection_nav.go:145` | **是（主链）** | 保留 |
| `OnTerminalControl` 生产者 | `inputbox_editor_hooks.go:60-65` | 7 处全部设置（`chat_composer.go:123…1077`、`chat_runtime_selection_nav.go:155`） | 是 | 保留（无留空） |
| 回调返回 false 的 raw 回退 | `chat_interaction.go:5382-5390` | 非 unified/Interaction==nil | 否（unified 返回 true） | 保留（降级承重） |
| `ReadTransientSecretPrompt` | `inputbox_editor.go:146-162` | `/login` 链 `chat_login_command.go:380`；降级 `chat_input_queue.go:1479` | **是（直写 stdout 残留，见 §4.5）** | 保留但需收口 |
| `KeyHandler.Start` | `keyhandler_unix.go:15`/`keyhandler_windows.go:31` | `chat_setup.go:72-75` | 是（unified） | 保留（平台差异） |
| `WaitForESC/Notify` | `keyhandler.go:79-106,127` | 仅测试 | 否 | 可删（待确认调试 API） |
| `LiveOSCProbe` | `osc_live.go:25-36` | `terminal_driver.go:134`、`render_bridge.go:24`；装配后 `chat_ui_actor.go:239/305` 仍投递主题 | 是 | 保留 |
| `EnsureConsoleUTF8Output` | `terminal_driver.go:52-54` | `main.go:57` | 是（启动期） | 保留 |
| raw mode 站点 | `inputbox_editor.go:196`（legacy）/`:255`（主链）、`transcript_pager.go:541`、`fullscreen_list.go:200`、`debug_overlay.go:82` | 见左 | 是（除 :196） | :196 随死链删；其余保留 |
| `Terminal.RawMode/DisableEcho/EnsureExitOnSigInt` | `terminal.go:410/426/396` | 无调用者 | 否 | 可删（待确认外部 API） |

### 3.3 矩阵三：模式 × legacy 链可达性（侦察：lf-cmd-modes）

链：A=surface 物理写；B=legacy 打印（`printDirectInteractiveOutput`→`fmt.Print`）；C=transcript legacy 分支；D=legacy 命令处理器；E=启动/选择类打印；F=InputBox 无 hooks 读。

| 模式 | A | B | C | D | E | F |
|---|---|---|---|---|---|---|
| **M1 unified 交互 TTY** | 不可达 | 大部分不可达（unified 重路由 `chat_surface_output.go:572-582,600-611`）；残留见 §4.5 | 不可达（`chat_transcript_renderer.go:42-116` fail-closed 门控） | 不可达（`chat_unified_command_gate.go:18-50` 硬门禁） | 启动期可达（会话前） | 可达（secret 残留） |
| **M2 plain 交互**（non-ANSI TTY / `--compat-mode` / Enable 失败） | 不可达（surface=nil） | **可达（承重）** | coordinator plain 回退 | **可达**（`command.go:48-51`） | **可达**（welcome/会话信息/选择打印） | 不可达（InputBox=nil） |
| **M3 非 TTY/管道** | 不可达 | **可达（承重）** | plain 回退 | **可达** | **可达** | 不可达 |
| **M4 `--no-interactive`/exec** | 不可达 | **可达（预期 stdout 投影）** | 不可达 | 可达（输出到 stdout，预期） | 不可达（banner 抑制） | 不可达 |
| **M5 `--output json`** | 不可达 | 受 `JSONOutput` 守卫；JSON 出口独立 | 不可达 | 受守卫 | stderr（待确认） | 不可达 |
| **M6 ACP / agent stdio** | 不可达（不建 UI） | 基本不可达（stdout=NDJSON，bridge 静音 `agent_stdio_bridge.go:713-729`） | 不可达 | ACP 专用 dispatch | 不可达 | 不可达 |

**配置面**：全仓无渲染模式 config 开关（`unified|legacy|RenderMode|AsyncTranscript|physicalWrites` 在 `internal/config`、`internal/agentconfig` 零命中）；模式由「终端能力 + CLI flag」决定。相关 flag：`--compat-mode`、`--no-interactive`、`--headless`、`--output`/`--json`、`--input-mode`、`--render-output-file`。

**文档承诺面**：`README.md:20/203/234-235`（交互 chat 与 `--no-interactive` 脚本/管道）；`docs/aicli/windows7.md`（compat-mode + input-mode）；`docs/architecture/aicli-chat-unified-renderer-architecture.md`（unified 强制路径、fail-closed 不回退 raw stdout）；`docs/acp/README.md`（stdout 仅 NDJSON）。

---

## 4. 判定汇总：是否需要移除、移除什么

### 4.1 A 类——保留（产品承诺或承重路径）

| 面 | 证据 |
|---|---|
| console/plain 输入链：`chat_legacy_console_line.go`、`chat_legacy_console_editor_windows.go`、`chat_system_console_editor_windows.go`、`chat_pipe_console_line.go` | `chat_setup.go:64-66,89-122,124-135`；`docs/aicli/windows7.md` 明文承诺 `--compat-mode`/`--input-mode` |
| plain/顺序输出打印族：`printDirectInteractiveOutput`→`fmt.Print`（`chat_surface_output.go:572-582`）、`ui.PrintWelcome`（`chat.go:980`）、`ui.PrintSessionInfo`（`chat.go:1312`）、选择/分隔打印族、`ui.DisplayAssistantMessage`（`chat_transcript_renderer.go:73`） | M2/M3/M4 输出主通道；README 管道/脚本承诺 |
| 启动期：`EnsureConsoleUTF8Output`（`main.go:57`）、`ClearIfSupported`（`chat_setup.go:347`）、provider/model 选择器（`chat.go:1008/1199`）、OSC 探针（`osc_live.go`，装配后仍供主题） | 启动先于 presenter；探测缓存共享 |
| 诊断：`NotifyChatDiagnostic`、`aicliDiag*`、`ui.PrintErrorTo`（`chat.go:1080`）、ACP stderr（`acp_mcp_host.go:648`） | unified 唯一诊断面 / 协议通道 |
| 输入主链：`readPromptWithHooksContext` + 全部 `OnTerminalControl` 生产者 + 回调 false 的降级回退 | `chat_composer.go:96…1065`；`chat_interaction.go:5382-5390` |
| 平台差异：`KeyHandler.Start`（SIGUSR2 vs stdin ESC）、`EnsureConsoleUTF8Output` | build tag 双实现 |
| secret 输入：`ReadTransientSecretPrompt`（`/login`） | `chat_login_command.go:380`；**保留但需收口直写（见 4.5）** |

### 4.2 B 类——可删（生产零调用方，低风险）

| 目标 | 证据 | 附带动作 |
|---|---|---|
| `InputBox.readPrompt` + 无 hooks 包装 `ReadWithHistoryPrompt`/`ReadTransientPrompt`/`ReadTransientLine` + `ReadWithHistory` | 侦察结论：生产调用者 = 0；仅测试 `inputbox_editor_test.go:1791` | **已执行（L1-a）**：删测试用例；`writer_inventory_test.go` 基线同步 |
| `writeEditorControlSequence` 的 `hooks==nil` 语义 | nil 传参仅存在于 `readPrompt`（`:208/:214`） | **已执行（L1-a）**：签名改 `LineEditorHooks` 值类型；`inputbox_editor_control_test.go` 同步 |
| `Terminal.PrintAt` | 无生产调用者（lf-ui-surface §1） | **已执行（L1-b）**：writer 基线 + terminal-inventory allowlist 同步 |
| `Status.PrintTo`/`PrintXxxTo` | 无生产调用者 | **已复核（L1-b）**：`PrintTo`/`PrintErrorTo`/`PrintWarningTo` 有生产调用方（`chat.go:1080`、`chat_selection_output.go:129`）→ 保留；`PrintSuccessTo`/`PrintInfoTo` 已删 |
| `screen_lease` raw 直写分支（`:286-330,:503-527`） | 仅 fence-on/测试可达 | **已执行（L1-c）**：租约统一走 transport（缺失 fail-closed）；测试迁移到 transport 断言 |
| `WaitForESC`/`Notify` | 仅测试引用 | **已复核（L1-b）**：`WaitForESC` 删（测试转本地 helper）、`ManualInterrupt` 删（零调用）；`Notify` 保留（Windows 生产派发 `keyhandler_windows.go:65`） |
| `Terminal.RawMode`/`DisableEcho`/`EnsureExitOnSigInt` | 无调用者 | **已执行（L1-b）**：D0-1 无外部消费者，直接删 |
| `InputBox.Read/ReadMultiLine/Show/Update` 等 legacy 方法 | 仓库内无调用者 | **暂缓（评估后）**：联动面 `input.go`/`layout.go`（`writeInputDocument`/`RenderInputArea`/`writeDoc`/`InputAreaDocument`）需逐函数边界评估，作为独立小刀（L1-d）或 L2 前置 |

### 4.3 C 类——改造后删（半死：状态 facade 复用、物理侧可剥离）

| 目标 | 说明 |
|---|---|
| `FixedBottomSurface` 物理绘制族：`Enable` 首帧块（`:429-445`）、`writeOutput` 物理分支（`:974-996`）、`appendOwnedDirectPaintLocked`（`:1139`）、`insertHistoryLinesInRegionLocked`（`:5206-5213`）、`renderOwnedViewportLocked`（`snapshot.go:72-80`）、`flushHoldingLock`/`flushHandoffHoldingLock`（`:372/:392`）、`clearActiveBand` paint 分支（`:2253-2256`）、`Disable` legacy paint 分支（`:520-543`）、`repaintActiveBandLocked` legacy 分支（`:2157-2161`） | 生产不可达已证；测试面大（13 个 surface 测试文件），需先迁移为 state-only 断言再删（注：`Disable` 的**租约**子分支已随 L1-c 改为 transport-only；非租约 paint 分支与其余各项仍待 L3） |
| DEC2026 true 分支 + `SetTerminalSynchronizedFrames(true)` | 唯一 true 开关在 fence 内；`withTerminalWriteLock` 锁本身保留（presenter batch 合法命中） |
| `surface.Apply`（legacy reducer 路径） | 仅 `!UnifiedRendererEnabled()` 可达；`chat_ui_actor.go:1139` 注释明确拒绝 unified 调用 |
| `TerminalOutput()` 默认 stdout 依赖 | 无生产注入；启动 `ClearIfSupported` 改显式 writer 后可去默认 stdout（保留 proxy） |

### 4.4 D 类——观察（依赖其他前置，本轮不承诺）

- fullscreen/pager/debug overlay 的 raw/no-lease 分支：需先给启动期 picker（`chat.go:1021/1214`、`login.go:278`）接租约 transport；
- `FixedBottomSurface` facade 读（popup 输入 `chat_surface_output.go:239,311`、几何 `chat_interaction.go:7401-7408`）：需 presenter 侧补齐 popup/几何 API 后才能迁；
- `chat_unified_command_gate.go` 硬门禁与 legacy 命令处理器须同批删除（handler 迁移到 `CommandResult`）。

### 4.5 unified 模式残留直写（先收口，是"单写端"完整性的缺口）

| 位置 | 内容 | 处置建议 |
|---|---|---|
| `chat_setup.go:891` | 恢复停放团队的 stderr 信息行（unified 可达，无 TTY 门控） | 收口到 `NotifyChatDiagnostic` |
| `chat_setup.go:867` | 退出恢复提示 `fmt.Printf`（仅 `Layout != nil`，Interaction.Shutdown 之后） | 决策点：shutdown 后是否允许 stdout（如允许，登记为 sanctioned） |
| `inputbox_editor.go:151,157` | secret 读经 `WriteTerminalText/Line(os.Stdout,…)` 绕过 gateway（M1 可达） | 经 editor hook / session 控制序列收口；需保留密码输入语义 |
| `chat.go:943`、`chat_setup.go:108` | stderr warning | 评估收口或登记 |

---

## 5. 实施方案（分批切片）

> 原则：**行为等价优先**——B/C 类删除在 unified 路径本已是 no-op（fence 已生效），每刀保证
> `ui`+`commands` 全量绿 + 门禁绿；一刀一提交；文档同步。C 类每小刀先"测试迁移"后"删实现"。

### D0 前置决策（已完成，2026-10-08 决议记录）

> 决议人：owner（2026-10-08）。执行原则：删除前逐符号 `rg` 复核仓库内调用面；与本文表格不一致处
> 以复核事实为准并回填。

1. **外部 API 确认 → 无仓库外消费者，L1 直接删。** 证据：模块根在 `backend/go.mod` 且模块路径无
   `/backend` 后缀（外部 `go get` 结构上无法解析该模块）；goproxy.cn 404、proxy.golang.org 网络超时、
   Sourcegraph 被访问策略拦截；owner 确认无外部消费者。→ §4.2 待删导出符号不再按「保留 + 注释」处理。
2. **secret 收口方案 → 选 (a)**：新增 `LineEditorHooks`/session 侧 secret 写入口（L2 执行）；
   不采用 (b) sanctioned 登记。
3. **门禁语义重构口径（L3）→ 按默认口径执行**：writer inventory 拆「sanctioned console writers
   （白名单类）」与「migration debt（必须递减）」两组；总数仍作回归栅栏。
4. **compat/plain 正规化命名 → 按默认口径执行**：`legacyConsole*` 先行文档层改称 `consoleMode*`
   （L4 执行），并纳入正式验收矩阵。

**L1 执行修正（复核发现的计划偏差，先于删除登记）**：

- `KeyHandler.Notify` 为 Windows 生产派发调用（`keyhandler_windows.go:65`），**保留**；`WaitForESC`
  仅测试引用，按计划删除（测试迁移为本地 helper）。
- `Status` 族：`PrintTo`/`PrintErrorTo`/`PrintWarningTo` 有生产调用方（`chat.go:1080`、
  `chat_selection_output.go:129`），**保留**；`PrintSuccessTo`/`PrintInfoTo` 零调用方，删除。

### L1 死码直删（B 类，1–2 提交）

- 删 `readPrompt` + 3 个无 hooks 包装 + `ReadWithHistory`；简化 `writeEditorControlSequence`（去 nil 分支）；
- 删 `Terminal.PrintAt`、`Status.PrintTo/PrintXxxTo`（确认后）、`screen_lease` raw 分支、`WaitForESC/Notify`（确认后）；
- 同步测试：`inputbox_editor_test.go`、`inputbox_editor_control_test.go`、`writer_inventory_test.go` 基线。
- 验收：`go test ./cmd/aicli/ui ./cmd/aicli/commands`；`-race` 点检输入相关用例；gofmt。

**L1 执行记录（2026-10-08，3 个代码提交）**

- **L1-a**（`48a9b3c5`）：编辑器死链——`readPrompt` + 3 无 hooks 包装 + `ReadWithHistory` 删除；
  `writeEditorControlSequence` 去 nil 分支（`*LineEditorHooks` → `LineEditorHooks`）；`readInteractiveLine*`
  保留为测试入口；门禁 −1（`readPrompt`）。
- **L1-b**（`48ca07c8`）：`Terminal.PrintAt/RawMode/DisableEcho/EnsureExitOnSigInt`、`Status.PrintSuccessTo/PrintInfoTo`、
  `KeyHandler.WaitForESC/ManualInterrupt` 删除；复核修正两处——`Notify/GetESCChannel` 为 Windows 生产派发（保留）、
  `Status.PrintTo/PrintErrorTo/PrintWarningTo` 有生产调用方（保留）；门禁 −1（`PrintAt`）；
  terminal-inventory allowlist 与架构文档同步。
- **L1-c**（`0b0fe123`）：screen_lease raw DEC 1049 分支退役——acquire/write/release 统一要求 transport、
  缺失 fail-closed；`Disable` 租约退出 transport-only；`writeLeaseSequencesLocked` 删除；测试迁移到
  transport 断言（新增 `recordingLeaseTransport`）；commands 三文件注入最小 transport
  （`newChatScreenTestSession` helper 修复 12 个 screen-framework 用例的 raw 租约依赖）；门禁 −4
  （screen_lease×3 + `Disable`）。
- **门禁基线：ui 直写债务 41 → 35（net −6）**。
- **偏差记录**：screen_lease raw 的测试迁移面大于计划预期（17 处调用点、多个用例需语义迁移），
  故拆为独立小刀 L1-c；`ReleaseExitFailureStillRepaints` 删除（由 `FailedUnifiedExitRetainsRetryableLease`
  覆盖更优语义）。
- **暂缓项**：`InputBox.Read/ReadMultiLine/Show/Update` 等 legacy 方法簇（零调用，D0-1 已排除外部消费者）
  与 `input.go`/`layout.go` 联动面需逐函数边界评估，作为独立小刀（L1-d）或 L2 前置。
- **环境偶发**：`TestTerminalSessionExecutorClaimMissReleasesStrandedInFlight` 在加载下偶发失败（本批 2 次）；
  静默隔离复跑 ×10/×100 与全量复跑均绿，与 L1 文件面无关，按环境偶发登记。
- **验收结果**：`gofmt` 干净；`go build ./...` 绿；`TestUIInteractiveDirectWriterInventory` 绿；
  `go test ./cmd/aicli/ui` 绿（含 lease 子集 `-race`）；`go test ./cmd/aicli/commands` 全量绿（174s）。

### L2 残留直写收口（4.5，1–2 提交）

- `chat_setup.go:891` → `NotifyChatDiagnostic`；`chat.go:943`/`chat_setup.go:108` 评估收口；
- secret 读经 hooks/session 收口（按 D0-2）；
- 扩展单写端断言：`TestUnifiedSessionSinglePhysicalWriterFence` 增加 secret 路径驱动；
- `chat_setup.go:867` 按 D0 决策登记或改造。
- 验收：单写端栅栏 PASS + 门禁计数下降 + 真机 e2e（unified 场景）。

### L3 FixedBottomSurface 拆壳（C 类，3 小刀）

- **L3-1**：删 `Enable` 首帧块 + DEC2026 true 分支 + `SetTerminalSynchronizedFrames(true)` 调用；
  迁移 `fixed_bottom_surface_test.go`、`terminal_write_lock_sync_test.go`、`terminal_write_lock_freeze_test.go`
  的 legacy 开启断言 → 「仅 false/禁止调用」断言。
- **L3-2**：删物理绘制实现（`appendOwnedDirectPaintLocked`/`insertHistoryLinesInRegionLocked`/`flush*`/
  `renderOwnedViewportLocked` 写体/`writeOutput` 物理分支），保留 state-only 语义；迁移 13 个 surface 测试文件。
- **L3-3**：删 `clearActiveBand` paint 分支、`Disable` legacy paint、`repaintActiveBandLocked` 分支、
  `surface.Apply`；门禁基线同步（预计 ui 债务 41 → ~25）。
- 每刀验收：`go test ./cmd/aicli/ui`（含 inventory）+ `./cmd/aicli/commands` 相关子集；一刀一提交。

### L4 门禁语义重构与降级正规化（1 提交，纯文档/测试）

- writer inventory 分组：console writers 白名单化；P0 台账 §4 更新"完成态"口径；
- README「写端门禁」补充 sanctioned 类说明；`windows7-compat-internals.md` 与本文互链；
- 验收矩阵新增 compat 场景（真机 e2e 或脚本化 PTY）。

### L5 观察项（可选，另行立项）

- 启动期 picker 接租约 → 再删 fullscreen/pager/debug raw 分支；
- presenter popup/几何 API 迁移 → 再删 surface facade 读；
- legacy 命令处理器批量迁 `CommandResult` → 删 `chat_unified_command_gate` 硬门禁。

### 批次依赖与回滚

```
D0 ──▶ L1 ──▶ L2 ──▶ L3-1 ──▶ L3-2 ──▶ L3-3 ──▶ L4
                     （L2 与 L3 可并行，文件面不重叠）
L5 独立，依赖各自前置
```

- 回滚：每批一个提交，`git revert <commit>` 即回到上一绿点；L3 各刀行为等价（unified 下本就是 no-op），
  回滚风险集中在测试迁移，不含生产行为变化。

---

## 6. 验收标准（每批适用）

1. **门禁**：`go test ./cmd/aicli/ui -run 'DirectWriterInventory|WriterInventory'` 绿；删除实现同步从基线
   `ui/writer_inventory_test.go` 摘除条目（迁移一处 → 划掉一处，不允许新增）。
2. **包级回归**：`go test ./cmd/aicli/ui ./cmd/aicli/commands` 全绿；触及输入/编辑器时追加 `-race` 点检。
3. **单写端**：`TestUnifiedSessionSinglePhysicalWriterFence` PASS（L2 起增加 secret 路径驱动）。
4. **真机**：unified 场景 e2e（`scripts/test-aicli-windows-terminal-e2e.ps1`）保持 PASS；
   L4 起追加 compat 场景（`--compat-mode` 基本可用性 + 无 ANSI 降级提示）。
5. **文档**：每批同步 P0 台账 §4 与本文 §4 状态列。

## 7. 风险与需人工确认

| 风险/未知 | 影响 | 缓解 |
|---|---|---|
| 13 个 surface 测试文件钉住 legacy paint 语义 | L3 工期与回归面最大项 | 先迁移断言（state-only/租赁 transport），再删实现；每小刀独立提交 |
| secret 读直写收口改变密码输入路径 | Win7/compat IME 语义 | 保留原 console 分支；unified 分支仅换写入口；真机回归 |
| `FixedBottomSurface` facade 读（popup/几何）仍被 unified 依赖 | 误删会破坏 unified 弹层 | 明确列入 D 类，须先补 presenter API（L5） |
| 仓库外消费者调用 ui 导出符号 | 删除造成外部破坏 | **已关闭（D0-1，2026-10-08）：无外部消费者**；逐符号复核后删除 |
| 门禁语义重构削弱约束 | 新增直写可能漏检 | sanctioned 类仍按"类白名单 + 零新增"扫描；债务计数独立递减 |
| `WaitForESC`/unix `KeyHandler` SIGUSR2 是否产品行为 | 误删调试/中断能力 | **已核查**：`WaitForESC` 仅测试引用→删（测试迁移 helper）；`Notify` 为 Windows 生产派发→保留；SIGUSR2 路径保留 |
| JSON 模式是否隐含 `NoInteractive`、debug_overlay 非租约入口未穷举 | 模式矩阵边缘缺口 | 标注"需人工确认"，不据此删除 |

## 8. 附录：证据来源

- 本地锚点（本文作者，基线 `d0d3da80`）：
  - 装配/降级：`chat_setup.go:64-135,218-250,305-355`；`chat_interaction.go:615-735`；`chat_ui_actor.go:200-331`；
  - fence 语义：`fixed_bottom_surface.go:248-291,409-447,3687-3699`；`chat_surface_output.go:430-482,560-615`；
  - 输出兜底：`chat_system_output.go:105-150`；`terminal.go:436-440`；门禁基线 `writer_inventory_test.go:57-121`。
- 三路只读侦察（2026-10-08，子代理会话产物）：
  - `lf-ui-surface`：surface/renderer 链全量矩阵 + fence 审计 + git 史（结论已并入 §3.1/§4）；
  - `lf-input-editor`：输入/编辑器/平台链（结论已并入 §3.2/§4.2）；
  - `lf-cmd-modes`：模式矩阵 M1–M6、配置面、文档承诺面（结论已并入 §3.3/§4.5）。
- 侦察中受只读策略拦下的收尾命令（目录枚举/行数统计）不影响上述结论；
  相关符号的逐一定位均以 `rg`/`view` 证据给出。
