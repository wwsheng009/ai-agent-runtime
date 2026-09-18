# 计划：TUI `/skill` 改为回合机制（对齐 Web 的模型驱动语义）

> 状态：已完成（2026-09-18；实现落在主树，测试与文档已同步，见文末"实施记录"）
> 背景：远程实测（`docs/skill_runtime/skill_loading_and_interaction_20260918.md` §6.6）证明 TUI `/skill` 走 `executeDirectFunction` 直执链（日志 `turn_id: "direct"`），而 Web `/skill` 是普通回合 + ProgramGuide 注入 + 模型自选程序。
> 目标语义（用户表述）：`/skill` 不直接执行，而是发起回合，把该 skill 的说明与程序清单注入系统提示，由模型自己决定用哪些程序。

## 目标

1. 交互式 TUI 的 `/skill <name> <args>` 默认改为：提交一个**普通 chat 回合**（走既有 send 管线、流式渲染），并为本回合：
   - 注入 `skill.ProgramGuide(<name>)` 为 system 消息；
   - 把该 skill 与其声明的程序（`ProgramTools`）**叠加**进本回合函数面（pin），让模型自行选择调用哪些程序。
2. 保留确定性直执能力：`/skill --direct <name> <args>` 走既有 `executeDirectFunction`（无模型、无回合）。
3. `/skills` 选择器的确认结果仍是 composer 草稿 `/skill <name> `，提交时自动落到同一条回合路径（单一语义）。
4. 非交互投影（plain/JSON/headless）行为不变：`chat_command_result.go:233` 的结构化分支本就以 `unifiedDirectInteractiveOutput(session)` 为门，headless 继续走 legacy 直执。

## 关键事实（已取证）

- 命令结果契约已有 post-commit 发送机制：`CommandResult.SendObjective`（`chat_command_result.go:167-174`，dispatch 见 `command.go:103-111`）与 `SendMessageAfterCommit`（`:175-182`）。→ 复用该模式新增 `SendSkillTurn`。
- 回合装配：`chat_core.go:155-263`（`history` 拼装 → `stableSharedFunctionSelectionForRequest(session, prompt)` → `executeToolLoop{Tools: toolDefinitionsFromSelection(selection)}`）。
- **工具面是会话级稳定快照**：`chat_tool_surface_stability.go:9-19` 忽略 prompt 且缓存 `session.stableSharedToolSelection`。→ pin 必须"按回合叠加"，不得写回缓存（否则污染后续回合）。
- Guide 生成已导出：`skill.ProgramGuide(skill)`（`internal/skill/executor.go:661-663`，内容见 `:687-724`，含 `/skill <name>` 前缀说明）。
- 程序名来源：`skillProgramTools` = `skill.Tools` ∪ workflow 步骤 `Tool`（`executor.go:657-683`）。

## 改动清单

| # | 文件 | 变更 |
|---|---|---|
| 1 | `backend/cmd/aicli/commands/chat_command_result.go` | 新增 `SendSkillTurn *SendSkillTurnRequest`（`{SkillName, Prompt, VisiblePrompt string}`）与类型定义；文档注释说明 post-commit 语义 |
| 2 | `backend/cmd/aicli/commands/chat_skill_picker.go` | `executeStructuredSkillCommand`：解析 `--direct`；默认路径改为返回 `SendSkillTurn`（保留名称/prompt 校验与 `DisableTools` 报错）；`--direct` 保留原 `executeDirectFunction` 分支 |
| 3 | `backend/cmd/aicli/commands/command.go` | dispatch：处理 `SendSkillTurn` → 先登记一次性 pin（`session.PendingSkillTurn = ...`）再经既有 send 管线发送 `VisiblePrompt`（`/skill <name> <args>`）；发送失败清除 pin 并回报错误 |
| 4 | `backend/cmd/aicli/commands/chat_core.go` | 回合开始时消费 pin：把 `ProgramGuide` 追加为 system 消息（参考 `goalContinuationInstructionMessage` 的既有做法）；`defer` 清空 pin（消费即焚） |
| 5 | `backend/cmd/aicli/commands/chat_tool_surface_stability.go` | 新增按回合叠加函数：`overlayPinnedFunctions(stableSelection, catalog, pinnedNames)`；pin 来源 = `skill__<name>` + 该 skill 的 `ProgramTools`（在 catalog 中存在的名字），不写回稳定缓存 |
| 6 | 测试 | `chat_skill_picker_test.go`：默认路径断言 `SendSkillTurn`（不再执行）；`--direct` 断言旧行为；校验失败保持。新增 `chat_core`/surface 测试：pin 回合的 tools 含 `skill__<name>`（若程序名存在则一并含），且 guide system 消息出现在请求里；pin 只影响该回合 |
| 7 | 文档 | `chat_slash_command_catalog.go` 的 `/skill` usage/summary 更新；`docs/skill_runtime/skill_loading_and_interaction_20260918.md` §6.6 更新为新语义 |

## 决策（默认，可回退）

- **D1 pin 内容**：同时 pin `skill__<name>` 与 `ProgramTools`（存在才 pin）。理由：模型既能调用程序（web 风格），也能选择调用 skill 本身（workflow 兜底）；程序名不存在时只 pin skill 函数并记 debug。
- **D2 直执保留**：`/skill --direct` 保留（无 provider / 需要确定性时的兜底）。
- **D3 选择器**：`/skills` 确认仍回填草稿，提交走同一回合路径（不改 picker 本身）。

## 验证

1. `go test ./cmd/aicli/... ./internal/skill/...`（含新增用例）。
2. `go build ./cmd/aicli`。
3. 手工：本地 `aicli chat`（或 `--web` 远程驱动）跑 `/skill run_shell_command echo TURN_OK`：
   - 日志 `turn_id` 不再是 `direct`；TUI 渲染为普通回合（工具调用行 + 模型回答）；
   - `/skill --direct run_shell_command echo DIRECT_OK` 仍渲染命令单元。
4. 回归：`/skills` 选择器、headless `aicli exec "/skill ..."` 行为不变。

## 风险

- 回合化后依赖模型/provider；`--direct` 作为兜底。
- `ProgramTools` 名与 aicli 函数名可能不完全一致 → 只 pin 命中项，未命中不阻断（guide 仍列出程序名，模型可向用户说明）。
- 既有测试 `chat_skill_picker_test.go`、可能存在的 `/skill` 文档/快照断言需同步更新。

## 实施记录（2026-09-18）

- 实现由隔离子代（worktree `tui-skill-turn-impl`）落地，但其编辑实际写入了主树；子代在执行中覆盖了 `internal/agent/turn_context.go` 既有的 `WithTurnID/TurnIDFromContext/runtimeEventPayloadWithTurnID`，导致主树编译失败并中断。
- 收尾由主会话完成：恢复被覆盖的 turn-id API；补强 `resolveSkillTurnPin` 在无 skills binding / summary-only 时的 guide 降级（改用函数目录 schema 描述）；补齐命令层与 agent 层单测；同步 3 处用户文档。
- 验证：`go build ./cmd/aicli`（输出到 `.scratch/aicli-skillturn.exe`，避免占用运行中的 `aicli.exe`）；`go test ./cmd/aicli/commands/ ./internal/agent/ ./internal/chat/ ./internal/skill/ -count=1` 全绿；`gofmt`/`go vet` 干净。
- 未做：真实 TUI 实例的端到端冒烟（用户运行中的 :58710 会话未打扰）；建议用户重启本地 TUI 后跑 `/skill run_shell_command echo TURN_OK` 验证一次。
