# artifact_read 契约闭包与子代理工具面收敛 — 分析文档

- 日期：2026-10-18
- 状态：全部计划项（P0-0…P2-1、W1–W3）已实现并通过测试（见 §10.7）；D-1 经第三轮修正（**allowlist 未列出 ≠ 显式拒绝**，见 §9.1 / §5.2）
- 复查：2026-10-18 第二轮（提示继承链）、第三轮（主代理面与显式 deny 语义）已并入（见 §11，新增 F35–F48）
- 触发事故：批次 `batch_d41625fd477e057f`（4 任务，`failed (3/4)`，仅 `sr-link` 失败）
- 涉及层次：提示层 / 工具面授权层 / 失败账本层 / 存储数据层 / 注册表语义层
- 相关文档：
  - `docs/plan/tool-output-artifact-cascade-audit-and-optimization-plan-20260919.md`
  - `docs/plan/subagent-tool-surface-optimization-findings-plan-20260927.md`
  - `docs/tools/unified-tool-registry-plan.md`
  - `docs/tools/tool-policy-wildcards.md`

---

## 0. 摘要（TL;DR）

`sr-link` 的 8 份交付已经全部落盘并通过复核，但它仍被记为 failed。原因不是交付质量，而是它在收尾时调用了两个**不在其工具面里**的工具（`todos`、`artifact_read`），被白名单闸门拒绝，而这类拒绝在设计上**永远无法恢复**，于是把整个 run 的 `Success` 翻成 `false`。

排查过程中发现这不是单点缺陷，而是**五个层次各自独立的问题**叠加：

| 层 | 问题 | 性质 |
|---|---|---|
| L1 提示层 | 运行时在模型看不见某工具时，仍在提示文本里教它调用 | 提示与工具面脱钩 |
| L2 授权层 | `artifact_read` 同时是"输出契约读取端"和"普通可裁剪工具"，两种身份共用一个机制 | 机制错配 |
| L3 账本层 | 面外调用被记为不可恢复失败，否决已完成的交付 | 记账口径错误 |
| L4 数据层 | `Store.Get` 无 session 约束，而 `Store.Search` 有 —— 同层内语义不一致 | 数据访问不一致 |
| L5 语义层 | `Essential` 字段语义已于 2026-10-16 变更，但注释/文档未同步 | 语义腐烂 |

**核心结论**：`artifact_read` 不应该走 `Essential` 豁免（该字段已失效），也不应该简单地"自动补进白名单"。正确做法是把它升格为**契约性工具（Contractual）**——由输出契约授予，独立于用户 allowlist，同时把读取范围约束到调用者的 **lineage scope**。

**必须先说清楚的一件事**：`artifact_read` 已在三个角色族的默认工具表中（`role_defaults.go:17/25/38`），但**工具本身早就有精确 session 相等**的检查（W2 前，`artifact_read.go:129-137`）。所以"按 id 跨会话任意读取"并不成立。真正的缺口是：(a) `session_id` 为空时 fail-open；(b) fork 子代理读**父会话** artifact 被误拒。W2 已把这条检查升级为 lineage scope + fail-closed。因此"功能完整性"与"数据访问范围"是两个**独立**命题，本文档分开处理。

---

## 1. 事故与现象

### 1.1 现象

批次 `batch_d41625fd477e057f` 共 4 个任务，终态 `failed (3/4)`。唯一失败的是 `sr-link`：

- 它的 8 份交付**已全部落盘**，并已被复核；
- 失败原因不是产物问题，而是收尾阶段两次工具调用被拒：

```
tool not allowed for this agent: todos
tool not allowed for this agent: artifact_read
```

- 这两条拒绝来自 `loop.go:3287` 的白名单闸门（策略闸的文案不同，可排除）；父会话 trace 中**恰好只有这两条**拒绝记录；
- 处置结论：不重试、不重派发（`ClassifySubagentFailure` 判为 `tool_error` + `Retryable=false`）。

### 1.2 为什么"两次收尾拒绝"能否定 8 份交付

因为失败账本是**全量否决**语义：

```go
// loop.go:1200
result.Success = len(pendingFailures) == 0
```

任何未恢复的工具失败都会让整个 run 判红，即使交付物已经全部产出。

### 1.3 模型为什么会去调它们

两次调用的来源不同，但都**不是模型幻觉**：

1. **`todos`**：`prompt/environment_context.go:182` 在**文件编辑指引**里写了一条 todos 使用规则。该指引只在"有写工具可见"时渲染（`tool_guidance.go:71-76`），而 `sr-link` 是 implement 型子代理，必然有写工具 → 必然读到这条规则 → 自然调用 `todos`。而 `todos` 从来不在任何角色默认表中。

   > **2026-10-18 复查修正**：上述描述**低估了缺陷范围**。`BuildSubagentPrompt` 本身并不渲染文件编辑指引；`todos` 那一行是通过**父级 SystemPrompt 的逐字继承**进入子代理的（完整链路见 §3-L1 与 F35/F36）。这使问题从"某一行漏了门控"升级为"子代理继承父代理的工具面说明"。
2. **`artifact_read`**：`output/tool_result_content.go:26` 的指针文案是
   `read the full raw output via artifact_read(artifact_id=<id>, offset=<bytes>, limit=<bytes>)`，
   由 `modelArtifactNotice()`（同文件 `:994-1004`）追加到**任何带 artifact id 的结果**末尾，不做工具面判断。同样的点名还在 `executor/output_capture.go:550`（shell 截断标记）与 skill 折叠路径（`cmd/aicli/commands/skills_integration.go:800-803`）。

即：**提示层在邀请模型调用两个它调不到的工具**。

---

## 2. 事实清单（本文档所有结论的代码依据，均已逐条核实）

### 2.1 工具面与授权

| # | 事实 | 证据 |
|---|---|---|
| F1 | `Essential` 字段**已不再是 allowlist 豁免**；窄 allowlist 下未列出的 essential 工具同样被裁剪，字段仅供观测/目录 API/测试引用 | `backend/internal/toolregistry/registry.go:84-89` |
| F2 | 同文件 control 组注释仍写"运行时必需，allowlist 不可裁剪"；`todos` 标 `Essential: true` | `registry.go:171-172` |
| F3 | `artifact_read` **没有** `Essential` 标记 | `registry.go:161` |
| F4 | `artifact_read` 在**三个角色族的默认工具表**中均有 | `backend/internal/agent/role_defaults.go:17,25,38` |
| F5 | `todos` **不在**任何角色默认表中 | 同上文件，全文无 `todos` |
| F6 | 白名单闸门：不在 `allowedTools` 即拒绝，文案 `tool not allowed for this agent: <name>` | `backend/internal/agent/loop.go:3286-3292` |
| F7 | 已有"按**本轮冻结工具面**豁免"的先例：`search_tool` 仅当确实在当前工具面里才放行 | `loop.go:3264` + `backend/internal/agent/tool_list.go:48-54`（遍历 `frozenTurnToolSurface`） |
| F8 | 子代理工具面解析统一入口 `resolveChildToolSurface`；`tools_whitelist` 非 nil 时**完全覆盖**角色默认，nil 才回落到角色默认 | `backend/internal/agent/tool_vocabulary.go:210-244` |
| F9 | 部分收窄**静默**：`validateChildToolAllowlist` 只在"一个都没授到"时 fail-fast | `tool_vocabulary.go:70-84` |
| F10 | 已有"派发期路由警告"先例（目标需 shell 但面无 shell） | `tool_vocabulary.go:254-266` |
| F11 | 策略层 deny 恒优先于 allow，且支持通配 | `backend/internal/policy/tool_policy.go:22-29`、`:166-175` |
| F12 | 父子策略交集：`intersectAllowedTools`，父策略只能收窄不能放宽 | `tool_policy.go:590-600` |
| F13 | 只读过滤 `filterReadOnlyTools` 只删写类工具；`artifact_read` 声明为 `ToolKindRead`，不受影响 | `backend/internal/agent/scheduler.go:2183`、`backend/internal/toolkit/tools/artifact_read.go:74-84` |
| F14 | essential 豁免移除已被测试钉死（`search_tool` 在窄 allowlist 下被 gate） | `backend/internal/policy/tool_policy_test.go:81-99` |

### 2.2 失败账本与分类

| # | 事实 | 证据 |
|---|---|---|
| F15 | 任何带 `Error` 的工具结果都进 `pendingFailures` | `loop.go:1467-1478` |
| F16 | 消账条件：**同名工具后续成功**。被白名单拒绝的工具永远不可能成功 → 永久未恢复 | `loop.go:8313-8323` |
| F17 | `Success = len(pendingFailures) == 0`；失败文本拼进 `result.Error` | `loop.go:1200`、`:1209-1211` |
| F18 | 拒绝路径仍走 gateway 并落 `tool.completed(denied=true)`，但 `result.Error` 保留 | `loop.go:3851-3881` |
| F19 | 失败分类：`tool not allowed for this agent: ...` → `tool_error`、`Retryable=false`、建议 `complete_locally_or_change_tool` | `backend/internal/agent/subagent_retry.go:24-38,76-77`、`subagent_retry_test.go:160-168` |
| F20 | 子代理终态由 `report.Error` 分类，再落到 batch 的 failed 计数 | `backend/internal/agent/scheduler.go:1286-1293` |
| F21 | 拒绝文本升级只覆盖 read-only 拒绝；白名单拒绝**原样透传** | `backend/internal/agent/denial_guidance.go:33-45` |

### 2.3 提示与渲染

| # | 事实 | 证据 |
|---|---|---|
| F22 | `todos` 规则写在**文件编辑指引**内，随该段一起渲染 | `backend/internal/prompt/environment_context.go:182` |
| F23 | 文件编辑指引的门控只有"有写工具可见"一个条件 | `backend/internal/prompt/tool_guidance.go:71-76` |
| F24 | 指针文案无条件点名 `artifact_read` | `backend/internal/output/tool_result_content.go:23-26`、`:994-1004` |
| F25 | shell 截断标记同样点名 `artifact_read` | `backend/internal/executor/output_capture.go:550` |
| F26 | 静态载荷（skill 正文）声明 `FoldRecoveryArtifactRead`——它的被折叠尾部**只能**靠 artifact_read 取回 | `backend/internal/toolresult/kind.go:171-197`、`cmd/aicli/commands/skills_integration.go:800-803` |
| F27 | 可重发类工具（view/grep/shell）的默认恢复提示是"用更窄窗口重发"，与 artifact 指针是两条并行提示 | `toolresult/kind.go:171-180`、`output/tool_result_content.go:1087-1095` |

### 2.4 存储与数据访问

| # | 事实 | 证据 |
|---|---|---|
| F28 | `Store.Get(id)` **只按 id 查询，不校验 session** | `backend/internal/artifact/store.go:302-351`（SQL 见 `:315-319`） |
| F29 | `Store.Search` **按 session 隔离** | `store.go:375`、`:385` |
| F30 | 记录里**存有** `session_id` 字段（写入时落库，Get 时也读出来了，只是没用于校验） | `store.go:274-277`、`:326-334` |
| F31 | 子代理用**独立** `childSessionID` 运行工具循环 | `scheduler.go:1043`、`:1102` |
| F32 | 子代理配置整体拷贝父级（含 `ArtifactStorePath`）→ 继承同一个 store 路径 → 同一份 SQLite | `child_factory.go:67`、`backend/internal/agent/agent.go:1070-1092` |
| F33 | store 按路径懒打开；同路径的不同实例访问同一数据库 | `store.go:97-120` |
| F34 | lineage 概念已存在：`runLineage{RootSessionID,...}`、batch 的 `RootScopeID`（空则归一到 parent） | `loop.go:232-237`、`backend/internal/agent/batch_lifecycle.go:16-17,52-53` |

### 2.5 子代理 prompt 继承与运行时归属（2026-10-18 第二轮复查新增）

