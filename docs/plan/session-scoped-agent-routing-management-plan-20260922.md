# 会话级 Agent 路由管理与切换方案（2026-09-22）

> 版本：**Draft v3.1**（2026-09-22 修订；v1 审查见 `docs/plan/session-scoped-agent-routing-management-plan-20260922-review.md`）
> 范围：仅规划，不含实现。本文件是路由管理与切换的唯一权威设计稿。
> v2 修订要点：
> 1. 修正 `enable_routing` 语义错配（B1）——它是技能候选路由/编排开关，本方案不再引用；
> 2. 以真实 `AICLISubagentRoutingConfig`（`config.go:672-708`）重写子 Agent 覆盖结构，删除虚构的 `AICLISubagentRouteProfileOverride`（B2）；
> 3. 新增"开启推导"（B3）与跨字段冲突消解阶梯（B4）；
> 4. 解析器恒非 nil、显式携带来源（B5）；状态栏插入算法改为 canonical 段序重构（B6）；
> 5. **新增 §5.2/§5.3：AICLI TUI 逐级（easy/normal/hard/expert）配置 provider/model/reasoning_effort 的交互式面板**（v1 最大缺口）；
> 6. 前端在右侧栏「会话详情」面内新增“路由”区块（v3 定稿，§7.2；不新建底部状态条），事件契约走 `contract.go` + contractgen（M7）。
> v3.1 修订要点：澄清 `/routing` 渲染形态——**独立全屏仅限「打开面板」一族命令**（`/routing`、`/routing main|sub [<level>]`、键路径直达，共用同一全屏面板，复用 `chatPicker*` + `ui.FullScreenList`）；`show/doctor/set/on|off/save/reset` 为行内文本；frontend 区块与 micro web 状态栏均非全屏（§5.3.1、§10.2 I-11）。
> 证据口径：全部结论附 `file:line`；源码路径一律以 `backend/` 前缀书写。

---

## §0 摘要

### §0.1 问题陈述（v2 重写）

1. **配置粒度错误 + 生效边界被高估（M6 修正）**：路由开关位于全局配置节 `aicli.main_agent.routing` / `aicli.subagents.routing`（`main_agent_routing.go:63-80`、`config.go:672-708`）。runtime 侧**没有**把新配置回写到已建会话 actor 的路径：配置注入入口为 `handler.go:448-454`（快照克隆 `:504`、MainAgent 分支 `:529-533`），接线发生在 actor 构建期（`session_runtime_support.go:3933`→`:3937`，定义 `:4146`），`NewReActLoop` 再深拷贝冻结（`loop.go:83` 字段、`:207` 深拷贝）。因此"改配置即全局热生效"**不成立**——只影响新建 actor/会话。本方案要解决的是"会话级、免重启、可回退"的粒度问题。
2. **可见性缺口**：TUI 状态栏段只有 State/Model/Path/Usage/Balance/Mode/Meta/Provider 八类（`statusline.go:12-24`），没有 routing 段；用户无法确认"是否启用 / 生效档位 / 来源（会话 or 配置）"。
3. **逐级可配置性缺口（v2 新增重点）**：主 Agent 的 `profiles.<level>`（`main_agent_routing.go:79`）与子 Agent 的 `levels.<level>`（`config.go:702`）决定每个难度档位用哪个 provider/model/reasoning_effort，但**目前没有任何 UI 能查看或编辑它们**——只能手写 YAML。本方案在 AICLI TUI 提供逐级交互式面板（§5.2/§5.3），frontend 提供等价设置页（§7.3）。
4. **会话级覆盖缺失**：需要"仅本会话"的临时覆盖（默认继承配置），并与既有 chat 偏好三层解析（`chat_preferences.go:13-22`、`chat_persistence_workspace.go:26-27`）同构，复用同一套来源枚举与落盘设施。
5. **前端缺口（B1/M15 修正）**：`frontend/` 既没有主 Agent 路由入口（既有的 `enable_routing` 是技能候选路由/编排开关，`handler.go:1610`、`:1934/:1937`，与本方案无关），也没有路由展示面（`web_statusbar.go:3-10` 面向 aicli micro web client）；展示位置 v3 定稿＝右侧栏 `sessionDetail` 面内新增路由区块（`panel-registry.ts:148-161`），**不新建底部状态条**。

### §0.2 方案一句话

在既有配置层之上新增**会话级 override**与**工作区偏好**两个可写层，经统一解析器（逐字段合并 + 校验 + 确定性回退）产出 `RoutingResolution`；TUI 以 `/routing` 命令 + 全屏面板提供**逐级 provider/model/effort 编辑**，状态栏/`/status` 行/micro web client 端点统一展示来源；frontend 在右侧栏「会话详情」面新增路由区块消费同一份契约（v3 定稿，§7.2）。

---

## §1 现状证据（代码锚点，v2 更正版）

### §1.1 配置与校验（后端 agentconfig）

| 事实 | 锚点 | 用途 |
| --- | --- | --- |
| 主 Agent 路由配置结构（Enabled/Levels/AllowExpert/DefaultDifficulty/CostGuardMode/MaxConsecutiveExpensiveSteps/ExpensiveLevels/MaxInvalidReportsPerTurn/DowngradeConfirmSteps/MinDwellSteps/HealthGate/Profiles） | `backend/internal/agentconfig/main_agent_routing.go:63-80` | §3 覆盖结构的镜像对象 |
| HealthGate 子结构（respect_provider_health / latch_scope / on_chain_exhausted / honor_min_dwell） | `main_agent_routing.go:49-59` | §3.2 键名表；M2 |
| 默认值应用（0=未设置→默认；`MaxConsecutiveExpensiveSteps` 例外 0=不限；`HonorMinDwell` 无条件 true） | `main_agent_routing.go:95-124` | §3.5 显式 0 语义（M3） |
| 校验硬规则：enabled⇒levels 非空；levels⊇expert⇒allow_expert；allow_expert⇒max_consecutive_expensive_steps 有限；default_difficulty∈levels；cost_guard_mode∈{soft,hard}；latch_scope=turn；on_chain_exhausted=baseline；profiles 键∈levels | `main_agent_routing.go:147-162`、`:188-200`、`:207-209` | §3.5 开启推导与冲突消解（B3/B4） |
| `EffectiveMainAgentRoutingConfig` 未配置返回 nil | `main_agent_routing.go:83-88` | §4.1 关闭态语义（B5） |
| 子 Agent 路由配置结构（Enabled *bool / DefaultDifficulty / Levels / TaskTypes / Roles / Failover / AvailabilityPolicy / …） | `backend/internal/agentconfig/config.go:672-708` | §3.2 子覆盖结构（B2） |
| `AICLISubagentRouteProfile`（provider/model/reasoning_effort/thinking_effort/max_tokens/timeout/temperature/availability/availability_reason/prompt_cache/candidates） | `config.go:720-738` | §3.2 profile 字段级覆盖 |
| Team 路由缺省回落子 Agent 路由 | `config.go:657-670`（`EffectiveTeamRoutingConfig`） | §3.2 覆盖链路 |
| 难度归一：easy/normal/hard/expert（含别名） | `config.go:1602-1620`（`normalizeSubagentDifficulty`） | §5.2 面板档位集合 |

### §1.2 aicli TUI（命令、选择器、偏好、状态栏）

| 事实 | 锚点 | 用途 |
| --- | --- | --- |
| `/model`、`/provider` legacy 分派 | `backend/cmd/aicli/commands/command.go:326-331` | §5.5 命令接线 |
| 结构化命令白名单（否定式 `commandMatches` 链，新命令需在此放行） | `chat_command_result.go:390-405` | §5.5 接线约束 |
| 全屏选择器基建：`chatPickerOpen` / `chatPickerStage` / `chatPickerStageResult` / `chatPickerClose` | `chat_picker_common.go:115-141`、`:155-181` | §5.2 面板复用 |
| `ui.FullScreenListOptions`（Title/Subtitle/Items + OnSelectionChanged/OnCancel/OnConfirm/OnDelete/PreviewForItem 钩子） | `backend/cmd/aicli/ui/fullscreen_list.go:30-56` | §5.2 面板能力边界 |
| `/model` 三级选择流（provider→model→reasoning）可复用范式 | `chat_model_picker.go:55-245` | §5.3 逐级编辑流程 |
| provider 候选枚举 / model 候选枚举 / 选择项构建 | `chat_model_command.go:820`、`chat_model_picker.go:265-280` | §5.3 |
| reasoning 目录（按模型能力，未声明则不猜测） | `chat_reasoning.go:29-50`（`reasoningEffortCatalogForModel`）、`chat_reasoning_command.go:173-183` | §5.3 effort 阶段 |
| `/agents routing` 既有语义（子 Agent/Team 摘要 + `routing test`） | `chat_debug.go:205`、`:258-261`、`:333`、`:719-742`；摘要渲染 `:80-123` | §6.4 边界（M14） |
| `/debug` 的 `routing` 参数（子 Agent / Team 配置摘要） | `chat_slash_command_catalog.go:104-119`（`:115`） | §6.4 不改既有语义（M14） |
| 命令目录 spec（Name/Aliases/Usage/Summary/Group/Args/Interactive/…） | `chat_slash_command_catalog.go:19-36` | §5.5 新命令登记 |
| 会话 metadata 键常量（`sessionmeta.LegacyAICLI*`）与读取 helper | `backend/internal/sessionmeta/sessionmeta.go:71-95`、`chat_session.go:32-46`、`chat_session.go:1679`（`runtimeSessionContextString`，仅字符串） | §3.4 存储键（M9） |
| 偏好来源枚举（`session` 值已存在） | `chat_preferences.go:13-22`（`:17`） | §3.1 来源复用（N2） |
| 工作区偏好读写（`$HOME/.aicli/workspace/<projectIDForPath>/chat-prefs.yaml`，**单数目录**；hash=sha256(path) 前 8 字节→16 hex，原子写） | `backend/internal/agentconfig/chat_persistence_workspace.go:29-34`、`:83-93`、`:98-104`、`:124-130`、`:170-180`、`:184-221` | §3.3 工作区层（D5 沿用）、§3.4 核实 |
| 会话持久化默认目录（`~/.aicli/sessions`；文件后端 `YYYY/MM/DD/<id>.json`，运行时后端 `sessions/runtime/*.sqlite`） | `backend/internal/aiclipaths/paths.go:10-13`、`backend/cmd/aicli/commands/chat_session.go:1334-1348` | §3.4 会话层物理落点（v3 核实） |
| 配置层写路由（项目层文件存在→项目；都不存在→用户层 `$HOME/.aicli/<name>`） | `backend/internal/agentconfig/config_layers.go:118-155`、`config_write_route.go:32-47`、`:130-142` | §5.4 config 层可写（v3） |
| 前端右侧栏面板注册表（`sessionDetail` 面 → `SessionDetailSurface`） | `frontend/src/components/workspace/panel-registry.ts:148-161`、`session-detail-surface.tsx:76-90` | §7.2 展示位置（v3） |
| 偏好更新结构（DefaultProvider/DefaultModel/ReasoningEffort/Stream/FastMode，指针=不修改） | `backend/internal/agentconfig/chat_persistence.go:15-24`、`:98-127` | §3.3 扩展 `Routing` 字段 |
| 状态栏段序装配（model→provider→balance→context→cwd→project→branch→window→in→out→Fast） | `chat_interaction.go:2526-2555` | §6.1 |
| **动态**插入索引：balance 插到 provider（或 model）之后 | `chat_interaction.go:1081-1091`（`chatAccountBalanceStatusInsertIndex`） | §6.1 必须重构（B6） |
| 状态段种类恰好 8 个 | `backend/cmd/aicli/ui/render/statusline.go:12-24` | §6.1 |
| 状态栏 web 投影端点（面向 aicli micro web client；provider/model/effort 由 cfg-bar 展示） | `chat_debug_display_http.go` 族 / `web_statusbar.go:3-10` | §6.5（M15） |
| `/status` 行表（命令注册 `chat_slash_command_catalog.go:82-83`） | `chat_status.go:93-105` | §6.3 |
| 宿主接线（主会话才接；子会话 return） | `chat_actor_host.go:2297`、`:2311-2316` | §4.2 / §5.6 非主会话（M16） |
| actor 构建期接线（第三参为内联表达式 `strings.TrimSpace(childAgentType)=="" && childDepth==0 && !childReadOnly`） | `session_runtime_support.go:3933`、`:3937-3938`、定义 `:4146` | §4.3（N3） |
| `LoopReActConfig.MainAgentRouting` 字段 / 深拷贝冻结 | `backend/internal/agent/loop.go:83`、`:207` | §4.3（N1） |
| 关闭态既有语义：`cfg == nil || !cfg.Enabled` | `main_agent_route.go:237-246` | §4.1（B5） |
| reasoning 切换后刷新状态栏 + 发布模型选择变更事件 | `chat_reasoning_command.go:206-212` | §6.2 / §7.1（M5：前端消费方未证实） |

### §1.3 测试锚点（REG 依据）

| 事实 | 锚点 | 用途 |
| --- | --- | --- |
| 宿主接线测试：**指针同一性**断言（wiring 必须把配置对象原样交给 loop） | `chat_main_agent_routing_test.go:30-40`；隔离测试 `:68-90` | §4.2 fast path（M8） |
| balance 段刷新 golden 测试（锚定动态插入行为） | `chat_balance_refresh_test.go:397` | §6.1 REG（B6） |
| 配置→loop 接线测试 | `main_agent_routing_wiring_test.go:26`、`:67` | §10.3 |
| actor host 主/子会话隔离测试 | `chat_actor_host_test.go:1514` | §10.3 |
| 事件契约登记位置 | `backend/internal/events/contract.go:105-108`；事件族 `events/main_agent_routing.go`；观察类型 `runtimeobserve/known_types.go:205` | §7.2（M7） |
| 前端事件契约是**生成物**（禁止直改） | `frontend/src/types/runtime/event-contract.ts:1` | §7.2（M7） |
| 前端既有模型面板（provider/model/reasoning 三级菜单，可复用交互范式） | `frontend/src/components/workspace/composer-model-panel.tsx:55-84` | §7.3 |
| 前端设置对话框分区注册点 | `settings-dialog.tsx:33-42`、`:107-142`、`:237-255` | §7.3 |

### §1.4 v1 锚点更正对照（N1）

| v1 写法 | v2 更正 |
| --- | --- |
| `handler.go:476/529/581` | `:476`（`mainAgentRoutingConfig()`）✅；快照克隆调用 `:454`、定义 `:504`、MainAgent 分支 `:529-533` |
| `loop.go:80` 字段 | `:83`（`:80` 是注释）；深拷贝 `:207` |
| `session_runtime_support.go:3937` 第三参 `isMain` | 内联表达式（`:3937-3938`），函数定义 `:4146` |
| §6.1"不得改动既有段索引算法" | **删除**：`chatAccountBalanceStatusInsertIndex` 动态返回 provider 之后（`chat_interaction.go:1081-1091`），必须重构（B6） |
| §3.1"新增来源值 session" | `session` 已存在（`chat_preferences.go:17`），只需新增展示文案（N2） |
| §3.2 `AICLISubagentRouteProfileOverride{Levels, DefaultDifficulty}` | **不存在**；真实结构见 `config.go:672-708`、`:720-738`（B2） |
| §5.1 `scope` 子命令 | **删除**，保留显式 `save workspace` / `reset workspace`（M12） |
| §3.2 顶层 `Enabled` + `MainAgent.Enabled` 双开关 | 只保留 `main_agent.enabled` / `sub_agent.enabled` 两个分节开关（M13） |
| §6.2 `soft!`（已触发） | 首版删除（无运行期状态来源，N10） |
| `command.go:326-347` legacy 分派、`chat_persistence_workspace.go` 原子写 | v2 已逐行复核（见 §1.2） |

---

## §2 目标、非目标与不变量

### §2.1 目标

