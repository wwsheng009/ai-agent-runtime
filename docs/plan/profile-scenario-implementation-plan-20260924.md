# Profile 场景化上下文裁剪 — 实施方案（Implementation Plan）

> 状态：**实施中**（2026-09-24 制定；同日 Batch 0/1/2/3/5/7/10/11a 已实施并验证，其中 Batch 7 的 server 半程待 V27 定位后接线——见附录 P 执行跟踪表与变更记录）
> 依据：`docs/plan/profile-scenario-context-pruning-plan-20260924.md`（下称"设计文档"：D1-D29 / Batch 0-14 / R1-R24 / 开放问题 1-22 / 附录 A-E）
> 定位：设计文档回答"做什么、为什么"；本方案回答"怎么落地、按什么顺序、谁验收、如何回滚"。设计论证不重复，只引用 §编号。
> 范围：backend（`internal/profile`、`internal/profileinput`、`internal/skill`、`internal/mcp`、`internal/agentconfig`、`internal/chat`、`internal/api/*`、`cmd/aicli`）+ frontend + docs。
> 纪律继承：**禁止假开关**（单一权威生效点 + 断言测试）；**不引入第二套语法/方言**；**profile 只能收窄安全基线**。

## 0. 30 秒速览

- **目标**：让用户用 profile 把「工具 / skills / MCP / prompt / 配置覆盖」按场景裁剪，并在 CLI、TUI、Web 三个入口完成「创建 → 校验 → 使用 → 切换 → 分享」全生命周期闭环；项目级 profile 在未信任工作区受分级门控。
- **体量**：14 个批次（Batch 0-14）、P0/P1 合计 ≈ 20 人日；单人 ≈ 4 周，后端/前端双人 ≈ 3 周日历（参考估算，见 §2.4）。
- **关键路径**：`V 表核实 → Batch 0 → 1 → 2 → 3`（P0 基础线）与 `Batch 10 → 11a → 12 → 8 → 13 → 14`（热切换 → 前端 → 闭环 → 安全收口线）。
- **最大风险**：R1（过滤链路未核实就编码）、R15（turn 前缀撕裂）、R22/R23（引用悬空 / 导入供应链）、R24（双写冲突）。
- **最先做的事**：§3 的 V 表核实（0.5-1 天）与 §11 的决策拍板——两者是全部批次的启动门禁。

## 1. 范围与目标

### 1.1 本期交付范围（需求 → 批次追溯）

| 需求 | 内容 | 承载批次 | 目标状态 |
|---|---|---|---|
| FR-1 | `aicli profile` 命令组 | Batch 2 | `list/show/validate/create` 可用 |
| FR-2 | 四内置模板 | Batch 2 | `create --template coding\|review\|minimal\|docs` 一键生成；review/minimal 工具面 ≤ 全量 40%（§5 验收 1） |
| FR-3 | Skills 粒度裁剪 | Batch 1 | allow/deny 生效，deny 优先，与目录替换正交 |
| FR-4 | MCP server 级选择 | Batch 1 | use/exclude 过滤后**不连接、不注册** |
| FR-5 | 量化与生效反馈 | Batch 3 | `profile show` + 启动摘要含计数与 token 估算；解析失败显式报错（不静默回退） |
| FR-6 | 优先级确定性 | Batch 3 | flag > `default_profile` > env 关系文档化 + 测试锁定 |
| FR-7 | Prompt append 模式 | Batch 1 | append 叠加顺序固定 + 顺序断言；默认 replace 行为不变 |
| FR-8 | 会话内切换 | Batch 10 / 11a（原 Batch 4 收敛，见 §2.2） | `/profile use` 下一 turn 生效；A1/A2/A3 全绿 |
| FR-9 | 子 agent 继承 | Batch 5 | 子会话工具面 ⊆ 父 profile 允许集 |
| FR-10 | exec 元数据 | Batch 3 | exec JSON 含生效 profile 与工具面摘要 |
| G1 | 创建闭环（三入口一语义） | Batch 13 | E2E-1/2 通过；A9 全绿 |
| G2 | 修改补全（rename/move/delete + 引用检查） | Batch 13 | E2E-3/5 通过；A10 全绿 |
| G3 | `default`/`apply` 拆分 | Batch 12/13 | A12 全绿 |
| G4 | TUI 生命周期子命令 | Batch 11b / 13 | 手工剧本：TUI 内完成 create→use |
| G5 | 导出/导入/项目级分发 | Batch 13 | E2E-4 通过；A11 全绿 |
| G6 | 项目级 profile 信任门控 | Batch 14 | E2E-6/7 通过；A13/A14 全绿 |
| G7 | E2E 剧本 | Batch 13/14 | E2E-1~7 全部通过 |

### 1.2 明确不做（本期）

- FR-11~FR-14（P2）：`--profile auto` ✅（slice 1）、usage 按 profile 聚合 ✅（slice 2/2b）、runtime-server 只读 API 扩展 ✅（随 Batch 8 核实回填）、项目级绑定 ✅ **第一阶段**（2026-09-25：只读发现 + 显式应用；**自动默认激活 / 多工作区自动解析 / 默认开启策略仍后置**，见 `fr14-profile-binding-implementation-plan-20260925.md`）；
- D12 模式 C（`runtime.base` 引用合并）与 M1 文件层插入（设计文档明确不推荐，§8.3）；
- 软删除（`.trash/`；开放问题 20 倾向硬删）；单文件内联导出（开放问题 21 倾向目录/zip）；
- headless（exec/ACP）运行期切换（开放问题 18：保持启动期解析，`agent_stdio.go:685` 现状即正确）；
- runtime-server 进程级策略（auth/mutation/usage/governance policy）纳入 profile（§9.2 明确排除）。

### 1.3 总验收目标

引用设计文档 §5 五条并追加安全线（第四轮）：

1. **量化**：`profile show minimal/review` 工具 schema 数 ≤ 全量基线 40%；请求 artifact（`~/.aicli/chat-logs/*_request_*.json`）验证被排除工具确实不在 `tools[]`；
2. **可用**：`aicli profile create review --use` → `aicli chat` 两条命令即可使用；
3. **校验**：`profile validate` 检出五类错误（工具名不存在 / skill 不存在 / mcp server 不存在 / prompt mode 非法或文件不可读 / allow-deny 同名冲突）；
4. **回归**：无 profile 场景全部既有测试通过、行为**逐字节零变化**（NFR-1）；
5. **多入口一致**：chat / exec / agent stdio 对同一 profile 解析出的工具面与 prompt 一致（表驱动测试）；
6. **安全**（第四轮追加）：未信任工作区项目级 profile 分级门控生效（A13/A14）；导入不自动激活（A11）；删除引用完整性检查生效（A10）。

## 2. 实施策略

### 2.1 五条实施原则

1. **先核实后编码**：§3 V 表是硬门禁——对应批次启动前必须回填（延续设计文档附录 A-E 的"先核实再实施"纪律）；
2. **每批次独立可验证**：以 §4 任务卡的 DoD 为出口，未达 DoD 不进入下一批次；
3. **先接线后 UI**：后端语义先冻结（Batch 1/7/10），前端只消费 API——估算口径（`internal/profile/estimate.go` 单点）、Switch Report 投影、引用检查结果均单一来源；
4. **安全收口不可裁剪**：Batch 14 是 **P0**，任何排期压缩不允许跳过 D29 门控；
5. **零破坏默认**：每个批次合并时，无 profile 路径行为逐字节不变（NFR-1），作为合并前回归门禁。

### 2.2 依赖图与关键路径

```text
Phase 0（V 表核实 + 决策拍板）────────── 全批次门禁
│
├─【P0 基础线】   Batch 0 ──▶ Batch 1 ──▶ Batch 2 ──▶ Batch 3
│                              │
├─【P1 覆盖线】                ├──▶ Batch 7 ──▶ Batch 9
│                              │
├─【P0 热切换线】              ├──▶ Batch 10 ──▶ Batch 11a
│                              │                    │
├─【P1 前端与闭环线】          │                    └──▶ Batch 12 ──▶ Batch 8 ──▶ Batch 13（含 11b）
│                              │
├─【P0 安全线】                │                                              Batch 13 ──▶ Batch 14
│                              │
└─【P1 收尾】                  └──▶ Batch 5（子 agent 继承）；Batch 6（P2，按需）
```

**执行级修正（相对设计文档的批次顺序说明，实施时以此为准）**：

1. **Batch 4 不单独执行**：第一轮 Batch 4（P1 会话内切换）与第三轮 Batch 10-11 同题，后者（D18-D23）是前者的完整化；实施以 Batch 10-11 为准，Batch 4 的测试项并入 A1-A8；
2. **Batch 11 拆两段**：**11a**（核心命令：status/list/show/diff/use/pick/reload/off/save，P0）仅依赖 Batch 10；**11b**（生命周期子命令：create/duplicate/save-as/edit/rename/move/delete/export，§23 G4）随 Batch 13 后端就绪落地（同一命令文件，命令 spec 可先注册）；
3. **Batch 8 与 12 的 API 依赖顺序**：12 先消费只读 `GET /api/runtime/profiles`（composer 候选目录），8 再补齐写端点（put/validate/preview + 13 的生命周期端点）；
4. **Batch 9 依赖 Batch 7**（白名单先行）；**Batch 5** 在 Batch 1 完成后即可并行启动。

### 2.3 里程碑

| 里程碑 | 内容 | 批次 | 出口判据 | 参考工期 |
|---|---|---|---|---|
| **M0** | 核实与决策就绪 | Phase 0 | V 表全部回填；§11 决策清单拍板 | 0.5-1 天 |
| **M1** | CLI 场景化裁剪可用 | 0-3 | §1.3 的 1-3 条 + 零变化回归 | ≈5 人日 |
| **M2** | 热切换可用（TUI） | 10, 11a | A1/A2/A3/A6 全绿；TUI 手工剧本 | ≈2.5 人日 |
| **M3** | 覆盖与开关 | 7, 9 | 白名单/零变化断言；T1 开关逐项断言 | ≈2.5 人日 |
| **M4** | Web 与前端 | 12, 8 | composer 切换 + Profiles 页可编辑 | ≈4.5 人日 |
| **M5** | 生命周期闭环 | 13（含 11b） | E2E-1~5 + A9-A12 | ≈3 人日 |
| **M6** | 安全收口 → **发布就绪** | 14 | E2E-6/7 + A13/A14 | ≈1.5 人日 |
| 收尾 | P1/P2 | 5, 6 | 子 agent 继承测试通过；P2 按需 | ≈0.5 人日 |

### 2.4 人力与排期（参考估算）

| 路径 | 说明 |
|---|---|
| **单人** | 按 M0→M6 顺序串行，≈20 人日（4 周） |
| **双人** | 后端（Batch 0/1/2/3/7/9/10/13 后端/14）+ 前端（Batch 8/12/13 前端）并行；≈3 周日历 |
| **关键交接物** | ① §10.5 API 契约冻结（Batch 12 启动前）② Switch Report JSON 结构（Batch 10 启动前）③ E2E 剧本可执行化（Batch 13 启动前） |

> 估算为参考值，以团队实际吞吐校准；Batch 10/11/12 沿用设计文档 §19 的原始估算（1.5/1/1.5 天）。

## 3. Phase 0：前置核实与工程准备（阻塞门禁）

### 3.1 核实总表 V1-V27（合并设计文档附录 A/C/D/E，去重）

> 每项核实完成后：① 回填**设计文档对应附录**（保持单一事实源）；② 更新本表"状态"列；③ 若结论改变实现路径，同步修正 §4 对应任务卡。

