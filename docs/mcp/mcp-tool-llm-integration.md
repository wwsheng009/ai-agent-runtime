# MCP 工具与 LLM API 集成及重名隔离

> 核对时间：2026-09-18；适用主线构建（Go 1.24，无 `win7compat` tag）。路径相对仓库根目录，行号以当前主线代码为准。
>
> **结论（TL;DR）**
> 1. runtime-server 把 MCP 工具与内置 toolkit 工具统一注册后，以**原生 `tools`（function calling）**形式放入发往上游 LLM API 的请求：OpenAI `chat/completions`、Anthropic `messages`、Gemini、Codex 各自协议字段。
> 2. 模型返回的 `tool_calls` 由 runtime 解析、经策略/审批中间件后路由回对应 MCP server 执行，结果作为工具消息回填。
> 3. 多个 MCP 服务存在**重名工具时已有完整隔离**：对外名称投影为 `mcp__<server>__<tool>`，短名歧义 fail-closed，执行时按 `(server, raw tool)` 精确路由；canonical 完全碰撞的后注册者进入 quarantine，不暴露执行。

---

## 1. 端到端链路

### 1.1 启动装配（runtime-server）

```
~/.aicli/mcp.yaml（或解析到的项目级配置）
  → mcpmanager.NewManager().LoadConfig()/Start()          backend/cmd/runtime-server/main.go:1408-1433
  → tools.NewDefaultManagerWithRuntimeConfig(mcpManager)   统一 MCP + 内置 toolkit
  → tools.NewAgentAdapter(...)                             适配为 skill.MCPManager 形状
  → skillsapi.NewHandler(..., mcpAdapter)                  main.go:1013-1041
  → 每个会话 agent.NewAgentWithLLM(cfg, mcpManager, llm)   backend/internal/api/skills/handler.go:4012-4035
```

要点：

- MCP 配置解析优先级：`./.aicli/mcp.yaml > ~/.aicli/mcp.yaml > 显式覆盖 > 向上搜索 > configs/mcp.yaml`；模板默认值会落到用户级目录，避免在任意工作目录生成 `configs/mcp.yaml`（`main.go:1573-1608`）。
- MCP 默认启动即连（后台并行建连，`buildSkillsMCPManager`）：不再有 `aicli.mcp.auto_connect` / `mcp.yaml global.autoConnect` 开关；单个 server 是否参与连接由 `mcpServers.<name>.enabled` 决定（缺省启用）。
- 核心装配代码：

  ```go
  // backend/cmd/runtime-server/main.go:1432-1433
  toolManager := runtimetools.NewDefaultManagerWithRuntimeConfig(manager, runtimeConfig)
  return runtimetools.NewAgentAdapter(toolManager), manager, nil
  ```

- 工具管理器同时持有 MCP 管理器与内置 toolkit；入站 API 调用方**不能**自带 tools（见 §3 注意事项）。

### 1.2 回合内工具面的组装与筛选

- 汇总与过滤：`backend/internal/agent/tool_surface_binding.go:93-160`（`CollectToolCatalogDefinitions`）
  - 从 `a.mcpManager.ListTools()` 收集；按执行策略 `AllowToolInfo` 过滤；
  - 去重并注入 `metadata["mcp_name"]`；
  - 经 `filterToolDefinitionsByShouldList` → `optimizeModelToolSurface` → 排序。
- MCP 与本地 toolkit 合并：`backend/internal/tools/manager.go:74-101`
  - MCP 工具先入列表；同名时以 MCP 为准（`ls/glob/grep/view` 除外，见 §3.4）；
  - 使用 `mcpregistry.CallableToolNames` 的结果作为对外名称。
- 工具直出（2026-09-18 起）：**不再按目录规模做自动搜索投影**。所有通过 `should_list`/`list_when` 与执行策略的工具（含全部启用的 MCP 工具）直接进入 `tools`；`search_tool` 仅在宿主显式注册时可用。工具规模由「MCP 工具级启用/禁用」配置显式治理。
- 回合冻结：`resolveAvailableTools` → `freezeToolSurfaceForTurn`（`backend/internal/agent/loop.go:1593-1599, 3765-3770`），保证同一 turn 内 tools 前缀稳定（prompt cache）。

