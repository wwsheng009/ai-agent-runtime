# 内置工具统一注册 + 元数据接口 — 实施方案

> 诉求（用户原话）：内置工具要有**统一的管理、注册**，**避免多个入口**；还需提供**元数据接口**（如描述、分类），
> 一是方便管理，二是方便**前端查询与配置**。
>
> 难度：hard（跨包、涉及策略判定与两宿主装配，需回归验证）。
>
> 与既有文档的关系：本文是 [`unified-tool-surface-registry.md`](../architecture/unified-tool-surface-registry.md)
> §3 目标 1/目标 3 与 [`profile-tool-groups-design.md`](profile-tool-groups-design.md) §3.3 的**落地施工单**，
> 不新立设计分支；组词汇、不变量（T1–T5）一律沿用既有定稿。

---

## 0. 结论先行

诉求对应的三件事，当前状态**各不相同**：

| 诉求 | 现状 | 判定 |
| --- | --- | --- |
| 统一**分类/账本** | `internal/toolsurface`（`Classify`/`Build`/`SummarizeSources`）已是单一实现点，请求路径已接入 | ✅ 已完成 |
| 统一**注册** | `internal/tools` 侧已收敛（核心表 `builtinToolkitRegistrations` + `code.*` 条件表 `codeToolRegistrations`，均有双向闸门）；**宿主侧 5 处也已收敛**为单一 helper `registerRuntimeBuiltinTool`（slice 2，含源码门禁） | ✅ 已完成 |
| **元数据对外接口** | 分类被刻意排除在定义外，`/runtime/tools` 响应无分类/分组 | ❌ 不存在 |

关键认知：**"装配"（谁组装工具面）已统一，"注册"（谁产出工具定义）没有。**
`toolsurface.Build` 消费的是别人喂进来的 catalog —— 它统一的是**后处理**，不是**来源**。
本方案补的正是"来源"这一层。

---

## 1. 现状证据（逐条核查）

### 1.1 注册点清单（"多个入口"的事实基础）

| # | 位置 | 产出 | 层 |
| --- | --- | --- | --- |
| 1 | `backend/internal/tools/manager.go:508 registerBuiltinToolkitTools` | 25 个核心工具（slice 1 后改为遍历构造表 `builtinToolkitRegistrations`，见 §8） | L2 核心 |
| 2 | `backend/cmd/aicli/commands/function_catalog.go:164 RegisterBuiltinToolFunction` | aicli 函数目录 | L3 宿主 |
| 3 | `backend/internal/toolbroker/*`（`team_tools.go:48 DefinitionsForContext`） | 编排面（job/subagent/team/schedule/plan/question） | 编排 |
| 4 | `backend/internal/mcp/registry/registry.go:115`、`backend/internal/mcp/server/server.go:52` | MCP 工具 | MCP |
| 5 | `backend/internal/agent/tool_surface_binding.go:101 CollectToolCatalogDefinitions` | 请求装配：四源同采 | 装配 |
| 6 | `backend/internal/toolsurface/toolsurface.go` | 分类/账本 | 观测 |
| 7 | `backend/internal/policy/tool_policy.go:158 IsRuntimeOwnedEssentialTool` | **第二份硬编码名单**（40+ 名），与 #1 天然漂移 | 策略 |

### 1.2 宿主侧注册调用点（#2 的散落证据）

同一调用形态 `session.FunctionCatalog.RegisterBuiltinToolFunction(functions.NewRuntimeToolFunction(...), desc)`
出现在 **5 个不同宿主文件**：

- `backend/cmd/aicli/commands/chat_setup.go:554`
- `backend/cmd/aicli/commands/chat_mcp_session_scope.go:511`
- `backend/cmd/aicli/commands/web_mcp_handlers.go:356`
- `backend/cmd/aicli/commands/acp_mcp_host.go:488`
- `backend/cmd/aicli/commands/chat_lsp_bootstrap.go:106`

### 1.3 元数据现状（为什么前端拿不到分类）

- 定义类型：`backend/internal/types/message.go:43`
  `ToolDefinition{Name, Description, Parameters, Metadata}`。
- `surface_source` 分类**不写进定义**——`backend/internal/agent/loop.go:4334-4338` 明确注释：
  写进每定义 metadata 会增大 `json.Marshal` 体积、被 `estimateToolDefinitionTokens` 计入，
  把贴边预算的压缩/preflight 时序推偏（compaction recovery 测试实测 flaky）。
  改为请求级汇总：`loop.go:2321 req.Metadata["tool_surface_sources"]`。
- 后果：**观测端点拿不到分类**。`unified-tool-surface-registry.md` §4.2 自己登记
  "`/api/runtime/tools` 与 TUI `/tools` 面都暴露账本（**现响应只有 name/description**）"。
- 端点：`backend/internal/api/runtimeapi/session_runtime_handlers.go:507 ListSessionRuntimeTools`，
  响应为 `{session_id, tools[], count, source}`（测试见 `session_runtime_handlers_test.go:308-313`）。

### 1.4 分组注册表

`backend/internal/toolgroups` **不存在**（glob 为空）。组词汇已定稿于
`docs/tools/profile-tool-groups-design.md:60-79`：

| 组 id | 含义 | 可关闭 |
| --- | --- | --- |
| `read` / `edit` / `shell` | 读类 / 写类 / 命令执行 | 否（protected） |
| `webfetch` / `search` / `io` | 网络取回 / 检索 / 产物·图像·下载 | 是 |
| `orchestration` | broker 编排面（`toolbroker.IsBrokerTool`） | 是 |
| `mesh` / `lsp` | 以 `mcp:<server>` 表达 | 是 |

### 1.5 前端

`frontend/`（React + Vite，`src/api`、`src/pages`、已有 `profiles-editor.spec.ts` e2e）
**尚未消费** `/runtime/tools`（grep `runtime/tools|surface_source|toolGroups` 为空）。
即：接口侧先把元数据补齐，前端才有可接的东西。

### 1.6 本轮联调发现的直接缺口（本方案要顺带闭环的验收点）

同一会话内三个数字不一致：profile 声明 7 / `/tools` 显示 10 / 模型实收 22。根因已定位为两条：
(a) `/tools` 兜底分支读的是 `runtimeMCP` 单一源（`chat_actor_host.go:1413`），**缺 broker 这一源**，
且传的是空账本 `toolsurface.Ledger{}`；
(b) profile 的 allowlist 在当时不约束控制面（`policy.IsRuntimeOwnedEssentialTool`；
    **2026-10-16 已变**：essential 豁免移除，allowlist 完全约束工具面，
    见 [`tool-policy-wildcards.md`](tool-policy-wildcards.md) §2）。
本方案的元数据接口必须做到**观测数字与请求数字同源**，否则新旧口径继续打架（→ 不变量 T7）。

---

## 2. 目标架构

### 2.1 新增 `internal/toolregistry`：注册的唯一真源

**定位澄清（避免与既有包职责重叠）**：

| 包 | 管什么 | 是否改动 |
| --- | --- | --- |
| `internal/toolkit` | 工具的**执行**（`Tool` 接口、`Registry.Register`） | 不动 |
| `internal/tools` | 工具的**装配与闸门**（manager、codeGate） | 收敛调用方式 |
| `internal/toolbroker` | 编排工具的**实现与暴露条件** | 不动 |
| `internal/toolsurface` | 工具面的**后处理与分类**（已有单一实现点） | 不动 |
| **`internal/toolregistry`（新）** | 工具的**身份与元数据**（名字/别名/分组/来源/暴露面/必需性） | 新增 |

叶子包，只依赖 `internal/types`（与 `toolsurface` 同策略，避免成环）。

```go
package toolregistry

// Exposure 是工具在哪个宿主面上暴露。
type Exposure string

const (
    ExposureShared        Exposure = "shared"
    ExposureActor         Exposure = "actor"
    ExposureRuntimeServer Exposure = "runtime_server"
    ExposureTUILocal      Exposure = "tui_local"
)

// Entry 是一个内置工具的元数据条目（身份真源）。
type Entry struct {
    Name        string     // canonical 名（模型可见）
    Aliases     []string   // read→view、rg→grep、bash/execute_shell_command→shell
    Description string     // 单行描述（供前端展示；与 toolkit.Tool.Description() 同源校验）
    Group       string     // read|edit|shell|webfetch|search|orchestration|io|mesh|lsp
    Source      string     // core|capability:<pkg>|mcp:<server>|ui
    Exposure    []Exposure
    Essential   bool       // 仅观测语义（2026-10-16 起不再是 allowlist 豁免）
    Contractual bool       // 运行时契约工具：allowlist 未列出仍授予，显式 deny 优先
    Protected   bool       // protected 组：不可整组关闭
}

func Builtins() []Entry                      // 核心面全量
func Lookup(name string) (Entry, bool)       // 归一化后查（别名可命中）
func Canonical(name string) string           // 别名 → canonical
func ByGroup(group string) []Entry
func Groups() []GroupInfo                    // 组目录（id/label/protected）
func IsEssential(name string) bool           // 取代 policy.IsRuntimeOwnedEssentialTool 的实现
```

**核心收益**：`Essential` 字段把 `policy/tool_policy.go:158` 那个 40+ 分支的 `switch`
变成"读注册表"——消除**第二份名单**，这是"避免多个入口"最有价值的一刀。

### 2.2 元数据接口（对前端）

**A. 增强既有会话级端点** `GET /api/runtime/tools`
（`session_runtime_handlers.go:507`，响应在既有 `{session_id, tools[], count, source}` 上扩展）：

```jsonc
{
  "session_id": "session_...",
  "count": 22,
  "source": "...",
  "tools": [
    {
      "name": "view",
      "description": "...",
      "parameters": { "...": "..." },
      "metadata": {
        "surface_source": "core",     // toolsurface.Classify（既有单一实现点）
        "group": "read",              // toolregistry
        "exposure": ["shared", "actor", "runtime_server"],
        "essential": false,
        "alias_of": ""                // 别名工具标注其 canonical
      }
    }
  ],
  "groups": [ { "id": "orchestration", "label": "编排面", "protected": false, "enabled": true, "tool_count": 12 } ],
  "filtered": [ { "name": "shell", "reason": "profile_deny" } ]
}
```

- 分类一律走 `toolsurface.Classify`（不新建第二套判定）；
- `group`/`essential`/`alias_of` 取自 `toolregistry`；
- `filtered[].reason` 取值：`profile_deny | group_disabled | mcp_disabled | host_read_only`
  （词表与 `profile-tool-groups-design.md` §5.4 一致）。

**B. 新增无会话端点** `GET /api/runtime/tools/catalog`
返回**全量注册表 + 组目录**（不受会话 profile 约束），供前端 profile 编辑器渲染
"可关闭组"清单与工具说明。这是"方便前端查询配置"的直接落点。

**C. 录制口径修正（必做，否则新接口继续少报）**
`/runtime/tools` 的目录来源必须与请求装配同源：
- 并入 broker 源（`broker.DefinitionsForContext`），
- 传入**真实账本** `toolsurface.Ledger{Broker: broker}`（当前 `chat_tools_screen.go:71` 传空账本）；
- TUI `/tools` 兜底分支（`chat_tools_screen.go:34-71`）复用同一 payload，消除 10 vs 22 缺口。

### 2.3 分组表不新建第二份

`profile-tool-groups-design.md` §4 落点 #1 原计划新建 `internal/toolgroups`（组表）。
本方案改为**由 `toolregistry.Group` 派生**，`internal/toolgroups` 只保留
`IsDisabled()/Validate()` 这类**判定**函数（如需独立包），组表本身不复制：

- `read|edit|shell` → `Protected: true`（草案 §3.3）；
- `orchestration` → 与 `toolbroker.IsBrokerTool` 做**漂移护栏测试**交叉校验；
- `mcp:<server>` → 直接复用 `toolsurface.Classify` 输出。

好处：草案 §6 要求的"`policy/specifier.go` 组表与 `toolgroups` 表一致性断言"变成同源，无需护栏。