| # | 核实项 | 来源 | 阻塞批次 | 核实方式 | 状态 |
|---|---|---|---|---|---|
| V1 | MCP 工具进入 CLI chat function registry 的确切注册函数 | 附录 A1 | 1 | 读 MCP 注册链路 | ✅ 已回填（Batch 1）：selection 经 `chat_setup.go:265-268` 落到 session，`mcp_integration.go:237` 消费；server 侧 `profile_support.go:391 resolveProfileMCPAdapter` 按 selection 构建 |
| V2 | agent 层 tool surface（`internal/agent/tool_list.go` 的 `ShouldList` 路径）是否受 ToolPolicy 过滤（覆盖 exec/headless 与子 agent） | 附录 A2 | 1, 5 | 读代码 + 未过滤实验 | ✅ 已回填（Batch 1）：exec/headless 路径经 `exec_run.go:268` 判定 profile 生效；子 agent 继承已于 Batch 5 落地（绑定快照 + `DeriveChild` 只收窄，见 V5） |
| V3 | skill registry 构建函数（allow/deny 插入点；CLI `skills_integration.go` + server 侧） | 附录 A3 | 1 | 读代码 | ✅ 已回填（Batch 1）：CLI `skills_integration.go`；server 侧 `internal/api/skills/handler.go:1732/1805`（运行时 registry/context 注入） |
| V4 | 内置工具名清单权威来源（优先复用 `chatcore/catalog.go` 构建入口，避免手工清单漂移） | 附录 A4 | 1, 2 | 读 `catalog.go:210-222` 及构建入口 | ✅ 已回填（Batch 2）：`profile_validate.go:123-137 validateToolNames` 以登记清单校验；未登记=warning（MCP/动态工具合法出现在名单中） |
| V5 | spawn 子会话 ToolPolicy 传递现状（D8 前置） | 附录 A5 | 5 | 读 spawn 装配处 | ✅ 已回填（Batch 5）：**API 侧曾缺失继承**——`sessionAgentController.Spawn` 只写父子/根/深度上下文，从不复制父 profile 绑定 → 子 actor `profileState == nil`、工具面可宽于已收窄父策略；本地侧与父共用 `ChatSession`/`apiAgent` 天然继承。已由 `sessionmeta.CopyProfileBinding` 单点快照 + 两个 spawn 装配点修复（子策略 = 父允许集 ∩ 子 agentdef 声明，`DeriveChild` 只收窄）。证据：`session_runtime_support.go:604-611`、`chat_actor_registry.go:715-722`、测试 `session_profile_inheritance_test.go`（5 例） |
| V6 | `/api/runtime/capabilities` 返回内容（是否含内置工具全清单与定义） | 附录 C1 | 8 | 读 `handler.go:802` + 实测响应 | ✅ 已回填（Batch 8 前置，2026-09-24）：**不含内置工具全清单**——仅 skills 能力描述 + agent 单条描述（`handler.go:1568-1586`、`skill/capability.go:87-100`）；会话级生效工具在 `GET /sessions/{id}/runtime/tools`（`session_runtime_handlers.go:484-536`）→ 工具面卡片数据源 = profiles get 视图 |
| V7 | agents 清单端点（`api/runtime/agents.ts` 对应后端路由与语义） | 附录 C2 | 8 | 读 + 实测 | ✅ 已回填（Batch 8 前置，2026-09-24）：**不是 profile 级 agent 清单**——`agents.ts` = `GET /api/runtime/agent-control/agents`（运行中子代理身份图，`handler.go:963`）→ Agents 卡片数据源 = profiles get 视图的 `agents` 字段（profile spec `agents` map） |
| V8 | `AICLISubagentsConfig` / `AICLITeamsConfig` 内部字段（是否已有 enabled 语义） | 附录 C3 | 9 | 读 `config.go:650-659` | ✅ 已回填（Batch 9，2026-09-24）：**无 `enabled` 语义**——两者仅有 `routing` 子节（teams 缺 routing 时回落 subagents.routing）→ T2 只能纯工具面（denylist）建模，**不新增配置开关** |
| V9 | supervision 审批可配置性（档位/超时/策略） | 附录 C4 | 9 | 读 `agentconfig/config.go:43` + `internal/supervision/config.go:18-121` | ✅ 已回填（Batch 9，2026-09-24）：**可配置但属宿主级 supervision 运行时预算/灰度**（`wake_max_*`/`turn_end_check`/`progress_check_interval`/`approval_terminal_guard`/`message_semantics_v2`），非"审批偏好档位" → **不纳入** profile 覆盖目录（避免假开关） |
| V10 | `WorkspaceSpec` 消费状态（决定是否纳入本期） | 附录 C5 | 7 | 读 `profile/spec.go:51-55` + grep 消费点 | ✅ 已回填（Batch 7 前置，2026-09-24）：**已消费**——定义在 `profile/spec.go:73-78`（Provider/Model/Tools）；`loader.go:35 LoadWorkspace` 被 `resolver.go:36` 调用，作为 `workspace.file` 层参与 provider/model/tool-policy 解析（`resolver.go:209`）→ 项目级绑定可复用既有层；Q12 于 2026-09-25 **分阶段解冻**：第一阶段（只读发现 + 显式应用）已落地（`internal/profile/binding.go`、`LayerProfilesForWorkspace`、列表端点 `project_binding`/`is_bound`），自动默认激活仍后置 |
| V11 | `skills_runtime.enabled=false` 时 profile 级 skill 目录的行为 | 附录 C6 | 9 | 读 `skills_integration.go:944-951` + `config.go:42` | ✅ 已回填（Batch 9，2026-09-24）：**一并关闭**——`enabled=false`（或 `SkillsRuntime==nil`）时在解析 skill 目录**之前**早退，profile 级 `ResolvedSkillDirs` 不再被扫描；键路径为**配置根** `skills_runtime.*`（Batch 7 白名单已修正，旧 `aicli.skills_runtime.*` 是 dormant 假开关） |
| V12 | `mcp.merge_strategy` 语义（定义接线或从 spec 删除） | 附录 C7 | 7 | 读 `profile/spec.go:34` | ✅ 已回填（Batch 7 前置，2026-09-24）：**零生产消费点**（全仓 grep 仅 `spec_test.go` 解析测试）→ dormant（R9）；按 Q11 **从 spec 移除**，validate 对未知字段报错；MCP 选择语义由 `use_servers`/`exclude_servers` 承担（Batch 1 已落地） |
| V13 | `SessionActor.InvalidateStableToolSurface` 能否从命令路径直接取得句柄（vs 走 SessionHub 全量失效） | 附录 D1 | 10 | 读 `hub.go:128` + `chat_actor_host.go` 持有关系 | ✅ 已回填（Batch 10）：**可以**——`session.LocalRuntimeHost.SessionHub.Get(sessionID)` 直接返回 `*SessionActor`（`hub.go:77-103`），命中即调 `actor.InvalidateStableToolSurface(ctx)`（`hub.go:149` 同款用法）；未命中退化为 `SessionHub.InvalidateStableToolSurfaces`（`:128-154`）。落地：`chat_profile_switch.go invalidateChatStableToolSurface`（报告 `tool_surface_scope=actor\|hub\|none`） |
| V14 | `FrozenTurnTools` 在 turn 终止时是否自动清空；actor 重建是否可能发生在 turn 中途（决定"立即删除锚点"vs"pending 落地"） | 附录 D2 | 10 | 读 `session_actor*.go` + `turn_tool_surface_snapshot_test.go` | ✅ 已回填（Batch 10）：turn 冻结面为 turn 作用域（turn 起点与终态均清空），存储层在 `CurrentTurnID != ""` 时**保留**（`session_runtime_store.go:555-590`、`turn_tool_surface_snapshot.go:134-172`）；compose 的两个调用点都在 run 起点（`chat_actor_host.go:1848` actor 构建 / `:2119-2145` 每 run 一次的 prepare 钩子），在途 turn 的 head 已 materialize 进活体 agent 的 `cfg.SystemPrompt` → 结论：**立即删除锚点**，不新增 `pending_profile_switch`（设计 §18.2 核实项 2） |
| V15 | Web 命令路径的 actor 句柄来源（`chatWebSession()` 与 `SubmitSessionRuntimeCommand` 的关系） | 附录 D3 | 12 | 读 `session_runtime_handlers.go` + host/session 查找 | ✅ 已回填（Batch 12，2026-09-24）：**server 路径不经 `chatWebSession()`**——`SubmitSessionRuntimeCommand` 走 `h.sessionManager` + `h.peekSessionHub()`（只读探测、不懒加载，`agent_control_runtime_state.go:104-110`）；`set_profile` 分支**先于** hub 解析处理（`session_runtime_handlers.go:876-894`），既不为一次切换凭空建 actor、也不受"会话租约被别的宿主持有"影响；actor 句柄由 `hub.Get(sessionID)` 精确取得（`session_profile_switch.go:329-377`），无活体 actor 时下一次 `GetOrCreate` 按新 sessionmeta 重建 → 无需 hub 全量失效 |
| V16 | 前端 `use-composer-command-executor.ts` 现有分支结构（`/model` 如何调 API、popupSelect 如何回填） | 附录 D4 | 12 | 读该文件 | ✅ 已回填（Batch 12，2026-09-24）：分支 = `switch (command.key)`（`use-composer-command-executor.ts:503-511`）；`/model` 的 `runModel`（`:264-304`）即"解析参数 → 调 API → 失败提示"样板，`/profile` 的 `runProfile`（`:306+`）逐段同构；popupSelect 候选由 `use-composer-command-surface.ts:142-151` 注入（读 `runtimeProfiles.sessionSwitch` 决定是否注册，R20）、`:228` 取 `profile` 命令，选中值经既有 `runCommand(key, value)` 通路回填 |
| V17 | 会话层写回实现（`/routing save --to session`；profile `save --to session` 的姊妹项） | 附录 D5 + E7 | 11a, 13 | 读 `chat_routing_layers.go` / `chat_routing_command.go` | ✅ 已回填（Batch 13 E7 复核，2026-09-24）：**复用既有存储、无第二套写回**——① 层常量单一来源（`chat_routing_command.go:34-36` session/workspace/config），`/profile save` 直接复用；② **session 层零写回**：会话绑定经 sessionmeta ⑩ 持久化（`chat_profile_switch.go` 身份 4 键 + resume 回填），命令只回显"无需额外保存"（测试 `TestProfileCommandSaveSessionLayerIsNoOp` + A4 `TestProfileSwitch_IdentityPersistsAcrossResume`）；③ workspace 层 = `agentconfig.WorkspacePrefsPathForPath` + `UpdateProfilesConfig`（与 `/routing save --to workspace` 同 prefs 文件族、不同 section；N9 不回退 cwd，`TestProfileCommandSaveWorkspaceLayerWritesPrefs`）；④ config 层 = `ensureWritableAICLIConfigPath` + `UpdateProfilesConfig` + `--yes` 门禁，写后 `chatProfileFileDeclaresDefaultProfile` 只读校验——分层路由把 profiles 段写到归属层时如实提示"未落在上述路径"（不谎报成功）；⑤ 写前门禁：子会话只读拒绝 / 无绑定拒绝 / 未知层拒绝（不静默回退默认层）；⑥ 干净 HEAD（`b522f2f6`）定向 6 例全绿（0.635s） |
| V18 | 切换后重组 prompt 时 `<environment_context>` 块是否与冻结值字节一致（⑧保留、①重建） | 附录 D6 | 10 | 读 `buildLocalChatSystemPrompt`（`chat_actor_host.go:2278+`） | ✅ 已回填（Batch 10）：环境块由 `sessionmeta.EnvironmentContextBlock` 承载并在会话期冻结（`sessionmeta.go:51-54`）；切换只删 `SystemPromptFrozen` 锚点（`clearFrozenChatSystemPromptAnchor`），**不触碰环境块**，下次 compose 复用同一冻结值 → ⑧保留、①重建 |
| V19 | `chatWebSession()` 单例假设在 runtime-server 多会话下的适用性 | 附录 D7 | 12 | 读 `chat_mcp_surface_invalidation.go:40-48` | ✅ 已回填（Batch 12，2026-09-24）：`chatWebSession()` 是 **aicli 进程内 web 会话单例**（`cmd/aicli/commands/web_handlers.go:35-37`，全部调用点都在 `cmd/aicli/commands`）；runtime-server **不适用也不引入**——server 按 sessionID 精确失效（`session_profile_switch.go:329-377`：`peekSessionHub` + `hub.Get(sessionID)`），多会话并存互不影响 |
| V20 | `foldertrust.Resolution` 消费方式（是否已有"按资源类型门控"先例，如插件/MCP 目录） | 附录 E1 | 14 | 读 `internal/foldertrust` + plugin/MCP 发现处 | ✅ 已回填（Batch 14 前置，2026-09-24）：**先例 = 单一 Resolution + 按资源类型消费**。Resolution 启动早期解析（`chat.go:582-585` 注释“before profile/plugin discovery”；`agent_stdio.go:682-683`、`exec_run.go:259-260`、`exec_resume.go:95-96`），经 `applyChatFolderTrust`（`chat_folder_trust.go:143-149`）挂会话；访问器 `sessionProjectScopeAllowed`（:99-107）session 优先、回落进程级。既有门控点：插件 `plugin_runtime.go:28`；MCP `mcp_integration.go:213-218`（未信任 + `IsProjectScopedPath` → 项目级 MCP 配置不加载）+ `acp_mcp_host.go:549`；agentdef `agentdefDiscoverOptions`（`chat_folder_trust.go:129-140`，未信任只 `SkipProjectRoot=true`，**builtins/user-home/profile root 仍放行** → 项目 profile 内 agents prompts 今日零门控，缺口实锤）。D29 沿用此模式：门控落在 **prompt 的解析/消费边界**（`resolved.Prompts` → `profileinput.LoadPromptText/LoadPromptLayers`），tools/skills/mcp/permission_mode/overrides 不受影响（分级）。**时序陷阱**：`chat_setup.go` 先投影 profile（:248-250）后挂 trust（:260）→ 门控读**进程级** `currentFolderTrust()` 而非 `session.FolderTrust` |
| V21 | `.aicli/profiles` 目录发现是否已被其他路径**间接门控**（确认 D29 缺口不是重复门控） | 附录 E2 | 14 | 全仓 grep `profiles`×`foldertrust` 交叉点 + 未信任目录加载实验 | ✅ 已回填（Batch 14 前置，2026-09-24）：**未被任何路径间接门控（缺口确认，非重复门控）**——`internal/profile` 与 `internal/api/skills`（server 消费面）对 foldertrust 零引用（唯一命中 `internal/profile/overrides.go:103-104` 是白名单排除域名，与门控无关）。**关键实锤**：`foldertrust.CollectRepoConfigKinds`（`configs.go:31-108`）marker 扫描只有 plugins/hooks/mcp/agents，**不含 `.aicli/profiles`** → 只有 `.aicli/profiles/` 的仓库 `RepoConfigsPresent=false` → `Decide` 第 4 步直接 trusted（`decide.go:51-53`）= 只读 `res.Trusted` 的 D29 门控在 E2E-6 场景**恒放行（假门控）** → Batch 14 必须同步扩展检测面（新增 `ConfigKindProfiles` + marker `.aicli/profiles`，提示语 kindList 同步）；server 侧入口 `resolveProfileSessionState(profileRef, agentID, workspacePath)`（`profile_support.go:163`）已带 workspacePath，可在此计算信任 |
| V22 | `mcp-form.tsx` 的 CRUD 表单模式可否复用于 profile 模板向导 | 附录 E3 | 8, 13 | 读 `sections/modes/mcp-form.tsx` | ✅ 已回填（Batch 8 前置，2026-09-24）：**可复用**——`mcp-form.tsx`（470 行）= 草稿类型 + 纯函数文本↔请求映射 + 表单组件 + 独立测试（`mcp-form.test.ts`），独立于 config document 草稿（提交时调 `/api/runtime/mcps`，父面板负责反馈/刷新）→ profile 创建向导按同一结构（`ProfileDraft` + 映射纯函数 + 测试） |
| V23 | `aicli init` 的模板生成机制（`init.go`）能否复用为 profile 脚手架 | 附录 E4 | 2 | 读 `backend/cmd/aicli/commands/init.go` | ✅ 已回填（Batch 2）：采用独立 `internal/profile/templates.go`（go:embed）+ `templates/{coding,review,minimal,docs}/`；模板↔样例同源一致性由 `consistency_test.go` 锁定 |
| V24 | 会话 `profile_ref` 在 server 侧的写入点（`/api/agent/chat` 路径）——引用检查必须覆盖 | 附录 E5 | 13 | grep `ProfileRef`/`profile_ref` in `internal/chat`/`internal/api` | ✅ 已回填（Batch 13 slice 10，2026-09-24）：server 侧会话绑定的**唯一写入点** = `applyProfileSessionContext`（`internal/api/skills/handler.go:3931-3967`）——经 `sessionmeta.Set` 写四个键：`ProfileRef`（同时镜像 legacy 别名 `LegacyAPIProfileReference`）、`ProfileName`、`ProfileAgent`、`ProfileRoot`（`:3948-3955`）；`/api/agent/chat` 首轮落地与 `set_profile` 切换共用它。消费面（只读）：请求回落会话绑定 `handler.go:1751-1755`、Switch Report 基线 `session_profile_switch.go:279`。**引用检查覆盖确认（D25）**：`collectRuntimeProfileReferences`（`profiles_lifecycle_handlers.go:309+`）对每个会话**同时**读 `ProfileRef` / `LegacyAPIProfileReference` / `ProfileRoot` 三类键并按 `sameFilePath` 比对（`:359-369`，有界扫描 500 条 `:27`）→ server 创建的会话与 CLI 会话共用同一 sessionmeta 键空间与同一存储，**无遗漏写入点** |
| V25 | 原子写工具在 `profile.yaml` 上的适用性（rename/move/delete 与 R24 冲突检测依赖） | 附录 E6 | 13 | 定位既有原子写工具及配置写入处用法 | ✅ 已回填（Batch 13 slice 10，2026-09-24）：**既有原子写工具可直接复用**——`writeProfileYAMLAtomic`（`internal/api/skills/profiles_store.go:447-449`：同目录临时文件 + rename）是 profile.yaml 的写盘通道（G2 编辑/保存：`profiles_write_handlers.go:349`）；`agentconfig.writeFileAtomic`（`chat_persistence.go:233`）是 config.yaml 的唯一落盘通道，由 `configwriteguard` 把守（`guard_test.go:35/124/276` 钉住"只有这两条通道"）。**逐场景判定**：① 内容写（编辑/保存）= 原子写；② **rename 的声明名改写 `profile.RewriteProfileName` 用 `os.WriteFile`（`internal/profile/rewrite.go:53`，非原子）**——可接受：它只在目录 rename 成功后改单行标量，且调用方 `chatProfileRenameLifecycle`（`cmd/aicli/commands/chat_profile_lifecycle_ops.go:39-51`）在失败时**回滚目录名**（D34「失败路径状态零改动」）；③ save-as 建目录时逐文件 `os.WriteFile`（`profiles_saveas_handlers.go:227-238`）——目标是本次新建目录（`preExisted` 守卫），失败即 `os.RemoveAll` 回滚，不会半写覆盖既有 profile。**R24 冲突检测依赖**：rename/move 目标已存在 → **409 拒绝**（`profiles_lifecycle_handlers.go:194`、`:266`），不合并、不静默覆盖；config 改写顺序 = **先写新键再清旧键**（`:452-457`），保证 default/查询在写入过程中始终可解析 |
| V26 | turn 生命周期决定"立即删除 prompt 锚点" vs "`pending_profile_switch` 到 turn 边界"（设计 §18.2 核实项 2） | 设计 §18.2 | 10 | 读 compose 调用点 + 存储层在途保留语义 + `SessionActor.RunInFlight()` | ✅ 已回填（Batch 10）：采用**立即删除**（不新增 pending 机制，D20）；A3 覆盖分层见设计 §18.2/§18.3 |
| V27 | runtime-server `/api/agent/chat`（`internal/api/skills`）侧 `runtime.overrides` 的生效点：该路径**不经过** `cmd/aicli/commands.resolveChatProfileState`（server 侧自有一份 `profile_support.go` 镜像解析），需确认哪些 server 侧决策读 `agentconfig.Config` 才能落覆盖（D13 server 半程） | 设计 D13 | 7（server 半程）、12 | 读 `internal/api/skills/profile_support.go`（`profileRuntimeState` 字段）+ `handler.AgentChat` 的配置消费点 | ✅ 已回填（Batch 7 server 半程，2026-09-24）：server 侧消费面 = `Handler.aicliConfig`（`SetAICLIConfig` 快照，`handler.go:164/450-550`）→ `runtimeSkillsConfig()`（`:8225`，注入点 `:1962`/`:2419`）、`subagentRoutingConfig()`（`:460`→`:4167`）、`teamRoutingConfig()`（`:468`→`team_handlers.go:132`）、`mainAgentRoutingConfig()`（`:476`）、`defaultReasoningEffort()`（`:496`→`:1591`）。**请求级已接线**：`profileRuntimeState.ConfigOverlay` + `buildProfileConfigOverlay`（基线=宿主快照、零变化返回 nil、不就地修改）+ `skillsRuntimeConfigFor` 消费于 AgentChat catalog/exposure 注入 → `skills_runtime.*` **字段级**白名单（`overrides.go:63-70`：`enabled`/`skill_dir(s)`/`extra_skill_dirs`/曝光 mode+top_k）真实生效；`catalog_budget_chars`/`document_mode`/`discipline_block` 不在放行集（UI 不得展示编辑入口）。**会话级余项（登记）**：routing 三件套在 actor 构建/spawn/team 编排期读取，调用点不携带 profile 解析结果，需 actor 缓存失效矩阵配套（R10）→ Batch 12 前置评估项 |

### 3.2 门禁规则

1. **V 表未回填 → 对应批次不得启动**（"阻塞批次"列为空的项不阻塞任何批次，可后置）；
2. **设计分支依赖**（结论可能改变实现路径，优先核实）：
   - **V13/V14**（Batch 10）：决定 §18.2 采用"立即删除锚点"还是"pending 落地"——以 A3 断言为验收线；
   - **V20/V21**（Batch 14）：确认 D29 门控的接入位置与是否已被间接覆盖；
   - **V4/V23**（Batch 1/2）：决定内置工具名清单与模板脚手架是否复用既有入口（防漂移）。
3. **回填纪律**：结论带 `file:line` 证据；无证据不回填（延续设计文档附录风格）。

### 3.3 工程准备（Phase 0 内完成）

| 项 | 内容 |
|---|---|
| 测试基线 | 合并前先跑基线：`make test`（= `cd backend && go test ./...`）全绿；`cd frontend && npm test`（vitest）全绿；记录基线快照 |
| 回归门禁命令 | 后端：`make test`；前端：`npm test` + `npm run lint`（含 i18n/行数/备份校验脚本）；E2E：`npm run test:e2e`（playwright，Batch 13 起启用） |
| 分支策略 | 每批次一条短分支（`feat/profile-batchN-<slug>`），按里程碑 M1-M6 合并；批次间不长期分叉（Batch 8/12 前后端并行时共享 API 契约冻结件） |
| 契约冻结件 | §10.5 API 清单（16 端点）+ Switch Report JSON schema——冻结后前端可并行 mock（不阻塞后端） |
| 手工验证环境 | 准备：① 未信任测试目录（含 `.aicli/profiles/`，供 V21/A13 实验）② 两个 profile（含 prompts）③ 请求 artifact 目录可读（`~/.aicli/chat-logs/`） |

## 4. 工作分解（WBS）任务卡

> 阅读方式：每张卡给出 **目标 / 依赖 / 前置核实 / 任务（文件级）/ 测试 / DoD / 风险**。DoD 未达不进入下一批次（§2.1 原则 2）。
> 批次编号沿用设计文档；"≈x 人日"为参考估算。

### Batch 0 — 核实 spike（P0，≈0.5 人日）

**目标**：回填 V1-V5（附录 A），作为 Batch 1 的输入。
**依赖**：无 ｜ **前置核实**：V1-V5（本批次即核实动作）
**任务**：
1. MCP 工具过滤链路：确认 ① CLI function registry 注册函数 ② agent 层 `tool_list.go` 是否同受 ToolPolicy 过滤（exec/headless/子 agent 路径）；若缺口存在，记入 Batch 1 范围（R1）；
2. skill 注册构建点定位（`skills_integration.go` + server 侧对应点）；
3. 内置工具名清单权威来源确认（优先 catalog 构建入口，R6）；
4. spawn 子会话 ToolPolicy 传递现状（D8 前置）。
**产出**：设计文档附录 A 回填（file:line）+ 本方案 §3.1 状态列更新。
**DoD**：V1-V5 全部有结论与证据；若发现缺口，明确写入 Batch 1 任务清单。
**风险**：R1（缺口的范围可能在核实后扩大 → 及时反馈到 §2.3 工期）。

### Batch 1 — spec 扩展 + 三处过滤落地（P0，≈2 人日）

**目标**：spec 支持 Skills/MCP/Prompts 声明；三个消费点按声明过滤（FR-3/4/7 落地）。
**依赖**：Batch 0 ｜ **前置核实**：V1-V4（缺口结论）
**任务**：
1. `backend/internal/profile/spec.go` — 增 `SkillsSpec{Allowlist,Denylist}`、`MCPSpec{UseServers,ExcludeServers}`、`PromptsSpec{Mode}`；
2. `backend/internal/profile/merge.go` — 三层 merge 规则（profile/agent/workspace 同名集合**取并集**；deny 恒优先）；
3. `backend/internal/profile/validate.go` — mode 枚举、集合去重与冲突检查；
4. `backend/internal/profileinput/inputs.go` — `ResolvedToolPolicy` 传递契约不变；prompt mode 进入 `ComposeSystemPrompt`/注入点；
5. **skills 过滤**：`backend/cmd/aicli/commands/skills_integration.go`（V3 定位点）+ `backend/internal/api/skills` 对应路径；
6. **mcp 过滤**：chat bootstrap（`chat_bootstrap.go`/`chat_setup.go` 消费 `MCPConfigPath` 处）+ server 侧 profile runtime state（`handler.go:1825` 附近）——按 `use/exclude` 过滤 server 集合**后再连接**；
7. **prompt mode**：`chat_setup.go:255`、`handler.go:1873` 分支按 mode 选择替换或追加（append 叠加顺序固定："内置基础 → profile 组合（system→role→tools）→ context notes"，R2）；
8. 若 Batch 0 发现 agent 层过滤缺口，一并补齐（含 exec/子 agent 路径）。
**测试**：spec/merge/validate 单测；skills deny 后不进 exposure/目录/调用；mcp 过滤后不连接不注册；prompt append 与 replace 两态 + 顺序断言。
**DoD**：三类过滤在 CLI 与 server 两侧生效；单测全绿；无 profile 路径零变化。
**风险**：R1、R2、R4（与 MCP 工具级启停的优先级：profile 过滤 > mcp.yaml enabled > 工具级 enabled）、R6。

### Batch 2 — `aicli profile` 命令组 + 模板（P0，≈1.5 人日）

**目标**：`profile list/show/validate/create` 可用；四内置模板一键生成（FR-1/2）。
**依赖**：Batch 1 ｜ **前置核实**：V4、V23
**任务**：
1. 新增 `backend/cmd/aicli/commands/profile.go`、`profile_list.go`、`profile_show.go`、`profile_validate.go`、`profile_create.go` + root command 注册；
2. 新增 `backend/internal/profile/templates/{coding,review,minimal,docs}/`（go:embed）+ `templates.go` 渲染（name/description 注入）；V23 结论若支持，与 `init.go` 模板机制共用一套定义（E4）；
3. 新增 `backend/internal/profile/estimate.go`（schema token 估算，**单点实现**，前端只消费 API 返回值，R13）；
4. 模板内容要点：`review`=只读工具集 + `read_only: true` + 无 MCP + 精简 role prompt；`minimal`=最小工具集 + `skills.denylist: ["*"]` 等价开关或空目录 + 最短 prompt；`docs`=文件读写 + 搜索 + docs 类 skill allowlist + deny shell 类；`coding`=全工具基线（补齐 `examples/profiles/coding/` 的 tools 声明与 README）；
5. `examples/profiles/coding/` 补齐为完整参考（含 tools/skills/mcp 声明）。
**测试**：四个子命令路径（含错误分支）；模板渲染快照；`examples/profiles` 与模板一致性测试（R5 同源断言）。
**DoD**：`aicli profile create review --use` 后 `aicli chat` 工具面显著缩小；validate 五类错误全检出；`make test` 全绿。
**风险**：R5、R6、R12（模板生成即"可用 profile"，降低 UI 依赖）。

### Batch 3 — 反馈、exec 元数据与文档（P0/P1，≈1 人日）

**目标**：量化反馈闭环（FR-5/6/10）+ 用户文档。
**依赖**：Batch 2 ｜ **前置核实**：无
**任务**：
1. chat 启动摘要（status 通道 + quiet 抑制）；`profile show` 复用同一估算输出（口径单点）；
2. `backend/cmd/aicli/commands/exec_run.go` 输出结构增加 profile 元数据（D9/FR-10）；
3. FR-6 优先级关系（flag > `default_profile` > `DEFAULT_PROFILE` env > 无）文档化 + 测试锁定；
4. 文档：新增 `docs/aicli/profiles.md`（概念、目录约定、四模板、优先级规则、validate 语义、与 mcp 工具级启停的关系）；更新 `docs/aicli/install.md`、`docs/README.md` 索引；`docs/multi-agents/profile/profile_system_implementation.md` 追加"现状与后续"注记。
**测试**：启动摘要快照（含 quiet 抑制）；exec JSON 元数据断言；优先级表驱动测试。
**DoD**：M1 里程碑判据全部满足（§1.3 的 1-3 条 + 零变化回归）。
**风险**：R7（默认全量不变——靠文档引导采用，不改默认）。

### Batch 4 — 会话内 `/profile` 切换（**不单独执行**）