| 编号 | 目标 | 验收锚点 |
| --- | --- | --- |
| G1 | 会话级路由覆盖：默认继承，`/routing on\|off`、逐级 profile 覆盖、可清除、可回退 | §10.1 U1-U3 |
| G2 | 工作区偏好层：`chat-prefs.yaml` 新增 `routing` 子树，沿用 D5 原子写 | §10.1 U-8/U-9 |
| G3 | **逐级 UI（v2 核心）**：AICLI TUI 全屏面板按 easy/normal/hard/expert 编辑 provider/model/reasoning_effort（主+子），frontend 会话详情路由区块提供等价编辑能力 | §5.2/§5.3、§7.2/§7.3、§10.2 I1-I3 |
| G4 | 可见性：状态栏 routing 段 + `/routing show` + `/status` 行 + micro web client 投影 + frontend 会话详情路由区块，统一携带来源 | §6、§7.2、§10.2 I-5/I-6 |
| G5 | runtime API：会话级路由读取 + 写入（含鉴权） | §4.4、§10.1 U-10/U-11、§10.2 I-6 |
| G6 | 测试与回归护栏：含既有指针同一性测试与 balance golden 测试的显式处置 | §10.3 |

### §2.2 非目标（v2 明确）

1. **不改 `enable_routing`**：它是技能候选路由/编排开关（`handler.go:1610`、`:1934/:1937`），本方案不引用、不改名、不接管（B1）。
2. **不改既有 `/debug routing` 与 `/agents routing`**：保留"子 Agent / Team routing"语义（`chat_debug.go:258-261`、`:719-742`；catalog `:115`）；主 Agent 视图走新入口 `/routing`（M14）。
3. **全局配置写入按现有层路由开放（v3 修订）**：UI 可写 config 层，目标文件由 `WritableLayer` 决定——`./.aicli/config.yaml` 存在则写项目层；否则写用户层 `~/.aicli/config.yaml`（即「工作区没有 `.aicli` 配置」时落全局；判定按**文件存在性**，`config_layers.go:118-140`）。写前必须 `ValidateMainAgentRoutingConfig`（`main_agent_routing.go:132-162`）+ 二次确认（§5.4）。
4. **不做候选链/可用性标注的 UI 编辑**：`candidates[]`、`availability`、`prompt_cache` 首版只在面板中只读展示；编辑列入后续迭代（§11 U-8）。
5. **不做 `model_changed` 消费方改造**：现状发布端存在（`chat_reasoning_command.go:206-212`）、前端零消费；模型变更如何通知前端列为待确认（U-2），本方案只新增 `session.routing_changed`。
6. **不做“当前生效档位”的强承诺**：level 在可观测时显示（turn 内难度判定落点），不可观测时显示 `default_difficulty` 并标注；可观测性列入 §11 U-10。
7. **不引入请求级路由为必需项**：请求级 `routing` 仅预留（§4.5：仅本 turn、不落盘），首版可写层 = 会话 > 工作区 > 配置（配置按层路由，§5.4）。

### §2.3 不变量（硬约束）

| 编号 | 不变量 | 依据 |
| --- | --- | --- |
| INV-A1 | 关闭态零行为变化：`Effective==nil \|\| !Effective.Enabled` ⇒ `loopConfig.MainAgentRouting=nil` | `main_agent_route.go:237-246`；B5 |
| INV-A2 | 会话覆盖只写会话记录 metadata（物理落 `~/.aicli/sessions/`，§3.4），绝不触碰配置文件；config 层写入只能走专用路由（§5.4） | §3.4 |
| INV-A3 | 主/子开关隔离：子会话不受主 Agent 路由影响；非主会话变更类命令拒绝 | `chat_actor_host.go:2311-2316`；M16 |
| INV-A4 | 默认不写工作区偏好：仅显式 `save workspace` 或面板目标=工作区时写入 | §5.2 |
| INV-A5 | 解析器只读来源对象；仅在"无会话覆盖且无工作区偏好"时返回原配置指针（fast path） | M8 |
| INV-A6 | 校验失败必须可见：warning 记录被丢弃字段、原因、最终来源；不静默吞字段 | B4 |
| INV-A7 | 覆盖按字段合并：标量字段级、profile 字段级、`candidates` 整链替换；map 按 key 合并 | §3.2/§3.5 |
| INV-A8 | UI 展示值 = 解析器产出值（面板、状态栏、`/routing show`、micro web client 端点、frontend 会话详情区块单一来源） | §5.4/§6 |

---

## §3 数据模型

### §3.1 解析模型（五层，逐字段）

```
请求级（仅本 turn，预留） > 会话覆盖（metadata） > 工作区偏好（chat-prefs.yaml）
    > 全局配置（aicli.main_agent.routing / aicli.subagents.routing） > 内置默认（off）
```

- **来源枚举复用**既有 `chatPreferenceSource`（`chat_preferences.go:13-22`；`session` 值已存在于 `:17`）——v2 只新增展示文案，不新增枚举值（N2/N7）。
- **解析结果恒非 nil**（B5）：

```go
type RoutingResolution struct {
    Effective *AICLIMainAgentRoutingConfig // 可为 nil（关闭/未配置）
    Sources   map[string]chatPreferenceSource // 逐字段来源，键见 §3.5 键名表
    Warnings  []RoutingWarning             // 被丢弃字段/回退原因（INV-A6）
    EffectiveSub *AICLISubagentRoutingConfig // 子 Agent 侧（含 team 回落说明）
    SubSources   map[string]chatPreferenceSource
}
type RoutingWarning struct {
    Field  string // 如 main_agent.profiles.easy.model
    Reason string // 如 "not in levels"、"allow_expert requires finite max_consecutive_expensive_steps"
    FallbackTo chatPreferenceSource // 回退后的最终来源
}
```

- 关闭态语义：`Effective` 为 nil（或 `Enabled=false`）时，UI 依据 `Sources["main_agent.enabled"]` 区分 `off (session)` / `off (inherited from config)` / `off (default)`（B5/N8）。
- 子 Agent 侧：`EffectiveSub` 的解析与主 Agent 同构；team 未单独配置时按 `EffectiveTeamRoutingConfig`（`config.go:657-670`）回落——面板与状态展示必须说明"team 继承 sub_agent"。

### §3.2 会话覆盖结构 `AICLISessionRoutingOverride`（v2 重写，B2/M1/M2/M3/M13/N4）

> 编码：JSON 字符串（§3.4）。**所有标量字段使用指针**：`nil`=继承，非 nil=显式覆盖（含显式 `false`/`0` 的受限语义见 §3.5.3）。

```go
// 主 Agent 覆盖：镜像真实 AICLIMainAgentRoutingConfig（main_agent_routing.go:63-80）。
// 不存在的字段一律不得出现（v1 的虚构结构已删除）。
type AICLISessionMainAgentRoutingOverride struct {
    Enabled                      *bool     `json:"enabled,omitempty"`
    Levels                       *[]string `json:"levels,omitempty"`
    AllowExpert                  *bool     `json:"allow_expert,omitempty"`
    DefaultDifficulty            *string   `json:"default_difficulty,omitempty"`
    CostGuardMode                *string   `json:"cost_guard_mode,omitempty"` // 仅 soft|hard（M1）
    MaxConsecutiveExpensiveSteps *int      `json:"max_consecutive_expensive_steps,omitempty"`
    ExpensiveLevels              *[]string `json:"expensive_levels,omitempty"`
    MaxInvalidReportsPerTurn     *int      `json:"max_invalid_reports_per_turn,omitempty"`
    DowngradeConfirmSteps        *int      `json:"downgrade_confirm_steps,omitempty"`
    MinDwellSteps                *int      `json:"min_dwell_steps,omitempty"`
    // health_gate 只开放 respect_provider_health；latch_scope / on_chain_exhausted /
    // honor_min_dwell 是硬规则，配置层与覆盖层都不开放（M2，main_agent_routing.go:193-200/:123）。
    RespectProviderHealth *bool `json:"respect_provider_health,omitempty"`
    // Profiles：键为归一化档位（easy|normal|hard|expert），键必须 ∈ Levels（B3）。
    Profiles map[string]AICLISessionRouteProfileOverride `json:"profiles,omitempty"`
}

// 子 Agent 覆盖：镜像真实 AICLISubagentRoutingConfig（config.go:672-708）的可用子集。
type AICLISessionSubAgentRoutingOverride struct {
    Enabled           *bool `json:"enabled,omitempty"`
    DefaultDifficulty *string `json:"default_difficulty,omitempty"`
    // Levels 对应真实配置的 levels（config.go:702），键为归一化档位。
    Levels map[string]AICLISessionRouteProfileOverride `json:"levels,omitempty"`
    // TaskTypes / Roles / Failover / AvailabilityPolicy 等首版不开放（§11 U-5 闭环后评估）。
}

// profile 字段级覆盖：nil=继承配置层同名字段（INV-A7）。
type AICLISessionRouteProfileOverride struct {
    Provider           *string `json:"provider,omitempty"`
    Model              *string `json:"model,omitempty"`
    ReasoningEffort    *string `json:"reasoning_effort,omitempty"`
    ThinkingEffort     *string `json:"thinking_effort,omitempty"`
    MaxTokens          *int    `json:"max_tokens,omitempty"`
    Timeout            *string `json:"timeout,omitempty"` // duration 字符串（如 "90s"）
    Temperature        *float64 `json:"temperature,omitempty"`
    Availability       *string `json:"availability,omitempty"`
    AvailabilityReason *string `json:"availability_reason,omitempty"`
    PromptCache        *bool   `json:"prompt_cache,omitempty"`
    Candidates         *[]AICLISubagentRouteCandidate `json:"candidates,omitempty"` // 整链替换
}

type AICLISessionRoutingOverride struct {
    MainAgent *AICLISessionMainAgentRoutingOverride `json:"main_agent,omitempty"`
    SubAgent  *AICLISessionSubAgentRoutingOverride  `json:"sub_agent,omitempty"`
    UpdatedAt time.Time `json:"updated_at,omitempty"`
    UpdatedBy string    `json:"updated_by,omitempty"` // aicli-tui | web | api | config（N4：与 Source 合并为写入端）
}
```

要点：
1. **主/子分节开关**（`main_agent.enabled`、`sub_agent.enabled`）是仅有的两个开关；v1 的顶层 `Enabled` 已删除（M13）。
2. 主 Agent 用 `profiles`、子 Agent 用 `levels` 承载逐级 provider/model/effort——这是既有配置键名，UI 层统一呈现为"级别"，但落盘键名不合并（§5.2 表格显式说明）。
3. `candidates` 首版只读展示；若未来开放，语义为**整链替换**（不做链内逐项合并）。
4. Team 覆盖：首版不新增 team 专属覆盖；`EffectiveTeamRoutingConfig` 回落链保持原样，文档与 UI 提示"team 未单独配置时继承子 Agent 路由"。

### §3.3 工作区偏好层（沿用 D5，扩展 routing 子树）

- 存储位置与写盘方式不变：`$HOME/.aicli/workspace/<projectIDForPath>/chat-prefs.yaml`（**目录单数**；id=sha256(path) 前 8 字节→16 hex，`chat_persistence_workspace.go:29-34`、`:83-93`、`:170-180`），原子写 `writeFileAtomic`（`:221`）。
- **扩展方式**：在 `AICLIChatConfig`（`config.go:596-608`）新增字段：

```go
// Routing 仅在工作区偏好文件（chat-prefs.yaml）中有意义。
// 全局配置中的 aicli.chat.routing 不参与解析——解析器只读
// aicli.main_agent.routing / aicli.subagents.routing（见 §3.5）。
Routing *AICLIWorkspaceRoutingPreferences `yaml:"routing,omitempty" mapstructure:"routing"`

type AICLIWorkspaceRoutingPreferences struct {
    MainAgent *AICLIMainAgentRoutingConfig `yaml:"main_agent,omitempty" mapstructure:"main_agent"`
    SubAgent  *AICLISubagentRoutingConfig  `yaml:"sub_agent,omitempty" mapstructure:"sub_agent"`
}
```

- 写盘 API：`AICLIChatPreferenceUpdate`（`chat_persistence.go:15-24`）新增 `Routing *AICLIWorkspaceRoutingPreferences`（外层 nil=不修改；内层 nil=清除），并在 `applyAICLIChatPreferenceUpdate`（`:98-127`）落地。现有 `SaveWorkspaceChatPreferences` 调用方（`chat_preferences.go:275`、`chat_reasoning_command.go:252`、`chat_stream_command.go:145`、`chat_fast_command.go:146`）零改动。
- **工作区身份与 cwd（N9）**：读写以**会话创建时记录的 workspace 路径**为准（`Session.Workspace`；缺省时回退当前 cwd），面板与 `/routing show` 必须回显目标路径与 `<hash>`；`/resume` 到其他目录的会话不得静默改用当前 cwd 的偏好文件。
- 全局配置中若出现 `aicli.chat.routing`，配置加载时给出 warning"该位置不参与解析，请使用 aicli.main_agent.routing / aicli.subagents.routing"（防呆，非错误）。

### §3.4 存储位置与键

| 层 | 位置 | 编码 | 依据 |
| --- | --- | --- | --- |
| 会话覆盖 | `Session.Metadata.Context["aicli_routing_override"]`；**物理落盘 `~/.aicli/sessions/`**（文件后端 `YYYY/MM/DD/<session-id>.json`；运行时后端 `sessions/runtime/session_runtime.sqlite`） | **JSON 字符串**（`AICLISessionRoutingOverride`） | `chat/session.go:52`（`Metadata.Context`）；`chat_session.go:1334-1348`（文件会话路径）；`aiclipaths/paths.go:10-13`（`DefaultSessionsDir`）；`chat_session.go:1679`（字符串读取家族）+ 新增 JSON 解码 helper（M9） |
| 工作区偏好 | `$HOME/.aicli/workspace/<projectIDForPath(workspacePath)>/chat-prefs.yaml` → `aicli.chat.routing` 子树（**目录单数**；id=sha256(path) 前 8 字节→16 hex） | YAML（`AICLIWorkspaceRoutingPreferences`） | `chat_persistence_workspace.go:29-34`、`:83-93`、`:170-180`、`:184-221` |
| 全局配置 | `aicli.main_agent.routing` / `aicli.subagents.routing`（**可写**：按层路由落到 `./.aicli/config.yaml` 或 `$HOME/.aicli/config.yaml`） | YAML（既有结构） | `main_agent_routing.go:63-80`、`config.go:672-708`；写路由 `config_layers.go:118-155`、`config_write_route.go:32-47` |

- 新增常量：`sessionmeta.LegacyAICLIRoutingOverride = "aicli_routing_override"`（`sessionmeta.go:71-95` 同族），并在 `chat_session.go:32-46` 建立别名映射，避免裸字面量。
- 会话 metadata 随会话经 SQLite marshal/unmarshal 持久化（`sqlite_storage.go:1299`、`:1437-1439`）。
- **写入串行化（M11）**：同一会话的 override 写入必须串行化——TUI 侧走 actor mailbox（或会话级 mutex），API 侧在 handler 内使用同一把会话锁做"读-改-写"；`UpdatedAt`/`UpdatedBy` 记录最后写入端，冲突时**后写胜出**并在下一次读取时以 warning 形式暴露被覆盖的旧值（如需要）。§10.1 增补并发写用例。
- **落点核实（v3，实测 + 代码双证）**：`~/.aicli/workspaces`（**复数**）**不存在**，代码中也没有该常量——不要新建。真实存在的是：① `~/.aicli/workspace/`（**单数**）＝工作区偏好目录（`chat_persistence_workspace.go:31`），本机实测 10 个 16-hex 目录，各含 `chat-prefs.yaml`（`aicli.chat.default_provider/default_model/reasoning_effort`）；② `~/.aicli/workspace_directories.yaml`＝工作区目录注册表（id=`sha1(pathKey(path))[:12]`，**12 hex**，`workspaceregistry/store.go:29,33,70-86`；HTTP `workspace_directory_handlers.go:66-73`）；③ `~/.aicli/sessions/`＝会话持久化根（`aiclipaths/paths.go:10-13`），本机含 `session_history.sqlite` 与 `sessions/runtime/session_runtime.sqlite` 等。
- **两套 id 不可混用（实现警示）**：工作区偏好目录名 = `projectIDForPath`（sha256 前 8 字节→16 hex，`chat_persistence_workspace.go:170-180`）；registry id = sha1 前 12 hex（`workspaceregistry/store.go:33`）。面板/日志回显必须用前者定位 `chat-prefs.yaml`，不得用 registry id 反查目录。
- **会话层不新建独立文件（v3）**：override 随会话记录持久化（本机实测 `~/.aicli/sessions/` 即会话存储根）；若未来要「可手工编辑的独立文件」，落点也应在 `~/.aicli/sessions/` 之下（§11 U-13）。

