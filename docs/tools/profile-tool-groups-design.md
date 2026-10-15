# profile 组字段（`tools.disabled_groups`）设计草案

> 状态：草案 v0.2（2026-10-15；6 项未决已定稿，见 §8）｜渐进式工具披露方案 **阶段 3 前置**
> **状态变更（v0.3，2026-10-15 核查后）：实施优先级下调。** 族级门控机制已存在两套——`policy.Capability`（8 域，含 `AllowedCapabilities` 执行期判定）与 `policy.ToolTaxonomy.Kind`（6 类）——本草案的 `disabled_groups` + `internal/toolgroups` 属**第三套同类机制**。保留为"配置便利性"备选；裁决见 `progressive-tool-disclosure-plan.md` §10。
> 上游方案：`docs/tools/progressive-tool-disclosure-plan.md`（§4.1 层一 / §4.5 与 profile 的分工）
> 登记处：`docs/architecture/unified-tool-surface-registry.md` §22
> 定稿去向（本草案**不改**正式文档）：键名与语义 → `docs/profile/configuration.md` §2；不变量 → `docs/profile/design.md` §2；术语 → `docs/profile/README.md` §2
> 行号为 2026-10-15 核查时点

## 1. 目标与非目标

### 目标
1. 给 profile 一个**按工具族收窄暴露面**的声明入口（"这个场景不要 mesh / lsp / orchestration"）；
2. **单一字段、纯减法、默认零变化**：不声明 = 行为与今天完全一致；
3. **复用既有组词汇**，不造第三套命名（§3.3）。

### 非目标
- 不做运行时装卸（与披露方案第一期非目标一致）；
- **不引入"启用组"语义**（理由见 §3.6）；
- 不改执行期权限/沙箱/审批语义（组关闭只影响"是否暴露"，不放宽也不收紧既有授权）；
- 不替代 `allowlist` / `denylist`（逐工具精确控制仍走它们）。

## 2. 现状事实（核查）

| # | 事实 | 位置 |
| --- | --- | --- |
| 1 | profile 的 `tools:` 声明分布在四层：`profile.yaml` → `agents.<id>.tools` → `agents/<id>/tools/policy.yaml` → `workspace.yaml` | `profile/resolver.go:82-88` |
| 2 | 原始字段模型只有 4 个：`allowlist` / `denylist` / `read_only` / `sandbox` | `profile/spec.go:79-85` |
| 3 | 合并语义（低→高）：allowlist/denylist 去重追加、标量覆盖、sandbox 深合并 | `profile/merge.go:153+` |
| 4 | 两道"这层是否有策略"的闸门硬编码了 4 个字段（漏改会把"只声明组"的层整层丢弃） | `profile/resolver.go:201-206`、`profileinput/inputs.go:174` |
| 5 | 解析结果 `ResolvedToolPolicy{Allowlist,Denylist,ReadOnly,Sandbox,Sources}` | `profile/resolved.go:20`、镜像 `profileinput/profile_types.go:12-18` |
| 6 | 转换入口 `BuildToolExecutionPolicy` → 运行时 `ToolExecutionPolicy` | `profileinput/inputs.go:166` |
| 7 | 双落点：runtime-server `applyAgentExecutionPolicy` / aicli `buildLocalChatToolPolicy`（已有等价护栏测试） | `runtimeapi/handler.go:5568`、`chat_actor_host.go:2767`、`profile_tool_policy_guard_test.go` |
| 8 | **既有组词汇已存在**：`shell / read / edit / webfetch` 四个 friendly group 及各自工具表（用于 `Shell(...)` / `Read(...)` 规则说明符） | `policy/specifier.go:53-97`；消费点 `policy/permissions_file.go:259` |
| 9 | 可见面过滤走 `policy.AllowsDefinition` | `policy/tool_policy.go:477-479`；调用点 `agent/loop.go:4296-4301` |
| 10 | 稳定面绑定键已含策略字段（read_only / allowlist / deny / capability / block_*） | `agent/tool_surface_binding.go:67-96` |
| 11 | save-as 只把 allowlist/denylist/read_only 写回差分 | `profile/saveas.go:44,72-79` |
| 12 | 官方键名以 `configuration.md` 为准（"所有键名以本文为准"） | `docs/profile/configuration.md:4,64-68` |
## 3. 字段设计

### 3.1 键名与形态

```yaml
tools:
  allowlist: [view, grep]
  disabled_groups: [mesh, lsp, orchestration]
```

- YAML / JSON 同名 `disabled_groups`（沿用既有 snake_case 风格）；
- 类型：字符串数组；空 / 缺省 = 零变化；
- 备选命名 `group_denylist` 已否决（开关语义而非名单语义）；**定稿：`disabled_groups`**。

### 3.2 语义