| # | 事实 | 证据 |
|---|---|---|
| F35 | `BuildSubagentPrompt` **逐字继承** `parent.SystemPrompt` 作为子代理提示的第一段 | `backend/internal/agent/prompt_builder.go:23-25` |
| F36 | 父会话的 `Config.SystemPrompt` 是**组合提示**，其中文件编辑指引按**父级** facts 渲染 | `backend/cmd/aicli/commands/chat_actor_host.go:2618`、`backend/cmd/aicli/commands/chat_session.go:2726-2732` |
| F37 | `BuildSubagentPrompt` 以**无条件**变体渲染 parallel guidance（不接 facts），且不渲染文件编辑指引 | `prompt_builder.go:57` |
| F38 | 子代理提示同时打印 `Allowed tools: ...`，与继承来的父级指引**互相矛盾**（同一条提示里既列了自己的工具，又教它用父级的工具） | `prompt_builder.go:85-93` |
| F39 | `FactsFromPolicy(policy)` 可用**任意策略**调用；策略未收窄时返回 nil（保持 legacy 无条件渲染，向后兼容） | `backend/internal/prompt/toolguidance/toolguidance.go:23-33` |
| F40 | `loopRunOptions.RootSessionID` 由**宿主**注入，scheduler 不发；子代理运行时 root 为空，仅分析侧回溯兜底 | `backend/internal/agent/scheduler.go:1100-1108`、`loop.go:262-267` |
| F41 | 只有**只读**拒绝有连击熔断（`readOnlyDenyStreak` → Escalate 3 / HardStop 5）；**面外拒绝无任何连击护栏** | `loop.go:208-214`、`:1343-1357`、`:3947-3958` |
| F42 | scheduler 走 `ChildAgentFactory.Build`，child_factory 从 `parent.GetConfig()` 取 SystemPrompt 交给 `BuildSubagentPrompt` | `scheduler.go:982-988`、`child_factory.go:41,126` |
| F43 | 网关对**失败结果恒归档**（无论多小）；成功结果仅超过阈值才归档——阈值按工具自身预算（shell/view/grep 32 KiB，glob/ls 16 KiB） | `backend/internal/output/gateway.go:141-168`、`:320-332` |
| F44 | 渲染层在结果**失败或截断**时追加 artifact 指针；未截断成功不追加 | `backend/internal/output/tool_result_content.go`、`artifact_read_no_cascade_test.go:48-89` |
| F45 | 主代理请求面同样被 `filterPolicyBlockedToolDefinitions` 按 `policy.AllowsDefinition` 裁剪 | `loop.go:4410`、`:4761-4778`、`tool_policy.go:550-553` |
| F46 | 子父 allowlist 交集 `intersectAllowedTools`：子代理永不得宽于父策略 | `tool_policy.go:1181+` |
| F47 | `AllowTool` 判定顺序为 deny → BlockDelegation → allowlist → capability → read-only；历史上 essential 豁免正是插在这里被移除的 | `tool_policy.go:118-134` |
| F48 | `artifact_read` 只要求 `CapReadOnly`；capability floor 恒含 `CapReadOnly` → capability scope **不会**拦它，只有 allowlist 会 | `capability.go:87-105`、`capability_scope.go:134` |
| F49 | **通配与闸门口径不一致**：`AllowedToolNames()` 原样返回模式，但 `whitelistSet` / `computeAvailableTools` / `fullCatalogForSearch` 都是**精确匹配** | `tool_policy.go:644-658`、`loop.go:5820-5830`、`:4330-4337`、`tool_list.go:154-160` |
| F50 | runtimeapi 把 **difficulty + collaboration guidance 写进 `Config.SystemPrompt`**（会被子代理继承）；file-editing / shell guidance 则是独立 system message（不会经由 SystemPrompt 继承） | `backend/internal/api/runtimeapi/instruction_messages.go:141-143,177-183`、`handler.go:2200-2201`、`agent_context.go:25-35` |
| F51 | 权限引擎的静态策略阶段直接调用 `Policy.AllowTool` → 契约性授予在引擎路径下同样生效（无"已广告但执行被拦"的第二套口径） | `engine.go:591-601` |
| F52 | fork 快照**剥离全部 system 消息**、保留 assistant/tool 消息 → 父级指针可能出现在子级历史中，scope 含父是必要的 | `subagent_context.go:42-54`、`:56-72` |

---

## 3. 问题分层分解

五个层次的问题**互相独立**：即使修好其中四个，剩一个仍会复现同类事故。下面逐层说明"哪里错了"和"为什么单修这一层不够"。

### L1 提示层：提示与工具面脱钩

**缺陷**：提示文本的生成完全不知道自己面对的是哪个工具面。

- `todos` 那一行（F22）的**门控谓词是错的**：它被"有写工具"门控，但它的**主语是 `todos`**（F23）。这是"条件渲染块里塞了无条件断言"的典型错误。
- **（复查新增，主要缺陷）子代理逐字继承父代理的系统提示**，而该提示里的引导段落是按**父级**工具面渲染的：

  ```
  chat_actor_host.go:2618   cfg.SystemPrompt = composeLocalChatSystemPrompt(...)  ← 按父级 facts 渲染
          ↓
  scheduler.go:982 → child_factory.go:126   BuildSubagentPrompt(parentConfig, promptTask)
          ↓
  prompt_builder.go:23-25   lines = append(lines, parent.SystemPrompt)         ← 逐字继承
          ↓
  environment_context.go:182   "When using `todos`, ..."                       ← 父级指引里的 todos 行
  ```

  于是子代理的系统提示里同时存在两段互相矛盾的话：继承来的"父级工具面说明"与 `prompt_builder.go:85-93` 自己打印的 `Allowed tools: [...]`（F38）。
  这不是某一行的门控问题——**是提示的所有权问题**：涉及工具面的指引必须由"知道自己工具面"的那一层渲染。
- `prompt_builder.go:57` 用**无条件变体**渲染 parallel guidance（F37），即使子代理没有可并行读工具也会出现——同一类缺陷的第二个实例。
- `artifact_read` 的指针（F24/F25）**没有任何门控**，因为它产生于 `output`/`executor` 包，而工具面信息在 `agent` 包——两者之间**没有参数通道**。

**为什么单修不够**：
- 只加门控 → 门控用的是**父级** facts，子代理照样继承（F35/F36），本事故不会消失；
- 只改提示也不够 → 未来任何新增的提示/通知/折叠路径都可能再次漏判。门控是**逐个补**的，而契约是**一次定义**的。

**注意**：`todos` 与 `artifact_read` 在这一层的性质**不同**——
- `todos`：模型**不需要**它（子代理不承担控制面职责）→ 正确修法是**不要教它**；
- `artifact_read`：模型**需要**它（F26 证明静态载荷的尾部只能靠它取回）→ 正确修法是**保证它可用**。

把这两者用同一个机制（比如"都豁免"）处理是错的。

### L2 授权层：契约性能力与可裁剪工具混用同一机制

**缺陷**：allowlist 是**硬闸门**（F1/F6/F11/F12，这是 2026-10-16 的明确产品决定，应当保留），但"输出契约的读取端"在结构上**必须恒可用**（F26）。

两种诉求落在同一个机制里，导致：
- 补进白名单 → 违背"profile 完全控制工具面"的决定；
- 不补 → 输出契约断链（本事故）；
- 加 `Essential` → 无效（F1 已使该字段失去豁免语义）。

**为什么单修不够**：这不是"要不要豁免一个工具"的问题，而是**缺少一个区分"用户可裁剪工具"与"契约性能力"的维度**。没有这个维度，任何工具只要同时具备"结构必需"和"用户可见"两种属性，就会重演同一困境。

### L3 账本层：面外调用否决已完成交付

**缺陷**：三层叠加——

1. 任何工具错误都进 `pendingFailures`（F15）；
2. 消账只认"同名工具后续成功"（F16），而被白名单拒绝的工具**永远不可能成功**；
3. `Success = len(pendingFailures) == 0`（F17）→ 全量否决。

即：**一个在结构上不可恢复的记账项，被用来否决整个 run 的成功判定**。

**为什么单修不够**：即使 L1/L2 都修好，历史回放（fork 会拷贝父级对话，F31/F32 的 lineage）、MCP 工具名漂移、skill 展开后的名字变化，仍可能产生面外调用。**系统必须对"模型调用了它看不见的东西"这一类事件有正确的默认处置**，而不能依赖上游永不出错。

### L4 数据层：`Get` 与 `Search` 的隔离语义不一致

**缺陷**：同一个 store 里两种读取语义（F28/F29）：

- `Search` 按 session 隔离；
- `Store.Get` 只按 id，**不校验 session**，而且 `session_id` 明明已经查出来了却没有用于校验（F30）。

但工具层并非完全没有防线：W2 之前 `artifact_read` 已有一道**精确 session 相等**的检查（`artifact_read.go:129-137`）。所以准确的定性是：

- 跨会话读取在**两个 id 都非空**时会被拒绝；
- `session_id == ""` 时 fail-open（旧数据可被任何会话读取）；
- fork 子代理读**直接父会话** artifact 会被误拒（历史里保留了父级 tool 消息，F52）。

W2 已用 `GetOwned` + `ReadScope{self, parent}` 取代这段手写检查：父会话可读、无关会话拒绝、空 owner fail-closed。

因此这条要单独定性：

- 它是一个**既有的、与本次事故无关的**隔离不一致；
- 修复 L2（契约授予）会把它从"所有角色默认子代理"**扩大到"所有子代理"**，所以**必须与 L2 一起评估**，不能分开排期；
- 但它**不是**本事故的根因，不应被当作本事故的"解决方案"。

**为什么单修不够**：即使加上 session 校验，也不解决"契约断链"和"判红"问题；反过来，只做 L2 而不做 L4，会静默扩大数据可达面。

### L5 语义层：`Essential` 语义腐烂

**缺陷**：同一个文件里两处注释互相矛盾（F1 vs F2）；`docs/tools/unified-tool-registry-plan.md:135` 也仍写着 `Essential bool // 控制面工具：allowlist 不可裁剪`。

**危害**：这不是文档洁癖。下一个遇到同类问题的人，读到"不可裁剪"就会去给工具加 `Essential: true`——而那个动作**不会有任何效果**，只会浪费一轮排查。本次排查中就实际走到了这一步。

---

## 4. 关键结论：`artifact_read` 的双重身份

这是整个问题的枢纽。`artifact_read` 同时承担两种语义，而当前实现把它们**合并成同一个工具、同一个实现、同一条无约束的 `Get`**：

| 身份 | 性质 | 正确的约束 |
|---|---|---|
| **输出契约的读取端** | 结构必然性。网关对任何结果都可能折叠并产出指针（F24-F26），指针若无读取端即为死链 | 应当**恒可用**（对任何产生过指针的主体） |
| **任意 artifact 的按 id 读取器** | 数据访问。可在同一 store 内跨会话命中（F28/F30/F33） | 必须**受主体范围约束**（lineage scope） |

由此得出三条设计结论：

**结论 1：不能在 allowlist 上做二选一。**
"补"违背授权模型，"不补"破坏契约。二者都只处理了其中一种身份。

**结论 2：不能使用 `Essential`。**
该字段已被明确降级为观测语义（F1），它不再影响可用性。任何依赖它的修法都是无效动作。

**结论 3：必须让读取能力与授权边界绑定，而不是解除授权边界。**
即：**读取权 = 指针所有权**。一个主体能解引用的，应当是"它自己执行面（lineage）产生过"的 artifact。这条不变量同时满足两端：

- 输出契约永远闭合——自己的输出永远读得回；
- 不引入新的跨主体通道——别人的输出（其他 lineage/session）读不到。

### 4.1 关于"应不应该总是可用"的正面回答

问：执行环境依赖 `artifact_read`，却不带这个工具，会不会破坏 agent 运行的功能完整性？

答：**会，而且这不是可接受的降级**。理由：

1. 被折叠的尾部**真的会丢**——对静态载荷（skill 正文，F26）而言，重发工具调用返回同一份文档，`artifact_read` 是**唯一**的取回路径；
2. 折叠对模型是**不可见**的——模型不会知道自己看到的是残缺输出，会把它当成完整证据使用，产生**静默的错误结论**，比报错更危险；
3. 指针是运行时**主动写进模型上下文**的（F24），即运行时代自身承诺了可取回性。承诺了又拿掉，是运行时的不一致，不是使用者配置错误。

所以正确方向是：**让 `artifact_read` 作为契约性能力恒可用，而不是让提示去回避它**。（"提示降级"只能作为契约被显式拒绝时的兜底安全网，不能作为主方案——见 §6 备选方案 C。）

---

## 5. 设计方案

### 5.1 不变量（先定义目标，再谈实现）

| 编号 | 不变量 | 说明 |
|---|---|---|
| **I1 契约闭合** | 凡运行时在自己的模型上下文里写入了 `art_…` 指针的主体，都必须具备解引用它的能力 | 输出契约不可断链 |
| **I2 授权最小** | 解引用范围不超过调用者的 lineage scope（自己 + 可追溯祖先；v1 = 直接父级） | 不引入跨主体读通道 |
| **I3 面外调用不判红** | 调用"本就不在模型可见面上"的工具，必须被记录与观测，但不得否决已完成交付 | 账本只记真实失败 |
| **I4 提示跟随工具面** | 提示/通知中不得仅因条件成立就提及模型调不到的工具 | 提示与面同源 |
| **I5 单一真源** | "必需/契约性"名单只有一处定义，且语义唯一 | 消除第二份名单与语义腐烂 |
| **I6 显式优于隐式** | allowlist 的最终形态（含契约性追加）必须可观测、可审计，不得静默改写 | 见 §5.7 |

