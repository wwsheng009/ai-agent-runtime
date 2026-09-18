# MCP 工具级启停与工具面直出改造计划

> 状态：待实施（2026-09-18 制定）；范围：backend（config/registry/manager/agent/api/CLI web）+ frontend + docs。

## 0. 目标（用户诉求）

1. **取消 `DefaultToolSearchThreshold = 24` 的自动隐藏逻辑**：MCP 工具默认直接进入模型请求的 `tools` 列表，不再因目录规模被搜索投影掉。
2. **新增 MCP 工具级启用/禁用**：服务级启停保留；工具级可逐个或批量启停，并持久化到 `mcp.yaml`。
3. **优化 MCP 管理模型与加载逻辑**，落到两套现有 UI：
   - `frontend/` 设置页 MCP 面板的「工具」对话框；
   - `aicli micro web client` 的 MCP 页签。
4. **默认不限制 MCP 工具进入 tools 列表**（禁用工具除外）。

## 1. 现状与关键证据（file:line）

| 模块 | 现状 |
|------|------|
| 搜索投影（问题根源） | `backend/internal/toolkit/search.go:13-15`（阈值 24）；`backend/internal/agent/loop.go:3468-3479`（会话面调用 `projectToolSurfaceWithSearch`）；`backend/internal/agent/tool_list.go:81-131`（非 core 工具投影隐藏，`ensureSearchToolPresent` 注入 `search_tool`） |
| 预算压缩（安全） | `loop.go:3748-3772` 只裁剪描述/注解（`compactToolDefinitionAnnotations`），**不移除工具**，保留即可 |
| 配置模型 | `backend/internal/mcp/config/types.go:92-107`（`MCPConfig`，服务级 `enabled/disabled`）；`loader.go:113-115` 缺省启用；`types.go:117-123` `IsEnabled()` |
| 配置写回 | `backend/internal/mcp/admin/configfile.go:50-63`（`UpsertRequest.Enabled *bool`）、`:155-269`（`BuildConfig` 合并）、`:124-150`（`SaveFile` 原子写） |
| 管理服务 | `backend/internal/mcp/admin/service.go:58-66`（接口）、`:258-284`（`SetEnabled`）、`:294-309`（`applyLocked` → `ReloadConfig`+`Start` 重连） |
| 注册表 | `backend/internal/mcp/registry/registry.go:23-28`（`ToolInfo`）；`:166-171` `RegisterTool(..., enabled)`；`:191-196` `ListTools` 只返回 `Enabled`；**`:390-424` 已有 `EnableTool/DisableTool/ToolEnabled`**（当前被健康检查使用：`manager.go:1061-1067`） |
| 工具注册 | `backend/internal/mcp/manager/manager.go:424-476`（`loadTools`，`:446` `RegisterTool(mcpName, tool, true)` 启用位硬编码 true）；`managers.go:219-236` 跳过停用服务 |
| 会话工具面 | `backend/internal/chat/turn_tool_surface_snapshot.go:83-115`（stable surface 冻结，跨回合复用）；`loop.go:3352-3386`（缓存命中直接返回）；变更后当前无失效机制 |
| 管理 API（runtime-server） | 路由 `backend/internal/api/skills/handler.go:818-825`；实现 `mcp_admin_handlers.go:51-112`（`ListRuntimeMCPTools` 只列已启用工具）、`:205-244`（服务级启停）、`:281-296`（写后仅 `gateway.Refresh` + 事件，不失效会话面） |
| 管理 API（CLI web） | 路由 `backend/cmd/aicli/pprof.go:304-307`；实现 `backend/cmd/aicli/commands/web_mcp_handlers.go:35-208`；`:214-246`（`chatWebMCPAdminService` + `refreshChatWebMCPTools` 只刷新 FunctionCatalog） |
| 前端 | `frontend/src/components/workspace/settings/backend-config-settings-page/sections/modes/mcp-tools-dialog.tsx`（纯展示工具列表，已有 `enabled` 标记但不可操作）；`mcp.tsx:464` 挂载；`api/runtime/mcp.ts`；`types/runtime/mcp.ts:122-135`；i18n `runtime-config/mcp.ts`（zh/en） |
| CLI web 前端 | `backend/cmd/aicli/commands/web/js/mcp.js`（MCP 页签：列表/新增/编辑/删除/启停/热重载，无工具级操作）；`web/js/mcp-kv.js`；静态资源 go:embed（`pprof.go:308-309`） |
| 相关测试 | `backend/internal/agent/tool_list_test.go:33-144,314`（投影语义）；`frontend/.../mcp-form.test.ts`；registry/manager/admin 各自单测 |