---

## 3. 分期（每期可独立验收）

### P0 — 注册表 + 元数据接口（低风险，纯增量）

1. 新增 `backend/internal/toolregistry`：`Entry`/`Builtins()`/`Lookup()`/`Groups()`/`IsEssential()`
   + 单测（完整性、别名归一、分组约束、与 broker 交叉校验）。
2. `/api/runtime/tools` 响应扩展（§2.2-A）；目录来源并入 broker + 真实账本（§2.2-C）。
3. 新增 `/api/runtime/tools/catalog`（§2.2-B）。
4. TUI `/tools` 复用同一 payload（顺带修空账本）。

**不变量**：不改任何执行语义、不改冻结面、不改 token 估算路径。

### P1 — `Essential` 名单去重（中风险）——**已完成**（2026-10-16）

- `policy.IsRuntimeOwnedEssentialTool` 改为委托 `toolregistry.IsEssential`；
- 保留 `runtimeOwnedEssentialExplicitlyDenied` 语义（deny 恒优先）；
- 回归：`internal/policy`（含 `supervision_tool_registry_test.go`）、`internal/agent`。

落地与验证记录见 §8「2026-10-16 — P1（`Essential` 名单去重）已落地」。
**实施时发现清单本身在迁移前是坏的**：基线把名字折向旧拼写、broker 却广播规范名，
于是窄 allowlist 会静默摘掉委派入口的定义；等价口径因此按「**身份级集合相等**」而非
「逐拼写相等」验收（§5.1 该行按此理解）。

### P1.1 — `Essential` 豁免移除 + 名单通配（2026-10-16）——**已完成**

P1 之后用户提出：profile 必须能**完全控制**工具面。同日落地（设计全文见
[`tool-policy-wildcards.md`](tool-policy-wildcards.md)）：

- `policy.AllowTool` 删除 essential 豁免分支：allowlist 变硬闸门，deny 恒优先；
  `IsRuntimeOwnedEssentialTool` / `runtimeOwnedEssentialExplicitlyDenied` 退役，
  测试与观测面改用 `toolregistry.IsEssential`（字段保留，仅观测语义）；
- 新增 `toolregistry.MatchPattern` / `MatchesAnyPattern` / `ListDeclaresWildcard` /
  `PatternHitsRegisteredName`：`*` 匹配一切、`prefix_*` 归一化前缀、其余归一精确；
- `allowlist` / `denylist` 支持通配；父子交集（`intersectAllowedTools`）通配感知；
- `disabled_capabilities` 通配在 profileinput 展开为封闭枚举域（`*`→全部用户可禁域）；
- 消费端适配：agent spawn 定义闸门、`childSurfaceLacksShell`、toolguidance facts、
  profile validate / reference_validation 的通配校验。

### P2 — 注册点收敛（中高风险）

> **施工时设计修正（2026-10-16，slice 1 落地）**：本节原先写的"遍历 `toolregistry.Builtins()`"
> **不可按字面实现**。三条实测证据：
> 1. 注册表（`toolregistry/builtinEntries`）含**非 toolkit** 条目——broker 编排面、
>    capability（`get_goal`/`read_goal`/`update_goal`）、agent 按需注册的 `search_tool`、
>    ADR-0004 code gate 的 `code.*`——无差别遍历会把它们当成 toolkit 内置工具；
> 2. 别名工具是**异构真实实例**（`bash`/`execute_shell_command`/`read`/`rg` 各自持有自己的
>    schema 与描述），构造参数只有宿主知道，构造表无法省掉；
> 3. 但"顺序风险"是**不存在**的：`toolkit.Registry.List()` 按工具名排序输出
>    （`internal/toolkit/registry.go:114-116`），注册顺序不可观察，不构成冻结面。
>
> 故落地形态为：注册表新增**正向**声明位 `Entry.ToolkitBuiltin`（回答"**有哪些**"，零值 =
> 本包不作声明），manager 保留构造表（回答"**怎么造**"），两侧成员集合由
> `internal/tools/builtin_registry_contract_test.go` **双向**钉死。仍是"单一真源"，
> 只是真源放到了能表达它的那一侧。

- **slice 1（已完成）**：`tools/manager.go` 的 25 个硬编码 `register(...)` → 单一构造表
  `builtinToolkitRegistrations`（21 条无条件 + 1 条按 provider 条件）；`Entry.ToolkitBuiltin`
  覆盖 18 个规范条目；新增漂移闸门测试。
- **slice 2（已完成，2026-10-16）**：5 处 `RegisterBuiltinToolFunction`（§1.2）收敛为
  `function_catalog.go` 的单一 helper `registerRuntimeBuiltinTool`。
  **落地形态**：helper 收 `*aicliFunctionCatalog` + `functions.RuntimeToolProvider` + 描述，
  内部就是原先那一行包装；5 处调用点各减为一行（`acp_mcp_host` / `chat_lsp_bootstrap` /
  `chat_mcp_session_scope` / `chat_setup` / `web_mcp_handlers`），其中 3 个文件的 `functions`
  import 随之失效并移除；**各调用点自己的过滤条件（skipMCP / sessionMCPOverrideDisabled /
  registered 去重）留在原处**——那是宿主策略，不是惯用法的一部分。
  **新增门禁**：`TestRuntimeToolFunctionWrappingIsSingleSourced`（源码扫描：非注释行里
  `functions.NewRuntimeToolFunction(` 只能出现 1 次，即 helper 本体；既防别的文件再抄一份，
  也防同文件里多抄一份）。
  **历史（为何曾一再推迟）**：该包有第二写入者。15:22 复核时其 21 个脏文件与 5 个目标文件
  **交集为空**；16:23 脏文件增至 31 且仍在活动，故继续推迟。最终在**再次确认 5 个目标文件干净、
  且改动期间其提交（`840b960f`）与本批文件零交集**之后施工。
- **slice 3（已完成，2026-10-16）**：`code.*` 条件注册路径（`gate.add`）收敛。
  **本节原先写的"仍受同一阻塞（5 处注册点位于 `cmd/aicli/commands`）"是错的，已改正**：
  闸门与它的名单都在 `internal/tools`（`manager.go:48 codeGate`、`:96` 由
  `registerBuiltinToolkitTools` 返回、`:1056 codeToolGate.sync` 动态增删；原内联
  `[]codeToolSpec` 字面量在 `:629-635`，现为 `:601` 的构造表），与 slice 2 的宿主包无关。
  改动三处：①内联字面量 → 包级构造表 `codeToolRegistrations`
  （`codeToolRegistration{Name, Class, New}`，用**构造函数**而非共享实例——实例上会写
  sandbox / basePath / codeIndexResolver，共享会让多个 Manager 互相覆写配置）；
  ②`toolregistry.Entry` 新增 `CodeTool` 声明位并标注 5 个 code.* 条目——**此前它们刻意
  留零值，因此 slice 1 的双向闸门完全不覆盖 code.***（这正是本 slice 的真实缺口）；
  ③契约测试补 `TestCodeToolRegistrationTableMatchesRegistryDeclaration`（双向 + class
  来自已知集合 + **`Name == New().Name()`**）与
  `TestCodeToolAndToolkitBuiltinDeclarationsAreDisjoint`。
  **顺带挖出一个此前无人钉死的不变量**：`sync` 用 `spec.tool` 注册（注册表 key =
  `tool.Name()`）却用 `spec.name` 注销与记账（`Unregister` / `registered` / `classes`）；
  两者不一致会造成两个静默故障——①降档时注销不掉，工具在档位降级后仍对模型可见；
  ②`isCodeTool` 认不出该工具，`manager.go:356` 的第二道防线失效。现由该断言钉死。
- 验收：两侧冻结面 diff 归零（对齐既有 T2 不变量）——slice 1 与 slice 3 均以"注册成员
  集合不变 + 全量消费者包回归"证明（见 §8）。

### P3 — `disabled_groups` 接线 —— **裁撤：不实施**（2026-10-16 定案，证据已二次复核）

**裁决**：不新建 `tools.disabled_groups` 字段、不新建 `internal/toolgroups` 组表。族级门控改为
**复用既有的 `policy.Capability` 域**（把已有能力域暴露到 profile），取消新建第三套组词汇与注册表。

**证据（两层，相互独立）**：

1. **上游两文档已一致降级**：`progressive-tool-disclosure-plan.md` §10 裁决表把"层一
   `disabled_groups` + `internal/toolgroups`"判为**降级**，理由原文为"族级门控的机制与数据表
   都已存在（`Capability` 8 域 / `Kind` 6 类），再建第三套是重复；仅'配置便利性'不足以支撑新体系"；
   `profile-tool-groups-design.md` 的 v0.3 状态行同口径（"属**第三套同类机制**……裁决见
   `progressive-tool-disclosure-plan.md` §10"）。
2. **首要用例在该草案自己的规则下表达不出来**（`file:line` 已按当前代码逐条复核）：
   - `toolregistry/registry.go:162-166`：`code.*` 五个条目归 `GroupRead`
     （slice 3 之前为 `:153-158`，加 `CodeTool` 字段后位移 +9）；
   - `toolregistry/registry.go:251`：`GroupRead` 的 `Protected=true`（原 `:243`）
     （`registry_test.go:161-166` 已把 read/edit/shell 三组钉为 protected，并显式引用该草案 §3.3）；
   - `profile-tool-groups-design.md:79`：草案自定规则——`disabled_groups` 含 protected 组 ⇒
     `profile validate` **报错**，并要求"改用 allowlist/denylist 逐工具表达"。

   ⇒「关掉 `code.*`」（披露方案 §4.6 点 2 的用例）用 `disabled_groups` **无法表达**：要么校验报错，
   要么退化成逐工具名单（组机制归零）。`progressive-tool-disclosure-plan.md:297` 也已明文要求
   `code.*` 的可用性**不得**用 `disabled_groups` 表达（须与 `codeGate.sync` 的动态性正交）。

**替换路径与本 campaign 的关系**：替换机制（`policy.Capability` 8 域 + `ToolTaxonomy.Kind`/
`RequiresNet`）只有在"域表 == 注册表全量"成立时才有意义。本轮修掉的 2 个真 bug 正是**域表未覆盖
全量**的直接后果：

- `spawn_subagents` 未登记 `agent_management` ⇒ **授权逃逸**（关不住模型可见的批量委派入口）；
- `code.*` 被名字启发式（含 `search`）误判为需要 `network` ⇒ 禁网 profile 下派生子能力面被**误拒**。

两者已由新增守卫 `policy/taxonomy_registry_coverage_test.go`（语料取自 `toolregistry.Entries()`、
不硬编码数量）钉成不变量：**注册表条目必须在策略表中声明；网络判定只认声明、不认名字**。

**替换路径的现状（2026-10-16 复核，**修正原稿的"残留"表述**）**：`tools.disabled_capabilities`
（`profile/spec.go:87`，合法域取自 `policy.AllUserDisablableCapabilities()`）**已落地并端到端接线**
——profile 双镜像 + Merge 并集 + validate 域名校验 → `profileinput.BuildToolExecutionPolicy`
（未知域报错；翻译为 `SetCapabilityScope(全集−disabled)`；`read_only`/`exec_shell` 为底座域不可禁用）
→ 双落点护栏 `TestProfileToolPolicyDisabledCapabilitiesEquivalence`；能力域 e2e
`profileinput/disabled_capabilities_test.go`（`network`/`background_task` 不在允许集、
`read_only`/`exec_shell`/`write_fs`/`agent_management` 在；被撤下的域再 `AllowCapabilities` 报错；
未知域报错；无声明保持 nil 零改动）。披露方案 §10.3 第 4 条已记为"可选 → 已实施（2026-10-15）"。
**本 campaign 负责的是它成立的前提**：域表覆盖注册表全量、网络判定不认名字——否则域门控会
漏放（`spawn_subagents` 授权逃逸）或误拒（`code.*` 被当需要 network）。
（原稿此处写"是独立提案、不在本 campaign 范围"**不准确**，已按代码复核改正。）