**取舍原则**：I1 与 I2 冲突时，优先 I2（安全），但必须让 I1 的破坏**显式可见**（指针文案降级 + 观测字段），不能静默丢数据。

### 5.2 授权层：引入"契约性工具"维度

在注册表中增加一个与 `Essential` **语义不同**的维度：

```go
// backend/internal/toolregistry/registry.go
type Entry struct {
    // ...既有字段...

    // Essential 仅观测语义（2026-10-16 起不再是 allowlist 豁免）。保留字段供
    // toolsurface metadata / 目录 API / 测试引用。
    Essential bool

    // Contractual 表示该工具是某条运行时契约的组成部分（输出契约、回执契约等），
    // 由运行时按契约授予，不参与用户 allowlist 的裁剪决策；但显式 deny 恒优先。
    // 与 Essential 的区别：Essential 不影响可用性，Contractual 影响可用性，
    // 但授权来源是契约而非用户名单。
    Contractual bool
}
```

`artifact_read` 标为 `Contractual: true`；`todos` **不标**（它不是契约的一部分，见 L1）。

**授予点必须在策略层，而不是只放在子代理解析层（2026-10-18 第三轮修正）**

原方案只改 `resolveChildToolSurface`，**覆盖不到主代理**。实测三条面装配路径都会丢弃它：

| 路径 | 裁剪点 | 证据 |
|---|---|---|
| 主代理请求面 | `loop.go:4410` → `filterPolicyBlockedToolDefinitions` → `policy.AllowsDefinition` | `loop.go:4410,4761-4778`、`tool_policy.go:550-553` |
| 子代理请求面 | 同一过滤 + `resolveChildToolSurface` 的 allowlist 收窄 | F8、F46、F47 |
| 子父交集 | `intersectAllowedTools`：子代理永不得宽于父策略 | `tool_policy.go:1181+`（F46） |

因此正确做法是把契约性工具放进 **`ToolExecutionPolicy`**，让三条路径自动一致：

```go
// backend/internal/policy/tool_policy.go
type ToolExecutionPolicy struct {
    // ...
    // ContractualTools 是"由运行时契约授予"的工具集合（当前仅 artifact_read）。
    // 判定顺序：DeniedTools（恒优先）→ ContractualTools → BlockDelegation → allowlist。
    // 它不是 Essential 豁免的复活：只对注册表声明 Contractual 的少数工具生效，
    // 且显式 deny 可以关闭；用于保证"运行时代自己写进上下文的指针必可解引用"。
    ContractualTools map[string]bool
}
```

`AllowTool` 的判定顺序（`tool_policy.go:118-134`）改为：

```
DeniedTools（恒优先，通配） → ContractualTools → BlockDelegation → allowlist（含通配） → capability → read-only
```

配套要求：

1. `NewToolExecutionPolicy` / profile overlay / `DeriveChildForTask` 从 `toolregistry.Entry.Contractual` 填充该集合（单一真源，不新建名单）。
2. `intersectAllowedTools` 必须保留父级已授予的契约工具（否则子代理交集又会丢掉它）。
3. `resolveChildToolSurface` 仍要把名字追加进 `task.ToolsWhitelist`——因为执行闸门用的是 `allowedTools` 映射（`loop.go:3286`），而且 prompt 的 `Allowed tools:` 也要与实际面一致（F38）。
4. **capability 不需要额外授予**：`artifact_read` 只要求 `CapReadOnly`（taxonomy/启发式都会落到它），而 capability floor 恒含 `CapReadOnly`（`capability_scope.go:134`），所以不会被 capability scope 拦下。

**D-1（2026-10-18 修正并取代上一版确认）：allowlist 未列出 ≠ 显式拒绝**

上一版把"父策略 allowlist 不含 `artifact_read`"判为显式选择、不追加。这个判定**必须推翻**，理由：

1. **它会静默丢数据**，而且发生在最常用的工具上：`view`/`grep`/`shell` 的大输出会折叠，**失败结果无论多小都会归档并带指针**（`gateway.go:141-168`，F43）；`edit` 失败同样会带指针。profile 作者写 allowlist 时想的是"业务工具"，不会想到还要单列一个读取器。
2. **allowlist 的语义是"允许哪些动作"，不是"是否承认输出契约"**。用它来表达后者，是把两个正交维度塞进一个字段。
3. **运行时代自己发出了指针**（而不是用户请求了它）——运行时代不能承诺一个自己已经拿掉的能力。

修正后的判定：

| 条件 | 处置 |
|---|---|
| `DeniedTools` 命中 `artifact_read`（含通配） | **不授予**（deny 恒优先）；profile 加载期发警告；渲染层必须降级（§5.4b） |
| 非空工具面且未 deny | **授予**——包括 allowlist 未列出、`tools_whitelist` 未列出的情况 |
| 空工具面 / `tools_whitelist: []` | 不授予（没有输出，也没有指针） |
| `disable_tools` 生效 | 不授予（执行侧已整体关闭） |

**与 2026-10-16 决定的关系**：该决定的核心是"profile 必须能完全控制工具面"。本规则保留了这一控制——只是把控制点从"必须记得在 allowlist 里列出 artifact_read"改成**显式 deny**。这是**单一工具、deny 优先、可审计**的窄豁免，不是旧的全局 `Essential` 豁免复活；`search_tool` 等其它工具不受影响（T11 守住）。

**安全前提**：该授予只有在 §5.3 的 `GetOwned` 同时上线时才成立——否则等于给所有代理开放按 id 跨会话读取。二者**必须同批上线**（§7.1）。

> **注意**：`tools_whitelist: []`（显式禁用全部工具）的文档契约必须保持不变——空面不产出任何结果，也就没有指针，因此无需追加。

**执行顺序约束（实现时必须遵守）**：

1. `[]` / `disable_tools` 判定优先：直接返回，不进入任何追加逻辑；
2. 追加必须在 `validateChildToolAllowlist`（`tool_vocabulary.go:229`）**之后**——否则会给"请求了但一个也没授到"的失败场景补上工具，破坏 fail-fast 语义（F9）；
3. 追加必须在只读过滤（`filterReadOnlyTools`）之后——`artifact_read` 本身是 read-only，不会被删（F13），但顺序固定可避免后续演进踩坑；
4. 追加结果写回 `task.ToolsWhitelist`，且必须放在 `task.ToolsWhitelist = child.AllowedToolNames()`（`tool_vocabulary.go:240`）**之后**——否则会被该赋值覆盖；同时保证 `childPolicy` 与之同步（照抄 `child_factory.go:97-104` 的重新派生做法）。

### 5.3 数据层：`GetOwned` 与 lineage scope

**新增带主体的读取方法**（不改变既有 `Get` 的对外行为，避免破坏宿主/诊断/TUI 路径）：

```go
// backend/internal/artifact/store.go
// GetOwned 读取一条 artifact，并校验调用主体是否有权解引用。
// scope 为空表示"无主体约束"（宿主/TUI 直连等既有路径），保持向后兼容。
// scope 非空时，仅当记录的 session_id 属于 scope 才返回。
func (s *Store) GetOwned(ctx context.Context, id string, scope ReadScope) (*Record, error)
```

```go
// ReadScope 是解引用授权范围：允许的 session id 集合。
// 由 agent 循环按 lineage 构造：{当前 session, 父 session, root session}。
type ReadScope struct {
    SessionIDs map[string]bool
    // Unrestricted 为 true 时不做主体校验（宿主/诊断路径）。
    Unrestricted bool
}
```

SQL 形态：

```sql
SELECT ... FROM artifacts
WHERE id = ? AND (? = 1 OR session_id IN (...))
```

**scope 的来源**：`runLineage` 已存在且 `loop.run` 已能拿到（`loop.go:232-237`）；但**运行时 `RootSessionID` 通常为空**——`scheduler.go:1100-1108` 只传 `ParentSessionID` 与 `SubagentID`，`loopRunOptions.RootSessionID` 由宿主注入，注释明说 root "由采集侧按父链回溯兜底"（F40）。因此：

- **v1 scope = {当前 session, 直接父 session}**，不依赖 root；
- 绑定通道沿用 `toolctx`（`loop.go:5539-5544` 已有 artifact store 的绑定点）；
- **已知边界**：depth≥2 的嵌套子代理无法读到祖父链路的 artifact。由于 fork 快照只取**直接父级**的已完成轮次，v1 已覆盖现有 fork 语义；若未来支持跨层 fork，需把 `RootScopeID` 线到 `SubagentRunOptions`（概念已存在于 `batch_lifecycle.go:16-17`）。

**为什么是 lineage 而不是精确 session**：
- 子代理有独立 session id（F31），但它需要读自己产生的 artifact → 必须包含自己；
- fork 场景下子代理历史可能携带父级 tool 消息（Q1 已关闭：`stripForkSystemPrefix` 只剥 system，tool 消息保留）→ **必须**包含父；
- 父代理读子代理 artifact 的场景**不需要**宽 scope：子代理回传的是聚合报告，其指针由父级网关在**父 session** 下重新归档（网关按处理侧 session 写记录）。

**迁移成本**：不需要 schema 迁移——`session_id` 已在记录里（F30），只需在读取路径上加校验。

**兼容性**：
- 宿主/TUI/诊断路径继续用 `Get`（`Unrestricted`），行为零变化；
- `artifact_read` 工具改为调用 `GetOwned`，并从 ctx 读取 scope；ctx 无 scope 时回落到 `Unrestricted`（与今天行为一致），保证灰度安全。

---

### 5.4 提示层：门控与兜底

**(a) `todos`：不要教它（不豁免）**

把 `environment_context.go:182` 那一行从 `RenderFileEditingGuidance` **移出**，改为受 facts 门控的独立段落：

```go
// prompt/tool_guidance.go
var todosGuidanceTools = toolregistry.CallableNames("todos")

func RenderTodosGuidanceFacts(facts *ToolGuidanceFacts) string {
    if !facts.any(todosGuidanceTools...) {
        return ""
    }
    return RenderTodosGuidance()
}
```

调用点：`internal/api/runtimeapi/agent_context.go:80`、`chat_session.go:2748+`（两处均已有 facts 可用，见 F23 所在文件）。

> 为什么不是"把 todos 补进白名单"：子代理不承担控制面职责。控制面工具属于父代理；给子代理补 `todos` 只会让它把 token 花在与交付无关的任务列表维护上。

**(a2) 子代理提示所有权：不得继承父级的工具面指引（复查新增，本事故的真实路径）**

仅做 (a) **修不好本事故**：子代理继承的是父级已渲染好的提示（F35/F36），渲染门控用的是父级 facts，而父代理有写工具 → 段落照样渲染 → 照样继承。必须同时改提示的所有权：

1. **子代理不再继承带工具面指引的父级文本**。可选实现（择一，实现时定稿）：
   - 组合提示时把"非指引部分"（profile 文本 + 环境上下文）与"指引部分"分开保存，子代理只取前者；或
   - 在 `internal/prompt` 提供 `StripSurfaceGuidance(prompt) string`，按已知段落标题（`Shell guidance:` / `File editing guidance:` / `Parallel tool guidance:` / `Task difficulty rating ...` / `Multi-agent collaboration guidance:`）剥离。
2. **由子代理自己按子策略渲染**：`FactsFromPolicy(childPolicy)`（F39，可直接用子策略调用；未收窄时返回 nil = 保持 legacy 行为）→ 渲染 parallel / file-editing 等段落。都放在 `BuildSubagentPrompt` 内，因为那里**知道子代理的工具面**。
3. `prompt_builder.go:57` 的无条件渲染改为 facts 变体（F37）。
4. `prompt_builder.go:85-93` 已打印 `Allowed tools: [...]`（F38）——修正后两段不再矛盾，保留它作为权威声明。

> 验证方式：构造"父级有写工具、子代理白名单只有 shell"的场景，断言子代理提示**不含** `todos`，且 `Allowed tools` 与实际面一致。

**(b) `artifact_read` 指针：按契约授予为主；显式 deny 时必须按工具类别正确降级**

主路径：§5.2 之后指针**总是可取回**（除非被显式 deny），无需改文案。

兜底路径（仅当 `DeniedTools` 显式拒绝时）：**绝不能继续输出一个指向不可调用工具的指针**。按工具类别分别处理：

| 结果类别 | 判据 | 降级方式 |
|---|---|---|
| **可重发类**（view/grep/shell/ls/glob） | 工具可再用更窄窗口重发 | 使用既有的 narrower-window 恢复提示（`toolresult/kind.go:174-180`、`output/tool_result_content.go:1087-1095`），**不输出 artifact 指针** |
| **静态载荷**（skill 正文等，声明 `FoldRecoveryArtifactRead`） | 重发返回同一文档，无法缩小 | 在硬上限内**直通**（不折叠）；超限时输出显式标记：`tail omitted and unrecoverable in this profile (artifact_reader denied)` |
| **失败结果**（网关对失败恒归档，F43） | 指针兼作恢复提示 | 保留错误正文；指针替换为 `artifact_unreadable: true` 元数据 + 明示"尾部不可取回"；不得指向不可调用工具 |
| **成功且未截断** | 无指针 | 不变 |