> 收敛说明：第一轮 Batch 4 与第三轮 Batch 10-11 同题；D18-D23 已给出完整设计（失效矩阵、执行核心、断言）。
> **实施动作**：以 Batch 10-11a 为准；Batch 4 原测试项（`chat_profile_test.go` 扩展、session metadata + runtime event）并入 Batch 10 的 A1-A8 断言与 Batch 11a 的 Switch Report 渲染。

### Batch 5 — 子 agent 继承（P1，≈0.5 人日）

**目标**：spawn 子会话默认继承父 profile 的裁剪策略（FR-9；agentdef 显式声明可覆盖）。
**依赖**：Batch 1（+ V5 结论） ｜ **前置核实**：V2、V5
**任务**：
1. 按 V5 结论在 spawn 装配处落地继承语义（子会话 ToolPolicy 来源 = 父 profile 允许集 ∩ agent 显式声明；**不放宽基线**）；
2. 测试：子会话工具面 ⊆ 父 profile 允许集；agentdef 显式声明且不放宽时的覆盖行为；不放宽基线断言（试图放宽 → 拒绝）。
**DoD**：继承测试全绿；不追溯已存在子代理（与 D8/§17.4 一致）。
**实施记录（2026-09-24 完成）**：按 V5 结论落地——**不在 spawn 直接复制 ToolPolicy**，而是把父会话的 profile 绑定快照进子会话 metadata（`sessionmeta.CopyProfileBinding`：4 键 canonical + legacy，只复制非空值；父未绑定时不写任何键 = 零变化），子 actor 构建期照常解析 profile，再由既有 agentdef 叠加层 `DeriveChild` 派生「父允许集 ∩ 子声明」；API（`session_runtime_support.go`）与本地（`chat_actor_registry.go`）两个装配点同一机制、单一实现点。测试：`session_profile_inheritance_test.go`（5 例：继承+持久化、父未绑定零变化、不追溯、端到端 profile 指令、agentdef ⊆ 父允许集）、`profile_binding_copy_test.go`（4 例）、`chat_profile_child_inheritance_test.go`（1 例）；反证已执行（临时停用 API 侧快照 → 继承用例立即失败，`profile_ref` 为空）。
**风险**：R16（漂移——`/profile status` 显示绑定关系）。

### Batch 6 — P2（按需排期，不承诺）

- `--profile auto` 自动路由（复用 server 端 `routeProfileForPrompt` 思路，映射规则配置化）；
- runtime-server 只读 API 扩展 + frontend 展示（与"路由档位 profile"文案区分）；✅ **已落地**（随 Batch 8 M4：只读清单/详情 API + 设置页 Profiles 面板；2026-09-24 核实回填）
- usage ledger 按 profile 聚合；✅ **已落地**（slice 2，2026-09-24：记录面 + `group_by=profile` 聚合；slice 2b，同日：前端分组对比 UI）
- workspace `.aicli/profile` 项目级绑定（依赖 V10 结论）；✅ **第一阶段已落地**（2026-09-25）：只读发现（`.aicli/profile` pointer-only 校验 + 目标必须落在本工作区项目层）+ 显式应用（沿用会话内 `/profile <ref>`，不新增自动激活）；自动默认激活 / 多工作区自动解析 / 默认开启策略仍后置（Q12 未撤）。

**落地状态（2026-09-24，slice 1 = FR-11；slice 2 = FR-13 后端半程；slice 2b = FR-13 前端展示面；2026-09-25 = FR-14 第一阶段）**：`--profile auto` 与 usage ledger 按 profile 聚合（记录面 + 聚合面 + 前端分组对比 UI）已实施并验证；FR-12 已随 Batch 8（M4）落地（2026-09-24 核实回填）；FR-14 第一阶段（项目绑定只读发现 + 显式应用，自动默认激活后置）已于 2026-09-25 落地并验证（证据见本文件变更记录与本文件 Batch 6 段）。
- 单一权威：新增 `internal/profile/autoroute.go`（`AutoProfileRef` / `AutoRouteRule` /
  `AutoRouteConfig` / `DefaultAutoRouteRules` / `IsAutoProfileRef` / `NormalizeAutoRouteRules` /
  `RouteProfileForPrompt` / `ResolveAutoProfileRef`）。匹配语义与历史 server 实现逐字一致
  （小写 + 子串包含；空提示词 → `""` 表示"不路由"），CLI 与 server 共用同一份规则，
  **删除** `internal/api/skills/handler.go` 内的重复实现（原 `routeProfileForPrompt`/`containsAny`）。
- 配置化：`agentconfig.ProfilesConfig` 新增 `auto{fallback, rules[{profile,keywords}]}`
  （`internal/agentconfig/config.go`）；未配置 → 内置启发式，开 auto 的既有部署行为不变（NFR-1）。
- 接线：server 侧 `(*Handler).routeAutoProfileForPrompt`（路由表经
  `SetProfileSupport(ProfileSupportConfig{AutoRoute})` 注入，`cmd/runtime-server/main.go` 传
  `profilesys.NewAutoRouteConfig(cfg.Profiles)`，与 registry 同一快照生命周期）；
  `prompt_layout_debug.go` 同一入口。CLI 侧 `resolveChatProfileState` 启动期解析
  `--profile auto` / `profiles.default_profile: auto`，**Reference 落地为具体 profile**
  （`/profile reload`/`/profile save` 无需提示词即可复用），`AutoRoutedFrom` 记录归因，
  启动摘要（`Profile Route:` 行）与 `/profile status`（`路由:` 行）两处可见。
- 失败语义（不猜、不静默降级）：启动期无提示词（纯交互式 `chat` / `agent stdio`）→ 显式报错
  并给出替代（`--prompt`/`--message`/exec stdin/`/profile use`）；**首轮延迟路由未做**，登记为
  后续可选增强（需要 turn 边界 hook + 切换报告，见 `chat_actor_executor.go:156-217` 单一咽喉）。
- 测试：`internal/profile/autoroute_test.go`（历史启发式逐字对齐 / 规则覆盖与兜底 / 归一化 /
  agentconfig 映射 / ref 判定）、`internal/api/skills/profile_auto_route_test.go`（默认 + 配置
  规则 + 设置期快照）、`cmd/aicli/commands/chat_profile_auto_route_test.go`（路由表驱动 /
  无提示词报错 / 配置默认 auto / 显式 ref 优先 / 归因可见面）。反证已执行：临时令
   `RouteProfileForPrompt` 恒返回兜底 → 三层用例同时失败（core / server / CLI）。
- FR-13 前端（slice 2b）：`UsageLedgerView` 契约扩展 `profileGroups`/`groupedTotal`——响应缺
  `groups` 数组 → `null`，UI 如实提示「未返回分组」（旧后端忽略 `group_by` 时绝不表达成
  「正常但为空」）；`groups: []` 才是真实空态；`profile === ""` 为「未归属」组。
  新增 `pages/usage-analytics/quota-ledger-groups.tsx`（分组表 + 「未归属」标记 + 聚合总数
  「参与聚合 N 条（截断前全量）」，`groupedTotal` 可大于 `records.length`，不据此推断分页）；
  `quota.tsx` 固定以 `group_by=profile` 请求（分组是面板固有展示面，非用户筛选）；
  `use-usage-quota` 过滤器 `Pick` 增 `groupBy` 并透传。
- FR-13 前端测试与反证：`usage.test.ts` +4 例（未请求分组 → null / 解析含空 profile 与丢弃缺
  profile 条目 / `groups` 非数组与非法 `grouped_total` → null / 传 `groupBy` 发
  `group_by=profile` 且未传不发）、`use-usage-quota.test.tsx` +1 例（透传）、`quota.test.tsx`
  +3 例（分组表渲染 / 未返回分组提示 / 空数组真实空态）；反证（禁用 `groups` 解析 + 去掉面板
  `groupBy`）→ 5 例精确失败（解析面 3 + 面板请求面 2；hook 层未受影响，分层正确），还原后复绿。
- FR-12 回填（2026-09-24 核实）：只读列表/详情 API 与设置页展示已随 Batch 8（M4）落地——`GET /api/runtime/profiles`（三来源清单 + 默认标注 + 解析状态）、`GET /api/runtime/profiles/{ref}`（解析后视图：工具面/skills/mcp/prompts/agents/overrides/估算）；设置页 Profiles 面板（`backend-config-settings-page/.../profiles.tsx`）消费 `listRuntimeProfiles`/`getRuntimeProfile`；命名与路由域「难度档位」区分（面板副标题「按场景维护 profile.yaml…」）。

### Batch 7 — 配置覆盖接线（P0/P1，≈1.5 人日）

**目标**：`runtime.overrides` 从 dormant 字段变为可用的会话级覆盖（D12 模式 B / D13 会话级 overlay）。
**依赖**：Batch 1 ｜ **前置核实**：V10、V12
**任务**：
1. **语义先行**：定义键路径语法、合并语义（复用 `MergeConfigYAML`）、特殊值（`{}`/null/slice）行为 → 写入 spec 文档 + `validate` 校验器（白名单 D14；**不靠文档约定**）；
2. **会话级 overlay**：`chatProfileState` 增加 `ConfigOverlay`（合并视图 + 覆盖键清单 + origins）；`chat_setup.go` 装配点消费；server 侧 `/api/agent/chat` 同语义（复用 `resolveChatProfileState` 路径）；
3. **origins 标记**：覆盖键 origins 标记为 `profile`（复用现有结构，不新增格式）；
4. **dormant 字段处置**（R9）：`mcp.merge_strategy` 按 V12 结论定义接线或从 spec 移除（validate 报未知字段）；接线前 UI 不展示其编辑入口；
5. 白名单边界按 §11 决策（Q9）执行：`skills_runtime.enabled`、`providers.items.*` 非密钥字段是否放行。
**测试**：覆盖生效（改 model/skills_runtime/chat 偏好）；白名单拒绝；null 屏蔽；slice 整表替换；未写键回落；无 profile 时**逐字节零变化**。
**DoD**：白名单校验双执行（validate + 运行时解析）；R9 陷阱消除；零变化断言全绿。
**风险**：R8（覆盖语义误用——UI 三处提示）、R9、R11。

**实施记录（2026-09-24 完成，CLI 与 aicli 进程内 web 会话两路径）**：

1. **语义与校验（任务 1/4/5）**：新增 `internal/profile/overrides.go`——键路径语法（点分隔、大小写不敏感匹配、空段报错）、`ValidateOverrides` 白名单/拒绝域（deny 先于 allow）、`MergeOverridesIntoYAML`（复用 `agentconfig.MergeConfigYAML`：标量替换、slice 整表替换、`{}` 无操作、`null` 屏蔽基线）、`OverrideKeyList`/`OverrideOrigins`/`CloneOverrides`；`validate.go` 在 `validateProfileSpecForResolve` 中执行（解析期与 `profile validate` **同一校验器**，双执行）；`mcp.merge_strategy` 按 V12 从 spec 移除并对未知字段显式报错。
2. **会话级 overlay（任务 2）**：`agentconfig.ApplyConfigOverlayYAML`（`config_overlay.go`）——把已解码 cfg 编码为文档 → `MergeConfigYAML` → 解码 + `ValidateConfig` 一次，**不改动 base**、零变化返回 `(nil,false)`、`yaml:"-"` 运行期字段（`ConfigFilePath`/`ConfigLayers`/`ConfigOrigins`/`ConfigOriginFiles`/`ConfigMergeMode`）随视图携带；`chatProfileState.ConfigOverlay`（合并视图 + `Keys` + `Origins`）在 `resolveChatProfileState` 装配，`applyProfileStateToChatSession`（唯一权威投影函数，Batch 10 建立）单点消费 → 启动路径与会话内热切换路径同一实现。基线/生效视图/键清单落在 `ChatSession.ProfileConfigBase`/`ProfileConfigOverlayApplied`/`ProfileConfigOverlayKeys`/`ProfileConfigOverlayOrigins`，保证"切换不叠层、解除即还原"，`/config reload` 整体替换会话配置时旧基线作废（以最新配置为新基线）。
3. **解析输入隔离**：热切换与 `/profile show` 的解析输入固定为未叠加覆盖的基线（`profileResolutionConfig`），否则上一个 profile 的 `aicli.*` 白名单覆盖会渗进新 profile 的解析（D19：切换结果只由新 profile 决定）。
4. **origins（任务 3）**：覆盖叶子标记为 `profile`，复用既有 origins 结构，不新增格式。
5. **失败语义**：合并/校验失败**不阻断会话**（profile 只能触碰白名单键，等价于该层配置调整未生效），但经 `emitProfileConfigOverlayWarning` 显式告警，不静默丢弃（禁止假开关）。
6. **测试**：`internal/agentconfig/config_overlay_test.go`（稀疏合并生效 / 未写键保留 / 基线不被就地修改 / 运行期字段携带 / `nil`·`{}`·同值零变化 / 解码与校验失败路径）；`cmd/aicli/commands/chat_profile_overlay_test.go`（构造短路、keys+origins、应用→幂等→还原、切换不叠层、外部替换后采用新基线、失败保持会话配置、解析基线优先级）。
7. **反证**：临时停用 `CopyConfigRuntimeMetadata` → `TestApplyConfigOverlayYAMLMergesSparseKeysWithoutTouchingBase` 立即失败（`ConfigFilePath = ""`）；临时改为"每次以当前会话配置为基线" → `TestApplyProfileConfigOverlaySwitchDoesNotStackOverlays` 与 `...AppliesThenRestoresBaseline` 立即失败；恢复后全绿。
8. **回归**：`gofmt -l` 对本次新增/修改文件零输出；`go test ./internal/agentconfig -count=1`（11.6s）、`./internal/profile -count=1`（2.2s）、`./cmd/aicli/commands -count=1`（145.7s）全绿，EXIT=0。
9. **server 半程（V27 已回填，2026-09-24）**：runtime-server `/api/agent/chat`（`internal/api/skills`）不经过 `resolveChatProfileState`，其配置消费面是 `Handler.aicliConfig`（`SetAICLIConfig` 快照，`handler.go:164/450-550`）。按"**有消费者才接线**"原则落地请求级半程：`profileRuntimeState` 增 `ConfigOverlay`/`OverlayKeys`/`OverlayOrigins`；`buildProfileConfigOverlay` 以**宿主快照**为基线叠加 `resolved.Overrides`（`ApplyConfigOverlayYAML`，零变化返回 nil、不就地修改宿主快照）；`skillsRuntimeConfigFor` 在 AgentChat 两个 catalog/exposure 注入点（`handler.go:1962`/`:2419`）消费 → `skills_runtime.*` **字段级**白名单（`enabled`/`skill_dir(s)`/`extra_skill_dirs`/曝光 mode+top_k，`overrides.go:63-70`）在 server 侧真实生效。
10. **会话级余项（登记，不建死字段）**：routing 三件套（`aicli.subagents.routing`/`aicli.teams.routing`/`aicli.main_agent.routing`）在 server 侧由 actor 构建/spawn/team 编排期读取（`handler.go:4167`、`session_runtime_support.go:225/381`、`team_handlers.go:132`），调用点不携带 profile 解析结果；会话级 overlay 需在 actor 构建期解析 profile 并配套 actor 缓存失效矩阵（R10 双权威风险）→ 转为 **Batch 12 前置评估项**，本轮不强行接线。
11. **server 半程测试与反证**：`internal/api/skills/profile_config_overlay_test.go` 5 例（覆盖生效 + 宿主快照不被就地修改 + nil/空状态回落同一指针 + 未声明覆盖/同值覆盖零变化 + 禁止域运行时双执行报错 + routing 域进入视图但不泄漏宿主快照）；反证 F1（停用 `skillsRuntimeConfigFor` 的覆盖消费 → `TestBuildProfileConfigOverlayAppliesSkillsRuntimeOverride` 立即失败）/F2（强制产出视图 → `TestBuildProfileConfigOverlayZeroChangeStaysNil` 立即失败），恢复后全绿。

### Batch 8 — 前端 Profiles 页与 API（P1，≈3 人日）

**目标**：Profiles 设置页（9 卡片编辑器）+ 完整读写 API（§10.2/10.3）。
**依赖**：Batch 7、Batch 12（只读 API 先行） ｜ **前置核实**：V6、V7、V22
**任务**：
1. **后端**：`/api/runtime/profiles` 端点族（§10.5 的 list/get/put/validate/preview/default/apply + **G1-G2 创建/生命周期：create/duplicate/rename/move/delete/references**，依设计文档 §11 补遗并入本批；`apply` 本批返回 501，执行核心随 Batch 12；export/import（G5）留 Batch 13）——解析复用 `internal/profile` registry/resolver（不新建解析逻辑），写回原子写 + admin token + mtime 冲突检测；
2. **前端**：`profiles` mode（挂 `mode-registry.ts`）+ 编辑器 9 卡片（基本/工具面/Skills/MCP/Prompts/Agents/Overrides/偏好与审批/校验与影响），复用 `ConfigDomainDialog`、`settings-*` 组件族、`impact-panel`、`validation-panel`、`preview-section`、`runtime-config-diff`；
3. **选择器数据源对接**（**V6/V7 已核实修正**）：工具面 = **profiles get 视图**（`tools.allow/deny/effective`）——`/capabilities` 不含内置工具全清单（仅 skills 能力描述 + agent 单条，`handler.go:1568-1586`）；Skills = `/skills`；MCP = `/mcps` + `/mcps/{name}/tools`（工具级**只读联动**，不双写——R10）；Agents = profiles get 视图 `agents` 字段（`agents.ts` 是运行中子代理身份图，非 profile 级清单）；
4. i18n（zh/en）+ vitest（编辑器纯函数单测 + 关键交互测试）；`npm run lint` 校验通过（i18n/行数脚本）。
**DoD**：Profiles 页可编辑、可预览、可校验、可保存；写回仅动 `<profile root>/profile.yaml`（D15）；零变化回归。
**风险**：R10（双权威漂移）、R12（渐进披露）、R13（估算口径单点）。

### Batch 9 — 特性开关与审批聚合（P1/P2，≈1 人日）

**目标**：开关目录（§9.2）逐项接线 + 审批偏好收窄约束（D16/D17）。
**依赖**：Batch 7 ｜ **前置核实**：V8、V9、V11
**任务**：
1. **T1 开关**：开关目录逐项接线到覆盖白名单，**每项配"权威生效点"断言测试**（防假开关）；
2. **T2 能力裁剪**：提供"禁用子代理/团队"的模板 denylist（`spawn_agent`/`spawn_subagents`/`spawn_team`），**不新增配置开关**；
3. **T3 审批**：profile 默认 `permission_mode`（已实现）+ `bypass_permissions` 禁止校验（D16）+ UI 来源标注（D17）；
4. 按 V8/V9/V11 结论决定 supervision 审批与 `skills_runtime.enabled` 是否纳入目录（无可配置项则**不做**，避免假开关）。
**DoD**：每个开关有单一权威生效点 + 断言；审批默认值不降级显式选择（`chat_profile_test.go:170-177` 语义保持）。
**风险**：R10、R11。

**落地结果（2026-09-24，已完成）**：
- **T1 开关（逐项断言，防假开关）**：新增 `internal/profile/overrides_catalog_test.go`——每个目录开关做**双重断言**：① `ValidateOverrides` 无 error（键在允许白名单内）② `ApplyConfigOverlayYAML` 应用到 `*Config` 后字段**真实翻转**（`skills_runtime.enabled`、`skills_runtime.aicli_skill_exposure_mode/_top_k`、`aicli.log.enabled`、`aicli.model_cards.enabled`、`aicli.main_agent.routing.enabled`）。**V11 修正**：`SkillsRuntime` 挂在**配置根**（`agentconfig/config.go:42`），Batch 7 曾把白名单/测试写成 `aicli.skills_runtime.*`（dormant 假开关）→ 已改为根级 `skills_runtime.*`，并新增反 dormant 用例（`aicli.skills_runtime.enabled` 必须被拒）。
- **T2 能力裁剪（模板 denylist，不新增开关）**：`internal/profile/templates/review/profile.yaml` 增 `tools.denylist: [spawn_agent, spawn_subagents, spawn_team]`。依据：三个委派工具属 runtime-owned（`policy.IsRuntimeOwnedEssentialTool`），**allowlist 裁不掉**，只有显式 denylist 才真正关闭；V8 已核实 `subagents`/`teams` 配置节无 `enabled` 语义，故**不新增配置开关**（否则是第二套开关 / R10）。断言：`TestRenderedReviewTemplateDeniesDelegationTools`（denylist 命中 + `ToolPolicy.AllowTool(name)` 报错 + `todos` 等仍可用）。
- **T3 审批**：① D16 单一权威断言 `TestBuildBinding_ProfileCannotDefaultToBypassPermissions`（profile 来源默认 `bypass_permissions` → `BuildBinding` 报错；非 profile 来源与 plan 档位不受限），落地于 `agentdef/build.go:125-135`；② D17 可发现性：`profile show` 新增 `permission_mode` + `permission_mode_source`（来源 = agentdef 类别 + 定义文件），文本行标注"默认值，会话内可被显式选择覆盖"；③ **修复 profile 路径默认权限模式静默丢失**（根因：`resolveChatProfileState` 只投影 prompt/工具/skills/mcp，从不填 `state.PermissionMode`，而 `resolveChatAgentdefState` 会填 → `--profile` 场景下 `applyProfileDefaultsToChatOptions` 拿不到默认值）：新增 `profileAgentPermissionMode`（`chat_profile.go`）复用 agentdef 权威解析（`Resolve` + `BuildBinding`），与 `--agent` 路径行为对齐，并继承 D16（违规按"未声明"处理，更保守）。
- **V8/V9/V11 回填**：V8 无 `enabled`（纯工具面）；V9 supervision 有配置但属宿主级运行时预算/灰度 → **不纳入**目录；V11 `skills_runtime.enabled=false` 会一并关闭 profile 级 skill 目录（`skills_integration.go:944-951` 早退）→ 纳入目录（根级路径）。