### 1.3 出站 LLM API 的报文映射（核心）

```
ReAct 循环
  req := llm.LLMRequest{ Tools: availableTools }            backend/internal/agent/loop.go:1668-1683
  → GatewayClient.buildAdapterRequest: Tools: req.Tools      backend/internal/llm/gateway_client.go:1385-1403
    （ProviderWrapper 路径: convertRequest → chatToolsToToolDefinitions  backend/internal/llm/provider.go:2062-2073）
  → buildProviderAdapterRequest                               backend/internal/llm/provider_adapter_request.go:100-149
      tools = BuildToolDefinitionsForRequestWithImageOptions(input.Tools, protocol, ...)
      RequestConfig{ Functions: tools, ToolChoice: metadata["tool_choice"] }
  → 协议转换                                                  backend/internal/llm/mcp_meta_tools_convert.go
      OpenAI    → {"type":"function","function":{name,description,parameters}}   :112-125
      Anthropic → {name, description, input_schema}                              :127-137
      Gemini    → {name, description, parameters}                                :139-149
      Codex     → 扁平 function / 自定义工具                                     :151+
  → HTTP body 落字段
      OpenAI    request["tools"] = config.Functions          backend/internal/llm/adapter/openai.go:107-110
      Gemini    request["tools"] = config.Functions          backend/internal/llm/adapter/gemini.go:109-113
      Codex     request["tools"] = tools + tool_choice       backend/internal/llm/adapter/codex.go:194-205
      Anthropic tools + tool_choice                          backend/internal/llm/adapter/anthropic_request_test.go:315-329（契约测试）
```

补充语义：

- `disable_tools` / compact：**不删除** tools 数组，仅把 `tool_choice` 置为 `none`，以保住 prompt-cache 前缀（`backend/internal/llm/provider_adapter_request.go:91-99`、`backend/internal/llm/request_tools.go:13-20`）。
- MCP 资源类 meta 工具（`list_mcp_resources` 等，定义见 `backend/internal/llm/adapter/mcp_meta_tools.go`）仅在 `includeMeta=true` 时追加；provider 主路径传 `false`（`provider_adapter_request.go:108`），当前默认不注入。
- 入站 API（`POST /api/agent/chat`）请求体没有 `tools` 字段（`backend/internal/api/skills/handler.go:1534-1570`），工具面完全由 runtime 按 MCP 配置 + 执行策略决定；仓库未提供入站 OpenAI 兼容 `/v1/chat/completions` 端点。

### 1.4 回程：模型 tool_calls 的执行

```
LLM 返回 tool_calls
 → agent.mcpManager.FindTool(call.Name)                     backend/internal/agent/approved_tool.go:194
 → 硬约束/审批/checkpoint 中间件                              approved_tool.go:204-235
 → CallToolWithMeta(ctx, toolInfo.MCPName, call.Name, args)  approved_tool.go:237-242
      ReAct 主循环：loop.go:2845-2849
      并行调度：tool_parallel_scheduler.go:271-275
 → tools.AgentAdapter.CallToolWithMeta                       backend/internal/tools/agent_adapter.go:79-89
 → tools.Manager.ExecuteWithMeta                             backend/internal/tools/manager.go:188-241
      ResolveTool → ExecutionLookupName → m.mcp.CallTool(info.MCPName, executionName)
 → mcp manager.CallTool 归一化为 (info.MCPName, info.Tool.Name) 后调用真实 server
                                                             backend/internal/mcp/manager/manager.go:337-373
```

---

## 2. 重名隔离机制（多 MCP 服务同名工具）

### 2.1 存储层：按 `(MCP 服务, 原始工具名)` 复合键隔离

- 注册表 key = `mcpName + "\x00" + rawToolName`（`backend/internal/mcp/registry/registry.go:62, 147`）。
- 按服务查询/解析使用 `GetTool(mcpName, toolName)` 与 `ResolveToolForMCP(mcpName, name)`（`registry.go:328-350, 377-388`）。
- 元数据记录身份：`mcp_name` / `mcp_raw_tool_name` / `mcp_canonical_name`（`registry.go:128-130`）；执行结果元数据补充 `tool_callable_name`（`backend/internal/tools/manager.go:512-524`），用于审计定位。