实现上需要给渲染层一个"读取端是否可用"的入参：

```go
// output/tool_result_content.go
// RenderOptions 承载"当前工具面是否可解引用 artifact"这一事实。
// 默认（零值）保持既有行为：可见。TUI/CLI 直连路径不传即为可见，行为零变化。
type RenderOptions struct {
    ArtifactReaderAvailable bool // true = 保留既有指针文案
    ...
}
```

不可见时的通用中性文案（可重发类会替换为 narrower-window 提示）：

```
raw output archived as art_<id> (<N> bytes, first <M> bytes shown above);
the artifact reader is not available in this tool surface — the omitted tail
cannot be retrieved in this run. Report partial evidence explicitly.
```

同样的处理适用于 `executor/output_capture.go:550` 与 skill 折叠路径（F25/F26）。

> 关键区别（相对上一版被否决的方案）：这里的降级**只是显式 deny 路径的安全网**；主路径由 §5.2 保证契约闭合。**不得**把降级当作解决"功能完整性"的手段——它只能保证"不骗模型"，不能保证"数据不丢"。

**profile 加载期校验**：若 `DeniedTools` 命中 `artifact_read` 且工具面非空，加载时发一条明确警告（并写入 `toolsurface`）：`artifact_reader_denied: folded outputs will be unreadable; re-enable unless this is intentional`。

### 5.5 账本层：面外调用与真实失败分离

在 `loop.go:1467-1478` 增加判定，"面外调用"**不进** `pendingFailures`：

```go
// 判据：错误文本是白名单闸文案，且被拒工具不在本轮冻结工具面内。
// 复用 F7 的既有判据（frozenTurnToolSurface），不新建第二套口径。
if isUnadvertisedToolDenial(toolResult, frozenTurnToolSurface(ctx)) {
    // 仍然：计入 toolErrorCount、发 tool.denied(surface_mismatch=true)、
    //      给模型一条"该工具不在你的工具面内，不要再调用"的回执。
    // 但不进 pendingFailures → 不影响 Success。
} else {
    pendingFailures = append(pendingFailures, ...)
}
```

**保留的严格性**：如果工具**在**面上却被策略拒绝（read-only / capability / 权限），**仍按失败处理**——那是真实的边界越界，且 `readOnlyDenyStreak` 的升级逻辑（`denial_guidance.go:28-31`）依赖它。区分二者的唯一谓词就是"是否出现在本轮冻结工具面里"。

**判据修正（实施时发现）**：不能把所有面外调用都判为非致命。只读 verifier 调用 `write_file` 时，该工具同样"不在面上"，但这是**真实的只读边界越界**，必须保持致命——否则 verifier 会静默成功、patch decision 被错误放行。因此：

- **只读类工具**（`todos`、`artifact_read`、`goal` 等）的面外调用 → 非致命 + 连击熔断；
- **写 / shell / 委派 / 编排类工具**的面外调用 → 仍走 `pendingFailures`（真实边界越界）。

这也让本方案与只读边界设计保持一致，而不是用"面外"概念把它绕过去。

**前置条件（复查新增）：统一"已暴露"判据**

当前有两套不同的集合在描述"模型能看到什么"：闸门用 `allowedTools`（来自 `options.ToolWhitelist`），而 search 豁免用 `frozenTurnToolSurface`（F7）。若二者不一致，`isUnadvertisedToolDenial` 会误判。因此 P0-1 必须先抽出**单一判据**：

```go
// 唯一口径：本回合该工具是否真的出现在模型可见的工具定义里。
func toolAdvertisedInTurn(ctx context.Context, name string) bool
```

`searchToolVisibleInTurn`（`tool_list.go:48-54`）改为调用它，闸门侧判据也用同一个函数。这同时也是 §6.2 第 5 条（与 search 豁免合并 helper）的落地。

**护栏（复查新增）：面外拒绝连击熔断**

把面外拒绝移出失败账本后，理论上存在"模型反复调面外工具 → 永不计失败"的漏洞。现有熔断只覆盖只读拒绝（F41），面外拒绝**无任何护栏**。因此需照抄 `readOnlyDenyStreak` 的模式（`loop.go:208-214`、`:1343-1357`）：

- 新增 `surfaceMismatchStreak`：连续面外拒绝计数，任一工具执行成功即清零（照抄 `resetReadOnlyDenyStreakOnSuccess` 的收敛规则）；
- 达阈值（建议 3）时在回执里升级为强提示；达硬停阈值（建议 5）时终止 run，`LimitReason="surface_mismatch"`；
- 该终止**属于**真实失败（`Success=false`）——熔断的目的是把"无意义的反复试错"变成一次可诊断的终止，而不是让它无限空转。

**可观测性（复查新增）**：不进 `pendingFailures` ≠ 不可见。需新增独立计数器（如 `SurfaceMismatchCount`）并进入 run 结果与报告，否则现有口径（`ToolErrorCount` / `UnrecoveredToolErrorCount`）要么漏计、要么污染，质量分析将看不到"提示泄漏"的真实频度。

**同时改进拒绝文案**（F21：白名单拒绝目前原样透传）：给面外拒绝一条可执行回执，例如
`this tool is not available in your current tool surface; do not call it again and finish with the tools you have`。

### 5.6 语义层：清理 `Essential` 的语义腐烂

二选一（建议前者）：

**方案 A（推荐）**：`Essential` 保持"仅观测"语义，**修正注释**：
- `registry.go:171` 的 `// ---- 控制面（control 组；运行时必需，allowlist 不可裁剪）----` → 改为 `// ---- 控制面（control 组；运行时自有工具，allowlist 可裁剪，见 Entry.Essential 注释）----`
- `docs/tools/unified-tool-registry-plan.md:135` 同步修正，并标注 2026-10-16 的语义变更点。

**方案 B**：删除 `Essential` 字段，把观测面迁到新字段。成本高、收益低，不推荐。

**必须新增一条契约测试**：断言"任何标为 `Contractual` 的工具都必须有明确的授予路径与观测记录"，防止未来有人往 `Contractual` 里塞普通工具。

### 5.7 观测与审计（I6）

契约性追加**绝不能静默**。需要落三处：

| 面 | 内容 |
|---|---|
| `route_warnings`（F10 的既有机制） | `artifact_reader_denied_by_policy`（警告级）；正常契约追加不再写 info 级 warning |
| `toolsurface` 元数据 | 追加项带 `contract_source: output_contract`，只表达 registry 的契约归属，不代表请求授予来源 |
| runtime event | 面外拒绝发 `tool.denied` 时带 `surface_mismatch=true`，便于离线分析统计"提示泄漏"类事故 |

**为什么要进 `toolsurface`**：该模块的存在目的就是"观测数字与请求数字同源"（见 `docs/tools/unified-tool-registry-plan.md` §1.2 的不变量 T7）。契约性追加如果不出现在观测面，就正好违反它。

---

## 6. 备选方案与取舍

### 6.1 方案对比

| 方案 | 契约闭合(I1) | 授权最小(I2) | profile 可控 | 主要缺陷 | 结论 |
|---|---|---|---|---|---|
| **A. 给 `artifact_read` 加 `Essential: true`** | ✅ | ❌ | ❌ | 无效动作——该字段已不影响可用性（F1），加了什么都不会发生 | **否决** |
| **B. 恢复全局 essential 豁免** | ✅ | ❌ | ❌ | 推翻 2026-10-16 的明确产品决定（F1/F14） | **否决** |
| **C. 只做提示降级（不给读取端）** | ❌ | ✅ | ✅ | 尾部真的丢失且**静默**——模型不知道自己看的是残缺输出，产生错误结论；比报错更难排查 | **仅可作兜底** |
| **D. 自动把 `artifact_read` 塞进每个 allowlist** | ✅ | ⚠️ | ❌ | 静默改写用户请求、不可观测；扩大数据可达面（F28）且无约束 | **否决**（改进版为 §5.2 + §5.3） |
| **E. 契约性授予 + lineage scope 约束**（本文推荐） | ✅ | ✅ | ✅（显式 deny 可覆盖） | 需要新增 `Contractual` 维度与 `GetOwned`；改动跨 4 个包 | **推荐** |
| **F. 让模型用 shell 自己读 artifact 文件** | ⚠️ | ❌ | ⚠️ | artifact 是 SQLite 记录不是文件（F28）；等于允许任意文件读取，边界更差 | **否决** |

### 6.2 明确不做的事

1. **不恢复 essential 豁免**（否决 B）——尊重 2026-10-16 决定。
2. **不把 `todos` 豁免或补进白名单**——正确修法是"不教它"（§5.4a）。
3. **不把"提示降级"当作主方案**（否决 C 作为主方案）——它会制造静默数据丢失。
4. **不以"让父代理别传窄白名单"作为规避**——那是把运行时缺陷转嫁成使用约束。
5. **不在本次一并改动 `search_tool` 的业务豁免语义**（F7）——它工作正常。但其"面内才放行"判据必须在 P0-1 中抽为**共享 helper**（§5.5 前置条件），不得再维护两套"模型能看到什么"的口径。

### 6.3 与其他既有设计的关系

- **与 `docs/tools/unified-tool-registry-plan.md`**：该计划的核心主张是"消除第二份名单"（P1 已完成）。本方案新增的 `Contractual` 必须落在同一注册表内，**不得**新建第二份名单，否则违背其 P1 结论。
- **与 tool-policy-wildcards**：契约性授予**必须尊重 deny 恒优先与通配语义**（F11）。deny `artifact_read`（含通配 deny）时不得追加。
- **与 `subagent-readonly-boundary-transparency-plan-20260917.md`**：只读边界的透明化思路（把被过滤的工具告知模型）与 §5.7 的观测字段一致，可复用其呈现位置。

---

## 7. 实施计划

### 7.1 阶段划分

| 阶段 | 内容 | 依赖 | 风险 |
|---|---|---|---|
| **P0-0** | 统一"已暴露"判据 helper + **通配感知的成员判定**（§5.5 前置条件、§10.6-W1） | 无 | 低（纯重构，行为不变；新增 T20） |
| **P0-1** | 面外调用非致命 + 连击熔断护栏（§5.5） | P0-0 | 低-中（改记账口径 + 新增 dilemma 护栏） |
| **P0-2** | `todos` 段落门控（§5.4a） | 无 | 低（删一行、加一个函数） |
| **P0-2b** | 子代理提示所有权：不继承父级工具面指引 + 按子策略自渲染（§5.4a2） | P0-2 | **中-高**（改子代理提示构造，是本事故的真实修复点） |
| **P0-3** | `Essential` 注释清理（§5.6） | 无 | 极低（纯注释） |
| **P1-1** | `Contractual` 维度 + **策略层**授予 + `resolveChildToolSurface` 同步（§5.2） | P0-3 | 中（改授权路径） |
| **P1-2** | `GetOwned` + lineage scope（§5.3） | 无（可与 P1-1 并行开发，但**必须同批上线**） | 中（数据访问语义） |
| **P1-3** | 观测与审计字段（§5.7） | P1-1 | 低 |
| **P2-1** | 指针文案降级兜底（§5.4b） | P1-1 | 中（改 render 签名，涉 TUI/CLI） |

**P1-1 与 P1-2 必须同批上线**：W2（P1-2）已经落地，`GetOwned` 已成为安全前提；P1-1 的契约授予不再扩大数据可达面。但 P1-1 仍应与 W2 同一发布批次，避免回滚时出现"授予在、范围约束不在"的组合。

### 7.2 涉及文件清单

**P0**

| 文件 | 改动 |
|---|---|
| `backend/internal/agent/tool_list.go` | 抽出 `toolAdvertisedInTurn`，`searchToolVisibleInTurn` 改调它（P0-0）；`fullCatalogForSearch` 改用 pattern-aware 判定（W1） |
| `backend/internal/agent/loop.go`（W1） | `whitelistSet` / `computeAvailableTools` 改为 pattern-aware（`toolregistry.MatchesAnyPattern`），或展开 `AllowedToolNames` |
| `backend/internal/agent/loop.go` | `:1467-1478` 增加面外判定分支；新增 `isUnadvertisedToolDenial`；新增 `surfaceMismatchStreak`、阈值常量、硬停分支（照抄 `:1343-1357`）；闸门处（`:3264`）改用共享判据 |
| `backend/internal/prompt/environment_context.go` | `:182` 移出 `RenderFileEditingGuidance`，新增 `RenderTodosGuidance` 本体 |
| `backend/internal/prompt/tool_guidance.go` | 新增 `todosGuidanceTools` + `RenderTodosGuidanceFacts`；如需要，新增 `StripSurfaceGuidance` |
| `backend/internal/agent/prompt_builder.go` | **P0-2b**：不再逐字继承父级指引；改为按子策略 facts 渲染（`:57` 换 facts 变体；`:23-25` 换版无指引的头部） |
| `backend/internal/prompt/toolguidance/toolguidance.go` | 如需扩展 child 场景的 facts 派生（F39 已可直接用） |
| `backend/internal/api/runtimeapi/agent_context.go` | `:80` 附近接入新渲染函数 |
| `backend/cmd/aicli/commands/chat_session.go` | `:2726-2732` 附近：组合时保留"无指引头部"供子代理继承 |
| `backend/internal/toolregistry/registry.go` | `:171` 注释修正 |
| `docs/tools/unified-tool-registry-plan.md` | `:135` 注释修正 |