## 2. 设计决策

- **D1 工具面直出**：删除会话/回合工具面的自动搜索投影调用与阈值常量；`search_tool` 不再自动注入（保留 toolkit 实现代码，供未来显式注册）。`fullCatalogForSearch` 及其索引可一并删除或保留为死代码清理项。所有通过 `ShouldList`/策略过滤的授权工具（含 MCP）默认全部进入 `tools`。
- **D2 配置模型**：`MCPConfig` 增加
  ```go
  Tools map[string]MCPToolConfig `yaml:"tools,omitempty" json:"tools,omitempty"`
  type MCPToolConfig struct {
      Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
  }
  func (m *MCPConfig) IsToolEnabled(name string) bool  // 缺省 true
  ```
  缺省（无 `tools` 条目）= 启用，向后兼容。
- **D3 暴露语义（关键）**：区分「用户意图」与「运行时健康」：
  - `ToolInfo.Enabled` 保留为运行时可用位（健康检查写它，`manager.go:1064-1066`）；
  - 新增 `ToolInfo.UserEnabled`（配置意图，注册时由 `IsToolEnabled` 写入，运行时由管理 API 翻转）；
  - **暴露条件 `exposed = Enabled && UserEnabled`**，统一应用到 `ListTools`、`ResolveTool`、`ResolveToolForMCP`、`ListToolsForMCP`、`ExecutionLookupName`、`callableNameStats`、`FindTool`；
  - 健康检查成功不再"复活"被用户禁用的工具；
  - 新增 `ListAllTools`（含禁用，带两个状态位）供管理 UI 与 `GET .../tools` 使用。
- **D4 运行时启停不重连**：新增 `Service.SetToolEnabled(mcpName, tool, enabled)` / `SetToolsEnabled(mcpName, tools[], enabled)`：
  1) 写 `mcp.yaml`（`BuildConfig` 合并 `tools` 条目）；
  2) `manager.SetToolEnabled(...)` 直接翻转 registry 标志（不重启服务）；
  3) 刷新 catalog（`gateway.Refresh()` / CLI `refreshChatWebMCPTools`）；
  4) 发布 `mcp.tool.enabled/disabled` 事件；
  5) **失效已有会话的 stable tool surface**（新增 store 能力 `ClearStableToolSurface`，遍历已知会话；执行中的回合不改写，下回合生效）。
  失败或管理服务不可用时回退到现有 `Reload` 全量重连路径。
- **D5 API 契约**（两套后端同名形状）：
  - `GET /api/runtime/mcps/{name}/tools` → `{name,count,tools:[{name,description,enabled,configured_enabled,healthy,inputSchema}]}`（**包含被禁用工具**）；
  - `POST /api/runtime/mcps/{name}/tools/{tool}/enable|disable` → `{name,tool,enabled,configured_enabled,healthy?}`；
  - `POST /api/runtime/mcps/{name}/tools/enable|disable`，body `{"tools":["a","b"]}`（缺省/空数组 = 全部）；
  - CLI web 使用相同路径前缀 `/web/api/mcps/...`；写操作沿用 `X-AICLI-Token`（`web_auth.go:188-195`）；runtime-server 沿用 `read_only` 策略校验（`mcp_admin_handlers.go:247-256`）。
