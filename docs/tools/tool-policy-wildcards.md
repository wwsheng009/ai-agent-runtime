# 工具策略完全控制与通配名单（2026-10-16）

> 诉求（用户原话，2026-10-16）：工具策略要"完全控制"——profile 配了
> `allowlist` 之后，`job_*`/`plan_*`/`subagent_*` 这些"运行时必需工具"**不再自动
> 放行**；`allowlist` / `denylist` / `disabled_capabilities` 三处都支持通配
> （`*` 与 `job_*` 这类前缀模式）；另确认工具清单与族级元数据的**接口读取方式**。

## 0. 结论先行

| 诉求 | 落地 |
| --- | --- |
| 去 essential 豁免：allowlist 完全生效 | ✅ `policy.AllowTool` 删除豁免分支 |
| allowlist 支持 `*`（全量放行，再 deny 减） | ✅ 通配名单语义 |
| `allowlist` / `denylist` / `disabled_capabilities` 三处通配 | ✅ 统一走 `toolregistry.MatchPattern` |
| 接口读取工具清单与族元数据 | ✅ `GET /api/runtime/tools`（§4） |

语义基线：**deny 恒优先于 allow**（通配 deny 同样拦得住通配 allow 放进来的
工具）；空 allowlist 仍等于"全部禁用"（DisableTools 面）；capability scope 与
read-only 在 allowlist 之后继续生效，不受通配影响。

## 1. 通配语义（单一实现点：`toolregistry.MatchPattern`）

| 模式 | 语义 | 例 |
| --- | --- | --- |
| `*` | 匹配一切 | `allowlist: ["*"]` = 全量放行 |
| `prefix_*` | 归一化后前缀匹配 | `"subagent_*"` 放行 `subagent_spawn`/`subagent_status`… |
| 其余 | 归一化精确匹配（别名/大小写/连字符变体） | `"background_task"` 命中 `job_start` |

- 归一化走 `toolregistry.Normalize`（`background_task→job_start`、
  `spawn_agent→subagent_spawn` 等别名同样命中）。
- **只支持尾缀 `*`**；中缀 `*`（`sub*agent`）按字面精确比较，不展开。
- `spawn_subagents` 是**独立注册条目**（非 `subagent_spawn` 别名），
  不落 `subagent_*` 前缀——需要控制面的名单要么显式列它，要么用 `*`。
- skill 层 `profile/merge.go` 的选择名单（skills/MCP）保持原有裸 `*` +
  大小写折叠语义，不并入本 helper（两套并存，互不影响）。

## 2. essential 豁免移除

### 2.1 旧语义（已废弃）

`toolregistry.Entry.Essential=true` 的 41 个"运行时自有工具"（job/plan/subagent/
team 协作面 + `search_tool` 等）在窄 allowlist 下自动存活——设计初衷是
"allowlist 不得 brick 控制面"，但代价是 **profile 无法真正收窄工具面**。

### 2.2 新语义

`policy.AllowTool` 的判定顺序不变，但 allowlist 闸门变成硬闸门：

```
deny（恒优先，支持通配）
  → BlockDelegation
  → allowlist（硬闸门，支持通配，无豁免；plan-mode 写豁免保留）
  → capability scope
  → read-only
```

- 窄 allowlist 下未列出的 essential 工具同样被裁（执行与定义过滤同向）。
- 需要控制面的 profile 显式放行：`allowlist: ["*"]`、`["subagent_*"]`、
  或逐个列名。
- `Entry.Essential` 字段保留，仅作观测面标记（`/api/runtime/tools` 的
  `metadata.essential`、目录 API），不再影响任何判定。
- `policy.IsRuntimeOwnedEssentialTool` 转发函数删除；测试与观测面改用
  `toolregistry.IsEssential`。

### 2.3 配套适配点

