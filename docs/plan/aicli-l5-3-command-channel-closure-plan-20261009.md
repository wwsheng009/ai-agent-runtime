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
- 残余拒绝面（Batch B 后）：
  - `chat_unified_command_gate.go`：gate default（未认领命令 + unknown 命令）与
    `/exit`/`/help` 兜底；`rejectUnmigratedUnifiedChatCommand` 保护 /call、/skill 等
    非分派入口——Batch C 处理；
  - `chat_debug_archive.go:57`：/debug 归档子命令未迁移（独立小项，Batch C 顺带）；
  - fence（/backtrack、/rewind、/resume 残余变体）：**Batch B 已全量删除**（`f396f2d9`）。
- fence 曾覆盖的变体（Batch B 收编明细）：
  - `/rewind` 数字首参/空参/list：收编为 /backtrack 别名语义（typed，含 --apply/--submit）；
    其余（select/audit/checkpoint-id 等）：`rewindUnsupportedMessage` typed 提示（与 legacy 同文案）。
  - `/resume` picker 前置缺失分支：`resumePickerUnavailableMessage` typed 降级（fail-closed，
    plain/JSON 路径仍回落 legacy 行读取器不变）。
- 门禁自身：`/exit`（"再见！"+ 进程退出）仍由 gate 处理；unknown 命令由 gate default 统一报
  "尚未迁移…禁用"（收口后应改为"未知命令"语义）。`CommandQuit` 已是 CommandResult 既有动作
  （`chat_command_result.go:24-25`；消费点 `chat_busy_command_exec.go:110`）。

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

### Batch C 待执行：删硬门禁 + unknown 语义

- `/exit` 结构化认领（farewell doc + `Action: CommandQuit`）；/debug 归档子命令补迁；
- unknown 命令改为 typed"未知命令"结果（可附 /help 引导）；
- 删除 `dispatchUnmigratedUnifiedChatCommand`/`rejectUnmigratedUnifiedChatCommand`；
  `command.go:45-51` 回落分支替换为结构化 unknown 路径（legacy `handleCommand` 仅保留给
  plain/compat/JSON 出口）；
- 更新门禁相关测试与文档（README/help 文案）。
- 验收：`rg '尚未迁移到统一渲染命令通道' backend/cmd/aicli/commands -g '!**/*_test.go'` = 0；
  commands/ui 全量绿；compat 真机场景 PASS。

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