### §3.5 约束校验与冲突消解（B3/B4/M1/M2/M3/N5）

#### §3.5.1 键名表（N5，`/routing main|sub <key> <value>` 与面板共用的唯一键空间）

| 作用域 | 键 | 取值/说明 |
| --- | --- | --- |
| main | `enabled` | true/false |
| main | `levels` | 逗号分隔档位集合；归一化后必须非空（enabled=true 时） |
| main | `allow_expert` | true/false；levels 含 expert 时必须 true |
| main | `default_difficulty` | easy\|normal\|hard\|expert；必须 ∈ levels |
| main | `cost_guard_mode` | **soft\|hard**（无 off，M1） |
| main | `max_consecutive_expensive_steps` | 整数；0=不限；allow_expert=true 时必须 >0 |
| main | `expensive_levels` | 逗号分隔；越界项被忽略并 warning |
| main | `max_invalid_reports_per_turn` | ≥0；**0=未设置**（M3） |
| main | `downgrade_confirm_steps` | ≥1；**0=未设置**（M3） |
| main | `min_dwell_steps` | ≥0；**0=未设置**（M3） |
| main | `health_gate.respect_provider_health` | true/false（M2；其余 health_gate 键不开放） |
| main | `profiles.<level>.<field>` | field ∈ provider\|model\|reasoning_effort\|thinking_effort\|max_tokens\|timeout\|temperature\|availability\|availability_reason\|prompt_cache（`candidates` 首版只读） |
| sub | `enabled` | true/false |
| sub | `default_difficulty` | easy\|normal\|hard\|expert |
| sub | `levels.<level>.<field>` | 同 profile field 集合 |

#### §3.5.2 合并 → 校验 → 回退（B4 消解阶梯）

```
baseline(配置层，或内置默认 off)
  → 叠加工作区字段（字段级）
  → 叠加会话字段（字段级）
  → 应用默认值（ApplyMainAgentRoutingDefaults 语义，含显式 0 规则 §3.5.3）
  → 校验（主：ValidateMainAgentRoutingConfig 口径；子：modelrouting 校验口径）
  → 失败：按"覆盖层由近及远"逐字段回退（session → workspace → 保持继承值），
         每轮回退后重校验；最多 3 轮；每轮回退产出 RoutingWarning{字段, 原因, 最终来源}
  → 仍失败：拒绝该次写入（UI 明示错误与建议动作），不落盘
```

- 回退顺序**确定性**：按 §3.5.1 键名表的固定顺序逐字段处理，禁止依赖 map 迭代顺序。
- 回退单位：标量=该字段；profile 覆盖=该 profile 的单个字段；`candidates`=整链（如开放）。
- 典型场景（v1 U4/U5 的显式定义）：
  - 继承 `levels=[normal]` + 会话设 `default_difficulty=hard` → 回退会话字段，warning：`default_difficulty "hard" not in levels; reverted to config value`。
  - 会话设 `allow_expert=true` + 继承 `max_consecutive_expensive_steps=0` → 回退会话 `allow_expert`（不得篡改继承值），warning 说明原因。

#### §3.5.3 开启推导（B3）

当 `enabled=true` 且 `levels` 未显式设置（任何层）时，按以下顺序推导，并把推导结果与来源写入 `RoutingResolution.Sources`（展示文案"推导"）：

1. `levels = [easy, normal, hard]`；若当前 provider/model 能力可判定不支持某档，则剔除（能力不可得时保留全集，不猜测）。
2. `expert` 仅当 `allow_expert=true` **且** `max_consecutive_expensive_steps` 为有限值（未设置时建议取 6）时纳入；否则不纳入，并 warning。
3. `default_difficulty` 未设置 → `normal`（必须 ∈ levels）。
4. 若推导后仍无法通过校验（如无可用 provider 能力且 levels 为空）→ **拒绝开启**，提示：`请先配置 aicli.main_agent.routing.levels，或执行 /routing main levels easy,normal`。

> v1 缺陷复述：无全局配置时 `/routing on` 会合并出 `{enabled:true, levels:[]}` 并被 `main_agent_routing.go:147-149` 拒绝；本节的推导是 S1 主用例的前置条件。

#### §3.5.4 显式 0 与硬规则（M3/M2）

- `min_dwell_steps` / `max_invalid_reports_per_turn` / `downgrade_confirm_steps`：**0 等同未设置**（与 `ApplyMainAgentRoutingDefaults` `:99-107` 一致）；UI 候选不提供 0；会话覆盖显式写 0 视为未设置并 warning。
- `max_consecutive_expensive_steps`：**0=不限**（唯一例外，`:92-93`）；仅当 `allow_expert=false` 时允许；UI 仅在此时提供"不限(0)"。
- `cost_guard_mode`：仅 `soft|hard`（`:188-192`）；"关闭护栏"的正确表达是关闭整个路由（`enabled=false`）。
- `health_gate.latch_scope`（固定 `turn`）、`health_gate.on_chain_exhausted`（固定 `baseline`）、`health_gate.honor_min_dwell`（无条件 true）为硬规则，**覆盖层与 UI 均不开放**（`:193-200`、`:123`）。

## §4 生效路径与 runtime API（B1/B2/B5/M6/M8/N3/N4）

> 本节回答两件事：(a) 解析结果在哪些**接线点**进入 loop 配置；(b) 会话级写入如何**在下一 turn 生效**。骨架是「actor 构建期读取 + 写后失效重建」——因为 `buildSessionLoopConfig`（`session_runtime_support.go:3933`）只在 actor 构建时执行一次，`NewReActLoop` 对路由配置做深拷贝冻结（`main_agent_route.go:954-957`、`main_agent_route_test.go:78-89`），**配置与 override 都不会热生效到已建 actor**（M6 事实）。

### §4.1 统一解析器（B1/B2/B5/M8）

新增 `backend/internal/agentconfig/routing_resolution.go`（纯函数，无 IO、无全局状态）：

```go
// 五层解析：请求 > 会话 > 工作区 > 全局 > 内置默认（§3.1）
func ResolveMainAgentRouting(
    cfg *Config,
    sessionOverride *AICLISessionRoutingOverride,
    workspace *AICLIWorkspaceRoutingPreferences,
    request *AICLIRequestRoutingOverride,
) RoutingResolution

func ResolveSubagentRouting(
    cfg *Config,
    sessionOverride *AICLISessionRoutingOverride,
    workspace *AICLIWorkspaceRoutingPreferences,
    request *AICLIRequestRoutingOverride,
) RoutingResolution
```

- `RoutingResolution`（§3.1 已定义）**恒非 nil**（B5）：关闭态 `Effective == nil`，其余字段仍填充 `Sources`/`Warnings`/`Disabled`。
- **快路径（M8，REG 前提）**：当且仅当四层都没有任何生效字段时，直接返回 `EffectiveMainAgentRoutingConfig(cfg)` 的**同一指针**（`main_agent_routing.go:83-88`），`Sources` 全为 `config|default`。调用方可用 `res.Effective == EffectiveMainAgentRoutingConfig(cfg)` 判定「零覆盖」，既有指针同一断言（`chat_main_agent_routing_test.go:38-40`）与 gate 测试（`main_agent_routing_wiring_test.go:67-`）无需改动。
- 深拷贝边界：一旦发生任何覆盖合并，解析器产出**新对象**（profile map 深拷贝），绝不原地改写 `cfg`（对照 `cloneMainAgentRoutingConfig` `main_agent_route.go:957-` 语义）。
- 合并与校验严格按 §3.5.2 阶梯；`Warnings` 携带每轮回退的 `{字段, 原因, 最终来源}`。
- 子 Agent 解析复用同一骨架，`levels`/`task_types`/`roles` 同源覆盖（`config.go:702-707`）；team 先取 `EffectiveTeamRoutingConfig`（`config.go:659-670`）再叠覆盖。

### §4.2 aicli 宿主接线（B1/N3）

- 主 Agent：`applyLocalChatMainAgentRouting`（`chat_actor_host.go:2297` 附近）改为「先解析、后接线」：

```go
res := agentconfig.ResolveMainAgentRouting(cfg, sessionOverride, workspacePrefs, requestOverride)
if isMain && res.Effective != nil {
    base.MainAgentRouting = res.Effective   // 快路径时指针与原实现完全一致
}
```

- 子 Agent：`localChatSubagentRoutingConfig` 的读取链固定为 **session > workspace > config**（供 scheduler / modelrouting 消费）；team 回落 `EffectiveTeamRoutingConfig` 不变（`config.go:659-670`）。
- 门禁同口径：带 `agent_type` / `depth>0` / `read_only` 任一标记的会话是子 Agent，主路由开关不得改变其行为（`session_runtime_support.go:3934-3938` 注释与 `:3938` 判据）；**非主会话的写入类命令被拒绝**（M16）。
- `enable_routing` 不改（非目标 2）：本方案的开关是 `aicli.main_agent.routing.enabled` 与子 Agent 路由自身的 `enabled`，与 `types/runtime/chat.ts:16` 的请求级 `enable_routing` 无关。

### §4.3 runtime server 接线（N3/M6）

- 接线点：`session_runtime_support.go:3933` `buildSessionLoopConfig(selectedConfig, requestedReasoningEffort)` 之后，`:3937` 的
  `applyAPISessionMainAgentRouting(loopConfig, h.mainAgentRoutingConfig(), strings.TrimSpace(childAgentType) == "" && childDepth == 0 && !childReadOnly)`（第三参为内联表达式，N3）改为
  `applyAPISessionMainAgentRouting(loopConfig, h.resolveMainAgentRoutingForSession(runtimeSession), strings.TrimSpace(childAgentType) == "" && childDepth == 0 && !childReadOnly)`。
- 新 helper `resolveMainAgentRoutingForSession(runtimeSession)`：读取顺序 = 运行时会话 context 的 `aicli_routing_override`（§3.4 键）→ 工作区偏好 → `h.aicliConfigSnapshot()`（`handler.go:476-482`；该快照已对 `MainAgent.Routing` 做深拷贝，`handler.go:526-533`）。
- 读取时机 = **actor 构建期**（hub factory 内）：这是唯一能保证「下一 turn 生效」的读取点，因为 loopConfig 在构建后即冻结（`main_agent_route.go:954-957`）。对照实现：子会话 `requestedModel` 等同样在构建期从 runtime session context 读取（`chat_actor_host.go:1350-1375`）。
- 子会话门禁保持 `:3938` 原判据不变；主路由对子会话恒不接线。

### §4.4 runtime API 端点（N4/M4）

| 方法/路径 | 语义 | 复用 |
| --- | --- | --- |
| `GET /api/runtime/sessions/{id}/routing` | 只读投影：Effective + Sources + Warnings + Disabled + revision，附加面板元数据（各层徽标/可写性/目标文件路径，§5.4） | 与 TUI `/routing show`、状态栏、frontend 会话详情区块**同一投影函数**（§6） |
| `PATCH /api/runtime/sessions/{id}/routing` | 写入/清除目标层（`target_layer: session\|workspace\|config`，缺省 session；body = §3.2 patch 结构） | 校验器与配置文件同源（§3.5）；session 层写后失效 actor；workspace/config 层经服务端层路由写文件（§5.4/§7.3） |

- 写入路径：校验（§3.5 阶梯）→ 串行化「读-改-写」（§3.4 M11）→ `session.SetContext(aicli_routing_override, json)`（对照既有 context 写入范式 `handler.go:3090-3101`、`chat/manager.go:503`）→ `sessionManager.Update` → **失效 actor**。
- **层路由（v3）**：`target_layer=session` → 走上一行 context 路径；`=workspace` → 以会话绑定 workspace 路径写 `chat-prefs.yaml` 的 `routing` 节（§5.4/N9）；`=config` → 服务端按 `WritableLayer` 路由（`./.aicli/config.yaml` 存在→项目层，否则 `~/.aicli/config.yaml`），写前 `ValidateMainAgentRoutingConfig` + 二次确认，响应回传 `target_path`；workspace/config 层写后同样失效相关 actor（下一次 turn 重建时重读，§4.5）。
- 失效方式：hub `StopContext`（`chat/hub.go:302` → `chat/actor.go:342`），与 `/model` 切换后的刷新路径同源（`chat_actor_host.go:1296-1323`：`setChatActorWarmup(nil)` → `SessionHub.StopContext` → provider reload → `startChatActorWarmup`）。失效后下一次 prompt 重建 actor 并重读 override。
- 鉴权（M4）：**逐 handler 复刻既有检查**（`handler.go:7723` `hasValidSearchAdminToken` / `:7726` `hasTrustedAdminRole` / `isLoopbackRequest`；token 注入 `SetAdminToken` `:630`；会话族端点注册 `:891-927`，无 `Use(middleware...)` 路由级中间件）。分级：GET 与既有只读端点同级；PATCH 与既有会话写端点同级（本机回环或 admin token），P1 验收必须包含「未授权写入被拒」用例；**禁止**通过通用 `PATCH /sessions/{id}` 的 `context` 字段绕过校验写入该键（处理方式见 §4.5）。
- 子会话：写入返回 409（`session is a child agent`），只读 GET 仍可用（显示继承结果）。
- 错误码：校验失败 400 + `errors.ErrValidationFailed`（`handler.go:3107` 同族）；并发冲突按 §3.4 后写胜出 + warning；actor 失效失败不回滚写入，响应携带 `actor_invalidated=false`。

### §4.5 生效时机、失效与回退

- 时机：写入成功 → 失效 actor → **下一次 turn** 重建并生效；进行中的 turn 不受影响（turn 边界语义）。
- 若 hub 未持有 actor（冷会话）：无需失效，下次构建自然读取新 override。
- 通用 `PATCH /sessions/{id}` 的 `context` 兼容处理（N4）：对 `aicli_routing_override` 键，二选一——(a) 拒绝并提示使用专用端点（推荐，首版）；(b) 内部转调专用校验器。**禁止**裸 `SetContext` 直写（会绕过校验与失效，产生「配置已改但路由没变」的幽灵态）。
- 请求级 override（§3.1 第 5 层）仅本 turn 有效、不落盘、优先级最高；与请求体 `enable_routing`（`types/runtime/chat.ts:16`）无交集——后者语义不变（非目标 2，B1）。
- 清除：PATCH 携带 `{"clear": true}`（或空 patch）→ 删除 context 键 → 失效；解析回到下层的 Effective 或 `nil`（关闭态零行为变化，`main_agent_routing_wiring_test.go:67-` 口径）。
- 配置快照热重载不参与（M6）：本方案不依赖 `SetAICLIConfig` 热更新改变已建 actor；`resolveMainAgentRoutingForSession` 每次构建都重新读快照，因此**新建 actor** 总是拿到最新配置。

## §5 逐级路由 UI（v2 核心新增，TUI 优先）

> **缺口定义**：数据模型**已经**具备逐级 `provider/model/reasoning_effort` 三元组——主 Agent `aicli.main_agent.routing.profiles.<level>`（`main_agent_routing.go:79`；profile 结构 `config.go:720-738` 含 provider/model/reasoning_effort/thinking_effort/max_tokens/timeout/temperature/availability/candidates）、子 Agent `aicli.subagents.routing.levels.<level>`（`config.go:702`）——但**没有任何 UI 暴露它们**：TUI 现有 `/model` `/provider` `/reasoning` 只改会话单值，`/agents routing` 是只读摘要（`chat_debug.go:80-123`、`:746-790`）。本节定义 TUI 主路径 UI；web 对等见 §7，只读投影见 §6。