- **D6 UI**：
  - frontend：`mcp-tools-dialog.tsx` 每行加启用开关（switch 或按钮）、批量「全部启用/全部禁用」、禁用态徽标；`mcp.tsx` 打开工具对话框后回传状态刷新列表；`api/runtime/mcp.ts` + `types/runtime/mcp.ts` + i18n（zh/en 形状一致）。
  - CLI web：`js/mcp.js` 服务行内新增「工具」入口 → 工具面板（每行开关 + 批量）；沿用 `esc()/showToast()`；`web_mcp_handlers.go` 增加路由与 handler；`index.html/app.js` 如需容器则同步。

## 3. 实施批次与文件清单

### Batch 1 — 取消工具面搜索投影（P0）
- `backend/internal/agent/loop.go:3468-3479`：移除 `projectToolSurfaceWithSearch(...)` 调用；保留 `filterToolDefinitionsByShouldList`、`optimizeModelToolSurface`、`compactToolSurfaceToBudget`（只压缩描述）。
- `backend/internal/agent/tool_list.go`：删除 `projectToolSurfaceWithSearch`、`ensureSearchToolPresent`、`searchToolDefinition`、`buildToolSearchIndex`、`executeSearchTool`、`search_tool` 执行分支（`loop.go:2519-2530` 附近）及相关常量；`fullCatalogForSearch` 删除或保留为显式 API（默认删除）。
- `backend/internal/toolkit/search.go`：删除 `DefaultToolSearchThreshold`（`ToolSearchName`/`SearchTool` 实现保留）。
- 测试：`backend/internal/agent/tool_list_test.go` 投影用例改为「大目录全部直出」断言；新增用例：目录 40+ 且含 MCP 工具时，`tools` 不含隐藏项、无 `search_tool` 自动注入。
- 文档：`docs/mcp/mcp-tool-llm-integration.md` §1.2、§2.2、§7 更新为「默认直出」。

### Batch 2 — 配置模型（P0）
- `backend/internal/mcp/config/types.go`：`MCPConfig.Tools` + `MCPToolConfig{Enabled *bool}` + `IsToolEnabled(name)`；`ToolInfo` 增加 `ConfiguredEnabled bool`（JSON `configured_enabled`）与保留 `Enabled`（JSON `enabled`，有效暴露位）。
- `backend/internal/mcp/config/loader.go:179-227`：validate 接受 `tools` 映射（工具名非空、无重复语义冲突）。
- `backend/internal/mcp/admin/configfile.go`：`UpsertRequest.Tools map[string]MCPToolConfig`；`BuildConfig` 合并（未提供的工具保持原值；`tools` 整体缺省不覆盖）。
- `backend/internal/mcp/admin/service.go`：`SetToolEnabled` / `SetToolsEnabled`；`applyToolToggleLocked`（写文件 → 调用 manager 运行时翻转 → refresh 回调 → 事件）；接口 `AdminService` 扩展。
- 测试：config loader 解析/缺省；admin configfile 合并与原子写；service 启停 + 回退 reload。

### Batch 3 — 注册表/管理器与暴露语义（P0）
- `backend/internal/mcp/registry/registry.go`：
  - `ToolInfo.UserEnabled`（注册默认 true）；`RegisterTool` 增加参数或提供 `SetToolUserEnabled`；
  - 新增 `ListAllTools()`（含禁用，返回 `Enabled/UserEnabled`）；
  - 暴露谓词统一（`ListTools`/`ResolveTool`/`ResolveToolForMCP`/`ListToolsForMCP`/`ExecutionLookupName`/`callableNameStats`/`FindTool`）。