### Batch 10 — 后端热切换执行核心（P0，≈1.5 人日）

**目标**：`applyRuntimeProfileSwitch` 五阶段实现（§18.1）+ 不变量 A1-A8。
**依赖**：Batch 1 ｜ **前置核实**：V13、V14、V18（**设计分支依赖**，优先核实）
**任务**：
1. 新增 `backend/cmd/aicli/commands/chat_profile_switch.go`：`applyRuntimeProfileSwitch(session, ref) (*ProfileSwitchReport, error)`——解析（只读，失败零状态改动）→ 应用（ProfileState + provider/model/permission 默认守卫）→ 失效（① 锚点删除 ② 工具面重置 ③ turn 冻结面按 V14 结论处理 ⑥ token 清零）→ 身份持久化（sessionmeta 4 键 + sync）→ 报告（D23）；
2. 失效接线复用：`chat_tool_surface_stability.go:41`、`internal/chat/hub.go:128`、`session_runtime_store.go:556`；② 句柄按 V13 结论选择精确失效或 hub 全量；
3. **V14 分支**：决定"立即删除锚点"vs"`pending_profile_switch` 到 turn 边界"（§18.2）；A3 是验收线；
4. V18 保证：重建 prompt 时 `<environment_context>` 块与冻结值字节一致（⑧保留）；
5. 新增 `chat_profile_switch_test.go`：A1-A8 断言（§18.3 建议名）。
**DoD**：**A1/A2/A3/A6 四条全绿**；`/profile` 尚不可用（无命令面），仅 Go 测试驱动。
**风险**：R14（cache 成本，已接受）、R15（前缀撕裂，D19 一次到位）、R19。

**落地结果（2026-09-24，已完成）**：
- 交付物：`cmd/aicli/commands/chat_profile_switch.go`——`applyRuntimeProfileSwitch` 五阶段 + `ProfileSwitchReport`/`ProfileSwitchChanged`（D23 投影）+ `clearFrozenChatSystemPromptAnchor` / `invalidateChatStableToolSurface` / `chatSessionTurnInFlight`；报告附事实字段 `in_flight_turn`/`anchor_cleared`/`tool_surface_scope`（actor|hub|none，不谎报失效）。
- 单一权威重构：`applyProfileStateToChatSession`（`chat_profile.go`）成为"profile → 会话生效面"唯一投影函数（含 `FunctionCatalog.SetToolPolicy` 同步），启动路径 `chat_setup.go:248-254` 改为调用它，消除双份逻辑。
- V14 结论：**立即删除锚点**，不引入 `pending_profile_switch`（依据见设计 §18.2 核实项 2）；V13：actor 句柄可得走 `hub.Get(sessionID)` → `actor.InvalidateStableToolSurface`，否则 hub 全量；V18：切换不触碰 `sessionmeta.EnvironmentContextBlock`（⑧保留）。
- **D30（新增）**：provider/model/permission **只报告不落地**（`changed.*_changed` 恒 false + warnings 指向 `/provider`、`/model`），会话显式选择永不被 profile 默认覆盖（A5 锁定）。
- 测试：`chat_profile_switch_test.go` 覆盖 A1/A2/A3/A4（持久化半程）/A5/A6；`go build ./cmd/aicli/...` 通过、`go test ./cmd/aicli/commands/ -count=1` 全绿（140.0s）。
- DoD：A1/A2/A3/A6 全绿 ✅；命令面仍不可用（Batch 11a）。

### Batch 11 — TUI `/profile` 命令面（P0，≈1.5 人日：11a ≈1 + 11b ≈0.5）

**目标**：11a 核心命令可用；11b 生命周期子命令随 Batch 13 落地。
**依赖**：Batch 10（11a）；Batch 13 后端（11b） ｜ **前置核实**：V17（`save --to session`）
**任务（11a）**：
1. catalog spec 注册（`chat_slash_command_catalog.go`，Group: session；§17.1 草案）；
2. 新增 `chat_profile_command.go`：handler（status/list/show/diff/use/pick/reload/off/save）+ picker（复用 `runtimeModelPickerState` 框架，`chat_model_switch.go:297-393`）+ Switch Report 文本渲染（与 Web 共用投影）；
3. 补全/帮助自动生效验证（`chat_slash_completion_test.go` 增例）；
4. 失败模式对齐 §17.2（"解析不了就报错"，不猜；失败零状态改动）。
**任务（11b，与 Batch 13 联调）**：create/duplicate/save-as/edit/rename/move/delete/export 八个生命周期子命令（§23 G4 行为细则）——命令 spec 可在 11a 一次性注册，行为在 Batch 13 后端就绪后启用。
**DoD（11a）**：TUI 内 `/profile use X` → 下一轮请求 tools/prompt 确实变化（手工 + A1/A2）。
**风险**：R16（`/profile status` 显示子代理绑定）、R18（reload 失败保留旧状态）。

**落地结果（2026-09-24，11a 已收口）**：
- 交付物：catalog 注册（`chat_slash_command_catalog.go:165-180`，Group: session；补全清单 `chat_slash_completion_test.go:441`）；命令 handler `chat_profile_command.go`（status/list/show/diff/use/pick/reload/off/save + `--to session|workspace|config` + `--yes`）；A4 resume 半程 `chat_profile_resume.go` + `chat_session.go` restore 钩子；11b 子命令显式拒绝（"尚未启用…随 Batch 13 落地"，不静默无操作）。
- 单一执行核心：所有切换/重载/关闭路径统一经 `applyRuntimeProfileSwitch`（Batch 10），命令层零复制失效逻辑；`/profile off` = 无 profile 基线重投影（A7）。
- 选择弹层统一（P0 门禁）：`/profile pick` 走 `useRuntimeSelectionPopup` + `renderSelectionPopupLines`（与 `/model` 同框架）；无弹层面时退化为只读列表 + 提示，绝不静默切换；零新增 direct writer（`TestChatInteractiveDirectWriterInventory` 通过）。
- 测试：`chat_profile_command_test.go` 24 例（含 A7 `TestProfileCommandOffRestoresBaseline`、生命周期显式禁用、catalog/帮助/补全一致性）；`chat_profile_switch_test.go` 增 A4 `TestProfileSwitch_IdentityPersistsAcrossResume`（switch → 持久化 → Load → restore → status 回显 → 再 sync 不丢 `profile_ref`）与 A8 `TestProfileSwitch_RapidSwitchCoalesces`。
- TUI 剧本固化（DoD 的"手工剧本"改为进程内 e2e）：`chat_profile_tty_test.go` `TestTTY_LiveLoop_ProfileUseSwitchesNextTurnSurface`——真实主循环 + stdin 注入 + vt.Screen 断言：`/profile use dev` 后第一轮触达 executor 时 `profile_ref=dev`、tools=`[grep,read_file]`、prompt 含 profile 文案、旧锚点未进入下一轮；`/profile off` 后下一轮回落基线；Switch Report 渲染到屏幕。
- 证据：`gofmt -l` 零输出；`go vet ./cmd/aicli/commands` 退出 0；`go test ./cmd/aicli/commands -run "TestProfile|DirectWriterInventory" -count=1` 通过；A4 反证（临时停用 restore 钩子 → 测试立即失败，恢复后通过）；全包回归证据见变更记录（Batch 11a）。

### Batch 12 — Web 命令 + 前端接线（P1，≈1.5 人日）

**目标**：Web 端 `/profile` 可切换（复用同一执行核心）；composer 接线。
**依赖**：Batch 11a ｜ **前置核实**：V15、V16、V19
**任务**：
1. 后端：`SubmitSessionRuntimeCommand` dispatch 新增 `set_profile` 分支（`session_runtime_handlers.go:889`）+ 请求结构 `Profile` 字段（`:40-50`）；**不新增路由**（复用 `handler.go:912`）；actor 句柄按 V15/V19 结论；
2. 目录 API：`GET /api/runtime/profiles`（list，供 composer 候选；handler 路由 + 新 handler）；
3. 前端：`api/runtime/profiles.ts` + `setSessionProfile()`（风格对齐 `approveSessionTool`/`interruptSessionTurn`）；
4. composer：`composer-builtin-commands.ts` 新增 `profile` 命令（`kind: popupSelect`）+ `use-composer-command-executor.ts` 分支 + `use-composer-command-surface.ts` 候选注入；**只注册确实可执行的命令**（后端无 `set_profile` 时不注册，R20）；
5. 会话详情 profile 徽标（复用 routing 区块风格）。
**DoD**：前端 `/profile` 可选择、可切换、可看到 Switch Report；切换后下一轮请求断言通过（A1/A2 的 Web 路径）。
**风险**：R20（旧后端兼容）。

### Batch 13 — 创建/编辑/分享闭环（P1，≈2.5 人日）

**目标**：G1-G5 全部落地（§23），生命周期闭环。
**依赖**：Batch 8、Batch 12（+ 11b） ｜ **前置核实**：V17、V22、V24、V25
**任务**：
1. **后端 API**：Batch 8 已落地 list/get/put/validate/preview + `default`/`apply` 拆分（G3，apply 暂 501）+ G1-G2（create/duplicate/rename/move/delete/references）；本批补 `export`/`import`（G5）、**save-as 差分固化**（G1 的 `from_session` 深化，D24）与 **`apply` 执行核心接线**（Batch 12 落地后由 501 转真实实现）；
2. **save-as 差分固化**（D24）：只固化与基线差分；prompts **不固化**；无差分报错不产空 profile；固化后三选一引导（立即使用/设为默认/稍后）；
3. **引用完整性**（D25）：四类引用检查（default / 活跃会话 / agent 引用 / 工作区文件）；硬删 + 二次确认 + 路径清单；**V24 保证覆盖 server 创建的会话**；
4. **原子写与冲突检测**（R24）：按 V25 结论落地；冲突拒绝并提示 reload（不合并、不静默覆盖）；
5. **TUI 11b**：生命周期子命令行为细则（§23 G4）；
6. **前端**：创建向导（V22 复用 `mcp-form.tsx` 模式）、列表操作（重命名/移动/删除/导出/导入）、`default`/`apply` 双按钮文案互斥（D26）、导入路径清单预览；
7. **导入安全纪律**（D28）：先 validate、绝不自动激活、显示路径清单、拒绝单文件格式。
**测试/验收**：**E2E-1~5 通过 + A9-A12 全绿**。
**风险**：R21（固化语义漂移）、R22（引用悬空）、R23（导入供应链）、R24（双写）。

### Batch 14 — 信任门控 + 安全验收（**P0（安全）**，≈1.5 人日）

**目标**：G6/D29 分级门控落地；安全闭环验收。
**依赖**：Batch 13 ｜ **前置核实**：V20、V21（**设计分支依赖**，优先核实）
**任务**：
1. `internal/profile` 接入 foldertrust（按 V20/V21 结论选择接入位置与复用模式）：未信任工作区加载项目级 profile 时——tools/skills/mcp 裁剪声明、`permission_mode` 默认、overrides **正常生效**；**prompts（含 agents prompts）不应用**；
2. **警告面**（三处）：TUI `/profile status`、启动摘要、Switch Report 显式提示"内容因工作区未信任而未应用"；前端 Profiles 页"部分内容未应用"徽标 + 一键信任入口（Q22，复用 foldertrust 既有 UI 通道）；
3. 信任后重载恢复：`/profile reload` 或重启即完整应用（复用 `chat_folder_trust.go:186-196` 先例）；
4. 断言 A13/A14：未信任时 `SystemPromptText` 不含 profile 文本；信任翻转后恢复注入（同一会话内）。
**测试/验收**：**E2E-6/7 通过 + A13/A14 全绿**。
**DoD**：安全门控生效且**不误伤收窄类声明**（分级而非一刀切）；导入路径同受门控（不给导入开后门，D28 第 4 条）。
**风险**：R23、D29 假门控（A13 防）。

## 5. 测试与验收策略

### 5.1 分层测试矩阵

| 层 | 范围 | 载体 | 时机 |
|---|---|---|---|
| **单元测试** | spec/merge/validate、estimate、模板渲染 | `backend/internal/profile/*_test.go` | Batch 1/2 起持续 |
| **断言测试** | A1-A14（防假开关，§5.2） | `chat_profile_switch_test.go` 等 | Batch 10/13/14 |
| **集成测试** | 多入口一致性（chat/exec/agent stdio 表驱动）；server 侧 `/api/agent/chat` 同语义 | `backend/cmd/aicli/commands/*_test.go` | Batch 3 起持续 |
| **前端测试** | 编辑器纯函数单测 + 关键交互（vitest）；i18n/行数校验 | `frontend` vitest + `npm run lint` | Batch 8 起持续 |
| **E2E** | E2E-1~7（§5.3 手册） | 手工剧本（Batch 13 起可部分自动化，playwright） | Batch 13/14 |
| **回归** | 无 profile 零变化（逐字节）+ 既有测试全绿 | `make test` + `npm test` | **每次合并前** |

### 5.2 断言总表 A1-A14（落点与验收线）

| # | 断言（设计文档 §18.3 / §24） | 落点批次 | 验收线 |
|---|---|---|---|
| A1 | 切换后请求 `tools[]` 与新 profile 声明一致（被排除工具不在请求中） | 10 | M2 门禁 |
| A2 | 切换后首个 turn 的 system prompt = 新 profile 组合结果 | 10 | M2 门禁 |
| A3 | 在途 turn 的冻结前缀不变 | 10 | M2 门禁（**V14 分支的验收线**） |
| A4 | `/profile use X` 后 resume，`profile_ref` 仍为 X | 10 | M2 |
| A5 | 显式 `/model` 选择不被 profile 默认覆盖 | 10 | M2 |
| A6 | 切换失败时 prompt 锚点/工具面/sessionmeta 全部不变（原子性） | 10 | **M2 门禁** |
| A7 | `/profile off` 回到无 profile 基线且锚点重建 | 10 | M2 |
| A8 | 同 turn 连续两次切换只产生一次失效（合并为最后一次） | 10 | M2 |
| A9 | `save-as` 产物仅含与基线差分；无差分不产空 profile | 13 | M5 门禁 |
| A10 | 删除被 `default_profile` 引用：无 `--force` 必失败；`--force` 后 default 清空 | 13 | M5 门禁 |
| A11 | 导入的 profile 在任何情况下不改变当前会话 profile 与 default | 13 | M5 门禁 |
| A12 | `apply` 只影响当前会话（default 不变）；`default` 只影响新会话（当前会话不变） | 13 | M5 门禁 |
| A13 | 未信任工作区：tools 裁剪生效 **且** `SystemPromptText` 不含 profile 文本 | 14 | **M6 门禁（安全）** |
| A14 | 信任后 `reload` 使 prompts 恢复注入（同一会话内翻转） | 14 | M6 门禁 |

### 5.3 E2E 执行手册（E2E-1~7）

> 与设计文档 §23 G7 的剧本一致；此处补充**执行方式与操作序列**，作为验收时的操作单。

| # | 剧本 | 执行方式（操作序列） | 通过标准 |
|---|---|---|---|
| E2E-1 | 前端：新建 → 模板 `review` → 校验 → "立即切换" | Profiles 页 → 新建向导选 review → validate → 点击"立即切换" | 下一轮请求 `tools[]` 与模板声明一致；Switch Report 显示变更项（A1/A2） |
| E2E-2 | TUI：固化 → 切换 → 持久化 | 调整工具面 → `/profile save-as my-review` → `/profile use my-review` → 退出 → `aicli chat --profile my-review` | 重开后工具面与固化时一致；`profile.yaml` 仅含差分（D24） |
| E2E-3 | 前端：修改闭环 | 改 deny 列表 → preview → 保存 → diff → 切换 | 保存后 diff 与实际文件一致；切换后断言生效 |
| E2E-4 | 跨目录分享 | `aicli profile export <ref> --output <dir>` → 另一工作区 `aicli profile import <path>` → 使用 | 导入后 validate 通过、未自动激活、使用后工具面一致 |
| E2E-5 | 删除保护 | 删除被 `default_profile` 引用的 profile（先不带 `--force`，再带） | 被阻止（无 `--force`）；`--force` 后 default 清空且报告明示 |
| E2E-6 | 未信任仓库门控 | 在未信任目录（含 `.aicli/profiles/` + prompts）启动 → `/profile status` → 检查警告 | 裁剪生效、prompts 未应用、警告出现（D29） |
| E2E-7 | resume 漂移容错 | 会话绑定 X → 删除 X → resume 该会话 | 警告 + 会话可用（不崩、不静默降级，R18） |

### 5.4 回归门禁（每次合并前）

1. `make test`（后端 `go test ./...`）全绿；
2. `cd frontend && npm test && npm run lint` 全绿（含 i18n/行数/备份校验脚本）；
3. **零变化抽查**：无 profile 路径的 chat/exec 启动摘要与请求 artifact 与基线快照一致（逐字节）；
4. 重点回归面：`cmd/aicli/...`、`internal/profile/...`、`internal/profileinput/...`、`internal/api/skills/...`、`internal/chatcore/...`（设计文档 §5.4）。

## 6. 工程纪律（评审检查单）

> 每个 PR 合并前逐项自查；评审人有权以任一项不满足为由拒绝合并。

| # | 检查项 | 判据 | 拒绝例（反模式） |
|---|---|---|---|
| 1 | **无假开关** | 每个新开关/字段有**单一权威生效点** + 断言测试；UI 不展示未接线字段 | 新增 `subagents.enabled` 配置开关但无消费点 |
| 2 | **无第二套方言** | 复用 `profile.yaml` 既有字段与 `MergeConfigYAML` 语义 | 单文件内联 profile 格式、`.trash/` 第二状态源 |
| 3 | **只收窄** | denylist 优先于 allowlist；安全域（密钥/端点/foldertrust/`BlockUntrustedMCP`）不可被 profile 覆盖或关闭 | profile 声明放宽 `bypass_permissions`、覆盖 `admin_token` |
| 4 | **双写禁止** | MCP 工具级启停、`mcp.yaml` 内容、全局 `config.yaml` 编辑入口保持唯一（profile 只做选择/覆盖视图） | 在 profile 里做 MCP 工具级启停开关 |
| 5 | **估算单点** | token 估算只在 `internal/profile/estimate.go`；前端只消费 API 返回值 | 前端自算工具面 token |
| 6 | **零变化** | 无 profile 路径行为逐字节不变 | "顺手"修改无 profile 时的默认行为 |
| 7 | **错误不猜** | "解析不了就报错"；失败时状态零改动（原子性） | 未知 profile 静默降级为无 profile |
| 8 | **写回边界** | 编辑 profile 只写 profile 目录；禁止把"解析后的合并视图"当作写回内容（D15） | 保存时写回全局 config.yaml |

## 7. 发布、灰度与回滚

### 7.1 发布单元

- **按里程碑合并**（M1-M6）：每批次一条短分支（`feat/profile-batchN-<slug>`），DoD 达成即合并，不留长分叉；
- **M6（Batch 14）为"发布就绪"点**：此前各里程碑为内部可用（CLI/TUI 先行），不对外宣传；
- 合并前必须通过 §5.4 回归门禁与 §6 评审检查单。

### 7.2 渐进可见性（能力探测，防旧后端问题 R20）

| 阶段 | 可见能力 | 前置 |
|---|---|---|
| M1 | CLI 命令组 + 模板 | 无（后端自含） |
| M2 | TUI `/profile` 核心命令 | Batch 10 执行核心 |
| M3 | 覆盖与开关（对 CLI/TUI 生效） | Batch 7/9 |
| M4 | Web composer `/profile` + Profiles 页 | Batch 12/8；前端**能力探测**：后端无 `set_profile` 时不注册命令（R20） |
| M5/M6 | 生命周期全命令 + 导入导出 + 安全门控 | Batch 13/14 |

### 7.3 回滚路径

1. **批次级独立回滚**：各批次无破坏性跨依赖（除 13→14 的安全链），任一里程碑可整体 revert；
2. **数据兼容**：无 schema 迁移；新增 spec 字段仅新代码消费；
   - **回滚前置检查**：确认旧版解析器对 `profile.yaml` 新增字段（skills/mcp/prompts）的行为（忽略 or 报错）——若报错，回滚清单包含用户 profile 文件处理说明（写入 `docs/aicli/profiles.md` 的"版本兼容"节）；