### §5.1 设计原则

1. **TUI 一等公民**：逐级编辑能力在 aicli TUI 内可完整完成（键盘可达、无鼠标依赖），复用既有全屏选择器交互（`chat_picker_common.go:115-141`、`ui/fullscreen_list.go:30-56`）。
2. **同一键空间**：面板与命令行走 §3.5.1 的键名表（`profiles.<level>.<field>` / `levels.<level>.<field>`）；面板只是键空间的图形化视图，不存在「只能面板改」的字段。
3. **写前必校验**：任何写入（session/workspace/config）先过 §3.5 阶梯校验，失败不落盘（与配置 fail-fast 同源：`main_agent_routing.go:132-162`）。
4. **来源可见**：每个字段显示当前值 + 来源（session/workspace/config/default/推导），不显示「无来源的假值」（§3.1 `Sources`）。
5. **不破坏既有交互（M14）**：`/model` `/provider` `/reasoning` 保持原语义（会话单值）；`/debug routing`（`chat_slash_command_catalog.go:115`，语义=子 Agent / Team routing 摘要）与 `/agents routing`（`:146`、`:176`；`chat_debug.go:258-365`、`:719-742`）**原语义不动**，仅在输出末尾追加提示「主 Agent 路由编辑请用 `/routing`」；主 Agent 路由只读视图由 `/routing show` / `/routing doctor` 自建渲染（复用 §6.1 投影），或在 `/debug` 下新增独立 token（如 `main-routing`）。

### §5.2 命令面（非交互 + 入口）

| 命令 | 行为 |
| --- | --- |
| `/routing` | 打开交互面板（§5.3），默认 `main` 作用域 + `session` 写入层；非主会话只读打开 |
| `/routing main [<level>]` / `/routing sub [<level>]` | 打开面板并直达主/子 Agent、可选直达某级 |
| `/routing show [main\|sub] [--json]` | 只读摘要：逐级 provider/model/effort + 来源 + warnings（与 §4.4 GET 投影同源） |
| `/routing doctor [main\|sub]` | 诊断视图：逐级解析结果 + 来源 + warnings + 回退阶梯记录（自建渲染，**不复用** `/debug routing`，M14） |
| `/routing main <key> <value>` | §3.5.1 键空间的非交互写入（如 `/routing main profiles.hard.model claude-x`、`/routing main enabled true`） |
| `/routing sub <key> <value>` | 同上（如 `/routing sub levels.expert.reasoning_effort high`） |
| `/routing on [main\|sub]` / `/routing off [main\|sub]` | 糖语法：等价 `enabled true/false`（G1 的 `/routing on\|off`） |
| `/routing main level <level> <field> <value>` | 糖语法（等价 `profiles.<level>.<field>`），面向日常使用 |
| `/routing save [session\|workspace\|config]` | 把面板草稿落到显式目标层（缺省 session；config 层写入按层路由 + 二次确认，见 §5.4） |
| `/routing reset [main\|sub] [<level>] [session\|workspace]` | 清除显式目标层覆盖（缺省 session） |

- 注册方式：走结构化命令 spec（`chat_slash_command_catalog.go:19-36`，带参范式见 `:104-119`），并在结构化白名单登记（`chat_command_result.go:390-405`）；legacy 分派（`command.go:326-331`）不新增分支。
- **两条正交轴（M12 消解）**：作用域轴 = `main|sub`（agent 种类）；写入层轴 = `session|workspace|config`（config 按层路由，§5.4）。写入类子命令用 `--to session|workspace|config` 指定落点（缺省 session），`save/reset` 用位置参数；**不存在**混用两轴的 `scope` 概念。影响矩阵见 §5.4。
- **建议值引擎（原需求「给出子命令建议值」）**：
  - level 候选：主 Agent 取 `Effective.Levels`（`main_agent_routing.go:66`）+ 内置四档；子 Agent 取 `levels` map 键集合（`config.go:702`）+ 内置四档（归一化口径 `config.go:1602-1620`），未启用档标注 `(未启用)`；
  - provider 候选：已配置 provider 列表（候选构建范式 `chat_model_command.go:820`）；
  - model 候选：所选 provider 的 catalog（沿用 `/model` 选择器数据源 `chat_model_picker.go:265-280`）；
  - reasoning_effort 候选：**按该级已选 model** 用 `reasoningEffortCatalogForModel`（`chat_reasoning.go:29-50`）计算并过滤（`chat_reasoning_command.go:173-183` 同逻辑），不支持的值不出现，并提示「该 model 不支持」；
  - 补全：命令行长按 Tab 逐段补全（作用域 → 键 → 值）；非法值给「最近似候选」。
- **渲染形态（v3.1）**：仅「打开面板」一族命令走**独立全屏渲染**——`/routing`、`/routing main|sub [<level>]`、键路径直达（如 `/routing main profiles.hard.model`），三者进入**同一块**全屏面板；其余子命令（`show`/`doctor`/`<key> <value>`/`on|off`/`level` 糖/`save`/`reset`）均为行内文本输出。细则与边界见 §5.3.1。

### §5.3 交互面板：Level → 字段 → 值 三级导航

复用全屏选择器基建（`chatPickerStage`/`chatPickerStageResult` `chat_picker_common.go:115-141`、`chatPickerOpen/Close` `:155-181`、lease hooks `:146-149`），`ui.FullScreenList`（`ui/fullscreen_list.go:30-56`）承载列表与预览：

```
┌ 路由面板  main · 写入层:[session] workspace  config      ← Tab 切换层
│ ▸ easy     deepseek-v3 / deepseek / —          (config)
│   normal   claude-sonnet-4 / anthropic / high  (session)  ← 覆盖徽标
│   hard     claude-opus-4 / anthropic / xhigh   (config)
│   expert   —（未配置，继承 baseline）            (default)
└ Enter=编辑  s=保存  r=重置  Esc=关闭
```

- 三级导航：**Level 列表** → **字段列表**（provider / model / reasoning_effort / thinking_effort / max_tokens / temperature / availability[只读] / candidates[只读]）→ **值选择器**（provider 列表 / model 列表 / reasoning 目录 / 数值输入 / 枚举）。
- 预览面板（`PreviewForItem`）：显示「当前生效值 + 来源 + 修改后将变成什么」，文本摘要复用 `chat_debug.go:80-123` 的渲染范式。
- 提交：`Enter` 确认 → §3.5 校验 → 写入所选层 → 触发 §4.5 失效 → 状态栏即时刷新（§6）。
- 草稿语义：面板内修改先在内存草稿，`Esc` 放弃；`s` 才落盘，支持「多层对比后再保存」。
- 直达：`/routing main expert` 直接打开 expert 级字段列表；`/routing main profiles.hard.model` 直达值选择器。
- 冲突：提交携带读到的 revision（§3.4 `UpdatedAt`/`UpdatedBy` 投影），不匹配则提示「配置已被其他端修改」并给重载/覆盖选项。

#### §5.3.1 渲染形态：独立全屏 vs 行内文本（v3.1）

- **独立全屏（同一面板）**：`/routing`、`/routing main [<level>]` / `/routing sub [<level>]`、键路径直达值选择器（如 `/routing main profiles.hard.model`，§5.2）三种入口进入**同一个**全屏面板；面板内 Level → 字段 → 值三级导航与预览面板是同一全屏界面内的 stage 切换，**不是**多个独立屏幕。
- **行内文本（不占独立屏幕）**：`/routing show`、`/routing doctor`、`/routing main|sub <key> <value>`、`/routing on|off`、`/routing main level <level> <field> <value>`、`/routing save`、`/routing reset`（§5.2 表）；输出汇入状态行/文本投影（§6.2/§6.3）。
- **边界**：frontend 路由区块是右侧栏「会话详情」面内区块——与面板同构但**非全屏**（§7.2）；micro web client 状态栏端点（`/web/api/statusbar`）既非全屏、也非 frontend 数据源（§6.4）。
- **复用基建不变**：全屏面板复用 `chatPickerOpen`/`chatPickerStage`/`chatPickerStageResult`/`chatPickerClose`（`chat_picker_common.go:115-141`、`:155-181`、lease hooks `:146-149`）与 `ui.FullScreenList`（`ui/fullscreen_list.go:30-56`），不新增独立渲染器。

### §5.4 写入层选择器（session / workspace / config 可写；config 按层路由）

| 层 | 落盘位置 | 生效范围 | 证据 |
| --- | --- | --- | --- |
| session | 会话记录 context `aicli_routing_override`（物理落 `~/.aicli/sessions/`：文件后端 `YYYY/MM/DD/<id>.json`；运行时后端 `sessions/runtime/session_runtime.sqlite`） | 当前会话，直到清除或会话删除 | §3.4 核实结论；`sessionmeta.go:71-95` 同族键、`chat_session.go:1334-1348` |
| workspace | `$HOME/.aicli/workspace/<projectIDForPath(workspacePath)>/chat-prefs.yaml` 的 `routing` 节（**单数目录**；16 hex） | 该工作区所有会话（默认继承） | `chat_persistence_workspace.go:29-34`、`:83-93`、`:170-180`、`:184-221` |
| config（**可写**，按层路由） | `./.aicli/config.yaml`（项目层，存在即写它）或 `$HOME/.aicli/config.yaml`（用户层，缺省落点） | 全局（所有工作区） | `config_layers.go:118-155`（`WritableLayer`/`RuntimeConfigWriteTarget`）、`config_write_route.go:32-47`、`:130-142` |

- 面板顶部 Tab 切换层（session / workspace / config 均可写）；每层显示徽标（已有覆盖 / 未设置）与目标文件路径。**config 层写入规则（v3）**：目标文件 = 最高「存在且可写」层——`./.aicli/config.yaml` 存在→项目层；否则→用户层 `~/.aicli/config.yaml`（即工作区没有 `.aicli` 配置时写全局；判定按**文件存在性**，`config_layers.go:113`、`:186-188` 用 `fileExists`）；写入前必须跑 `ValidateMainAgentRoutingConfig`（`main_agent_routing.go:132-162`）+ 二次确认（显示目标路径与「影响所有工作区」范围），写后按 §4.5 失效。
- 三层的优先级与字段级合并严格按 §3.1/§3.5.2；UI 只负责选择落点，不改变优先级。
- session 层是默认层（最安全、可回退）；`reset` 的逐层回退语义：清除 session → 看到 workspace 值，清除 workspace → 看到 config 值（面板即时演示该阶梯）。
- **影响矩阵（M12）**：`--to` / `save|reset` 的位置参数只决定写入落点，不改变解析优先级；未带目标的 `set/enable/disable/unset` 默认写 session；面板顶部 Tab 等价于全局 `--to`。
- **工作区路径（N9）**：workspace 层以**会话创建时记录的 workspace 路径**（`sessionmeta.WorkspacePath`；`handler.go:2679` 绑定）计算 `projectIDForPath`（sha256 前 8 字节→16 hex，`chat_persistence_workspace.go:170-180`），不随当前 shell 的 cwd 漂移；写入前后回显目标文件路径；**不得使用 `workspace_directories.yaml` 的 12-hex registry id**（§3.4 核实结论）。

### §5.5 与会话单值命令的协同（不冲突）

- `/model` `/provider` `/reasoning` 继续表示「**baseline 三元组**」（当前会话的基础模型）；路由启用时作为各档未配置时的基线（§3.5.2 baseline 语义）。
- 面板中每级显示 `继承 baseline` 状态；`/model` 变更后路由快照刷新（`chat_reasoning_command.go:206-212` 的刷新范式），面板与状态栏同步更新。
- 冲突处理：档位显式配置 > baseline；面板把 baseline 显示为只读行（如 `baseline: claude-sonnet-4/high`），避免用户误解。

### §5.6 子 Agent（sub）作用域

- `/routing sub` 编辑 `aicli.subagents.routing.levels.<level>`（`config.go:702`），字段集合同 §3.5.1；team 专属覆盖（`aicli.teams.routing`）首版**只读展示**（`config.go:653-670`）。
- `task_types`/`roles` 覆盖表首版只读（键空间大、歧义多），面板显示其存在与优先级（`config.go:703-707`），编辑列入后续迭代（§11 待确认项）。
- 子会话中的 `/routing` 面板**只读打开**、写入类命令被拒绝（M16）：子 Agent 的路由由父会话的 `sub` 作用域在 spawn 时解析，且子 Agent 侧的路由在构造期一次解析完成（`config.go:693-695` 口径），不引入请求级改道。

### §5.7 web 对等（指向 §7）

- web 面板与 TUI 同构：逐级表格（level × provider/model/effort）+ 层切换 + 校验错误回显，复用同一 GET/PATCH 端点（§4.4）与投影函数（§6）；展示位置＝右侧栏「会话详情」面内路由区块（v3 定稿，§7.2）。

## §6 可见性：状态栏与只读投影（TUI / web）

### §6.1 单一投影函数（防四处漂移）

新增 `chatRoutingStatusProjection(session) RoutingStatusProjection`（`backend/cmd/aicli/commands`），字段：

```go
type RoutingStatusProjection struct {
    Enabled   bool
    Level     string   // 当前 turn 生效档位（未判定时为空）
    Provider  string
    Model     string
    Reasoning string
    Source    string   // session | workspace | config | default | derived
    Disabled  bool
    Warnings  []string
    Revision  string   // §3.4 UpdatedAt/UpdatedBy 投影
}
```

所有展示面（TUI 状态栏段、`/routing show`、`/routing doctor`、micro web client 状态栏快照、frontend 会话详情路由区块、GET `/routing`）**只消费该投影**，禁止各自拼装字段；runtime server 侧以同名 snake_case schema（`schema_version: 1`）序列化，TUI 与 web 字段一一对应。

### §6.2 TUI 状态栏段

- 接线点（B6）：`buildChatSurfaceStatusSegments`（`chat_interaction.go:2507-2546`）中插入 `chatSurfaceRoutingStatusSegment`，位置在 provider 段（`:2539`）之后、balance 段（`:2542`）之前；**必须同步修改索引/优先级相关代码与断言**：`statusline.go:12-24` 的 `StatusSegmentKind` 增加第 9 种（现有 8 种）、段装配顺序（`:2535-2546`）更新、按序索引的既有断言（如 `chat_balance_refresh_test.go:397`）列入 REG 变更清单（§10.3）；宽度自适应沿用 `fitChatSurfaceStatusSegments`（`:2329`）。
- 内容：`route:hard · claude-opus-4 · xhigh · *`（后缀 `*`=session 覆盖，`~`=workspace，无后缀=config/default）；关闭态显示 `route:off`；未判定档位时只显示 baseline（`route:-`）。
- 可见性（v3.1 实现口径，闭环 §11 U-4）：仅当**任一层显式配置了路由**（会话覆盖 / workspace 偏好 / 全局配置 `aicli.main_agent.routing`）时才显示该段；四层都没有任何配置时不显示——保持「默认关闭零行为变化」（§8.1），并避免默认会话在窄终端白占宽度（B6 索引修订之外的既有状态栏断言因此可原样保持全绿）。「配置了但关闭」仍显示 `route:off`（用户要能看出路由被关掉）。
- 刷新时机：路由写入/清除后、`/model` `/reasoning` 变更后（复用 `chat_reasoning_command.go:206-208` 的 `Interaction.RefreshStatus("")` 范式）、turn 内难度判定落地后（`main_agent_route.go:601-` `applyPredictedDifficulty` 的落点，实现时确认刷新钩子）。
- 窄终端：由 fit 逻辑决定裁剪顺序，优先保留 model 与 routing 段（验收见 §10）。

### §6.3 只读文本投影