### 2.2 暴露层：可调用名（callable name）投影

规则实现于 `callableToolName`（`backend/internal/mcp/registry/registry.go:288-295`）：

| 条件 | 对模型暴露的名称 |
|------|------------------|
| 原始名全局唯一 且 满足 provider 命名规则 `^[a-zA-Z0-9_-]{1,64}$` 且 不遮蔽其他工具的 canonical 名 | 保留原始名（向后兼容） |
| 两个及以上服务存在同名工具 | 各自投影为 `mcp__<server>__<tool>` |
| 原始名不满足 provider 命名规则（含空格/特殊字符/超长） | `mcp__<server>__<tool>`（超长截断+hash） |
| 原始名恰好等于另一个工具的 canonical 名（遮蔽） | 投影为自身的 `mcp__<server>__<原始名>` |

防碰撞细节：

- `portableNamePart` 对非法字符替换为 `_`，且只要发生替换就追加 FNV-1a 短哈希，避免 `server.name` 与 `server_name` 规整后碰撞（`registry.go:534-553`）。
- canonical 超过 64 字符时截断并追加 identity hash（`registry.go:220-228`）。

所有面向模型/技能的列表出口统一使用 `CallableToolNames`：

- `backend/internal/tools/manager.go:81-92`（runtime-server / agent 主链路）；
- `backend/internal/skill/mcp_adapter.go:143-158`（CLI skill 链路）；
- `backend/internal/mcp/catalog/manager_gateway.go:17-28`（catalog 快照链路）。

### 2.3 解析层：canonical 优先 + 短名歧义 fail-closed

- `Registry.ResolveTool`（`registry.go:297-326`）：
  1. 先做 canonical 精确匹配；
  2. 再做原始名匹配，**仅唯一命中时可用**；
  3. 命中多个候选 → 返回 `AmbiguousToolError`，并附候选 canonical 名列表，调用方必须改用 canonical 名。
- `Registry.ResolveToolForMCP`（`registry.go:329-350`）：同一服务内 canonical 优先，其次精确 raw；同名多个 → 歧义错误。

### 2.4 执行层：防止 raw 名遮蔽与错误路由

- `ExecutionLookupName`（`registry.go:253-272`）用于"某工具 raw 名恰好等于另一工具 canonical 名"的场景：此类工具执行时改用自身 canonical 身份，确保解析不落到隔壁工具。
- 使用点：`tools.Manager.ExecuteWithMeta`（`manager.go:211`）与 `skill.MCPAdapter`（`mcp_adapter.go:81`）。
- `mcp manager.CallTool` 解析成功后强制归一化为 `mcpName = info.MCPName; toolName = info.Tool.Name`，再调用对应 client（`manager.go:348-350, 367`）。

### 2.5 canonical 完全碰撞：后注册者 quarantine（fail-closed）

不同 `(server, tool)` 组合在极端情况下投影出相同 canonical 名时，`RegisterTool` 检测并**拒绝注册**，把后到者写入 quarantine（不暴露、不可执行），可通过 `ListQuarantinedTools` 诊断（`registry.go:144-165`；`mcp manager.ListQuarantinedTools`，`manager.go:332-335`）。

### 2.6 工具级启用/禁用（2026-09-18 起）

配置（`mcp.yaml`，缺省启用，向后兼容）：

```yaml
mcpServers:
  chrome-devtools:
    enabled: true
    tools:
      list_pages: { enabled: false }   # 只禁用该工具，服务保持连接
```

