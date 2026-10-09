# L5-3 命令通道收口方案（legacy 处理器 → CommandResult，删硬门禁）

> 性质：L5-3 独立方案文档（由《L5 立项评估》§3 升级；触发式立项已触发开工）。
> 目标终态：unified（默认交互）下所有命令走 parse → `CommandResult` → render 单一模型；
> 门禁三件套与 `handleCommand` 回落分支删除；「命令不可能触达 legacy 直写/模态读」
> 由结构保证而非门禁枚举。
> 基线：`feat/render-p0-writer-unification` @ `f5c2286d`（Batch A 已执行）。
> 上游：退役方案 §5（L5 观察项）、P0 台账 §4、
> [L5 立项评估](aicli-render-l5-candidates-20261009.md) §3。

## 1. 现状（2026-10-09 机械枚举）

- 目录全集 `chatSlashCommandCatalog()`：72 个名称（60 主名 + 12 别名）。
- 覆盖判定 = 结构化分派认领 ∪ 守卫允许清单（`chat_command_result.go:366-383`）∪ 门禁（/exit 等）：
  **唯一缺口 `/normal`**（目录全名，Alias=/n；`/n` 已认领、全名漏接）→ Batch A 已修复。
- 残余拒绝面（Batch C 后）：统一会话的最终回落为两档 typed 结果
  （`chat_unified_command_fallback.go`）——未知命令 → "错误: 未知命令: X"；目录内命令的
  未支持参数/状态形式 → "错误: X 的当前参数或状态形式不受支持"。无任何"尚未迁移"类文案。
- 后续小项（登记）：/goal、/memory 的持久化/状态错误分支等仍落入"不受支持"档，可精确化为
  逐分支 typed 文案；裸形式与主路径已全部 typed 并被覆盖测试机械守卫。
- fence（/backtrack、/rewind、/resume 残余变体）：**Batch B 已全量删除**（`f396f2d9`）。
- fence 曾覆盖的变体（Batch B 收编明细）：
  - `/rewind` 数字首参/空参/list：收编为 /backtrack 别名语义（typed，含 --apply/--submit）；
    其余（select/audit/checkpoint-id 等）：`rewindUnsupportedMessage` typed 提示（与 legacy 同文案）。
  - `/resume` picker 前置缺失分支：`resumePickerUnavailableMessage` typed 降级（fail-closed，
    plain/JSON 路径仍回落 legacy 行读取器不变）。
- 门禁（gate/reject）：**Batch C 已全量删除**（`2a125651`）；`/exit` 已结构化
  （`CommandQuit`，消费点 `chat_busy_command_exec.go:110`），unknown 语义见上。

## 2. 分批

### Batch A 已执行（2026-10-09，`f5c2286d`）

- `/normal` 缺口修复：守卫放行 + 结构化认领（与 /s、/n 同一处理器，stream=false）；
  回归测试 `TestStructuredStreamShortcuts_AllFormsClaimed`（4 子例：/s、/n、/normal-plain、
  /normal-unified）。
- 验证：目标测试 + Stream 族绿；gofmt/vet/build 绿；commands 全量复跑绿（187s）。
  首跑出现 1 个环境 flake（`TestLocalActorRegistryDiscardWorktreeRefusesWhileChildRunning`：
  `initLocalIsolationTestRepo` 内 git 临时仓库初始化偶发 "not a git repository"）——
  隔离 ×5 全绿、actor 家族子集绿、全量复跑绿，按环境偶发登记（非本刀文件面）。

### Batch B 已执行（2026-10-09，`f396f2d9`）

- **/rewind 别名语义收编**（与 legacy 路由 `command.go:298-312` 对齐）：数字首参、空参、
  list/ls 等价 `/backtrack`（含 `--apply/--submit` typed 效应，`ApplyBacktrack` 走既有统一
  apply 事务）；其余（select/audit/checkpoint-id 等）保留 legacy"未提供"提示，改为 typed
  结果（`rewindUnsupportedMessage` 单一文案源，legacy 出口共用）。
- **/resume picker 降级 typed 化**：统一会话在 `canOpenChatResumePicker` 前置缺失时提交
  `resumePickerUnavailableMessage`（fail-closed，不回落 legacy 行读取器）；plain/JSON
  路径保持 `handled=false` 回落 legacy 不变。
- **fence 全量删除**：`unifiedInteractiveLegacyCommandFence`/`rejectUnifiedInteractiveLegacyCommand`
  与全部调用点删除（backtrack×2、backtrack_select、resume×2、export、login、provider、
  model、skills、theme）；backtrack/resume 统一分支保留防御性 fail-closed 渲染（不可达兜底）。
- **测试迁移**：fence 测试改写为 `TestUnifiedResidualCommandsStayTypedWithoutFence`（数字
  /rewind → typed 错误单元格；checkpoint-id → typed 提示；/resume → typed 降级；零裸写、
  零迁移文案）；`TestStructuredRewindApplyRemainsFenced` →
  `TestStructuredRewindNumericAliasCarriesTypedApply` + 新增 checkpoint typed 断言；
  timeline 测试 marker 同步。
