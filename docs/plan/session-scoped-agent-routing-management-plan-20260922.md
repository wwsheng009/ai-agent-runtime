# 会话级 Agent 路由管理与切换方案（2026-09-22）

> 前置方案：`docs/plan/main-agent-dynamic-provider-model-switching-plan-20260921.md`（下称 **MA 方案**）。
> 本文档只做**规划**，不含实现。实现阶段需按 §9 分期提交，并按 §10 补齐测试。
>
> 状态：Draft v1（待评审）
> 范围：aicli TUI / runtime server / frontend workspace 三端一致的**会话级**路由治理能力。

---

## §0 摘要

MA 方案的动态 provider/model 切换（MA-P0 ~ MA-P4 + v4 修订）已经落地，主 Agent 可以按 step 难度自动升级/降级模型。但它当前**只有一个控制面：配置文档**（`aicli.main_agent.routing`）。这带来三个实际使用问题：

1. **粒度错误**：想试一次路由，必须改配置文件；改完对**所有** aicli 进程与 runtime server 生效（热重载后全局改变行为），无法"只在这个会话里开"。
2. **不可发现**：会话里没有命令能查看/开关路由，用户只能靠改 YAML + 重启或热重载，也没有状态栏提示"当前这个会话到底在不在路由"。
3. **三端割裂**：frontend workspace 把 `enable_routing: true` 硬编码在回合请求里（`turn-bootstrap.ts:114`），只能在全局设置页改配置文档，无法按工作区/会话管理。

本方案提出 **三层解析 + 会话级 override + 工作区偏好** 的治理模型，并交付：

- **TUI 新子命令 `/routing`**（含完整子命令与建议值表，§5、§14），支持会话级开关、main_agent / sub_agent 分节配置、保存到工作区偏好、一键 inherit 回退配置默认。
- **状态栏可见性**：底部状态行新增 routing 段，与 模型 / provider / reasoning effort 同帧刷新（§6）。
- **frontend workspace 对应能力**：工作区级设置 + 会话级 API + 底部 chip（§7）。
- **默认继承配置文件**：不配置 = 与今天完全一致（关闭态零行为变化）。

---

## §1 现状证据（代码锚点）

> 路径按仓库根书写。注意源码实际位于 `backend/` 前缀下（旧文档写作 `cmd/aicli/...`）。

| 层 | 事实 | 锚点 |
| --- | --- | --- |
| 配置模型 | `AICLIMainAgentConfig.Routing *AICLIMainAgentRoutingConfig`，挂在 `cfg.AICLI.MainAgent` | `backend/internal/agentconfig/main_agent_routing.go` |
| 配置读取 | `EffectiveMainAgentRoutingConfig(cfg)` 未配置返回 nil | `backend/internal/agentconfig/main_agent_routing.go:83` |
| 默认值回落 | `ApplyMainAgentRoutingDefaults()`；`MaxConsecutiveExpensiveSteps=0` 是**合法值**（表示不限），不可当"未设置"哨兵 | `backend/internal/agentconfig/main_agent_routing.go` |
| 校验 | `ValidateMainAgentRoutingConfig` fail-fast，且校验时会**就地**应用默认值 | 同上 |
| aicli 宿主接线 | `localChatMainAgentRoutingConfig(session)` 直接读全局配置 | `backend/cmd/aicli/commands/chat_actor_host.go:2297` |
| aicli 接线应用 | `applyLocalChatMainAgentRouting(loopConfig, session, isBaseSession)`，只接主会话 | `backend/cmd/aicli/commands/chat_actor_host.go:2311`，调用点 `:1398` |
| runtime 宿主接线 | `applyAPISessionMainAgentRouting(loopConfig, h.mainAgentRoutingConfig(), 是否主会话)` | `backend/internal/api/skills/session_runtime_support.go:3937` |
| runtime 配置快照 | `cloneAICLIRoutingConfig` 深拷贝必须保留 `aicli.main_agent.routing`（漏拷=静默失效） | `backend/internal/api/skills/handler.go:476,529,581`；测试 `main_agent_routing_wiring_test.go:26` |
| loop 侧入口 | `LoopReActConfig.MainAgentRouting *agentconfig.AICLIMainAgentRoutingConfig`（`yaml:"-"`） | `backend/internal/agent/loop.go:80` |
| **偏好持久化（复用点）** | `chatPreferenceSource` 枚举 `flag/session/config/interactive/workspace/default`；逐字段优先级 **CLI flag > restored session context > workspace preference > global `aicli.chat` > interactive > provider default** | `backend/cmd/aicli/commands/chat_preferences.go` |
| 工作区偏好文件 | `$HOME/.aicli/workspace/<sha256(cwd)[:8]hex>/chat-prefs.yaml`，内容为 `aicli.chat` 形状，**局部 YAML 写回 + 原子写**（禁止整体 marshal） | `backend/internal/agentconfig/chat_persistence_workspace.go` |
| 偏好设计决策 | D5：workspace 级持久化取代全局写入 | `docs/plan/aicli-model-preference-persistence-plan.md` |
| 会话持久化容器 | `runtimechat.Session.Metadata.Context map[string]interface{}`（随会话落盘，可承载会话级 override） | `backend/internal/chat/session.go:52,63` |
| TUI 命令分派（结构化） | `chat_command_result.go` 大分派 + 未识别命令白名单（390-403 行） | `backend/cmd/aicli/commands/chat_command_result.go` |
| TUI 命令分派（legacy） | `command.go` 平行分派 | `backend/cmd/aicli/commands/command.go:326-347` |
| 命令 spec / 帮助 | `chatSlashCommandCatalog()`、分组 `chatSlashCommandGroup{Basics,Session,Model,Context,Permission,...}` | `backend/cmd/aicli/commands/chat_slash_command_catalog.go:38` |
| 参数补全（建议值） | `CompleteSlashArgs` switch，`/model`、`/reasoning_effort` 等已有动态候选 | `backend/cmd/aicli/commands/chat_slash_argument_completion.go:53-140` |
| 状态栏段模型 | `StatusSegmentKind`：`StatusSegState/Model/Path/Usage/Balance/Mode/Meta/Provider` | `backend/cmd/aicli/ui/style/statusline.go:12-24` |
| 状态栏装配 | 段顺序 `model · provider · balance · context · cwd · project · branch · window · in · out · Fast` | `backend/cmd/aicli/commands/chat_interaction.go:2535-2546` |
| 状态栏刷新入口 | `session.Interaction.RefreshStatus("")`（`/reasoning_effort` 切换后已调用） | `backend/cmd/aicli/commands/chat_reasoning_command.go:206` |
| `/status` 面板 | 行表 `Model / Model provider / Account balance / Directory / Permissions / ... / Reasoning output` | `backend/cmd/aicli/commands/chat_status.go:93-105` |
| web 状态栏端点 | `GET /web/api/statusbar`，注释明确"provider / model / reasoning_effort 由 Web 底部 cfg-bar 展示，此处不重复" | `backend/cmd/aicli/commands/web_statusbar.go:1-36` |
| 路由事件 | `EventMainAgentRouteApplied = "main_agent.route_applied"` 等 6 个；`model_changed` 用于前端底部栏同步 | `backend/internal/events/main_agent_routing.go`、`internal/events/contract.go:105` |
| frontend 请求契约 | `AgentChatRequest.enable_routing?: boolean` | `frontend/src/types/runtime/chat.ts:16` |
| frontend 硬编码 | `enable_routing: true` 写死在回合 bootstrap | `frontend/src/hooks/workspace/agent-chat-turn/turn-bootstrap.ts:114` |
| frontend 全局编辑入口 | 设置页 agent-routing 域（编辑 runtime 配置文档 = 全局） | `frontend/src/components/workspace/settings/backend-config-settings-page/domains/agent-routing.ts`、`.../runtime-agent-routing-domain-editor.tsx` |
| frontend 会话设置入口 | 聊天设置页（provider/model/maxSteps） | `frontend/src/components/workspace/settings/chat-settings-page.tsx` |
| frontend 模型面板 | composer 的 provider/model/reasoning_effort 选择器 | `frontend/src/components/workspace/composer-model-panel.tsx` |