- 状态语义分离：`registry.ToolInfo.Enabled`（运行时健康位，健康检查写入）与 `registry.ToolInfo.UserDisabled`（用户配置位）；暴露条件 = `Enabled && !UserDisabled`。健康检查成功不会"复活"被用户禁用的工具。
- 生效路径：`admin.Service.SetToolEnabled/SetToolsEnabled` 写回 `mcp.yaml` → `manager.SetToolEnabled` 直接翻转注册表标志（**不重连 MCP 服务**）→ catalog 刷新 → 事件 `mcp.tool.state_changed`。
- 被禁用工具不进入工具面、catalog 与搜索索引；执行解析（`ResolveTool`/`ResolveToolForMCP`）返回 not found（fail-closed）。
- 管理 API：`GET /api/runtime/mcps/{name}/tools` 返回全量工具（含禁用）与 `enabled / configured_enabled / healthy`；`POST .../tools/{tool}/enable|disable` 与 `POST .../tools/enable|disable`（body `{"tools":[...]}`，空数组 = 全部）。
- 注意：会话工具面按 prompt-cache 语义冻结（`turn_tool_surface_snapshot.go:74-80`），工具开关对**已存在会话**在下一轮新会话或显式刷新后生效；新建会话立即生效。

---

## 3. 边界与注意事项

1. **不重名就不加前缀是设计行为**：唯一工具名保持原名（如 `search`）；只有重名、provider 不安全或遮蔽时才投影为 `mcp__…`。列表中出现无前缀工具不代表缺少隔离。
2. **本地 toolkit 优先级**（`backend/internal/tools/manager.go:291-322`）：
   - `ls / glob / grep / view` 四个名字**无条件本地优先**，外部 MCP 的同名工具不会出现在工具面/不会被调用（测试：`backend/internal/tools/manager_test.go:734-780`）。
   - 其他名字仅在 MCP 服务名为 `toolkit`（内置 toolkit MCP 约定）时本地优先；否则同名时 MCP 优先（`manager.go:188-251`）。
3. **策略 allowlist 按"暴露名"精确匹配**：`AllowedTools[toolName]` 为精确比较（`backend/internal/policy/tool_policy.go:97`）。重名工具暴露为 canonical 名时，按原始短名配置的 allowlist 会匹配失败并被拦截（fail-closed）——需要按 canonical 名配置。
4. **管理 API 展示口径**：`GET /api/runtime/mcp/{name}/tools` 在 runtime manager 可用时列**原始名**（`backend/internal/api/skills/mcp_admin_handlers.go:76-90`），fallback 到接口形态 manager 时列 callable 名（`:91-103`）。仅是展示差异；执行入口仍按 `(mcp_name, raw)` 服务端内解析，不会串服务。
5. **Win7 兼容构建（`win7compat` tag）**：`CallableToolNames` / `ExecutionLookupName` / `CanonicalToolName` 为简化实现（`backend/internal/mcp/registry/registry_win7compat.go:64-97`），无重名隔离；但该构建下 MCP 管理器整体禁用（`backend/internal/mcp/manager/manager_win7compat.go:96-132`，`ListTools` 返回 nil、`CallTool` 恒报错），当前无实际暴露面。**若未来在 Win7 构建启用 MCP，必须补齐隔离逻辑。**
6. **其他维度的隔离**（按 agent/session/tenant 的工具可见性与权限）属于 policy/capability 层，不在本文范围；重名解析层只保证"名字到服务的确定性路由"。

---

## 4. 验证（可复现）

```bash
cd backend
go test ./internal/mcp/registry ./internal/tools ./internal/skill ./internal/agent \
  -run "Collision|Callable|Ambiguous|ResolveToolForMCP|Shadow|MCPTool" -count=1 -v
```

2026-09-18 实测结果（全部 PASS）：

| 测试 | 覆盖点 |
|------|--------|
| `TestResolveToolFailsClosedForAmbiguousShortName` | 短名歧义 fail-closed |
| `TestCallableToolNamesAvoidRawNameShadowingCanonicalIdentity` | raw 名遮蔽 canonical 时的投影 |
| `TestResolveToolForMCPPrefersCanonicalIdentityOverRawAlias` | 服务内 canonical 优先 |
| `TestCallableToolNamesCanonicalizeProviderUnsafeAndLongNames` | 非法/超长名投影 |
| `TestAgentAdapter_FindTool_PrefersRuntimeManagerCollisionResolution` | AgentAdapter 走运行时管理器消解 |
| `TestManager_MCPNameCollisionUsesCanonicalNamesAndFailsClosed` | 两服务同名：canonical 可见、短名报错、canonical 精确路由 |
| `TestSubagentMCPToolsSharingRetiredNamesSurvive` | 子代理共享工具名场景 |

