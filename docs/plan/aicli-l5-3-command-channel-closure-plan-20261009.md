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
- 残余拒绝面（"尚未迁移/已拒绝"类文案现存 3 处）：
  - `chat_unified_command_gate.go`：gate default（未认领命令 + unknown 命令）；
  - `chat_debug_archive.go:57`：/debug 归档子命令未迁移（独立小项，Batch C 顺带）；
  - `chat_command_result.go:895-898`：fence（/backtrack、/rewind、/resume 残余变体）。
- fence 可达变体（Batch B 的输入，逐变体枚举）：
  - `/rewind <n> --apply`：检查点应用命名空间不认领（`chat_backtrack_command_test.go:70`
    钉住当前行为）；`/backtrack ... --submit` 已结构化（typed `ApplyBacktrack` 效应）。
  - `/resume` picker 前置缺失分支：`canOpenChatResumePicker`（`chat_resume_command.go:186-196`）
    要求 unified 前置齐全（Interaction/SessionManager/Surface enabled+OwnedViewport+无租约/无弹层
    +CanUseFullScreenList），缺失时 `handled=false` → fence fail-closed；直连目标与 picker
    可用分支均已结构化。
  - `command_invoke.go:30/122`：/call、/skill 非分派入口保护。
- 门禁自身：`/exit`（"再见！"+ 进程退出）仍由 gate 处理；unknown 命令由 gate default 统一报
  "尚未迁移…禁用"（收口后应改为"未知命令"语义）。`CommandQuit` 已是 CommandResult 既有动作
  （`chat_command_result.go:24-25`；消费点 `chat_busy_command_exec.go:110`）。

## 2. 分批

### Batch A 已执行（2026-10-09，`f5c2286d`）

- `/normal` 缺口修复：守卫放行 + 结构化认领（与 /s、/n 同一处理器，stream=false）；
  回归测试 `TestStructuredStreamShortcuts_AllFormsClaimed`（4 子例：/s、/n、/normal-plain、
  /normal-unified）。
- 验证：目标测试 + Stream 族绿；gofmt/vet/build 绿；commands 全量回归（随本批执行）。

### Batch B 待执行：fence 残余迁移 → 删 fence

- 逐变体枚举 fence 可达面；每个变体给出 typed 结果（迁移或显式降级错误单元格），
  不留 fail-closed 泛化文案；
- `/rewind --apply`：参照 /backtrack apply 的 typed mutation 先例迁移（或显式 typed 拒绝，
  二选一在开工时定稿）；
- `/resume` picker 前置缺失分支：改为 typed 不可用说明（保持 fail-closed，不回落 legacy）；
- 删除 `unifiedInteractiveLegacyCommandFence`/`rejectUnifiedInteractiveLegacyCommand` 与
  `command_invoke.go` 的 reject 保护；更新"remains fenced"测试断言。
- 验收：unified 下 fence 文案 = 0（`rg '正在迁移到统一渲染器'`）；compat/plain 行为不变。

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