---

## §2 目标、非目标与不变量

### §2.1 目标

| 编号 | 目标 |
| --- | --- |
| G1 | 会话级开关路由（开/关/继承），不触碰配置文件 |
| G2 | TUI 子命令管理：main_agent / sub_agent 分节、难度档位、成本护栏、保存/清除工作区偏好 |
| G3 | 默认继承配置；显式覆盖才改变行为；覆盖可保存为**工作区偏好**（像 provider/model 一样） |
| G4 | 状态栏底部与 `/status` 可见 routing 状态与来源（session / workspace / config） |
| G5 | frontend workspace 具备同等能力（工作区设置 + 会话级 API + 底部 chip） |
| G6 | 三端同一份 wire 语义与同一套解析优先级 |

### §2.2 非目标

- 不改路由算法本身（难度评估、升级重试、成本护栏判定逻辑）。
- 不改 `aicli.subagents.routing` 的既有语义（本方案只做**覆盖**入口，不改子 Agent 路由实现）。
- 不新增 provider/model 持久化字段（见 INV-A2）。
- 不做跨工作区的"全局用户偏好"新层级（仍只有：会话 > 工作区 > 全局配置）。

### §2.3 不变量（硬约束）

| 编号 | 不变量 | 来源 |
| --- | --- | --- |
| INV-A1 | 会话级开关是**用户意图**，与 `/model` 同层级，优先级高于配置文件默认值，但**不改写**配置文件 | MA 方案 §5.6 INV-3 扩展 |
| INV-A2 | route 偏移（step 内自动升降级）**不得**写回会话持久化字段，也不得改写用户意图 | MA 方案 INV-1..4；回归锚点 `backend/cmd/aicli/commands/chat_actor_host_test.go:1514` |
| INV-A3 | 关闭态零行为变化：未配置 override 时，`loopConfig.MainAgentRouting` 与今天逐位相同 | MA 方案 §11；测试锚点 `main_agent_routing_wiring_test.go:67` |
| INV-A4 | 主会话 override 不改变子 Agent / team 路由（配置隔离）；反之亦然 | MA 方案 §6.3 |
| INV-A5 | 不向 LLM 暴露 provider/model/路由状态（前缀冻结、不注入策略状态） | MA 方案 §5.6 INV-1 |
| INV-A6 | 持久化分层单向：会话 override 只影响本会话；工作区偏好只影响本工作目录；两者都不写全局配置 | 本文档 |

---


## §3 数据模型

### §3.1 三层解析模型

```
effective_routing(session) =
      session_override            (会话级：本会话显式设置，最高优先)
   ?? workspace_preference        (工作区级：本工作目录偏好，如 ~/.aicli/workspace/<hash>/chat-prefs.yaml)
   ?? global_config                (全局：aicli.main_agent.routing / aicli.subagents.routing)
   ?? builtin_default              (内置默认：enabled=false，即今日行为)
```

解析**逐字段**进行（不是整块覆盖）：例如会话只设了 `enabled=true`，其余字段（levels / guard / dwell）仍从工作区偏好或全局配置继承。这与 `chat_preferences.go` 既有的 per-field 优先级机制同构，因此可以复用同一套 `chatPreferenceSource` 语义，并新增来源值：

| source 值 | 含义 | 状态栏/状态面板展示 |
| --- | --- | --- |
| `session` | 本会话显式覆盖 | `session` |
| `workspace` | 本工作区偏好 | `ws` |
| `config` | 全局配置文档 | `cfg` |
| `default` | 内置默认（关闭） | 不展示段 |

### §3.2 新增结构：`AICLISessionRoutingOverride`

设计要点：**全部字段用指针表达"未设置 = 继承"**。不能复用 0 值哨兵，因为 `MaxConsecutiveExpensiveSteps=0` 在 MA 方案里是合法值（表示不限）。

```go
// 建议位置：backend/internal/agentconfig/session_routing_override.go
type AICLISessionRoutingOverride struct {
    Version   int    `json:"version,omitempty" yaml:"version,omitempty"`     // 前向兼容，当前为 1
    Enabled   *bool  `json:"enabled,omitempty" yaml:"enabled,omitempty"`     // 会话级总开关（main_agent）
    MainAgent *AICLISessionMainAgentRoutingOverride `json:"main_agent,omitempty" yaml:"main_agent,omitempty"`
    SubAgent  *AICLISessionSubAgentRoutingOverride  `json:"sub_agent,omitempty" yaml:"sub_agent,omitempty"`
    UpdatedAt string `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
    UpdatedBy string `json:"updated_by,omitempty" yaml:"updated_by,omitempty"` // aicli-tui | web | api
    Source    string `json:"source,omitempty" yaml:"source,omitempty"`         // session | workspace
}

type AICLISessionMainAgentRoutingOverride struct {
    Enabled                      *bool     `json:"enabled,omitempty"`
    Levels                       *[]string `json:"levels,omitempty"`
    AllowExpert                  *bool     `json:"allow_expert,omitempty"`
    DefaultDifficulty            *string   `json:"default_difficulty,omitempty"`
    AllowEscalationRetry         *bool     `json:"allow_escalation_retry,omitempty"`
    CostGuardMode                *string   `json:"cost_guard_mode,omitempty"`                  // soft | hard | off
    MaxConsecutiveExpensiveSteps *int      `json:"max_consecutive_expensive_steps,omitempty"`  // 0 = 不限（合法值）
    ExpensiveLevels              *[]string `json:"expensive_levels,omitempty"`
    MaxInvalidReportsPerTurn     *int      `json:"max_invalid_reports_per_turn,omitempty"`
    DowngradeConfirmSteps        *int      `json:"downgrade_confirm_steps,omitempty"`
    MinDwellSteps                *int      `json:"min_dwell_steps,omitempty"`
    HealthRespectProviderHealth  *bool     `json:"health_respect_provider_health,omitempty"`
}