`TestManager_MCPNameCollisionUsesCanonicalNamesAndFailsClosed` 的关键断言（`backend/internal/tools/manager_test.go:708-732`）：

- `FindTool("mcp__docs__search")` 与 `FindTool("mcp__issues__search")` 均解析成功且 `MCPName` 正确；
- `Execute(ctx, "search")` → `registry.IsAmbiguousToolError`（不会随机选一个服务）；
- `Execute(ctx, "mcp__issues__search")` → 实际路由 `mcp=issues, raw_tool=search`，输出 `issues:search`。

---

## 5. 关键代码索引

| 主题 | 位置 |
|------|------|
| runtime-server MCP 装配 | `backend/cmd/runtime-server/main.go:1408-1433` |
| API handler 注入 agent | `backend/internal/api/skills/handler.go:4012-4035` |
| 入站 AgentChat 请求体 | `backend/internal/api/skills/handler.go:1534-1570` |
| 回合工具面汇总 | `backend/internal/agent/tool_surface_binding.go:93-160` |
| 工具面冻结 | `backend/internal/agent/loop.go:1593-1599, 3765-3770` |
| 工具合并/本地优先 | `backend/internal/tools/manager.go:74-101, 291-322` |
| LLMRequest.Tools | `backend/internal/agent/loop.go:1668-1683` |
| Provider 请求转换 | `backend/internal/llm/provider_adapter_request.go:100-149` |
| 协议 tools 转换 | `backend/internal/llm/mcp_meta_tools_convert.go:23-55, 112-160` |
| OpenAI wire `tools` | `backend/internal/llm/adapter/openai.go:107-110` |
| Codex wire `tools` | `backend/internal/llm/adapter/codex.go:194-205` |
| 工具调用执行 | `backend/internal/agent/approved_tool.go:194-242`；`backend/internal/tools/manager.go:188-241` |
| MCP 注册表/命名 | `backend/internal/mcp/registry/registry.go:144-165, 220-242, 253-295, 297-350, 534-553` |
| MCP manager 调用归一化 | `backend/internal/mcp/manager/manager.go:337-373` |

## 6. 相关文档

- 命名与解析机制的完整设计背景与演进记录：`docs/plan/deepseek-reasonix-agent-tool-definition-analysis-plan.md`（重点：canonical identity、`ResolveToolForMCP` 优先级修复）。
- chrome-devtools MCP 接入与使用：`docs/mcp/chrome-devtools.md`。
- MCP 文档目录索引：`docs/mcp/README.md`。

---

## 7. 附录：验证"请求里实际发送的工具列表"

三条独立路径（按可信度排序）：

### 7.1 运行时会话工具面（协议转换前的冻结面）

```bash
# 精简清单（name + metadata.tool_source）
curl -s "http://127.0.0.1:8101/api/runtime/sessions/<session_id>/runtime/tools" | jq -r '.tools[].name'

# 完整定义（description/parameters/metadata）
curl -s "http://127.0.0.1:8101/api/runtime/sessions/<session_id>/runtime" \
  | jq -r '.state.stable_tool_surface[].name'
```

`state.stable_tool_surface_set=true` 时，该列表就是本会话冻结后进入每轮请求的 tools（再做 OpenAI/Anthropic/Gemini/Codex 协议转换）。

### 7.2 HTTP 请求体 artifact（CLI 会话，HTTP debug 捕获生效时）

路径：`%USERPROFILE%\.aicli\chat-logs\<yyyy>\<MM>\<dd>\<session>.<ts>.http\NNN_request_<source>.json`

- 小请求：`body_json` 为结构化对象；大请求退化为 `body_text`/`body_preview`（`body_truncated=true`，捕获上限 262144 字节，见 `cmd/aicli/commands/chat_http_artifacts.go`）。
- 单请求取工具名（OpenAI 协议）：

```powershell
jq -r '.body_json.tools[].function.name' <...>\001_request_provider_wrapper.json
```