- `/routing show` / `/routing doctor`（§5.2）共用投影；既有 `/agents routing`（`chat_debug.go:80-123`、`:746-790`）**保持子 Agent / Team 摘要语义**（M14），仅追加一行提示「主 Agent 路由请用 `/routing`」。
- 内容：逐级表（level / provider / model / effort / source）+ `baseline:` 行 + warnings；`--json` 输出 §6.1 schema。
- `/status` 行表（`chat_status.go:93-105`；命令注册 `chat_slash_command_catalog.go:82-83`）新增 `Routing:` 行：`enabled/level/model/effort (source)`，关闭态 `off (source)`；与状态栏段、`/routing show` 同源（§6.1）。

### §6.4 micro web client 状态栏

- `/web/api/statusbar`（`web_statusbar.go:83-85`，注册 `pprof.go:345`；端点说明 `chat_debug_endpoints.go:94`）与 TUI 底部状态行同源（`web_statusbar.go:1-7` 文件头注释）；**消费方是 aicli micro web client，不是 `frontend/` workspace app**（M15：`frontend/src` 无 statusbar / cfg-bar 组件）。新增段自动进入 micro web client 快照，**无需新端点**。
- **frontend 不复用该端点**：`frontend/` 的展示位置＝右侧栏 `sessionDetail` 面内路由区块（§7.2），数据走 §4.4 专用端点；本端点保持只服务 aicli micro web client。
- 可见性（v3.1 实现口径，闭环 §11 U-4）：仅当任一层显式配置路由时才常显，零配置不显示；段内默认显示精简形式（level+model），完整三元组在 `/routing show` 与面板中查看。

### §6.5 runtime server 侧投影

- `GET /api/runtime/sessions/{id}/routing`（§4.4）返回 §6.1 同源字段 + `effective_from: "next_turn"` + `revision`，供 web 面板与外部客户端；TUI `/routing show --json` 与 web 面板共用同一 schema。

## §7 前端面板与事件契约

### §7.1 事件契约（新增 `session.routing_changed`；不复用 `model_changed`，M5/M7）

- **不复用 `model_changed`（M5）**：`frontend/` 全量检索 `model_changed` 为空——后端 `publishChatModelSelectionChanged`（`chat_model_command.go:411-432`；payload `provider/model/reasoning_effort/base_url` `:425-430`）**没有已证实的前端消费方**；v1「复用该事件」无依据，v2 删除该说法。
- **新增 `session.routing_changed`**：在 `backend/internal/events/contract.go` 注册（参考既有登记 `:105-108`，`Channels: ChannelSessionStore`），再重跑 `backend/cmd/contractgen` 重新生成前端投影（`frontend/src/types/runtime/event-contract.ts:1` 是生成物，**禁止直接编辑**，M7）；契约含 `session_id`、`routing`（§6.1 schema）、`effective_from: "next_turn"`、`revision`。
- 消费方：TUI 写入路径发布；micro web client 与 `frontend/` 各自决定是否订阅（首版：`frontend/` 面板保存后主动重拉 `GET /routing`，不依赖 SSE；SSE 作为可选增量）。
- **独立待确认项（M5 要求）**：模型/provider/effort 变更如何通知前端，现状可能根本不通知——列入 §11，不在本方案中假定已有通道。
- 语义：事件只是「失效信号 + 摘要」；客户端以 `GET /routing` 为权威（防止 payload 与投影漂移）；顺序=写入落盘 → 发布 → 失效 actor（§4.5）。

### §7.2 web 面板（frontend workspace）

- 位置（v3 定稿）：**右侧栏「会话详情」面（`sessionDetail`）内新增「路由」区块**——`frontend/src/components/workspace/panel-registry.ts:148-161` 注册的 `SessionDetailSurface`（`session-detail-surface.tsx:326`）中，与既有 `SessionDetailNetworkSection`、`SessionUsagePanel` 并列；字段分组沿用 `FIELD_GROUP_ORDER`（`:76-90`）。**不新建底部状态条**（§11 U-1 关闭）。形态与 §5.3 同构——level 表格 × provider/model/effort 三列 + 写入层切换（session/workspace/config 均可写；config 按层路由）+ 保存/重置 + 校验错误按字段回显（400 结构见 §4.4）。
- 只读快照：区块内展示路由摘要（`enabled/level/model/effort (source)`）与目标文件路径；编辑动作（保存/重置）走 §4.4 专用端点，**禁止**与通用 `PATCH /sessions/{id}` 的 `context` 混用（N4）。
- 数据源：会话基本信息走既有 `useSessionDetail` → `getRuntimeSession`（`use-session-detail.ts:48`；`api/runtime/sessions.ts:85-96`，`GET /api/runtime/sessions/{id}`，payload 含 `metadata.context`，`types/runtime/sessions.ts:5-17`）；路由快照走 `GET/PATCH /api/runtime/sessions/{id}/routing`（§4.4，同一投影函数）；子会话只读（禁用写控件并说明原因）。
- 命名：请求/响应字段沿用前端既有 snake_case 风格（对照 `types/runtime/chat.ts:14-17`）。

### §7.3 frontend workspace 的层写入与 config 写入（原需求④）

- **M10 决策（选 (a)）**：新增服务端工作区偏好读写路径——前端写 workspace 层时，PATCH 以该会话绑定的 workspace 路径（`sessionmeta.WorkspacePath`）定位 `chat-prefs.yaml` 的 `routing` 节（与 TUI 同一文件、同一键），才谈得上跨端一致；浏览器本地存储**不参与**后端解析优先级。首版不新增独立 workspace 管理端点（跨会话浏览/编辑列入 §11）；若实现成本超预期则退回 (b)：前端仅作浏览器本地默认值并明确标注不参与解析。
- **config 层写入（v3 修订）**：前端可写 config 层，目标文件由服务端按 `WritableLayer` 路由（`./.aicli/config.yaml` 存在→项目层；否则 `~/.aicli/config.yaml`，即工作区无 `.aicli` 配置时落全局），响应回传目标路径；必须二次确认（目标路径 + 「影响所有工作区」）+ 服务端写前 `ValidateMainAgentRoutingConfig`。
- 权限：与既有 workspace 配置写入同级别，不新增权限模型；config 层写入纳入同一鉴权（§4.4/M4）与二次确认。

### §7.4 一致性验收

- 四处渲染（TUI 状态栏段、`/routing show`、micro web client 状态栏快照、frontend 会话详情路由区块）必须来自同一投影函数（§6.1）；§10 增补「四处一致」用例：同一状态输入下逐字段相等。

## §8 兼容与迁移

### §8.1 兼容矩阵

| 维度 | 结论 | 依据 |
| --- | --- | --- |
| 默认关闭零行为变化 | 主 Agent `routing.enabled` 缺省 false（`main_agent_routing.go:64`）；子 Agent `enabled` 为 `*bool`（`config.go:674`）；解析器快路径保证无覆盖时指针同一（§4.1） | `main_agent_route.go:237-246`；`main_agent_routing_test.go:243-256` |
| 新增字段全部可选 | `AICLIChatConfig.Routing`（§3.3）与会话 override 均 `omitempty`；旧 `chat-prefs.yaml` / 旧会话无该键 → 空覆盖 | §3.2/§3.3 |
| 会话键新增 | 旧会话无 `aicli_routing_override` → 无覆盖；键名走 `sessionmeta` 常量（M9） | §3.4；`sessionmeta.go:71-95` |
| YAML 往返 | 新字段不改变既有 round-trip 断言；`candidates` 首版只读 | `main_agent_routing_test.go:278-325` |
| `roles` → `task_types` | 兼容别名与 deprecation warning 语义不变 | `config.go:703-705` |
| 请求级 `enable_routing` | 语义不变（非目标 2）；请求级 `routing` 仅本 turn（§4.5，N6） | `types/runtime/chat.ts:16` |
| 既有命令语义 | `/debug routing`、`/agents routing` 原语义不动（M14）；`/model` `/provider` `/reasoning` 不变 | §5.1；`chat_slash_command_catalog.go:115/146/176` |
| 既有测试 | 快路径保住指针同一断言（M8）；状态栏索引断言随 B6 显式修订并列入 REG 变更清单（§10.3） | `chat_main_agent_routing_test.go:38-40`；`chat_balance_refresh_test.go:397` |

### §8.2 迁移步骤（无破坏、逐层可停）

1. 落地 §4.1 解析器 + 快路径（纯新增文件），跑 REG——无 UI、无行为变化。
2. 落地 §4.2/§4.3 接线（仍无 UI；`apply*` 调用点等价替换）。
3. 落地 §3.4/§4.4 会话覆盖存储与 API（新键默认无值）。
4. 落地 §5 TUI 面板与 §6 状态栏段（B6 索引修订随此步完成）。
5. 落地 §7 web 面板与 `session.routing_changed`（含 contractgen 重跑）。

### §8.3 回滚策略

- 分层回滚：解析器/接线/API/UI 各自独立，可按阶段回退；关闭 `aicli.main_agent.routing.enabled=false` 即回到基线（INV-A1 零行为变化）。
- 数据回滚：清除会话 context 键、删除 `chat-prefs.yaml` 的 `routing` 节即可；**无 schema 迁移、无数据重写**。
- 紧急开关（列入 §11）：`AICLI_DISABLE_ROUTING_UI=1` 仅隐藏 UI 入口、保留解析与 API（灰度用）。

## §9 分阶段实施

| 阶段 | 内容 | 交付物 | 验收 |
| --- | --- | --- | --- |
| P0 解析器与接线 | §3 数据模型、§4.1 解析器、§4.2/§4.3 接线 | `routing_resolution.go` + 接线改造（无 UI） | REG 全绿；单测：快路径指针同一（M8）、深拷贝隔离、§3.5.2 阶梯回退 |
| P1 会话覆盖与 API | §3.4 存储、§4.4/§4.5 端点与失效、§7.1 事件（先发 bus） | GET/PATCH `/routing` + 失效路径 + `session.routing_changed` | U-1 写入→下一 turn 生效；U-2 并发写（M11）；未授权写入被拒（M4）；子会话写入 409（M16） |
| P2 TUI 面板 | §5 命令面 + 三级导航 + 建议值引擎 + §6.2 状态栏段（B6 索引修订） | `/routing` 全家 + 状态栏 routing 段 | I-1…I-5（§10.2） |
| P3 web 面板与投影一致 | §7.2/§7.3 面板 + §6.4/§7.4 一致性 | `frontend/` 会话详情路由区块 + micro web client 段 | I-6 四处一致；M10 决策 (a) 的 workspace 写入往返 |
| P4 打磨 | 窄终端裁剪、可见性开关、`--json`、迁移提示文案 | 文档化收尾 | I-7 窄终端；文案走查 |

- 依赖顺序：P0 → P1 → P2 → P3；P2 与 P3 可并行（共享同一 API 与投影）。
- 发布闸门：**P1 未完成不得发布 P2**（避免 UI 写出「不生效的层」）；P0 未完成不得发布 P1。
- 每阶段结束跑 §10.3 REG 清单；B6 索引修订与 P2 同步落地。

### §9.1 实施进度（落地记录，2026-09-22）

| 阶段 | 状态 | 落点（实现文件） | 验证 |
| --- | --- | --- | --- |
| P0 解析器与接线 | ✅ 已落地 | `backend/internal/agentconfig/routing_resolution.go`（恒非 nil 解析器 + §3.5.2 阶梯回退）、`backend/internal/agentconfig/main_agent_routing.go`、`backend/internal/agentconfig/routing_override_patch.go`、宿主读取链 `backend/cmd/aicli/commands/chat_actor_host.go:2315`（§4.2 `resolveLocalChatMainAgentRouting`） | `go test ./internal/agentconfig/ -run Routing`；`backend/cmd/aicli/commands/chat_main_agent_routing_test.go`（快路径指针同一 M8 / 深拷贝隔离） |
| P1 会话覆盖与 API | ✅ 已落地 | `backend/internal/api/skills/session_routing_handlers.go`（GET/PATCH `/routing` + 校验 + `actor_invalidated`）、`backend/internal/api/skills/routing_analytics_handlers.go`、`backend/internal/events/session_routing_events.go`（`session.routing_changed`）、`backend/internal/agentconfig/routing_config_write.go`（§3.4 存储） | `session_routing_handlers_test.go`、`main_agent_routing_wiring_test.go`、`routing_analytics_handlers_test.go`；REG 全绿 |
| P2 TUI 面板 | ✅ 已落地（本次增量） | 命令面 `backend/cmd/aicli/commands/chat_routing_command.go`（§5.2 全表 + 忙时队列策略 + `--to session|workspace|config` / `--yes`）；三层写入 `backend/cmd/aicli/commands/chat_routing_layers.go`（§5.4：workspace 以会话绑定工作区为准（N9）、config 需 `--yes` 且写后刷新进程内快照、写入成功后复用 `refreshLocalRuntimeAfterModelSelection` 失效重建本地 actor（§4.5/M6））；三级导航 `backend/cmd/aicli/commands/chat_routing_panel.go`（复用 `chatPickerOpen/Stage/Close` + `ui.FullScreenList`；lease hooks `OpenRoutingPanel`/`CloseRoutingPanel` 见 `backend/cmd/aicli/ui/action.go`、`app_state.go`、`controller_state.go`）；状态栏段 `chat_routing_status.go`（§6.2 可见性）；`/status` Routing 行 `chat_status.go`（§6.3）；micro web 快照 `web_statusbar.go`（§6.4） | `chat_routing_panel_test.go`（写入键映射/键路径直达/建议值集合/写入层选择项与层名一一对应/无全屏退化边界 I-1·I-11）、`chat_routing_command_test.go`（含 `TestChatRoutingCommandWriteLayerBoundaries`/`WritesWorkspaceLayer`/`ConfigLayerRequiresConfirm`/`SaveToWorkspaceLayer`/`WriteRefreshesLocalRuntime`）、`chat_routing_status_test.go`、子会话只读（M16/I-8，本次增量）：`chat_routing_command_test.go:TestChatRoutingCommandChildSessionReadOnly` / `TestChatRoutingCommandChildSessionBlocksWorkspaceSideEffects` |
| P3 web 面板与投影一致 | ✅ 已落地（本次增量） | 路由区块（非全屏）`frontend/src/components/workspace/session-detail-routing/`（`-section`/`-levels`/`-layers`/`-status`/`-actions`/`-shared` + `-shared.test.ts`/`-section.test.tsx`），挂载于 `session-detail-surface.tsx`（与 `SessionDetailNetworkSection`/`SessionUsagePanel` 并列）；数据层 `frontend/src/api/runtime/session-routing.ts`、`frontend/src/hooks/workspace/use-session-routing.ts`、`frontend/src/types/runtime/session-routing.ts`（逐字段对齐后端 JSON）；文案 `i18n/resources/{zh-CN,en-US}/workspace/panels-session-detail.ts`（各 +89 行）；事件契约 `frontend/src/types/runtime/event-contract.ts` 由 `go run ./cmd/contractgen` 生成（含 `session.routing_changed`） | 本次复跑：`npx vitest run src/api/runtime/session-routing.test.ts src/components/workspace/session-detail-routing/` → 3 files / 32 tests 全绿；`go run ./cmd/contractgen -check` → 生成物与注册表一致；子会话报告 `npm run lint:i18n`（violations=0）、`npx tsc -b`、`npx eslint <改动文件>` 均 exit 0 |
| P4 打磨 | ✅ 已落地（本次增量） | 灰度开关：`backend/cmd/aicli/commands/chat_routing_status.go`（`chatRoutingUIDisabled` + 状态栏段短路）、`chat_routing_command.go`（`/routing` 输出开关说明而非路由输出）、`chat_slash_command_catalog.go`（`chatSlashCommandCatalogHideRouting` 隐藏目录项与补全）；迁移提示（I-10）：`chat_debug_document.go`（`/debug routing` 追加 `Migration:` 元信息）、`chat_debug.go`（`/agents routing` 同款提示）；窄终端裁剪（I-7）：沿用状态栏「只丢尾部可选段」策略并补测试钉住 | `chat_routing_ui_switch_test.go`（关闭态隐藏 UI 入口 / 默认态对照 / 两处迁移提示）、`chat_routing_status_test.go:TestChatStatusFitKeepsModelAndRoutingOnNarrowTerminal`（I-7）；`go test ./cmd/aicli/commands/ -count=1` 全绿 |

