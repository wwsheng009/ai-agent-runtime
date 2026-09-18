# Skill 模型驱动调用实施方案（Web / 多端统一）

状态: **in-progress（P0/P1/P2 已实施，P1b 待实施）**
日期: 2026-09-18
关联文档:
- `docs/skill_runtime/skill_invocation_mechanism.md`（现状：两条链路并存的权威说明）
- `docs/plan/skills-exposure-implementation-and-test-plan.md`（技能暴露策略）
- `docs/skill_runtime/skills_api_client.md`（`ExecuteSkill` 定位为 admin/debug 入口）

## 0. 实施结果（P0，2026-09-18）

- `internal/skill/executor.go`：新增 `options.execution_mode`（`auto`/`model`）。`model`
  模式跳过 Handler/Workflow 直执行；注入 skill 程序清单（description + 程序清单 +
  workflow 步骤摘要）；工具面取“声明 tools ∪ workflow step tools”；工具循环默认开启，
  显式 `tool_loop=false` 仍可关闭。
- `internal/skill/executor_test.go`：新增
  `TestExecutor_ModelMode_LetsModelChooseWorkflowProgram`（模型自选程序 + 说明注入 +
  workflow 不再直执行）与 `TestExecutor_AutoMode_WorkflowStillDirectExecutesWithoutLLM`
  （auto 语义零回归）。
- 前端 `use-composer-command-executor.ts`：`/skill` 携带 `options.execution_mode=model`；
  `skills.test.ts` 扩展 options wire 契约断言；composer 测试新增 `/skill` 模型驱动与
  未知名称前置失败两条用例。
- 验证：`go test ./internal/skill/ ./internal/api/skills/ -count=1` 通过；
  前端 35 用例通过（`tsc --noEmit` / `eslint` 干净）。
- P1（说明注入）与 P2（前端回合化）已于同日实施：`expose_skills` 回合把 skill
  说明与程序清单注入模型上下文，前端 `/skill` 提交为普通回合，消息流可见。
- 仍未完成（P1b）：`skill__<name>` 函数级派发，用于覆盖“程序只有自定义 Handler、
  没有独立 MCP 工具”的 skill；当前口径覆盖 workflow 容器型 skill（bash 等既有工具）。

## 1. 背景

当前 skill 调用在仓库内存在三条链路，语义不一致：

| 链路 | 模型感知 skill？ | 谁决定执行什么 | 结果可见性 |
|---|---|---|---|
| `aicli chat`（skills-as-functions） | ✅ 模型看到 `skill__<name>` function schema | **模型**发起 tool_call | CLI 命令单元 |
| web `/api/agent/chat`（route-first） | ⚠️ 后端 `Router.RouteDirect` 直接执行 | 后端（仅 `allows_direct_route` 的 skill） | SSE 事件流 |
| web `/skill`（composer） | ❌ 直调 `POST /api/runtime/skills/{name}/execute`（admin/debug） | 前端 + 后端直执 | 仅状态栏提示条 |

问题（以 `/skill run_shell_command pwd` 实测为例）：

1. `run_shell_command` 是 workflow skill（`.agents/skills/run_shell_command/skill.yaml`），
   `Executor.Execute` 的优先级是 **Handler → Workflow → executeDefault**，命中 workflow 后
   **零次 LLM 调用** —— 模型既没有读到 skill 说明，也没有“选择调用哪个程序”的机会。
2. 该链路不产生运行时事件（无 `AppendEvent`/`Publish`），前端消息流（SSE 轨迹 + 历史同步）
   都不会更新；web 前端还丢弃了 execute 响应体 → 只剩提示条。
3. 即使落到 `executeDefault`，`tool_loop` 默认关闭（`executor.go:486-508`），模型只有单次
   调用、无工具面，仍无法“决定调用哪些程序”。

## 2. 目标

1. **模型驱动**：skill 的说明文档（manifest description / systemPrompt / companion prompt /
   workflow 程序清单）进入模型上下文，由模型决定调用 skill 中的哪些程序（工具/步骤）。
2. **可见性**：skill 执行过程与结果进入对话消息流（assistant 说明 + tool 行 + 输出）。
3. **多端一致**：web 与 aicli 使用同一套 skill-as-function 语义；`ExecuteSkill` REST 保持
   admin/debug 定位，不回退其契约。

## 3. 非目标

- 不移除/不改变 workflow 的确定性直执行能力（保留给 CI、脚本、admin 调试与
  `allows_direct_route` 场景）。
- 不重写 agent ReAct 循环；P1 只在工具面注入/派发层接入。
- 不在本方案中处理 skill 市场、权限、配额（沿用既有 `permissions` / usage ledger）。

## 4. 设计

### 4.1 执行模式（后端 `internal/skill`）

新增请求级选项 `options.execution_mode`：

| 值 | 语义 |
|---|---|
| `auto`（默认） | 保持现状：Handler → Workflow → executeDefault（含 `tool_loop` 显式开启时的工具循环） |
| `model` | 跳过 Handler/Workflow 直执，强制走 `executeDefault`；默认开启工具循环（`tool_loop`） |