1. **纯减法**：只把组内工具从授权面里移除，永不新增 / 放宽；
2. **跨层并集**：四层声明的 `disabled_groups` 取并集（与 allowlist/denylist 同构）；低层不能重新启用高层关闭的组；
3. **优先级**：`profile 显式 denylist > disabled_groups > allowlist`；组关闭压过 allowlist 条目（校验期给出警告，见 §3.4）；
4. **默认零变化**：不声明该键的既有 profile 行为逐字节不变（含 tools 前缀）。

### 3.3 组词汇（canonical registry）

复用既有 specifier 组，扩展披露方案需要的族；建议落点 `backend/internal/toolgroups`（叶子包，无内部依赖）：

| 组 id | 含义 | 工具集来源 | 可关闭 | 备注 |
| --- | --- | --- | --- | --- |
| `read` | 读类 | `policy/specifier.go:79-83` readGroupTools | 否（protected） | 高频基础族，逐工具收窄请用 allowlist/denylist |
| `edit` | 写类 | `:85-92` editGroupTools | 否（protected） | 同上 |
| `shell` | 命令执行 | shell / bash / execute_shell_command 等别名 | 否（protected） | 同上 |
| `webfetch` | 网络取回 | `:94-97` webFetchGroupTools | 是 | 与 `io` 在 download 上重叠；离线场景可整组关闭 |
| `search` | 检索类 | `web_search`、`sourcegraph`（定稿集合；grep 不重复入组） | 是 | 与 `read` 重叠时按"任一命中即关" |
| `orchestration` | broker 编排面 | `toolbroker.IsBrokerTool`（job_* / subagent / team / schedule / plan / question） | 是 | 披露方案默认"会话相关" |
| `io` | 产物 / 图像 / 下载 | `download`、`openai_image_generate`、artifact 类（定稿集合） | 是 | 与 `webfetch` 在 download 上重叠（并集语义） |
| `mesh` / `lsp` | mesh 族 / lsp 族 | 以 `mcp:<server>` 为主表达；`mesh` / `lsp` 保留为别名（成员由 registry §8 审计锁定） | 是 | 目录中无对应 server 时校验**警告**（不报错） |

要点：

- **重叠成员**（如 `download` ∈ webfetch ∩ io）：工具被任一所属组关闭即移除（并集语义）；
- **MCP 工具**：按 `mcp:<server>` 成组（复用 toolsurface 的 `Classify` 标签），`disabled_groups: ["mcp:chrome-devtools"]` 亦可表达；
- **protected 组不可关闭**：`disabled_groups` 含 protected 组 → `profile validate` **报错**（"错误不猜"），并提示改用 allowlist/denylist 逐工具表达。

### 3.4 校验规则（profile validate）

| 情形 | 处理 |
| --- | --- |
| 未知组名 | 错误（列出合法组） |
| protected 组 | 错误（附替代写法） |
| allowlist 条目落在被关闭组 | 警告（该条目不会生效，可解释） |
| 重复 / 大小写不一 | 归一化去重（小写；`mcp:` 前缀保留） |

### 3.5 与既有字段的关系（正交性）

| 既有机制 | 关系 |
| --- | --- |
| `denylist` | 逐工具 deny；与组关闭同为减法，deny 恒优先（更精确） |
| `allowlist` | 白名单；组关闭在其之上继续收窄（并集） |
| `read_only` | 正交：只读裁剪移除写工具，与组关闭互不替代 |
| `mcp.use_servers` / `exclude_servers` | **mesh/lsp 的真源**；组关闭是"服务已连但本场景不用"的第二道闸 |
| capability scope / `block_*` | 正交（授权域与暴露面两个维度） |

### 3.6 为什么不做 `enabled_groups`（被否决方案）

- "启用默认关闭的组"在现有体系里已有真源：mesh/lsp 由 `mcp.use_servers` 决定是否连；orchestration 由会话形态（多代理 / 团队）决定；再加一个 enable 键会形成**两个真源**；
- 与 profile 不变量"只收窄"冲突（enable 带放宽语义）；
- 结论：**只引入 `disabled_groups`**；将来若确有需要，另立提案评估。
## 4. 实现落点（逐文件清单）