**P1**

| 文件 | 改动 |
|---|---|
| `backend/internal/toolregistry/registry.go` | 新增 `Entry.Contractual`；`artifact_read` 标记 |
| `backend/internal/policy/tool_policy.go` | 新增 `ContractualTools`；`AllowTool` 判定顺序插入契约分支；`intersectAllowedTools` 保留契约工具（P1-1） |
| `backend/internal/agent/tool_vocabulary.go` | `resolveChildToolSurface` 追加契约工具 + 路由警告 |
| `backend/internal/artifact/store.go` | 新增 `GetOwned` + `ReadScope` |
| `backend/internal/toolctx/*` | 新增 scope 绑定通道 |
| `backend/internal/agent/loop.go` | `:5539-5544` 绑定 scope |
| `backend/internal/toolkit/tools/artifact_read.go` | 改用 `GetOwned` |
| `backend/internal/toolsurface/*` | 元数据 `contract_source` |
| `backend/internal/agent/scheduler.go` | 路由警告透传（复用 F10 机制） |

**P2**

| 文件 | 改动 |
|---|---|
| `backend/internal/output/tool_result_content.go` | `RenderOptions.ArtifactReaderAvailable` + 中性文案 |
| `backend/internal/executor/output_capture.go` | `:550` 文案参数化 |
| `cmd/aicli/commands/skills_integration.go` | `:800-803` 折叠声明路径对齐 |

### 7.3 不在本方案范围内（需另行决策）

- `Store.Get` 是否最终废弃/收窄（宿主路径仍依赖它）；
- `search_tool` 豁免与面外判定的 helper 合并（§6.2 第 5 条）；
- 其他 session 作用域的存储路径（checkpoint、memory）是否存在同类 `Get` 不一致——本次只核实了 artifact。

---

## 8. 验证方案

### 8.1 必须新增的回归测试

| # | 测试 | 断言 | 覆盖不变量 |
|---|---|---|---|
| T1 | 面外拒绝非致命 | 构造一次"调用不在冻结面内的工具"被拒：`Success==true`、`ToolErrorCount==1`、`UnrecoveredToolErrorCount==0`，且发 `tool.denied(surface_mismatch=true)` | I3 |
| T2 | 面内拒绝仍致命 | 工具**在**面内但被 read-only/策略拒绝：`Success==false`（保持现有严格性） | I3 |
| T3 | `todos` 提示不泄漏 | 用 `tools_whitelist=["shell","write","apply_patch"]`（无 `todos`）渲染系统提示，断言**不含** `` `todos` `` | I4 |
| T4 | `todos` 提示仍存在 | whitelist 含 `todos` 时断言存在（防止门控写反） | I4 |
| T5 | 契约性追加 | 子代理 whitelist 不含 `artifact_read` → 解析后**在**面内，且 `contract_source=output_contract` | I1、I6 |
| T6 | 显式 deny 覆盖契约 | 父策略显式 deny `artifact_read` → **不**追加，且发 `artifact_reader_denied_by_policy` | I2、I5 |
| T7 | 空的 whitelist 契约不变 | `tools_whitelist: []` → 仍然零工具，不追加任何东西 | 契约兼容 |
| T8 | `GetOwned` 同 lineage 可读 | 子 session 读自己产生的 artifact → 成功 | I2 |
| T9 | `GetOwned` 跨 lineage 拒绝 | 用另一个 root 的 session 读 → 拒绝，且返回可操作的错误文案 | I2 |
| T10 | `Get` 行为不变 | 宿主路径（无 scope）读取任意 id → 行为与今日一致 | 向后兼容 |
| T11 | `Contractual` 契约测试 | 凡标 `Contractual` 的工具必须有授予路径与观测字段（防止误标） | I5 |
| T12 | **子代理不继承父级指引** | 父级有写工具、子代理白名单只有 `shell`：子代理提示**不含** `todos`/`File editing guidance`，且 `Allowed tools` 与实际面一致 | I4 |
| T13 | 子代理自渲染 guidance | 子代理有读工具时含 parallel guidance；无读工具时不含（修 F37） | I4 |
| T14 | 共享"已暴露"判据 | 面内但被 deny 的工具 `toolAdvertisedInTurn==true`；面外工具为 false；`search_tool` 豁免与账本判定得到同一结果 | I3 |
| T15 | 面外拒绝连击熔断 | 连续 N 次面外拒绝后 run 终止，`LimitReason="surface_mismatch"`、`Success=false`；中间任何一次成功即清零 | I3 |
| T16 | 契约追加不破坏 fail-fast | 请求了但父策略全不授的场景仍然 fail-fast（追加不得发生在 `validateChildToolAllowlist` 之前） | I5 |
| T17 | **主代理面契约闭合** | profile allowlist = `["view","grep","shell","edit"]`（不含 artifact_read）：请求面**仍含** artifact_read，且 `contract_source=output_contract` | I1、I6 |
| T18 | 显式 deny 关闭契约 | `DeniedTools` 含 `artifact_read`：请求面**不含**它，加载期有警告；失败结果渲染不出指针，改为 `artifact_unreadable=true` | I2、I6 |
| T19 | 失败结果降级 | `edit`/`shell` 失败（小输出也会归档）在无读取端时**不输出** `artifact_read` 字样 | I1、I4 |
| T20 | 通配 allowlist 端到端 | `tools_whitelist=["*"]` 的子代理能拿到全部工具（修 F49/W1）；`"prefix_*"` 同理 | I5 |
| T21 | 空 session_id 语义 | 旧 artifact（`session_id=""`）在受限 `GetOwned` 下行为已定义且有观测计数 | I2 |

### 8.2 必须保持通过的既有测试

| 测试 | 理由 |
|---|---|
| `TestClassifySubagentFailureToolWhitelistDenial`（`subagent_retry_test.go:160-168`） | 分类语义不变；P0-1 只改"是否进失败账本"，不改分类 |
| `TestToolExecutionPolicy_AllowTool_RuntimeSearchGatedByAllowlist`（`tool_policy_test.go:81-99`） | 确认 essential 豁免**没有**被本方案悄悄恢复 |
| `TestGateway_SkipsReArchivingArtifactReadWindows`（`output/artifact_read_no_cascade_test.go:15-42`） | 防止 deref 级联指针回归 |
| `TestRenderToolResultContentForModel_NoIDNoticeOnUntruncatedSuccess`（同文件 `:48-89`） | 指针出现条件不变 |
| `tool_vocabulary_contract_test.go` 系列 | 角色默认表与注册表一致性 |
| `builtin_registry_contract_test.go` 系列 | 注册表与真实装配双向对账 |

### 8.3 端到端验证

1. **复现原事故形态**：派发一个 write 型子代理，显式 `tools_whitelist` 不含 `todos`/`artifact_read`，目标要求产出多文件。断言：batch 状态为 `completed`，交付完整，无 `failed` 计数。
2. **契约闭合验证**：构造超过模型可见预算的 skill 输出（静态载荷，F26），断言子代理能成功 `artifact_read` 取回尾部，且不发生指针级联。
3. **越权验证**：构造两个独立 root 的子代理，让 A 拿到 B 的 artifact id 并尝试读取 → 必须被拒绝且错误文案可操作。
4. **观测验证**：`/runtime/tools` 与 `toolsurface` 输出中，契约性追加项可见且带 `contract_source`。

---

## 9. 决策点、风险与未决问题

### 9.1 需评审确认的决策点

**D-1（2026-10-18 修正，上一版确认作废）：allowlist 未列出 `artifact_read` ≠ 显式拒绝**

上一版把"allowlist 不含 `artifact_read`"判为显式选择、不追加。第三轮复查推翻了它：

- allowlist 的语义是"允许哪些动作"，不是"是否承认输出契约"；
- profile 作者写 allowlist 时不会想到单列一个读取器；
- 折叠发生在 `view`/`grep`/`shell` 等最常用工具上，**失败结果无论多小都会带指针**（F43），漏掉即静默丢数据。

修正后的判定（完整表与依据见 §5.2）：

| 条件 | 处置 |
|---|---|
| `DeniedTools` 命中 `artifact_read`（含通配） | 不授予；加载期警告；渲染层按类别降级（§5.4b） |
| 非空工具面且未 deny | **授予**（含 allowlist / `tools_whitelist` 未列出的情况） |
| 空工具面 / `tools_whitelist: []` / `disable_tools` | 不授予 |

与 2026-10-16 决定的关系：控制点从"在 allowlist 里记得列出它"改为**显式 deny**；这是单一工具、deny 优先、可审计的窄豁免，不是全局 `Essential` 豁免复活。

**D-2：`Contractual` 的适用范围**

建议**只给 `artifact_read` 一个**，并在契约测试（T11）中钉死"新增成员必须附授予路径"。扩大适用面应当逐案评审——一旦 `Contractual` 变成"绕过 allowlist 的后门"，它就退化成本方案要否定的 B/Essential 老路。

**D-3：`GetOwned` 的默认行为**

建议 ctx 无 scope 时回落 `Unrestricted`（灰度安全）。若评审认为不安全，可改为"无 scope 即拒绝并记录"，但这会立即影响 TUI/CLI 直连面，需要单独评估。

### 9.2 未决问题（需进一步查实）

| # | 问题 | 影响 | 查法 |
|---|---|---|---|
| Q1 | ~~fork 快照是否包含 tool 消息~~ **已关闭**：system 消息全部剥离，tool 消息保留 → 父级指针可能被引用，scope **必须**含父（`subagent_context.go:42-54`，F52） | 已定 | — |
| Q2 | MCP / 第三方工具的结果是否也会被 gateway 归档并产出指针 | 决定 §5.2 "存在任何工具即追加"的判据是否需要收窄 | 查 `output.Gateway.Process` 的归档条件是否按工具来源区分 |
| Q3 | 历史记录中是否存在 `session_id` 为空/缺失的 artifact | 影响 `GetOwned` 对旧数据的行为 | 查 `store.go` 写入路径是否强制非空；查历史库样本 |
| Q4 | TUI / CLI 直连面是否有独立的 `artifact_read` 暴露逻辑 | 决定 P2 的默认值（`ArtifactReaderAvailable`） | 查 `aiclitools`/`chat_actor_host` 的工具面装配 |
| Q5 | `checkpoint` / `memory_entries` 等同类存储是否存在 `Get` 无主体约束 | 判断 L4 是孤立问题还是系统模式 | 按 `store.go` 同法核查其余读取方法 |

### 9.3 风险

| 风险 | 等级 | 缓解 |
|---|---|---|
| P1-1 与 P1-2 未同批上线 → 数据可达面静默扩大 | **高** | 合并为同一变更集；CI 中断言二者共存（可用 T5+T9 的组合用例） |
| `Contractual` 被滥用为绕过后门 | 中 | T11 契约测试 + 评审门槛（D-2） |
| 通配 allowlist 在闸门处被当成字面名 → 子代理零工具（F49） | **高** | 升为 P0-0/W1；T20 守住 |
| 面外判定误伤真实失败 | 中 | 判据严格限定为"白名单闸文案 **且** 不在冻结面内"；T2 专测 |
| 提示门控改动导致系统提示结构变化，影响既有 prompt 快照测试 | 低 | 按 facts 既有兼容约定：`nil` facts 保持旧行为（`tool_guidance.go:13-15`） |
| 提示继承类缺陷回归（父级指引再次被子代理继承） | 中-高 | T12/T13 纳入回归集；`BuildSubagentPrompt` 的单测钉死"不继承指引" |
| 契约性追加使 `route_warnings` 噪音增加 | 低 | 信息级与警告级分开；仅在"显式拒绝"时才用警告级 |

### 9.4 建议的验收标准

1. 原事故形态可复现，且复现后 batch 状态为 `completed`（§8.3-1）；
2. T1–T16 全部通过，§8.2 列出的既有测试无一回归；
3. `toolsurface` 与 `/runtime/tools` 的数字同源（追加项可见）；
4. 无任何路径静默改写 allowlist——契约追加 100% 带 `contract_source` 记录。

---