type AICLISessionSubAgentRoutingOverride struct {
    Enabled  *bool                                        `json:"enabled,omitempty"`
    Profiles map[string]AICLISubagentRouteProfileOverride `json:"profiles,omitempty"`
}

type AICLISubagentRouteProfileOverride struct {
    Levels            *[]string `json:"levels,omitempty"`
    DefaultDifficulty *string   `json:"default_difficulty,omitempty"`
}
```

约束：

- 该结构**只承载用户意图**，禁止写入"当前 step 实际使用的 provider/model"（INV-A2）。
- 字段集合是 `AICLIMainAgentRoutingConfig` 的**可覆盖子集**；`HealthGate.LatchScope`、`OnChainExhausted`、`HonorMinDwell` 在 MA 方案里是强制固定值，**不开放覆盖**（出现在会话 override 中应被拒绝并给出 warning）。

### §3.3 存储位置

| 层级 | 位置 | 生命周期 | 说明 |
| --- | --- | --- | --- |
| 会话级 | `runtimechat.Session.Metadata.Context["aicli_routing_override"]` | 随会话文件持久化，`/resume`、`--session` 恢复 | 复用既有 `Context map[string]interface{}`，**不新增顶层字段**，避免旧会话文件反序列化兼容问题 |
| 工作区级 | `$HOME/.aicli/workspace/<hash>/chat-prefs.yaml` 新增 `routing` 子树 | 长期，跨会话 | 需在 `AICLIChatConfig` 增加 `Routing *AICLIChatRoutingPreference`；写回沿用**局部 YAML 写回 + 原子写**（D5 决策） |
| 全局 | `aicli.main_agent.routing`（配置文件） | 长期 | 语义不变，仅作为默认来源；本方案**不提供**从 TUI 直接改全局配置的能力（避免复现用户痛点） |

会话级读写的建议 API（`backend/internal/chat/session.go` 附近新增，或 `chat_session.go` 包装）：

```go
func ReadSessionRoutingOverride(s *runtimechat.Session) (*agentconfig.AICLISessionRoutingOverride, error)
func WriteSessionRoutingOverride(s *runtimechat.Session, ov *agentconfig.AICLISessionRoutingOverride) error // 变更时合并写
func ClearSessionRoutingOverride(s *runtimechat.Session) error                                            // /routing inherit
```

要点：

- 反序列化必须容忍未知字段与 `version` 不匹配（未来版本降级只 warning）。
- 写入必须**幂等**：内容未变化时不写会话文件（避免每次 `/routing status` 都刷 `UpdatedAt`，参照 `PreserveUpdatedAt` 的既有取舍）。

---

## §4 解析与接线（后端）

### §4.1 统一解析器

```go
// 建议位置：backend/internal/agentconfig/session_routing_resolve.go
type RoutingResolution struct {
    Effective *AICLIMainAgentRoutingConfig
    Sources   map[string]string // 字段 → session|workspace|config|default
    Warnings  []string
}