- `backend/internal/mcp/manager/manager.go`：
  - `loadTools`（:446）按 `MCPConfig.IsToolEnabled(tool.Name)` 写入用户意图；
  - 新增 `SetToolEnabled(mcpName, tool, enabled) error`、`SetToolsEnabled(...)`、`ListAllTools(mcpName)`；更新 `Manager` 接口；
  - 健康检查路径（:1061-1067）保持只写运行时位。
- 会话面失效：
  - `backend/internal/chat/session_runtime_store.go`：新增 `ClearStableToolSurface(ctx, sessionID)`（或等效 SaveState 变体）；
  - `backend/internal/api/skills/mcp_admin_handlers.go:281-296`：`afterRuntimeMCPMutation` 增加对已知会话的失效遍历；
  - `backend/cmd/runtime-server/main.go:1041-1046`：`NewService(..., WithManager(manager), WithRefresh(hook))` 装配 catalog + 会话刷新回调。
- 测试：registry 语义矩阵（用户禁用 + 健康禁用组合、canonical 解析、quarantine 不受影响）；manager 启停；会话面失效单测。

### Batch 4 — 管理 API 与两套 UI（P1）
- runtime-server：`mcp_admin_handlers.go` 新增 `ListRuntimeMCPTools`（改为全量 + 状态位）、`SetRuntimeMCPToolEnabled`、`SetRuntimeMCPToolsEnabled`；`handler.go:818-825` 注册路由。
- CLI web：`web_mcp_handlers.go` 新增 `/tools`（全量）、`/tools/{tool}/enable|disable`、`/tools/enable|disable`；`pprof.go:304-307` 注册；`chatWebMCPAdminService` 扩展。
- frontend：
  - `mcp-tools-dialog.tsx`：逐工具开关 + 批量 + 状态徽标（`configured_enabled` 与 `healthy` 区分展示）+ 操作后局部刷新；
  - `api/runtime/mcp.ts`、`types/runtime/mcp.ts`、i18n `runtime-config/mcp.ts`（zh/en）、`mcp-form.test.ts` 相邻测试；
  - 新增 `mcp-tools-dialog.test.tsx` 覆盖开关/批量/禁用态。
- CLI web：`web/js/mcp.js` 工具面板 + 开关/批量 + 状态展示；`mcp-kv.js` 复用 KV 行组件（如需）。
- 验证：`npx tsc -b --force`、`npm test`、`node scripts/verify-frontend-i18n.ts`、`npm run lint`（如可用）。

### Batch 5 — 文档与端到端验证（P1）
- 文档：`docs/mcp/mcp-tool-llm-integration.md`（投影移除、工具级开关、默认直出、§7 验证更新）；`docs/aicli/web-remote-api.md`（新增端点表）；`docs/plan/mcp-management-ui-plan.md` 追加二期注记；`docs/mcp/mcp-tool-llm-integration.md` §3 注意事项更新（allowlist 与禁用工具的关系）。
- 验证矩阵：
  1) `go test ./internal/mcp/... ./internal/agent/... ./internal/tools/... ./internal/chat/... ./internal/api/skills/... ./cmd/aicli/...`（backend 目录，PowerShell 单引号）；
  2) 手工 e2e（runtime-server）：连接 chrome-devtools → 工具面 41+29=70 直出 → 禁用 `list_pages` → 下一回合 reload 会话工具面 → 请求 artifact 中消失；重新启用后恢复；
  3) 手工 e2e（CLI web）：`/web/` MCP 页签逐工具开关 + 批量；`~/.aicli/chat-logs/*.http/*_request_*.json` 校验；
  4) frontend：`npm test -- mcp` 局部 + 全量。

## 4. 风险与兼容性