- P2 已知边界（2026-09-22 收口）：`thinking_effort`/`max_tokens`/`temperature` 在本机没有目录候选，面板第三级改为**自由文本录入 + 就地校验**（`ui.FullScreenListOptions.FreeTextMode` / `chatPickerFreeTextStage`；字段准入 `chatRoutingPanelFieldUsesFreeText`、提示 `chatRoutingPanelFreeTextHint`、校验 `chatRoutingPanelPreviewValue` 复用 `chatRoutingApplyKey` 的字段解析与 `chatRoutingValidateCandidate` 的 §3.5.2 候选校验——解析层错误（数值/非空/未知字段）硬阻断，候选层给同源结论后允许继续），不再退化为「请改用 /routing 命令直接写入」的假入口；`availability`/`candidates` 仍为只读字段，不进面板字段列表。
- P2 验收对照：I-1（三级导航 + `Esc` 不落盘）按代码路径与退化边界单测钉住，交互路径本身依赖真实 TTY（与 `/model` 选择器同口径，不做伪 TTY 测试）；I-3 建议值引擎复用 `/model` 同源 catalog，且 `reasoning_effort` 按**该级生效 model** 过滤（档位 `model` → 档位 `provider` 默认模型 → 会话模型；无法确定或目录为空时给空候选、不回落会话候选，见 §9.1.1 P2-5）；I-5 状态栏段与 `/status` 行同源投影；I-11 渲染形态由「仅打开面板一族进全屏 + 忙时/非 TTY 退化为行内文本」共同保证。
- P4 验收对照：U-6 关闭态**只**隐藏 UI 入口（`/routing` 文本、命令目录、状态栏段），解析器与 `/api/runtime/sessions/{id}/routing` API 行为不变（单测钉住）；I-10 既有 `/debug routing`、`/agents routing` 语义逐字段不变，仅追加一行迁移提示；I-7 段序为 `model → provider → routing`（B6）且裁剪只丢尾部，窄终端下 model 与 routing 必然保留（单测钉住）。
- P3 验收对照：I-6 四处一致＝后端三处（状态栏段 / `/routing show` / micro web 快照）同源投影（`chat_routing_status_test.go:107-115` 等）＋ frontend 区块**直读**投影字段（`session-detail-routing-shared.ts` 只做集合运算，不重算 provider/model/effort/source）；§7.2 形态约束（会话详情面内区块、无全屏、无底部状态条）与 §7.3 写入契约（三层可写、config 二次确认 + `target_path` 回显、`writable_layers` 白名单置灰）均在组件与单测中体现。
- P3 已知边界（诚实记录）：`verify-max-lines.mjs` 与 `src/types/runtime/event-contract.test.ts`（2 个用例：`subagent.batch.canceled` 双通道、以及「前端派生名单与收敛前字面量等价」的冻结清单）在 **HEAD 上即为红灯**，与本次路由改动无关（已用 `git show HEAD:frontend/src/types/runtime/event-contract.ts` 与 `git status` 核对），按「变更清单之外不得修改既有断言」未处理，建议由契约维护方单独收敛。

#### §9.1.1 复审修复记录（2026-09-22，按复审顺序）

| 项 | 症状（修复前） | 修复落点 | 回归证据 |
| --- | --- | --- | --- |
| P0-1 前端数据破坏路径 | config 层二次确认期间草稿仍可编辑：确认时若补丁已被改回投影原值（空补丁 + `confirm=true`），后端按 §4.5「空补丁 = 清除该层覆盖」处理，会清掉全局 `routing` 节 | `frontend/src/components/workspace/session-detail-routing/session-detail-routing-section.tsx`（`runWrite` 复核 `plan.hasChanges`，空补丁只提示不发请求；换会话回到默认 `session` 层）；`session-detail-routing-levels.tsx`（`disabled` 置灰档位输入框） | `session-detail-routing-section.test.tsx`（12 例，含「确认期间改回原值不发请求」「子会话只读置灰」「保存中置灰」） |
| P1-1 后端快照漂移 | config 层写入只改文件、不刷新 handler 进程内快照 → PATCH 回显 / 后续 GET 仍按旧配置解析，主 Agent 接线（`mainAgentRoutingConfig` → `buildSessionActor`）要等进程重启 | `backend/internal/api/skills/session_routing_handlers.go`（`syncAICLIRoutingSnapshot`：与落盘同一 base + 同一补丁 → `SetAICLIConfig`）；`handler.go`（`cloneAICLIRoutingConfig` 保留「已接线但无 `aicli` 节」状态，S1 主用例：无全局配置时首次写入 config 层） | `session_routing_handlers_test.go:TestSessionRoutingConfigLayerWriteRefreshesSnapshot`（快照 + GET 投影双断言）、`main_agent_routing_wiring_test.go:TestCloneAICLIRoutingConfigKeepsWiredStateWithoutAICLISection` |
| P1-2 前端写入并发 | `use-session-routing.ts` 写入无 sid / 序号守卫：切会话后旧会话的写入回包会覆盖新会话投影，并作废新会话在途 GET；切会话不清 `snapshot`（新会话加载失败时界面继续显示旧会话数据）；写入失败静默吞错 | `frontend/src/hooks/workspace/use-session-routing.ts`（`sidRef` + `writeSeq` 判定，`stale` 结果静默丢弃；切会话即清投影 / 错误；失败如实回显） | `use-session-routing.test.tsx`（3 例：切会话清投影、在途写入跨会话丢弃、写入成功以服务端投影为准） |
| P1-3 前端只读态 | 不可写层 / 子会话 / 保存中档位输入框仍可编辑（给出「点了必然失败」或与在途写入交错的入口） | 同 P0-1 的 `disabled` 传递（`!layerWritable \|\| saving`） | 同上（置灰两例） |
| P1-5 TUI 状态栏误报 `route:off` | `chatRoutingStatusEngaged` 只看覆盖里是否残留 `updated_at/updated_by` → 空覆盖（仅清字段后）也显示 `route:off` | `backend/cmd/aicli/commands/chat_routing_status.go`（解码后判 `HasMainAgentFields() \|\| HasSubAgentFields()`） | `chat_routing_status_test.go` |
| 同批后端写入卫生 | ① PATCH 请求体无上限、未知字段静默忽略；② workspace 偏好读取失败被当作「无工作区覆盖」继续 → 跳过校验写出与现状不符的配置；③ `upsertRoutingSection` 的 `set=false` 会删除对应节（「只改 sub_agent / 只清一个字段」连带抹掉 `aicli.main_agent.routing`）；④ `applySessionRoutingOverride` 编码失败返回「已清除」= 静默保留旧覆盖 + 假成功；⑤ 投影不反映 workspace 偏好读取失败 | `session_routing_handlers.go`（`http.MaxBytesReader` 64KiB + `DisallowUnknownFields`；workspacePrefs 失败改 500；`writeConfigRoutingSection` 的 set/clear 语义重写；`applySessionRoutingOverride` 返回错误 → 500；warnings 追加）；`backend/internal/agentconfig/routing_config_write.go`、`routing_override_patch.go`（`ClearMainAgentRoutingConfigFields` / `IsEmptyMainAgentRoutingConfig`） | `session_routing_handlers_test.go`、`routing_resolution_test.go`、`go test ./internal/agentconfig/... ./internal/api/skills/...` |
| P2-1 TUI 写入层缺口（§10.2 I-2 的 TUI 部分） | `/routing set\|save\|reset` 只支持 `session` 层，`workspace\|config` 显式拒绝「尚未在此版本启用」；帮助面与 Tab 补全也只登记 `--to session` | 新增 `backend/cmd/aicli/commands/chat_routing_layers.go`（三层写入/清除 + 目标路径回显 + config `--yes` 二次确认 + 写后 `ReloadGlobalConfig` 刷新进程内快照）；`chat_routing_command.go` 接入 `--to`/`--yes` 并按层分发；`chat_slash_command_catalog.go`、`chat_slash_argument_completion.go` 同步帮助面 | `chat_routing_command_test.go`（`TestChatRoutingCommandWriteLayerBoundaries` / `WritesWorkspaceLayer` / `SaveToWorkspaceLayer` / `ConfigLayerRequiresConfirm`）PASS、`chat_routing_panel_test.go:TestChatRoutingPanelLayerItemsMatchNames`（写入层选择项与层名一一对应、config 档回显目标路径） |
| P2-2 TUI 写入不重建 actor（§4.5/M6） | 路由写入只刷新状态栏：`MainAgentRouting` 在 actor 构建期被 `cloneMainAgentRoutingConfig` 深拷贝冻结（`internal/agent/loop.go:203-208`），已缓存 actor 不感知新配置，「下一 turn 生效」实际要等 actor 被闲置淘汰 | `chat_routing_command.go`（`chatRoutingWithRuntimeRefresh` / `chatRoutingWithRuntimeRefreshText` / `chatRoutingRefreshRuntimeAfterWrite`：写入/清除/保存成功后复用 `/model` 一族的 `refreshLocalRuntimeAfterModelSelection`（`chat_actor_host.go:1324`）停 actor + 重新预热；失败只追加警告、不回滚落盘；无本地 host 时 no-op） | `chat_routing_command_test.go:TestChatRoutingCommandWriteRefreshesLocalRuntime`（预热重新向 hub 请求 actor、且无警告文案） |
| P2-3 TUI 子会话未拒绝写入（§10.2 I-8/INV-A3，M16 的 TUI 部分） | TUI 侧**没有**子会话判据：`chat_actor_host.go` 的 `applyLocalChatMainAgentRouting`/`isBaseSession` 分支只把主 Agent 路由接主会话，但命令面任一层写入照常落盘 → 子会话里 `/routing on` 显示「已写入…（下一 turn 生效）」却永不生效（API 侧 U-5 已 409，TUI 侧缺口）；且 `--to workspace\|config` 会产生真实副作用。可达性成立：`aicli chat --session <child>`、`/load`、`/resume`、ACP（`agent_stdio.go:680`）都能把任意子会话载入 TUI | 判据单点：`chat_routing_command.go` 新增 `chatRoutingSessionIsChildAgent` + `chatRoutingChildSessionReadOnlyNote`（与宿主接线/API **同源**：`agent_type` / `depth>0` / `read_only` 任一命中即子会话，对齐 `chat_actor_host.go:1379-1403` 与 `internal/api/skills/session_runtime_support.go:3938-3942`）；拦截点：`chatRoutingWriteKey`（含面板最终写入）、`chatRoutingResetFromArgs`、`chatRoutingSaveFromArgs`，一律**先拒绝后落盘**；`chatRoutingPanelEntry` 在子会话退化为只读摘要 + 提示，不进入可写选择器（不触碰 TTY） | `chat_routing_command_test.go:TestChatRoutingCommandChildSessionReadOnly`（三种子会话标记 × `/routing on\|off\|main enabled\|main level …\|main profiles…\|reset\|save` 全部拒绝、context 无落盘；`show`/`doctor` 仍可用；面板入口含只读提示）、`TestChatRoutingCommandChildSessionBlocksWorkspaceSideEffects`（`--to workspace` 被拒且工作区目录零新增文件；主会话不被误伤） |
| P2-5 reasoning 建议值未按档位 model 过滤（§10.2 I-3） | 面板第三级与 Tab 补全的 `reasoning_effort` 候选只按**会话级** provider/model 取目录（`chat_reasoning.go:29-50`）：档位写了 `model`（如 beta-model）时仍给会话模型（alpha）的候选，可能把该档位模型不支持的值写进 `profiles.<level>.reasoning_effort` | 档位感知包装（同源，不新造解析）：`backend/cmd/aicli/commands/chat_routing_status.go:70-80`（`chatRoutingLevelSummaryFor` 取行，行来自既有 `chatRoutingLevelSummaries`）、`chat_routing_panel.go`（`chatRoutingPanelValueOptionsForLevel` / `chatRoutingReasoningValueOptionsForLevel`：档位 `model` → 档位 `provider` 的 `DefaultModel` → 会话模型三级取值；provider 未注册 / 模型无法确定 / catalog 为空 → 空候选，**不**回落会话候选；非 reasoning 字段委托原 `chatRoutingPanelValueOptions`，行为不变）、`chat_routing_completion.go:232`（键路径 `profiles.<level>.reasoning_effort` 经 `chatRoutingLevelFromKey` 取档位；`level <level> <field> <value>` 糖同源）、面板第三级 `chat_routing_panel.go:331` | `chat_routing_panel_test.go:TestChatRoutingPanelReasoningOptionsFollowLevelModel`（hard=beta-model → 仅 `high`；normal 只写 provider → 取该 provider 默认模型目录 `minimal`；easy 未覆盖 → 回落会话候选；provider 无默认模型 → 空候选不回落）；`go test ./cmd/aicli/commands/ -count=1` 全绿 |
| U-2 / U-14 并发写与物理落点（§10.1，2026-09-22 增补） | ① 同一会话 override 的「读-改-写」在 TUI 与 API handler 两端各写各的：并发 PATCH 各自基于同一份旧快照落盘，后写者静默覆盖前者字段（M11 丢更新）；② workspace / config 两层同为「读-改-写」，TUI 与 API handler 共享同一对写入函数（`UpdateWorkspaceRoutingSection` / `UpdateAICLIRoutingSection`），两个会话绑定同一工作区也落到同一份 `chat-prefs.yaml`，同样没有跨写者串行化；③ U-14 的物理落点（会话 JSON / sqlite，且**不产生** `~/.aicli/workspaces`）此前无测试钉住 | ① 新增进程内会话写锁 `backend/internal/chat/session_write_lock.go`（`LockSessionWrite`：按会话 ID 注册、refs 计数 + 幂等释放、空 ID no-op）；API handler 的 session 分支**锁内重读**会话再合并（`reloadSessionForRoutingWrite`），TUI 的 `chatRoutingWriteKey` / `chatRoutingResetFromArgs` 取同一把锁（`chatRoutingWithSessionWriteLock` / `chatRoutingLockSessionWrite`），落盘即释放、runtime 重建不再占锁；② 新增路径级文件写锁 `backend/internal/agentconfig/config_file_write_lock.go`（`LockConfigFileWrite`：按目标文件路径注册、Windows 大小写归一），`UpdateWorkspaceRoutingSection` 与 `UpdateAICLIRoutingSection` 整段「读-改-写」入锁；同日第二次增补把同一份配置文件的**其余写点**（chat / theme / provider / provider 管理 / proxy / 层文档 / workspace prefs / starter 配置）全部并入同一把锁与共享写事务 `config_file_write.go`（`updateConfigFileDocument`），锁范围由「仅 routing」扩展为「本包全部配置写点」（§12「并发写串行化范围」已同步更新） | `session_routing_handlers_test.go`：`TestSessionRoutingConcurrentWritesDoNotLoseFields`（U-2：`racingRoutingStorage` 把丢更新窗口变成确定性顺序，两字段都必须保留）、`TestSessionRoutingSessionLayerPhysicalLanding`（U-14：文件后端 `sessions/YYYY/MM/DD/<id>.json` 的 `metadata.context.aicli_routing_override` + sqlite 读回 + `~/.aicli/workspaces` 不存在）；`config_file_write_lock_test.go`：`TestLockConfigFileWriteBlocksUntilReleased`、`TestConfigFileWritersHonorSharedWriteLock`（9 个写点逐一占锁 → 每个写入都必须等待）、`TestWorkspacePrefsWritersHonorSharedWriteLock`、`TestConcurrentConfigSectionWritersPreserveEverySection`（4 写者并发 ×8 轮，各节最后一次写入都在）、`TestUpdateWorkspaceRoutingSectionSerializesConcurrentWriters`、`TestUpdateAICLIRoutingSectionSerializesConcurrentWriters`。routing 两个并发用例 + 本轮统一用例均经「临时摘锁 → 必须 FAIL → 还原复跑 ok」的证伪验证（摘 `UpdateAICLIChatPreferences` 的锁 → `TestConfigFileWritersHonorSharedWriteLock/chat` FAIL）；`go test -race ./internal/agentconfig/` 亦通过 |