3. **安全不可回滚项**：D29 门控若因故回滚，必须**同时**回滚"导入功能"（R23：无门控的导入是供应链面）——两者同批发布、同批复滚；
4. **回滚验证**：回滚后跑 §5.4 零变化抽查 + 既有测试全绿。

### 7.4 文档与用户沟通

- `docs/aicli/profiles.md`（Batch 3 交付，M1 起对外可用）；
- 发布注记：M1（CLI 裁剪）、M4（Web/TUI 切换）、M6（生命周期 + 安全）三篇；每篇附"与 mcp 工具级启停的关系"说明（R4）。

## 8. 实施期风险登记（执行视角）

| # | 风险（设计文档编号） | 触发场景 | 应对 |
|---|---|---|---|
| 1 | R1 过滤链路缺口扩大 | Batch 0 核实发现 agent 层/子 agent 未过滤 | 及时更新 §2.3 工期；缺口纳入 Batch 1（不做"半程过滤"） |
| 2 | R2 prompt 顺序不稳 | append 模式叠加顺序漂移 | 固定顺序 + 顺序断言；默认 replace 不动 |
| 3 | R15 前缀撕裂 | 在途 turn 时切换 | D19 一次到位失效；A3 断言；V14 分支先行决策 |
| 4 | R22/R23 引用与供应链 | 删除悬空 / 恶意导入 | A10/A11 + D25/D28/D29；Batch 13/14 不得拆分发布 |
| 5 | R24 双写冲突 | TUI 与前端同时编辑 | 原子写 + mtime 冲突检测；冲突拒绝并提示 reload |
| 6 | **并行冲突**（实施期特有） | Batch 8/12 同改 `handler.go`/profiles API；Batch 11/13 同改 `chat_profile_command.go` | 契约冻结件先行（§3.3）；按"12 只读 → 8 写端点"顺序；小步合并 |
| 7 | **前端工作量误估**（实施期特有） | 9 卡片编辑器超出预算 | 渐进披露：基础视图（基本/工具面/Skills/MCP）先交付，高级卡片折叠后补 |
| 8 | **验证环境依赖**（实施期特有） | A1/A2 需要真实请求 artifact；provider cache 行为需真实 provider | §3.3 准备手工验证环境；E2E 剧本在 M2 起先行手工演练 |
| 9 | **接口冻结延迟** | API 契约未定导致前端空转 | M4 前冻结 §10.5 契约；前端以 mock 先行（不阻塞后端） |

## 9. 角色分工与协作

### 9.1 单人执行路径（推荐顺序）

`Phase 0 → Batch 0 → 1 → 2 → 3（M1）→ 10 → 11a（M2）→ 7 → 9（M3）→ 12 → 8（M4）→ 13（M5）→ 14（M6）→ 5`

### 9.2 双人并行路径

| 角色 | 批次 | 交接物 |
|---|---|---|
| 后端 | 0/1/2/3/7/9/10/13（后端部分）/14 | ① §10.5 API 契约（Batch 12 前）② Switch Report JSON（Batch 10 前）③ 引用检查结果结构（Batch 13 前） |
| 前端 | 8/12/13（前端部分） | 消费契约 mock；E2E-1/3/4 前端侧操作单 |
| 共同 | E2E-1~7 演练；§6 评审互查 | E2E 操作单（§5.3） |

### 9.3 协作纪律

- 契约变更必须**先改设计文档 §10.5**再改代码（单一事实源）；
- 每批次完成即回填：§3.1 V 表状态、§附录 P 跟踪表、设计文档对应附录（如有核实结论）。

## 10. 交付物清单

| 类别 | 交付物 |
|---|---|
| **后端新增** | `cmd/aicli/commands/profile{,_list,_show,_validate,_create}.go`、`chat_profile_switch.go`、`chat_profile_command.go`、`chat_profile_switch_test.go`、`internal/profile/templates/`（4 模板 + `templates.go`）、`internal/profile/estimate.go` |
| **后端修改** | `internal/profile/{spec,merge,validate}.go`、`internal/profileinput/inputs.go`、`cmd/aicli/commands/skills_integration.go`、`internal/api/skills/*`、chat bootstrap/setup 与 runtime store 相关文件、`internal/api/{handler,session_runtime_handlers}.go`、`exec_run.go`、`chat_slash_command_catalog.go`、foldertrust 接入点（Batch 14） |
| **前端新增** | `api/runtime/profiles.ts`、profiles mode 组件（9 卡片 + 向导 + 生命周期操作 + 导入导出）、i18n 文案 |
| **前端修改** | `mode-registry.ts`、`composer-builtin-commands.ts`、`use-composer-command-executor.ts`、`use-composer-command-surface.ts`、会话详情徽标组件 |
| **文档** | `docs/aicli/profiles.md`（新）、`docs/aicli/install.md`、`docs/README.md`、`docs/multi-agents/profile/profile_system_implementation.md`、设计文档附录 A-E 回填 |
| **测试** | A1-A14 断言、spec/merge/validate/estimate/模板快照单测、多入口一致性表驱动测试、vitest、E2E-1~7 操作单 |
| **配置** | 无破坏性变更；`examples/profiles/coding/` 补齐为完整参考 |

## 11. 阻塞性开放问题决策清单（Phase 0 内拍板）

| # | 问题（设计文档编号） | 阻塞批次 | 建议（设计文档倾向） | 决策人 | 期限 |
|---|---|---|---|---|---|
| Q2 | token 估算口径（粗估 vs usage ledger） | 2/3 | 先粗估（字节/4）并标注 | 技术负责人 | M0 |
| Q5 | `/capabilities` 是否含内置工具全清单 | 8 | 先核实 V6；不足则新增只读端点 | 技术负责人 | Phase 0 |
| Q9 | 白名单边界（`skills_runtime.enabled`、`providers.items.*` 非密钥字段） | 7/9 | 放行非密钥字段；密钥/端点类拒绝 | 技术负责人 | M0 |
| Q11 | `mcp.merge_strategy` 定义或删除 | 7 | 二选一（不留 dormant） | 技术负责人 | M0 |
| Q12 | `WorkspaceSpec` 是否纳入本期 | 7/14 | 2026-09-25 分阶段决议：**只读发现 + 显式应用解冻**（第一阶段已落地）；**自动默认激活 / 多工作区自动解析 / 默认开启策略仍后置**（Q12 原“整体后置”不再适用，但后两项未撤） | 技术负责人 | M0 / M6 |
| Q13 | 在途 turn 切换策略（立即 vs pending） | 10 | 按 V14 + A3 验收线 | 技术负责人 | M0 |
| Q19 | `save-as` 差分口径 | 13 | 按声明式字段逐个差分 | 技术负责人 | M4 |
| Q20 | 删除硬删 vs 软删 | 13 | 硬删 + 二次确认 | 产品 | M4 |
| Q21 | 导出包格式 | 13 | 目录/zip（不做单文件内联） | 产品 | M4 |
| Q22 | 未信任工作区"一键信任并重载" | 14 | 提供（复用 foldertrust UI 通道，显式确认） | 产品 | M5 |

**Q9/Q11/Q12 结论（Batch 7 前置核实，2026-09-24）**：Q9 = 按建议执行——白名单放行非密钥字段（`skills_runtime.enabled`、`providers.items.*` 非密钥字段），密钥/端点类（`api_key`/`base_url` 等）一律拒绝；Q11 = **从 spec 移除** `mcp.merge_strategy`（零消费点，移除后 validate 报未知字段，避免"配了不生效"）；Q12 = **分阶段解冻**（2026-09-25 更新）：V10 显示 `WorkspaceSpec` 已被 resolver 消费，但 `.aicli/profile` 项目级绑定当时整体后置；后续按“发现可解冻、激活仍后置”拆分——第一阶段落地只读发现 + 显式应用，自动默认激活 / 多工作区自动解析 / 默认开启策略仍未实施（见 `fr14-profile-binding-implementation-plan-20260925.md`）。

**已定/不阻塞（记录）**：Q1 保持默认全量（不改默认）；Q3 已由本期纳入（Batch 8/12）；Q4 四模板先行，`web-debug` 后置；Q14 `/profile use` 写 sessionmeta（是）；Q16 独立目录端点；Q17 切换记录会话事件（不进 token 统计）；Q18 headless 不暴露运行期切换。

## 附录 P — 执行跟踪表（批次 × 状态）

| 批次 | 里程碑 | 状态 | DoD 勾选 | 备注（V 表/决策依赖） |
|---|---|---|---|---|
| 0 | M0-M1 | ✅ 已完成（V5 转 Batch 5 前置） | V1-V4 回填（含证据） | 证据见 §3.1 状态列 |
| 1 | M1 | ✅ 已完成 | 三类过滤（skills/mcp/prompt mode）+ 单测 + 零变化 | 测试：`go test ./cmd/aicli/... ./internal/profile/... ./internal/agentconfig/...` 全绿；CLI 与 server 两侧接线 |
| 2 | M1 | ✅ 已完成（含降噪加严） | 四模板 + validate 五类检出；E2E 实测 | `profile validate/show --agent explore` 退出码 0；`docs/aicli/profiles.md` 已交付；`examples/profiles/coding` 修正为嵌套 schema |
| 3 | M1 | ✅ 已完成 | §1.3 第 1-3 条全部达成 + 零变化回归 | 启动摘要（`chat_profile_summary.go`）+ exec 元数据（`ExecProfileMetadata`）+ 优先级测试；量化实测与两命令闭环证据见变更记录 |
| 5 | 收尾 | ✅ 已完成 | 子会话 ⊆ 父允许集；不追溯已存在子代理 | V2、V5 已回填；证据见变更记录（Batch 5） |
| 7 | M3 | ✅ 已完成（CLI + aicli 进程内 web 会话 + server 请求级；server 会话级 routing 转 Batch 12 前置） | 白名单 + 零变化 + server 请求级生效 | V10、V12、**V27** 已回填；Q9/Q11/Q12 见任务卡；server 侧 `skills_runtime.*` 字段级覆盖真实生效（`handler.go:1962`/`:2419`）；证据见变更记录（Batch 7 / Batch 7 server 半程） |
| 8 | M4 | ✅ 已完成（代码已提交：`7fc2a3e0` 后端全链路 / `a3bca1e8` 设置页面板） | Profiles 页可编辑 | V6、V7、V22 已回填（Batch 8 前置） |
| 9 | M3 | ✅ 已完成 | T1 双重断言（白名单通过 + 字段真实翻转）+ T2 denylist 断言 + T3 D16/D17 断言；显式选择不被降级 | V8/V9/V11 已回填；修复 profile 路径默认 `permission_mode` 静默丢失；落地证据：设计文档「Batch 9 落地状态」块 + `internal/profile/overrides_catalog_test.go` |
| 10 | M2 | ✅ 已完成 | A1/A2/A3/A6 全绿（另覆盖 A4 持久化半程 / A5） | V13/V14/V18/V26 已回填；新增 D30；证据见变更记录 |
| 11 | M2/M5 | ✅ 已完成（11a + 11b） | 11a：命令面可用 + A4 resume 半程/A7/A8 全绿；11b：TUI 生命周期子命令（create/duplicate/rename/move/delete/export/import，含 D37 import 闭环）已随 Batch 13 slice 5-7 启用 | V17 已回填（Batch 13 E7 复核，2026-09-24：session 层零写回/sessionmeta ⑩ 持久化；workspace·config 层复用 agentconfig 写通道 + `--yes` 门禁 + 分层路由如实提示）；证据见变更记录（Batch 11a / Batch 13） |
| 12 | M4 | ✅ 已完成 | composer 可切换 + Switch Report 可见 + R20 能力门控（旧后端不注册命令） | V15/V16/V19 已回填；证据见变更记录（Batch 12） |
| 13 | M5 | ✅ 已完成（slice 1-10：`apply` 执行核心 / export·import（API+CLI）/ TUI 生命周期子命令（含 import 闭环，D37）/ save-as 差分固化（TUI+API）/ 前端分享入口（D38）/ E2E-1·3·4·5 前端半程 / **slice 10 = 前端「从当前会话创建」入口（`/profile save-as`，G1/D24 的最后一处缺口）**） | E2E-1~5 + A9-A12 | V22、**V24、V25** 已回填；V17 已回填（E7 复核，2026-09-24）；Q19/Q20/Q21 已闭环（差分口径=声明式字段逐个差分 / 硬删+二次确认 / 目录·zip 不做单文件内联，均落在各 slice 的测试锚点内） |
| 14 | M6 | ✅ 已完成（V20/V21 已回填、D29 接入设计已冻结；slice 2 落地：foldertrust 检测面扩展 + 分级门控核心 + CLI/server 接线；slice 3 落地：三处警告面（`/profile status` / 启动摘要 / Switch Report，CLI+server）；slice 4 落地：E2E-6/7 自动化剧本（真实判定链 + `/trust grant` 恢复 + resume 漂移容错）；slice 5 落地：Q22 前端闭环（列表可选 `workspace` 参数 + "部分内容未应用"徽标 + 两步确认一键信任 + `/api/runtime/harness/trust` 只读/授予端点）） | E2E-6/7 + A13/A14 | V20、V21 已回填；Q22 已闭环（撤销信任仍走 CLI `/trust`） |
| 6 | P2 | ✅ 已完成（本期范围：slice 1 = FR-11 `--profile auto`；slice 2 = FR-13 后端记录面 + 聚合面；slice 2b = FR-13 前端展示面（分组对比 UI）；FR-12 已随 Batch 8 M4 落地（2026-09-24 核实回填）；**FR-14 第一阶段已落地**（2026-09-25：只读发现 + 显式应用；自动默认激活 / 多工作区自动解析 / 默认开启策略仍后置）） | FR-11 用例全绿 + 零变化（未配置 auto 时行为不变）+ 反证；FR-13 写入/聚合/反例用例全绿（含禁用写入路径反证）+ 未指定 `group_by` 时响应逐字节不变；FR-13 前端 3 文件 40 例全绿 + 反证 5 例精确失败；FR-14 后端 `internal/profile` 全包 + 7 例 API 用例全绿、前端 12 例全绿 | 单一权威 `internal/profile/autoroute.go`；`metadata.profile` 写时解析（aicli `WithProfileLookup` / server `UsageScope.Profile`）；`GET /api/runtime/usage/ledger?group_by=profile`；前端 `profileGroups`/`groupedTotal` 契约 + `LedgerProfileGroups`；FR-14 单一绑定 helper `internal/profile/binding.go` + `LayerProfilesForWorkspace` + `project_binding`/`is_bound` 只读投影；证据见 Batch 6 落地状态与变更记录 |

---