**同线的另一条（prompt 撤段）也已实现，但机制与 §10.3 原文不同**：§10.3 第 2 条要求"6 处 guidance
的注入条件改为'该能力域已启用'"，实际落地是**按主题工具是否对模型可见**撤段
（`prompt/tool_guidance.go` 的 `ToolGuidanceFacts`，工具名语料取自
`toolregistry.CallableNames(...)`）——效果等价且更直接（域被禁用 ⇒ 工具不可见 ⇒ 撤段），
并额外覆盖"被 allowlist 裁掉"的情形。

---

## 4. 不变量（在既有 T1–T5 之上新增）

| # | 不变量 |
| --- | --- |
| T6 | 工具的身份/分组/暴露面只有**一个真源**（`toolregistry`）；`policy`、`toolsurface`、`toolgroups`、前端不得各自维护名单 |
| T7 | 元数据接口输出的 `count` 必须等于**下一 turn 请求体的 tool_count**（同一 catalog 来源 + 同一 `Classify`），观测数字与请求数字不得再分叉 |

---

## 5. 验收

### 5.1 单测

- `toolregistry`：每个 builtin 必须有 `Group`/`Source`/非空 `Exposure`；
  别名归一（`read→view`、`rg→grep`、`bash/execute_shell_command→shell`）；
  `orchestration` 组与 `toolbroker.IsBrokerTool` 按**两个方向**钉死，两侧语料都是**派生的**：
  ① `TestBrokerDispatchedNamesAreLabelledOrchestrationInRegistry`——凡 broker 派发的名字
  （**含别名**）必须归 `orchestration` 组（语料取自注册表、判定用 broker 自身函数）；
  ② `TestRegistryKnowsEveryBrokerCanonical`——broker 真源表 `brokerToolCanonicalNames` 的每个
  规范名必须在注册表里登记为**规范名**且归 `orchestration` 组（另含表内重复项守卫与语料下限）。
  但"**registry orchestration ⊆ broker 工具**"故意不成立：`spawn_subagents` 由
  `internal/agent/loop.go:8342` 自建定义、`:3408` 自行派发，不是 broker 工具但同属编排族——
  原稿写的"双向集合一致"经实测**不成立**，已改正（见 §8）；
  `IsEssential` 与 `policy.IsRuntimeOwnedEssentialTool` **逐名等价**（P1 后改为同一实现）。
- `runtimeapi`：`/runtime/tools` 响应含 `groups`/`filtered`；`/catalog` 无会话依赖；
  **请求面 ⊆ 注册表**：真实装配路径产出的请求面里，非 MCP 包装的每个名字都必须能在注册表查到、
  且用**规范名**拼写（`TestListSessionRuntimeTools_RequestSurfaceNamesAreCanonicalAndRegistered`，
  见 §8）——这是"单一真源"的端到端定义，也是 `spawn_subagents` 目前**唯一**的闸门。
- `policy`：`toolregistry` 的**全量**条目必须在策略表声明（无 `undeclared`），且`CapNetwork`
  只由声明决定、不得由名字启发式产生（双向相等）；缺陷修复点单独钉死
  ——`taxonomy_registry_coverage_test.go`（本轮新增，见 §8）。
- `tools`：`code.*` 条件注册表与 `toolregistry` 的 `CodeTool` 声明位**双向相等**
  （`builtin_registry_contract_test.go` 的 `TestCodeToolRegistrationTableMatchesRegistryDeclaration`）：
  任一侧单独增删即红；并要求 `Name == New().Name()`（`codeToolGate.sync` 的注册 key 与
  注销/记账 key 必须同名）、`Class` 取自已知集合、`CodeTool` 与 `ToolkitBuiltin` 两个声明位
  互斥（`TestCodeToolAndToolkitBuiltinDeclarationsAreDisjoint`）。见 §8。

### 5.2 端到端（P0 完成后）

- 真实 `runtime-server` 上对会话 `session_20261016084658_OUD1k1j0` 请求 `/api/runtime/tools`：
  `count == 22`，且 `groups` 中 `orchestration.tool_count == 12`；
- 与 http 日志 `001_request_provider_wrapper.json` 的 `tool_count = 22` **一致**
  （当前缺口：`/tools` 显示 10）→ 不变量 T7 达成。

### 5.3 回归范围

- `go test ./internal/policy/ ./internal/toolsurface/ ./internal/api/runtimeapi/ ./cmd/aicli/commands/`
- `go vet` 干净；
- 内存受限构建：`GOFLAGS=-p=1`、`GOGC=40`、`GOMAXPROCS=2`。

---

## 6. 风险与对策

| 风险 | 对策 |
| --- | --- |
| 把元数据写进 `ToolDefinition.Metadata` 会推高 token 估算（`loop.go:4334-4338` 的既有教训） | 元数据只在**观测端点的响应**里 enrich，**不写回**参与请求的定义；请求侧保持现状 |
| 注册表与 `toolkit.Registry` 双名单漂移 | `toolregistry` 单测断言"builtin 集合 == `registerBuiltinToolkitTools` 实际注册集合"；`code.*` 侧同理由 slice 3 的双向断言钉死（不再依赖人工同步） |
| 端点增强破坏既有前端/测试契约 | `tools`/`count`/`source` 字段保持向后兼容，只**新增** `groups`/`filtered`/`metadata` 子字段 |
| `/catalog` 暴露工具全貌（含未启用工具） | 只读、无执行语义；如需可加 `?exposure=` 过滤 |

---

## 7. 建议的最小可提交切片

若只想先要"前端能查配置"这一件事，**P0 的第 1+3 步**即可独立交付：
`internal/toolregistry` + `GET /api/runtime/tools/catalog`。
它零执行语义改动、不动冻结面、不动策略判定，却直接满足"统一管理 + 分类元数据 + 前端可查"。
P1/P2/P3 可随后续 profile 组功能一并推进。

---

## 8. 施工与验证记录

### 2026-10-16 — P0 第 1+3 步（§7 最小可提交切片）已落地

- **§2.2-B** `GET /api/runtime/tools/catalog`：
  - 实现 `internal/api/runtimeapi/tool_catalog_handlers.go`（`ListBuiltinToolCatalog` + `toolCatalogTool/Group/Response` DTO + `toolExposureStrings`）；
  - 路由注册 `internal/api/runtimeapi/handler.go:1102`（`/validate` 与 Profiles 块之间，先例 `GetScheduleCatalog`），URL `/api/runtime/tools/catalog`；
  - 单测 `internal/api/runtimeapi/tool_catalog_handlers_test.go`：`..._ProjectsRegistrySingleSource`、`..._AliasesRideWithCanonical`、`..._GroupCountsComeFromSameEntries` → 3 PASS；
  - 语义：只列 canonical（别名随 canonical 出行）、组计数之和 == `count`、`shell→bash/execute_shell_command`、`view→read`、`grep→rg`、`read/edit/shell` protected、`orchestration` 不 protected；`exposure` 复用单一渲染点；
  - 刻意**不含** `enabled`/`filtered`：会话生效语义归 §2.2-A，避免该端点变成第二套生效判定。
  - 本切片零执行语义改动、不动冻结面、不动 token 估算路径（§6 风险表第 1 条规避）。

### 2026-10-16 — 旧别名断言漂移修复（战役既有红转绿）

生产引导串已改用 `toolregistry` **规范名**，但 6 处镜像测试仍断言**别名**，使 §5.3 回归范围内两个包变红。按规范名对齐（未动任何生产语义）：

| 文件:行 | 旧断言（别名） | 新断言（规范名） | 注册表依据 |
| --- | --- | --- | --- |
| `internal/api/runtimeapi/session_agent_controller_test.go:1573` | `resolve_agent_approval` | `subagent_approve` | `toolregistry/registry.go:197` |
| `internal/api/runtimeapi/supervision_read_result_tool_test.go:106` | `read_agent_events` | `subagent_events` | `registry.go:189` |
| `internal/api/runtimeapi/supervision_read_result_tool_test.go:122` | `wait_agent` | `subagent_wait` | `registry.go:188` |
| `cmd/aicli/commands/chat_actor_message_semantics_test.go:119` | `resolve_agent_approval` | `subagent_approve` | 同上 |
| `cmd/aicli/commands/chat_supervision_result_tools_test.go:101` | `read_agent_events` | `subagent_events` | 同上 |
| `cmd/aicli/commands/chat_supervision_result_tools_test.go:107` | `wait_agent` | `subagent_wait` | 同上 |

同源旁证：生产串 `internal/supervision/agent_result.go:408-409` 本就是规范名，且 `internal/supervision/agent_result_test.go:95-96` 早已断言规范名（该包一直是绿的）——漂移只存在于 runtimeapi / commands 两份镜像测试。

**归因方法（可复现）**：把 `handler.go` 还原到 HEAD（该文件在战役基线里**未被修改**）后重跑这三个测试，**同样失败** → 证明与本次改动无关；恢复后按 sha256 校验字节一致（`FE0F3D75…`）。

### 2026-10-16 — §5.3 回归实测

| 目标 | 结果 |
| --- | --- |
| `go test ./internal/policy/ ./internal/toolsurface/` | ok 1.307s / 0.608s |
| `go test ./internal/api/runtimeapi/` | **ok 48.024s**（修复前 3 FAIL） |
| `go test ./internal/toolbroker/ ./internal/toolregistry/ ./internal/tools/` | ok 25.721s / 0.905s / 5.772s |
| `go test ./cmd/aicli/commands/` | 默认并行 1 FAIL；加 `-parallel 1` → **ok 163.279s，零 FAIL** |
| `go vet ./internal/api/runtimeapi/` | 干净 |

**`cmd/aicli/commands` 那条 FAIL 的纠正口径（2026-10-16 按原始日志复核后改写）**：
失败**不是等待超时**——该测试 `0.10s` 即失败，日志里的原始报错是不变量断言：

```
chat_runtime_events_terminal_projection_test.go:778: streaming range ledger is inconsistent:
{CellID:2 Revision:2 Kind:assistant Phase:1
 Source:STREAM-FINAL-TAIL-01 … STREAM-FINAL-TAIL-40 …
 HistoryCommitBlocked:false Stable:{Start:0 End:0} Enqueued:{Start:0 End:0} Acked:{Start:0 End:0}}
```

即 40 行尾段已产生且 `Revision:2`，但 Stable / Enqueued / Acked 三区间**全为 `{0,0}`**（尾段未登记进账本）。
`cmdfull.log` 统计：`DATA RACE=0`、`panic=0`、FAIL 行 = 1。同一棵树上 13:54 默认并行 FAIL、
13:58 `-parallel 1` **`ok 163.279s`**。**根因未定论**（记账竞态 vs 顺序假设，均需独立复现），
但「与战役无关」有直接证据：该测试文件 mtime `10-07 20:41`、账本实现
`cmd/aicli/ui/cell/active.go` mtime `07-31 15:22`，战役从未触碰二者；战役在此包只改了
`chat_actor_host.go` / `chat_agent_screen.go` / `chat_approval_explain.go` 及若干测试
（`cmd/aicli/ui/cell/tool.go` 仅把显示名改走 `toolregistry.IsSpellingOf("shell", …)`）。
⇒ **预存在的并行载荷敏感缺陷，不属本战役范围，本轮只记录不修**；`-parallel 1` 是当前可信验证口径。

### 2026-10-16 — 后台 job 判定口径纠正（重要）

后台 job 行显示的 `exit_code=0` **不可当作 go 的判定结果**：脚本形态
`go test … > x.log 2>&1; Write-Output "…=$LASTEXITCODE"` 的**进程**退出码来自末尾的
`Write-Output`，恒为 0。判定必须读日志内容，或读回显出来的 `$LASTEXITCODE` 值。

