# MCP 管理功能实施方案（console + 微型 Web 客户端）

> 状态：已完成（2026-09-18；逐条核对与遗留差异见文末「5. 实施状态核对」）
> 目标页面：`http://localhost:5193/runtime/config`、aicli 微型 Web 客户端 `/web/`
> 关联代码：`internal/mcp/{config,manager,registry}`、`internal/api/skills/handler.go`、
> `cmd/aicli/commands/{mcp.go,web_handlers.go,pprof.go}`、`frontend/src/pages/runtime-config-page.tsx`

## 1. 目标

1. console `/runtime/config` 增加 MCP 管理：列表、新增、编辑、删除、启用/停用、热重载、状态与工具数。
2. aicli 微型 Web 客户端（`/web/`）增加「MCP」页签，同等能力。
3. 两个前端共用同一份 `mcp.yaml` 与同一套后端核心代码。

## 2. 现状（侦察结论）

| 层 | 现状 |
|---|---|
| 配置 | `mcpServers` YAML/JSON；Loader 在 `internal/mcp/config`；**读写逻辑私藏在 `cmd/aicli/commands/mcp.go`**，HTTP 侧无法复用 |
| 运行时 | `internal/mcp/manager`：LoadConfig/Start/Stop/ReloadConfig/SetMCPEnabled/ListMCPs/GetMCPStatus；`SetMCPEnabled` 只改内存、**不落盘**、启用时不重连 |
| HTTP | 仅 `POST /api/runtime/mcps/reload`（handler.go:790）；console 前端零消费 |
| console | `frontend/` React + vite；`/runtime/config` → `BackendConfigSettingsPage`（mode 菜单 + domains/sections，i18n 双语，vitest） |
| 微型客户端 | aicli loopback `/web/`，内嵌 `commands/web/`（index.html + js/*.js）；已有 技能/配置/缓存 页签与写令牌校验 |

## 3. 设计

### 3.1 共享后端核心：`backend/internal/mcp/admin`

- 从 CLI 抽出：`LoadFile / SaveFile`（临时文件 + rename 原子写，yaml/json 双格式）、transport 归一化、
  `BuildConfig`（新增/更新项构建与校验，复用 `config.Loader`）。
- `Service`（持有 config path + `manager.Manager` + catalog 刷新回调）：
  - `List / Get / Add / Update / Remove / SetEnabled / Reload`
  - 每次变更：读文件 → 改 → 校验 → 原子写 → `manager.ReloadConfig()` → `manager.Start(ctx)` → 刷新回调。
  - `EnsureManager()`：manager 为空时按解析路径惰性创建（解决 `auto_connect=false` 时无法管理的问题）。
- CLI `aicli mcp add/remove/enable/disable/reload` 改调同一 Service（去重；顺带修复 enable/disable 不落盘）。

### 3.2 runtime-server HTTP API（`internal/api/skills/handler.go`）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/runtime/mcps` | 列表（配置 + 连接状态 + 工具数） |
| POST | `/api/runtime/mcps` | 新增 |
| PUT | `/api/runtime/mcps/{name}` | 编辑 |
| DELETE | `/api/runtime/mcps/{name}` | 删除 |
| POST | `/api/runtime/mcps/{name}/enable` `/disable` | 启用/停用 |
| POST | `/api/runtime/mcps/reload` | 热重载（已有，改走 Service） |

- 鉴权沿用 `authorizeUsageAdmin`（loopback 免 token / admin token）。
- 写操作受现有 mutation policy 约束（reload 受 `disable_reload_ops`）。
- `cmd/runtime-server/main.go` 注入解析后的 MCP 配置路径、惰性 manager、catalog 刷新回调。
- 配置文件不存在时在解析出的最高优先级可写路径创建（默认 `~/.aicli/mcp.yaml`）。

### 3.3 console 前端（`frontend/`）

- 新增 `src/api/runtime/mcp.ts` + 类型。
- `BackendConfigSettingsPage` 新增 mode `mcp`（菜单入口 + `sections/modes/mcp.tsx`）：
  - 列表：名称 / type / URL或命令 / 状态（connected、toolCount、trustLevel）/ 启用开关；
  - 操作：新增、编辑、删除（二次确认）、启用/停用、热重载；
  - 表单字段：name、type、command|url、args、headers/env、description、timeout、maxParallelCalls、trustLevel、enabled；
  - 独立数据流，不接入 config document 的 unsaved bar。
- i18n：`resources/{zh-CN,en-US}/runtime-config.ts` 增加 `mcp.*`。
- 测试：API 单测 + 面板 vitest。

### 3.4 微型 Web 客户端（`backend/cmd/aicli/commands/`）

- 新增 `web_mcp_handlers.go`：`/web/api/mcps`（GET/POST）、`/web/api/mcps/{name}`（PUT/DELETE）、
  `/web/api/mcps/{name}/enable|disable`、`/web/api/mcps/reload`；写操作复用 `/web/` 写令牌校验；
  内部调用 3.1 的同一 Service。
- 前端资产：`web/js/mcp.js` + `index.html` 加「MCP」页签/面板 + `js/ui.js` 挂载 + 复用 `style.css`。
- 测试：`web_mcp_handlers_test.go`（增删改查、写令牌、错误路径）。

## 4. 验证

1. `go build ./...`；`go test ./internal/mcp/... ./internal/api/skills/... ./cmd/aicli/commands/`。
2. 前端 `pnpm test`（定向）+ `pnpm build`。
3. 手动 e2e：runtime-server curl 走一遍增删改查/启停/热重载；console 与 `/web/` 页面各实操一遍
   （含 mcp-chrome streamable 实例）。

## 5. 实施状态核对（2026-09-18）

### 3.1 共享后端核心 `internal/mcp/admin` — 完成

- `configfile.go`：`LoadFile/SaveFile`（临时文件 + rename 原子写，yaml/json 双格式）、
  `NormalizeTransportType`、`IsURLTransport`、`BuildConfig`（复用 `config.Loader` 校验）、
  `EnsureFile`、`ValidationError/NotFoundError`；`service.go`：`List/Get/Add/Update/Remove/
  SetEnabled/Reload`，每次变更走 `applyLocked`（读→改→校验→原子写→`ReloadConfig`→`Start`）。
- `ensureManagerLocked` 支持 manager 未注入时按配置路径惰性创建（`auto_connect=false` 可管理）。
- CLI `aicli mcp add/remove/enable/disable/reload` 已改调同一 Service
  （`mcp.go:newMCPAdminService`）；顺带修复 enable/disable 不落盘（实测文件写入 `enabled:false`）。
- 测试：`service_test.go`，`go test ./internal/mcp/...` 全绿。

### 3.2 runtime-server HTTP API — 完成

- 7 条路由全部注册（`handler.go:804-810`），鉴权沿用 `authorizeUsageAdmin`；
  写操作受 `read_only` 策略约束（`mcp_admin_handlers.go:authorizeMCPAdminMutation`），
  reload 保留 `disable_reload_ops` 校验（`handler.go:10477`）并改走 `service.Reload`。
- `main.go` 注入解析后的 MCP 配置路径 + handler 级 catalog 刷新
  （`afterRuntimeMCPMutation` → `gateway.Refresh()` + 生命周期事件）；
  `auto_connect=false` 时只装配 manager 不建连；配置缺失时回落 `~/.aicli/mcp.yaml` 并由 admin 包创建。
  ⚠️ 与计划的差异：catalog 刷新回调落在 handler 层而非 Service 构造参数，效果等价。
- e2e（独立端口 + 真实 mcp-chrome streamable）：新增→`connected:true, toolCount:27`→停用（真断连、落盘）→
  编辑→热重载→启用（重连 27 工具）→删除→列表清空、文件清空。

### 3.3 console 前端 — 完成（两处形态差异）

- `api/runtime/mcp.ts` + `types/runtime/mcp.ts`；mode `mcp`（registry/type/use-config-state/page 渲染）；
  `sections/modes/mcp.tsx` + `mcp-form.tsx`；i18n 双语 `runtime-config/mcp.ts`；独立数据流，不接入 unsaved bar。
- 列表展示 name/type/URL或命令/connected/enabled/trustLevel/toolCount/lastError；
  操作含新增、编辑、删除（confirm）、启停、热重载、刷新。
- ⚠️ 差异 1：计划中的「启用开关」实现为图标按钮（PowerIcon + 独立 pending 态），无 switch 组件。
- ⚠️ 差异 2：表单字段齐全（name/type/command|url/args/env|headers/description/timeout/maxParallelCalls/
  trustLevel/enabled），但 env 仅 stdio、headers 仅 URL 传输（符合传输语义）。
- 测试：`mcp.test.ts` 8 例（API 层）+ `mcp-form.test.ts` 12 例（草稿归一/校验/组包/解析）；
  面板级渲染交互测试未做（仓库无 @testing-library/react，既有面板测试亦为 renderToStaticMarkup）。
- 定向 `pnpm exec vitest run src/components/workspace/settings src/api/runtime`：52 文件 / 341 用例通过；
  `vite build` 通过；`pnpm build` 目前被其它并行 WIP 文件的 4 个既有 tsc 错误阻塞（非本方案文件）。

### 3.4 微型 Web 客户端 — 完成（未实机点击）

- `web_mcp_handlers.go`：`/web/api/mcps`（GET/POST）、`/{name}`（GET/PUT/DELETE）、
  `/{name}/enable|disable`、`/reload`，内部调用同一 `mcpadmin.Service`；注册在
  `ChatWebAuthGuard` 包裹的 mux 上（`pprof.go:315`），写操作令牌校验沿用 `/web/` 统一守卫。
- 资产：`web/js/mcp.js`（新增）、`index.html` 页签/面板、`ui.js`/`app.js` 接线、`style.css` 复用。
- 测试：`web_mcp_handlers_test.go` 10 例（列表/新增/405/更新删除/启停/热重载/非法路径/400/404）
  + 嵌入资源可服务测试；令牌覆盖由通用 `web_auth_token_test.go` 保障，无 MCP 专用令牌用例。

### 4 验证 — 两条完成、一条部分

1. ✅ `go build ./cmd/... ./internal/...`；✅ `go test ./internal/mcp/... ./internal/api/skills/ ./cmd/aicli/commands/`。
2. ⚠️ 前端定向测试 ✅；`pnpm build` 受并行 WIP 阻塞（`vite build` ✅）。
3. ✅ runtime-server curl 全流程；❌ console 与 `/web/` 页面未实机点击（当前运行中的
   runtime-server / aicli 仍是旧二进制，需重启后新端点才可用）。

### 遗留事项

1. 两个前端面板的浏览器实机验证（需重启 runtime-server 与 aicli 进程）。
2. console 面板级渲染/交互测试（待仓库引入 @testing-library/react 或改用静态渲染断言）。
3. 启用开关形态（图标按钮 vs switch）与 catalog 刷新回调位置，如需严格对齐可再收敛。

## 6. 追加迭代（2026-09-18）：env/headers 行编辑器 + `aicli chat` 的 `/mcp`

### 6.1 console：结构化键值行编辑器

- 新增 `sections/modes/key-value-editor.tsx`（组件）与 `key-value-rows.ts`（纯逻辑：稳定 id、
  多行粘贴解析、重复/空键标注）；`mcp-form.tsx` 的 `McpDraft.env/headers` 由文本改为行数组，
  `createMcpDraft` 按传输拆分 `HEADER_*`、`buildMcpUpsertRequest` 合并回 `env` 并**始终下发
  `env`**（显式 `{}` 才能清空历史值）；重复键返回 `duplicateKey` 校验错误。
- 测试：`mcp-form.test.ts` 21 例 + `key-value-editor.test.tsx` 13 例；定向 `vitest`（settings+api）
  53 文件 / 363 用例通过；i18n lint 0 violations；`vite build` 通过。

### 6.2 微型 Web：同款行编辑器

- 新增 `web/js/mcp-kv.js`（纯函数 `splitMcpEnv/mergeMcpEnv/annotateKvRows/parseKvText` + DOM 层
  `renderKvRows/readKvRows/appendKvRow`，无 innerHTML）；`index.html` 两个 textarea 换成
  `.kv-rows` 容器 + 「＋ 添加一行」，`style.css` 补齐样式；`mcp.js` 改为拆分/合并、删除旧文本解析。
- 两端语义对齐：stdio 的 `HEADER_*` 视为普通环境变量（不拆前缀，保存不丢键），URL 传输才拆分
  为请求头行；重复键后者胜出并标红提示。

### 6.3 `aicli chat` 新增 `/mcp` 子命令

- `chat_mcp_command.go`：`/mcp [list|status <name>|add <name> <url> [--type|--trust|--header|--env|
  --description|--disabled]|add <name> --command <cmd> [--arg <a>]...|enable|disable|remove|reload|help]`，
  与 CLI/HTTP 共用 `internal/mcp/admin.Service`；写操作落盘 + 热重载，成功后把最新 MCP 工具
  重新注册进当前会话（复用 `MCPManagerInstance` 与 `registerMCPTools`）。
- 接线：斜杠命令目录（帮助/补全）、`handleCommand` 路由、统一命令分发、参数补全；新增
  `chat_mcp_command_test.go` 10 例（含结构化路由声明与请求映射），目录 ↔ 路由一致性测试通过。
- 文档：`docs/aicli/install.md` 命令表与说明、`docs/user-guide/runtime-server.md` 与
  `docs/aicli/web-remote-api.md` 的 env/headers 语义（省略=保持、`{}`=清空、非空=整体替换）。

### 6.4 追加迭代遗留

1. 两个前端页面的浏览器实机点击仍未执行（同上，需重启进程）。
2. `/mcp add|enable|disable|remove|reload` 会等待热重载/重连完成（60s 上限），连接慢的远端
   MCP 会让命令单元停留数秒；如需异步化可后续改为后台任务 + 事件回报。
3. 微型 Web 的行编辑器 DOM 层用自写 stub 验证（未引入 jsdom）；console 面板仍是逻辑层 +
   静态渲染测试。