> 变更记录：2026-09-24 初版（依据设计文档第四轮定稿制定；执行级修正见 §2.2）。
> 变更记录：2026-09-24 实施（Batch 0/1/2 完成并验证：V1-V4/V23 回填；`examples/profiles/coding` 修正为嵌套 schema 且工具策略归属 `profile.yaml` 的 `agents.<id>.tools`；validate 降噪——纯 `*` 声明不产生噪音、error/warning 分级固化；新增 `docs/aicli/profiles.md` 与 README 索引）。
> 变更记录：2026-09-24 实施（Batch 3 完成并验证——反馈/exec 元数据/文档）：
> ① 交付物：chat 启动摘要行（`cmd/aicli/commands/chat_profile_summary.go`，含 `估算` 标注与 quiet/JSON/headless 统一抑制）、exec 元数据（`ExecProfileMetadata`，`--output json` 的 `profile.{ref,name,agent,tool_count,skill_count}`，未声明 profile 时字段整体省略=形状零变化）、FR-6 优先级测试锁定（`TestResolveChatProfileState_PrecedenceFlagBeatsConfigDefault`：`--profile` flag > `config.profiles.default_profile` > 无 profile）。
> ② §1.3 第 1 条量化实测（`aicli exec --enable-tools --debug-http` 请求 artifact `*_request_provider_wrapper.json`）：全量基线 `tools[]`=45，其中 **21 个为 profile 可控的目录工具**、24 个为 runtime-owned 控制面工具（`policy/tool_policy.go` `IsRuntimeOwnedEssentialTool`：设计上不受 allowlist 收窄，仅显式 denylist 可裁剪——T2 模板 denylist 属 Batch 9）；`minimal` 生效 2（view/grep）=9.5%、`review` 生效 6（fetch/glob/grep/ls/view/web_search）=28.6%，均 ≤ 40%；两个模板被排除的目录工具**均未出现在 `tools[]`**（extra=0，无"假裁剪"）。该界由新增回归测试 `profile_quant_test.go` 持续钉住，全量清单单一事实源为新增的 `internal/policy.KnownToolTaxonomyNames()`（与 `profile validate` 同一张登记表，不再维护第二份名单）。
> ③ §1.3 第 2 条两命令闭环实测：`aicli profile create review --use --set-default` → `aicli exec`（等价 chat 入口）自动采用，`profile list` 标记"默认 profile: review-use"，返回 `profile.name=review-use, tool_count=6`；实测后 config 逐字节恢复（SHA256 一致）。注意设计 §D5/D26 的"注册（`--use`）与激活（`--set-default`）分离"语义：两命令需 `--use --set-default` 同写（设计 §5.2 的简写 `--use` 指该闭环）。
> ④ §1.3 第 3 条沿用 Batch 2 的 validate 五类检出；回归：`go test ./cmd/aicli/... ./internal/profile/... ./internal/agentconfig/... -count=1` 全绿（≈162s）。
> 变更记录：2026-09-24 实施（Batch 5 完成并验证——子 agent 继承，FR-9/D8）：
> ① 根因（V5）：API 侧 `sessionAgentController.Spawn` 从不把父会话 profile 绑定写入子会话 → 子 actor 构建期 `profileState == nil`，工具面可宽于已收窄的父策略（FR-9/NFR-3 违反）；本地侧因共用父 `ChatSession`/`apiAgent` 天然继承（本次仍补上同一快照机制，使子会话独立加载/重启恢复后仍可解析父级 profile）。
> ② 交付物（单一实现点）：`internal/sessionmeta/sessionmeta.go` 新增 `CopyProfileBinding`（`profile_ref`/`profile_name`/`profile_agent`/`profile_root` 四键及其 legacy 别名一次性快照；只复制非空值，src 未绑定 → dst 不写任何键，NFR-1 零变化；快照而非动态引用 → 不追溯）；调用点 `internal/api/skills/session_runtime_support.go:604-611`、`cmd/aicli/commands/chat_actor_registry.go:715-722`。子策略派生保持既有链路：`applyAPIChildAgentdefToolPolicy`/`applyLocalChildAgentdefToolPolicy` 在父策略上 `DeriveChild`（只收窄）。
> ③ 测试：`internal/api/skills/session_profile_inheritance_test.go`（5 例：绑定继承并持久化、父未绑定=零变化、profile 切换不追溯已存在子代理、子 actor 端到端解析出继承的 profile 指令、agentdef 叠加 ⊆ 父允许集）；`internal/sessionmeta/profile_binding_copy_test.go`（4 例）；`cmd/aicli/commands/chat_profile_child_inheritance_test.go`（本地 agentdef 只收窄 + 父策略不被改写）。
> ④ 反证与回归：临时停用 API 侧快照后 `TestSessionAgentControllerSpawn_InheritsParentProfileBinding` 立即失败（子会话 `profile_ref` 为空），恢复后 5/5 通过；`go test ./internal/api/skills/... ./internal/sessionmeta/... ./cmd/aicli/commands/... -count=1` 全绿（35.1s / 1.1s / 144.7s）。
> 变更记录：2026-09-24 实施（Batch 10 完成并验证——后端热切换执行核心，FR-8 / D18-D23 / D30）：
> ① 交付物：新增 `cmd/aicli/commands/chat_profile_switch.go`——`applyRuntimeProfileSwitch(session, ref)` 五阶段（解析 → 应用 → 失效 → 身份持久化 → 报告，设计 §18.1）；`ProfileSwitchReport`/`ProfileSwitchChanged`（D23 契约；JSON 字段即 TUI/Web 共用投影）；固定 `effective_at:"next_turn"` + `cache_notice`（§16.2 告知义务）；报告附事实字段 `in_flight_turn`/`anchor_cleared`/`tool_surface_scope`（actor|hub|none，不谎报失效）。
> ② 失效接线（D19 一次到位 / D20 复用既有 API）：① `sessionmeta.Delete(ctx, SystemPromptFrozen)`；② `resetStableSharedToolSurface`（进程内）+ `session.LocalRuntimeHost.SessionHub.Get(sessionID)` → `SessionActor.InvalidateStableToolSurface`，句柄不可得退化为 `SessionHub.InvalidateStableToolSurfaces`；③ turn 冻结面**不显式清理**（存储层在途保留，`session_runtime_store.go:555-590`）；⑥ `ContextWindowTokenCount=0`。
> ③ 单一权威重构：新增 `applyProfileStateToChatSession`（`chat_profile.go`）作为"profile → 会话生效面"唯一投影函数（含 `FunctionCatalog.SetToolPolicy` 同步，防"prompt 新 + tools 旧"），`chat_setup.go` 启动路径改为调用它；profile 身份 4 键经 `sessionmeta.Set` + `syncRuntimeSessionFromChat` 持久化（A4 持久化半程已断言）。
> ④ 决策与核实：新增 **D30**（切换不隐式改写 provider/model/permission——只报告 + warnings 指向显式命令，A5 锁定；避免第二套切换路径）；**V14/核实项 2 结论 = 立即删除锚点**（依据：compose 调用点都在 run 起点 `chat_actor_host.go:1848`/`:2119-2145`，在途 head 已 materialize 进 `cfg.SystemPrompt`，不新增 pending 机制），A3 覆盖分层见设计 §18.2/§18.3；**V13** 句柄路径可得、**V18** 环境块保留，均已回填 §3.1。
> ⑤ 测试：`cmd/aicli/commands/chat_profile_switch_test.go` 覆盖 A1（`_ToolSurfaceMatchesDeclaredPolicy`）/A2（`_SystemPromptRebuiltFromNewProfile`）/A3（`_InFlightTurnPrefixStable`）/A4 持久化半程/A5（`_ExplicitSelectionWins`）/A6（`_FailureLeavesStateUntouched`）；`go build ./cmd/aicli/...` 通过，`go test ./cmd/aicli/commands/ -count=1` 全绿（140.0s）。A7/A8 与 A4 的 resume 展示半程依赖命令面，随 Batch 11a 落地。
> 变更记录：2026-09-24 实施（Batch 11a 完成并验证——TUI `/profile` 命令面，FR-8 / §17.1/§17.2 / D21）：
> ① 交付物：catalog 注册（`chat_slash_command_catalog.go:165-180`，Group: session；补全清单 `chat_slash_completion_test.go:441` 同步）；命令 handler `chat_profile_command.go`（status/list/show/diff/use/pick/reload/off/save + `--to session|workspace|config` + `--yes`；Switch Report 文本渲染与 Web 共用 D23 字段语义）；A4 resume 半程 `chat_profile_resume.go`（`hydrateChatProfileIdentityFromResumedSession` 读回 sessionmeta 四键 + `chatReapplyResumedProfileState` 重投影；解析失败保留身份并提示 `/profile reload`，R18）+ `chat_session.go` restore 钩子（优先级：启动显式 `--profile`/配置默认 > 会话持久化绑定）；11b 生命周期子命令显式拒绝（"尚未启用…随 Batch 13 落地"，不静默无操作）。
> ② 交互路径统一（P0 门禁）：`/profile pick` 由 legacy 直写终端改为选择弹层（`useRuntimeSelectionPopup` + `renderSelectionPopupLines`，与 `/model` 同框架）；无弹层面时退化为只读列表 + 提示（绝不静默切换）；`TestChatInteractiveDirectWriterInventory` 通过（零新增 raw writer）。
> ③ 单一执行核心（D21）：use/reload/off 全部经 `applyRuntimeProfileSwitch`/`applyRuntimeProfileDetach`（Batch 10），命令层零复制失效逻辑。
> ④ 测试：`chat_profile_command_test.go` 24 例；`chat_profile_switch_test.go` 增 A4 `TestProfileSwitch_IdentityPersistsAcrossResume`（switch → Load → restore → status 回显 → 再 sync 不丢 `profile_ref`）与 A8 `TestProfileSwitch_RapidSwitchCoalesces`；新增 TUI 剧本 `chat_profile_tty_test.go` `TestTTY_LiveLoop_ProfileUseSwitchesNextTurnSurface`（真实主循环 + stdin 注入 + vt.Screen：use 后第一轮生效面切换 / off 后回落基线 / Switch Report 渲染）。
> ⑤ 反证：临时停用 `chat_session.go` restore 钩子 → A4 测试立即失败（`chat_profile_switch_test.go:412: resume 必须回填 profile 身份，got ref="" name=""`），恢复后通过。
> ⑥ 回归：`gofmt -l` 零输出；`go vet ./cmd/aicli/commands` 退出 0；`go test ./cmd/aicli/commands -run "TestProfile|DirectWriterInventory" -count=1` 通过；`go test ./cmd/aicli/commands -count=1` 全绿（139.721s，EXIT=0，含新增 TUI 剧本）。
> ⑦ 记录：全量回归期间另见两例与本批无关的偶发失败（`TestMeshTakeoverRequestedConsumesMarkerOnce`、`TestAICLIChatActorExecutor_AutoStartTeamMarksBaseSessionRunningUntilSettled`），单测复跑均通过（后者 `-count=1` 通过）→ 记为既有 flake，不属本批范围。
> 变更记录：2026-09-24 实施（Batch 7 完成并验证——配置覆盖接线，FR-3/FR-4 / D13 / R8-R11；CLI 与 aicli 进程内 web 会话两路径）：
> ① 语义（复用既有合并机制，不引入第二套方言）：键路径点分隔、从配置根起、大小写不敏感匹配、不支持转义；合并语义**完全复用 `agentconfig.MergeConfigYAML`**——标量替换、slice 整表替换、空映射 `{}` 无操作、`null` 屏蔽基线值、未写键回落基线。
> ② 交付物（三处，职责单一）：`internal/profile/overrides.go`（键路径解析 + `ValidateOverrides` 白名单/拒绝域，deny 先于 allow + `MergeOverridesIntoYAML`/`OverrideKeyList`/`OverrideOrigins`/`CloneOverrides`）；`internal/agentconfig/config_overlay.go`（`ApplyConfigOverlayYAML(base, overlayYAML)`：编码 base → `MergeConfigYAML` → 解码 + `validateLoadedConfig` 一次；`CopyConfigRuntimeMetadata` 携带 `ConfigFilePath`/`ConfigLayers`/`ConfigOrigins`/`ConfigOriginFiles`/`ConfigMergeMode`；零变化返回 `(nil,false,nil)`）；`cmd/aicli/commands/chat_profile_overlay.go`（`chatProfileConfigOverlay` + `applyProfileConfigOverlay` + `restoreProfileConfigBase` + `emitProfileConfigOverlayWarning` + `profileResolutionConfig`）。
> ③ 白名单双执行（任务 4/5）：解析期 `validateProfileSpecForResolve` 与命令期 `profile validate` 调用**同一校验器**；`mcp.merge_strategy` 按 V12 从 spec 移除并对未知字段显式报错（不静默忽略）。
> ④ 单一权威生效点（R9 陷阱消除）：`chatProfileState.ConfigOverlay` 在 `resolveChatProfileState` 装配，由 `applyProfileStateToChatSession`（Batch 10 建立的唯一权威投影函数）单点消费 → 启动路径与会话内热切换路径**同一实现**，不存在第二条应用路径。基线/生效视图/键清单落在 `ChatSession.ProfileConfigBase`/`ProfileConfigOverlayApplied`/`ProfileConfigOverlayKeys`/`ProfileConfigOverlayOrigins`。
> ⑤ 解析输入隔离（D19）：热切换与 `/profile show` 的解析输入固定为未叠加覆盖的基线（`profileResolutionConfig`），否则上一个 profile 的 `aicli.*` 白名单覆盖会渗进新 profile 的解析——切换结果只由新 profile 决定。
> ⑥ 失败语义（禁假开关）：合并/校验失败**不阻断会话**（等价于该层配置调整未生效），但经 `emitProfileConfigOverlayWarning` 显式告警，不静默丢弃。
> ⑦ 测试：`internal/agentconfig/config_overlay_test.go`（稀疏合并生效 / 未写键保留 / 基线不被就地修改 / 运行期字段携带 / `nil`·`{}`·同值零变化 / 解码与校验失败路径）；`cmd/aicli/commands/chat_profile_overlay_test.go` 7 例（构造短路、keys+origins、应用→幂等→还原、切换不叠层、外部替换后采用新基线、失败保持会话配置、解析基线优先级）。
> ⑧ 反证：临时停用 `CopyConfigRuntimeMetadata` → `TestApplyConfigOverlayYAMLMergesSparseKeysWithoutTouchingBase` 立即失败（`ConfigFilePath = ""`）；临时改为"每次以当前会话配置为基线" → `TestApplyProfileConfigOverlaySwitchDoesNotStackOverlays` 与 `...AppliesThenRestoresBaseline` 立即失败；恢复后全绿。
> ⑨ 回归：`gofmt -l` 对本次新增/修改文件零输出；`go test ./internal/agentconfig -count=1`（11.6s）、`./internal/profile -count=1`（2.2s）、`./cmd/aicli/commands -count=1`（145.7s）全绿，EXIT=0。
> ⑩ 未覆盖（登记 V27）：runtime-server `/api/agent/chat`（`internal/api/skills/profile_support.go` 的 `profileRuntimeState`）是**另一份镜像解析**，不经过 `resolveChatProfileState`；在该路径配置消费点定位前**不新增字段**（避免死字段假开关），server 半程与 Batch 12 由 V27 阻塞门禁。〔同日已解除：见下条 server 半程记录〕
> 变更记录：2026-09-24 实施（Batch 7 **server 半程**完成并验证——V27 定位 + 请求级接线；设计 D13/D14、R9/R10）：
> ⑪ V27 定位：server 侧配置消费面 = `Handler.aicliConfig`（`SetAICLIConfig` 的 routing/skills 快照，`handler.go:164/450-550`），消费者 5 处——`runtimeSkillsConfig()`（`:8225`）、`subagentRoutingConfig()`（`:460`）、`teamRoutingConfig()`（`:468`）、`mainAgentRoutingConfig()`（`:476`）、`defaultReasoningEffort()`（`:496`）。
> ⑫ 请求级接线（有消费者才接线，不建死字段）：`profileRuntimeState` 增 `ConfigOverlay`/`OverlayKeys`/`OverlayOrigins`（`profile_support.go`）；`buildProfileConfigOverlay` 基线 = **宿主快照**（`cloneAICLIRoutingConfig`），经 `profilesys.MergeOverridesIntoYAML` + `agentconfig.ApplyConfigOverlayYAML` 合并，零变化返回 nil、宿主快照不被就地修改；`skillsRuntimeConfigFor` 消费于 AgentChat 的 catalog/exposure 注入（`handler.go:1962`/`:2419`）→ `skills_runtime.*` **字段级**白名单（`enabled`/`skill_dir(s)`/`extra_skill_dirs`/曝光 mode+top_k，`overrides.go:63-70`）在 server 侧真实生效；`catalog_budget_chars`/`document_mode`/`discipline_block` 不在放行集（前端不得展示编辑入口）。
> ⑬ 会话级余项（登记，转 Batch 12 前置）：routing 三件套在 actor 构建/spawn/team 编排期读取（`handler.go:4167`、`session_runtime_support.go:225/381`、`team_handlers.go:132`），调用点不携带 profile 解析结果；会话级 overlay 需配套 actor 缓存失效矩阵（R10 双权威风险），本轮不强行接线。
> ⑭ 测试与反证：`internal/api/skills/profile_config_overlay_test.go` 5 例（覆盖生效 + 宿主快照不变 + nil/空状态同一指针回落 + 未声明/同值覆盖零变化 + 禁止域 `runtime.mode` 报错 + routing 域进视图但不泄漏宿主快照）；反证 F1（停用 `skillsRuntimeConfigFor` 覆盖消费 → `TestBuildProfileConfigOverlayAppliesSkillsRuntimeOverride` FAIL）/F2（强制产出视图 → `TestBuildProfileConfigOverlayZeroChangeStaysNil` FAIL），恢复后全绿。
> ⑮ 回归：`gofmt -l` 零输出；`go build ./...` OK；`go test ./internal/api/skills -run "TestBuildProfileConfigOverlay|TestSetAICLIConfig" -count=1` ok（0.24s）；全量 `go test ./internal/api/skills -count=1` 全绿（32.4s，EXIT=0）。
> 变更记录：2026-09-24 实施（Batch 12 完成并验证——Web 会话级切换 + 前端接线，FR-8 / D19/D21/D23 / R20）：
> ① 交付物（后端）：新增 `internal/api/skills/session_profile_switch.go`——`applySessionProfileSwitch` 五阶段（解析 → 应用 → 失效 → 身份与持久化 → 报告，设计 §18.1），与 CLI 侧 `chat_profile_switch.go` 同构（同一 JSON 契约 `ProfileSwitchReport`、同一失效三件套 ①锚点 ②稳定工具面 ⑥token 计数）。**server 差异（V15/V19 结论）**：「下一轮生效」的权威路径是**驱逐空闲 actor**（`invalidateSessionProfileRuntime`，`session_profile_switch.go:350-377`）——server 的 agent/prompt/工具策略在 `buildSessionActor` 构建期固化，只写 sessionmeta 而不驱逐等于假开关；在途 turn 绝不打断（D18/A3），登记进程内 pending 标记、由命令入口 `reconcilePendingProfileSwitch` 在 actor 空闲时兑现（`session_runtime_handlers.go:896-898`）。
> ② 命令面接线：`SubmitSessionRuntimeCommand` 增 `set_profile`（别名 `profile`）分支，**先于 hub 解析**处理（`session_runtime_handlers.go:876-894`）；请求结构增 `Profile` 字段（`:56`）；不新增路由（复用 `handler.go:952` 既有端点）。
> ③ 目录 API 与能力广告（R20）：`GET /api/runtime/profiles` 清单响应增 `session_switch` 布尔（`profiles_store.go:59/134`），声明本后端支持会话级切换；前端只在为 true 时注册 `/profile` 命令（`composer-builtin-commands.ts:9-10/34/45-47`）——旧后端不注册，而不是注册后执行时报错。
> ④ 前端：`api/runtime/profiles/session-switch.ts` `setSessionProfile()`（风格对齐 `approveSessionTool`/`interruptSessionTurn`）+ 契约归一（`normalize.ts`/`types/runtime/profiles.ts`）；composer 注册 `profile` 命令（`kind: popupSelect`）+ executor 分支 `runProfile`（`use-composer-command-executor.ts:306+/510-511`）+ 候选注入（`use-composer-command-surface.ts:142-151/228`）+ 候选纯函数 `lib/composer-profile-options.ts`；Switch Report 弹层 `composer-profile-dialog.tsx` + i18n（zh/en）+ 会话区接线（`main-section.tsx`）。
> ⑤ 测试：后端 `session_profile_switch_test.go` 4 例（持久化绑定并清失效面 / 未知 profile 是校验错误且状态不动 / 驱逐空闲 actor 使下一轮取新面 / 在途 turn 不打断并在下一边界收敛）；前端 `use-composer-command-executor.profile.test.tsx` 11 例（弹窗打开、ref 切换回执、大小写不敏感唯一命中、未命中/解析失败/目录未就绪三种"不可用"不混淆、无会话不请求、在途回执、差异告警、失败回填、宿主未接线）+ `composer-profile-dialog.test.tsx` 11 例 + `composer-profile-options.test.ts` 12 例 + `use-composer-command-surface.test.tsx`（候选注入/能力门控）。测试文件按行数门禁拆分出 `use-composer-command-executor.test-helpers.tsx`（共享脚手架；`vi.mock` 提升语义要求 mock 留在各测试文件内，故脚手架只放渲染/驱动）。
> ⑥ 回归：`go vet ./internal/api/skills` 退出 0；`go build ./...` OK；`go test ./internal/api/skills -count=1` 全绿（40.1s）；前端 `npx vitest run`（两个 executor 文件 34 例）通过、`npm run test` 全量 329 files / 2735 tests 通过、`npm run lint` 退出 0（i18n/no-backups/max-lines/message-tokens 全过）、`npx tsc -b` 退出 0。
> ⑦ 核实回填：V15（server 不经 `chatWebSession()`，actor 句柄 `hub.Get` 精确可得）、V16（executor 分支结构与 popupSelect 回填通路）、V19（单例仅 aicli 本地 web 适用，server 按 sessionID 精确失效）→ 设计文档附录 D 第 3/4/7 项同步回填。
> 变更记录：2026-09-24 实施（Batch 13 **slice 1** 完成并验证——`apply` 执行核心接线与会话契约；G3 / A12 的 apply 半程）：
> ① 交付物（后端）：`ApplyRuntimeProfile`（`profiles_write_handlers.go`）由 501 转真实实现——请求体 `profileApplyRequest{SessionID}`（**必填**，D31）→ `applySessionProfileSwitch`（Batch 12 执行核心，五阶段与失效动作零复制）→ 响应 `{ok, session_id, profile, switch_report}`（`switch_report` 与 composer `/profile` 同一 JSON 契约，无第二套语义）。错误分类：缺 `session_id` → 400（可执行提示）、切换校验失败 → 400、租约冲突沿用 `writeSessionLeaseConflict`、未知 profile 沿用 Batch 8 的 `writeProfileTargetError` 口径、未知会话 → **404**（`chat.ErrSessionNotFound`，与 `handler.go` 会话读取口径一致，不落 500）。
> ② 前端（设置页不再假报警）：设置页（`/runtime-config`）**没有会话上下文** → 页内 `apply` 只做引导——`profiles.tsx` 删除 `runApply` 与 `applyNotImplemented` 状态、按钮固定禁用（`applyDisabled`）；`profile-list-row.tsx` 的 `onApply` 转可选；i18n（zh/en）删 `applyNotImplemented`，`applyDisabled` 文案指向"在会话内用 `/profile` 切换"；`mutations.ts applyRuntimeProfile` 保留为端点绑定（含 API 测试），注释固化"sessionId 必填、服务端不推断「当前会话」"。
> ③ 测试：后端 `TestRuntimeProfilesAPI_ApplyWiresSessionSwitchCore`（缺参 400 + 提示 / 显式会话真实切换：`effective_at=next_turn`、`anchor_cleared=true`、sessionmeta 绑定落库、冻结锚点被清 / **A12 作用域**：其它会话与 `default` 零变化 / 未知会话 404）；`TestRuntimeProfilesAPI_SetDefaultAndNotImplementedBoundaries` 的 apply 断言由 501 改为 400；前端 `profiles.test.tsx` 末条用例改为"apply 在本页只做引导：按钮禁用、不发请求"。
> ④ 回归：`gofmt -l` 对改动文件零输出；`go test ./internal/api/skills -count=1` 全绿（41.350s）；前端 `npx tsc -b` 退出 0、`npm run test` 全量 329 files / 2735 tests 通过、`npm run lint` 退出 0（i18n/no-backups/max-lines/message-tokens 全过）。
> ⑤ 核实与登记：设计文档 §G3 新增 **D31**（apply 必须显式 `session_id`，服务端不推断"当前会话"；缺参 400 / 未知会话 404）；修正本表过期行——Batch 8「未开始」→ ✅ 已完成（`7fc2a3e0`/`a3bca1e8`）、Batch 9 悬空引用改指设计文档「Batch 9 落地状态」块、Batch 11 行 V17 改标"部分回填（命令面可用；写回细节随 Batch 13 E7）"、Batch 13 行 → 🚧 进行中（slice 1）。
> ⑥ Batch 13 剩余：`from_session`（create 第三模式）/ save-as 差分固化（D24）/ export·import（G5）/ 引用完整性四类检查（D25，V24 待回填）/ 原子写与冲突检测（R24，V25 待回填）/ TUI 11b 生命周期子命令 / 前端创建向导与列表操作 / E2E-1~5 与 A9-A11（A12 的 apply 半程本 slice 已绿）。