func ResolveSessionMainAgentRouting(
    cfg *Config,
    sessionOverride *AICLISessionRoutingOverride,
    workspacePref *AICLIChatRoutingPreference,
) RoutingResolution
```

行为定义：

1. 从 `EffectiveMainAgentRoutingConfig(cfg)` 深拷贝基线（nil → 零值基线）。
2. 逐字段应用 workspace 偏好。
3. 逐字段应用 session override（仅非 nil 字段）。
4. `ApplyMainAgentRoutingDefaults()` 补默认值。
5. `ValidateMainAgentRoutingConfig()` 校验；**会话/工作区层的非法值不阻断会话**：丢弃该字段 + 追加 `Warnings` + 状态栏/`/routing status` 显示 warning 徽标（全局配置层仍保持 fail-fast，维持既有启动语义）。
6. `Enabled=false` 时返回 `nil`（与今日"未启用"逐位一致，INV-A3）。

### §4.2 aicli 宿主接线

| 位置 | 现状 | 改为 |
| --- | --- | --- |
| `chat_actor_host.go:2297` `localChatMainAgentRoutingConfig(session)` | 读全局配置 | 调 `ResolveSessionMainAgentRouting(session.Config, sessionRoutingOverride(session), workspaceRoutingPref(session.Config))`，并把 `Warnings` 记录到会话（供状态栏/`/routing status` 展示） |
| `chat_actor_host.go:2311` `applyLocalChatMainAgentRouting` | 只接主会话 | **不变**（保持 INV-A4） |
| `chat_actor_host.go:1398` 调用点 | 每次 turn 构建 loopConfig 时调用 | **不变**（保证下一 turn 生效，见 §4.4） |

`ChatSession` 新增字段（内存态，不直接序列化）：

```go
routingOverride  *agentconfig.AICLISessionRoutingOverride // 会话级覆盖（与 RuntimeSession.Metadata.Context 同步）
routingResolution *agentconfig.RoutingResolution          // 最近一次解析结果（供状态栏/命令展示）
```

### §4.3 runtime server 接线

| 位置 | 现状 | 改为 |
| --- | --- | --- |
| `handler.go:476` `mainAgentRoutingConfig()` | 返回全局配置 | 保留（作为基线）；新增 `mainAgentRoutingConfigForSession(sessionID)`，叠加会话 override |
| `session_runtime_support.go:3937` | `applyAPISessionMainAgentRouting(loopConfig, h.mainAgentRoutingConfig(), isMain)` | `applyAPISessionMainAgentRouting(loopConfig, h.mainAgentRoutingConfigForSession(sid), isMain)` |
| `handler.go:529/581` 快照克隆 | 深拷贝全局 | **必须同时深拷贝 override**（否则会话级配置在热重载后串台）；补一条与 `TestCloneAICLIRoutingConfigKeepsMainAgentRouting` 同风格的回归测试 |

会话级 override 在 runtime server 的存放：

- 首选：`Session.Metadata.Context` 同一 key（跨端一致，frontend 与 aicli 共享同一份会话数据）。
- 请求级临时覆盖（不进存储）：`AgentChatRequest.routing`（§7.3b），仅对本次 turn 生效。

### §4.4 生效时机与并发

- override 变更**只在 turn 边界生效**：`loopConfig` 每 turn 重建，天然满足；禁止运行中修改 `loopConfig.MainAgentRouting`（turn 是治理边界，沿用 MA 方案）。
- 解析结果按 turn 做**深拷贝快照**，避免运行期与热重载写入竞争。
- 同一会话并发 turn（P4 刷新续传场景）：新 turn 使用新 override，旧 turn 保持启动时的快照（不回溯）。

---

## §5 TUI 子命令设计（核心交付）

### §5.1 命令形态

新增 `/routing`（别名 `/route`），归属分组 `chatSlashCommandGroupModel`（与 provider/model/reasoning 同类，便于 `/help` 归类）。

```
/routing                                   # = /routing status
/routing status                            # 三层来源 + 生效值 + warnings
/routing on|off|toggle                     # 会话级总开关（= main_agent.enabled）
/routing inherit [all|main|sub|<key>]      # 清除会话级覆盖，回落工作区/配置
/routing main [status|<key> <value>]       # main_agent 分节
/routing sub  [status|on|off|profile <name> <key> <value>]
/routing levels easy,normal,hard           # 难度档位（main_agent.levels 简写）
/routing guard soft|hard|off               # 成本护栏模式简写
/routing scope session|workspace|global    # 查看/切换"写入目标"
/routing save [workspace]                  # 把当前会话生效值保存为工作区偏好
/routing reset [workspace]                 # 清除工作区偏好（回落配置）
/routing explain                           # 逐字段说明"为什么是这个值"（三层差异）
/routing doctor                            # 配置体检（与 /debug routing 合并视图）
```

语义表：

| 子命令 | 作用域 | 持久化 | 生效时机 | 输出 |
| --- | --- | --- | --- | --- |
| `status` | 会话 | 无 | 立即（只读） | 多行摘要 + 来源标签 + warning |
| `on/off/toggle` | 会话 | 会话 metadata | 下一 turn | 原子结果单元（`commandTextResult`） |
| `inherit` | 会话 | 清除会话 key | 下一 turn | 显示回落后的来源 |
| `main <key> <value>` | 会话 | 会话 metadata | 下一 turn | 键/旧值/新值/来源 |
| `sub ...` | 会话 | 会话 metadata | 下一 turn | 同上（子 Agent 独立节） |
| `levels` / `guard` | 会话 | 会话 metadata | 下一 turn | 同上（`main` 的简写糖） |
| `scope` | 会话 | 会话 metadata（记录默认写入目标） | 立即 | 当前写入目标；`global` 只读（提示改用配置文件） |
| `save workspace` | 工作区 | `chat-prefs.yaml` → `routing` | 下一 turn（且新会话默认继承） | 保存路径 + 摘要 |
| `reset workspace` | 工作区 | 删除 `routing` 子树 | 下一 turn | 清除结果 |
| `explain` / `doctor` | 会话 | 无 | 立即（只读） | 三层差异表 / 体检项 |

> `scope` 的存在意义：让"临时开一下"（session）与"这个项目以后都这样"（workspace）显式分开。默认 `session`，避免用户误改工作区偏好。

### §5.2 子命令建议值（补全候选）

用户明确要求"可以给出子命令的建议值"，因此**第一等子命令、第二等键名、第三等取值**都要进补全。实现落在 `chat_slash_argument_completion.go` 的 `CompleteSlashArgs` 新增 `case "/routing", "/route":`，候选动态生成（来源标注在 `Summary` 里）。

一级子命令候选：

| 建议值 | Summary（补全列表显示） |
| --- | --- |
| `status` | 查看三层来源与生效值 |
| `on` | 会话级开启（仅本会话） |
| `off` | 会话级关闭（仅本会话） |
| `toggle` | 切换会话级开关 |
| `inherit` | 清除会话覆盖，回落工作区/配置 |
| `main` | main_agent 分节（难度/护栏/重试） |
| `sub` | sub_agent 分节（独立于 main） |
| `levels` | 设置难度档位（简写） |
| `guard` | 设置成本护栏（简写） |
| `scope` | 选择写入目标 session/workspace/global |
| `save` | 保存为工作区偏好 |
| `reset` | 清除工作区偏好 |
| `explain` | 逐字段解释取值来源 |
| `doctor` | 配置体检 |

`/routing main` 键名候选（值类型 → 建议值）：

| 键 | 值建议 | 说明 |
| --- | --- | --- |
| `enabled` | `on` / `off` / `inherit` | 等价总开关 |
| `levels` | 动态：`easy,normal,hard`（按 provider/model 声明过滤） | 逗号分隔 |
| `allow_expert` | `on` / `off` / `inherit` | 是否允许 expert 档 |
| `default_difficulty` | 动态：难度档位（默认 `normal`） | |
| `allow_escalation_retry` | `on` / `off` / `inherit` | |
| `cost_guard_mode` | `soft` / `hard` / `off` / `inherit` | |
| `max_consecutive_expensive_steps` | `0`(不限) / `3` / `6` / `12` / `inherit` | 0 是合法值 |
| `expensive_levels` | 动态：难度档位多选 | 逗号分隔 |
| `max_invalid_reports_per_turn` | `1` / `3` / `5` / `inherit` | 默认 3 |
| `downgrade_confirm_steps` | `1` / `2` / `3` / `5` / `inherit` | 默认 3 |
| `min_dwell_steps` | `1` / `2` / `3` / `inherit` | 默认 2 |
| `health.respect_provider_health` | `on` / `off` / `inherit` | 其余 health 子键**不开放** |

`/routing sub` 候选：

| 建议值 | 说明 |
| --- | --- |
| `status` | 子 Agent 路由摘要（来自 `aicli.subagents.routing`） |
| `on` / `off` / `inherit` | 子 Agent 路由开关覆盖 |
| `profile <name> ...` | profile 名候选来自配置 `profiles` 键；键候选 `levels` / `default_difficulty` |

候选生成规则（重要，避免与真实能力不一致）：

- 难度档位候选**不是硬编码**，取自当前 provider/model 声明的 reasoning/difficulty 能力（与 `/reasoning_effort` 的 `reasoningEffortCatalogForModel` 同源思路）。
- 当 provider 不支持某档位时，候选仍展示但 Summary 标注 `(当前模型不支持)`，由校验层拒绝并给出可执行提示。
- 候选必须包含 `inherit`，让"回退继承"在所有键上一致可用。

### §5.3 帮助与分派接线

| 需要改的位置 | 改动 |
| --- | --- |
| `chat_slash_command_catalog.go:38` | 新增 `/routing` spec（含 `Args` 全表，供 `/help` 与补全共用） |
| `chat_slash_argument_completion.go:53` | 新增 `/routing` case（§5.2） |
| `chat_command_result.go:390-403` | 未识别命令白名单加入 `/routing`（漏加会导致命令被当作普通消息发给模型） |
| `chat_command_result.go` 分派段 | 新增 `executeStructuredRoutingCommand(session, command)`（结构化渲染，原子单元） |
| `command.go:326-347` | legacy 平行分派（兼容控制台模式） |
| `chat_command.go:13-30` `ChatCommandLongHelp` | 斜杠命令清单加入 `/routing` 一行 |
| 新文件 | `chat_routing_command.go`（解析）、`chat_routing_apply.go`（应用+持久化）、`chat_routing_render.go`（status/explain 渲染） |

命令解析约定：

- 值统一大小写不敏感；`on/off/true/false/1/0/yes/no` 归一化（复用 `/reasoning` 的既有约定）。
- 未识别键 → 错误单元 + 该键的候选建议（例如 `未知键 cost_guard，可选: soft|hard|off|inherit`）。
- 所有变更类子命令返回**原子结果单元**（`commandTextResult`），与 `/clear`、`/yolo`、`/reasoning_effort` 的迁移风格一致。

### §5.4 会话级状态展示（`/routing status` 输出契约）

```
Routing: ON (source=session)  guard=hard  levels=easy,normal,hard
  enabled                  session   on            (config: off)
  cost_guard_mode          session   hard          (config: soft)
  min_dwell_steps          workspace 2             (config: 2)
  max_consecutive_expensive_steps inherit  6       (config: 6)
  sub_agent.enabled        config    on