- `job_918bb2f7fb…`（`cmd/aicli/commands` 默认并行）行显示 `exit_code=0`，但 `cmdfull.log` 实为 `FAIL`。
- `job_31cf34fd31…`（`-parallel 1`）行显示 `exit_code=0`，`cmdparallel1.log` 为 `ok 163.279s`。
- `job_0167b47230…`（`go build ./...`）行显示 `exit_code=0`，**实际输出是 OOM 崩溃**：
  `# cmd/aicli/commands` → `runtime: VirtualAlloc of 8192 bytes failed with errno=1455`
  → `fatal error: out of memory`，崩在 `cmd/compile`。⇒ **全仓 `go build ./...` 仍不可用**，
  单目标构建才可行（历史证据：`cmd/aicli` 构建成功产出 `backend/aicli.exe` 55 539 712 B、
  `GO_EXIT=0`、v0.4.5 / buildTime `2026-10-16T00:33:50Z`，但该证据**先于**本轮改动，只作环境能力证明）。

### 2026-10-16 — 并行写入者观察（影响验收口径）

`cmd/aicli/commands` 在本轮实测（13:54 / 13:58）之后**已被第二个写入者继续修改**：14:06–14:13
落地了完整的 `/provider refresh` 子命令（新建 `chat_provider_refresh_command.go`，并接入
`chat_model_picker.go`、`chat_model_switch.go`、`chat_slash_argument_completion.go`、
`chat_slash_command_catalog.go`、`chat_runtime_command_registry.go`）。该写入者不属于本战役。

因此：**`cmd/aicli/commands` 的绿/红结论只对 13:54–13:58 的树状态有效**，之后该包必须重新实测；
另需注意 `p22ac`（14:05–14:12 写 `toolregistry/predicates.go`、`toolsurface/observation.{go,_test.go}`、
`session_runtime_handlers.{go,_test.go}`、`chat_tools_screen.go`）与第二写入者在同一包
`cmd/aicli/commands` **存在交叠**，验收时须检查二者是否互相覆盖命令注册项。

### 2026-10-16 — P0 步骤 2（§2.2-A + §2.2-C）状态

**已完成**（跨包：会话级 `/api/runtime/tools` 扩展 `groups[]/filtered[]/metadata{}` + 目录来源同源化）。
验收点即 §5.2 不变量 T7 的 `count == 22` 与 `orchestration.tool_count == 12`；其闭合方式、
本环境不可复现性与残留 R1 的处置，见本节末「2026-10-16 — 收口：T7 端点对齐测试 + R1 定案」。

（原记录：施工中，由子代理 `p22ac` 承担，本轮结论不作完成声明。该子代理被 30 分钟批次
截止线杀死（`error_class=deadline`）且 `no_result_recorded`，故改由父会话接手完成核验——
见下文「终局核验」与「收口」两节。）

**前置只读复核（不等子代理，先按设计读码）**：

- 合规：`internal/toolsurface/observation.go`（共享构建点）显式声明「分类事实只有两个来源」——
  来源账本走 `Ledger.Classify`（`observation.go:110`，单一实现点）、身份/分组/暴露层走
  `toolregistry.LookupLive`（`:134`）与 `toolregistry.Groups()`（`:175`）；`Enrich` 只写
  `cloneMetadata` 出来的副本（`:105`），**不回写请求面定义**（token 估算路径不受影响）✓
- 合规：`filtered[]` **不做推断**——P0 只翻译已有单一事实来源的会话级 MCP 停用
  （`session_runtime_handlers.go:545` `sessionRuntimeFilteredTools`，名字口径 `mcp:<server>`），
  `profile_deny` / `group_disabled` / `host_read_only` 明确标注为后续期次落地，
  `Group.Enabled` 在 P0 恒 `true`（`observation.go:56-59` 注释）——**没有为凑字段虚构数据** ✓
- 合规：响应体**可加不可改**（`session_runtime_handlers.go:524-535`：`tools`/`count`/`source`/`session_id`
  语义与顺序不变，仅新增 `groups`/`filtered`；`mcp_session` 保持条件存在）✓
- §2.2-C 主路径到位：第 2 条路 `sessionLiveRequestSurface` 经 `agent.CollectToolCatalogDefinitions`
  （与 inline web turn 同一装配点）→ `toolsurface.Build(catalog, Ledger{Broker: apiAgent.GetToolBroker()})`
  （`session_runtime_handlers.go:637`）——**真实账本 + broker 源**，即 §1.6 缺口的修复点 ✓
- **残留风险 R1（待核）**：`ListSessionRuntimeTools` 传给 `Enrich` 的仍是空账本
  （`session_runtime_handlers.go:523`）。对第 1/2 条路无害（快照与 `Build` 已写入 `surface_source`，
  `Enrich` 第 107 行只在**缺失**时补），但**第 3 条旧回退路**（无 runtime config 时由
  `runtimeServerToolSurfaceForSession` 取 MCP+goal 面）的定义若无 `surface_source`，
  `Ledger{}.Classify` 会默认判为 `core` —— 即「编排面误标 core」在回退路复现。
  处置需二选一：回退路也带账本，或加测试固定该行为并写成已知限制。

**验收待办（子代理产出后逐条核）**：

1. **T7 实测**：会话级 `GET /api/runtime/tools` 的 `count == 22`、`groups[]` 中
   `orchestration.tool_count == 12`。请求侧证据已钉死：
   `C:\Users\vince\.aicli\chat-logs\2026\10\16\session_20261016084658_OUD1k1j0\http\001_request_provider_wrapper.json`
   （mtime `2026-10-16T08:47:16`，121 182 B）第 22 行与第 47 行均为 `"tool_count": 22`。
2. R1 的处置（见上）。
3. `Enrich` 不回写请求面的测试覆盖（防止后续有人把 metadata 写回 `def.Metadata` 同一底层 map）。
4. `cmd/aicli/commands` **必须重新实测**——第二写入者已在 13:58 之后继续改该包；且 `p22ac`
   与之在同包有文件交叠（`chat_tools_screen.go` vs 命令目录文件），需确认没有互相覆盖命令注册项
   （`chat_slash_command_catalog.go` / `chat_runtime_command_registry.go`）。

### 2026-10-16 — P0 步骤 2 产物预核验（子代理仍在跑，只读读码，不作完成声明）

`backend/internal/toolsurface/observation_test.go`（14:17:41，6 995 B）已覆盖的关键不变量：

- **输入不被改写**（含共享 metadata map）：`:42-44` 断言 `input[0].Metadata` 仍为 `nil`、
  `input[1].Metadata` 保持原值 —— 上列待办 3（`Enrich` 不回写请求面）**已有测试** ✓
- 账本已给的 `surface_source` 被保留、仅缺失时补 `core`：`:28-30`（即 R1 的现状行为已被测试固定）
- 退役名不借用现役条目事实（外部 MCP 恰好同名时不套类别）：`:59-71` ✓
- 未登记外部工具不虚构分组、不计入任何组计数：`:89-90` ✓
- P0 `enabled` 恒 `true`；`group_disabled` 作为 P3 事实已能被 `groups[].enabled` 反映：`:94-95`、`:119-131` ✓
- 空裁剪是 `[]` 而非 `null`：`:113-116` ✓
- 空目录不虚构 payload（`Observe` 返回 `ok=false`）：`:144-146` ✓

`backend/internal/api/runtimeapi/session_runtime_handlers_test.go:415-474`（T7 面）已断言：
`groups[]` 长度 == `len(toolregistry.Groups())`、read 组 `Protected=true`、orchestration 组
`Protected=false`、每工具带 `surface_source`/`group`/`essential`/`exposure`、原始 body 含 `"filtered":[]`。

**遗留（子代理终结后核）**：

- T7 的**精确数值尚无单测钉死**：`session_runtime_handlers_test.go:456` 用的是
  `GreaterOrEqual(orchestration.ToolCount, 1)`，且没有 `count == 22` 断言。故 T7 的
  `count == 22` / `orchestration.tool_count == 12` 必须由「钉死的请求侧证据（`tool_count`
  22 ×2）」＋「端点实测」共同构成；若子代理未补精确断言，验收时需补一条或做端点实测。
- R1（第 3 条回退路的空账本 → 默认 `core`）仍无测试或已知限制说明覆盖。

### 2026-10-16 — P0 步骤 2 终局核验（父会话接手；子代理被批次截止线杀死）

**子代理结局**：`p22ac` 被 30 分钟批次截止线硬杀（`error_class=deadline`，`elapsed_ms=1 800 061`，
`context deadline exceeded`），**无耐久结果**（`no_result_recorded`）。最后写入 `14:17:41`，
其后约 11 分钟无任何文件写入 ⇒ 判定它死在「跑验收测试」环节：**改动已落盘，验证结论为零**。
父会话已接手核验（此时本机无并发重型进程，内存争用解除）。子代理已 `close`，槽位释放。

**构建**（`GOGC=15` / `GOMAXPROCS=2` / `GOFLAGS=-p=1`，以下同）：

- `go build ./internal/toolregistry/ ./internal/toolsurface/ ./internal/api/runtimeapi/` → `BUILD_EXIT=0` ✓
- 仍**不可**全仓 `go build ./...`（OOM，见上文判定口径纠正）。

**单测（全部 `-count=1`）**：

| 目标 | 结果 |
|---|---|
| `internal/toolregistry` | **8/8 PASS**，`ok 0.465s` ✓ |
| `internal/toolsurface` | **13/13 PASS**，`ok 0.653s` ✓（含新增 7 条 observation 测试） |
| `internal/api/runtimeapi`（定向 `TestListSessionRuntimeTools`） | **7/7 PASS** ✓ |
| `internal/api/runtimeapi`（整包） | **`ok 44.520s`** ✓ |
| `cmd/aicli/commands` | **`ok 172.949s`** ✓ |
| 契约面：`toolbroker` / `policy` / `skill` / `toolargs` / `toolkit` | `ok 24.627s` / `1.871s` / `3.107s` / `0.838s` / `3.599s` ✓ |
| §5.3 回归面：`agent` / `agentcontrol` / `supervision` / `output` / `toolresult` / `prompt` / `background` / `chatcore` / `team` | `19.237s` / `7.952s` / `3.244s` / `1.312s` / `0.666s` / `1.303s` / `60.952s` / `1.166s` / `4.887s` 全绿 ✓ |

两点由本轮实测澄清：

- **同包交叠未造成互相覆盖**：第二写入者的 `/provider refresh` 与 `p22ac` 的 `chat_tools_screen`
  改动在 `cmd/aicli/commands` 共存且全绿（原「验收待办 4」的担忧证伪）。
- 历史疑点 `TestFinalizeAgentWaitResultProvidesSchedulingGuidance` 的 FAIL **未复现**，
  `toolbroker` 整包绿（原切片结论作废）。

**§2.2-C「端点显示与请求携带同源」已结构证实**：
`internal/agent/tool_surface_binding.go:101 CollectToolCatalogDefinitions` 是唯一采集点，两个宿主同调它——
runtime-server `session_runtime_handlers.go:637`、aicli TUI `chat_tools_screen.go:111`
（`localToolsScreenAssembly`，同一 `buildLocalChatAgent`）。

**仍未闭环（诚实标注，不宣称完成、不造假断言充数）**：