1. **工具面变大**：直出后 provider `tools` 体积上升，仅描述压缩（`compactToolDefinitionAnnotations`）兜底；token 压力由用户通过工具级开关治理（默认全开是明确要求）。
2. **健康检查与用户意图分离**：`enabled`（有效暴露）与 `configured_enabled`（用户配置）双状态必须在 API/UI/文档中一致表达，避免"健康成功又复活"。
3. **stable surface 失效时机**：仅在回合边界生效，避免同一回合工具前缀变更破坏 prompt cache；需要 store 的失效能力与已知会话枚举（若 store 无枚举接口，退化为按 session 注册表/`SessionHub` 遍历）。
4. **双实现同步**：runtime-server 与 CLI 共用 `admin.Service`，工具级写路径必须复用同一实现，避免行为分叉。
5. **向后兼容**：`mcp.yaml` 未写 `tools` 时行为不变（全部启用）；旧会话在下次工具开关前保持原冻结面。
6. **search_tool 移除**：`should_list=never` 等显式隐藏工具将不再可被模型发现；如未来需要，重新以配置化 opt-in 提供。

## 5. 开放问题（默认按右侧执行）

| 问题 | 默认决策 |
|------|----------|
| `search_tool` 是否保留常驻？ | 不保留（无隐藏工具即无必要；需要时后续以配置 opt-in 恢复） |
| 配置形态：`tools: {name: {enabled}}` vs `disabledTools: []` | 采用 `tools: {name: {enabled: bool}}`，缺省启用 |
| 批量接口语义：空数组 | 空/缺省 = 全部工具 |
| 被禁用工具的执行行为 | `FindTool` 返回 not found（fail-closed），不进入 catalog/search/工具面 |

---

## 6. 实施状态（2026-09-18 本轮更新）

### 已完成并通过测试
- ✅ **Batch 1 取消搜索投影**：删除 `DefaultToolSearchThreshold`、`projectToolSurfaceWithSearch`/`ensureSearchToolPresent`/`searchToolDefinition` 及 `loop.go` 调用；大目录工具（含 MCP）全部直出，`search_tool` 不再自动注入。测试：`internal/agent` 全绿（投影用例改写为「直出」断言）。
- ✅ **Batch 2 配置模型**：`MCPConfig.Tools map[string]MCPToolConfig`（`enabled` 指针，缺省启用）、`IsToolEnabled/SetToolEnabled`；loader 校验；`UpsertRequest.Tools` 合并写回。测试：`internal/mcp/config`、`internal/mcp/admin` 全绿。
- ✅ **Batch 3 注册表/管理器语义**：`ToolInfo.UserDisabled`（零值=启用）与 `Enabled`（运行时健康位）分离，`toolExposed = Enabled && !UserDisabled` 统一应用于 ListTools/Resolve/ExecutionLookupName/callableNameStats；`ListAllTools(ListAllToolsForMCP)`、`SetToolUserEnabled`；`manager.SetToolEnabled/SetToolsEnabled/ListAllToolsForMCP`（不重连）；`loadTools` 按配置应用禁用位。测试：`internal/mcp/registry`、`internal/mcp/manager`、`internal/tools`、`internal/skill` 全绿。
- ✅ **Batch 4a runtime-server API**：`GET /api/runtime/mcps/{name}/tools` 返回全量工具（`enabled/configured_enabled/healthy`）；新增 `POST .../tools/{tool}/enable|disable` 与批量 `POST .../tools/enable|disable`（空数组=全部）。测试：`internal/api/skills` 全绿。
- ✅ **Batch 4b CLI 微型 Web**：`web_mcp_handlers.go` 支持 `{name}/tools`、`{name}/tools/{tool}/enable|disable`、`{name}/tools/enable|disable`；`web/js/mcp.js` 工具弹窗支持逐工具开关 + 全部启用/全部禁用 + 健康异常徽标。测试：`cmd/aicli/commands -run MCP` 全绿。
- ✅ **文档**：`docs/mcp/mcp-tool-llm-integration.md`（§1.2 直出、新增 §2.6 工具级启停、§7 结论更新）；`docs/aicli/web-remote-api.md` 端点表新增三行。