- 聚合本会话全部请求中出现过的工具名：

```powershell
jq -r 'select(.body_json != null) | .body_json.tools[]?.function.name' <...>\.http\*request*.json | Sort-Object -Unique
```

### 7.3 observe 平面（实时观测，但**看不到请求体原文**）

`GET /api/runtime/observe/v1/capabilities` 的 redaction 声明明确剔除：`prompt`、`system_instruction`、`tool_arguments`、`tool_result`、`provider_http_body`、`reasoning`、`authorization`、`api_key`。observe 只提供 `request_body_bytes`、`tools_sha256`、`tool_count` 等诊断字段（网关侧 `reportHTTPDebug`，`backend/internal/llm/gateway_client.go:528-540`）。

### 7.4 实测样例（session_20260918172548_iHgz994o，2026-09-18）

| 观察项 | 结果 |
|--------|------|
| HTTP 请求 artifact | 112 个；47 个带完整 `body_json`，65 个截断 |
| 请求内 tools 数量 | 全部为 **41** |
| 聚合唯一工具名 | 41 个（broker/toolkit + `search_tool`），与 `stable_tool_surface` 完全一致 |
| chrome-devtools MCP | 已连接（`/web/api/mcps`：connected=true，toolCount=29），但 41 个请求工具中**不含**其任何工具 |
| 可发现性 | `search_tool` receipts：`query=list_pages` → 7 条、`query=pages browser open list` → 10 条命中（源自未投影完整目录 `fullCatalogForSearch`） |

**结论**：2026-09-18 起已移除目录规模投影——通过策略过滤的启用工具（含 MCP）直接进入请求的 `tools` 数组。工具规模改由「MCP 工具级启用/禁用」显式治理（§2.6）：`mcp.yaml` 的 `tools.<name>.enabled`，或管理 API 的 `POST .../mcps/{name}/tools/{tool}/enable|disable` 与批量 `POST .../tools/enable|disable`（空数组 = 全部）。被禁用工具不进入工具面/目录/搜索且不可执行；会话工具面按 prompt-cache 语义冻结，但 MCP 目录变化（异步建连完成 / 热重载 / 服务与工具启停）会经生命周期事件使活跃会话在下一个 turn 边界重建工具面（见 §8）。

---

## 8. MCP 延迟建连与会话冻结工具面的刷新机制（2026-09-18 修复）

### 8.1 问题链（为什么新程序下 `list_pages` 仍不可见）

1. aicli chat/TUI 启动走 `initMCPManagerAsync`（`backend/cmd/aicli/commands/mcp_integration.go:26-31`）：MCP 后台并行建连、立即返回；npx 型 server（chrome-devtools）从启动到工具发布有数秒延迟。
2. 会话工具面在首个 turn 冻结（`backend/internal/chat/turn_tool_surface_snapshot.go:95-113`）并持久化到 `RuntimeState`（`runtime_state.go:83-88`），resume 后沿用。
3. 旧刷新条件只覆盖「≤2 个核心工具的 goal-projection 面」（`loop.go:3367-3375` 的 `isSimpleGoalProjectedToolSurface`，判定见 `loop.go:3501-3522`）；完整 41 工具冻结面永远不满足 → MCP 工具整个会话不可见，即使 `ListTools()` 实时可见。

判定证据（2026-09-18）：`/web/api/mcps` 显示 chrome-devtools connected、toolCount=29；`/api/runtime/sessions/<id>/runtime/tools` 返回 41 个内置工具、0 个 MCP 工具。

### 8.2 修复（B + A）