| # | 文件 | 改动 |
| --- | --- | --- |
| 1 | `internal/toolgroups`（新增，叶子包） | 组注册表：`Groups() / GroupForTool() / IsDisabled() / Validate()`；specifier 组表迁移或加漂移护栏 |
| 2 | `internal/profile/spec.go:79-85` | `ToolPolicySpec` 增 `DisabledGroups []string`（yaml/json: `disabled_groups`） |
| 3 | `internal/profile/resolved.go:20` + `profileinput/profile_types.go:12-18` | `ResolvedToolPolicy` 增同名字段（两处镜像同步） |
| 4 | `internal/profile/merge.go:153+` | 合并规则：并集去重（与 allowlist 同构） |
| 5 | `internal/profile/resolver.go:201-206`、`profileinput/inputs.go:174` | 两道 hasPolicy 闸门纳入新字段（否则"只声明组"的层会被丢弃） |
| 6 | `internal/profileinput/inputs.go:166` | `BuildToolExecutionPolicy` 落到 `ToolExecutionPolicy.DisabledGroups`（归一化 + 校验） |
| 7 | `internal/policy/tool_policy.go` | 新字段；`AllowsDefinition` / `AllowTool` 纳入组判定；`Clone()`（:482-493）与子策略派生（:532-553）必须继承（只读子代理不得重新启用） |
| 8 | `internal/agent/tool_surface_binding.go:67-96` | eligibility key 增 `disabled_groups`（排序入 payload）→ 换 profile = 新 cache generation |
| 9 | `internal/profile/saveas.go:44,72-79` | 差分落盘：仅非空时写 `disabled_groups` |
| 10 | `internal/api/runtimeapi/handler.go:5568`、`cmd/aicli/commands/chat_actor_host.go:2767` | 两个落点接线（扩展既有 guard 测试） |
| 11 | `internal/profile/validate.go` | 新校验规则（§3.4） |
| 12 | 文档 | 定稿时并入 `docs/profile/configuration.md` §2 / `design.md` §2 / `README.md` §2 |

## 5. 与稳定面 / 缓存的关系

1. **换面时机**：`disabled_groups` 属策略 → 进入 eligibility key → 改 profile = 新 key = 新会话 / 新 cache generation；会话内切换走既有 switch 语义（回合边界生效）；
2. **装配期过滤点**：沿用 `policy.AllowsDefinition` 单一判定（`agent/loop.go:4296-4301`），使"可见面 = 可执行面"继续成立——组关闭的工具在 `AllowTool` 同样被拒（防 resume / 直连路径绕过）；
3. **MCP 晚到**：组判定基于工具名 / `mcp:<server>`，**不依赖构建期快照白名单**，因此比"把组翻译成 allowlist"更稳（后者需 `syncLocalChatToolPolicyAllowlist` 重同步）；若两者并存，仍复用既有重同步通道（`chat_tool_policy_sync.go`）保持口径一致；
4. **观测**：请求 metadata 增 `disabled_groups` 与 `group_filtered_tools`（或并入 `surface_filtered_tools` 并附原因 `group_disabled`）；`/runtime/tools` 与 profile switch report 输出同一口径；
5. **disable_tools 例外不受影响**：目录保持冻结，只翻 `tool_choice=none`。

## 6. 测试清单

- `profile/merge_test.go`：跨层并集、低层不可重新启用；
- `profile/validate_test.go`：未知组 / protected 组报错、allowlist 重叠警告；
- `profile_tool_policy_guard_test.go`：双落点等价性扩展（含 `disabled_groups`）；
- 装配期：组关闭工具不出现在 tools 数组（golden 字节）；`disable_tools` 路径不受影响；
- 执行期：`AllowTool` / `AllowToolCall` 对被关闭组工具拒绝（可见面 = 可执行面）；
- 稳定面：改 profile → eligibility key 变化 → 新会话生效；会话内不改写前缀；
- 漂移护栏：`policy/specifier.go` 组表与 `toolgroups` 表一致性断言；
- save-as：非空才落盘、往返一致。

## 7. 分期与回滚

- **3a（字段与可见面）**：spec/resolved/merge/闸门/校验 + `ToolExecutionPolicy.DisabledGroups` + 装配期过滤 + 观测；
- **3b（执行面与派生）**：`AllowTool` 组判定 + Clone/子策略继承 + save-as + 文档并入；
- **回滚**：不声明 `disabled_groups` 即与今天逐字节一致；紧急降级可保留解析但不接线（**不推荐**长期如此）。

## 8. 决策记录（v0.2，2026-10-15 定稿）

| # | 议题 | 决策 | 理由 / 备注 |
| --- | --- | --- | --- |
| 1 | 键名 | `disabled_groups` | 开关语义而非名单语义，与 `read_only` 同级 |
| 2 | 组注册表落点 | `internal/toolgroups`（叶子包） | 供 profile / policy 复用；specifier 组表迁移或加漂移护栏 |
| 3 | protected 集合 | `read` / `edit` / `shell` | 高频基础族用逐工具 allowlist/denylist 表达更精确；`webfetch` 可关闭（离线场景） |
| 4 | `search` / `io` 成员 | `search = {web_search, sourcegraph}`；`io = {download, openai_image_generate, artifact 类}`；grep 不入 search | 重叠（download ∈ webfetch ∩ io）按并集处理 |
| 5 | `mesh` / `lsp` | 以 `mcp:<server>` 为主表达，`mesh` / `lsp` 保留别名；无对应 server 时校验**警告** | 避免独立组与 MCP 配置形成两个真源 |
| 6 | 正式文档并入时机 | 随阶段 3a 实现同一批变更并入 configuration.md §2 / design.md §2 / README.md §2 | 不让文档领先于代码 |

**遗留（不阻塞实现）**：实现时若目录中出现同类新工具，按同一 PR 追加成员并补漂移护栏测试。