### 待完成
- ⏳ **Batch 4c frontend（React 设置页）**：`mcp-tools-dialog.tsx` 的逐工具开关/批量按钮、`api/runtime/mcp.ts`、`types/runtime/mcp.ts`、zh/en i18n、`mcp-tools-dialog.test.tsx`。本轮派出的后台子任务批次未产出（stale），需重跑或本地实现。
- ⏳ **会话工具面显式刷新**：当前遵循 prompt-cache 冻结语义（`turn_tool_surface_snapshot.go:74-80`），工具开关对**已存在会话**在新会话或显式刷新后生效。如需同会话即时生效，需要新增 `ClearStableToolSurface` 能力与按会话遍历失效 + CLI runtime host 对应刷新。
- ⏳ 全量回归：`go test ./...`（本轮已覆盖 mcp/tools/agent/skill/api-skills/policy/cmd-aicli-MCP）；frontend `tsc -b`、vitest 全量待 Batch 4c 完成后执行。

### 本轮验证命令（backend）
`go build ./...`；`go test ./internal/mcp/... ./internal/tools/... ./internal/agent/... ./internal/skill/... ./internal/api/skills/... ./internal/policy/... -count=1`；`go test ./cmd/aicli/commands/ -run "MCP|Mcp" -count=1`。

### 更新（2026-09-18 第二轮）
- ✅ **Batch 4c frontend 已完成**（工作区未跟踪新文件，已核验契约一致）：
  - `types/runtime/mcp.ts`（`RuntimeMcpTool`/`RuntimeMcpToolsResponse`/`RuntimeMcpToolToggleResponse`/`RuntimeMcpToolsBulkToggleResponse`，含端点契约注释）；
  - `api/runtime/mcp.ts`（`listRuntimeMcpTools`、`setRuntimeMcpToolEnabled`、`setRuntimeMcpToolsEnabled` + URL builder，批量始终发送 `{tools:[]}`）；
  - `mcp-tools-dialog.tsx`（逐工具复选框、全部启用/禁用、`configured_enabled/healthy/notExposed` 徽标、pending 并发保护、成功后局部刷新、Esc/遮罩/焦点约定）；`mcp.tsx` 已接入。
  - i18n zh-CN/en-US `runtime-config/mcp.ts` 的 `mcp.tools.*` 完整。
- 验证：`npx vitest run mcp` → **3 个文件 39 用例全过**（api/runtime/mcp 11、mcp-tools-dialog 7、mcp-form 21）；`npm run lint:i18n` → violations=0；`npx tsc -b --force` 全仓 4 个错误均在无关文件（`artifact-output-link.tsx`、`workspace-shell.tsx`、`use-right-rail-width.test.tsx`，他人并行改动/既有问题），**MCP 相关文件 0 错误**。

### 更新（2026-09-18 第三轮：测试补强 + fail-closed 解析）
- registry：`ResolveTool`/`ResolveToolForMCP` 加入 `toolExposed` 过滤——被用户禁用（或健康位为 false）的工具**不可解析/不可执行**（`CallTool` 返回 not found），且不再参与短名歧义判定；`ListAllToolsForMCP` 仍保留全量清单供管理端展示。
- 新增单元测试：
  - `internal/mcp/config/config_test.go`：`TestMCPConfig_ToolEnableDisableSemantics`、`TestMCPConfig_ToolEnableSurvivesFileRoundTrip`（YAML 解析 + SetToolEnabled 删除显式条目 + 文件 round-trip）。
  - `internal/mcp/registry/registry_test.go`：`TestSetToolUserEnabledGatesSurfaceResolutionAndInventory`、`TestSetToolUserEnabledUnknownToolFailsClosed`。
  - `cmd/aicli/commands/web_mcp_handlers_test.go`：单工具启停、批量启停（body tools）、工具清单含禁用项与 `configured_enabled` 字段的成功路径。
- 验证：`internal/mcp/{config,registry,manager,admin,catalog}`、`internal/{tools,agent,skill,api/skills}`、`cmd/aicli/commands -run "MCP|Mcp"` 全绿。