- **T7 的端点侧精确数值**（`count == 22`）无单测钉死；且该会话 http 日志只存了 provider
  请求/响应（`session_20261016084658_OUD1k1j0\http\` 仅 2 个文件），**未记录 `/tools` 响应**。
  故 T7 目前 =「请求侧钉死证据（`tool_count` 22 ×2）」＋「同源结构证实」＋「形状单测」，
  **精确端点数值仍需一次实机探针**才能宣告闭环。
- R1（第 3 条回退路空账本 → 默认 `core`）仍无测试或已知限制说明覆盖。

### 2026-10-16 — 收口：T7 端点对齐测试 + R1 定案

**T7 收口方式（不虚构数字）**：新增
`backend/internal/api/runtimeapi/session_runtime_handlers_test.go::TestListSessionRuntimeTools_EndpointMatchesLiveRequestSurface`
—— 不做硬编码计数（不同宿主/配置下目录大小本就不同，硬写 `22` 等于造假），而是把 HTTP payload 的
工具名序列与**真实请求装配点** `sessionLiveRequestSurface(...)`（与 inline web turn 同一
`buildSessionAgentForContext → CollectToolCatalogDefinitions` 路径）**逐名逐序**对齐，
并断言 `count == len(请求面)`。首跑 **PASS**（`TestListSessionRuntimeTools` 组 8/8 PASS，`ok 2.561s`）。
任何「端点显示 ≠ 请求携带」的回归（漏项/多项/错序）都会被这条测试抓住。

**T7 仍不能宣称「端点实测 22」**：本机无承载该 runtime API 的服务在监听（候选端口中 4003 对
`/api/runtime/sessions` 返回 404），且 T7 那个 08:46 的会话已过期、MCP 未必在线 ⇒
**实机探针在本环境不可复现**。故 T7 的闭合依据 = 请求侧钉死证据（`tool_count` 22 ×2）＋
同源结构证实 ＋ 上述端点-请求逐名对齐测试；`count == 22` 这一**具体数值**目前只有请求侧直接证据。
若需数值级闭环，须在有 MCP 的真实运行时重放一次会话并抓 `/tools` 响应。

**R1 定案：已知限制（P0 明确接受），就地注释 + 既有测试钉死**。不改代码——在回退路手拼
`CapabilityTools` 名单会复活「手抄表漂移」（正是本战役的靶子）；正解是 P1 把分类事实收敛到
`internal/toolregistry` 后按 registry 补标。处置落点：

- 代码注释：`session_runtime_handlers.go` `ListSessionRuntimeTools` 的 `Enrich` 调用点（R1 段）；
- 行为钉死：`internal/toolsurface/toolsurface_test.go::TestClassifyWithoutBrokerKeepsCore`
  （`Ledger{}.Classify(job_start) == core`）；
- 影响面：仅「无 runtime config」的降级宿主；其面仍是宿主真实携带的 MCP+goal 面
  （名单/数量不失真），退化的只是非 MCP 条目的来源标签。

**P0 步骤 2 判定**：实现与验证均完成。T7 的数值级闭环与本环境不可复现性、R1 的处置，
均已按上述方式留证，不再作为挂起待办。

---

### 2026-10-16 — P1（`Essential` 名单去重）已落地：修掉一个真红 bug + 迁移审计

**结论**：P1 的**委托主体早已在磁盘上**（`tool_policy.go` 的 essential switch 已删，改为
`toolregistry.IsEssential` 转发；`IsDelegation` / `IsWriteLike` / `IsShellLike` 同批一并委托）。
本轮补的是它缺的另一半：**等价审计 + 回归验证 + 口径澄清**。

#### 发现（本轮核心）：P1 不是「换个地方查表」，而是修掉一个真红 bug

`git show HEAD` 基线两侧方向**相反**：

- 基线 `policy.normalizeToolName` 折向**旧拼写**（`job_start`→`background_task`、
  `spawnagent`→`spawn_agent`），essential switch 再按**旧拼写**匹配；
- 而 `toolbroker` 广播的是**规范名**（`broker.go:61 ToolSubagentSpawn = "subagent_spawn"`、
  `:76 ToolTeamSpawn = "team_spawn"`；旧拼写注释原文是 "stay callable as aliases"）。

⇒ 基线 `IsRuntimeOwnedEssentialTool("subagent_spawn") == false`。窄 allowlist 下
`filterPolicyBlockedToolDefinitions` 会把这些委派入口的**定义从 provider 请求里静默摘掉**，
而 prompt 仍在教模型委派——正是 `policy/supervision_tool_registry_test.go:14-16` 记载的
「Definitions 里有、模型却看不到」的假接入。既有 `toolbroker` 交叉校验测试只覆盖巡检族
（`advertisedSupervisionToolRegistry` 4 个 + retired 3 个），故这个 bug 一直没被抓住。

#### 本轮新增的两条可回归断言（均首跑 PASS）

1. `backend/internal/policy/essential_equivalence_test.go` —— **迁移审计**（不是第二份名单）：
   - 把 P1 前的实现**逐字冻结**为基准（`legacyP1Normalize` / `legacyP1Essential`，取自
     `git HEAD`，注释标明「只作审计基准，不得跟随真源演进」）；
   - 语料**从 `toolregistry.Entries()` 派生**（规范名 + 现役别名 + 旧表独有拼写 + 负对照），
     不含任何手写必需名单；
   - 三条断言：①名字级**零丢项**（旧表必需的名字今天仍必需——最危险的方向）；
     ②**身份级集合相等**（折到 `Canonical` 后工具集合逐一相同，即迁移只改名字可达性）；
     ③每处**名字级放宽都必须由 `Entry.Essential` 声明**（不许注册表之外冒出来的放宽）。
   - **测试自身即基线证伪**：它要求 `subagent_spawn` 出现在「放宽项」中（即
     `legacyP1Essential==false` 且今天 `==true`）⇒ P1 前此测试对规范名是红的。
2. `backend/internal/toolbroker/broker_advertised_essentials_test.go` —— **全广播面生存测试**：
   遍历 `Broker.Definitions()` 全部定义，断言「广播名必需性 == `Entry.Essential`」，并对必需项
   断言窄 allowlist 下 `AllowsDefinition` 存活；覆盖面远大于既有巡检族测试。

#### 验证（`-count=1`；内存约束前缀全部生效）

| 目标 | 结果 |
| --- | --- |
| 新增两条测试 | PASS（policy `ok 0.656s` / toolbroker `ok 1.519s`） |
| `internal/policy` 全包 | `ok 1.261s` |
| `internal/agent` 全包（§P1 指定） | `ok 18.155s` |
| `internal/toolbroker` 全包 | `ok 29.251s` |
| `go vet ./internal/policy/ ./internal/toolbroker/ ./internal/toolregistry/` | 干净（`VET_EXIT=0`） |

#### 口径澄清（对齐 §5.1 的「逐名等价」）

严格逐**拼写**等价**不成立**，也**不该**追求——基线把一部分规范名判成非必需，正是那个 bug。
正确口径是**身份级**：essential 的**工具集合**逐一相同（已断言）；名字级差异只能是
「补齐规范名/别名的可达性」，且每一处都由 `Entry.Essential` 声明。

#### 未做（避免越界）

`internal/agent` 只做回归、未改；`cmd/aicli/commands` 未动（该包有第二个写入者在改
`/provider refresh`）；P1 不触碰 `disabled_groups` 接线（属 P3）。

---

### 2026-10-16 — P2 slice 1（内置工具注册点收敛）已落地：单一构造表 + 双向漂移闸门

**范围**：`tools/manager.go` 里那段 25 个硬编码 `register(...)`（原注释块 `:544-577`）。
**不含**：5 处 `RegisterBuiltinToolFunction`（slice 2，见阻塞原因）、`code.*` 条件注册
（slice 3 —— **已于同日落地，见本文件末条日志**；本句保留为当时范围的历史记录）。

#### 设计修正（先证伪再动手）

本节原定做法「遍历 `toolregistry.Builtins()`」**按字面不可实现**，三条实测证据：

1. `toolregistry/builtinEntries` 有 ~66 条，含**非 toolkit** 条目：broker 编排面、capability
   （`get_goal`/`read_goal`/`update_goal`，`internal/goal/capability.go:16-17`）、agent 按需注册的
   `search_tool`（`toolkit.ToolSearchName`）、ADR-0004 code gate 的 `code.*`。无差别遍历会把
   「别人提供的工具」误判成 toolkit 内置工具。
2. 别名工具是**异构真实实例**且各有自己的 schema/描述：`NewBashTool()`
   （`toolkit/tools/bash.go:87`）、`NewExecuteShellCommandTool()`（`execute_shell_command.go:18`，
   独立 wrapper 类型与描述）、`NewReadTool()`（`view.go:96`）、`NewRGTool()`（`grep.go:317`）。
   构造参数只有宿主知道 ⇒ 构造表无法省掉。
3. **顺序风险不存在**：`toolkit.Registry.List()` 按工具名排序输出
   （`internal/toolkit/registry.go:114-116`）⇒ 注册顺序不可观察，不构成冻结面。

⇒ 落地形态：注册表加**正向**声明位 `Entry.ToolkitBuiltin`（"**有哪些**"；零值 = 本包**不**对
"由谁提供"作声明，调用方保持"不认识"语义），manager 保留构造表（"**怎么造**"），
两侧成员集合由测试**双向**钉死。真源没变，只是放到了能表达它的那一侧。

#### 落点

- `internal/toolregistry/registry.go`：`Entry` 新增 `ToolkitBuiltin bool`（含"为什么需要这一维"的
  注释，并列出留零值的四类宿主）；18 个规范条目置真：
  `shell`/`aicli_exec`/`view`/`glob`/`grep`/`ls`/`edit`/`write`/`append_write`/`multiedit`/
  `apply_patch`/`fetch`/`download`/`web_search`/`sourcegraph`/`artifact_read`/
  `openai_image_generate`/`todos`。`search_tool`、capability、编排面、`code.*` **一律留零值**。
- `internal/tools/manager.go`：25 个 `register(...)` → 单一构造表 `builtinToolkitRegistrations`
  （21 条无条件 + 1 条 `Condition: shouldRegisterOpenAIImageGenerateTool`）；原注释块的
  别名理由（read/rg/bash/execute_shell_command）逐条搬进表内，未丢信息；
  `_ = registry.Register(tool)` 的「重复注册静默忽略」既有行为原样保留。
  **实例集合逐个不变**（21 无条件 + 1 条件）。
- `internal/tools/builtin_registry_contract_test.go`（新，3 条断言）：
  1. `TestBuiltinRegistrationTableMatchesRegistryDeclaration`：构造表规范名集合
     **双向等于** `ToolkitBuiltin` 集合；并挡住"声明集合被清空导致真空通过"。
  2. `TestDeclaredLiveSpellingsAreRoutableOrRegistered`：对每个声明的现役拼写断言
     **要么有注册实例、要么被派发期归一**（`canonicalManagedToolName`）——即"声明面里
     不许存在模型调用会 `tool not found` 的死拼写"，这是 P1 那类病的通用闸门。
  3. `TestAliasInstancesAreRegistered`：显式钉住 `bash`/`execute_shell_command`→`shell`、
     `read`→`view`、`rg`→`grep` 必须各有独立实例；并钉住 `image_generate` **不得**有实例
     （走派发期归一，`manager_test.go:648` 已有同向断言）。

#### 验证（`-count=1`；内存约束前缀全部生效；不硬编码任何数量断言）

| 目标 | 结果 |
| --- | --- |
| 新增 3 条测试 | PASS（3/3） |
| `go vet ./internal/tools/ ./internal/toolregistry/` | 干净（`VET_EXIT=0`） |
| `internal/toolregistry` + `internal/tools` 全包 | `ok 0.470s` / `ok 4.301s` |
| `internal/toolkit` 全包 | `ok 3.407s` |
| `internal/agent` 全包 | `ok 19.419s` |
| `internal/toolsurface` / `internal/policy` | `ok 0.771s` / `ok 1.636s` |
| `internal/api/runtimeapi`（含 T7 端点对齐测试）/ `internal/toolbroker` | `ok 53.770s` / `ok 26.444s` |

#### 测试自身可证伪（红探针，已回滚）

新测试首跑即绿，故做了两次**故意变红**验证，证明它们真的在守不变量：

1. 删掉 `read` 构造行 → `TestDeclaredLiveSpellingsAreRoutableOrRegistered` 报
   「"view" 的现役别名 "read" 既没有注册实例、也不被派发期归一（模型调用它会得到 tool not found）」
   ＋ `TestAliasInstancesAreRegistered` 报「别名 "read" 必须作为独立实例注册到 "view"」。
2. 给非 toolkit 的 `search_tool` 加 `ToolkitBuiltin: true` → 双向断言报
   「注册表把 "search_tool" 声明为 ToolkitBuiltin，但 `builtinToolkitRegistrations` 没有构造行
   （该工具不会被注册，等于对模型不可见）」。

两次探针均按设计变红，回滚后 3/3 复绿（`PROBE_REVERT_EXIT=0`）。

#### 未闭环（诚实标注）

- **slice 2 阻塞**：5 处 `RegisterBuiltinToolFunction` 全在 `cmd/aicli/commands`，该包有本 campaign
  之外的第二个写入者（`/provider refresh`）在改；跨写入者改同一包会互相破坏，故推迟。
- **未证明**「注册面字节级零 diff」：本机无承载 runtime API 的服务在监听（同 T7 的环境限制），
  无法抓改动前后的 `/tools` 响应对比。当前证据为「实例集合逐个不变（代码级核对）＋ 全部消费者包
  回归绿（含 T7 端点-请求逐名对齐测试）」。真正的字节级对比需要在有 MCP 的真实运行时抓一次响应。
- `register` 吞掉 `registry.Register` 的错误（schema 非法时会**静默不注册**）是既有行为，
  本轮保持原样；若要收紧，应在 slice 2 一并把错误上报到装配日志。

---

### 2026-10-16 — 覆盖探针挖出 2 个真 bug（授权逃逸 + 误拒）已修；并给出 §P3 裁撤的两重证据

**背景**：上一条（P2 slice 1）留下的悬案是"策略表与 `toolregistry` 是否**全量**对齐"。本轮不靠
肉眼比对，而是写一次性覆盖探针（对 `toolregistry.Entries()` 逐条跑 `policy.Resolve(...)` 与
`controlPlaneToolCapabilities(...)`，把"未声明""声明与实算不一致"全部打出来）。探针给出 2 个真
缺陷，均已在生产代码修复（探针文件与脚本**已删除**，只留守卫测试）。

#### Bug 1 — 授权逃逸：`spawn_subagents` 不受 `agent_management` 约束

- 事实：`spawn_subagents` 是**模型可见**入口（`agent/loop.go:8342` 注册，
  `SpawnSubagentsToolName` 常量在 `agent/agent.go:614`），但 `policy/capability.go` 的控制面表
  只把它解析成 `read_only`。⇒ profile 写 `disabled_capabilities: [agent_management]` 时，
  **关不住**这个批量委派入口——与 `capability.go:119-123` 头注释记录的 ack_lifecycle 同类事故
  （"映射只能从一条路径到达 = 门控变死代码"）。
- 修复：`policy/capability.go` 新增 `case "spawn_subagents"` →
  `[]Capability{CapReadOnly, CapAgentManagement}`，与 `subagent_spawn` / `team_spawn` **同级登记**。
- 实测（探针 before/after）：修复前 `caps=[read_only]` → 修复后
  `caps=[read_only agent_management]`，与同族两个工具一致 ✓

#### Bug 2 — 误拒：`code.*` 因名字启发式被判需要 `network`

- 事实：`policy` 对未登记名字走 `Contains("search")` 一类启发式，`code_search` 因此被判需要
  `network`。危害方向是**误拒**（不是放宽）：`policy/capability_scope.go:73-79`
  `AllowCapabilities` 对派生出的子能力面做边界校验、越界即 error。生产可达路径：
  `agent/tool_vocabulary.go:175-182` 用 `task.ToolsWhitelist` **裸名字列表**调
  `CapabilitiesForTask(...)` → `resolveCapabilities`（`capability_scope.go:120-129`）→
  `DefaultCapabilityResolver.Resolve(EvalRequest{ToolName: name})`（**无 metadata**）。
  `code.*` 无 `DefinitionMetadata`（grep `func (t *codeXTool) DefinitionMetadata` = 空；
  先前把 `lsp_bridge.go:349-353` 归给 `code.*` 是**误判**，那属于 `lspServersTool`），
  所以必然落到启发式。
- 本地性证据（"不触网"是查出来的，不是拍脑袋）：`registry.go:153-158` 注释"符号索引（`code.*`，
  条件注册；随 grep 语义归 read 组）"+ `GroupRead`；`internal/tools/code_index_resolver.go:44-46`
  （只按 workspace root 解析，无 root 即 fail closed）、`:52-53`（"语义通道只借用诊断池的进程
  （单实例）"= 本地语言服务器进程）；`manager.go:628-638` 是 5 个 `code.*` 的条件注册点。
- 修复：`policy/taxonomy.go` 在 `update_goal` 之后补 `read_goal` 行
  （`Kind: types.ToolKindControl`、`ReadOnly: true`，与 `get_goal`/`update_goal` 同级）+ 注释块
  + 5 条 `code.*` 行（`Kind: types.ToolKindRead`、`ReadOnly: true`，不触网、不写盘）。

#### 新增守卫：`backend/internal/policy/taxonomy_registry_coverage_test.go`（新文件）

三条断言，语料**全部取自 `toolregistry.Entries()`**，不硬编码任何数量：

1. `TestRegistryEntriesAreAllDeclaredInPolicyTables`：每个注册表条目必须命中
   `LookupToolTaxonomy` 或控制面能力表，否则报 `undeclared`；含 `require.NotEmpty(entries)`
   vacuum guard（注册表被清空时测试必须红，而不是静默通过）；
2. `TestRegistryEntriesNeverInferNetworkFromName`：`declared`（`row.RequiresNet ||
   row.Kind == types.ToolKindNetwork`，或控制面表含 `CapNetwork`）必须**等于** `Resolve(...)`
   实际含 `CapNetwork`——双向，既防"该声明没声明"，也防"启发式偷偷加进来"；
3. `TestFixedDefectsStayPinned`：6 个名字必须恰为 `[]Capability{CapReadOnly}`；
   `spawn_subagents` 必须 = `[]Capability{CapReadOnly, CapAgentManagement}`，并与
   `subagent_spawn`/`team_spawn` 逐名等价。

#### 测试自身可证伪（红探针，已回滚）

(A) 删掉 `code.*` 的 taxonomy 行 ⇒ 断言 1 报 `undeclared`、断言 2 报 Network 不一致；
(B) 去掉 `spawn_subagents` 的 `agent_management` ⇒ 断言 3 红；
(C) 往控制面表塞 `CapNetwork` ⇒ 断言 2 红。

**方法论教训（本轮踩坑）**：探针 B 第一次跑是**假阴性**——脚本用字面量空格匹配
gofmt/tabwriter 渲染后的行，而对齐宽度会随键名长度变化。凡对 Go 源文件做文本探针，必须用抗对齐
正则（如 `(?m)^[ \t]*"key":[^\r\n]*\r?\n`），不要匹配行内空格序列。

#### 验证（`-count=1`；内存约束前缀 `GOGC=15 GOMAXPROCS=2 GOFLAGS=-p=1` 全部生效）

- `go vet ./internal/policy/...` ⇒ 0；
- 受影响消费者包全量回归（policy 是工具面过滤的上游，故按"所有消费面"跑，**8/8 exit=0**）：
  `./internal/policy/` ok、`./internal/tools/` ok 4.595s、`./internal/toolbroker/` ok 24.439s、
  `./internal/agent/` ok 17.246s、`./internal/api/runtimeapi/` ok 48.443s、
  `./internal/toolsurface/` ok 0.480s、`./internal/toolkit/` ok 3.242s、
  `./internal/profileinput/`（含 `disabled_capabilities` 端到端真实工具用例）ok 0.797s、
  `./internal/toolregistry/` ok 0.459s；
- `gofmt -l` 改动文件 ⇒ 空。

#### 未做（避免越界）

`cmd/aicli/commands` 未动（该包有第二写入者在改 `/provider refresh`）；把 `Capability` 域做成
profile 可声明字段（`progressive-tool-disclosure-plan.md` §10 替换方案本体）未做，属**独立提案**。

#### 同轮定案

§P3（`disabled_groups` 接线）**裁撤**：见 §3 的 P3 段落（本轮补写的两重证据）。

---

### 2026-10-16 — P2 slice 3（`code.*` 条件注册点收敛）已落地：单一构造表 + 双向闸门 + 钉死 `sync` 不变量

**起因：§3 原写的阻塞判断被证伪。** 原文称 slice 3"仍受同一阻塞（5 处注册点位于
`cmd/aicli/commands`）"。实读代码后该判断**是错的**：闸门与它的名单都在 `internal/tools`
—— `manager.go:48` 声明 `codeGate *codeToolGate`、`:96` 由 `registerBuiltinToolkitTools`
返回、`codeToolGate.sync` 负责按档位动态增删，而手写名单原为 `:629-635` 的内联
`[]codeToolSpec` 字面量。与 slice 2 的宿主包**无交集**，故不受第二写入者影响，可直接施工。

**改动（3 个文件）**：

1. `internal/tools/manager.go`：内联 `[]codeToolSpec` 字面量 → 包级构造表
   `codeToolRegistrations`（元素 `codeToolRegistration{Name, Class, New}`），`gate.add` 改为
   遍历该表。关键决定：存**构造函数**而非共享实例 —— 工具实例上会写 sandbox / basePath /
   codeIndexResolver，共享实例会让多个 `Manager` 互相覆写配置（多会话即触发）。
2. `internal/toolregistry/registry.go`：`Entry` 新增 `CodeTool` 声明位，标注 5 个 `code.*`
   条目；并把 `ToolkitBuiltin` 文档里"`code.*` 故意留零值"的旧说法改写为指向 `CodeTool`。
3. `internal/tools/builtin_registry_contract_test.go`：新增两条测试（见下）。

**真缺口（本 slice 的实质，与 slice 2 无关）**：`code.*` 5 条在 `toolregistry` 里**没有**
`ToolkitBuiltin: true`（对比同表图像工具、`todos` 都有），因此 slice 1 的双向漂移闸门
**完全不覆盖 `code.*`**（`declaredToolkitBuiltins()` 把它们排除在外），而它们的名字确实
手写在**两处**且互不钉死。这不是文档遗漏，而是 slice 1 当时的显式决定——现在补齐。

**闸门（2 条断言，语料取自两侧声明，不硬编码数量）**：

1. `TestCodeToolRegistrationTableMatchesRegistryDeclaration`：`codeToolRegistrations` 的
   `Name` 集合 == `toolregistry` 中 `CodeTool: true` 的集合（**双向**，任一侧单独增删即红）；
   `Class` 必须取自已知集合；且 **`Name == New().Name()`**。
2. `TestCodeToolAndToolkitBuiltinDeclarationsAreDisjoint`：同一条目不得同时声明两个位
   （否则 slice 1 与 slice 3 的闸门会互相掩盖）。

**顺带挖出一个此前无人钉死的不变量（有真实后果）**：`codeToolGate.sync` 用 `spec.tool`
**注册**（注册表 key = `tool.Name()`）却用 `spec.name` **注销与记账**
（`Unregister(spec.name)` / `registered[spec.name]` / `classes[spec.name]`）。两者不一致会
造成两个**静默**故障：①降档时注销不掉，工具在档位降级后仍对模型可见；②`isCodeTool` 认不出
该工具，`manager.go:356` 的第二道防线失效。现由断言 1 的 `Name == New().Name()` 钉死。

**测试自身可证伪（红探针，4 条，已回滚）**：脚本写在仓库外，每条探针都断言 `changed=True`
（替换真实发生）以防上轮那种"假阴性 no-op"。四个注入点**全部按设计变红**：删掉注册表侧的
`CodeTool` 声明（注册了却没声明）✓、在注册表侧多声明一条无人注册的 `code.*`（声明了却没人
注册）✓、改 `New()` 返回的工具名使 `Name != New().Name()`（不变量）✓、把某条 `Class` 改成
未知值（class 已知集合）✓。探针跑完文件哈希还原一致，末态复跑全绿。

**验证（`-count=1`；内存约束前缀 `GOGC=15 GOMAXPROCS=2 GOFLAGS=-p=1` 生效）**：

- `gofmt -l` 三个改动文件 ⇒ 空；
- 定向：`go test -run 'TestCodeTool|TestBuiltinRegistrationTableMatchesRegistryDeclaration'`
  `./internal/tools/` ok 1.952s、`./internal/toolregistry/` ok 0.728s；既有的档位行为测试
  （`TestCodeToolTiersRegisterByStaleness` 5 个子用例、`TestCodeToolEscapeHatches`）同时通过
  —— 它们正是走新表驱动的 `gate.add` 路径，等于**行为未变**的证明；
- 消费者包全量回归（工具面注册是所有宿主的上游，故按"所有消费面"跑，**9/9 exit=0**）：
  `./internal/tools/` 4.771s、`./internal/toolregistry/` 0.949s、`./internal/policy/` 1.806s、
  `./internal/toolkit/` 3.648s、`./internal/toolbroker/` 27.679s、`./internal/agent/` 19.184s、
  `./internal/api/runtimeapi/` 49.594s、`./internal/toolsurface/` 0.898s、
  `./internal/profileinput/` 1.363s。

**未做**：slice 2（5 处 `RegisterBuiltinToolFunction` 收敛为单一 helper）仍推迟，理由见 §3
的量化复核（目标文件与活动写入者交集为空，但不愿与**活动**写入者共用同一包）。

---

### 2026-10-16 — 派发面/分组面对账：补一条**派生式**闸门，并改正 §5.1 里不可能成立的"双向一致"承诺

**起因（验收承诺落空）**：§5.1 明文承诺"`orchestration` 组与 `toolbroker.IsBrokerTool` 集合一致
（双向断言）"。实查：`internal/toolregistry` 的 8 个测试里**没有**这条；真正存在的对账在
`internal/toolbroker/toolregistry_contract_test.go`，但它只钉**名字拼写**（规范名必须是
`normalizeToolName` 不动点、别名归一到所属条目、legacy 拼写仍可解析），**不钉分组归属**；
而且反向方向用的是**手抄的 35 个常量名单**——正是本战役要消灭的形态。

**先量化，再动手**（探针是临时 `main` 包，写在 `backend/tmp_registry_probe/`，跑完即删）：

| 观测 | 数值 |
| --- | --- |
| `broker.Definitions()` 条目数 | **9**（`ask_user_question` + `job_*`；team/subagent 族由 `DefinitionsForContext` 补） |
| `toolregistry` 中 `Group == orchestration` 条目数 | **44** |
| registry 里被 `IsBrokerTool` 认作 broker 工具的名字数（含别名） | **74** |
| 上述 74 个名字中分组 ≠ orchestration 的 | **0** |
| registry orchestration 中 `IsBrokerTool == false` 的 | **1**：`spawn_subagents` |

**结论：原稿的"双向集合一致"不可能成立，而且不是漂移。** `spawn_subagents` 由 `internal/agent`
自建定义并自行派发（`loop.go:8342` 构造 model-visible 定义、`:3408` 在循环里拦截调用），
broker 不接管它 ⇒ `IsBrokerTool == false` 是**设计**；它按族归类仍属编排面。可成立的只有单向：
**凡 broker 派发的名字必须归 `orchestration` 组**（否则 `/tools` 的 `surface_source`（走
`toolsurface.go:66` 的 `IsBrokerTool`）与 `/catalog` 的分组（走 `toolregistry.Group`）会对同一工具
给出两个矛盾的族标签）。

**改动**：

1. `internal/toolbroker/toolregistry_contract_test.go` 新增
   `TestBrokerDispatchedNamesAreLabelledOrchestrationInRegistry`：遍历 `toolregistry.Entries()`
   （toolregistry 是叶子包，不能反向 import broker），对每个条目的**规范名与全部别名**用
   `broker.IsBrokerTool` 判定，命中者必须归 `orchestration` 组；语料下限 `checked >= 40`
   （今天实测 74）防"什么都没扫到"的假绿。**语料完全派生、无手抄名单**：新增 broker 工具、
   或把 broker 认得的别名挂到别的组上，都会红。
2. 本文档 §5.1 该条改写为单向表述 + 反例说明（原稿"双向一致"已删除）。

**测试自身可证伪（红探针，3 条，已回滚）**：

1. 把 `job_start` 的组从 orchestration 改成 io ⇒ 红，报 **2** 处（规范名 `job_start` 与别名
   `background_task` 同时命中）——证明别名确实被扫到；
2. 把 broker 认得的别名 `task_output` 挂到 read 组的 `view` 上 ⇒ 红，精确指出
   `"task_output"（条目 view）…应归 "orchestration"`；
3. 抬高语料下限（`checked < 40` → `< 400`）⇒ 红，报文 `only 74 broker-dispatched registry names
   were checked`——证明下限守卫真的会拦假绿。

文件哈希还原一致，末态复跑绿。

**一次探针自身的错误（记录，避免重犯）**：探针 3 初版把判定改成
`if false && !broker.IsBrokerTool(name)`，本意是"扫不到语料"，实际效果是**短路成"全部条目都算
broker 派发"**，于是它打到的是违例路径（32 处违例）而不是下限守卫。教训：探针红了之后必须核对
**它红的理由**与设计它红的理由一致，不能只看 `exit=1`。

**验证（`-count=1`；内存约束前缀 `GOGC=15 GOMAXPROCS=2 GOFLAGS=-p=1`）**：

- `gofmt -l` 改动文件 ⇒ 空；
- 基线 `-run TestBrokerDispatchedNamesAreLabelledOrchestrationInRegistry` ⇒ 绿（0.00s）；
- 回归 **4/4 exit=0**：`./internal/toolbroker/` 27.383s、`./internal/toolregistry/` 0.868s、
  `./internal/toolsurface/` 0.941s、`./internal/api/runtimeapi/` 49.485s。

**已知残留（诚实登记）**：broker → registry 方向里"**新增 broker 工具而注册表完全不知道**"这一种，
仍靠 `TestRegistryKnowsEveryBrokerCanonical` 的**手抄 35 个常量名单**兜底（本轮的派生式闸门遍历的是
注册表，扫不到注册表里根本没有的名字）。要彻底去手抄，需 broker 侧暴露一份可枚举的工具名集合
（如 `Broker.BrokerToolNames()`）——属**独立小提案**，本轮不做。

> **同日下一轮已闭合**：见本文件末条日志（`brokerToolCanonicalNames` 真源表 + 两个测试改为派生）。

---

### 2026-10-16 — broker 派发面真源化：`switch` → 数据表，两条对账断言由手抄名单改为**派生**

**起因**：上一条登记的已知残留——broker → registry 方向"新增 broker 工具而注册表完全不知道"，靠
`TestRegistryKnowsEveryBrokerCanonical` 手抄的 35 个常量兜底；另有 `TestCanonicalFamilyRoundTrip`
也另抄了一份 35 个常量的名单。两份手抄名单正是本战役的靶子。

**关键观测（先查再动）**：`IsBrokerTool` 的实现是一个 **43 个常量的 `switch`**（`broker.go:241`），
且全仓库**只有这一处**枚举（已 grep 确认没有第二份重复名单）——所以它可以变成数据而不制造新漂移。

**改动（3 个文件）**：

1. `internal/toolbroker/broker.go`：新增包级真源表 `brokerToolCanonicalNames []string`（43 条，
   别名不入表）+ 派生查表集合 `brokerToolCanonicalSet`；`IsBrokerTool` 由 `switch` 改为
   `brokerToolCanonicalSet[normalizeToolName(name)]`。语义等价（同一批常量、同一归一入口），
   但**变得可枚举**——新增 broker 工具只改这一处，且立刻被对账测试看到。
2. `toolregistry_contract_test.go::TestRegistryKnowsEveryBrokerCanonical`：语料由手抄 35 个常量改为
   **遍历真源表**；并补三条此前没有的断言——必须归 `orchestration` 组、表内不得重复、
   语料下限 ≥30（防"表被清空"导致假绿）。
3. `tool_naming_contract_test.go::TestCanonicalFamilyRoundTrip`：同样改为遍历真源表（顺带把覆盖从
   35 条扩到全部 43 条，含 supervision 族）。

**红探针（4 条，全部按设计变红，已回滚，文件哈希还原一致）**：

| 探针 | 注入 | 期望红点 | 结果 |
| --- | --- | --- | --- |
| A | 真源表里塞 `"totally_bogus_tool"` | 反向对账：注册表不认识 | 红 ✓ `is not registered in toolregistry` |
| B | 表内重复 `ToolAskUserQuestion` | 去重守卫 | 红 ✓ `appears twice in brokerToolCanonicalNames` |
| C | 语料下限 30 → 300 | 下限守卫 | 红 ✓ `only 43 entries; the reconciliation would silently pass` |
| D | 把别名 `"background_task"` 混进真源表 | 不动点断言 | 红 ✓ `must be a normalizeToolName fixed point, got "job_start"` |

**探针自身的 bug（记录）**：探针 B 首跑 `changed=False`——脚本只按 CRLF 找锚点，而该文件是 LF，
替换根本没发生（等于什么都没测，却报了 `exit=0`）。改成 LF/CRLF 双锚点后红。教训：**探针必须先验证
"注入确实生效"**；`changed=False` 的探针结果一律视为无效，不能读成"没红 = 没问题"。

**工作树上下文（诚实登记）**：本轮改动的 3 个文件都落在 `internal/toolbroker`，而该包在本战役里
**已有更早、尚未提交的改动**（11:08–11:24 的拼写收敛 + `normalizeToolName` 委托
`toolregistry.Normalize`；14:47 的 `broker_advertised_essentials_test.go`）。本轮改动与它们
**不同区域、无重叠**（`git diff` 区块可核：我的区块是 `IsBrokerTool` 附近的 `+238,31` / `-239,6`），
但所有验证证据都是在**混合工作树**上取得的——与战役此前各轮口径一致。
（该混合状态已在提交 `3bc49a2f` 时收敛：见本文件末条日志。）
另：broker 侧剩下的 `switch normalizeToolName`（`broker.go:230` volatile-empty、`:1548`、
`supervision_tools.go:1213`、`team_tools.go:16`）都是**按行为分组**的子表（空回放缓存、
team 可见性、参数审计…），不构成第二份"工具身份"真源，故不在本轮收敛范围内。

**验证（`-count=1`；内存约束前缀 `GOGC=15 GOMAXPROCS=2 GOFLAGS=-p=1`）**：

- `gofmt -l` 三个改动文件 ⇒ 空；`go build` 受影响包 ⇒ 0；
- 定向对账 6 条断言 ⇒ ok 1.329s；
- 回归 **5/5 exit=0**：`./internal/toolbroker/` 25.268s、`./internal/toolregistry/` 0.768s、
  `./internal/toolsurface/` 0.841s、`./internal/api/runtimeapi/` 45.847s、`./internal/agent/` 18.041s。

**顺带观测到一条既有时序 flaky（非本轮引入，未修）**：
`TestBrokerExecuteWaitTeamTimeoutBillsNoProgressWindow`（`broker_team_wait_budget_test.go:103`，
`timeout_ms: 1` + 预算窗口记账）在**全包并发**下曾失败 1 次（`budget.observed()` 期望
`[]bool{false}` 实得 `nil`）；单测 `-count=3` 3/3 绿、全包复跑亦绿。证据表明与本轮无关：
`IsBrokerTool` 在 `broker.go` 内**零调用点**（只在包外被 `agent` / `toolsurface` 消费），
不在该用例的 `Execute` 路径上。

---

### 2026-10-16 — 请求面 ⊆ 注册表：把"单一真源"写成端到端断言（含一次**打空**的探针）

**动机**：战役的核心承诺是"注册表 = 工具身份单一真源"。已有闸门都钉**表与表**之间的一致性
（注册表 ↔ 内置构造表 / `code.*` 构造表 / 策略表 / broker 派发面），但**没有一条**从"模型实际
看到的目录"这一端反向检查。于是补一条端到端断言。

**先探针、后落地**：临时测试走真实装配路径 `handler.sessionLiveRequestSurface(ctx, session.ID)`
（与 inline web turn 同一个函数），观测到本 harness 的请求面 = **29 个名字**：
`ask_user_question`、`get_goal`、`job_*`(8)、`plan_*`(3)、`spawn_subagents`、`subagent_*`(12)、
`team_spawn`/`team_wait`、`update_goal`。其中 **2 个是 MCP 包装**（`get_goal`/`update_goal`，
带 `mcp_name`），**27 个非 MCP 全部已登记且用规范名**（0 缺失、0 别名拼写）——承诺今天成立。

**改动**：`session_runtime_handlers_test.go` 新增
`TestListSessionRuntimeTools_RequestSurfaceNamesAreCanonicalAndRegistered`——对请求面里每个
**非 MCP 包装**的名字断言：①能在 `toolregistry.Lookup` 查到（查不到 ⇒ 策略/分组会落到**启发式**，
即 P1 病类）；②`Canonical(name) == name`（否则"同一工具两个规范名"复发，而 `/catalog` 只列
canonical，会与 `/tools` 对不上）。另有语料下限 `checked > 10`（今天实测 27）防假绿。

**这条闸门的独有覆盖**：`spawn_subagents` —— 它不是 broker 工具（broker 闸门跳过它）、
也不是 toolkit 内置（slice 1 闸门跳过它），**此前没有任何闸门覆盖它**，本闸门覆盖。

**红探针（2 条，全部按设计变红，已回滚，哈希还原一致）**：

| 探针 | 注入（改注册表） | 期望红点 | 结果 |
| --- | --- | --- | --- |
| B | `spawn_subagents` → `spawn_subagents_renamed`（请求面仍是旧名） | 查不到 | 红 ✓ `is not in toolregistry` |
| C | `job_start` → `job_start_renamed` 且旧名降为别名 | 别名拼写 | 红 ✓ `is an alias spelling` |

**一次打空的探针（记录，重要）**：探针首版把靶子选成 `todos`（注册表里确有此条、还是
`ToolkitBuiltin`），两条探针**都没红**。原因不是断言太弱，而是 **`todos` 根本不在本 harness 的
请求面里**——该请求面只有编排族 + goal 能力，**不含** `view`/`shell`/`todos` 等 toolkit 内置。
于是先去打印请求面全名单，再换成语料内的靶子（`spawn_subagents`/`job_start`），两条立刻红。
教训：**红探针的靶子必须在语料内**；"没红"要先怀疑靶子选错。

**覆盖边界（不夸大）**：本闸门覆盖的是**本 harness 装配出的请求面**（29 个名字）；
toolkit 内置（`view`/`shell`/…）不在其中，它们由 slice 1 的"表 ↔ 表"闸门覆盖。

**验证（`-count=1`；内存约束前缀 `GOGC=15 GOMAXPROCS=2 GOFLAGS=-p=1`）**：

- `gofmt -l` 改动文件 ⇒ 空；
- 基线 + 末态复跑 ⇒ 绿；
- 回归 **3/3 exit=0**：`./internal/api/runtimeapi/` 52.594s、`./internal/toolregistry/` 0.896s、
  `./internal/toolbroker/` 26.684s。

---

### 2026-10-16 — 提交 `3bc49a2f`（工具身份单一真源，29 文件）＋ 提交前的**隔离快照验证**

**为什么先验证再提交**：工作树里同时存在**第二写入者**的未提交改动（`cmd/aicli/commands` 等）。
把本战役的文件单独提交，可能与"留在 HEAD 的另一半"组成一个**红快照**（被排除的镜像测试仍在断言
旧拼写）。故提交前先把暂存内容导出为补丁，在**隔离 worktree**（HEAD + 该补丁、不含任何其它未提交
改动）里构建并跑测试——这是"提交不红"的唯一可靠证明。

**第一次隔离验证就抓到问题（这一步没白做）**：只暂存 23 个文件时，隔离快照 **红**——
`internal/policy` 里 8 个用例失败：`TestNormalizeToolNameMapsReportTaskOutcomeAliases`
（期望 `report_task_outcome`、实得 `team_task_report`）、plan_enter 门控 6 例、
`TestDeriveGrantPatternForWriteStdinIsJobScoped`。根因：`policy/capability.go` 已折向规范名，
而**镜像测试**（`capability_test.go`、`engine_plan_enter_test.go`）与配套生产文件
（`grant_pattern.go` / `grants.go` / `plan_enter.go` / `plan_paths.go`）被我判成"非本战役"而排除。
补入这 6 个文件（合计 **29**）后隔离快照转绿。

**提交范围（29 文件，4076 插入 / 435 删除）**：`internal/toolregistry`（新包：registry/predicates/
registry_test）、`internal/tools`（构造表 + 契约测试）、`internal/toolbroker`（`broker.go` +
`toolregistry_contract_test.go` + `tool_naming_contract_test.go` + `broker_advertised_essentials_test.go`）、
`internal/policy`（taxonomy / tool_policy / capability / grants / plan_* + `essential_equivalence_test.go`
+ `taxonomy_registry_coverage_test.go`）、`internal/api/runtimeapi`（catalog 端点 + `/tools` enrich +
端到端闸门）、`internal/toolsurface`（observation）、`cmd/aicli/ui/cell/tool.go`，以及本文档。

**刻意排除（未提交，仍是工作树里的他方改动）**：`cmd/aicli/commands/**`（第二写入者的 21+ 文件）、
`internal/agent/**`、`internal/prompt/**`、`internal/supervision/**`、`internal/team/**`、
`internal/background/**`、`internal/output/**`、`internal/chatcore/**`、`internal/agentcontrol/**`、
`internal/agentguidance/**`、`internal/toolresult/**`、`internal/profile/**`、`internal/skill/**`、
`internal/toolargs/**`、`internal/toolkit/**`、`cmd/tool-schema-measure/**`、`.race_out.txt`。

**归属判据与局限（重要）**：本战役文件 = ①本文档 §8 自述的产物 + ②本次会话亲手改过的文件。
**"是否 import `internal/toolregistry`" 不能当判据**——第二写入者也已采纳该包（29 个改动文件里
有一批是他们的），这也说明本战役的"单一真源"正在被其他工作流复用。
按此判据，`internal/toolbroker` 的拼写收敛（`types.go` / `supervision_tools.go` / `spawn_*_arg_types.go` /
`broker_arg_audit*.go` / `agent_events_view.go` 等，11:08–11:24）**判为他方**、未提交；
但它们与本次提交的 `broker.go` 同处一包——这是本提交最脆弱的边界，已如实登记。

**隔离快照证据**：`go build`（toolregistry / tools / toolbroker / policy / toolsurface / runtimeapi /
`cmd/aicli/ui/cell`）⇒ **0**；`go test -count=1` 同 6 包 ⇒ **6/6 ok**
（toolregistry 0.525s、tools 4.804s、toolbroker 25.377s、policy 1.800s、toolsurface 0.824s、
runtimeapi 50.452s）。提交后，本战役文件在工作树里已全部干净。

---

### 2026-10-16 — 提交快照的全量消费者包回归：**11/11 绿**（§5.3 在提交态达成）

提交 `3bc49a2f` / `56ae9693` / `119d3e8f` 之后，在**隔离 worktree（直接 checkout HEAD，无需打补丁）**
里跑 §5.3 承诺的全量消费者面：**11/11 exit=0** ——
`toolregistry` 0.254s、`tools` 5.679s、`toolbroker` 28.893s、`policy` 2.535s、`toolsurface` 1.069s、
`runtimeapi` 54.695s、`agent` 18.698s、`prompt` 1.285s、`skill` 3.197s、`toolargs` 0.624s、
`toolkit` 4.531s。

**意义**：此前所有回归证据都取自**混合工作树**（含第二写入者的在飞改动）；这一条是**提交快照自身**
的证据——"这个提交不会破坏它的消费者"。§5.3 的回归范围据此在提交态闭合。

**未决（保持未提交，判据见上一条日志）**：`internal/policy/taxonomy_vocabulary_test.go`（新文件，
"一份词表"守卫：`knownToolTaxonomy` 只登记规范名）与 `internal/policy/capability_scope.go`
（1 行：`background_task` → `job_start`），以及 `internal/toolbroker` 的拼写收敛批次
（`types.go` / `supervision_tools.go` / `spawn_*_arg_types.go` / `broker_arg_audit*.go` /
`agent_events_view.go` 及其测试）。它们与本战役**同主题**（都指向规范名），但**无法证明归属**，
故不代为提交——宁可留白，不越权。

---

### 2026-10-16 — P2 slice 2 已落地：宿主侧 5 处注册收敛为单一惯用法 + 源码门禁

**改动（7 文件）**：`function_catalog.go` 新增
`registerRuntimeBuiltinTool(catalog *aicliFunctionCatalog, manager functions.RuntimeToolProvider, desc runtimetools.ToolDescriptor)`
——内部就是原先那一行 `catalog.RegisterBuiltinToolFunction(functions.NewRuntimeToolFunction(manager, desc), desc)`；
5 处调用点各减为一行：`acp_mcp_host.go`、`chat_lsp_bootstrap.go`、`chat_mcp_session_scope.go`、
`chat_setup.go`、`web_mcp_handlers.go`。其中 3 个文件的 `functions` import 随之失效并移除
（另 2 个文件仍用它做别的事）。
**各调用点自己的过滤条件（skipMCP / sessionMCPOverrideDisabled / registered 去重）留在原处**——
那是宿主策略，不是惯用法的一部分。

**新增门禁**：`runtime_tool_registration_test.go::TestRuntimeToolFunctionWrappingIsSingleSourced`
——源码扫描：生产文件**非注释行**里 `functions.NewRuntimeToolFunction(` 只能出现 **1** 次（helper 本体），
既防"别的文件再抄一份"，也防"同文件里多抄一份"；另有 `scanned > 50` 的语料下限防假绿。

**红探针（2 条，按设计变红，已回滚，哈希还原一致）**：

| 探针 | 注入 | 期望红点 | 结果 |
| --- | --- | --- | --- |
| A | 在 `chat_setup.go` 里再写一次直接包装 | 多入口 | 红 ✓ `chat_setup.go 直接包装了 runtime 工具函数…` |
| B | 改测试里的 needle 使语料扫不到 | 次数守卫 | 红 ✓ `expected exactly one production call site …` |

**验证**：

- `gofmt -l` 7 个文件 ⇒ 空；`go build ./cmd/aicli/commands/` ⇒ 0；
- **整包测试（工作树，含他方在飞改动）⇒ ok 172.025s**（行为未变的直接证据：该包自身的
  注册路径用例全绿）；
- **隔离快照（HEAD + 本补丁）**：build 0、门禁绿；整包测试 **一次红一次绿**——红的用例是
  `TestStreamingAssistantFinalTailTransfersExactlyOnceToNativeHistory`（流式尾包移交，时序敏感）。
  对照实验：该用例在**有补丁**与**纯 HEAD** 两个快照下各单跑 `-count=3` **均 3/3 绿** ⇒
  既有、负载敏感的 flaky，与本改动无关（本改动只换"用哪个函数对象注册"，不触及流式路径）。
- 改动期间第二写入者推进了 HEAD（`840b960f fix(aicli): 修复 runtime context map 并发读写 fatal`），
  经核对与本批 7 文件**零交集**，补丁在新 HEAD 上干净应用。

**操作事故与教训（重要）**：首次提交**只进了 1 个文件**——6 个改动文件在提交时已不在索引里
（工作树里它们的改动没丢，但暂存被清空）。原因：**同一仓库里有另一个写入者并发执行 git 命令**
（其提交 `840b960f` 就在我暂存之后落地）。处置：`git add` + `git commit --amend --no-edit`
在**同一条命令内**完成，补齐为 7 文件（`55144f6e`）。
教训：在这种多写入者共享工作树的仓库里，**暂存与提交之间不要插入其它工具调用**；
提交后必须 `git show --stat` 复核实际入库文件数，不能只看命令的退出码。