Warnings: 会话级 levels 含当前模型不支持的档位 "expert"，已忽略该档位
Hint: /routing save workspace 可把本会话设置保存为工作区偏好；/routing inherit 恢复继承
```

要点：**必须逐字段显示 source 与 config 基线值**——用户当前的痛点正是"不知道哪一层生效、改了会不会影响别人"。

---

## §6 状态栏与可见性（TUI）

### §6.1 新增状态段

在 `backend/cmd/aicli/ui/style/statusline.go:12-24` 的 `StatusSegmentKind` 追加：

```go
// StatusSegRouting 表示会话级路由治理状态（会话/工作区/配置来源 + 护栏模式）。
// 关闭态不渲染任何段，保证 INV-A3 零行为变化。
StatusSegRouting
```

插入顺序约束：既有顺序 `model → provider → balance → …` 是**被测试锚定的**（`chat_balance_refresh_test.go:397` 与 `chatAccountBalanceStatusInsertIndex` 依赖 `StatusSegProvider` 位置）。因此 routing 段插入 **provider 之后、balance 之前**，且不得改动既有段索引算法。

### §6.2 段文本规则

| 生效状态 | 段文本 | Role | Priority（建议） |
| --- | --- | --- | --- |
| 未启用（default/config 且 off） | **不渲染** | — | — |
| 会话级开启 | `Route session:auto` | `RoleInfo` | 30 |
| 工作区开启 | `Route ws:auto` | `RoleInfo` | 30 |
| 配置开启 | `Route cfg:auto` | `RoleTextSecondary` | 25 |
| 护栏 hard | `Route session:hard` | `RoleWarning` | 35 |
| 护栏 soft 且已触发 | `Route session:soft!` | `RoleWarning` | 40 |
| 有 warning（非法值被忽略） | 后缀 `⚠` | `RoleWarning` | 45 |

- `auto` 表示按 step 难度自动选档（区别于显式锁定单档）。
- 段宽度窄屏优先折叠：Priority 低于 model/provider，保证关键信息（模型）不被挤掉。

### §6.3 装配与刷新

| 位置 | 改动 |
| --- | --- |
| `chat_interaction.go:2535-2546` | 在 provider 段之后插入 `chatSurfaceRoutingStatusSegment(session)`（新增函数，读 `session.routingResolution`，nil 时返回空段） |
| `chat_interaction.go` | 新增 `chatSurfaceRoutingStatusSegment` + `presentChatStatusSegment` 复用 |
| `/routing` 变更路径 | 变更后调用 `session.Interaction.RefreshStatus("")`（与 `/reasoning_effort` 一致，`chat_reasoning_command.go:206`） |
| `/model`、`/provider`、`/reasoning_effort` 切换 | **必须同帧刷新三件套 + routing**：模型切换走 `publishChatModelSelectionChanged(session)`（`chat_reasoning_command.go:211` 已有先例），本方案要求 routing 变更同样发布该事件（前端底部栏据此同步），并在 TUI 侧统一走一次 `RefreshStatus` |

**模型切换时的状态栏一致性（用户诉求 3）**：底部行必须同时反映 `model · provider · reasoning_effort · routing` 四项，且四项来源一致（会话 override / 工作区偏好 / 配置）。实现上收敛为**单一刷新函数**：

```go
// 建议：chat_interaction.go 或 chat_model_switch.go
func refreshChatSurfaceIdentityStatus(session *ChatSession) // model/provider/effort/routing 一次装配
```

`/model`、`/provider`、`/reasoning_effort`、`/routing` 四条命令路径都调用它，禁止各自拼装（避免漂移）。

### §6.4 `/status` 面板与调试视图

- `chat_status.go:93-105` 行表新增 `{Label: "Routing", Value: buildChatStatusRoutingValue(session)}`，输出形如 `ON (session) · guard=hard · levels=easy,normal,hard`；关闭时输出 `off (inherited from config)`。
- `/debug routing`（既有，见 `chat_slash_command_catalog.go:115`）扩展为三层来源视图；`/routing doctor` 复用同一渲染，避免两份实现。

### §6.5 web 端点

`web_statusbar.go` 增加段：`chatWebStatusBarSegRouting = "routing"`，字段 `routing`（文本）+ `routing_source`（session|workspace|config）。文档注释中"provider / model / reasoning_effort 由 Web 底部 cfg-bar 展示"的既有分工**不变**；routing 由该端点提供，前端据此渲染 chip。

---

## §7 frontend / workspace 方案

### §7.1 现状与缺口

| 现状 | 锚点 | 缺口 |
| --- | --- | --- |
| 回合请求硬编码 `enable_routing: true` | `frontend/src/hooks/workspace/agent-chat-turn/turn-bootstrap.ts:114` | 前端**无法**按会话/工作区关闭路由 |
| 请求契约有 `enable_routing?: boolean` | `frontend/src/types/runtime/chat.ts:16` | 只有开关，无难度/护栏/来源语义 |
| 全局编辑入口（改 runtime 配置文档） | `settings/backend-config-settings-page/domains/agent-routing.ts`、`runtime-agent-routing-domain-editor.tsx` | 与 aicli 同样"改一次影响所有会话" |
| 聊天设置页只有 provider/model/maxSteps | `settings/chat-settings-page.tsx` | 没有 routing 分节 |
| composer 有 provider/model/reasoning_effort 选择器 | `composer-model-panel.tsx` | 没有 routing 快捷开关 |
| 路由预览 API 已有 | `lib/runtime-api` → `previewRuntimeAgentRoute`、`updateRuntimeAgentRoutingConfig`、`updateRuntimeTeamRoutingInheritance` | 均为**配置文档级**，需新增会话级 API |

### §7.2 目标分层（与后端一致）

```
全局默认（配置文档，只读继承）
   ↑ 被覆盖