## 10. 整体逻辑梳理与合理性审查（第二、三轮复查）

### 10.1 端到端因果链（复查后的完整版）

```
[父会话初始化]
  chat_actor_host.go:2618  cfg.SystemPrompt = composeLocalChatSystemPrompt(...)
  chat_session.go:2726-32  按【父级】facts 渲染 guidance（写工具可见 → 含 File editing guidance）
  environment_context.go:182  该段落内含 "When using `todos`, ..."
        │
[派发子代理]
  scheduler.go:982 → ChildAgentFactory.Build
  child_factory.go:84       resolveChildToolSurface → 子代理面 = tools_whitelist（不含 todos/artifact_read）
  child_factory.go:126      BuildSubagentPrompt(parentConfig, task)
  prompt_builder.go:23-25   parent.SystemPrompt 被【逐字】写入子代理提示      ← 缺陷①提示所有权
  prompt_builder.go:57      无条件渲染 parallel guidance（不接 facts）        ← 缺陷②
  prompt_builder.go:85-93   同时打印 "Allowed tools: [...]"（与①矛盾）      ← 缺陷③自相矛盾
        │
[子代理运行]
  模型据提示调用 todos / artifact_read（它并非幻觉）
  loop.go:3286  allowedTools 不含 → deny："tool not allowed for this agent: X"
        │
[记账]
  loop.go:1467-78   全部进 pendingFailures
  loop.go:8313-23   消账只认“同名工具后续成功” → 永不可能恢复
  loop.go:1200      Success = len(pendingFailures)==0 → false
        │
[调度]
  scheduler.go:1286-93  分类 tool_error / Retryable=false → batch failed(3/4)
```

**修复点映射**：缺陷①→P0-2b；缺陷②→P0-2b-3；缺陷③→P0-2b 后自动消解；记账链→P0-1（+熔断护栏）；契约断链→P1-1；读取范围→P1-2。

**关键结论**：由于缺陷①发生在渲染之后，只改渲染器的门控（P0-2）**修不好本事故**——这是第二轮复查对第一版方案最重要的修正。

**第三轮追加结论**：即使修好提示层，只要 `artifact_read` 仍在 allowlist 里可选，`view`/`grep`/`shell`/`edit` 的折叠输出就会在“未列出”的 profile 中静默不可读。因此**授予点必须上移到策略层**，且 **allowlist 未列出不得等同于拒绝**（见 D-1 修正）。

### 10.2 不变量覆盖矩阵

| 不变量 | 承接修复 | 覆盖 | 残余风险 |
|---|---|---|---|
| I1 契约闭合 | P1-1（Contractual 追加） | ✅（默认开启） | 显式 deny → P2-1 降级兜底；§5.3 嵌套深度限制 |
| I2 授权最小 | P1-2（GetOwned + scope） | ✅（v1 = {self, parent}） | depth≥2 嵌套 fork 待拓展；空 session_id 语义见 W2 |
| I3 面外调用不判红 | P0-1 + 熔断 | ✅ | 阈值需实测定参（T15） |
| I4 提示跟随工具面 | P0-2 + **P0-2b** | ✅ | 新增提示段落需持续遵守\"在知道面的层渲染\" |
| I5 单一真源 | P0-3 + Contractual 落在注册表 | ✅ | T11 防滥用 |
| I6 显式优于隐式 | P1-3（contract_source 观测） | ✅ | `toolsurface` 与请求面同源需 CI 验证（T5） |

### 10.3 复查发现与处置（11 项）

| # | 发现 | 性质 | 处置 |
|---|---|---|---|
| 1 | 子代理逐字继承父级工具面指引（F35/F36/F42） | **主缺陷**，首版方案未覆盖 | §5.4a2 / P0-2b |
| 2 | 子代理 parallel guidance 无条件渲染（F37） | 同类缺陷第二实例 | P0-2b-3 |
| 3 | `Allowed tools` 与继承指引互相矛盾（F38） | 提示自相矛盾 | P0-2b 后自行消解，保留 `Allowed tools` 作权威声明 |
| 4 | 两套\"模型看到什么\"的口径（闸门 `allowedTools` vs `frozenTurnToolSurface`） | 新判定会有误判 | 抽共享 helper，已升为 P0-0 前置 |
| 5 | 面外拒绝无连击护栏（F41） | 非致命化后的新漏洞 | surfaceMismatchStreak，照抄 M6 模式 |
| 6 | 不进 `pendingFailures` 会影响现有计数口径 | 可观测性损失 | 新增独立计数器（§5.5 末尾） |
| 7 | `RootSessionID` 运行时通常为空（F40） | 原 scope 设计不可实现 | v1 scope = {self, parent}，嵌套限制写入文档 |
| 8 | 契约追加可能破坏 fail-fast（F9） | 顺序敏感 | §5.2 执行顺序约束（追加在验证之后） |
| 9 | 授予点只在子代理层，**覆盖不到主代理**（F45） | 原方案遗漏一半场景 | 改为策略层 `ContractualTools`（§5.2） |
| 10 | 把 allowlist 未列出当成"显式拒绝"（上一版 D-1） | 会导致 view/shell/edit 大输出静默不可读 | D-1 修正：只有 `DeniedTools` 算拒绝 |
| 11 | 失败结果无论多小都会归档并带指针（F43） | 指针需求比想象中广 | 降级表必须覆盖失败路径（§5.4b，T19） |

### 10.4 合理性论证

1. **与 2026-10-16 决定一致**：该决定要求\"profile 必须能完全控制工具面\"。契约性授予只填\"未声明\"的缺省，**不覆盖任何显式 deny**（D-1），因此是补充而非推翻；`search_tool` 的既有豁免去除结果（F14）不受影响（T11 守住）。
2. **与\"单一真源\"一致**：`Contractual` 落在 `toolregistry`（P1 已完成去重的同一处），不新建第二份名单（§6.3）。
3. **两个工具分开治**：`todos` 是提示 bug（正确修法是不要教它），`artifact_read` 是契约缺口（正确修法是保证可用）。用同一机制处理两者都会错一半。
4. **不选静默降级**：降级会使模型误以为看到完整输出，产生错误结论——比报错更难排查。所以降级只能是\"契约被显式拒绝\"的兜底，且必须显式声明尾部不可取回。
5. **非致命 ≠ 放纵**：面外拒绝仍计数、仍发事件、仍有连击熔断（T15），只是不再否决已完成交付（I3）。
6. **成本与收益**：P0 五项（P0-0/1/2/2b/3）均为低-中风险且独立可验；P1 两项必须同批上线（§7.1）；P2 只改渲染入参。

### 10.5 残余风险与跟踪项

- **Q2/Q3/Q4/Q5** 仍待查实（§9.2）；**Q1 已关闭**（fork 保留 tool 消息 → 含父必要）；另有 W2（空 session_id）需先定语义；
- **嵌套 fork（depth≥2）**不支持读祖父 artifact——已知边界，需跨层 fork 时再线 `RootScopeID`；
- **熔断阈值**（3/5）为建议值，需实测标定；
- **提示段落守则**需写入开发规范：*涉及工具面的引导只能在知道该工具面的层渲染*（否则缺陷①会以新形式重现）。

### 10.6 闭环审计（第四轮）：是否自洽

**结论**：**因果链闭环、授权语义闭环；但还不是完全自洽——存在 2 个必须先补的机械缺口、1 个实现定稿项、1 个传递性边界。**

逐项审计：

| 检查项 | 结论 | 依据 |
|---|---|---|
| 1. 因果链（提示→调用→拒绝→记账→判红）是否完整可解释 | ✅ 闭环 | §10.1；F15–F18 |
| 2. 授权语义（deny → contractual → allowlist）是否唯一 | ✅ 闭环 | F47、F51；引擎路径也走 `AllowTool` |
| 3. 三条面装配路径（主/子/交集）是否同源 | ✅ 闭环 | F45/F46 + §5.2 |
| 4. 提示是否只在知道面的地方渲染 | ✅ 设计闭环，⚠️ 实现需结构化拆分 | F35/F36/F50；§10.6-W3 |
| 5. 账本是否只记真实失败且仍可观测 | ✅ 闭环 | §5.5 + `SurfaceMismatchCount` + 熔断 |
| 6. 契约追加是否会被静默覆盖 | ✅ 已避免 | `tool_vocabulary.go:240` 之后再追加（§5.2 顺序约束） |
| 7. **通配 allowlist 是否真的端到端生效** | ❌ **不闭环** | F49：策略层支持通配，但闸门/请求面精确匹配 |
| 8. **旧数据/空 session_id 的解引用语义** | ❌ **未定义** | Q3；`GetOwned` 需要明确 fail-open/closed |
| 9. 解引用范围是否在 fork 下闭合 | ⚠️ **v1 覆盖，嵌套有边界** | F52；depth≥2 需祖先链 |

**必须先补的三个工作项（否则上线后会有新坑）**：

| 编号 | 问题 | 修法 |
|---|---|---|
| **W1** | 通配 allowlist 在子代理闸门处被当成字面名 → 子代理**零工具**（`"*"` / `"prefix_*"`） | 把 `whitelistSet` / `computeAvailableTools` / `fullCatalogForSearch` 的成员判定改为共享的 **pattern-aware** helper（`toolregistry.MatchesAnyPattern`）；或把 `AllowedToolNames()` 展开成具体名。新增 T20（`["*"]` 子代理能拿到全部工具） |
| **W2** | `session_id` 为空的旧 artifact 在 `GetOwned` 下语义未定义 | 明确为 **fail-closed**（受限 scope 拒绝）+ 观测计数，或明确为 unowned（仅 `Unrestricted` 可读） |
| **W3** | 父级 guidance 继承的修复方式依赖宿主形状（CLI 写入 SystemPrompt；runtimeapi 部分块写入、部分独立消息） | **结构化拆分**：组合器返回 `head`（profile + env）与 `guidance` 两段；子代理只取 `head`，再按子策略自渲染。**禁止**用字符串剥离（会误删用户文本） |

**审计中确认闭环的两点**（避免过度设计）：

- **权限引擎不会形成第二套口径**：`validateStaticPolicy` 直接调用 `Policy.AllowTool`（F51），契约性授予在引擎路径下同样生效；
- **fork 不携带父级 guidance**：`stripForkSystemPrefix` 剥离全部 system 消息（F52），所以 §5.4a2 的修复不需要额外处理 fork 历史；但 tool 消息被保留，因此 scope 必须含父——这与 v1 设计一致。

**仍属边界（不阻塞 P0/P1）**：Q2（MCP 工具是否产出指针——实际由网关行为决定，F43 已说明与工具身份无关）、Q3（见 W2）、Q4（TUI 直连面）、Q5（其它存储的同类 `Get`）、depth≥2 嵌套 fork（需祖先链）。

### 10.7 实施记录（2026-10-18，全部计划项）

`P0-0`、`P0-1`、`P0-2`、`P0-3`、`P1-1`、`P1-2`、`P1-3`、`P2-1` 均已落地；W1–W3 为其中的机械闭环项。

| 项 | 状态 | 落地内容 |
|---|---|---|
| **W1 通配 allowlist** | ✅ | `whitelistAdmits`（nil=全放行、空=全拒、其余走 `toolregistry.MatchesAnyPattern`）替换 `whitelistSet`；闸门、`computeAvailableTools`、`fullCatalogForSearch`、并行预检、`shouldExposeSpawnSubagents` 统一判据 |
| **W2 读取范围** | ✅ | `artifact.ReadScope` + `GetOwned`；`toolctx.WithArtifactReadScopeForSessions`；`loop.run` 绑定 `{当前 session, 直接父 session}`；`artifact_read` 改用 `GetOwned`，无 scope 回落 self-only；空 owner fail-closed；`DerefMissReasonUnscoped` |
| **W3 提示所有权** | ✅ | `agent.ChildPromptHeadOption`；CLI/runtimeapi 产出 head-only 组合；`BuildSubagentPrompt(parent, task, facts)` 只继承 head 并按子策略渲染；嵌套子级回填 head |
| **P0-0 共享判据** | ✅ | `toolAdvertisedInTurn` + `toolSurfaceKnown`；`searchToolVisibleInTurn` 改调同一 helper |
| **P0-1 面外拒绝** | ✅ | 只读类面外调用不进 `pendingFailures`；`surfaceMismatchStreak`（3 升级 / 5 硬停）、`SurfaceMismatchCount`（run 结果 + Stop hook 载荷）、`tool.surface_mismatch` 事件、成功即清零；**写/shell/委派/编排类仍致命** |
| **P0-2 todos 门控** | ✅ | `todos` 规则移出 `RenderFileEditingGuidance`，新增 `RenderTodosGuidance` + `RenderTodosGuidanceFacts`；CLI/runtimeapi 按 facts 渲染 |
| **P0-3 语义清理** | ✅ | `registry.go` control 组注释与 `docs/tools/unified-tool-registry-plan.md` 的 `Essential` 说明已修正，并新增 `Contractual` 行 |
| **P1-1 Contractual 授予** | ✅ | `Entry.Contractual`（仅 `artifact_read`）；`ToolExecutionPolicy.ContractualTools`；`AllowTool` 顺序 deny → contractual → BlockDelegation → allowlist → capability → read-only；空 allowlist 仍全拒；子代理在**只读过滤之后**追加契约工具；显式 deny 优先 |
| **P1-2 数据范围** | ✅ | 同 W2 |
| **P1-3 观测** | ✅ | `toolsurface` 输出 `contractual` 与 `contract_source=output_contract`；正常契约追加不写 `route_warnings` |
| **P2-1 指针降级** | ✅ | `output.DegradeArtifactPointerForModel`：可重发类 → narrower-window；静态载荷 → unrecoverable；失败结果 → `artifact_unreadable=true`；同时替换 mid-line fold hint，确保最终文本不再出现 `artifact_read(`；agent 的 payload 与 approved replay 路径已接线 |