- **TUI 写入层缺口（已闭环，2026-09-22 增补）**：TUI `/routing set|save|reset` 原仅支持 `session` 层、对 `workspace|config` 显式拒绝；本次增量已补齐三层写入（`backend/cmd/aicli/commands/chat_routing_layers.go`；`chat_routing_command.go` 的 `--to <layer>` / `--yes|-y` 解析与 `chatRoutingWriteKey` / `chatRoutingResetFromArgs` / `chatRoutingSaveFromArgs` 按层分发），全屏面板亦提供写入层选择器并回显目标路径（`chat_routing_panel.go:309-360`）。config 层与 API `writeConfigRoutingSection` 同源（Materialize → Validate → `UpdateAICLIRoutingSection` → `ReloadGlobalConfig` 刷新快照），未 `--yes` 时拒绝且不落盘；workspace 层以**会话绑定 workspace 路径**为准（N9），不随 cwd 漂移；`save` 只做字段级合并、**不自动清除会话层**（需显式 `/routing reset --to session` 回落）。未知层报 `未知写入层 ...（可用 session|workspace|config）`，无绑定工作区报 `未绑定工作区 ...`。§10.2 I-2 的 TUI 部分据此闭环。
- **写入后失效重建（TUI 侧，如实登记）**：本地 actor 在构建期把路由配置深拷贝进 loop（`internal/agent/loop.go:203-208`），因此任何一层写入成功后都会复用 `/model` 一族的 `refreshLocalRuntimeAfterModelSelection`（停 actor + 重新预热）——这是「下一 turn 生效」成立的前提。刷新失败不回滚已落盘写入，只在结果文本追加「警告: ...（旧 actor 可能仍用旧配置）」。无本地 runtime host（结构化/无头调用）时该步骤静默 no-op；未持久化的新会话无缓存 actor，预热按既有语义跳过（`chat_actor_warmup.go:20-32`）。
- **子会话只读（M16 / I-8 / INV-A3 的 TUI 部分，2026-09-22 增补闭环）**：宿主接线只把主 Agent 路由接主会话（`chat_actor_host.go` 的 `isBaseSession` 分支，第三参即 `internal/api/skills/session_runtime_support.go:3938-3942` 的 child 标记），而 TUI 能载入任意 session id（`aicli chat --session <id>`、`/load`、`/resume`、ACP `agent_stdio.go:680`）——因此判据必须落在命令面自身，否则会出现「UI 显示已写入、下一 turn 永不生效」的层（方案 R1），`--to workspace|config` 更会留下真实副作用。落点：`chat_routing_command.go` 的 `chatRoutingSessionIsChildAgent`（**同源判据**：`agent_type` / `depth>0` / `read_only` 任一命中即子会话）与唯一文案 `chatRoutingChildSessionReadOnlyNote`；拦截点为 `chatRoutingWriteKey`（面板最终写入亦经此，先拒绝后落盘）、`chatRoutingResetFromArgs`、`chatRoutingSaveFromArgs`；`chatRoutingPanelEntry` 在子会话退化为只读摘要 + 提示。只读命令（`show` / `doctor` / 状态栏段）行为不变。
- **面板「只读打开」的落地形态（已闭环，2026-09-22 增补）**：§5.6/I-8 的「子会话中的 `/routing` 面板只读打开」已实现为**真正的只读全屏导航**（有全屏能力时）：`chat_routing_panel.go` 的 `chatRoutingReadOnlyPanelEntry` / `chatRoutingReadOnlyFieldItems` / `chatRoutingReadOnlyFieldSummary` 提供「档位 → 字段」两级导航，字段行内直接给出当前生效值与候选摘要，Enter 给该字段的只读摘要；不提供写入层选择器、不落盘，也不出现「能按 Enter 但必然失败」的假入口（I-11）。无全屏能力（非 TTY）时退回只读投影摘要 + 说明行（原「入口退化」形态）。两条路径复用唯一文案 `chatRoutingChildSessionReadOnlyNote`，写入侧拦截点不变（`chatRoutingWriteKey` / `chatRoutingResetFromArgs` / `chatRoutingSaveFromArgs`）。用例：`chat_routing_panel_test.go` 的 `TestChatRoutingReadOnlyNavigationKeepsFieldSpace`、`TestChatRoutingReadOnlyFieldSummaryStaysReadOnly`、`TestChatRoutingPanelEntryReadOnlyRoutingBySessionKind`。
- **Tab 补全同源与只读字段（§10.2 I-9，2026-09-22 增补闭环）**：`/routing` 补全的档位候选来自 `chatRoutingLevelSummaries`（与状态栏段 / `/routing show` / 全屏面板同源），取值候选来自面板的 `chatRoutingPanelValueOptions`，键空间与 `chatRoutingApplyMainKey`/`chatRoutingApplySubKey` 一一对应——因此 Tab 给出的候选一定是 §3.5 校验会接受的值；补全只影响**输入**，不改变校验、写入与生效语义。`candidates` 是只读字段（§5.3），面板与补全均不提供。逐段形态：作用域（`show`/`doctor`/`on`/`off`/`main`/`sub`/`reset`/`save`）→ 键（含 `profiles.*` / `levels.*` 键路径与 `level <level> <field>` 糖）→ 值（enabled/allow_expert/cost_guard_mode/levels/default_difficulty、provider/model/reasoning_effort），`--to <layer>` 在任意位置给 `session|workspace|config`，`--` 前缀给 flag 候选；常规匹配为空且查询非空时退化为标准 Levenshtein 最近似候选（阈值随查询长度放宽，差得太远不给候选）；唯一候选接受后由既有 `/`-补全接受路径写回行内（`TestChatSlashArgumentCompletionRoutingAcceptsValue` 钉住）。
- **快照刷新边界（已闭环，2026-09-22 增补）**：进程内快照的刷新原先只覆盖「经本 handler 的 PATCH 写入」这一条路径，现已补上**外部改动感知**（`backend/internal/runtimeserver/config_external_reload.go` 的 `ConfigExternalReloader`）：轮询来源签名（默认 `ConfigExternalReloadPollInterval`=2s）——显式 `--config` 路径 + 分层搜索栈（`agentconfig.ConfigLayerStack`）+ 预设层（`~/.aicli/presets.yaml` 与系统预设目录下的文件），即 `LoadRuntimeAgentConfig` 的全部输入（内置预设编译在二进制内，没有可观察文件）；签名变化后用**同一个** `LoadRuntimeAgentConfig` 重载，再经 `analyzeConfigDocumentRuntimeImpact`（与 API 写入同一套文档级 diff 与热/冷判据）把**可热重载**路径交给**同一个** `RuntimeConfigHotReloader.Apply`，需重启路径只给 warning（文案与 API 写入同源 `buildConfigDocumentWarnings`：「以下变更仍需重启 runtime-server 才会生效」）。加载/解析失败或无法比对时保持旧快照 + warning，绝不中断服务；轮询随启动 ctx 取消退出。未采用 fsnotify（go.mod 虽有依赖）：只看来源文件 stat、代价低于一次 HTTP 请求，且不引入 watch 描述符生命周期与编辑器 rename 语义差异。`SetAICLIConfig` 仍是唯一注入点（本增量不新增注入路径）。会话级覆盖随会话记录持久化（`Session.Metadata.Context`），不受此边界影响；workspace 层由每次投影/解析时读文件，也不受此边界影响。用例：`config_external_reload_test.go` 的 `TestConfigSourceSignatureForWatchesExplicitPathAndPresets`、`TestConfigExternalReloaderAppliesExternalChange`、`TestConfigExternalReloaderKeepsColdPathJudgement`、`TestConfigExternalReloaderDetectsRealFileEdit`、`TestConfigExternalReloaderRunStopsOnContextCancel`。

- **reasoning 建议值按档位 model 过滤（§10.2 I-3，2026-09-22 增补闭环）**：候选目录仍与 `/model` 一族同源（`reasoningEffortCatalogForModel`），但 provider/model 取「该级生效值」：档位写了 `model` 用它；只写 `provider` 用该 provider 的 `DefaultModel`（§3.5 逐字段继承）；两者都没有才回落会话模型。该级 provider 未注册、或无法确定模型、或该模型没有能力卡片时返回**空候选**（面板退化为「没有可用的 reasoning_effort 候选值；用 /routing … <value> 直接写入」），不回落会话候选——避免出现该档位模型不支持的值。档位行取自同源投影 `chatRoutingLevelSummaries`（`chat_routing_status.go:70-80`），键路径 `profiles.<level>.reasoning_effort` 的档位由 `chatRoutingLevelFromKey` 解析；面板与 Tab 补全走同一函数（`chat_routing_panel.go`、`chat_routing_completion.go:232`）。补全/面板只影响**输入候选**，不改变 §3.5 校验、写入与生效语义。

- **并发写串行化范围（U-2，2026-09-22 增补；同日第二次增补把范围扩到本包全部配置写点）**：写入锁分两层——**进程内**按会话 ID / 目标文件路径注册（aicli TUI 与内嵌 runtime-server 的 API handler、两个绑定同一工作区的会话命中同一把锁），**跨进程**再对旁路锁文件 `<目标文件路径>.lock` 取 OS 级锁（Windows `LockFileEx` / Unix `flock`，阻塞式；平台实现 `config_file_write_lock_os_{windows,unix,fallback}.go`，入口 `LockConfigFileWrite`）。为什么锁旁路文件：目标文件由 `writeFileAtomic`「写临时文件 + rename」替换，rename 后是新 inode / 新文件对象，锁目标文件会在替换那一刻失去互斥意义；旁路文件只创建不重命名，身份稳定，且释放时**不删除**（unlink 会与另一进程的「打开 → 加锁」形成竞态），0 字节残留无害。降级（静默，绝不让写入失败）：锁文件所在目录不存在、创建/加锁失败（权限、只读盘、不支持字节区间锁 / flock 的网络盘）时退化为「仅进程内锁」，即回到**后写胜出**（单次写入仍由原子写保证不撕裂）。**范围统一（已闭环，同日第二次增补）**：本包内对**同一份配置文件**的所有「读-改-写」已收敛到同一个写事务（`config_file_write.go` 的 `updateConfigFileDocument`：锁内 读 → 解析 → 变更 → 编码 → 原子写；已持锁的复合写点改用 `updateConfigFileDocumentLocked` 并归还锁）。已入锁写点：`UpdateAICLIChatPreferences`、`UpdateAICLIThemePreferences`、`UpdateProviderConfig`、`UpdateAICLIRoutingSection`、`SetProvidersEnabledConfig` / `SetDefaultProviderConfig` / `DeleteProvidersConfig`（`provider_management.go`）、`SetGlobalProxyConfig` / `RemoveGlobalProxyConfig`（`provider_proxy.go`）、`applyDocumentChanges`（层文档：按目标文件逐个取锁、不嵌套）、`SaveWorkspaceChatPreferencesForPath` / `ClearWorkspaceChatPreferences`、`EnsureStarterConfigAtPath`（公开入口取锁，内部 `ensureStarterConfigAtPathLocked` 供事务内调用，避免自锁）。随之落地的两处行为修正：`UpdateProviderConfig` 的新 provider 解码移到**落盘前**（变更无效 → 直接失败，不再留下被改写的半成品文件）；`UpdateWorkspaceRoutingSection` 整段持锁并改调 `saveWorkspaceChatPreferencesAtLocked`（原实现会在锁内再取同一把锁 = 自锁）。锁键归一化为**绝对路径**（`~` 展开 + `Clean` + `Abs`；Windows 大小写不敏感），相对 / 绝对 / 大小写变体命中同一把锁。**如实登记的两个例外**（故意不取锁，注释已就地写明理由）：① `preset.go::EnsureUserPresetsFile`——create-once、独立文件（`~/.aicli/presets.yaml`），除首次创建外没有并发「读-改-写」写者；② `provider_proxy.go::SetProviderProxyConfig` / `RemoveProviderProxyConfig`——锁外读旧值合并字段补丁，落盘交给已持锁的 `UpdateProviderConfig`（自身再取锁即自锁）。**边界**：锁覆盖本包的进程内写者；本包之外若新增对同一文件的写入通道，必须复用 `updateConfigFileDocument` 或自取 `LockConfigFileWrite`（规则已写入锁的 doc comment）。workspace 层锁键为目标 `chat-prefs.yaml` 路径（Windows 下大小写归一），config 层锁键为按层路由后的配置文件路径（项目层与全局层各自成键）。用例：`config_file_write_lock_xproc_test.go` 的 `TestLockConfigFileWriteBlocksAcrossProcesses`（子进程真实持锁 → 父进程静默窗口内不得抢到）、`TestConfigSectionWriterBlocksAcrossProcesses`（跨进程走完整写事务：子进程持锁 → 父进程 `UpdateAICLIChatPreferences` 必须等待）、`TestLockConfigFileWriteUsesSidecarLockFile`、`TestLockConfigFileWriteDegradesWhenLockDirectoryMissing`；`config_file_write_lock_test.go` 的 `TestLockConfigFileWriteBlocksUntilReleased`、`TestNormalizeConfigWriteLockKeyUnifiesPathSpellings`、`TestConfigFileWritersHonorSharedWriteLock`（9 个写点：chat / theme / provider / routing / provider_enable / provider_default / provider_delete / global_proxy / layer_document）、`TestWorkspacePrefsWritersHonorSharedWriteLock`（chat-prefs 的 chat_prefs / routing 两个写点）、`TestConcurrentConfigSectionWritersPreserveEverySection`。routing 的两个并发用例与本轮「统一」用例均经「临时摘锁 → 必须 FAIL → 还原复跑 ok」的证伪验证。

## §10 测试与验收

### §10.1 后端用例（U）

| 编号 | 用例 | 期望 | 覆盖 |
| --- | --- | --- | --- |
| U-1 | 写入会话 override → 下一 turn | 新 actor 使用覆盖值；进行中 turn 不受影响；响应 `actor_invalidated=true` | §4.4/§4.5 |
| U-2 | 两会话端并发写同一 override（TUI + API） | 串行化，后写胜出，无丢更新；warning 可见 | M11、§3.4 |
| U-3 | 非法组合（`default_difficulty∉levels`、`allow_expert` + 无限成本护栏） | 按 §3.5.2 阶梯回退，warning 记录最终来源；仍非法则 400 不落盘 | B4 |
| U-4 | 无任何覆盖 | 解析器返回**原配置指针**（`res.Effective == EffectiveMainAgentRoutingConfig(cfg)`） | M8、§4.1 |
| U-5 | 子会话（agent_type/depth/read_only）调用 PATCH | 409 拒绝；GET 返回继承结果 | M16、§4.4 |
| U-6 | 关闭态（`enabled=false` 或缺省） | `loopConfig.MainAgentRouting=nil`；行为与基线逐位一致 | INV-A1、B5 |
| U-7 | 解析器产出对象的深拷贝隔离 | 修改解析结果不污染 `cfg` 快照；`main_agent_route_test.go:78-89` 同口径 | §4.1 |
| U-8 | 清除 override | 解析回落到下层；`Sources` 正确标注 config/default | N8、§4.5 |
| U-9 | workspace 层写入（N9） | 以会话绑定 workspace 路径定位 `chat-prefs.yaml`；回显路径；cwd 漂移不影响 | N9、§5.4 |
| U-10 | 未授权 PATCH（无 token、非回环） | 拒绝（与既有会话写端点同级别） | M4、§4.4 |
| U-11 | 通用 `PATCH /sessions/{id}` 携带 `aicli_routing_override` | 按 §4.5 处理（首版拒绝并提示专用端点），不得绕过校验/失效 | N4 |
| U-12 | 无全局配置时开启（S1 主用例） | §3.5.3 推导出 `levels`，通过校验并生效；推导来源可见 | B3 |
| U-13 | config 层写入路由（v3） | `./.aicli/config.yaml` 不存在 → 写 `~/.aicli/config.yaml`；存在 → 写项目层；写前校验失败 → 拒绝且文件不变；响应回显目标路径 | §5.4、`config_layers.go:118-155` |
| U-14 | 会话层物理落点（v3 核实） | 文件后端：会话 JSON（`~/.aicli/sessions/YYYY/MM/DD/<id>.json`）出现 `metadata.context.aicli_routing_override`；运行时后端：`sessions/runtime/session_runtime.sqlite` 同键可读回；**不产生 `~/.aicli/workspaces` 目录** | §3.4 核实结论 |