| 位置 | 变更 |
| --- | --- |
| `policy.intersectAllowedTools` | 父子 allowlist 交集改为通配感知（父通配不把子裁到 0；子通配不越父） |
| `agent.go` spawn 定义闸门 | 字面 map 查表 → `policy.DeniesTool`/`AllowsTool`（含通配与归一拼写） |
| `agent.childSurfaceLacksShell` | 通配名单不再误报"无 shell"（`AdmitsAnyShellLikeTool`） |
| `prompt/toolguidance.FactsFromPolicy` | 通配 allowlist 不构成"only 字面名单"语义，`Only` 保持 nil |
| `profile/validate.go` | `disabled_capabilities` 通配按封闭枚举校验命中 |
| `profile/reference_validation.go` | `tools.allowlist/denylist` 的通配模式按登记表校验命中，不再报"未登记" |
| `profileinput` | `disabled_capabilities` 通配**展开**为具体域（`*`→全部用户可禁域） |

## 3. `disabled_capabilities` 通配

能力域是**封闭枚举**（`policy.AllUserDisablableCapabilities()`：
`write_fs`/`network`/`external_side_effect`/`ask_user`/`background_task`/
`agent_management`，不含 `read_only`/`exec_shell` 地板域）。通配在解析期
**展开为具体域**：

- `"*"` → 禁用全部用户可禁域（地板域仍保留）；
- `"agent_*"` → 前缀匹配域名（命中 `agent_management`）；
- 模式命中不了任何域 → 报错（解析期）/报 issue（validate）。

## 4. 接口读取：工具清单与族级元数据

端点：`GET /api/runtime/tools`（实现：`internal/api/runtimeapi/tool_catalog_handlers.go`，
§2.2-A 渐进披露设计）。

响应结构（关键部分）：

```jsonc
{
  "tools": [
    {
      "name": "job_start",
      "description": "...",
      "metadata": {
        "surface_source": "builtin",     // builtin / host / mcp:<server> / code
        "group": "orchestration",        // 族分组
        "essential": true,               // runtime-owned 标记（观测用，非豁免）
        "mcp_name": "..."                // 仅 MCP 工具携带
      }
    }
  ],
  "groups": { "orchestration": { "label": "...", "tool_count": 12 } },
  "filtered": [
    { "name": "deferred_helper", "reason": "..." }
  ]
}
```

- `tools[]`：当前会话**可见**工具全集，每项带 `metadata`（来源、族、essential）。
- `groups{}`：族级分组 + 计数——前端配置界面可直接用。
- `filtered[]`：被裁剪的工具与原因（allowlist/denylist/能力域裁剪的事实账本）。

## 5. 测试与回归

- `toolregistry/pattern_match_test.go`：MatchPattern 三类语义 + 空名单 +
  中缀 `*` 字面行为。
- `policy/tool_policy_test.go`：essential 被窄名单裁剪；`["*"]` 全量放行；
  `subagent_*` 前缀；通配 deny 拦截通配 allow；deny 归一拼写口径保留。
- `policy/supervision_tool_registry_test.go`：窄名单裁 + `subagent_*`/
  `supervision_*` 通配放行。
- `toolbroker/*`：广播面 essential 标记一致性 + "窄名单裁、通配留"。
- `agent/search_tool_policy_integration_test.go`：宿主显式暴露 search_tool 时
  名单放行（表面冻结语义不变）。
- `profileinput/disabled_capabilities_test.go`：`*` 展开 / `agent_*` 前缀 /
  未命中报错。
- `profile`：validate 对通配能力域与通配工具名的校验。

已知行为迁移（对既有 profile 的影响）：

1. 此前依赖"essential 自动存活"的窄 allowlist profile 需要补通配或显式名单，
   否则控制面工具（含 spawn/plan/todos）会被裁掉——这是**预期变化**（完全控制）。
2. `bash` 等别名在名单里继续可用（归一精确匹配），`*-a` 之类中缀不生效。
3. `saveas` 差分落盘仍以字面名单形态写出（`allowlist: ["*"]` 原样保留）。