- **B｜turn 边界能力追加刷新**（`backend/internal/agent/loop.go`）：`resolveAvailableTools` 的 cached 分支改用 `shouldRefreshStableToolSurface`：`liveMCPToolSurfaceAddsCapabilities` 检测到实时 MCP 目录包含冻结面缺失且通过执行策略的工具时，在 turn 边界重建；`sessionStableToolSurfaceState().refreshable`（即 `CanRefreshStableToolSurface()`）保证只在无在途 turn 冻结时发生。仅「新增能力」触发重建；移除方向交给 A，避免重连期目录抖动导致反复换 schema。
- **A｜MCP 生命周期事件显式失效**：
  - chat 层：`SessionActor.InvalidateStableToolSurface`（`turn_tool_surface_snapshot.go`）与 `SessionHub.InvalidateStableToolSurfaces`（`hub.go:127-154`）；在途 turn 保留 `FrozenTurnTools` 前缀，新 turn 边界重建。
  - 持久化：`chat.StableToolSurfaceInvalidator`（`session_runtime_store.go`）批量清理 SQLite / 内存 store 的 `stable_tool_surface_json`；无在途 turn 时同时清 `frozen_turn_tools_json`。
  - CLI：`wireChatMCPToolSurfaceInvalidation`（`cmd/aicli/commands/chat_mcp_surface_invalidation.go`）订阅 `mcp.connected / mcp.tools.loaded / mcp.reconnected / mcp.disabled / mcp.stopped / mcp.tool.state_changed`；管理面 refresh（`refreshChatWebMCPTools`）同样触发失效。
  - runtime-server：`attachRuntimeMCPLifecycleBridge`（`internal/api/skills/handler.go:4873`）在原有 tool catalog Refresh 之外调用 `invalidateSessionRuntimeToolSurfaces`。

### 8.3 生效时机

- 异步建连完成 / 热重载 / MCP 服务启停 / 工具级启停 → 活跃会话在**下一个 turn 边界**重建工具面（请求前缀只变一次）。
- 在途 turn 的工具 schema 不变（prompt cache 与 tool-call 连续性）。
- 新会话首个 turn 直接包含当时已发布的 MCP 工具。

---

## 9. 配置简化：默认建连 + 单一启停口径（2026-09-18）

- **移除的开关**：`mcp.yaml` 的 `global.autoConnect`（原本就无消费点）与 `aicli.mcp.auto_connect`（应用配置/env `MCP_AUTO_CONNECT`，仅被 runtime-server 用作启动门控）。两者删除后不再有“进程级自动建连”配置。
- **默认行为**：只要解析到实际存在的 MCP 配置文件，CLI chat 与 runtime-server 都在启动时异步并行建连（`initMCPManagerAsync` / `buildSkillsMCPManager` 的 `StartAsync`）；配置缺失时静默跳过。`~/.aicli/config.yaml` 里遗留的 `auto_connect:` 行、`mcp.yaml` 里遗留的 `global.autoConnect:` 行会被直接忽略（下次管理接口落盘时自动消失）。
- **唯一启停口径**：`mcpServers.<name>.enabled`（缺省 `true`；`disabled` 兼容官方写法）。运行期经管理接口 `POST /api/runtime/mcps/{name}/enable|disable`（CLI 微型 Web 同套 `internal/mcp/admin` 实现）持久化并重连；工具级另见 `mcpServers.<name>.tools.<tool>.enabled` 与 `/tools/{tool}/enable|disable`、`/tools/enable|disable`（空数组 = 全部）。
- **“整体停用 MCP”的口径**：让配置解析不到（删除/改名 mcp.yaml，或用 `config_file` 指向不存在路径），而不是再引入全局开关。

### 9.1 可观测性（2026-09-18）

- **启动日志**：runtime-server 加载 MCP 后输出 `MCP config loaded`，字段为 `path`（实际生效的绝对路径）、`source`（命中层：`explicit|project|user|upward|executable|default|user-fallback`）、`servers`、`enabled`；随后异步建连输出 `MCP manager starting in background`（`servers` / `enabled`）。
- **管理接口**：`GET /api/runtime/mcps` 在原有 `mcps` / `count` 之外新增：
  - `config`：`path` / `source` / `exists` / `size_bytes` / `mod_time` / `manager_loaded` / `candidates[]`（候选路径优先级清单，含 `exists`，文件属性每次读取实时刷新）；
  - `summary`：`{total, enabled, disabled, connected, tools}`。
- CLI 微型 Web 的同一接口共用该实现；会话覆盖场景 `source=session-override`。
- 解析函数：`aiclipaths.ResolveMCPConfigPathDetailed`（`ResolveMCPConfigPath` 委托它，路径语义不变）。