### 最终验证汇总（2026-09-18）
- **backend**：`go build ./...` ✓；改动文件 `gofmt -l` clean ✓；定向套件 `internal/mcp/{admin,catalog,client,config,manager,protocol,registry,transport}`、`internal/{tools,agent,skill,api/skills,policy}`、`cmd/aicli/commands -run "MCP|Mcp"` 全绿 ✓。
- `go test ./internal/...` 全量：唯一失败为 `internal/api/skills TestSessionAgentController_NoReclaimNoEvent`（满负载下时序偶发）；隔离 `-count=3` 重跑 3/3 通过，与本次改动无关。
- **frontend**：`npx vitest run mcp` → 3 文件 39 用例全过；`npm run lint:i18n` → violations=0；`npx tsc -b --force` → 全仓 4 个错误均在 MCP 之外（`artifact-output-link.tsx`、`workspace-shell.tsx`、`use-right-rail-width.test.tsx`，他人并行修改/既有），MCP 相关文件 0 错误。
- **可选后续**：同会话即时生效需要新增 `ClearStableToolSurface` 并按会话遍历失效（当前实现遵循 prompt-cache 冻结语义：开关对新会话立即生效）。

## 第四轮（2026-09-18）：MCP 延迟建连 → 会话冻结工具面缺失的修复（B + A）

### 症状与根因
- 症状：新程序下 `session_20260918172548_iHgz994o` 的 `/runtime/tools` 固定 41 个内置工具，无 `list_pages`；`/web/api/mcps` 显示 chrome-devtools connected、29 工具。
- 根因：`initMCPManagerAsync`（chat/TUI 启动）后台并行建连，工具面在首个 turn 即冻结（freeze-once + 持久化，resume 沿用）；旧刷新条件只处理 ≤2 核心工具的 goal-projection 面，完整冻结面永不重建。

### 实现
- **B**（`internal/agent/loop.go`）：`resolveAvailableTools` cached 分支新增 `shouldRefreshStableToolSurface` / `liveMCPToolSurfaceAddsCapabilities`：实时 MCP 目录含冻结面缺失且通过策略的工具时，在 turn 边界重建（仅新增方向；移除方向交给 A 事件）。
- **A**（`internal/chat`）：`SessionActor.InvalidateStableToolSurface`、`SessionHub.InvalidateStableToolSurfaces`、`chat.StableToolSurfaceInvalidator`（SQLite/内存 store 批量清理；在途 turn 保留前缀冻结）。
- **CLI 事件接线**（`cmd/aicli/commands/chat_mcp_surface_invalidation.go`）：订阅 `mcp.connected / mcp.tools.loaded / mcp.reconnected / mcp.disabled / mcp.stopped / mcp.tool.state_changed`，异步清理活跃会话；`initMCPManagerWithMode` 注册；`refreshChatWebMCPTools` 同步触发。
- **runtime-server 事件接线**（`internal/api/skills/handler.go`）：`attachRuntimeMCPLifecycleBridge` 扩展 `mcp.tool.state_changed` 并调用 `invalidateSessionRuntimeToolSurfaces`（覆盖持久化面）。

### 测试
- `internal/agent`：`TestReActLoop_ResolveAvailableTools_RefreshesFrozenSurfaceWhenMCPToolsAppear`。
- `internal/chat`：`TestSessionActor_InvalidateStableToolSurface`、`TestSessionHub_InvalidateStableToolSurfaces`、`TestRuntimeStores_InvalidateStableToolSurfaces`（sqlite + in_memory）。
- `internal/api/skills`：`TestHandler_InvalidateSessionRuntimeToolSurfaces`（支持/不支持扩展的 store 分支）。

### 文档
- `docs/mcp/mcp-tool-llm-integration.md` 新增 §8（问题链、B+A 修复、生效时机）。