工作区偏好（浏览器本地设置 + 后端工作区偏好）
   ↑ 被覆盖
会话级 override（会话 metadata / 请求级临时）
```

### §7.3 需要新增/修改的前端能力

**(a) 工作区级设置**

- `frontend/src/core/settings`（`local.ts` 等）在 `chat` 段下新增 `routing`：`{ enabled?: boolean|null, levels?: string[], costGuardMode?: "soft"|"hard"|"off"|null, defaultDifficulty?: string|null, maxConsecutiveExpensiveSteps?: number|null }`。
- 沿用既有 `useAppSettings().updateSection` 的读写与持久化模式，不引入第二套设置机制。
- 语义与后端 `AICLIChatRoutingPreference`（`chat-prefs.yaml` 的 `routing` 子树）**字段名逐字对齐**（§8 契约表）。

**(b) 回合请求**

```ts
// turn-bootstrap.ts
enable_routing: resolveRoutingEnabled({ settings, sessionRouting, globalDefault }),
...(sessionRouting ? { routing: sessionRouting } : {}),   // 新增：请求级临时覆盖（不落库）
```

- `AgentChatRequest` 新增 `routing?: AgentRoutingOverridePayload`（形状 = §3.2 的 snake_case 投影）。
- `enable_routing` 保留（向后兼容），但**由解析结果决定**，不再硬编码 `true`。
- 优先级：请求级 `routing` > 会话 override > 工作区偏好 > 全局配置（与后端 §3.1 一致；请求级仅本 turn 有效）。

**(c) 会话级 API（runtime server）**

| 方法 | 路径 | Body | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/runtime/sessions/{id}/routing` | — | 返回 `{ override, effective, sources, warnings }` |
| `PUT` | `/api/runtime/sessions/{id}/routing` | `AICLISessionRoutingOverride` | 写入会话 metadata，返回同上 |
| `DELETE` | `/api/runtime/sessions/{id}/routing` | — | 清除会话 override（= `/routing inherit`） |

- 复用 runtime 既有 session 路由与鉴权中间件；写入必须走与 aicli 相同的存储 key（§3.3），保证两端互操作。
- 变更后发布 SSE 事件（见 (e)）。

**(d) 设置面板与 composer**

- `chat-settings-page.tsx` 新增"路由（本工作区）"卡片：总开关（继承/开/关三态）、难度档位多选、护栏模式、`max_consecutive_expensive_steps`、`default_difficulty`；底部两个动作按钮：「保存为工作区偏好」「恢复继承」。
  - 需要「本会话临时生效」入口时，改为在 composer 面板操作（下一项），避免设置页语义混淆。
- `composer-model-panel.tsx` 在 provider/model/reasoning_effort 同排新增 routing 快捷开关（三态：继承 / 开 / 关）+ 当前来源徽标（`session`/`ws`/`cfg`），与 TUI 的 `/routing on|off|inherit` 一一对应。
- 全局设置页（`backend-config-settings-page` 的 agent-routing 域）保持"全局默认"定位，UI 增加提示文案：**「本页为全局默认，会话与工作区可覆盖」**，并在编辑器顶部展示当前生效来源。

**(e) 事件与状态同步**

| 事件 | 触发 | 消费方 |
| --- | --- | --- |
| `session.routing_changed`（新增） | PUT/DELETE 会话 routing，或 TUI `/routing` 变更（同一会话） | 前端设置面板、composer 徽标、底部 chip |
| `main_agent.route_applied`（既有） | 路由实际切换档位 | 既有观测链路（不变） |
| `model_changed`（既有） | provider/model/effort 切换 | 前端底部栏（本方案要求 routing 变更复用同一同步路径） |

事件需登记到 `backend/internal/events/contract.go`（ChannelSessionStore）与 `frontend/src/types/runtime/event-contract.ts`（与既有 6 个 routing 事件同处，`event-contract.ts:66-71` 附近）。

**(f) 底部状态栏 chip**

- 前端底部条（与 cfg-bar 同区）新增 routing chip，文本与 TUI 段**同规则**：关闭不显示；`Route session:auto` / `Route ws:hard` / `Route cfg:auto` / warning 徽标。
- 数据源：`GET /api/runtime/sessions/{id}/routing`（首屏）+ `session.routing_changed`、`model_changed`（增量）。

### §7.4 三端契约表（单一来源，禁止二套命名）

| 语义 | 配置文档（YAML） | 会话/请求 wire（JSON） | 前端 TS | TUI 子命令 |
| --- | --- | --- | --- | --- |
| 总开关 | `enabled` | `enabled` | `enabled` | `/routing on\|off` |
| 难度档位 | `levels` | `levels` | `levels` | `/routing levels` |
| 默认难度 | `default_difficulty` | `default_difficulty` | `defaultDifficulty`（本地设置）/ `default_difficulty`（wire） | `/routing main default_difficulty` |
| 允许 expert | `allow_expert` | `allow_expert` | `allowExpert`（本地）/ `allow_expert`（wire） | `/routing main allow_expert` |
| 成本护栏 | `cost_guard_mode` | `cost_guard_mode` | `costGuardMode`（本地）/ `cost_guard_mode`（wire） | `/routing guard` |
| 连续昂贵上限 | `max_consecutive_expensive_steps` | 同名 | `maxConsecutiveExpensiveSteps`（本地）/ 同名（wire） | `/routing main max_consecutive_expensive_steps` |
| 子 Agent 开关 | `aicli.subagents.routing.enabled` | `sub_agent.enabled` | `subAgent.enabled` | `/routing sub on\|off` |

> 约定：**wire 一律 snake_case**（与 `AgentChatRequest` 既有字段一致），前端本地设置用 camelCase 并在请求边界转换；转换函数必须单点实现（`lib/runtime-api` 或 settings 适配层），禁止散落。

---

## §8 变更清单（文件级）

### §8.1 后端 · agentconfig

| 文件 | 改动 |
| --- | --- |
| `backend/internal/agentconfig/session_routing_override.go`（新） | §3.2 结构 + 读写/校验/合并 |
| `backend/internal/agentconfig/session_routing_resolve.go`（新） | §4.1 三层解析器 + sources/warnings |
| `backend/internal/agentconfig/chat_persistence_workspace.go` | `routing` 子树读写（局部 YAML 写回 + 原子写） |
| `backend/internal/agentconfig/chat_config.go`（或同类） | `AICLIChatConfig.Routing *AICLIChatRoutingPreference` |
| `backend/internal/agentconfig/main_agent_routing.go` | 复用既有 `ApplyMainAgentRoutingDefaults` / `ValidateMainAgentRoutingConfig`；不开放 health 子键覆盖 |