### §10.2 UI 用例（I）

| 编号 | 用例 | 期望 |
| --- | --- | --- |
| I-1 | `/routing` 面板：Level → 字段 → 值，写入 session | 三级导航可用；提交后状态栏即时更新；`Esc` 不落盘 |
| I-2 | 面板写 workspace 层；config 写入按层路由 | 目标层徽标与文件路径回显；config 层保存需二次确认并回显目标路径（无项目层时落全局）；`reset` 逐层回退可见 |
| I-3 | 建议值过滤 | provider/model 候选来自 catalog；reasoning 候选按该级已选 model 过滤（`chat_reasoning.go:29-50`），不支持值不出现 |
| I-4 | 非法输入 | 错误按字段回显、不落盘；提示建议动作（如先设 `levels`） |
| I-5 | 状态栏 routing 段 | `route:<level> · <model> · <effort> · <来源后缀>`；`/model` 变更后同步刷新（`chat_reasoning_command.go:206-208` 范式） |
| I-6 | 四处一致（§7.4） | TUI 状态栏段 / `/routing show` / micro web client 快照 / frontend 会话详情路由区块逐字段相等（同一投影函数） |
| I-7 | 窄终端 | fit 逻辑裁剪后仍保留 model 与 routing 段 |
| I-8 | 子会话内 `/routing` | 只读打开；写入类子命令提示 + 拒绝（M16） |
| I-9 | 命令行补全 | Tab 逐段补全（作用域 → 键 → 值）；非法值给最近似候选 |
| I-10 | 既有命令回归 | `/debug routing`、`/agents routing` 输出语义不变（仅追加迁移提示）；`/agents routing test` 不受影响（M14） |
| I-11 | 渲染形态（v3.1，§5.3.1） | 仅「打开面板」一族命令（`/routing`、`/routing main\|sub [<level>]`、键路径直达）触发独立全屏；三者进入同一面板，面板内三级导航为 stage 切换；`show`/`doctor`/`set`/`on\|off`/`level` 糖/`save`/`reset` 均为行内文本、不切换屏幕；frontend 区块与 micro web 状态栏均非全屏 |

### §10.3 REG 清单与 REG 变更清单（M8/B6）

**REG（必须保持全绿，且纳入每次阶段发布）**

| 编号 | 对象 | 断言要点 |
| --- | --- | --- |
| REG1 | `main_agent_routing_test.go`、`main_agent_routing_wiring_test.go` | 配置校验/默认值/YAML 往返；关闭态 gate（`:67-`） |
| REG2 | `chat_main_agent_routing_test.go:30/38-40/68` | **指针同一性**（快路径下必须原样通过）+ 子会话隔离 |
| REG3 | `main_agent_route_test.go:78-89` | `NewReActLoop` 深拷贝隔离 |
| REG4 | `main_agent_routing_prompt_test.go` | 引导片段注入语义（开关/顺序/稳定） |
| REG5 | 状态栏相关：`ui/statusbar_test.go`、`chat_balance_refresh_test.go:397`、`chat_interaction.go` 段装配测试 | 段顺序/索引/渲染 |
| REG6 | 命令面：结构化命令白名单与 catalog 测试（`chat_debug_endpoints_test.go:185` 等） | 既有命令 token 与输出不变 |
| REG7 | 子 Agent 路由：modelrouting 既有测试 | 难度归一/候选链/兼容语义不变 |

**REG 变更清单（允许修改的既有断言，必须与实现同 PR 并注明理由）**

| 对象 | 变更 | 原因 |
| --- | --- | --- |
| `chat_balance_refresh_test.go:397` 及按序索引断言的同族测试 | 索引/顺序更新（+1 段） | B6：状态栏插入新段必然改变索引 |
| `statusline.go:12-24` `StatusSegmentKind` 相关测试 | 增加第 9 种 kind 的期望 | B6 |
| `frontend/src/types/runtime/event-contract.ts` | **不手改**：重跑 `backend/cmd/contractgen` 生成 | M7 |

- 变更清单之外**不得**修改任何既有断言；如发现必须修改，回到 §11 待确认流程。

## §11 待确认项（实现前闭环）

| 编号 | 待确认 | 当前建议 | 来源 |
| --- | --- | --- | --- |
| U-1 | `frontend/` 展示位置（**v3 已定**） | 右侧栏「会话详情」面（`sessionDetail`）内新增路由区块（`panel-registry.ts:148-161`）；**不新建底部状态条** | M15、§7.2 |
| U-2 | 模型/provider/effort 变更如何通知前端（现状可能根本不通知） | 本方案不假定已有通道；`session.routing_changed` 是否兼作该用途单独决策 | M5 |
| U-3 | 是否需要独立 workspace 管理端点（跨会话浏览/编辑 `chat-prefs.yaml`） | 首版不做（走会话绑定路径，§7.3）；有跨会话需求再加 | M10 |
| U-4 | 状态栏 routing 段的默认可见性与 Priority 数值 | **已闭环（v3.1 落地）**：可见性＝仅当任一层显式配置路由时显示（零配置不显示，§6.2 可见性条）；段内默认精简形式（level+model）；Priority 与真实段优先级表对齐后确定；**首版不做** `soft!` 等瞬态后缀 | N10 |
| U-5 | `task_types` / `roles` 覆盖表是否开放编辑 | 首版只读展示 | §5.6 |
| U-6 | 紧急开关 `AICLI_DISABLE_ROUTING_UI=1` 是否随 P2 落地 | **已闭环（v3.1 落地于 P4）**：只隐藏 UI 入口（`/routing` 输出开关说明、命令目录项、状态栏段），解析器与 API 不变；识别值 `1/true/on/yes/y/enabled` | §8.3 |
| U-7 | 请求级 `routing`（第 5 层）是否对外开放 | 首版保留结构、不开放（仅会话/工作区/配置三层可写） | N6 |
| U-8 | `candidates` 候选链是否开放编辑 | 首版只读 | §5.3 |
| U-9 | micro web client 是否订阅 `session.routing_changed` | 首版可选（保存后主动重拉已足够） | §7.1 |
| U-10 | 运行期“当前生效档位”是否可观测（turn 内难度判定落点） | 未证实；可观测则状态栏显示 level，否则显示 `default_difficulty` 并标注 | §2.2 #6、§6.2 |
| U-13 | 会话层是否改为「可手工编辑的独立文件」（用户提议的 `~/.aicli/workspaces` 复数目录**经核实不存在**） | 不新建；override 随会话记录持久化于 `~/.aicli/sessions/`（§3.4 核实结论）；如确需独立文件，落 `~/.aicli/sessions/` 之下 | v3 核实 |

## §12 风险与缓解

| 编号 | 风险 | 缓解 |
| --- | --- | --- |
| R1 | UI 写出「不生效的层」（尤其子会话） | P1 发布闸门（§9）；子会话只读（M16）；写入响应回传 `actor_invalidated` |
| R2 | 状态栏插入新段引发渲染/索引回归 | B6 显式索引修订 + REG5 + 变更清单（§10.3） |
| R3 | TUI / micro web / frontend 会话详情 / API 四处投影漂移 | 单一投影函数（§6.1）+ I-6 用例 |
| R4 | 并发写丢更新 | §3.4 串行化 + U-2；进程内按会话/路径串行化 + 跨进程旁路文件 OS 锁（§9.1 增补；降级退回后写胜出）+ 外部改动由 `ConfigExternalReloader` 兜底刷新；后写胜出并留 warning |
| R5 | 新增写端点鉴权缺口 | 逐 handler 复刻检查（M4）+ U-10 + P1 验收 |
| R6 | config 层写入写出「启动即失败」配置 | 已开放（v3）但强制：层路由（`WritableLayer`）+ 写前 `ValidateMainAgentRoutingConfig`（`main_agent_routing.go:132-162`）+ 二次确认（目标路径 + 影响所有工作区）+ 写后 §4.5 失效 | §5.4 |
| R7 | workspace 路径漂移导致写到错误文件 | 以会话绑定 workspace 路径计算哈希（N9）+ 写入前后回显路径 |
| R8 | 前端契约被手改 | contractgen 生成流程（M7）+ 变更清单禁止手改 |
| R9 | 用户误以为「立即生效」 | 事件 `effective_from: "next_turn"` + 面板/状态栏文案「下一轮生效」（§4.5/§7.1） |

## §13 修订对照与变更记录

### §13.1 v1 问题 → v2 章节对照

| v1 问题 | v2 落点 | 处置 |
| --- | --- | --- |
| B1 `enable_routing` 语义错配 | §0.1、§4.2、§8.1 | 明确非目标 2；开关只用 `main_agent.routing.enabled` / `subagents.routing.enabled` |
| B2 子覆盖结构虚构 | §3.2、§4.1 | 以真实结构镜像（`config.go:672-708`、`:720-738`） |
| B3 无全局配置时开启不可行 | §3.5.3 | 开启推导 + 拒绝条件 + 建议动作 |
| B4 逐字段覆盖与跨字段约束冲突 | §3.5.2 | 合并→校验→3 轮回退阶梯（确定性顺序） |
| B5 关闭态丢失来源 | §3.1、§4.1、§6.1 | `RoutingResolution` 恒非 nil + `Sources` |
| B6 状态栏插入破坏索引 | §6.2、§10.3 | 显式索引修订 + REG 变更清单 |
| M1 `guard off` 非法 | §3.5.4 | 仅 `soft|hard` |
| M2 health 键路径错误 | §3.5.1、§3.5.4 | 仅开放 `health_gate.respect_provider_health` |
| M3 默认值吞掉显式 0 | §3.5.4 | 0=未设置（`max_consecutive_expensive_steps` 例外） |
| M4 runtime 无路由级鉴权 | §4.4、§10.1 U-10 | 逐 handler 复刻 + 写端点分级 |
| M5 `model_changed` 前端零消费 | §7.1、§11 U-2 | 不复用；新增 `session.routing_changed` |
| M6 热重载前提不成立 | §0.1、§4.5 | 改写为「actor 构建期读取 + 写后失效重建」 |
| M7 前端契约是生成物 | §7.1、§10.3 | contractgen 流程 + 禁止手改 |
| M8 既有测试未纳入 REG | §4.1、§10.3 | 快路径保住指针同一 + REG2 纳入 |
| M9 会话键未复用约定 | §3.4 | `sessionmeta` 常量 + JSON 字符串 + 解码 helper |
| M10 前端工作区偏好无服务端对应 | §7.3、§11 U-3 | 选 (a)：服务端读写同一 `chat-prefs.yaml` |
| M11 metadata 并发写无保护 | §3.4、§4.4、§10.1 U-2 | 会话级串行化 + 后写胜出 |
| M12 `scope` 语义重叠 | §5.2、§5.4 | 两轴正交 + `--to` + 影响矩阵 |
| M13 顶层 Enabled 双开关 | §3.2 | 删除顶层，仅两个分节开关 |
| M14 `/debug routing` 语义冲突 | §5.1、§5.2、§6.3 | 原语义不动；`/routing show|doctor` 自建渲染 |
| M15 micro web 与 frontend 两套界面 | §6.4、§7.2、§7.4 | 限定消费方；frontend 展示位置 v3 定稿＝右侧栏会话详情面路由区块 |
| M16 非主会话 `/routing` 未定义 | §4.2、§5.6、§10.1 U-5 | 只读 + 写入拒绝 |
| N1 锚点更正 | §1.4 | 逐条更正 |
| N2 `session` 来源值已存在 | §3.1、§1.4 | 复用枚举，仅新增文案 |
| N3 第三参误写 `isMain` | §4.3 | 改为内联表达式 |
| N4 `Source` 语义冗余 | §3.2 | 合并为 `UpdatedBy`（最近写入端） |
| N5 键空间未定义 | §3.5.1 | 唯一键名表 |
| N6 请求级 routing 与 `enable_routing` | §4.5、§8.1 | 请求级仅本 turn；与 `enable_routing` 无交集 |
| N7 层数命名冲突 | §3.1、§4.1 | 统一为「请求级（仅本 turn） > 会话 > 工作区 > 配置 > 默认」 |
| N8 关闭态来源判定 | §3.1、§6.1 | 依赖 B5 修复后的 `Sources` |
| N9 工作区 key 与 cwd 关系 | §5.4 | 以会话绑定 workspace 路径为准 |
| N10 Priority / `soft!` 无依据 | §6.2、§11 U-4 | 首版不做瞬态后缀；Priority 待对齐段优先级表 |

### §13.2 变更记录

- **2026-09-22 增补（实现收口，方案版本不变）**：把 §9.1/§12 登记的四处实现缺口全部收口并补齐用例——① `thinking_effort`/`max_tokens`/`temperature` 面板数值项自由文本录入 + 与写入路径同源的就地校验；② 子会话只读改为真正的只读全屏导航（`chatRoutingReadOnlyPanelEntry`）；③ routing 文件写入的跨进程互斥（旁路锁文件 + Windows `LockFileEx` / Unix `flock`，带静默降级与残留项登记）；④ 外部改动感知（`ConfigExternalReloader` 轮询来源签名 → 与 API 写入同一套判据应用，含冷路径 warning 与保持旧快照的降级）。四处均在原登记条目就地更新为「已闭环」。
- **v3.1（2026-09-22）**：澄清 `/routing` 的 TUI 渲染形态——新增 §5.3.1（独立全屏 vs 行内文本）+ §5.2 渲染形态条目 + §10.2 I-11 验收用例 + 头部版本升至 v3.1。结论：独立全屏渲染**仅限「打开面板」一族命令**（`/routing`、`/routing main|sub [<level>]`、键路径直达值选择器），三种入口进入**同一个**全屏面板（内部三级导航为 stage 切换，复用 `chatPicker*` + `ui.FullScreenList`，不新增独立渲染器）；`show`/`doctor`/`set`/`on|off`/`level` 糖/`save`/`reset` 均为行内文本；frontend 会话详情区块与 micro web client 状态栏端点均非全屏。
- **v3（2026-09-22）**：按用户指示修订三处并核实落点。① **frontend 展示位置定稿**：右侧栏「会话详情」面（`sessionDetail`）内新增路由区块，不新建底部状态条（§7.2/U-1）；② **config 层开放写入**：复用现有层路由——`./.aicli/config.yaml` 存在→项目层，否则→全局 `~/.aicli/config.yaml`（「工作区无 `.aicli` 配置即写全局」），写前校验 + 二次确认（§2.2 #3、§5.4、§7.3）；③ **会话层落点核实**：`~/.aicli/workspaces`（复数）**不存在**（实测 + 代码无常量），会话 override 随会话记录持久化于 `~/.aicli/sessions/`（文件后端 `YYYY/MM/DD/<id>.json`、运行时后端 `sessions/runtime/*.sqlite`），工作区层目录为**单数** `workspace/` 且 hash=`projectIDForPath`（16 hex，与 registry 的 12-hex id 不同）；新增 §3.4 核实结论、§10.1 U-13/U-14。
- **v2（2026-09-22）**：按审查报告重写。核心变化：①新增 §5 逐级路由 UI（TUI 优先，补齐 `profiles/levels` 三元组的编辑缺口）；②解析器/接线改为「actor 构建期读取 + 写后失效重建」（M6）；③事件契约改为新增 `session.routing_changed` 并走 contractgen（M5/M7）；④两轴（main|sub / 写入层）语义与影响矩阵显式化（M12）；⑤状态栏段插入的索引修订与 REG 变更清单显式化（B6/M8）；⑥frontend 与 micro web client 的边界与工作区偏好决策显式化（M10/M15）。
- **v1（2026-09-21）**：初稿（已归档为审查对象）。