**验证命令与结果**（workdir=`backend`）：

| 命令 | 结果 |
|---|---|
| `go build ./internal/agent/ ./internal/prompt/ ./internal/api/runtimeapi/ ./cmd/aicli/commands/` | PASS |
| `go build ./...` | PASS |
| `go test ./internal/output/ ./internal/policy/ ./internal/toolsurface/ ./internal/agent/ ./internal/artifact/ ./internal/toolctx/ ./internal/prompt/ ./internal/api/runtimeapi/ -count=1` | PASS |
| `go test ./internal/toolkit/tools/ -count=1` | PASS |
| `go test ./cmd/aicli/commands/ -run "TestComposeLocalChatSystemPrompt\|TestComposeChatSystemPrompt\|TestComposeDurableChatSystemPrompt" -count=1` | PASS |

**新增测试**：`TestWhitelistAdmitsSupportsWildcards`、`TestStoreGetOwnedEnforcesReadScope`、`TestArtifactReadAllowsParentSessionArtifactInLineageScope`、`TestArtifactReadRejectsUnscopedArtifact`、`TestPromptBuilder_UsesSurfaceIndependentHead`、`TestPromptBuilder_GatesGuidanceByChildFacts`、`TestPromptBuilder_EmptyHeadDoesNotFallBackToParentPrompt`、`TestRenderFileEditingGuidance_DoesNotMentionTodos`、`TestRenderTodosGuidanceFactsFollowsVisibility`、`TestToolAdvertisedInTurnMatchesFrozenSurface`、`TestIsUnadvertisedToolDenialRequiresKnownSurface`、`TestDeniedToolNameParsesJoinedDenialText`、`TestNonFatalSurfaceMismatchName`、`TestReActLoop_UnadvertisedReadOnlyToolIsNonFatal`（T1）、`TestReActLoop_InSurfaceWriteDeniedByReadOnlyStaysFatal`（T2）、`TestReActLoop_UnadvertisedWriteToolStaysFatalAndUncounted`、`TestToolExecutionPolicy_ContractualToolSurvivesNarrowAllowlist`、`TestToolExecutionPolicy_ContractualToolBlockedByEmptyAllowlist`、`TestContractualEntriesAreGrantedByNarrowAllowlist`（T11）、`TestToolExecutionPolicy_LiteralPolicyHonorsContract`、`TestFilterPolicyBlockedToolDefinitionsKeepsContractualTool`（T17）、`TestResolveChildToolSurfaceAddsContractualTool`、`TestResolveChildToolSurfaceContractualToolRespectsEmptyAndDeny`、`TestResolveChildToolSurfaceSkipsContractualToolAbsentFromSurface`、`TestDegradeArtifactPointerForModel`、`TestDegradeArtifactPointerForModelKeepsPathNoticeReadable`。

**实施中的两处设计修正**：

1. **P0-1 只对只读类工具生效**：最初按"所有面外调用"实现，导致只读 verifier 的 `write_file` 越界被判成非致命，patch decision 被错误放行。现在写/shell/委派/编排类仍致命。
2. **契约追加必须在只读过滤之后**：最初放在 `resolveChildToolSurface`，会把"请求的写工具全被剥离"的子代理又补上 `artifact_read`。现在由 `finalizeChildToolSurface` 在两条构建路径的过滤之后统一执行。

### 10.8 实施审查（2026-10-18）：发现与处置

本轮审查对照实际代码逐项核对，发现 **5 个真实缺陷并已修复**，另有若干残留设计项。

**已修复**

| # | 缺陷 | 影响 | 修复 |
|---|---|---|---|
| R1 | 门面拒绝的 `surface_mismatch` 只判断"不在面上"，未复用"只读类"判据 | 写/shell/委派类被拒时会计入 `SurfaceMismatchCount` 并推进熔断，账本与事件口径分裂 | 抽出 `nonFatalSurfaceMismatchName`，gate 与账本共用；新增 `TestReActLoop_UnadvertisedWriteToolStaysFatalAndUncounted` |
| R2 | `childPromptHead` 把"显式空 head"当成"未配置"，回退到完整父提示 | 无 profile/无环境的 runtimeapi 会话仍会把 guidance 泄漏给子代理 | 按 key 是否存在区分；新增 `TestPromptBuilder_EmptyHeadDoesNotFallBackToParentPrompt` |
| R3 | `artifact_unreadable` 在 `toolResultsToPayloads` 复制 metadata **之后**才写入 | 模型侧 payload 看不到该标记 | 降级后显式回填 `toolPayloads[i].Metadata` |
| R4 | 路径型指针（`raw_output_artifact_path`）被按 ID 指针降级并标记不可读 | 实际可从磁盘读取的文件被误报为 unrecoverable | 路径通知改用 on-disk 提示，且不设置 `artifact_unreadable`；新增 `TestDegradeArtifactPointerForModelKeepsPathNoticeReadable` |
| R5 | 直接构造 `ToolExecutionPolicy{}` 的宿主不会得到 `ContractualTools` | 契约在任何未走构造函数的路径静默失效 | `contractualToolAllowed` 在集合为空时回退 `toolregistry.IsContractual`；新增 literal policy 测试 |
| R6 | P2-1 的降级改变了模型可见 payload 大小，显式预算校准测试出现约 2/5 波动 | 测试不稳定 | 将 `context_max_prompt_tokens` 从 1300 重新标定到 1280；`-count=10` 稳定通过，并在测试注释记录原因 |
| R7 | `finalizeChildToolSurface` 无条件追加 `artifact_read`，未检查宿主实际注册面 | 可能向模型广告一个没有 schema 的工具（prompt/白名单与请求面不一致） | `appendContractualToolNames` 增加 `available` 参数；已知面不含该工具时跳过，未知面保守追加；新增 `TestResolveChildToolSurfaceSkipsContractualToolAbsentFromSurface` |
| R8 | 真机 R3：`artifact_read` 的 `out of scope` 被分类为 `provider_error/UPSTREAM_ERROR`、`retryable=true` | 确定性工具失败被建议重试，浪费 token | `ClassifySubagentFailure` 识别 `out of scope` / `不属于当前会话或父会话的读取范围` → `tool_error` / `TOOL_EXECUTION` / 不可重试；新增 `TestClassifySubagentFailureArtifactScopeDenial` |

**本轮复查补齐的残留项（O1、O3–O7）**

| # | 原残留 | 处置 | 验证 |
|---|---|---|---|
| O1 | `toolsurface` 的 `granted_by=output_contract` 是静态属性：即使用户显式列出 `artifact_read`，仍标为契约授予 | 改为 `contract_source=output_contract`，只表达 registry 的契约归属；不再写 `granted_by`，避免把 registry 事实误当成请求授予事实 | `TestEnrichMarksContractOriginWithoutClaimingGrant` |
| O3 | 每次契约追加都写一条 `route_warnings` | 删除该 info 级 warning；契约追加是正常行为，不再污染父级 route 告警 | `TestResolveChildToolSurfaceAddsContractualTool` 断言无 `granted_by_output_contract` |
| O4 | `frozenTurnToolSurface` 每次调用都 clone；一次拒绝/降级会调用多次 | 给 `TurnToolSurfaceSnapshot` 增加 `LoadTurnToolSurfaceNames`；`toolAdvertisedInTurn` / `toolSurfaceKnown` 只取名称，不再深拷贝 schema；in-memory 与 runtime 快照均实现 | agent/chat 全量包测试 |
| O5 | `GetOwned` 先加载完整记录再校验 scope | 先以 `SELECT session_id` 做窄查询校验 owner，拒绝路径不再加载 content/summary/metadata；允许路径再走完整 `Get` | `TestStoreGetOwnedEnforcesReadScope` + artifact 包测试 |
| O6 | `ExecuteApprovedToolCall` 绑定 self-only scope | 新增 `WithArtifactReadScope`；审批重放保留调用方已绑定的 lineage，并始终加入当前 session；`Unrestricted` 会被收窄为 self-only | `TestBindApprovedArtifactReadScopePreservesLineage` / `...NarrowsUnrestricted` |
| O7 | 真机 R2：指针 `size=32731`，但 `artifact_read total_bytes=155009`（指针 size 低估归档大小） | 根因是 `recordToolExecutionOutcome` 把 shell 元数据放进 `tool_metadata`，而 `metadataInt` 只读顶层；现支持嵌套 `tool_metadata`，指针优先采用 `output_window_total_bytes` | `TestMetadataIntReadsShellWindowFromNestedToolMetadata` |
| O8 | `tool.surface_mismatch` 由 `loop.go` 以裸字面量发射，但未登记 events 契约，也未进 runtimeobserve 已知类型目录 | 在 `events/contract.go` 登记为 0 通道（当前无 chat 侧消费者），并补 `runtimeobserve/known_types.go`；避免被统计为 `unknown_events_dropped` | `go test ./internal/events/ ./internal/runtimeobserve/` |
| O9 | 真机第 2 轮：分析侧已是 `tool_error/TOOL_EXECUTION`，但 supervision 的 `failed items` 仍把 `out of scope` 推导为 `retryable=true`；任务级修复后，批次外壳又因 `ErrorDetail` 为空继续显示 true | `supervision.retryableFailure` 增加确定性工具/策略错误标记：`tool_error`、`tool_execution`、`out of scope`、`stale_context`、`not allowed`、`read_only`、`artifact_unreadable`；新增 `batchFailureDetail`，在 `ErrorDetail` 为空时回退读取 `ResultSummary.CriticalErrors`；保留 timeout/network 等瞬时错误的可重试判定 | `TestRetryableFailureTreatsArtifactScopeDenialAsDeterministic`、`TestBatchObligationSourceUsesCriticalErrorsForRetryVerdict` + supervision 包测试 |

**审查结论**：R1–R8 已修复；O1、O3–O9 已在本轮补齐。O2 在当前代码中未发现对应残留。修复后 `go build ./...` 与 `go test ./internal/agent/ ./internal/policy/ ./internal/output/ ./internal/toolsurface/ ./internal/artifact/ ./internal/toolctx/ ./internal/chat/ ./internal/api/runtimeapi/ ./internal/events/ ./internal/runtimeobserve/ ./internal/supervision/ -count=1` 全部通过。

全量 `go test ./... -count=1 -p 4` 的唯一失败是 Windows ConPTY 的
`TestOpenTerminalPTYSurvivesCallerContextCancel`（时序抖动）；该测试单独
`-count=3` 连续通过。新增 `tool.surface_mismatch` 后已运行
`go run ./cmd/contractgen` 重新生成 `frontend/src/types/runtime/event-contract.ts`，
`cmd/contractgen` 的生成物一致性测试通过。

### 10.9 真机验证（2026-10-18，session_20261018172544_t4ctTkFL）

环境：`http://127.0.0.1:55567`；provider `magpie / group/auto-deepseek-v4-1-flash`；`Permission Mode: bypass_permissions`；主代理工具面含 `artifact_read`。

| 轮次 | 子代理白名单 | 结果 |
|---|---|---|
| R1 | `["view"]`，read_only | 未触发 artifact 折叠：`view` 在 568 行处按自身窗口截断（`is_truncated=true`），不产生 `art_...` 指针；无 `tool not allowed` |
| R2 | `["shell"]`，非只读 | shell 输出 155,009 字节 → 折叠并产出 `Full raw output artifact_id: art_99cb057a0daa4e54a1da0aa87d9398ab size=32731 kind=text`；`artifact_read` 成功返回 `total_bytes=155009 window=[0,1024) eof=false next_offset=1024`；无 `tool not allowed` |
| R3 | `["view"]`，read_only，跨会话 id | 子代理系统提示实际为 `Allowed tools: artifact_read, view.`（契约追加，**无 todos**）；读取 R2 子会话的 artifact 被拒：`TOOL_EXECUTION` / `artifact read out of scope: ... belongs to session subagent_subagent_1_420b35e...`；未泄露数据 |
| R4 | `["view"]`，read_only，越权调 shell | 子代理调用 `shell` 被执行期白名单强制拦截：`AGENT_PERMISSION` / `Tool execution failed: tool not allowed for this agent: shell`；证明白名单并未失效 |