`execution_mode=model` 时的上下文组装：

1. `resolveSkillPrompts` 仍负责显式 systemPrompt / Codex `Body`（说明文档优先）；
2. 若无显式说明文档，则注入 **skill 程序清单投影**（`buildSkillProgramGuide`）：
   - skill 名称与 description；
   - 可用程序（声明的 tools ∪ workflow 步骤用到的 tools，去重保序）；
   - workflow 步骤摘要（step id/name/tool），作为“可用程序及推荐参数”的元信息。
3. 工具面 = `skillProgramTools(skill)`（声明 tools ∪ workflow step tools），
   模型自行选择调用哪一个/哪几个；
4. 多步工具循环回灌工具结果，直至模型给出最终回答（沿用现有
   `executeSkillToolCalls` + 步数上限）。

### 4.2 消息流（P2）

`/skill` 不再走“直执 + 提示条”，而是把 skill 作为模型可调用能力纳入**普通回合**：

```
用户: /skill run_shell_command pwd
  → 回合提交（携带 expose_skills=[run_shell_command]）
  → 模型读到 skill 说明 + skill__run_shell_command 函数 schema
  → 模型发起 tool_call(skill__run_shell_command, {prompt:"pwd"})
  → SkillFunction.Execute → skill.Executor（workflow / 工具循环）
  → tool 结果回灌 → 模型输出最终回答
  → SSE 事件流渲染：assistant 消息 + tool 行 + 输出
```

P2 之前（P0/P1 过渡期）UI 侧的最小可见性：
- 前端展示 execute 响应的 `result.output` 与 `observations`（工具步骤）；
- 执行成功后触发一次当前会话的权威历史同步（`useSessionRefresh.refreshSession`），
  使 `persistChatTurn` 落库的 user/assistant 两条消息出现在线程中。

### 4.3 多端复用（P1）

- 将 `skill__<name>` 的 schema 生成与函数派发从 `cmd/aicli/commands`
  （`SkillFunction` / `aicliFunctionCatalog`）下沉到共享包：
  - schema 生成：`internal/skill`（`FunctionName` / `FunctionSchema`）；
  - 目录与选择：复用 `internal/chatcore.Catalog`（`CatalogEntry.IsSkill`、
    `SelectionOptions.ExposedSkills`、`SkillExposureOnly/Prefer`）。
- web agent chat 请求新增 `expose_skills: ["<name>"]`（与 aicli 的 explicit-mention /
  `skills-mode=only` 同语义），在回合工具面中注入对应 `skill__<name>` schema，
  并把 tool_call 派发到 `skill.Executor`。

## 5. 分阶段实施

### P0（本阶段，已实施）后端模型驱动执行模式

范围: `backend/internal/skill`、`backend/internal/api/skills`（透传 options，无契约变更）、
前端 `/skill` 携带 `options.execution_mode=model`。

改动点:
1. `executor.go`：新增 `execution_mode` 解析（`auto`/`model`，未知值回落 `auto`）；
   `Execute` 在 `model` 模式下跳过 Handler/Workflow 直执。
2. `executor.go#executeDefault`：`model` 模式默认开启工具循环；工具面取
   “声明 tools ∪ workflow steps tools”；无显式说明文档时注入 skill 程序清单投影。
3. `executor_test.go`：覆盖“模型自选工具执行 workflow 程序”“auto 模式行为不变”
   “程序清单注入内容”。
4. 前端 `use-composer-command-executor.ts`：`/skill` 执行携带
   `options: { execution_mode: "model" }`。

验收标准:
- `execution_mode=model` 下，workflow skill 不再直接执行 workflow；模型发起
  tool_call 后由 `executeSkillToolCalls` 执行对应程序，全流程可被 scripted provider 断言。
- `execution_mode` 缺省/`auto` 时，现有直执行语义与既有测试全部不变。
- 请求含 `tool_loop=false` 时不得被 `model` 模式强制开启（显式关闭优先）。

### P1（已实施）web 回合的 skill 说明注入（expose_skills）

实施口径调整（2026-09-18）：先交付"说明文档注入 + 模型直接选择程序"的路径。对 workflow
容器型 skill（如 `run_shell_command`），其程序本身就是运行时既有 MCP 工具（bash），
模型读到程序清单后直接调用即可；函数级 `skill__<name>` 派发留给 P1b，用于覆盖
"程序只有自定义 Handler、没有独立 MCP 工具"的 skill。

实现:
1. `internal/skill`：导出 `ProgramGuide` / `ProgramTools` 投影，与 `execution_mode=model`
   共用同一实现（`internal/skill/executor.go`）。
2. `internal/api/skills/handler.go`：AgentChat 请求新增 `expose_skills: []string`；命中时
   把说明与程序清单作为 system 消息注入 `contextMessages`（流式 ReAct 与非流式 ReAct
   两条路径）；显式暴露 skill 时强制 `enable_react=true`；未知 skill 快速 400。
3. 测试：`internal/api/skills/skill_exposure_context_test.go`（注入内容 / 去重 /
   未知名称失败 / 空输入 noop）。