> 变更记录：2026-09-24 实施（Batch 14 **前置核实**完成——V20/V21 回填与 D29 接入设计冻结；P0 安全线）：
> ① V20（`foldertrust.Resolution` 消费方式）：**先例 = 单一 Resolution + 按资源类型消费**。Resolution 启动早期解析（`chat.go:582-585` 注释 "before profile/plugin discovery"；`agent_stdio.go:682-683`、`exec_run.go:259-260`、`exec_resume.go:95-96`），经 `applyChatFolderTrust`（`chat_folder_trust.go:143-149`）挂会话，访问器 `sessionProjectScopeAllowed`（:99-107）session 优先、回落进程级。既有门控点：插件 `plugin_runtime.go:28`；MCP `mcp_integration.go:213-218`（未信任 + `IsProjectScopedPath` → 项目级 MCP 配置直接不加载）+ `acp_mcp_host.go:549`；agentdef `agentdefDiscoverOptions`（`chat_folder_trust.go:129-140`，未信任只 `SkipProjectRoot=true`，builtins/user-home/profile root 仍放行）。
> ② V20 缺口实锤：上述放行名单包含 profile root → **项目 profile 内 agents 的 prompts 今日完全未门控**；`internal/profile` 与 `internal/api/skills` 对 foldertrust 零引用（唯一命中 `internal/profile/overrides.go:103-104` 是白名单排除域名，与门控无关）。
> ③ V21（是否已被间接门控）：**未被门控，且是"假门控"风险而非重复门控**——`foldertrust.CollectRepoConfigKinds`（`configs.go:31-108`）的 marker 扫描只有 plugins/hooks/mcp/agents，**不含 `.aicli/profiles`**；只有 `.aicli/profiles/` 的仓库 `RepoConfigsPresent=false` → `Decide` 第 4 步返回 trusted（`decide.go:51-53`）→ 只读 `res.Trusted` 的门控在 E2E-6 场景恒放行。**因此 Batch 14 必须同步扩展检测面**（新增 `ConfigKindProfiles` + marker `.aicli/profiles`，提示语 kindList 同步），否则 A13 无法成立。
> ④ 接入设计冻结（D29 落地口径）：门控点 = **prompt 的解析/消费边界**（`resolved.Prompts` → `profileinput.LoadPromptText/LoadPromptLayers` → `PromptText`/`PromptLayers`），CLI 与 server 共用同一实现；判定 = 项目层 profile（`resolved.Paths.ProfileRoot` 在 `<workspace>/.aicli/profiles` 下，与 `profile.LayerRoot("project")` 同源）且工作区未信任 → 清空 prompts + 标记 suppressed + 三处警告（TUI `/profile status`、启动摘要、Switch Report）；tools/skills/mcp/permission_mode/overrides **不受影响**（分级，D29 表）。**时序陷阱**：`chat_setup.go` 先投影 profile（:248-250）再挂 trust（:260）→ 门控读**进程级** `currentFolderTrust()`，不能读 `session.FolderTrust`；server 侧 `resolveProfileSessionState(profileRef, agentID, workspacePath)`（`profile_support.go:163`）已带 workspacePath，可在此计算信任。
> ⑤ 恢复路径（A14）：`/trust grant` 已在同一会话内重解析翻转（`chat_folder_trust.go:186-199`）+ `/profile reload`（`chat_profile_command.go:96-97`）→ 复用既有通道，不新增机制。
> ⑥ 断言锚点预置：A13 断言 `SystemPromptText`（`chat.go:163`）不含 profile 文本；空文本不会误伤基线提示——`chat_session.go:1949-1952` 的 `profilePrompt != ""` 守卫保证空 prompt 不走 replace。A14 同会话翻转：未信任投影 → `/trust grant` 重解析 → `/profile reload` → 断言恢复注入。
> 变更记录：2026-09-24 实施（Batch 14 **slice 2** 完成并验证——D29 检测面扩展 + 分级门控核心 + CLI/server 接线；P0 安全线）：
> ① 交付物（检测面，关闭 V21 的假门控缺口）：`internal/foldertrust/configs.go` 新增 `ConfigKindProfiles`（marker `.aicli/profiles`，`directoryNonEmpty` 规则与 agents 同款——只含点文件的空壳目录不计）；`internal/foldertrust/resolve.go` 提示语 kindList 写死值 `plugins/hooks/MCP` → `plugins/hooks/MCP/profiles`，文案改为 "can run commands or inject prompts"。此前只带 `.aicli/profiles/` 的仓库 `RepoConfigsPresent=false` → `Decide` 第 4 步 trusted，门控恒放行；扩展后该仓库进入 prompt/未信任分支。
> ② 交付物（门控核心）：新增 `internal/profile/prompts_gate.go`——`EvaluateProjectPromptGate`（判定，纯函数、零 I/O）+ `ApplyProjectPromptGate`（落地：清空 `Prompts` 内容载体、置 `PromptSuppressed`/`PromptSuppressionReason`，**保留 `PromptMode` 声明**——mode 只表达 replace/append 语义，内容为空时无副作用，且 `chat_session.go` 的 `profilePrompt != ""` 守卫保证空文本不替换基线提示）；`ResolvedAgent` 新增两个 JSON 字段供警告面与前端消费。判定口径：项目层（`foldertrust.IsProjectScopedPath(profileRoot, workspaceRoot)`，与 MCP 门控同一实现）+ 工作区未信任 + 有可扣留内容；user 层 profile 永不受影响；工作区根未知 + 未信任 → 失败关闭；无 prompt 文件 → 不制造"假警告"。分级：tools/skills/mcp/permission_mode/overrides **不受影响**（D29 表）。
> ③ 接线：CLI 单点 `resolveChatProfileState`（`cmd/aicli/commands/chat_profile.go`，profile 解析后、`BuildResolvedAgentInputs` 前）——读**进程级** `currentFolderTrust()`（时序：会话级 trust 挂载在 `chat_setup` 更晚，不能读 `session.FolderTrust`）；server 双点 `resolveProfileRuntimeState` / `resolveProfileSessionState`（`internal/api/skills/profile_support.go`）+ 新增 `workspaceFolderTrust(workspacePath)`（`SkipPrompt=true` + `Interactive=false`：server 永不弹提示，项目级配置存在且无记录即未信任=失败关闭；特性关闭时 foldertrust 既有语义 trusted → 门控自然放行）。
> ④ 测试（A13/A14 核心半程）：`internal/profile/prompts_gate_test.go`（判定矩阵：trusted / 未信任项目层 / 未信任外部层 / 未知根失败关闭 / user-home 层豁免 / 无内容不告警 + 落地断言含"分级不误伤"）；`internal/foldertrust/foldertrust_test.go` 增补 `.aicli/profiles` 检出与点文件空壳反例；`cmd/aicli/commands/chat_profile_prompt_gate_test.go`（CLI：未信任 `state.PromptText` 为空且 tools 声明保留；信任翻转后恢复注入）；`internal/api/skills/profile_prompt_gate_test.go`（server 半程：未信任 `PromptText` 为空 + 特性关闭恢复）。
> ⑤ 回归：`go build ./...` 退出 0；`go test ./internal/foldertrust/... ./internal/profile/... ./internal/profileinput/... ./internal/api/skills/... ./cmd/aicli/commands/... -count=1` 全绿；`gofmt -l` 对本次改动文件零输出。
> ⑥ 余项（slice 3/4）：三处警告面（TUI `/profile status`、启动摘要、Switch Report）、前端 Profiles 页"部分内容未应用"徽标 + 一键信任入口（Q22）、E2E-6/7 手工剧本（未信任目录含 `.aicli/profiles/`）。
> 变更记录：2026-09-24 实施（Batch 14 **slice 3** 完成并验证——三处警告面（CLI + server）；P0 安全线）：
> ① 交付物（CLI 警告面）：`ChatSession` 新增 `ProfilePromptSuppressed`/`ProfilePromptSuppressionReason`（`chat.go:172-179`，注释说明"禁止静默少一层生效面"），由 `applyProfileStateToChatSession`（`chat_profile.go`）投影；新增单点文案函数 `profilePromptSuppressionNotice`（reason + `；/trust grant 后可 /profile reload 恢复`），三处复用：TUI `/profile status`（`chat_profile_command.go`，`SystemPromptText` 行后追加 `⚠`）、启动摘要（`chat_profile_summary.go`，此前被扣留时该行整体消失 → 现在显式 `⚠` 提示）、Switch Report（`chat_profile_switch.go`，`report.Warnings` 追加；detach profile 路径同步清空两个标记，避免残留假警告）。
> ② 交付物（server 警告面）：`internal/api/skills/session_profile_switch.go` 新增 `sessionProfilePromptSuppressionWarning(state)`，`applySessionProfileSwitch` 阶段 2 追加到 `report.Warnings`（Web 端不再出现"已切换成功却少一层提示词"的假开关）。
> ③ 测试：`cmd/aicli/commands/chat_profile_prompt_gate_test.go` 新增 `TestProfilePromptSuppressionWarnings`（`setProcessFolderTrust` 构造进程级未信任态 → 断言统一文案 / status `⚠` / 摘要 `⚠` / switch 警告四断言）；`internal/api/skills/profile_prompt_gate_test.go` 补两条（未信任→有警告；特性关闭→无警告）。
> ④ 回归：`go build ./...` 退出 0；`go test ./internal/api/skills/... -count=1` 33.4s 全绿；`go test ./cmd/aicli/commands/... -count=1` 135.2s 全绿（首轮一次 flake = `TestResumePickerSessionLoaderPinsCurrentSession` 撞 sqlite 并发栈，单独重跑与全包复跑均绿，判定为并行跑两个 go test 进程的环境抖动，非本次改动）；`gofmt -l` 对本 slice 改动文件零输出（`session_profile_switch.go` 存在**本次改动区域之外**的历史格式漂移：结构体字段对齐 + 一处注释空格，未顺手 reformat 以免污染 diff）。
> ⑤ 余项（slice 4）：前端 Profiles 页"部分内容未应用"徽标 + 一键信任入口（Q22）、E2E-6/7 剧本（未信任目录含 `.aicli/profiles/`）。
> 变更记录：2026-09-24 实施（Batch 14 **slice 4** 完成并验证——E2E-6/7 自动化剧本落地；P0 安全线）：
> ① 交付物（E2E-6，未信任仓库门控）：新增 `cmd/aicli/commands/chat_profile_e2e_test.go::TestE2E6UntrustedWorkspaceProfileGate`，**走真实判定链**而非直接构造 `foldertrust.Resolution`（补上 slice 2 单测的覆盖缺口）：临时工作区只放 `.aicli/profiles/<name>/`（`profile.yaml` + `agents/coder/prompts/system.md` 标记文本 + `agents/coder/tools/policy.yaml` denylist）+ `AICLI_HOME` 指向临时目录（真实信任存储零记录、`AICLI_FOLDER_TRUST=1`）→ `foldertrust.Resolve(SkipPrompt=true)` 必须给出 `OutcomeUntrusted`（**钉住 V21 修复点**：`.aicli/profiles` 必须计入 marker 扫描，否则 `Decide` 第 4 步直接放行=假门控）→ `resolveChatProfileState` + `applyProfileStateToChatSession` + `applyChatFolderTrust`（与 `chat_setup.go:248-260` 同序，门控读进程级结论）→ 四断言：`composeDurableChatSystemPromptWithGuidanceForCWD` 不含 profile 文本（prompts 未应用）、`session.ToolPolicy` 非空且 `AllowTool("write_file")` 报错而 `AllowTool("read_file")` 放行（**分级不误伤**；该策略即执行器 `buildLocalChatToolPolicy` 的 `Clone` 源）、`profilePromptSuppressionNotice` 含「未信任」、`chatProfileStatusText` 含「未应用」；随后 `foldertrust.GrantTrust(workspace)` → 重解析（`Trusted`）→ 同一会话重新投影 → 断言 profile 文本**恢复注入**且 suppressed 标记清除（A14 端到端）。
> ② 交付物（E2E-7，resume 漂移容错）：`TestE2E7ResumedProfileDriftKeepsSessionUsable`——会话绑定 `user:gone` → profile 已不存在 → `chatReapplyResumedProfileState` 必须在 stderr 给出「解析失败」+ `/profile reload` 恢复路径（`captureStderr` 捕获），且会话可用：身份字段（`ProfileReference`/`ProfileName`/`ProfileAgent`）保留（R18 不静默降级：绑定不丢，下一次 sync 不会误删 sessionmeta 的 `profile_ref`）、`SystemPromptText` 保持空（不半截生效）。
> ③ 回归：`gofmt -l cmd/aicli/commands/chat_profile_e2e_test.go` 零输出；`go test ./cmd/aicli/commands/ -run "TestE2E6|TestE2E7" -count=1 -v` 全绿（0.49s）。全包 `go test ./cmd/aicli/commands/... -count=1`（155.9s）首轮两处失败（`TestChatSlashArgumentCompletionProfileWithoutSession` / `TestProfileCommandPickWithoutProfilesExplainsHow`）经定位为**环境残留**：本机真实 `~/.aicli/profiles/xxx` 是一次旧运行遗留的测试模板产物（`--template coding --to user`），而这两个用例假设 user 层目录为空；已将该目录移出到 `%TEMP%\aicli-stale-profile-xxx-20260924230300`（未删除），复跑四用例全绿。另复跑 `TestProfileCommandLayerProfileCreatedByTUIResolves` 前后该目录 mtime 未变（22:48:04），确认现行 `useTemporaryHome`（同时设 `HOME`/`USERPROFILE`）已隔离、无持续泄漏——**与本次改动无关**。
> ④ 余项（Batch 14 收口）：前端 Profiles 页"部分内容未应用"徽标 + 一键信任入口（Q22）。

> 变更记录：2026-09-24 实施（Batch 14 **slice 5** 完成并验证——Q22 前端闭环：清单信任上下文 + "部分内容未应用"徽标 + 一键信任入口；M6 收口）：
> ① 交付物（后端数据面，`126645d2`）：`GET /api/runtime/profiles` 新增可选 `workspace` 参数（别名 `workspace_path`）——给出时响应附 `workspace_path` / `workspace_trusted` / `workspace_trust_feature_enabled`，并对逐条有效条目做 D29 判定，命中者标 `prompt_suppressed` + `prompt_suppression_reason`；不给出时响应与既有完全一致（旧调用零变化）。判定**与运行期门控同源**（`profilesys.EvaluateProjectPromptGate`，即 `profile_support.go` 的 `ApplyProjectPromptGate`），清单上的"未应用"与实际生效面不会分叉；只对确有 prompt 内容的项目层 profile 置位（无内容不制造假警告）。成本：信任特性关闭或已信任 → 零额外开销，仅"未信任"时对有效条目各做一次解析。
> ② 交付物（信任端点）：新增 `GET/POST /api/runtime/harness/trust`（`handler.go` 路由注册）——读侧按 `workspace_path` 现算（`workspaceFolderTrust`：`SkipPrompt=true` + `Interactive=false`，server 永不弹提示 → 失败关闭）；写侧只接受 `action="grant"`（其它动作 400；**撤销信任不提供 UI 动作**，仍走 CLI `/trust`，避免误触让项目级配置整体失效），授权与 profile 写端点同级（回环 / admin token / admin role），读写共用 `buildHarnessTrustResponse` 单一响应构造。
> ③ 交付物（前端 UI）：新增 `profiles-trust-notice.tsx`——未信任时的提示条 + **两步确认**一键信任（首次点击只展开确认、不发请求；`profiles-trust-grant`/`-confirm`/`-cancel` 三个 testid）；`profiles.tsx` 以 `useRuntimeClientIdentity()` 取工作区（与 Harness 设置页同源），`listRuntimeProfiles({ workspace })` 带参请求并把响应三字段存进 `trust` 状态，仅当 `workspacePath && featureEnabled && !trusted` 时渲染提示条；授予成功 → `refresh()` + `profiles.trust.granted` 回执，失败走 `formatProfileError`。`profile-list-row.tsx` 对 `promptSuppressed` 条目在名字旁挂"部分内容未应用"徽标（`Badge` 不透传 `title`/`data-*`，故 testid 与 `title` 挂在外层包裹 `span`）；i18n zh/en 同步新增 `list.promptSuppressed` 与 `trust` 块（键结构一致，`lint:i18n` 0 违规）。
> ④ 测试：后端 `internal/api/skills/harness_trust_handlers_test.go` 2 例（闭环：未信任 → 清单标记 → `action=grant` → 同一清单标记消失 → 再 GET 仍信任；边界：非 `grant` 动作 400）；前端 `profiles-trust.test.tsx` 3 例（未信任：请求带 `workspace` + 提示条 + 徽标；两步确认：POST body 含 `action=grant`/`workspace_path`，刷新后徽标与提示条消失且回执含 granted 文案；特性关闭：不渲染提示条）。回归修补：`use-composer-command-surface.test.tsx` / `composer-profile-options.test.ts` 的清单 fixture 补新字段。
> ⑤ 回归：后端 `go test ./internal/api/skills/ -count=1` 全绿（25.3s）；前端 Profiles 设置域 + composer 相关 8 文件 74 例全绿、`npx tsc -b` 退出 0、`npm run lint:i18n` 0 违规（scanned=902）。
> ⑥ 结论：Batch 14 四项（分级门控 / 三处警告面 / 信任后恢复 / A13-A14 + E2E-6/7）与 Q22 前端闭环全部落地 → **M6 收口**；撤销信任与多工作区批量信任仍走 CLI（登记为后续可选增强，非本批次门禁）。

> 变更记录：2026-09-24 实施（Batch 13 **slice 10** 完成并验证——前端「从当前会话创建」入口（`/profile save-as`）+ V24/V25 回填；M5 收口）：
> ① 交付物（前端数据面）：`types/runtime/profiles.ts` 的 `RuntimeProfileCreateRequest` 新增 `fromSession?`（与 `template`/`fromRef` 互斥，注释固化）；`api/runtime/profiles/normalize.ts` 的 `buildCreateBody` 落 `from_session`，`normalizeCreateResponse` 读回 `mode`/`fromSession`/`baseline`/`surface`/`omitted`（save-as 报告面，D36 契约）。
> ② 交付物（命令面）：`composer-builtin-commands.ts` 新增 `parseProfileSaveAsCommandArgs`（`need-name` / `unknown-flag` / `bad-layer` / `unexpected-argument` 四态；`--to user|project` 缺省空串 = 由后端按默认层落盘，不猜层）；`use-composer-command-executor.ts` 的 `runProfile` 前置子命令分流（`save-as` / `save-as …`）→ `runProfileSaveAs`：无会话 → `noSession`、进行中 → `inProgress`（`savingAsRef` 并发保护，不并发写盘）、成功 → `saveAs.done`（values `{profile: ref || name, fields, omitted}`）、失败 → `saveAs.failed` + logger（不伪装成功）；`saveAsSurfaceFieldCount` 按后端 `runtimeProfileSaveAsSurfaceSummary` 的**扁平计数摘要**口径统计差分字段数。i18n zh/en 对称新增 `composer.builtin.profile.saveAs.*`，并把 `argumentHint` 扩为 `[profile | save-as <名称> [--to user|project]]`。
> ③ 测试：新增 `use-composer-command-executor.profile-save-as.test.tsx` 6 例（成功路径请求体 `{name, layer: undefined, fromSession}` + 回执 values / `--to project` 透传 / 四态参数错且不发请求 / 无会话 / 并发保护（deferred promise）/ 后端失败不伪装成功）；`composer-builtin-commands.test.ts` 追加 `parseProfileSaveAsCommandArgs` 4 例（含"开关在前也认"与"`--to` 后无值 = 层非法而非按缺省"）。回归：`npx vitest run`（3 文件 31 例）全绿。
> ④ 核实回填：V24（server 侧会话绑定唯一写入点 `applyProfileSessionContext`；引用检查三类键全覆盖）、V25（原子写工具 `writeProfileYAMLAtomic` 的适用面 + rename 声明名改写非原子但带目录回滚 + R24 冲突 409 拒绝）→ 见 §3.1。
> ⑤ 结论：设计文档 D38 遗留①「前端"从当前会话创建"按钮缺（`from_session` 有 API、前端零引用）」**关闭**；Batch 13（M5）出口条件（E2E-1~5 + A9-A12）达成，M5 收口。