**真机结论**

1. **契约追加生效，且白名单仍然强制**：`["shell"]` 与 `["view"]` 子代理都能调用 `artifact_read`；`Allowed tools` 行显示 `artifact_read` 被追加，`todos` 未出现。R4 进一步证明 `["view"]` 子代理调用 `shell` 会被 `AGENT_PERMISSION` / `tool not allowed` 拒绝。父代理把"白名单未严格强制"作为结论是**误读**：额外出现的只有运行时契约工具 `artifact_read`。
2. **读取范围隔离生效**：同一父会话下的**兄弟子会话** artifact 被 `GetOwned` 拒绝，错误文本明确给出 owner session；没有跨 lineage 泄露。
3. **R3 的 failed 是预期的负向测试结果**：子代理唯一的任务就是一次被拒绝的越权读取，因此 `Success=false` 正确；不应据此重派。
4. **分类缺陷（R8）已在代码修复**：真机运行的是修复前的二进制，`/analysis/subagents` 显示 R3 为 `provider_error/UPSTREAM_ERROR/retryable=true`。修复后需重启服务再复验；当前单测已覆盖。
5. **指针 size 低估（O7）已在代码修复**：根因是 `output_window_total_bytes` 被放在 `tool_metadata` 嵌套层，而指针渲染只读顶层；修复后指针会采用完整 capture 大小。真机运行的是修复前二进制，重启后可在新一轮 shell 大输出中复验 `size=155009`。
6. **view 不是 artifact 折叠的可靠触发器**：`view` 的 568 行窗口通常低于 32 KiB 预算；真机验证 artifact 路径应使用 shell（或其它单次大输出工具）。
7. `subagent_spawn` 不接受 `tools_whitelist`（工具 schema 明确要求用 `subagent_spawn_batch` 施加逐任务白名单）；这不是本次实现的缺陷。

### 10.10 修复后真机回归（2026-10-18，session_20261018182230_9r45tRlG）

环境：`http://127.0.0.1:53923`；provider `workbuddy / cn:hy3`；`Permission Mode: bypass_permissions`；服务启动于 `10:22:30Z`。

| 轮次 | 配置 / 目标 | 结果 |
|---|---|---|
| T1 | `["shell"]`，非只读；执行 `1..4000` 生成大输出 | 207,009 字节触发折叠；指针 `art_de0c940136d84e39868093916215cf68 size=207009 kind=text`；`artifact_read` 返回 `total_bytes=207009 window=[0,1024) eof=false next_offset=1024`；`Allowed tools: artifact_read, shell.`；batch 结果无 `granted_by_output_contract` |
| T2 | `["view"]`，read_only；读取 T1 子会话 artifact | 被拒：`artifact read out of scope ... belongs to session subagent_subagent_1_5e23b6100e9c4ec6bb509fa2e40a5211`；`/web/api/analysis/subagents` 显示 `failure_category=tool_error`、`error_code=TOOL_EXECUTION`、`retried=0` |

**结论**

1. **O7 真机通过**：指针 `size` 与 `artifact_read total_bytes` 均为 207009，不再出现折叠后 32KB 级低估。
2. **O1/O3 契约链路通过**：子代理提示只有 `artifact_read, shell`，契约工具被追加但未泄漏其他工具；batch 结果不再出现 `granted_by_output_contract`。
3. **R8 分析分类通过**：跨会话 scope 拒绝被分析侧归类为 `tool_error/TOOL_EXECUTION`，未自动重试。
4. **O9 真机暴露、代码已修复**：父级 `failed items` 仍显示 `retryable=true`；根因是 supervision 的 `retryableFailure` 只按 permission/denied 等标记推导。已补确定性工具错误标记并加单测；当前 53923 运行的是修复前二进制，重启后复验 `retryable=false`。
5. **O8/O6/O5 的单元覆盖**：events 契约/runtimeobserve 目录、审批重放 lineage、scope 预检均有回归测试；CLI web 宿主未暴露 toolsurface 端点，`contract_source` 的在线观测需在 runtime-server/TUI 侧验证。

### 10.11 O9 复验（2026-10-18，session_20261018184210_doiPxAoG）

环境：`http://127.0.0.1:53676`；provider `workbuddy / cn:hy3`；`Permission Mode: bypass_permissions`；服务启动于 `10:42:10Z`（已包含 O9 的任务级修复）。

| 轮次 | 配置 / 目标 | 结果 |
|---|---|---|
| T1 | `["shell"]`，非只读；生成大输出 | `batch_7dee814e19660e7e` succeeded；3000 行 / 155,009 字节；指针 `art_078baed3cbce4ceaaf45c1c7ad6d3ee2 size=155009`；`artifact_read total_bytes=155009 window=[0,64)`；`Allowed tools: artifact_read, shell.` |
| T2 | `["view"]`，read_only；读取 T1 子会话 artifact | `batch_711c96b537dd54df` failed_with_result；scope 拒绝生效；`/web/api/analysis/subagents` 显示 `failure_category=tool_error`、`error_code=TOOL_EXECUTION`、`retried=0`；**任务级 `retryable=false` 已生效** |

**复验发现**

1. **O9 任务级修复真机通过**：`subagent_1` 的 failed item 显示 `retryable=false`，且 `do_not_retry=true`。
2. **批次外壳仍有残留**：batch 行仍显示 `error_class=task_failure retryable=true`。根因是 batch 的 `ErrorDetail` 为空，确定性错误只存在 `ResultSummary.CriticalErrors`；`retryableFailure` 未读取该字段。
3. **已补批次级回退**：`batchFailureDetail` 在 `ErrorDetail` 为空时解析 `ResultSummary.CriticalErrors`，再交给 `retryableFailure`；新增 `TestBatchObligationSourceUsesCriticalErrorsForRetryVerdict`。
4. **待复验**：53676 进程启动早于批次级修复，重启后同一形态应显示 batch `retryable=false`。

---

## 11. 附录

### 11.1 证据索引（按文件聚合）

| 文件 | 关键行 | 用途 |
|---|---|---|
| `backend/internal/toolregistry/registry.go` | `84-89`（Essential 语义变更）、`161`（artifact_read 条目）、`171-175`（control 组，注释矛盾） | F1/F2/F3 |
| `backend/internal/agent/role_defaults.go` | `9-39` | F4/F5 |
| `backend/internal/agent/loop.go` | `1200`、`1209-1211`、`1467-1478`、`3286-3292`、`3851-3881`、`8313-8323`、`5539-5544`、`262-272`（ToolWhitelist）、`4330-4337`、`5820-5830` | F6/F15/F16/F17/F18/F49 |
| `backend/internal/agent/tool_list.go` | `48-54` | F7 |
| `backend/internal/agent/tool_vocabulary.go` | `70-84`、`210-244`、`254-266` | F8/F9/F10 |
| `backend/internal/policy/tool_policy.go` | `22-29`、`166-175`、`590-600`、`644-658` | F11/F12/F49 |
| `backend/internal/policy/tool_policy_test.go` | `81-99` | F14 |
| `backend/internal/agent/subagent_retry.go` | `24-38`、`76-77` | F19 |
| `backend/internal/agent/subagent_retry_test.go` | `160-168` | F19 |
| `backend/internal/agent/scheduler.go` | `1043`、`1102-1110`、`1286-1293`、`2028`、`2183` | F13/F20/F31；`ToolWhitelist: task.ToolsWhitelist` |
| `backend/internal/agent/child_factory.go` | `67`、`84`、`97-124` | F32、双路径共用解析入口 |
| `backend/internal/agent/agent.go` | `1070-1092` | F32 |
| `backend/internal/prompt/environment_context.go` | `170-183` | F22 |
| `backend/internal/prompt/tool_guidance.go` | `13-15`、`71-76` | F23（含 nil facts 兼容约定） |
| `backend/internal/output/tool_result_content.go` | `23-26`、`994-1004`、`1087-1095` | F24/F27 |
| `backend/internal/executor/output_capture.go` | `550` | F25 |
| `backend/internal/toolresult/kind.go` | `153-197` | F26 |
| `cmd/aicli/commands/skills_integration.go` | `800-803` | F26 |
| `backend/internal/artifact/store.go` | `97-120`、`274-300`、`302-351`、`354-389` | F28/F29/F30/F33 |
| `backend/internal/toolkit/tools/artifact_read.go` | `74-84`、`94-120` | F13、只读语义与 store 绑定 |
| `backend/internal/agent/batch_lifecycle.go` | `16-17`、`52-53` | F34 |
| `backend/internal/agent/prompt_builder.go` | `23-25`、`57`、`85-93` | F35/F37/F38 |
| `backend/cmd/aicli/commands/chat_actor_host.go` | `2618` | F36 |
| `backend/cmd/aicli/commands/chat_session.go` | `2726-2732` | F36 |
| `backend/internal/prompt/toolguidance/toolguidance.go` | `23-33` | F39 |
| `backend/internal/output/gateway.go` | `141-168`、`320-332` | F43 |
| `backend/internal/agent/loop.go`（主面过滤） | `4410`、`4761-4778` | F45 |
| `backend/internal/policy/capability.go` / `capability_scope.go` | `87-105` / `134` | F48 |
| `backend/internal/policy/engine.go` | `591-601` | F51 |
| `backend/internal/agent/subagent_context.go` | `42-72` | F52 |
| `backend/internal/api/runtimeapi/instruction_messages.go` | `141-143`、`177-183` | F50 |
| `backend/internal/api/runtimeapi/handler.go` | `2200-2201` | F50 |
| `backend/internal/api/runtimeapi/agent_context.go` | `25-35` | F50 |
| `backend/internal/agent/loop.go`（runLineage） | `232-237` | F34 |
| `docs/tools/unified-tool-registry-plan.md` | `135`、`231-241` | L5、2026-10-16 决定 |

### 11.2 术语表

| 术语 | 含义 |
|---|---|
| **工具面（tool surface）** | 一次运行中模型可见/可调用的工具集合。本轮冻结面见 `frozenTurnToolSurface` |
| **面外调用** | 模型调用了不在其冻结工具面内的工具。属提示泄漏或历史残留，非模型幻觉 |
| **输出契约** | 网关折叠超限输出、内联 `art_…` 指针，并承诺可取回的一整套约定 |
| **契约性工具（Contractual）** | 由运行时契约授予、独立于用户 allowlist 的工具（本文档新增维度，目前仅 `artifact_read`） |
| **lineage scope** | 同一 root 会话树内的 session 集合，用于解引用授权范围 |
| **契约降级（显式）** | 契约被显式 deny 时，把指针文案替换为"尾部不可取回"的显式说明（实现 I6 的兜底，非主方案） |
| **静默降级** | 不给读取端、也不改文案，让模型误以为看到完整输出。**本方案明确禁止** |

### 11.3 变更记录

| 日期 | 变更 |
|---|---|
| 2026-10-18 | 初稿。基于批次 `batch_d41625fd477e057f` 事故与当日全量代码核查（F1–F34） |
| 2026-10-18 | 第二轮复查：确认 D-1；新增 F35–F42（子代理提示继承链）；重写 §3-L1、§5.2–§5.5；新增 P0-0/P0-2b 与 T12–T16；新增 §10 整体逻辑梳理与合理性审查 |
| 2026-10-18 | 第三轮修正：**推翻上一版 D-1**（allowlist 未列出 ≠ 显式拒绝）；授予点从子代理层上移到策略层（新增 F43–F48）；补全显式 deny 的分类降级与 T17–T19 |
| 2026-10-18 | 第四轮闭环审计：新增 F49–F52（通配闸门、runtimeapi 提示形状、引擎口径、fork 消息过滤）；Q1 关闭；新增 §10.6（W1–W3）与 T20–T21；明确“闭环但不完全自洽”的边界 |
| 2026-10-18 | 实施：W1/W2/W3/P0-2 落地（见 §10.7）；修正 L4 对既有 session 检查的表述 |
| 2026-10-18 | 实施：P0-0/P0-1/P0-3/P1-1/P1-3/P2-1 全部落地；P0-1 收敛为“只读类面外调用非致命”，契约追加移到只读过滤之后（见 §10.7） |

### 11.4 一句话总结

> `artifact_read` 不是"要不要豁免的工具"，而是**输出契约的读取端**。
> 修法不是放开 allowlist，而是**给它一条独立于 allowlist 的授予通道，并把读取范围钉在自己的 lineage 上**；
> 同时让提示跟随工具面、让面外调用不否决已完成的交付。