- 验证：定向 4 项 + 家族子集（Backtrack/Rewind/Resume/Timeline/Plan/Gate）绿；
  gofmt/vet/build 绿；commands 全量复跑绿（180.8s）。
- 验收：`rg 'unifiedInteractiveLegacyCommandFence|rejectUnifiedInteractiveLegacyCommand|已拒绝旧终端直写'`
  非测试 = 0；unified 下 fence 文案 = 0；compat/plain 行为不变。

### Batch C 已执行（2026-10-09，`2a125651`）

- **硬门禁删除**：`dispatchUnmigratedUnifiedChatCommand`/`rejectUnmigratedUnifiedChatCommand`
  删除（文件重命名为 `chat_unified_command_fallback.go`）；`command_invoke.go` 的 /call、/skill
  保护移除；`command.go` 回落分支替换为 typed 未知命令路径。
- **/exit 结构化**：`/exit`/`/quit`/`/q` 认领为 farewell 单元格 + `CommandQuit` 动作
  （Phase B 统一退出；忙时既有 quit 拦截与 Reject 策略不变）。
- **unknown 语义（两档回落）**：未知命令 → `错误: 未知命令: X\n输入 /help 查看可用命令`；
  目录内命令的未支持参数/状态形式 → `错误: X 的当前参数或状态形式不受支持\n输入 /help 查看用法`。
- **/help 全变体**：统一会话带参 /help 也直接渲染帮助文档（plain 保留 legacy 回落）。
- **参数拒绝面收口**：/status、/new、/history 带参 → typed"不接受参数"；/load 缺参/解析/加载
  错误、/title//rename 缺参与无会话 → typed 单元格；/goal --json → typed 提示。
- **/debug**：export 早已结构化；残余兜底文案改为防御性 fail-closed（不可达）。
- **机械守卫**：新增 `TestUnifiedCatalogCommandsNeverFallToUnknown`（72 名称全集逐一断言
  被认领）+ `TestUnifiedKnownCommandVariantsStayTyped`（7 项参数拒绝面）+
  `TestUnifiedFallbackDistinguishesKnownAndUnknown`（两档回落）。
- 验证：定向 9 项绿；gofmt/vet/build 绿；commands 全量复跑绿（183.9s）；期间修复改名
  导致的源扫描清单失败（`ab5496a4`）。
- 验收：`rg '尚未迁移到统一渲染命令通道'`（非测试）= 0；门禁符号 = 0。
- 登记后续小项：/goal、/memory 的持久化/状态错误分支等仍落入"不受支持"档（见 §1）。

## 3. 验收（每批适用）

- commands/ui 全量绿；每刀一提交、可独立 revert；
- 覆盖不回归：建议 Batch C 增加机械覆盖测试（目录全集逐个断言"被认领或显式 unknown"，
  禁止静默回落）——/normal 类缺口即由该法暴露；
- 真机：unified 基本命令流 + compat 场景（退役方案 §6）PASS。

## 4. 风险

- `/rewind --apply` 为 checkpoint 应用复杂流，需语义交互模型（参照 typed mutation 先例）；
- `/resume` 前置缺失分支涉及 surface/租约/弹层状态，迁移不得放松 fail-closed；
- 门禁删除改变 unknown 文案与退出路径，需同步帮助文档与测试断言。

## 5. 执行记录

- 2026-10-09 Batch A：`f5c2286d`（/normal 修复 + 回归测试 4 子例）；机械枚举口径
  （目录全集 − 分派认领 − 守卫 − 门禁）固化为 §1，后续批次复测同法。
- 2026-10-09 登记（环境 flake，非本刀）：`TestLocalActorRegistryDiscardWorktreeRefusesWhileChildRunning`
  （全量首跑偶发 1 次；隔离 ×5 / 家族子集 / 全量复跑全绿；失败点在 `initLocalIsolationTestRepo`
  的 git 临时仓库初始化，与命令通道改动无文件面交集）。
- 2026-10-09 Batch B：`f396f2d9`（/rewind 别名语义 + /resume typed 降级 + fence 全量删除，
  13 files，+100/−130）；fence 家族测试改写为正向 typed 断言。
- 2026-10-09 Batch B 验证：commands 全量复跑绿（180.8s，exit 0）；非测试 fence 引用 = 0。
- 2026-10-09 Batch C：`2a125651`（硬门禁删除 + /exit 结构化 + 未知命令两档回落 + 参数拒绝面
  收口，11 files，+280/−106）；新增目录覆盖/参数面/回落三组机械测试。
- 2026-10-09 Batch C 验证：commands 全量复跑绿（183.9s，exit 0）；`尚未迁移…`（非测试）= 0；
  门禁符号 = 0；源扫描清单随改名同步（`ab5496a4`）。
- 2026-10-09 登记（环境 flake，非本刀）：`TestTTY_LiveLoop_LLMRetryRendersAdvancingTimerE2E`
  （全量负载下偶发：retry 状态行与 turn 完成的绘制竞态，测试注释已声明该时序脆弱性；
  隔离 ×10 全绿）；`TestStreamingAssistantFinalTailTransfersExactlyOnceToNativeHistory`
  （再次偶发，隔离复跑绿，已在退役方案 §5 登记）。