> 变更记录：2026-09-24 实施（Batch 6 **slice 1** 完成并验证——FR-11 `--profile auto` 自动路由：单一权威 + 规则配置化；P2 提前落地）：
> ① 交付物（单一权威）：新增 `internal/profile/autoroute.go`（`AutoProfileRef`/`AutoRouteRule`/`AutoRouteConfig`/`DefaultAutoRouteRules`/`IsAutoProfileRef`/`NormalizeAutoRouteRules`/`RouteProfileForPrompt`/`ResolveAutoProfileRef`），匹配语义（小写 + 子串包含；空提示词 → `""` 表示"不路由"，由调用方保留默认语义）与历史 server 实现逐字一致；删除 `internal/api/skills/handler.go` 内的重复实现（`routeProfileForPrompt`/`containsAny`/`isAutoProfileRef`），CLI 与 server 共用同一份规则。
> ② 交付物（配置面）：`agentconfig.ProfilesConfig` 新增 `auto{fallback, rules[{profile,keywords}]}` + 映射函数 `profilesys.NewAutoRouteConfig`（放 internal/profile，避免 agentconfig 反向依赖）；`rules` 非空 = 整体替换内置启发式、按声明顺序首个命中胜出，`fallback` 空 → `executor`；未启用 `auto` 的部署逐字零变化（NFR-1）。
> ③ 交付物（server 接线）：`ProfileSupportConfig` 新增 `AutoRoute`（`SetProfileSupport` 设置期快照，复制 Rules 防调用方后续改切片）；`(*Handler).routeAutoProfileForPrompt`（nil-safe）替换内联启发式，`prompt_layout_debug.go` 同一入口；`cmd/runtime-server/main.go` 传 `profilesys.NewAutoRouteConfig(cfg.Profiles)`（与 registry 同一快照生命周期，热重载一起重建）。
> ④ 交付物（CLI 接线）：`resolveChatProfileState` 启动期解析 `--profile auto` / `profiles.default_profile: auto`——先路由再进注册表，**Reference 落地为具体 profile**（`/profile reload`、`/profile save` 无需提示词即可复用同一引用）；`AutoRoutedFrom` 归因贯穿 `chatProfileState` → `ChatSession.ProfileAutoRoutedFrom` → `applyProfileStateToChatSession`；两处可见面：启动摘要 `Profile Route:` 行、`/profile status` `路由:` 行（非 auto → 零变化）；启动期无提示词（纯交互式 chat / agent stdio）→ 显式报错并给出替代（`--prompt`/`--message`/exec stdin/`/profile use`），不猜、不静默降级；`profile` 命令 Long 同步说明。
> ⑤ 测试与反证：3 个新测试文件——`internal/profile/autoroute_test.go`（历史启发式逐字对齐 / 规则替换 / 脏规则丢弃 / 归一化 / `NewAutoRouteConfig` 映射 / ref 判定 / `ResolveAutoProfileRef`）、`internal/api/skills/profile_auto_route_test.go`（默认启发式 / 配置规则 / 设置期快照）、`cmd/aicli/commands/chat_profile_auto_route_test.go`（路由落地 / 无提示词报错 / 配置默认 auto / 显式 ref 优先 / 归因可见面）。反证已执行：临时令 `RouteProfileForPrompt` 恒返回兜底 → 三层用例同时失败（core / server / CLI），随后还原并复绿。
> ⑥ 回归：`go build ./...` 退出 0；`go test -count=1 ./internal/agentconfig/ ./internal/profile/`（9.5s / 2.0s）、`go test -count=1 -run Profile ./internal/api/skills/`（2.8s）、`go test -count=1 -run Profile ./cmd/aicli/commands/`（6.3s）全绿；`gofmt -l` 对本次改动文件零输出。
> ⑦ 文档：`docs/aicli/profiles.md` 新增 `--profile auto` 小节（含 `profiles.auto` YAML 示例与失败语义）；设计文档 §1.1 全局配置行与 FR-11 条、本文件 Batch 6 段与附录 P 跟踪表同步回填。
> ⑧ 余项（Batch 6 其余三项，仍 P2 按需）：FR-12 runtime-server 只读 API + frontend 展示、FR-13 usage ledger 按 profile 聚合、FR-14 workspace `.aicli/profile` 项目级绑定；另登记可选增强：交互式会话"首轮延迟路由"（需 turn 边界 hook + 切换报告，咽喉点见 `chat_actor_executor.go:156-217`）。

> 变更记录：2026-09-24 实施（Batch 6 **slice 2** 完成并验证——FR-13 usage ledger 按 profile 聚合：记录面 + 聚合面；P2 提前落地）：
> ① 交付物（记录面·写时单一权威）：两处写入点同键同义——`internal/usageledger` 新增可选 `WithProfileLookup`（`NewService(store, opts ...Option)` 变参，旧调用点逐字兼容），事件时刻经注入回调解析 profile；`cmd/aicli/commands/chat_cache_local.go` 接线 `localSessionProfileLookup(host.SessionStore)`（声明名 `sessionmeta.ProfileName` 优先，回退绑定 `ProfileRef`；会话读不到/未绑定返回 ""）。runtime-server 路径：`UsageScope` 新增 `Profile`（`json:"profile,omitempty"`，不参与配额身份），AgentChat 在 profile 解析后**单点赋值**（覆盖全部 8 个 `recordUsage` 调用点，含 `streamLLMChat` 的传值路径），`appendUsageLedger` 落 `metadata.profile`；新增 `ledgerProfileName`（声明名优先、回退 ref，随 `usage_ledger_group.go`）。
> ② 不猜纪律：`execute` 等未解析 profile 的入口、未绑定会话、历史行一律不写 `profile` 键——聚合时归入 `profile=""`（未归属）组，分组请求数守恒、未接线路径可观察；`WithProfileLookup` 未注入或解析为空同样不写键。
> ③ 交付物（聚合面）：`GET /api/runtime/usage/ledger?group_by=profile` → `groups[]`（`profile/requests/failures/input_tokens/output_tokens/total_tokens`，按 total_tokens 降序 → profile 升序）+ `grouped_total`；聚合基于"过滤后、截断前"集合（`records` 仍按 `limit` 截断）；未指定 `group_by` 时响应三键（records/count/filters）逐字节不变；非法 `group_by` 返回 400；零 schema 变更（复用 `metadata_json` 列）。新文件 `internal/api/skills/usage_ledger_group.go`。
> ④ 测试与反证：`internal/usageledger/service_profile_test.go`（写入正例 + 未注入/空答案两个反例）、`internal/api/skills/usage_ledger_group_test.go`（分组守恒/未归属合并/排序确定性/`UsageScope` JSON omitempty/`ledgerProfileName` 回退链）、`cmd/aicli/commands/chat_cache_local_profile_test.go`（声明名优先/回退 ref/未知会话三例）。反证已执行：临时禁用 `onRequestFinished` 的 profile 写入 → `TestServiceRecordsProfileFromLookup` 立即失败（`expected: "coding" / actual: <nil>`），还原并复绿。
> ⑤ 回归：`go build ./internal/usageledger/ ./internal/api/skills/`、`go build ./cmd/aicli/...` 退出 0；`go test ./internal/usageledger/ -count=1`、`go test ./internal/api/skills/ -run 'AggregateUsageLedger|UsageScopeJSON|LedgerProfileName' -count=1`、`go test ./cmd/aicli/commands/ -run 'TestLocalSessionProfileLookup' -count=1` 全绿；`gofmt -l` 对本次改动文件零输出。
> ⑥ 文档：设计文档 §2 P2 FR-13 条新增落地状态（记录面键名/不猜纪律/聚合参数与口径/未归属语义）；本文件 Batch 6 段与附录 P 跟踪表同步回填。
> ⑦ 余项：FR-13 **前端展示面**（分组对比 UI/报告——前端已有 `getUsageLedger` 消费方，`normalizeUsageLedger` 契约需扩展 `groups`）；FR-12、FR-14 仍 P2 待排期。

> 变更记录：2026-09-24 实施（Batch 6 **slice 2b** 完成并验证——FR-13 前端展示面：usage ledger 分组对比 UI；P2 提前落地）：
> ① 交付物（契约面）：`types/runtime/usage.ts` 新增 `UsageLedgerGroup`；`UsageLedgerView` 增 `profileGroups: UsageLedgerGroup[] | null` 与 `groupedTotal: number | null`。`api/runtime/usage.ts`：`UsageLedgerQuery` 增 `groupBy?: "profile"`（URL 构造落 `group_by`，未传不发参）；新增 `normalizeUsageLedgerGroup`（`profile` 键必须存在且为 string——空串是合法的「未归属」组身份，不能像 record 缺 id 那样丢弃）；`normalizeUsageLedger` 解析 `groups`（非数组 → `null`）与 `grouped_total`（须为非负有限数，否则 `null`）；`hooks/use-usage-quota.ts` 的 `UsageQuotaLedgerFilters` Pick 增 `groupBy` 并透传（`useCallback` 依赖同步）。
> ② 交付物（展示面）：新增 `pages/usage-analytics/quota-ledger-groups.tsx`（`LedgerProfileGroups`：分组表 + 「未归属」标记 + 聚合总数「参与聚合 N 条（截断前全量）」）；`quota.tsx` 以 `useMemo` 固定 `groupBy: "profile"` 请求（分组是面板固有展示面，非用户筛选），组件拆出以守 500 行门禁（曾达 512 行触发 `verify:lines` 失败）；i18n zh/en 对称新增 `quota.ledger.groups.*`。
> ③ 不伪造空态：旧后端忽略 `group_by` → `profileGroups === null` → UI 提示「未返回分组」，绝不表达成「正常但为空」；`groups: []` 是真实空态；`groupedTotal` 可 > `records.length`（后端聚合基于过滤后、截断前集合），UI 不据此推断分页。
> ④ 测试：`usage.test.ts` +4 例（未请求分组 → null / 解析含空 profile 与丢弃缺 profile 条目 / `groups` 非数组与非法 `grouped_total` → null / 传 `groupBy` 发 `group_by=profile` 且未传不发）、`use-usage-quota.test.tsx` +1 例（透传；固件补字段）、`quota.test.tsx` +3 例（分组表渲染含「未归属」与聚合总数 / 未返回分组如实提示 / 空数组真实空态）——3 文件 40 例全绿。
> ⑤ 反证：临时禁用 groups 解析（`Array.isArray(record.groups) && false`）与面板 `groupBy: "profile"` → **5 例精确失败**（解析面 3 + 面板请求面 2；hook 测试未受影响，分层正确），还原后复绿。
> ⑥ 回归：`npx tsc -b` 退出 0；`npm run lint` 退出 0（0 errors / 2 warnings 均为既有 `react-hooks/exhaustive-deps`，与本切片无关）；`npm run lint:i18n` 0 违规（scanned=905）；`npm run verify:lines` 0 个 > 500 非空行；全量 `npx vitest run` 退出 0（333 files / 2768 tests passed，284.62s；含同树并行工作流的测试文件）。
> ⑦ 文档：设计文档 §2 P2 FR-13 条落地状态补前端半程；本文件 Batch 6 段与附录 P 跟踪表同步回填。
> ⑧ 余项：FR-12、FR-14 仍 P2 待排期。

> 变更记录：2026-09-24 实施（Batch 6 **收口**——FR-12 核实回填 + FR-14 后置确认；P2 项清账）：
> ① 核实（FR-12 Web 集成）：只读面与设置页展示已由 **Batch 8（M4）** 完整覆盖，无需新增代码——`GET /api/runtime/profiles`（三来源清单 + 默认标注 + 解析状态，`profiles_handlers.go:25-26`）与 `GET /api/runtime/profiles/{ref}`（解析后视图：工具面/skills/mcp/prompts/agents/overrides/估算）；前端设置页 Profiles 面板（`backend-config-settings-page/sections/modes/profiles.tsx:77/124`）以 `listRuntimeProfiles({ workspace })` + `getRuntimeProfile(ref)` 展示列表与详情。
> ② 命名区分（设计文档原注记「与 subagent 路由难度档位 profile 区分」）：面板标题 "Profiles"、副标题「按场景维护 profile.yaml：工具/技能/MCP/提示词/Agent/偏好一次成型，保存前可先校验并预览影响面。」；路由域使用「难度档位」（`editor-agent-routing.ts`），两者不共用词汇。
> ③ FR-14 状态确认：按 **Q12 结论**（`.aicli/profile` 项目级绑定后置、不在本期范围）保持待排期，不计入 Batch 6 本期出口。
> ④ 回填：设计文档 §2 P2 FR-12 条新增落地状态；本文件 Batch 6 段（条目 ✅ + 落地状态句 + FR-12 回填要点）与附录 P 跟踪表 Batch 6 行由 🚧 改 ✅（本期范围 FR-11/FR-12/FR-13 全落地；FR-14 按 Q12 后置）。
> ⑤ 结论：Batch 6（P2 按需项）本期范围清账——FR-11 ✅（slice 1）、FR-12 ✅（随 Batch 8，核实回填）、FR-13 ✅（slice 2 后端 + slice 2b 前端）；FR-14 为唯一余项且已按 Q12 决策后置。

> 变更记录：2026-09-24 实施（Batch 13 **E7 复核**（V17 清账）+ HEAD 悬空引用修复）：
> ① E7 逐行复核（`/profile save --to session|workspace|config` 写回实现，结论 = **复用既有存储、无第二套**）：层常量单一来源 `chat_routing_command.go:34-36`；**session 层零写回**——会话绑定经 sessionmeta ⑩ 持久化（身份 4 键 + resume 回填），命令只回显"无需额外保存"；workspace 层 = `agentconfig.WorkspacePrefsPathForPath` + `UpdateProfilesConfig`（与 `/routing save --to workspace` 同 prefs 文件族、不同 section；N9 不回退 cwd）；config 层 = `ensureWritableAICLIConfigPath` + `UpdateProfilesConfig` + `--yes` 二次确认，写后 `chatProfileFileDeclaresDefaultProfile` 只读校验，配置分层把 profiles 段路由到归属层时如实提示"未落在上述路径"（不谎报成功）；写前门禁 = 子会话只读拒绝 / 无绑定拒绝 / 未知层拒绝（不静默回退默认层）。
> ② 证据（**干净 HEAD `b522f2f6` 的临时 worktree**，隔离同树在途改动）：`go test ./cmd/aicli/commands/ -run "TestProfileCommandSave|TestProfileSwitch_IdentityPersistsAcrossResume" -count=1 -v` → 6 例全绿（0.635s：SessionLayerIsNoOp / ConfigRequiresConfirmation / WorkspaceLayerWritesPrefs / WithoutBindingFails / UnknownLayerFails / A4 身份跨 resume）；工作树补充：`go test ./internal/profile/` 全包 ok（2.670s）、`TestProfileCommandLayerProfileCreatedByTUIResolves` PASS（0.13s，层兜底"写读分叉"闭环）。
> ③ **HEAD 悬空引用修复（重要，超出 V17 范围但阻塞全仓）**：`74b42959`（Batch 14 slice 3）提交的 `chat_profile.go:195` 调用 `profilesys.RegisterLayerFallbacks`，其定义此前**只存在于同树并行工作流的未提交** `internal/profile/layer.go`（纯新增 +154）→ 干净 HEAD 自该提交起无法编译 `cmd/aicli`（`git grep HEAD` 全仓仅调用点、零定义；此前各切片"绿灯"均在脏工作树取得、**对 HEAD 无效**）。修复 = `b522f2f6` 仅提交该定义文件（`LayerNames` / `LayerRootForWorkspace` / `LayerProfile` / `LayerForRoot` / `LayerProfiles` / `RegisterLayerFallbacks`），census 确认无其他同类悬空引用；在途测试与 server 侧镜像仍未提交（归并行工作流）。
> ④ 环境提示（非缺陷）：干净检出的 `go build ./...` 会因 `internal/webui` embed `dist` 目录缺失而失败——该目录是前端构建产物（主工作树已生成），需先构建前端再全量编译。
> ⑤ 回填：本文件 §3.1 V17 行 待回填 → ✅ 已回填；附录 P 跟踪表 Batch 11/13 行"V17 部分回填"同步更新；设计文档第四部分核实清单第 5 行标注复核完成。

> 变更记录：2026-09-24 **完成度审计**（profile 全链路复核 + 设计文档回填清账）：
> ① 审计证据（**干净 HEAD `772793b2` 的临时 worktree**，隔离同树在途脏改动）：`go build ./cmd/aicli/... ./internal/profile/... ./internal/api/skills/... ./internal/foldertrust/...` 退出 0；`go test ./internal/profile/...`（3.4s）/`./internal/foldertrust/...`（1.3s）/`./internal/profileinput/...`（1.2s）全绿；定向 `go test ./cmd/aicli/commands/ -run "Profile|E2E6|E2E7"` 全绿（6.9s，复跑 6.1s）/`./internal/api/skills/ -run "Profile|PromptGate"` 全绿（5.3s）。**注**：首轮 CLI 测试与 vitest 并发时出现 Go 编译器崩溃 + 3 个 vitest worker 异常退出，单独/复跑均全绿——判定为机器负载抖动（并行工作流同时构建），非代码缺陷。
> ② 前端证据（`frontend/src` 零在途改动 ≡ HEAD）：`npm run verify:lines` 0 超标（1324 文件，最大 500 行）；`npm run lint:i18n` scanned=905 / violations=0；定向 vitest 10 文件 / 86 例全绿；全量 `npm run test` **333/333 文件全绿**（371.6s）。
> ③ 审计结论：附录 P 跟踪表 Batch 0-14 全部 ✅；§3.1 V 表（V1-V27）零待回填；**唯一未实施项 = FR-14**（按 Q12 后置，需先撤该决策才可开工）。
> ④ 设计文档回填清账（同 commit）：状态行「待实施」→「已实施」；附录 A 四行「待核实」→ ✅ 回填（V1-V4）；附录 C/D/E 标题「（待回填）」→「（已回填）」；附录 E 新增回填结论表（第 1/2/4/5/6/7 项 → V20/V21/V23/V24/V25/V17）。

> 变更记录：2026-09-25 实施（**FR-14 分阶段解冻 第一阶段**：项目绑定只读发现 + 显式应用；自动默认激活仍后置）：
> ① 决策与范围（对齐 `docs/plan/fr14-profile-binding-implementation-plan-20260925.md`）：Q12 由“整体后置”改为**分阶段解冻**——只读发现与显式应用进入本期；自动默认激活、多工作区自动解析、默认开启策略**仍后置**（不引入任何隐式激活路径）。
> ② 交付物（后端·单一绑定 helper）：新增 `internal/profile/binding.go`（`ProjectProfileBinding` / `LoadProjectProfileBinding`）——只读 `<workspace>/.aicli/profile`，pointer-only（唯一字段 `profile`，必须字符串标量）、ref 单段安全校验（拒绝绝对路径/驱动器/UNC/`/`/`\`/`:`/`.`/`..`/路径穿越/非法名）、目标限定 `<projectRoot>/<ref>/profile.yaml` 且必须存在；`Present=false`（无文件）与 `Present=true, Valid=false`（文件坏了/目标缺失）刻意分开，**不回退** user/config/default。
> ③ 交付物（后端·workspace-aware 发现）：`internal/profile/layer.go` 新增 `LayerProfilesForWorkspace`（project 层按工作区解析、user 层不变；空工作区退化为既有 `LayerProfiles()`），层扫描口径抽成 `layerProfilesWith` 单点；`internal/api/skills/profiles_store.go` 列表端点：workspace 声明时按该工作区枚举项目层、回填 `project_binding` 元数据、给绑定目标标注只读 `is_bound`（**不改** `is_default`）、`annotateWorkspaceTrust` 复用同一 `EvaluateProjectPromptGate` 给绑定目标打 D29 扣留标记；workspace 参数本身不可用（不存在/非目录）→ 400，绑定**文件**问题仍是 200 + `project_binding.error`。
> ④ 交付物（前端）：`types/runtime/profiles.ts` 新增 `RuntimeProfileProjectBinding` 与条目 `isBound`；`api/runtime/profiles/normalize.ts` 新增 `normalizeProfileProjectBinding`（字段缺失 → `null`，不把旧后端当成“绑定无效”）；新增只读卡片 `modes/profiles-project-binding.tsx`（引用/工作区/指针文件/目标目录/状态/错误/D29 提示）并在 `profiles.tsx` 接线（仅 `present=true` 时挂载）；`profile-list-row.tsx` 增“项目绑定”徽标；i18n zh/en 对称新增 `profiles.list.projectBound` 与 `profiles.projectBinding.*`。
> ⑤ 只读与显式应用（A12/D26 语义不变）：发现不写盘、不改 default、不碰会话；绑定卡片**不提供**“应用到会话”按钮（设置页无会话上下文，apply 必须显式 session_id），改为指向会话内 `/profile <ref>`——复用既有显式切换核心，未新增第二套 actor/session 变更路径。
> ⑥ 测试与反证：新增 `internal/profile/binding_test.go`（缺文件/坏文档表 13 例/不安全 ref 表 14 例/目标缺失不回退 user 同名/目标非文件/双工作区隔离/相对路径归一化）、`layer_test.go` 增 workspace 枚举用例（cwd 项目层不得混入）、`internal/api/skills/profiles_binding_handlers_test.go` 8 例（合法绑定元数据 + `is_bound` 且 `is_default=false`、无绑定文件不是错误、非法 YAML → 200 + error、目标缺失不回退 user 同名、非法 workspace → 400、双工作区隔离、D29 扣留随信任授予消失、绑定不被隐性激活反证）、前端 `profiles.test.tsx` +5 例（旧后端无字段不渲染/`present=false` 不渲染/合法绑定只读卡片 + 徽标且不触发 apply·default/绑定不可用显示错误且隐藏应用指引/未信任显示扣留警告）。
> ⑦ 验证证据：`go build ./cmd/aicli/... ./internal/profile/... ./internal/api/skills/... ./internal/foldertrust/...` 退出 0；`go test ./internal/profile/ ./internal/foldertrust/ ./internal/profileinput/ -count=1` 全绿（3.3s / 1.2s / 1.6s）；`go test ./internal/api/skills/ ./internal/profile/ -count=1`（全包）ok（38.1s / 3.0s）；定向 `-run "TestRuntimeProfilesAPI(...|BindingIsNotImplicitlyActivated)$"` 8/8 PASS；`gofmt -l` 对改动文件零输出；前端 `npm run verify:lines` 0 超标（1325 文件）、`npm run lint:i18n` 0 违规（scanned=906）、`npx tsc -b` 退出 0、全量 `npm run test` **333/333 文件、2773/2773 用例通过**（360.1s）。
> ⑧ 仍后置（有测试/文档断言）：不做 `.aicli/profile` 的自动默认激活、不做多工作区自动选择/猜测、不改 `profiles.default_profile`、不让项目绑定覆盖 `--profile` / `/profile use` / 请求级 `profile`。