验收标准:
- `/skill run_shell_command pwd` 作为普通回合提交后，模型上下文中出现
  "Skill program guide"，并可直接调用 `bash` 执行 `pwd`（工具行与输出进入消息流）。
- `expose_skills` 缺省时不改变任何既有行为与工具面。

### P1b（待实施）函数级 skill 派发（skill__<name>）

范围: `backend/internal/chatcore`、`backend/internal/agent`、`backend/cmd/aicli/commands`。

1. 下沉 `SkillFunction`/schema 生成到 `internal/skill` + `internal/chatcore.Catalog`，
   aicli 侧改为薄封装，避免双实现漂移。
2. 回合工具面注入 `skill__<name>` schema，tool_call 派发到 `skill.Executor`
   （handler / workflow 型 skill 语义完整保留）。
3. 观测：回合 metadata 写入 `skill_exposure`（复用 `BuildSkillExposureMetadata`），
   `--skills-debug` 与 web trace 共用同一投影。

### P2（已实施）前端 `/skill` 回合化 + 消息流渲染

实现（2026-09-18）:
1. `types/runtime/chat.ts`：`AgentChatRequest` 增加 `expose_skills?: string[]`（snake_case）。
2. `agent-chat-turn/turn-bootstrap.ts`：`prepareAgentChatTurn` 接受 `exposeSkills`，仅非空时
   写入请求体（缺省/空数组不发送，工具面零变化）。
3. `use-workspace-agent-chat-turn.ts`：`submitPrompt(options?)` 支持覆盖 prompt 与
   `expose_skills`，返回是否已启动（无会话 / 同会话在途返回 false）。
4. composer 执行器：`/skill` 优先走 `onRunSkillTurn` 回合回调（成功不再显示"执行成功"
   回执，消息在流里）；无回调宿主保留 `executeSkill` REST 回退（admin/debug）；
   空 prompt 保持弹窗/提示语义，提交失败复用 `composer.builtin.skill.failed`。
5. 接线：`workspace-shell/main-section.tsx` 从既有 `onSubmit` 构造
   `handleRunSkillTurn`（`{prompt, exposeSkills:[name]}`），`types.ts` 中 `onSubmit`
   签名放宽为可带 `AgentChatSubmitOptions`（向后兼容）。
6. 可读性修正（同日反馈）：回合的用户消息写成 `/skill <name> <args>`（`lib/composer-
   skill-options.ts:composerSkillTurnPrompt`，与输入框回填形态一致），线程里能直接
   看出这是 skill 调用；`skill.ProgramGuide` 同时声明该前缀只是调用标记，不是请求本体，
   避免模型把前缀当作 `{{prompt}}` 参数。

验证:
- `npx vitest run src/hooks/workspace`（44 files / 312 tests）全过；其中 composer 23 用例、
  turn-bootstrap 3 用例覆盖新语义与 body 映射。
- `npx tsc --noEmit` exit=0；`eslint`（改动文件）干净；`npm run lint:i18n` 扫描 815 项 0 违规。
- 已知既有问题（非本次引入）：`tsconfig.app.json` 下 `workspace-shell.tsx` 等 3 处历史报错。

验收标准:
- `/skill run_shell_command pwd` 在消息流中可见：模型说明 → tool 行 → 命令输出。
- 刷新/切换会话后消息不重复、不丢失（以权威历史为准，轨迹去重语义不变）。

### P3（可选）skill 文档与治理

1. 为 workflow skill 补充 `prompt.md`/`systemPrompt` 说明（`run_shell_command` 当前只有
   description），使“模型读文档”有真实文档可读。
2. `skills lint`/CI 校验：声明了 tools/workflow 的 skill 必须提供说明文档或自动生成程序清单。
3. 观测指标：模型驱动 vs 直执的占比、tool_loop 步数分布、失败原因分布。

## 6. 测试矩阵

| 层 | 用例 | 覆盖点 |
|---|---|---|
| `internal/skill` | `TestExecutor_ModelMode_*` | 模式解析、跳过 workflow、模型自选工具、步数上限 |
| `internal/skill` | 既有 `TestExecutor_Execute*` | auto 模式回归（不变） |
| `internal/api/skills` | `TestExecuteSkill_*` | options 透传、session 落库、权限/配额回归 |
| 前端 `skills.test.ts` | execute body 契约 | `session_id`/`options` 字段名 |
| 前端 composer | `/skill` 提交/回退 | 命令解析、错误提示 |

## 7. 风险与回滚

- **风险 1**：模型驱动引入 LLM 依赖与延迟。缓解：显式 `execution_mode=model` 才生效；
  `auto` 默认不变；步数上限封顶 32。
- **风险 2**：模型选错程序/参数。缓解：程序清单投影给出步骤与推荐参数；工具结果回灌允许
  自我纠正；保留 workflow 直执行作为确定性兜底（`execution_mode=workflow`/`auto`）。
- **风险 3**：双实现漂移（aicli `SkillFunction` vs web 注入）。缓解：P1 统一下沉到
  `internal/skill` + `internal/chatcore`。
- **回滚**：P0 仅新增选项，缺省行为不变；前端一行开关即可回退到直执行语义。