### §8.2 后端 · aicli

| 文件 | 改动 |
| --- | --- |
| `backend/cmd/aicli/commands/chat_routing_command.go`（新） | 命令解析（§5.1） |
| `backend/cmd/aicli/commands/chat_routing_apply.go`（新） | 应用 + 会话/工作区持久化 + 事件发布 |
| `backend/cmd/aicli/commands/chat_routing_render.go`（新） | `status` / `explain` / `doctor` 渲染 |
| `backend/cmd/aicli/commands/chat_command_result.go` | 分派 + 白名单（390-403） |
| `backend/cmd/aicli/commands/command.go` | legacy 分派 |
| `backend/cmd/aicli/commands/chat_slash_command_catalog.go` | `/routing` spec |
| `backend/cmd/aicli/commands/chat_slash_argument_completion.go` | 建议值候选（§5.2） |
| `backend/cmd/aicli/commands/chat_command.go` | 长帮助 |
| `backend/cmd/aicli/commands/chat_actor_host.go:2297` | 读解析器替代全局读取 |
| `backend/cmd/aicli/commands/chat_interaction.go` | routing 状态段 + 统一刷新函数（§6.3） |
| `backend/cmd/aicli/ui/style/statusline.go` | `StatusSegRouting` |
| `backend/cmd/aicli/commands/chat_status.go` | `/status` 面板 Routing 行 |
| `backend/cmd/aicli/commands/web_statusbar.go` | routing 段 |
| `backend/cmd/aicli/commands/chat_preferences.go` | `chatPreferenceSource` 增加 `session` 来源与展示 |

### §8.3 后端 · runtime server / 事件

| 文件 | 改动 |
| --- | --- |
| `backend/internal/api/skills/handler.go` | `mainAgentRoutingConfigForSession`；快照克隆覆盖 override |
| `backend/internal/api/skills/session_runtime_support.go:3937` | 接线到会话级解析 |
| `backend/internal/api/runtime/*`（session 路由文件） | `GET/PUT/DELETE /api/runtime/sessions/{id}/routing` |
| `backend/internal/events/*` | `session.routing_changed` 事件类型 + 契约登记 |
| `backend/internal/runtimeobserve/known_types.go:205` 附近 | 新事件类型登记 |

### §8.4 frontend

| 文件 | 改动 |
| --- | --- |
| `frontend/src/types/runtime/chat.ts` | `AgentChatRequest.routing?` |
| `frontend/src/types/runtime/event-contract.ts` | `session.routing_changed` |
| `frontend/src/hooks/workspace/agent-chat-turn/turn-bootstrap.ts:114` | 解析 `enable_routing` + 注入 `routing` |
| `frontend/src/core/settings/*` | `chat.routing` 段 |
| `frontend/src/lib/runtime-api.ts` | 会话级 routing API + snake/camel 转换单点 |
| `frontend/src/components/workspace/settings/chat-settings-page.tsx` | 工作区路由卡片 |
| `frontend/src/components/workspace/composer-model-panel.tsx` | routing 三态开关 + 来源徽标 |
| `frontend/src/components/workspace/settings/backend-config-settings-page/domains/agent-routing.ts` | 增加"全局默认"提示与生效来源展示 |
| 底部状态栏组件（cfg-bar 同区） | routing chip |

---

## §9 实施阶段

| 阶段 | 内容 | 依赖 | 出口标准 |
| --- | --- | --- | --- |
| **S1（P0）** | 解析器 + 会话级存储 + `/routing status\|on\|off\|toggle\|inherit` + 状态栏段 + `/status` 行 | 无 | 会话内开关只影响本会话；关闭态零变化；状态栏四项一致刷新 |
| **S2（P0）** | `/routing main\|sub\|levels\|guard\|scope` + 全量键 + 补全候选 + `save/reset workspace` | S1 | 工作区偏好可保存/清除；新会话继承工作区偏好 |
| **S3（P1）** | runtime server 会话级 API + `session.routing_changed` + frontend 工作区设置 + composer 开关 + 底部 chip | S1/S2 | 三端同一会话互操作（TUI 改 → Web 立即看到） |
| **S4（P1）** | `/routing explain` + `/routing doctor`（合并 `/debug routing`） | S1 | 三层差异表可解释任意字段取值来源 |
| **S5（P2）** | 子 Agent / team 显式 scope 覆盖与继承策略可视化 | S2 | `--scope subagent` 显式生效，默认不继承（INV-A4） |

每阶段独立可发布；S1 单独上线即可解决用户当前最主要痛点（改配置影响全局）。

---

## §10 测试计划

编号沿用 MA 方案风格（U=单元，I=集成，REG=回归护栏）。

### §10.1 单元（U）

| 编号 | 用例 | 断言要点 |
| --- | --- | --- |
| U1 | 三层优先级 | `session > workspace > config > default`，逐字段独立解析 |
| U2 | 指针"未设置"语义 | nil 字段继承上层；显式 `false`/`0` 覆盖上层（不得被当作未设置） |
| U3 | `MaxConsecutiveExpensiveSteps=0` | 显式 0 必须解析为"不限"，不得回落默认 6 |
| U4 | 非法会话值降级 | 非法值 → 丢弃该字段 + warning，**不**阻断会话；全局配置层仍 fail-fast |
| U5 | health 子键拒绝 | `health.latch_scope` 等出现在 override → 拒绝 + warning |
| U6 | 关闭态零行为变化 | `enabled=false`（任意层）→ `ResolveSessionMainAgentRouting` 返回 nil |
| U7 | 子 Agent 隔离 | 主会话 override 不改变 `applyAPISessionMainAgentRouting(child)` 结果（对齐 `main_agent_routing_wiring_test.go:67`） |
| U8 | 状态栏段 | 关闭不渲染；开启文本含 scope 前缀；段顺序为 model→provider→routing→balance |
| U9 | 命令解析 | `/routing` 全子命令 + 别名 + 归一化 + 未识别键给出候选 |
| U10 | 补全候选 | §5.2 表逐项；动态难度候选来自模型能力；含 `inherit` |
| U11 | 会话 metadata 读写 | 未知字段/未知 version 容忍；内容未变不写盘 |
| U12 | 工作区偏好局部写回 | 只改 `routing` 子树，`chat-prefs.yaml` 其他键逐字不变（D5 决策） |

### §10.2 集成（I）

| 编号 | 用例 | 断言要点 |
| --- | --- | --- |
| I1 | aicli 端到端 | `/routing on` → 下一 turn `loopConfig.MainAgentRouting != nil`；`off` → nil |
| I2 | 会话隔离 | 会话 A 开启不影响会话 B（并发两个 ChatSession） |
| I3 | 恢复语义 | `--session <id>` 恢复后 override 生效；`/routing inherit` 后恢复为配置默认 |
| I4 | 工作区偏好 | `save workspace` → 新会话（同 cwd）默认继承；不同 cwd 不受影响 |
| I5 | runtime API | `PUT` 后 `GET` 一致；`DELETE` 后回落；跨进程重启后 override 仍在 |
| I6 | 快照克隆 | 热重载后会话级 override 不串台、不丢失（扩展 `TestCloneAICLIRoutingConfigKeepsMainAgentRouting`） |
| I7 | 事件 | `session.routing_changed` 在 TUI 与 API 两条写入路径都会发布 |
| I8 | frontend | 工作区关闭 → 请求 `enable_routing=false`；`PUT` 会话 override → 底部 chip 与 composer 徽标更新 |
| I9 | 状态栏一致性 | 依次执行 `/model`、`/provider`、`/reasoning_effort`、`/routing on` 后，底部行四项同帧正确 |

### §10.3 回归护栏（REG）

| 编号 | 必须保持 | 锚点 |
| --- | --- | --- |
| REG1 | routing override **不得**持久化为 provider/model | `backend/cmd/aicli/commands/chat_actor_host_test.go:1514` |
| REG2 | 主 Agent 路由接线与配置隔离 | `backend/internal/api/skills/main_agent_routing_wiring_test.go`（全量） |
| REG3 | 状态栏既有段顺序与折叠行为 | `backend/cmd/aicli/commands/chat_balance_refresh_test.go:397`、`ui/style/statusline_blank_test.go`、`ui/rendering_golden_test.go` |
| REG4 | 未识别斜杠命令仍走"发给模型"路径，但 `/routing` 必须被识别 | `chat_command_result.go:390-403` 相关测试 |
| REG5 | 工作区偏好写回不破坏其他键 | `chat_persistence_workspace` 既有测试 |
| REG6 | 关闭态：`enable_routing` 缺省语义与今日一致（后端默认不启用） | MA 方案 §11 验收项 |

---

## §11 风险与缓解

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| 会话 metadata 写入放大 | 每次切换写会话文件，`UpdatedAt` 抖动、I/O 增加 | 仅在内容变化时写；批量变更合并为一次写；必要时沿用 `PreserveUpdatedAt` 思路 |
| 旧会话文件兼容 | 反序列化失败或字段丢失 | `Context` 内 JSON + version 字段 + 容忍未知字段；缺失即"继承" |
| 状态栏宽度挤压 | 窄屏模型名被折叠 | routing 段 Priority 低于 model/provider；关闭态完全不渲染 |
| 与全局热重载竞态 | 解析到半更新的配置 | turn 边界深拷贝快照；handler 侧克隆必须包含 override |
| 双端语义漂移 | TUI 与 Web 行为不一致 | §7.4 契约表 + 跨端集成测试（I5/I8） |
| 用户误把"临时开关"存成工作区偏好 | 影响后续所有会话 | `scope` 默认 `session`；`save workspace` 需显式子命令；保存后回显路径 |
| 覆盖 health 等固定语义字段 | 破坏 MA 方案安全边界 | 白名单式可覆盖字段集合，其余显式拒绝 + warning（U5） |

---

## §12 验收标准

1. 未做任何配置/命令变更时，行为与今日**逐位一致**（INV-A3，REG6）。
2. 会话内 `/routing on` 只影响该会话；另一并发会话与 runtime server 其他会话不受影响（I2）。
3. `/routing status` 能逐字段说明取值来源（session/workspace/config/default）与基线值（§5.4）。
4. `/routing save workspace` 后，同工作目录的新会话默认继承；`/routing reset workspace` 可完全撤销（I4）。
5. 模型切换后，TUI 底部状态行同时正确显示 `model · provider · reasoning_effort · routing`（I9）。
6. frontend workspace 可独立于全局配置设置工作区路由；会话级覆盖可保存/清除；底部 chip 与 TUI 语义一致（I8）。
7. 所有 REG 护栏测试通过，尤其是「route 偏移不落盘为 provider/model」（REG1）。

---

## §13 开放问题（待评审决策）

| 编号 | 问题 | 建议 |
| --- | --- | --- |
| O1 | 会话 override 存 `Metadata.Context`（JSON 字符串）还是新增顶层结构化字段？ | 建议 `Context`（零迁移成本、跨端一致）；若后续字段膨胀再迁移 |
| O2 | 请求级 `routing`（不落库）是否首发就做？ | 建议 S3 一起做，前端 composer 的"仅本回合"体验依赖它 |
| O3 | 是否需要 `/routing scope global` 直接改配置文件？ | 建议**不做**（会复现用户痛点）；只提供提示与文件路径 |
| O4 | 子 Agent 覆盖首发是否包含 `profiles` 级联？ | 建议 S5，先只做 `enabled` + `levels` |
| O5 | 工作区偏好与"浏览器本地设置"冲突时以谁为准？ | 建议后端工作区偏好为准（跨设备/跨端一致），前端本地设置作为未登录/离线兜底 |
| O6 | 是否需要"路由审计"（谁在哪个会话改了什么）？ | 建议复用 `UpdatedBy` + 既有观测事件，不新建审计链路 |

---

## §14 附：子命令建议值速查表

```
/routing status
/routing on | off | toggle | inherit [all|main|sub|<key>]
/routing main status
/routing main enabled on|off|inherit
/routing main levels easy,normal,hard|inherit
/routing main allow_expert on|off|inherit
/routing main default_difficulty easy|normal|hard|inherit
/routing main allow_escalation_retry on|off|inherit
/routing main cost_guard_mode soft|hard|off|inherit
/routing main max_consecutive_expensive_steps 0|3|6|12|inherit
/routing main expensive_levels hard,expert|inherit
/routing main max_invalid_reports_per_turn 1|3|5|inherit
/routing main downgrade_confirm_steps 1|2|3|5|inherit
/routing main min_dwell_steps 1|2|3|inherit
/routing main health.respect_provider_health on|off|inherit
/routing sub status | on | off | inherit
/routing sub profile <name> levels <...>|inherit
/routing sub profile <name> default_difficulty <...>|inherit
/routing levels easy,normal,hard
/routing guard soft|hard|off
/routing scope session|workspace|global
/routing save workspace
/routing reset workspace
/routing explain
/routing doctor
```

---

## 变更记录

| 日期 | 版本 | 说明 |
| --- | --- | --- |
| 2026-09-22 | v1 Draft | 首版规划：三层解析模型、会话级 override、`/routing` 子命令与建议值、状态栏与 frontend workspace 方案 |
