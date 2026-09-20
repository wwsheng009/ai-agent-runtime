# ACP × MCP 集成实施计划

更新时间: 2026-09-20（第二轮：按 Zed 客户端实际行为定稿 §5，新增 §2.5 与 §11；第三轮：P0–P2 落地 + 工具面断点修复，新增 §13）

状态: **已实施**（P0–P2 已落地并自测通过；§5 决策已定稿；P3 剩余项见 §6.4；实测证据见 §13）

关联文档:
- [../acp/README.md](../acp/README.md)（ACP 协议实现与端到端自测说明）
- [./acp-v1-capability-gap-implementation-plan-20260919.md](./acp-v1-capability-gap-implementation-plan-20260919.md)
  （§11.1 A4 / A8，§11.4-D2 为本文档的上游裁定）

证据口径: 本文档所有「已核实」结论均来自工作树代码、ACP v1 schema 本地快照
（`.tmp/acp-schema.json`，170 个 `$defs`）与 **Zed 上游源码快照**（`.tmp/zed_*.rs`，
2026-09-19/20 抓取，见 §10.6）。schema 等价上游为
<https://agentclientprotocol.com/protocol/schema>。**推断项与待实测项均显式标注**，
不得当作已验证事实引用。

---

## 1. 结论摘要

ACP 与 MCP 的集成现状是**三层分离**，三层状态各不相同：

| 层 | 状态 | 一句话 | 关键证据 |
|---|---|---|---|
| 协议字段层 | **已解析、零消费** | `session/new` / `session/load` / `session/resume` 的 `mcpServers` 只落进 `json.RawMessage`，全仓库无任何读取点 | `internal/acp/types.go:484,708,729`；grep `MCPServers` 仅命中类型声明 |
| 能力声明层 | **诚实声明不支持** | `initialize` 返回 `mcpCapabilities: {http:false, sse:false}` | `internal/acp/types.go:1424-1427` |
| 运行时 MCP 层 | **已就绪，且 ACP 会话实际在用** | ACP 宿主复用 `bootstrapChatSession` → 初始化 MCP 管理器并把 MCP 工具注册进函数目录 | `agent_stdio.go:622` → `chat_setup.go:511` → `:408`、`:414-418` |

> 注：上表是**开工前基线**（P0 立项时口径，写于第三轮落地之前）。落地后的真实状态见 §13.1
> ——`mcpServers` 已被消费并生效（P0–P2 已落地）。

由此得到三条此前容易被误读的结论：

1. **客户端下发的 MCP server（`session/new.mcpServers`）完全没有接**——字段解析后即丢弃，
   这是 §11.4-D2 的既定裁定，不是缺陷遗漏。
2. **本地配置链里的 MCP server，ACP 会话当前就能用**——因为 ACP 会话与交互式 chat 共用
   同一套会话引导路径。文档口径若写成「ACP 不支持 MCP」是不准确的。
3. **Zed 真的会下发，而且来源不止用户手写配置**——Zed 把 `context_servers` 转成
   `mcpServers`，在 `session/new` / `load` / `resume` 三个入口都下发，并按 `enabled` 过滤
   （§2.5 Z1/Z2/Z12）；来源包含**项目级 `.zed/settings.json`**（§2.5 Z11）。
   Zed 侧另有 Restricted Mode（未信任 worktree 阻止 MCP server 安装与启动），但它
   **只覆盖项目级、全局 server 不受门控**，且该机制与转发列表的关系未确认（§4.9 补充段）
   ——所以「仓库级配置进入下发列表」这条路必须由**我们**兜住，§4.9 的安全模型
   从「加固项」变成了**上线前置条件**。

本文档的目标是把第 1 条从「解析但不生效」推进到「按协议生效」，并同时收口第 2 条
目前**零测试覆盖**的现状。

---

## 2. 协议规范要求（ACP v1）

### 2.1 请求字段与必填性

| 方法 | `mcpServers` 必填性 | `additionalDirectories` | 备注 |
|---|---|---|---|
| `session/new` | **required**（`["cwd","mcpServers"]`） | 可选 | 客户端几乎总会下发该字段 |
| `session/load` | **required**（`["mcpServers","cwd","sessionId"]`） | 可选 | 恢复会话时同样要连 |
| `session/resume` | 可选（required 仅 `["sessionId","cwd"]`） | 可选 | 重连语义，客户端可能不再下发 |

含义：**「忽略该字段」在协议上是合法的降级，但客户端会持续下发**。我们当前用
`json.RawMessage` + `omitempty` 接收，既不会因形状不符报错，也不会做任何事——
这是前向兼容的最小实现。

### 2.2 `McpServer` 是联合类型（union）

| 变体 | 判别式 | 必填字段 | 规范门控 |
|---|---|---|---|
| stdio | **无 `type` 字段** | `name, command, args, env` | "All Agents MUST support this transport" |
| http | `type: "http"` | `name, url, headers` | 仅当 `mcpCapabilities.http == true` |
| sse | `type: "sse"` | `name, url, headers` | 仅当 `mcpCapabilities.sse == true` |

两个实现时极易踩的形状细节：

- `env` 与 `headers` 都是 **`[{name, value}]` 数组**（`EnvVariable` / `HttpHeader`），
  **不是 map**。Go 侧直接 `map[string]string` 反序列化会失败。
- stdio 变体没有 `type` 判别式，只能靠「是否存在 `command`」判定；
  实现时不能假设 `type` 字段总是存在。

### 2.3 容错要求

schema 对这三个字段统一带 `x-deserialize-skip-invalid-items: true`。规范意图是：
**客户端可以下发本 agent 不认识的 server 条目，agent 应跳过该条目而不是让整个
`session/new` 失败**。这直接决定了 §4.10 的错误语义设计。

### 2.4 能力位语义

`agentCapabilities.mcpCapabilities` 的 `http` / `sse` 描述的是
**agent 侧连接 MCP server 的传输能力**（`"Agent supports McpServer::Http"`），
默认 `{http:false, sse:false}`。stdio 不设能力位——它是强制项。

因此当前 `{false, false}` 的声明与「不消费 `mcpServers`」是**自洽**的：
既没承诺 http/sse，也没承诺 stdio 的连接行为。一旦 P1 落地（stdio 生效），
这个自洽性就被打破——见 §4.8。

### 2.5 Zed（ACP 客户端）的实际行为【已核实】

> 证据来源：本地源码快照 `.tmp/zed_agent_servers_acp.rs`（对应上游
> `crates/agent_servers/src/acp.rs`）与 `.tmp/zed_context_server_store.rs`（对应上游
> `crates/project/src/context_server_store.rs`），2026-09-19/20 抓取，复现命令见 §10.6。
> 行号指本地快照。**本节结论只对 Zed 成立**，其它客户端需另行核实。

| # | 事实 | 证据 | 对实现的影响 |
|---|---|---|---|
| Z1 | Zed **会**把 `context_servers` 转成 `mcpServers` 下发，不是发空数组 | `mcp_servers_for_project()`：`zed_agent_servers_acp.rs:4386-4432` 读 `project.context_server_store().configured_server_ids()` | D1 的默认值必须以「用户配置」为前提；「默认不连」会造成与用户预期不符的静默差异 |
| Z2 | `session/new`、`session/load`、`session/resume` **三个入口都调用同一收集函数** | `:1636`（new）、`:1766`（load）、`:1810`（resume） | 三个入口必须共用同一条解析→映射→建连路径（§4.6） |
| Z3 | 映射规则：`Custom{command}` / `Extension{command}` → `McpServer::Stdio(name=server id)`；`Http{url,headers}` → `McpServer::Http` | `:4395-4427` | 与 §4.4 映射表一致；`name` 是 settings 里的键（用户可控字符串） |
| Z4 | **Zed 不会发送 SSE**：其配置枚举只有 `Custom` / `Extension` / `Http` 三个变体 | `zed_context_server_store.rs:160-176` | SSE 优先级低于 HTTP；仍须按 §2.3 容错跳过（其它客户端可能发） |
| Z5 | HTTP 条目**丢弃 `timeout` 与 `oauth`**，只保留 `url` + `headers` | `:4415-4427`（`timeout: _`、`oauth: _`） | 受 OAuth 保护的远程 server 下发到我们这里必然缺凭据 → 必须可见诊断，不能静默失败 |
| Z6 | **Zed 不检查 `mcpCapabilities`**：客户端侧无任何 `mcp_capabilities` 读取点 | 在 `zed_agent_servers_acp.rs` / `zed_acp_thread.rs` / `acp_client.rs` grep `mcp_capabilities\|mcpCapabilities` → **零命中** | 能力位**不是**保护手段：声明 `{http:false}` 挡不住 http 条目，必须「跳过 + 诊断」 |
| Z7 | 远程项目只转发 `remote: true` 的 stdio server；HTTP 条目**不受**该门控 | `:4404`（`if is_local \|\| *remote`）；Http 分支无守卫 | 解释「远程项目下 server 变少」；与我们的信任模型无关 |
| Z8 | `additionalDirectories` 由 `agentCapabilities.sessionCapabilities.additionalDirectories` 门控，我们未声明 → Zed 恒发 `[]` | `:1745-1750`；`internal/acp/types.go:369-373` 无该字段 | D6「不做」在 Zed 下**无行为差异**；将来要做时，开关就在能力位上 |
| Z9 | `session/fork`（`mcpServers` 的第 4 个入口）在 crate 中为 `#[cfg(feature = "unstable_session_fork")]`，稳定 schema 无此方法 | `.tmp/acp_agent.rs:1117-1120`；`.tmp/acp-schema.json` grep `Fork` 零命中 | 仅登记为前瞻项，不进本期 |
| Z10 | Zed 在 `Drop` 中 `child.kill()` 结束 agent 进程 | `:1557-1562` | agent 被强杀时，其 stdio MCP 子进程需我们兜底回收（Windows 无进程组语义） |
| Z11 | **项目级 settings 参与合并**：`resolve_all_context_server_settings()` 遍历 `visible_worktrees` 后读 `ProjectSettings::get(...).context_servers`，该函数内**无信任过滤**；但 Zed 另有 **Restricted Mode**（未信任 worktree 阻止 MCP server 安装与启动、需用户确认），二者与**转发列表**的关系**未确认** | `zed_context_server_store.rs:1133-1156`（`:1146` 读取点）；官方文档 `zed.dev/docs/trusted-worktrees`：*"Restricted Mode prevents: … MCP servers from being installed and spawned"* | 项目级来源**可能**进入下发列表；不能把门控外包给客户端，见 §4.9 |
| Z12 | `configured_server_ids()` **只按 `enabled` 过滤**（被禁用的 server 不下发）——这是上游 PR **#43467**（2025-11-25，*"ACP agents would start MCP servers that were disabled in Zed"*）的修复 | `zed_context_server_store.rs:377-384`（`.filter(\|(_, e)\| e.settings.enabled())`） | 用户「禁用」即不下发，我们**不需要**再实现启用筛选 |
| Z13 | 官方文档确认转发语义：*"Zed-configured MCP servers **may be forwarded** to External Agents over ACP. External Agents may also read their own native MCP configuration."* | `zed.dev/docs/ai/external-agents`（#mcp、#configuration-boundaries 表）；`zed.dev/docs/ai/mcp`「Agent Path Support」表 | 默认行为应与该语义一致：收到即视为用户意图 |
| Z14 | 规范要求客户端 **MUST** 在使用 http/sse 前校验 agent 能力；Zed 未做（见 Z6） | `agentclientprotocol.com/protocol/v1/session-setup`：*"Before using HTTP or SSE transports, Clients MUST verify the Agent's capabilities during initialization"*；同页：*"All Agents MUST support connecting to MCP servers via stdio"* | Z6 属 **Zed 侧合规缺口**，短期不会修 → 降级必须由我们实现 |

**一句话**：Zed 侧「需要实现的功能」只有一件——把 `context_servers` 收集成 `mcpServers` 下发。
**不存在**客户端侧的 MCP 工具路由（crate 里的 `McpServer::Acp` 变体由 `unstable_mcp_over_acp`
特性门控，Zed 未启用），因此我们只需处理「入站 server 列表」，不需要新增出站通道。
（该方向的早期实现见上游 PR **#36752**，2025-08-22 *"acp: Support calling tools provided by MCP servers"*；
若它将来转正，才需要评估出站通道，属 §11.3 的前瞻项。）

---

## 3. 现状盘点

### 3.1 协议字段层：已解析、零消费

> **本节是改造前基线**（第三轮落地之前的口径）。当前实现见 §13.1：`MCPServers` 已有消费点
> （`agent_stdio_mcp.go` 的 `toMCPConfig` + `acp_mcp_host.go` 的装配/门控）。原文保留，
> 用于对照「集成前无存量行为需要兼容」这一判断。

| 位置 | 内容 |
|---|---|
| `internal/acp/types.go:481-485` | `NewSessionRequest{ Cwd, AdditionalDirectories, MCPServers json.RawMessage }` |
| `internal/acp/types.go:704-709` | `LoadSessionRequest`（同上三个字段 + `SessionID`） |
| `internal/acp/types.go:725-730` | `ResumeSessionRequest`（同上） |
| `internal/acp/server.go:548-559` | `handleSessionNew`：decode → 直接透传 backend，不解释字段 |
| `cmd/aicli/commands/agent_stdio.go:217-263` | `NewSession` **只读 `req.Cwd`**（含 `os.Chdir`，即 A5 副作用），其余字段丢弃 |
| `cmd/aicli/commands/agent_stdio.go:695` | 注释：`MCPServers on load requests are ignored (not supported by this host).` |
| `cmd/aicli/commands/agent.go:80` | `--help` 文案：`MCPServers 参数暂不支持。` |
| `docs/acp/README.md:379-383` | FAQ：字段保留但不生效；http/sse 能力位均 false；口径为「继续接受、暂不生效」 |

grep `MCPServers` 全仓库命中：`internal/acp/types.go` 三处声明 + `scripts/acp_e2e_*.go`
中**客户端侧**发送的 `"mcpServers": []`。**没有任何消费点**——这既是「未集成」的直接证据，
也意味着任何集成改造都不会有存量行为需要兼容。

### 3.2 能力声明层

- `internal/acp/types.go:441-445`：`MCPCapabilities{ HTTP bool; SSE bool }`。
- `internal/acp/types.go:1413-1437`：`DefaultAgentCapabilities()` 返回 `{HTTP:false, SSE:false}`。
- `internal/acp/server.go:211-218`：三项能力全空时才安装默认值（显式传入的空结构体不会被覆盖）。

### 3.3 运行时 MCP 层（已就绪）

| 能力 | 位置 | 说明 |
|---|---|---|
| 配置模型 | `internal/mcp/config/types.go:19-22` | `Config.MCPServers map[string]MCPConfig`，YAML/JSON 键名均为 `mcpServers` |
| 单 server 配置 | `internal/mcp/config/types.go:91-108` | `Name/Type(stdio\|sse\|websocket\|streamable)/Command/Args/URL/Env/Enabled/Timeout/Tools/TrustLevel...` |
| **无 Headers 字段** | 同上 | 远程传输的 HTTP 头靠 `Env` 承载（见下） |
| 远程头注入 | `internal/mcp/transport/streamable.go:47-53`、`transport/websocket.go:63-73` | `buildHeadersFromEnv(env)`：**把 `Env` 的每个 key/value 直接 `headers.Set(k,v)`** |
| 信任等级 | `internal/mcp/config/types.go:174-187` | `ResolvedTrustLevel()`：stdio→`local`，其余→`untrusted_remote` |
| 管理器接口 | `internal/mcp/manager/manager.go:37-105` | `LoadConfig/Start/StartAsync/Stop/ListTools/CallTool/FindTool/SetMCPEnabled/SetToolEnabled/ListMCPs/ReloadConfig/AddLifecycleObserver` |
| **无动态加 server API** | 同上 | 只有 `LoadConfig(path)`（:175）与 `ReloadConfig()`（:832）；无 `AddServer`/`RegisterServer`（grep 确认） |
| 工具命名 | `internal/mcp/registry/registry.go:231-251` | 唯一原始名保持原名；冲突时用规范名 `mcp__<server>__<tool>`（超长截断+哈希后缀） |
| 工具合并 | `internal/tools/manager.go:35-140` | `Manager` 统一 toolkit + MCP；MCP 优先；`shouldPreferLocalToolkit` 让 `ls/glob/grep/view` 保持本地实现 |

### 3.4 ACP 会话当前实际可用的 MCP（已核实链路）

```
cmd/aicli/commands/agent_stdio.go:622   bootstrapChatSession(cfg, chatOpts, profileState, persistenceState, runtimeState)
  └─ cmd/aicli/commands/chat_setup.go:511   initializeChatCapabilities(cfg, opts, session)
       ├─ chat_setup.go:394-398  若 configureRuntimeServerChatExecutor 命中（--runtime-mode=server）→ 提前返回，本地 MCP 不初始化
       └─ chat_setup.go:408      prepareChatMCPManager(cfg, session)        ← 初始化 MCP 管理器
          chat_setup.go:414-418  runtimetools.NewDefaultManagerWithRuntimeConfig(MCPManagerInstance, ...)
                                 → toolManager.ListTools() 全量注册进 session.FunctionCatalog
```

配置来源解析（`cmd/aicli/commands/mcp_integration.go`）：

| 步骤 | 位置 | 行为 |
|---|---|---|
| 路径优先级 | `internal/aiclipaths/paths.go:176-184` | `./.aicli/mcp.yaml` > `~/.aicli/mcp.yaml` > 显式 override > 向上搜索 > 可执行目录 > `configs/mcp.yaml` |
| 会话覆盖 | `mcp_integration.go:120-132` | 会话级 `MCPConfigPath`（profile 提供）优先 |
| **信任门控** | `mcp_integration.go:134-157` | 项目级配置在 folder trust 未授权时**被拒绝**；用户级路径始终放行；配置不存在则静默跳过 |
| 管理器初始化 | `mcp_integration.go:22-71` | 进程级单例 `MCPManagerInstance`（:18），同路径重复调用直接返回；支持异步建连（`StartAsync`） |

结论（已核实）：**只要本地配置链里有启用的 MCP server，`aicli agent stdio` 建立的 ACP 会话
就会连上它，且其工具对模型可见**。注意两点限定：

- `--runtime-mode=server` 时本地不初始化 MCP（由 runtime-server 侧负责）。
- 建连是**异步**的（`initMCPManagerAsync`），而工具注册发生在引导期的一次性快照上，
  见下节。

### 3.5 差距清单

| # | 差距 | 现状 | 影响 |
|---|---|---|---|
| G1 | 客户端下发的 `mcpServers` 不生效 | `json.RawMessage`，零消费 | Zed 等客户端配置的 MCP server 在 ACP 会话里不可用 |
| G2 | 无类型化 `McpServer` 模型 | 无 stdio/http/sse 判别与 `{name,value}` 形状支持 | 任何集成方案的前置 |
| G3 | 会话级 MCP 作用域缺失 | 进程级单例，多会话共享 | 会话私有 server 无法隔离/回收 |
| G4 | 无动态增删 server 的 API | 只有 `LoadConfig(path)` / `ReloadConfig()` | 需新增 API 或落临时配置文件 |
| G5 | `MCPConfig` 无 `Headers` 字段 | 远程传输复用 `Env` 当 header | ACP 的 `headers` 只能塞进 `Env`（语义混淆） |
| G6 | 工具面注册是一次性快照 | 引导期 `ListTools()` 注册；失效通道只覆盖 web（`chat_mcp_surface_invalidation.go:40-47` 走 `chatWebSession()`） | **推断**：异步建连晚到的 MCP 工具，ACP 会话首轮可能不可见（待实测，见 §7.4） |
| G7 | ACP × MCP 零测试 | `agent_stdio*_test.go` 中 grep `mcp` 无命中 | 3.4 这条**已能跑通**的链路也无回归保护 |
| G8 | 能力位与实际不符的风险 | `{false,false}` 现在自洽 | P1 落地后 stdio 已生效但能力位仍全 false，需重新定义自洽口径 |
| G9 | 客户端下发条目可能**超出我们声明支持的传输** | 我们声明 `{http:false,sse:false}`，但 Zed 不读该位、照发 http（§2.5 Z6） | 「跳过」必须带**可见诊断**，否则用户观感是「配了却没生效」 |
| G10 | 子进程回收只在正常 `Stop()` 路径内 | Zed 退出时直接 `kill` agent 进程（§2.5 Z10） | agent 被强杀时 stdio MCP 子进程可能变孤儿（Windows 无进程组语义），需兜底 |

---

## 4. 方案设计

### 4.1 目标与非目标

**目标**

1. `session/new` / `session/load`（可选 `session/resume`）下发的 `mcpServers` 按协议生效：
   stdio 变体必做，http/sse 视能力位分期。
2. 与本地配置链的 MCP **共存**，且行为可预测（同名冲突有明确规则）。
3. 会话级隔离与生命周期回收：一个会话的 server 不泄漏到另一个会话，`session/close` /
   `session/delete` / 进程退出都能收干净。
4. 单个 server 失败**不阻塞**会话创建（协议要求 `session/new` 必须返回 `sessionId`）。
5. 安全边界清晰：协议输入不得绕过既有的 folder trust / 工具策略模型。

**非目标**

- 不实现 ACP 的 `fs/*` / `terminal/*` 客户端能力族（与本议题无关）。
- 不改动交互式 chat（`aicli chat`）与 runtime-server 的 MCP 行为。
- 不实现 MCP 资源（resources）/ 提示（prompts）在 ACP 侧的暴露——仅工具面。

### 4.2 总体数据流

```
session/new(req.MCPServers)                       session/new(cwd)
        │                                               │
        ▼                                               ▼
 [解析] decodeMcpServers()                     [既有] resolveChatMCPStartupConfigPath()
   → []acp.McpServer（跳过非法条目）              → 本地配置链 mcp.yaml（folder trust 门控）
        │                                               │
        ▼                                               │
 [映射] toMCPConfig()  → config.MCPConfig              │
        │                                               │
        ▼                                               ▼
 [归并] 会话级 MCP 作用域（client 下发 + 本地配置，冲突规则见 §4.5）
        │
        ▼
 [连接] 会话级 manager / 带会话标签的 server 集合（异步建连）
        │
        ▼
 [注册] 工具面注册（连接就绪后刷新，见 §4.7）
        │
        ▼
 [回收] session/close · session/delete · 进程退出
```

### 4.3 解析层：类型化 `McpServer`

新增 `internal/acp/mcp.go`（或 `types_mcp.go`）：

```go
// McpServer 是 ACP v1 的联合类型：stdio 无判别式，http/sse 靠 type 字段。
type McpServer struct {
    // Type 取值 "http" / "sse" / ""（空 = stdio）
    Type    string
    Name    string
    Command string        // stdio
    Args    []string      // stdio
    URL     string        // http / sse
    Env     []EnvVariable // stdio
    Headers []HTTPHeader  // http / sse
}

type EnvVariable struct{ Name, Value string }
type HTTPHeader  struct{ Name, Value string }
```

解析要点：

- 输入是 `json.RawMessage`，用 `[]json.RawMessage` 先拆数组，**逐条**解析；
  单条失败只记日志并跳过（落实 §2.3 的 `skip-invalid-items` 语义）。
- 判定顺序：有 `type:"http"` → http；有 `type:"sse"` → sse；否则若 `command` 非空 → stdio；
  其余（未知 `type`、缺 `command`、缺 `url`）→ 跳过。
- 保留 `_meta` 原文（`json.RawMessage`）以便未来扩展，但本阶段不消费。
- 请求类型里的字段类型从 `json.RawMessage` 改为 `[]McpServer` 会破坏「任意形状都不报错」的
  现状；**建议保留 `json.RawMessage` 字段，由显式解析函数处理**，避免严格反序列化把
  整个 `session/new` 打挂。

### 4.4 映射层：`McpServer` → `config.MCPConfig`

| ACP 字段 | `MCPConfig` 字段 | 转换规则 |
|---|---|---|
| `name` | `Name` | 原样；同时作为 server 键（见冲突规则）。Zed 侧 `name` = settings 里的 context server 键（§2.5 Z3），属用户可控字符串，需按 §4.5 去重与清洗 |
| （无判别式） | `Type` | stdio → `"stdio"` |
| `command` | `Command` | 原样（规范要求绝对路径，但不强制校验） |
| `args` | `Args` | 原样 |
| `env: [{name,value}]` | `Env map[string]string` | 列表转 map；重复 name 后者覆盖并记日志 |
| `type:"http"` | `Type` | → `"streamable"`（运行时对 `http`/`streamable*` 均识别，见 `config/types.go:189-200`） |
| `type:"sse"` | `Type` | → `"sse"`。Zed 不会产生该变体（§2.5 Z4），其它客户端可能；仍须支持或显式跳过 |
| `url` | `URL` | 原样 |
| `headers: [{name,value}]` | **无对应字段** | 见 §5-D3。Zed 只带 `url` + `headers`，`timeout`/`oauth` 不下发（§2.5 Z5）→ OAuth 保护的 server 到这里必然缺凭据，须诊断而非静默失败 |
| — | `Enabled` | 置 `true`（客户端下发即意图启用） |
| — | `TrustLevel` | 见 §4.9；stdio 建议 `local`，远程建议 `untrusted_remote`（与 `ResolvedTrustLevel()` 默认一致） |
| — | `Timeout` | 不设，走全局 `connectTimeout` |
| （协议**无** `cwd` 字段） | `WorkingDir` | 取**会话 cwd**（`session/new.cwd`）；协议不提供 server 级 cwd（`.tmp/acp_agent.rs:2884-2892` 仅 name/command/args/env） |

映射必须是**纯函数**（输入 `McpServer` → 输出 `MCPConfig` + error/warning），便于单测覆盖
全部形状分支，不依赖进程状态。

### 4.5 会话级作用域与冲突规则

现状是进程级单例（`MCPManagerInstance`），为交互式 chat 设计。ACP 需要「会话私有 + 全局共享」
两种来源共存。候选方案：

| 方案 | 做法 | 优点 | 缺点 |
|---|---|---|---|
| A. 每会话一个 manager | 每会话 `manager.NewManager()` 独立实例 | 隔离天然干净，回收=Stop | 每会话重复建连本地配置链 server；工具名冲突跨会话不可见；内存/进程放大 |
| B. 单 manager + 会话标签 | 扩展 manager：server 记录归属会话；工具面按会话过滤 | 本地配置只连一次；回收=移除会话标签 | 需新增 API 与过滤逻辑；并发改造面较大 |
| C. 双 manager | 全局 manager（本地配置链）+ 会话 manager（客户端下发） | 改造局部化，冲突规则显式 | 两套工具面需合并去重；命名冲突规则要写死 |

**建议：C（双 manager）**，理由：客户端下发的 server 与本地配置链在**生命周期与信任级别上
本质不同**（前者随会话生灭、信任来自协议对端；后者是用户本地配置、受 folder trust 门控），
用两个实例把差异显式化，比在单 manager 里加标签更容易验证与回滚。

冲突规则（需在 §5-D4 裁定后固化）：

1. 客户端下发与本地配置**同名**：默认**客户端优先**（会话级覆盖全局），并在日志与
   （可选）`session_info` 中记录覆盖事件。
2. 工具名冲突：沿用 `internal/tools/manager.go` 既有规则（MCP 优先于 toolkit，
   `ls/glob/grep/view` 保持本地）；MCP 内部冲突用规范名 `mcp__<server>__<tool>` 区分。
3. 同名 server 在一次请求内重复出现：保留首个，其余跳过并记日志。

### 4.6 生命周期与回收

| 事件 | 动作 |
|---|---|
| `session/new` | 解析 + 映射 + 建连（异步，不阻塞返回）；失败仅记录 |
| `session/load` | 同 `session/new`（schema 中同样 required）；已存在会话则复用，避免重复建连 |
| `session/resume` | 字段可选：下发则（重）连，不下发则保持既有连接 |
| `session/close` | 停止该会话私有 manager；全局 manager 不受影响 |
| `session/delete` | 先 close 语义，再删存储（与既有顺序一致） |
| 进程退出 | 统一 Stop（复用既有 `StopMCPManager` 路径 + 会话私有实例的 Stop） |

**协议依据（ACP v1 schema 原文，`.tmp/acp-schema.json`）**：`session/close` 是**唯一**能承载
MCP 回收的协议钩子——`CloseSessionRequest` 的描述是「the agent **must** cancel any ongoing
work related to the session (treat it as if `session/cancel` was called) and then free up any
resources associated with the session」。协议**没有**「关闭/停止某个 MCP server」的方法，也没有
MCP 连接状态通知：`mcpServers` 只在 `session/new`（必填）、`session/load`（必填）与
`session/resume`（可选）各下发一次，语义仅「servers the agent should connect to for this
session」。因此回收时机**完全由 agent 自主决定**；客户端能表达「不再需要」的手段只有
`session/close` / `session/delete` / 关掉 agent 进程。`session/cancel` 只取消在途 prompt，
**不是**回收信号（我们据此保持 MCP 连接不动）。

Windows 注意：stdio server 是**子进程树**，Stop 时必须确保子进程被回收（现有 manager 的
`Stop()` 已有该职责，会话私有实例需复用同一实现而非另写一套）。

三个入口的差异只在**触发时机**，解析 → 映射 → 建连 → 去重必须是**同一个函数**：
Zed 已核实三处都会下发（§2.5 Z2），若只接 `session/new`，`load`/`resume` 会走出第二套行为。
另外 `session/load` / `resume` 的响应被 Zed 阻塞等待（`zed_agent_servers_acp.rs:1766-1780`、
`:1810-1826`），因此建连**不得同步阻塞**在响应路径上，否则会放大恢复会话的延迟
（配合 §4.7 的 R1 + R3）。

兜底回收（G10 / §2.5 Z10）：agent 进程被客户端强杀时不会走 `session/close`，
需在进程启动阶段就建立「父死子亡」保障（Windows 用 Job Object，或退化为启动时记录
PID 列表 + 退出钩子清理），并在 §7.4 保留一条实测项。

### 4.7 工具面注册与刷新

现状：引导期一次性注册（`chat_setup.go:415-418`），MCP 目录变化的失效通道
（`chat_mcp_surface_invalidation.go:24-47`）只覆盖 web 的 `LocalRuntimeHost.SessionHub`。

设计选项：

| 选项 | 说明 | 评价 |
|---|---|---|
| R1. 连接就绪后增量注册 | 会话私有 manager 建连完成回调里，把新工具注册进 `FunctionCatalog` | 与既有 MCP 生命周期事件机制一致（`mcp.tools.loaded` 等），改动最小 |
| R2. 每轮动态求值工具面 | turn 开始时重新 `ListTools()` | 语义最干净，但改动 turn 路径，影响面大 |
| R3. 首轮前等待就绪 | `session/new` 后、首个 `session/prompt` 前 `WaitReady(ctx)` 带超时 | 消除「首轮缺工具」，但会把连接延迟转嫁到首轮 prompt |

**建议：R1 + R3 组合**——会话私有 manager 用 R1 增量注册；首个 prompt 前做一次带短超时的
`WaitReady`（例如 2s），超时即放行（不阻塞用户）。R2 作为长期方向记录，不在本期做。

**实施修正（第三轮实测，2026-09-20）**：R1 + R3 已落地，但实测证明**它们不是充分条件**——
真正的闸门在 `buildLocalChatToolPolicy`（`chat_actor_host.go`）：agent 构建期把
`toolSurface.ListTools()` 快照成合成 allowlist（`AllowlistEnabled=true`），bootstrap 之后
才握手的 MCP 工具会被 `AllowToolInfo` 直接拒绝，后果有两条：① 不进模型工具面
（`CollectToolCatalogDefinitions` 过滤掉）；② `shouldRefreshStableToolSurface` 的
「实时目录新增能力」判断读同一策略，会话冻结工具面**永不重建**——两个断点其实是同一道闸门。
修复：`chat_tool_policy_sync.go` 的 `syncLocalChatToolPolicyAllowlist`（由
`localChatPrepareRunHook` 每轮调用）把 live 工具面（MCP + broker + `spawn_subagents`）
增量并入**合成**策略；显式 profile 策略 / overlay `AllowTools` 收窄 / `DisableTools` /
显式 `DeniedTools` 一律不扩权。验证见 §13.3。

### 4.8 能力位与协商

- **stdio 生效后**，`mcpCapabilities` 的语义边界要重新表述：该字段只描述 http/sse
  传输能力，stdio 是强制项、不体现在能力位。因此 P1 落地后 `{false,false}` 仍然**不需要**
  改成 true —— 但文档必须写清「stdio 已生效」，否则会出现「能力位全 false ⇒ 不支持 MCP」
  的误读（正是本轮调查踩到的口径问题）。
- **http/sse 落地后**（P2）才把对应位改 `true`。能力位一旦置 true，客户端会开始下发
  该传输的 server，**置位即承诺**，不得提前。
- **能力位不设防（新增，§2.5 Z6）**：Zed 不读 `mcpCapabilities`，即使我们声明
  `{http:false}`，只要用户在 Zed 里配了 HTTP context server，条目照发。因此能力位的
  真实作用是「对合规客户端的承诺」，**不是**输入过滤器；对未声明传输的条目必须
  「跳过 + 可见诊断」（§4.10），不能依赖能力位把条目挡在门外。
- 可选增强：若未来支持「仅用客户端下发的 server、忽略本地配置」这类模式，应通过
  **配置项/命令行开关**表达，不通过能力位。

### 4.9 安全模型（本议题的核心风险）

**风险**：客户端下发的 stdio server 等价于「协议对端指定一条命令，由本进程执行」。
第二轮核实后，这个风险的**可达性比第一轮假设更高**：

- Zed 在 `session/new` / `load` / `resume` **都**下发该字段（§2.5 Z2），不是只在新会话时；
- 下发内容来自 Zed 的 `context_servers`，其来源包含**项目级 `.zed/settings.json`**
  （§2.5 Z11）与**已安装扩展**（`ContextServerConfiguration::Extension`）；
- Zed 对 agent 的 `mcpCapabilities` **不做任何过滤**（§2.5 Z6），我们无法用能力位拒收。

即：**「clone 一个仓库 → 在 Zed 打开 → agent 执行仓库里写的命令」是一条真实可达路径**，
而它完全绕开我们的 folder trust（后者只覆盖 `.aicli/mcp.yaml`）。

**补充（第二轮后半，外部核实）**：Zed 侧并非毫无防线——未信任 worktree 处于
**Restricted Mode**，官方文档明确其会阻止「MCP servers from being installed and spawned」
并要求用户确认。但结论不变，原因有三：

(a) 该机制只覆盖**项目级**来源，**全局（用户级）server 不受门控**——原文
*"Global MCP servers … are installed and started as usual, independent of worktree trust"*；
(b) 它与 `mcpServers` **转发列表**的关系未确认（源码路径上 `configured_server_ids()`
只按 `enabled` 过滤，见 Z12）；
(c) 其它客户端可能完全没有该机制，我们的实现不能依赖某一个客户端的信任模型。

因此门控必须在我们这一侧，客户端的信任模型只能算纵深防御。

现有安全资产与它们的覆盖边界：

| 资产 | 覆盖范围 | 对本议题是否够用 |
|---|---|---|
| `internal/foldertrust` | 项目级**配置文件**（`.aicli/mcp.yaml`）在未授权时不加载 | ✗ 不覆盖协议输入 |
| `MCPConfig.TrustLevel` | 标记来源可信度（stdio→local，远程→untrusted） | △ 只做标记，不阻断 |
| 工具权限策略 / `permission-mode` / ACP `session/request_permission` | 工具**调用**前的审批 | △ 拦的是调用，不是**进程启动** |
| 沙箱（`runtimeexecutor.Sandbox`） | 工具执行的路径/命令约束 | △ 不约束 MCP 子进程 |

设计约束（建议）：

1. **启动即受控**：客户端下发的 stdio server 在**启动前**过一道策略判定，
   与 folder trust 复用同一套判定函数（未信任工作区 → 拒绝启动 + 记录原因）。
2. **可关闭**：提供开关（如 `--acp-mcp=off|client|local|merge`，默认 `merge`），
   允许纯本地模式（只连配置链）与完全关闭。
3. **可审计**：每次因客户端下发而启动的进程，必须留下一条可检索日志
   （server 名、命令、来源=client、所属会话）。
4. **不静默降级**：被策略拒绝的 server 要能让用户看见（日志 + 可选 `session_info`），
   避免「配了但没生效」的静默失败。
5. **不引入新的凭据通道**：客户端下发的 `headers` 可能含 token，日志中必须脱敏。
6. **来源可见（新增）**：被拒绝/跳过的 server 必须让用户看见**来源**（client + 会话 + server 名 + 原因），
   否则 §2.5 Z11 这条路径会变成静默执行；`session_info` 的 `_meta` 与日志至少覆盖其一。

### 4.10 错误语义与降级

| 场景 | 行为 |
|---|---|
| 单条 `mcpServers` 形状非法 | 跳过该条，记日志，其余继续（§2.3） |
| server 启动失败 / 连接超时 | 不阻塞 `session/new`；状态标记为失败；工具面不含其工具 |
| 全部 server 失败 | 同上，会话照常可用（工具面退回 toolkit + 本地配置链） |
| 本地配置链 MCP 初始化失败 | 保持既有行为（`chat_setup.go:409-411` 打 warning 继续） |
| 会话私有 manager Stop 失败 | 记录日志，不阻断 `session/close` 的幂等语义 |
| 客户端下发**未声明支持**的传输（如能力位 `http:false` 时收到 http） | 跳过该条 + **可见诊断**（含 server 名与原因）；会话照常可用（§2.5 Z6） |
| 客户端下发的远程 server 缺凭据（Zed 丢弃 `oauth`，§2.5 Z5） | 连接失败按普通失败处理，但诊断文案要指出「凭据未随协议下发」，避免被误判为网络问题 |

### 4.11 观测与诊断

- 会话私有 server 的连通状态需要可见：建议在既有 debug/状态输出中增加一节
  （来源=client/local、server 名、状态、工具数），复用 `MCPStatus` 结构。
- 生命周期事件：复用 manager 的 `AddLifecycleObserver`（`mcp.connected` /
  `mcp.tools.loaded` 等），避免另造事件通道。
- 建议补一条「会话级 MCP 摘要」进 `session_info` 的 `_meta`（可选，避免污染协议字段）。

---

## 5. 关键决策（第二轮定稿：依据 Zed 现实行为）

> 本节仿照上游文档 §11.4 的 D 系列：结论一旦变更，先改本节再动代码。
> 与第一轮「建议」相比的实质变化用 **Δ** 标出，依据均指向 §2.5 的已核实事实。

- **D1 — 客户端下发的 stdio server 默认是否自动启动？**
  **定稿：默认连（`merge`），但启动前必须过门控**——(1) 工作区未受信任 → 拒绝启动 +
  可见诊断；(2) `--acp-mcp=off|client|local|merge`（默认 `merge`）；(3) 每次因客户端下发
  而启动的进程留审计日志（server 名、命令、来源、会话）。
  **Δ 依据**：Z1/Z13 证明下发来源是 Zed 的 `context_servers`（用户配置 + 已安装扩展），
  默认连符合用户意图；但 **Z11 证明下发集合包含仓库级 `.zed/settings.json` 这一来源**，
  而 Zed 的 Restricted Mode 只覆盖项目级、全局 server 不受门控、且该机制与转发列表的关系
  未确认——所以门控**不能外包给客户端**，不是可选加固。
  **协议限制（新增）**：`mcpServers` 条目**不携带来源信息**（`name` 只是 settings 的键），
  agent 侧无法区分「用户全局配置」与「仓库级配置」——分级信任做不到，只能整体门控。
  重评触发：若 Zed 侧补上来源标注、或对项目级条目增加确认，可放宽为「按来源分级」。

- **D2 — 会话作用域实现方式？** **定稿：§4.5 方案 C（双 manager）**。
  理由不变（客户端下发与本地配置链在生命周期、信任级别上本质不同）；Z2 进一步要求
  三个入口共用同一路径，双 manager 让「会话私有」只有单一实例来源，更易验证与回滚。
  重评触发：若工具面合并被证明过于复杂（同名工具大量冲突），退回方案 B。

- **D3 — `headers` 如何映射？**
  **定稿：P1 塞 `Env`（零 transport 改动，复用 `buildHeadersFromEnv`），
  P2 增加 `MCPConfig.Headers` 并保持向后兼容（Headers 优先、Env 兜底）**。
  **Δ 依据**：Z4 表明 Zed 当前**不产生 SSE**，Z5 表明 HTTP 条目只带 `url` + `headers`，
  故 P1 阶段真正要处理的远程形状只有「http + headers」一种，`Env` 兜底足够；P2 再落结构化字段。
  注意语义代价：远程传输下 `Env` 会被整体当作 HTTP 头，配置里不可混入真正的环境变量。

- **D4 — 同名冲突规则？** **定稿：server 同名 → 客户端优先；工具名冲突 → 沿用
  `tools.Manager` 既有规则 + 规范名 `mcp__<server>__<tool>` 去重**（§4.5）。
  **Δ 依据**：Z3 表明 `name` 是用户在 Zed settings 里写的键，与本地 `mcp.yaml` 的 server
  重名属常态而非边缘情况，冲突规则必须有单测固化。

- **D5 — 是否在首个 prompt 前等待 MCP 就绪？** **定稿：带 2s 短超时 `WaitReady`，超时放行**。
  **Δ 依据**：Zed 阻塞等待 `session/new` / `load` / `resume` 的响应
  （`zed_agent_servers_acp.rs:1641`、`:1776`、`:1821`），建连不能同步阻塞在响应路径上；
  把等待挪到首个 prompt 之前，是唯一不放大 Zed 侧延迟的位置。

- **D6 — `additionalDirectories` 是否同期处理？** **定稿：不**（理由同第一轮：与 A5
  `os.Chdir` 进程级副作用同源，属独立议题，避免范围蔓延）。
  **Δ 依据**：Z8 表明该字段由 `sessionCapabilities.additionalDirectories` 门控，我们未声明
  → Zed 恒发 `[]`，因此「不做」在 Zed 下**没有任何行为差异**；将来要做时开关就在能力位上。

- **D7 — 能力位口径？** **定稿：stdio 生效不改变能力位（仍 `{false,false}`），
  仅在 P2 支持 http/sse 后置 `true`**；文档必须显式写明「能力位不含 stdio」。
  **Δ 依据（重要）**：Z6 表明能力位**挡不住**客户端的 http 条目（Zed 不读它），
  因此 P1 必须实现「未声明传输 → 跳过 + 可见诊断」，不能把能力位当输入过滤器。

---

## 6. 实施计划

### 6.0 分期总览

| 期 | 目标 | 可独立交付 | 依赖 | 成本 |
|---|---|---|---|---|
| **P0** | 证据收口：把「现状能跑通」与「客户端下发不生效」变成自动化断言；实测 G6 | ✅ 是（不改运行行为） | 无 | S–M |
| **P1** | stdio 最小可用：解析 → 映射 → 会话级连接 → 工具面 → 回收 → 安全门控 | ✅ 是（http/sse 未支持，能力位不变） | P0 | M–L |
| **P2** | http/sse：`Headers` 字段 + 远程传输合并 + 能力位置 `true` | ✅ 是 | P1 | M |
| **P3** | 收口：观测、工具面刷新、多会话并发、Zed 实机验证、文档终稿 | 部分 | P1（P2 可选） | M |

成本口径：S ≈ 0.5 人日，M ≈ 1–2 人日，L ≈ 3–5 人日（含测试与文档）。

### 6.1 P0 — 证据收口（不改行为）

**为什么先做**：§3.4 那条「ACP 会话已能连本地配置链 MCP」的链路目前**零测试覆盖**（G7）。
P1 会改动同一条引导路径，没有基线断言就无法区分「我改坏了」与「本来就不工作」。

| 交付物 | 内容 |
|---|---|
| `backend/scripts/acp_e2e_mcp_local.go`（新增） | 正向：隔离 `USERPROFILE` + 写入 `~/.aicli/mcp.yaml` 指向 echo MCP server → 启动真实 `aicli agent stdio` → `session/new` + `session/prompt` → 断言 MCP 工具在工具面内且可调用 |
| `backend/scripts/acp_e2e_mcp_client_supplied_negative.go`（新增） | 负向：`session/new` 下发一个「启动即写标记文件」的 stdio server → 断言**标记文件不存在**（锁定当前「不启动客户端下发 server」的行为） |
| `docs/acp/README.md:379-383` | FAQ 口径修正为 §1 的三层表述（现状写法易被读成「完全不支持 MCP」） |
| 实测记录 | G6：异步建连（`StartAsync`）晚到的工具在 ACP 首个 prompt 时是否可见 → 结论写入本文档 §7.4 |

**验收标准**

1. 正向脚本在本地可重复通过；负向脚本稳定通过（当前状态下应始终通过）。
2. 两个脚本默认走 LIVE 门控（对齐 `manager_live_test.go` 的 `LIVE_MCP_TEST=1` 约定），
   避免在无网络/无 ws 依赖的环境里假失败。
3. §7.4 的 G6 结论从「推断」升级为「已核实（含命令与输出摘要）」，或明确记为「无法稳定复现」。

**风险**：echo MCP server 走 WebSocket（`cmd/echo-mcp-server/main.go`），E2E 需先拉起该进程；
脚本内需自管生命周期（起 → 等就绪 → 跑 → 停）。

### 6.2 P1 — stdio 最小可用

**改动清单**

| 文件 | 改动 | 类型 |
|---|---|---|
| `internal/acp/mcp.go` | `McpServer` / `EnvVariable` / `HTTPHeader` 类型；`DecodeMCPServers(json.RawMessage)`（逐条容错解析） | 新增 |
| `internal/acp/mcp_test.go` | 解析用例矩阵（见 §7.1） | 新增 |
| `cmd/aicli/commands/agent_stdio_mcp.go` | `toMCPConfig(McpServer) (config.MCPConfig, error)` 纯函数；会话级 manager 装配与回收 | 新增 |
| `cmd/aicli/commands/agent_stdio_mcp_test.go` | 映射与冲突规则单测 | 新增 |
| `cmd/aicli/commands/agent_stdio.go` | `NewSession`(:217) / `LoadSession`(:408) / `ResumeSession`(:488) / `CloseSession` 接入；移除 :695 的「ignored」注释 | 改动 |
| `cmd/aicli/commands/chat_setup.go` | 工具面装配处合并「全局 manager + 会话 manager」两套工具；保持 `:394-398` 的 runtime-server 提前返回语义 | 改动 |
| `cmd/aicli/commands/mcp_integration.go` | 抽出可复用的「按给定 `*config.Config` 建 manager 并注册工具」函数，供全局/会话两条路径共用（不复制实现） | 重构 |
| `cmd/aicli/commands/agent.go` | `--acp-mcp=off\|client\|local\|merge` 开关（默认 `merge`）；更新 :80 的 help 文案 | 改动 |
| `internal/mcp/manager/manager.go` | 按需新增会话级入口（如 `NewScopedManager()` 或允许 `LoadConfig` 接受内存快照），避免为会话写临时文件 | 改动 |
| `internal/foldertrust`（复用） | 会话级 stdio 启动前的信任判定（复用既有判定函数，不新增一套） | 复用 |
| `docs/acp/README.md` | 新增「MCP 集成」章节：能力边界、开关、安全模型、示例 | 文档 |

**验收标准（逐条可判定）**

1. `session/new` 下发 stdio server → 该 server 的工具在**首个 prompt** 的工具面内（D5 的
   短超时 `WaitReady` 生效）；无超时阻塞。
2. 未信任工作区下发 stdio server → **不启动进程**，日志含拒绝原因与 server 名，
   `session/new` 仍正常返回 `sessionId`。
3. 单条形状非法 / 未知 `type` → 跳过该条，其余正常，`session/new` 不报错（§2.3）。
4. `session/close` 后：会话私有子进程被回收（Windows 下用任务列表校验无残留）；
   全局 manager 的 server 不受影响。
5. 本地配置链行为不变：P0 正向脚本仍通过。
6. `--acp-mcp=off` 时，客户端下发的 server 一律不启动；`local` 时只连本地配置链。
7. 同名冲突按 §5-D4 规则生效，且有日志可查。
8. **未声明传输的条目**：能力位仍为 `{http:false,sse:false}` 时收到 http 条目
   （Zed 的真实行为，§2.5 Z6）→ 跳过该条 + 诊断可见（含 server 名与原因），
   `session/new` 正常返回，且**不产生任何连接尝试**（用注入的 fake launcher 断言零调用）。
9. **三入口一致**：同一个 server 分别经 `session/new` 与 `session/load` 下发时，
   解析 / 映射 / 建连 / 去重行为一致（同一函数覆盖），不存在「只有 new 生效」的分叉。
10. **强杀兜底**：模拟父进程被 `kill`（不发 `session/close`）→ 无 stdio 子进程残留
    （Windows 任务列表校验），对应 §2.5 Z10 / G10。

**测试**：§7.1 单测全绿 + P0 两个脚本 + 新增 `acp_e2e_mcp_client_stdio.go`
（正向：客户端下发 echo server → 工具可用）。

### 6.3 P2 — http/sse

| 项 | 内容 |
|---|---|
| `MCPConfig.Headers` | `internal/mcp/config/types.go` 新增字段；`streamable.go` / `websocket.go` 改为「Headers 优先、Env 兜底」 |
| 能力位 | `internal/acp/types.go:1424-1427` 置 `HTTP:true` / `SSE:true`（**置位即承诺**，须在实现与测试齐备后同一提交内完成） |
| 映射 | `toMCPConfig` 的 http/sse 分支：`headers` → `Headers`（P2 起不再塞 `Env`） |
| 测试 | 远程传输头注入单测（含脱敏断言）；`sse` 变体的端到端（可用本地 mock SSE server） |
| 成本 | M |

**注意**：`type:"http"` 映射到运行时的 `"streamable"` 需要在文档与日志中显式说明
（ACP 叫 http，运行时叫 streamable，见 `internal/mcp/config/types.go:189-200` 的类型归一化）。

**优先级说明（第二轮）**：Z6 表明 Zed 会照发 http 条目，因此 P2 的价值不是「合规」而是
「让用户在 Zed 里配的 HTTP context server 真的能用」。P1 阶段必须用**可见诊断**兜住这段空窗，
否则用户观感是「配了却没生效」（G9）。

### 6.4 P3 — 收口

1. **观测**：会话级 MCP 状态（来源、server 名、状态、工具数）接入既有 debug/状态输出，
   复用 `MCPStatus`；生命周期事件复用 `AddLifecycleObserver`。
2. **工具面刷新**：把 §4.7 的 R1 增量注册补到会话私有 manager 的全部生命周期事件上，
   并评估是否把失效通道扩展到非 web 宿主（现仅 `chatWebSession()`，`chat_mcp_surface_invalidation.go:40-47`）。
   **（已实施）**：新增 `invalidateACPSessionToolSurface`（ACP 会话独立失效入口）+
   `chat_tool_policy_sync.go` 的合成策略同步；见 §4.7 实施修正。
3. **并发压测**：多 ACP 会话并发 + 同名 server 交叉下发，验证隔离与回收无泄漏。
   **（已实施）**：`scripts/acp_e2e_mcp_multi_session.go`——4 会话并发 `session/new` 与并发 `prompt`，
   逐会话断言「只见自己的工具」（`foreign_tools=0`）、单会话关闭只回收自己的子进程、
   全部关闭后无孤儿进程；见 §13.3。
4. **Zed 实机验证**：见 §7.3。
5. **文档终稿**：本文档状态改为「已实施」，`docs/acp/README.md` 与上游
   `acp-v1-capability-gap-implementation-plan-20260919.md` 的 §11.1 A4/A8 同步更新
   （A4 由 PARTIAL 转 DONE，A8 由 NO 转 DONE）。

---

## 7. 验证策略

### 7.1 单元测试矩阵

| 被测单元 | 用例 |
|---|---|
| `DecodeMCPServers` | stdio 最小形状；stdio 带 `env`（含重复 name）；`type:"http"`；`type:"sse"`；未知 `type` → 跳过；`type:"http"` 缺 `url` → 跳过；无 `command` 且无 `type` → 跳过；顶层非数组；空数组；**单项非法但其余合法 → 只跳过非法项**；`env`/`headers` 传成 map（非数组）→ 该条跳过不 panic |
| `toMCPConfig` | 字段逐项映射；`env` 列表 → map；`headers` → `Env`（P1）/ `Headers`（P2）；`Enabled=true`；`TrustLevel` 取值；ACP `http` → 运行时 `streamable` |
| 冲突规则 | 同名 server 客户端优先；一次请求内重复 name 保留首个；工具名冲突走规范名 `mcp__<server>__<tool>` |
| 安全门控 | 未信任工作区 → 拒绝且不调用启动路径（用注入的 fake launcher 断言零调用） |
| 开关 | `--acp-mcp=off/local/client/merge` 四态下的装配决策表 |

约定：解析与映射均为纯函数，单测**不得**依赖真实进程、网络或用户目录。

### 7.2 端到端脚本矩阵

复用既有 `backend/scripts/acp_e2e_*.go` 模式（隔离 `USERPROFILE` + 本地 mock provider +
真实 `aicli` 二进制 + 断言上游请求体）：

| 脚本 | 断言 | 引入期 |
|---|---|---|
| `acp_e2e_mcp_local.go` | 本地配置链 MCP 在 ACP 会话可用（基线） | P0 |
| `acp_e2e_mcp_client_supplied_negative.go` | 客户端下发 stdio **不**被启动（负向基线） | P0 |
| `acp_e2e_mcp_client_stdio.go` | 客户端下发 stdio 被连接、工具可用、`session/close` 后子进程回收 | P1 |
| `acp_e2e_mcp_reject_untrusted.go` | 未信任工作区拒绝启动 + 会话仍成功 | P1 |
| `acp_e2e_mcp_remote.go` | http/sse 连接与头注入 | P2 |
| `acp_e2e_mcp_session_load.go` | `session/load` 下发 stdio server 与 `session/new` 行为一致（首轮可见 + 可调用 + 回收） | P3 |
| `acp_e2e_mcp_parent_kill.go` | agent 被硬杀（`taskkill /F /PID`，不带 `/T`）后 stdio MCP 子进程被 Job Object 回收 | P3 |
| `acp_e2e_mcp_multi_session.go` | 4 会话并发：工具面严格隔离、生命周期按会话回收、无孤儿 | P3 |

### 7.3 实机验证（Zed）

1. 在 Zed 的 MCP 设置中配置一个 stdio server（指向本地 echo server）。
2. 打开本项目工作区，创建 ACP 会话。
3. 校验点：日志中出现「收到客户端下发 server」；工具面含其工具；调用成功。
4. 报文回填：Zed 侧「下发什么」已由源码核实（§2.5 Z1–Z7），实机只需确认
   **配置了 context server 的 Zed 会话确实把条目送到我们这里**——在 `session/new`
   处理处临时插桩打印 server 名与 transport（或抓一次 ACP 报文），把真实样本回填 §10.6。
   注意项目级 `.zed/settings.json` 也参与合并（Z11），样本应覆盖「用户级配置」与
   「项目级配置」两种来源。

#### 7.3.1 实机报文回填（2026-09-20，已获得）

Zed 初始化会话时实际下发的 `session/load` 报文（用户从实机抓取，原样摘录）：

```json
{
  "mcpServers": [
    {
      "name": "mcp-server-context7",
      "command": "C:\\Program Files\\nodejs\\node.exe",
      "args": ["C:/Users/vince/AppData/Local/Zed/extensions/work/mcp-server-context7/node_modules/@upstash/context7-mcp/dist/index.js"],
      "env": []
    }
  ],
  "cwd": "E:\\projects\\ai\\ai-agent-runtime",
  "sessionId": "session_20260920123337_5Xjh3A0e"
}
```

形状特征与逐条核对结论（每条都有测试固化，见 §13.3）：

| 特征 | 结论 | 落点 |
|---|---|---|
| 条目**无 `type` 字段** | 按**隐式 stdio** 识别（`TransportKind()` 空值 → stdio），不依赖显式标签 | `internal/acp/mcp_types.go` `decodeMCPServerEntry` 的 `case "", MCPTransportStdio`；单测 `TestDecodeMCPServersZedLoadSampleShape` |
| `env` 是**空数组** `[]`（既不是 map 也不是 `[{name,value}]`） | **正常条目**，不触发「非法条目跳过」；映射为「无环境变量」（`Env=nil`） | `decodeKeyValueList`（`trimmed[0]=='['` → 直接返回列表）+ `keyValueMap`（空 → nil）；单测 `TestZedLoadSampleShapeIsRecognized` |
| 走 **`session/load`**（不是 `session/new`） | 与 `session/new` **共用同一装配路径**：`LoadSession` → `withACPMCPClientPlan` → `bootstrapSessionWithIDLocked` → `chatOpts.ACPMCPClientPlan` → `buildACPSessionMCP` | `cmd/aicli/commands/agent_stdio.go:460` / `:510` / `:632`、`chat_setup.go:428`；E2E `acp_e2e_mcp_session_load.go`（**已改为 Zed 同形状下发**：去掉 `type`、加 `env: []`） |
| `sessionId` 形如 `session_<ts>_<rand>` | 正是 aicli 自己铸造的 id 格式（`internal/chat/session.go:1082`），durable store 能按该 id 命中，不需要额外映射 | 同上 E2E 的 `session/new → close → load` 路径 |
| `command` 是 Windows 绝对路径、`args` 用正斜杠 | 原样保留，不做路径归一化；stdio 建连直接使用 | `acpMCPServerToConfig`（`WorkingDir` 取会话 `cwd`）；单测同上 |

实测（报文形状与上表完全一致：无 `type`、`env: []`）：

```
OK session/load assembled the client-supplied server (pid=19876 marker=present(started))
OK prompt #1 upstream_tools=46 load_tool_visible=true callable=true
OK session/load server pid=19876 reaped after session/close (tasklist/proc probe)
E2E PASS acp_e2e_mcp_session_load
```

#### 7.3.2 实机实证（2026-09-20，本机正在运行的 Zed 会话）

上表的协议级结论已由**生产进程树 + durable 记录**独立确认，不再依赖人工 GUI 步骤：

| 观察 | 证据 |
|---|---|
| Zed 以 ACP 模式启动本 agent | `Zed.exe`(32628) → `aicli-5x.exe acp`(35408)，启动于 12:56:48 |
| **我方 agent 真的拉起了报文里的 context7 server** | `node.exe`(35508) 的 `ParentProcessId = 35408`，命令行即报文中的 `@upstash/context7-mcp/dist/index.js` |
| 对照：另一个 context7 进程不属于我们 | `node.exe`(32492) 的父进程是 Zed 侧进程（25880），非本 agent |
| `session/load` 能命中 durable 记录 | `backend/data/runtime/session_runtime.sqlite` 中 `session_20260920123337_5Xjh3A0e` **存在**（`status=idle`，171 条事件） |

注：该 store 由 `backend/configs/runtime.yaml` 的
`sessionRuntime.storePath: ../data/runtime/session_runtime.sqlite` 决定；
`~/.aicli/sessions/runtime/session_runtime.sqlite` 是**另一个**库（CLI 侧），排查时勿混淆。

**仍未关闭**：Zed GUI 内**实际调用**该工具（需在 agent 面板发一次会触发 context7 的提问）
与 §7.4 第 2 条（Restricted Mode 是否过滤转发列表）——这两项需要一次人工 GUI 会话，
协议级与进程级证据均不能替代。

### 7.4 待实测清单（不得当作已核实结论引用）

| # | 待确认项 | 现状 | 关闭时点 |
|---|---|---|---|
| 1 | G6：`StartAsync` 异步建连晚到的 MCP 工具，ACP 首个 prompt 时是否可见 | **已核实（2026-09-20）**：`acp_e2e_mcp_local.go` 与 `acp_e2e_mcp_client_stdio.go` 均实测 `first_visible_prompt=1`、`upstream_tools=46`、工具 visible + callable（§13.3） | 已关闭 |
| 2 | Zed 的 **Restricted Mode** 是否也拦截**转发列表**（未信任 worktree 的项目级 server 是否仍出现在 `mcpServers` 中） | **未核实**（源码路径无信任过滤；文档只说阻止「安装与启动」，Z11） | P0/P3 |
| 3 | Windows 下 stdio MCP 子进程树是否被 `Stop()` 完整回收 | **已核实（2026-09-20）**：`acp_e2e_mcp_client_stdio.go` 在 `session/close` 后用 tasklist/proc 探针确认子进程已回收（§13.3） | 已关闭 |
| 4 | 同一 server 经 `session/new` 与 `session/load` 下发时**我方行为**是否一致 | **已核实（2026-09-20）**：`acp_e2e_mcp_session_load.go` 实测 `session/load` 走同一装配路径——server 真实启动、`prompt #1` 工具 visible + callable、`session/close` 后回收（§13.3） | 已关闭 |
| 5 | agent 进程被客户端强杀（Zed `Drop` → `child.kill()`）时 stdio MCP 子进程是否残留 | **已核实（2026-09-20，Windows）**：`acp_e2e_mcp_parent_kill.go` 用 `taskkill /F /PID`（不带 `/T`）硬杀 agent，MCP 子进程 600–800ms 内被 Job Object（`KILL_ON_JOB_CLOSE`）回收；**Unix 未关闭**：进程组只在优雅路径生效，孤儿属已知平台限制（脚本以 DIAG 记录，不误报为失败） | Windows 已关闭 / Unix 开放 |
| 6 | ~~Zed 是否按 `enabled` 筛选下发集合~~ → **已核实：是**（`zed_context_server_store.rs:377-384`，即上游 PR #43467 的修复） | **已关闭**（2026-09-20） | — |

---

## 8. 风险与缓解

| # | 风险 | 等级 | 缓解措施 | 关闭时点 |
|---|---|---|---|---|
| R1 | **协议输入 → 任意命令执行**：客户端下发的 stdio server 由本进程拉起；且**来源含仓库级 `.zed/settings.json`**（Z11），协议不提供来源信息 | 高 | D1：启动前过 folder trust 判定；`--acp-mcp` 开关；每次启动留审计日志（来源=client、会话、命令）；拒绝/跳过必须可见（§4.9 约束 6） | P1 |
| R2 | 会话隔离失效 / 资源泄漏（进程、连接、工具面残留） | 中高 | 双 manager + `session/close` 回收 + P3 并发压测 | P1/P3 |
| R3 | 客户端每次 `session/new` 重复下发 → 重复建连、进程放大 | 中 | 会话内按 name 去重；同一会话复用已连实例；跨会话连接复用**本期不做**，记录为后续优化 | P1 |
| R4 | 工具名污染 / 与本地 toolkit 及本地 MCP 冲突 | 中 | 沿用 `tools.Manager` 既有规则；冲突走规范名 `mcp__<server>__<tool>`；单测固化 | P1 |
| R5 | 异步建连导致首轮缺工具（G6） | 中 | D5 短超时 `WaitReady` + R1 增量注册 | P1 |
| R6 | 能力位与实际能力不符（两个方向：过早置 `true` 承诺了没实现的能力；或以为 `false` 能挡住条目） | 中 | 置位与实现/测试同提交；同时**不把能力位当输入过滤器**——未声明传输一律「跳过 + 诊断」（Z6 / G9） | P1/P2 |
| R7 | Windows 下 stdio 子进程树回收不干净（含被客户端强杀的场景） | 中 | 复用既有 `Stop()` 实现 + 「父死子亡」兜底（G10）；待实测 #3/#5 给出结论 | P1 |
| R8 | `headers` / `env` 中的 token 泄漏进日志 | 中 | 统一脱敏函数 + 单测断言（P2 头注入测试含脱敏用例） | P2 |
| R9 | 文档口径漂移（README 的「不支持 MCP」被继续引用） | 低 | P0 同步 README；本文档作为该议题的单一事实源 | P0 |
| R10 | 与 `--runtime-mode=server` 冲突：该模式下本地 MCP 不初始化（`chat_setup.go:394-398`），客户端下发 server 的归属未定义 | 中 | P1 显式定义并测试：ACP 宿主在该模式下是否仍需连接客户端下发 server（建议：仍需，因为协议要求 agent 侧连接） | P1 |
| R11 | 三入口分叉：只接 `session/new`，`load`/`resume` 走第二套行为（Z2 已证明 Zed 三处都发） | 中 | 三入口共用同一函数（§4.6）；验收标准 9 固化 | P1 |
| R12 | 用户观感失败：条目被跳过但无任何可见反馈，表现为「在 Zed 里配了却没生效」 | 中 | §4.10 的「跳过 + 可见诊断」；P1 验收标准 8 固化 | P1 |

---

## 9. 文档同步清单

| 文件 | 需同步内容 | 时点 |
|---|---|---|
| `docs/acp/README.md:379-383` | FAQ 改为三层表述；补「本地配置链 MCP 可用」与「客户端下发不生效（截至 P0）」；P1 后补开关与安全模型章节 | P0 / P1 |
| `docs/plan/acp-v1-capability-gap-implementation-plan-20260919.md` | §11.1 A4（PARTIAL → DONE）、A8（NO → DONE）；§11.4-D2 追加「已由本文档接管」的指针 | P1 完成时 |
| 本文档状态行 | 「待实施」→「实施中 / 已实施（含提交号）」 | 每期结束时 |
| `cmd/aicli/commands/agent.go:80` | `--help` 文案（现为「MCPServers 参数暂不支持。」） | P1 |
| `cmd/aicli/commands/agent_stdio.go:695` | 移除/改写「MCPServers on load requests are ignored」注释 | P1 |

**执行状态（2026-09-20）**：全部落地——`agent_stdio.go` 的 load 注释已改写为「load 请求的
`mcpServers` 与 `session/new` 走同一装配路径」；`docs/acp/README.md` 三层口径与 MCP 集成章节已就位；
`agent.go --help` 文案已改写；上游 `acp-v1-capability-gap-implementation-plan-20260919.md`
的 §11.1 A4 / A8 与 §11.4-D2 已同步（A4、A8 转 DONE）。

---

## 10. 附录：证据索引

### 10.1 协议字段与能力位

- `backend/internal/acp/types.go:481-485` — `NewSessionRequest`（`MCPServers json.RawMessage`）
- `backend/internal/acp/types.go:704-709` — `LoadSessionRequest`
- `backend/internal/acp/types.go:725-730` — `ResumeSessionRequest`
- `backend/internal/acp/types.go:441-445` — `MCPCapabilities`
- `backend/internal/acp/types.go:1413-1437` — `DefaultAgentCapabilities()`（MCP 两位均 `false`）
- `backend/internal/acp/server.go:548-559` — `handleSessionNew` 透传
- `backend/internal/acp/server.go:211-218` — 空能力兜底逻辑

### 10.2 ACP 宿主侧（会话引导与 MCP 初始化）

- `backend/cmd/aicli/commands/agent_stdio.go:217-263` — `NewSession`（只读 `Cwd`；含 `os.Chdir`）
- `backend/cmd/aicli/commands/agent_stdio.go:408`、`:488` — `LoadSession` / `ResumeSession`
- `backend/cmd/aicli/commands/agent_stdio.go:622` — `bootstrapChatSession` 调用点
- `backend/cmd/aicli/commands/agent_stdio.go:695` — 「ignored」注释
- `backend/cmd/aicli/commands/chat_setup.go:394-398` — runtime-server 模式提前返回
- `backend/cmd/aicli/commands/chat_setup.go:408`、`:414-418` — MCP 管理器初始化与工具注册
- `backend/cmd/aicli/commands/chat_setup.go:491-527`、`:511` — `bootstrapChatSession`
- `backend/cmd/aicli/commands/mcp_integration.go:18`、`:22-71` — 进程级单例与异步初始化
- `backend/cmd/aicli/commands/mcp_integration.go:120-132` — 会话级 `MCPConfigPath` 覆盖
- `backend/cmd/aicli/commands/mcp_integration.go:134-157` — folder trust 门控与静默跳过
- `backend/cmd/aicli/commands/mcp_integration.go:159-195` — 管理器装配与工具注册
- `backend/cmd/aicli/commands/chat_mcp_surface_invalidation.go:13-36`、`:40-47` — 工具面失效通道（仅 web）
- `backend/cmd/aicli/commands/agent.go:80` — help 文案

### 10.3 运行时 MCP 层

- `backend/internal/mcp/config/types.go:19-22` — `Config.MCPServers`
- `backend/internal/mcp/config/types.go:91-108` — `MCPConfig` 字段（**无 `Headers`**）
- `backend/internal/mcp/config/types.go:124-130`、`:174-187`、`:189-200` — 启用判定、信任等级、类型归一化
- `backend/internal/mcp/manager/manager.go:37-105` — 管理器接口（**无动态加 server API**）
- `backend/internal/mcp/manager/manager.go:175`、`:188`、`:198`、`:562`、`:607`、`:617`、`:832` — `LoadConfig` / `Start` / `StartAsync` / `Stop` / `ListTools` / `CallTool` / `ReloadConfig`
- `backend/internal/mcp/transport/streamable.go:47-53`、`websocket.go:63-73` — `buildHeadersFromEnv`（Env 全量当 HTTP 头）
- `backend/internal/mcp/registry/registry.go:231-251` — 工具规范名与去重
- `backend/internal/tools/manager.go:35-140` — toolkit + MCP 合并、`shouldPreferLocalToolkit`
- `backend/internal/aiclipaths/paths.go:176-184` — `mcp.yaml` 解析优先级

### 10.4 测试与验证资产

- `backend/internal/mcp/server/echo/server.go:228-245` — echo/add 工具
- `backend/cmd/echo-mcp-server/main.go` — WebSocket 启动入口
- `backend/internal/mcp/manager/manager_live_test.go:18-80` — LIVE 门控用法参考（`LIVE_MCP_TEST=1`）
- `backend/scripts/acp_e2e_*.go` — 既有 ACP 端到端脚本（隔离 `USERPROFILE` + mock provider 模式）
- 测试覆盖（第三轮补齐）：`backend/cmd/aicli/commands/agent_stdio*_test.go` 的 MCP 用例
  （解析 / 映射 / 四态装配 / 回收）与 `chat_tool_policy_sync_test.go`（合成策略同步、不扩权表驱动）；
  原「`mcp` 零命中」缺口已关闭
- 新增 E2E：`backend/scripts/acp_e2e_mcp_local.go`、`acp_e2e_mcp_client_stdio.go`、
  `acp_e2e_mcp_client_supplied_negative.go`、`acp_e2e_mcp_reject_untrusted.go`、`acp_e2e_mcp_remote.go`；
  第三轮补：`acp_e2e_mcp_session_load.go`、`acp_e2e_mcp_parent_kill.go`、`acp_e2e_mcp_multi_session.go`；
  实测日志 `backend/e2e-local-run2.log`、`backend/e2e-client-run2.log`

### 10.5 复现调查的命令

```powershell
cd backend

# 协议字段是否有消费点（预期：仅类型声明与脚本客户端侧）
rg -n "MCPServers" --glob "*.go" .

# 能力位声明
rg -n "MCPCapabilities" --glob "*.go" .

# 管理器是否有动态加 server 能力（预期：无 AddServer/RegisterServer）
rg -n "func \(m \*Manager\)" internal/mcp/manager/manager.go

# ACP 测试是否覆盖 MCP（预期：零命中）
rg -n -i "mcp" cmd/aicli/commands/agent_stdio*_test.go
```

### 10.6 Zed 客户端侧证据（第二轮新增）

本地快照位于 `.tmp/`（不入库），2026-09-19/20 抓取；**行号指快照文件**：

- `.tmp/zed_agent_servers_acp.rs`（上游 `crates/agent_servers/src/acp.rs`）
  - `:4386-4432` — `mcp_servers_for_project()`：`context_servers` → `acp::McpServer` 映射
  - `:4404` — 远程项目门控 `if is_local || *remote`
  - `:4415-4427` — HTTP 分支（`timeout` / `oauth` 被丢弃）
  - `:1475-1499` — `SessionDirectories::into_*_session_request()`（`additionalDirectories` + `mcpServers`）
  - `:1636` / `:1766` / `:1810` — `session/new` / `load` / `resume` 三处调用点
  - `:1745-1750` — `supports_session_additional_directories()`
  - `:1557-1562` — `impl Drop for AcpConnection`（`child.kill()`）
- `.tmp/zed_context_server_store.rs`（上游 `crates/project/src/context_server_store.rs`）
  - `:160-176` — `enum ContextServerConfiguration { Custom, Extension, Http }`（**无 SSE**）
  - `:1133-1156` — `resolve_all_context_server_settings()`（`:1146` 读 `ProjectSettings`，无信任过滤）
- `.tmp/acp_agent.rs` / `.tmp/acp_client.rs` — 上游 `agent-client-protocol` crate 快照
  - `:2651-2671` — `McpServer` 枚举（Http / Sse / Acp[unstable] / Stdio）
  - `:2884-2892` — `McpServerStdio` 字段（**无 cwd**）
  - `:1117-1120` — `ForkSessionRequest` 由 `unstable_session_fork` 门控

**实机报文样本（2026-09-20，用户从 Zed 实机抓取）**：Zed 初始化会话时发的是
`session/load`，条目**无 `type` 标签**（隐式 stdio）且 **`env` 为空数组**——这两点
是源码快照看不出的编码细节，已回填 §7.3.1 并固化为单测 + E2E 形状。

**外部文档与上游 PR**（子代理核实，2026-09-20；用于交叉验证本地快照结论）：

| 来源 | 关键内容 | 支撑 |
|---|---|---|
| `zed.dev/docs/ai/external-agents`（#mcp、#configuration-boundaries 表） | *"Zed-configured MCP servers **may be forwarded** to External Agents over ACP. External Agents may also read their own native MCP configuration."*（该章节由 PR **#40658**，2025-10-20 引入） | Z13 |
| `zed.dev/docs/ai/mcp`（「Agent Path Support」表） | External Agents 行：*"Zed can forward configured MCP servers over ACP; agents may also read native MCP config"* | Z13 |
| `zed.dev/docs/trusted-worktrees` | Restricted Mode *"prevents: … MCP servers from being installed and spawned"*、未信任时 *"waits for user confirmation"*；但 *"Global MCP servers … are installed and started as usual, **independent of worktree trust**"* | Z11 |
| `agentclientprotocol.com/protocol/v1/session-setup` | *"All Agents **MUST** support connecting to MCP servers via stdio"*；*"Before using HTTP or SSE transports, Clients **MUST** verify the Agent's capabilities during initialization"* | Z14 / Z6 |
| 上游 PR **#43467**（commit `94f9b858`，2025-11-25） | *"ACP agents would start MCP servers that were disabled in Zed"* → 即 `configured_server_ids()` 的 `enabled` 过滤（Z12）；精确版本号未定位【推断 ≈ v0.213±2】 | Z12 |
| 上游 PR **#36752**（2025-08-22） | *"acp: Support calling tools provided by MCP servers"*（`McpServer::Acp` 方向，`Release Notes: - N/A`） | §2.5 前瞻项 |

> 注：上游 **`CHANGELOG.md` 已不存在（404）**，发布说明载体为 GitHub Releases；
> #36752 / #35196 / #35068 均标 `Release Notes: - N/A`，不进 changelog——**不能靠 changelog 判断某修复是否已进某个 Zed 版本**，只能看源码快照或实测。

复现（PowerShell）：

```powershell
$base = 'https://raw.githubusercontent.com/zed-industries/zed/main'
Invoke-WebRequest "$base/crates/agent_servers/src/acp.rs"        -OutFile .tmp/zed_agent_servers_acp.rs
Invoke-WebRequest "$base/crates/project/src/context_server_store.rs" -OutFile .tmp/zed_context_server_store.rs

# 关键断言：Zed 客户端侧不读 mcpCapabilities（预期：零命中）
Select-String -Path .tmp/zed_agent_servers_acp.rs,.tmp/zed_acp_thread.rs -Pattern 'mcp_capabilities|mcpCapabilities'
```

---

## 11. 方案完整性审查（第二轮）

> 审查对象：§4 方案设计 + §6 分期计划，检查是否覆盖「Zed 现实行为 / ACP 规范 / 本仓库约束」。
> 判定只有三种：**已覆盖** / **本轮补齐** / **仍缺口**。

### 11.1 覆盖矩阵

| 方向 | 覆盖情况 | 说明 |
|---|---|---|
| ACP 规范（必填性、union、容错、能力位） | 已覆盖 | §2.1–§2.4；解析层与 §4.10 错误语义与之对齐 |
| Zed 现实行为 | **本轮补齐** | §2.5 新增 Z1–Z14（本地快照 + 官方文档/上游 PR 交叉验证）；§4.4 / §4.6 / §4.8 / §4.9 / §4.10 据此修订 |
| 会话生命周期（new / load / resume / close / delete / 退出 / 强杀） | **本轮补齐** | §4.6 新增三入口同一函数、非阻塞建连、强杀兜底 |
| 安全（信任、审计、脱敏、来源可见） | **本轮补齐** | §4.9 新增约束 6；R1 重新定性 |
| 观测与诊断 | 已覆盖 | §4.11 + P3.1；新增「跳过 / 拒绝必须可见」 |
| 验证（单测 / E2E / 实机 / 待实测） | 已覆盖 | §7.1–§7.4；「Zed 下发什么」由待实测转为已核实，待实测收敛为 **5 条**（原第 6 条已关闭） |
| 运行时约束（无动态 API、无 Headers、单例、工具面快照） | 已覆盖 | §3.5 G3–G6 与 §6 改动清单 |

### 11.2 本轮补齐的缺口

| # | 缺口 | 原状 | 落点 |
|---|---|---|---|
| C1 | 三入口（new / load / resume）行为一致性 | 只列了各入口行为，未要求同一实现 | §4.6、§5-D2、§6.2 验收 9、R11 |
| C2 | 未声明传输的**可见**诊断 | 只说「跳过」 | §4.10、§5-D7、§6.2 验收 8、R12 |
| C3 | 被客户端强杀时的子进程兜底 | 只有正常 `Stop()` 路径 | §4.6、§6.2 验收 10、R7 |
| C4 | 项目级 `.zed/settings.json` 来源的安全定性 | 假设「客户端 = 用户本地 IDE 配置」 | §2.5 Z11、§4.9、§5-D1、R1 |
| C5 | Zed 不读 `mcpCapabilities` | 未提，暗含「能力位可挡输入」 | §2.5 Z6、§4.8、§5-D7 |
| C6 | 协议不提供 server 来源信息 | 未提 | §5-D1（分级信任不可行） |
| C7 | Zed 丢弃 `timeout` / `oauth` | 未提 | §2.5 Z5、§4.4、§4.10 |
| C8 | `additionalDirectories` 的能力位门控 | 只说「不做」 | §2.5 Z8、§5-D6 |
| C9 | server 进程的工作目录无定义 | §4.4 未列 | §4.4 新增 `WorkingDir` 行 |
| C10 | Zed 侧 **Restricted Mode**（未信任 worktree 阻止 MCP server 安装/启动）与**转发列表**的关系未确认 | 暗含「客户端信任模型可依赖」 | §2.5 Z11、§4.9 补充段、§7.4-2 |

### 11.3 仍存在的缺口（需决策或后续立项）

| # | 缺口 | 影响 | 建议 |
|---|---|---|---|
| O1 | **跨会话连接复用**：同一 server 在多会话各连一份 | 进程 / 内存放大 | 本期不做；P3 压测后评估连接池 |
| O2 | **stdio 的 cwd 只能取会话 cwd**（协议无该字段） | 相对路径依赖会话 cwd，可能与用户预期不符 | P1 写死为会话 cwd 并写入 README |
| O3 | **OAuth / 动态凭据**：Zed 丢弃 `oauth`，我们无凭据通道 | 受 OAuth 保护的远程 server 必然不可用 | 记为已知限制，不实现 |
| O4 | **MCP resources / prompts** 不经 ACP 暴露 | 功能面小于 Zed 原生 | 非目标，保持 |
| O5 | **`session/fork`** 若转正会成为第 4 个 `mcpServers` 入口 | 未来兼容性 | 在 session 方法分发处预留注释与测试位 |
| O6 | **Windows 垫片命令**（`npx` / `uvx` 等 `.cmd`）：`StdioTransport` 用 `exec.CommandContext` 直拉，未做 shell 包装 | 用户配 `npx` 可能启动失败 | **P1 内关闭**：加一条实测（`internal/mcp/transport/transport.go:110`） |
| O7 | **审计日志的落点与保留策略**未定 | 「可审计」无法验收 | **P1 内关闭**：明确写 debug 日志还是独立审计文件、保留多久 |
| O8 | **`--acp-mcp=client`**（只用客户端下发）时工具面语义未定义 | 可能出现「工具面突然变空」 | P1 定义并测试四态装配表 |
| O9 | `_meta` 里的「会话 MCP 摘要」是可选增强，schema 未定义 | 客户端无法依赖 | 保持可选；若做，先在本节补 schema |

### 11.4 结论

- §4 / §6 的主体设计（双 manager、纯函数映射、分期推进）在 Zed 现实行为下**依然成立**，
  无需推翻；本轮改动集中在**安全定性、可见性要求、三入口一致性、强杀兜底**四处。
- 唯一被**上调等级**的是安全：`mcpServers` 可达仓库级配置（Z11），D1 的门控从「加固项」
  变成「上线前置条件」。外部核实补充：Zed 的 Restricted Mode 确实会拦「安装与启动」，
  但**只覆盖项目级、全局 server 不受门控**，且它与转发列表的关系未确认（§4.9 补充段）
  ——**结论不变，门控仍在我们这一侧**。
- §11.3 的 O1–O9 不影响 P1 可交付性，但 **O6（Windows 垫片）与 O7（审计落点）必须在 P1 内关闭**，
  否则验收标准 2 与「可审计」无法判定。

---

## 12. 一页速览（给评审）

- **现在（已实施，证据见 §13）**：`mcpServers` 已按协议生效——三入口同一装配路径、逐条容错、
  信任门控 + 可见诊断 + 审计；回收三条路径（`session/close`、`session/delete`、强杀时的 Job Object
  兜底）均有实测。开工前基线「解析但不生效 + 零测试覆盖」见 §1 / §3.1 的注。
- **Zed 侧要实现的只有一件事**：把 `context_servers` 收集成 `mcpServers` 下发
  （客户端侧**不存在** MCP 工具路由；`McpServer::Acp` 由 `unstable_mcp_over_acp` 门控，未启用）。
- **客户端现实（已核实）**：Zed 把 `context_servers`（含**仓库级 `.zed/settings.json`** 来源）转成
  `mcpServers`，在 new / load / resume 三个入口下发，按 `enabled` 过滤（PR #43467），
  且**不读我们的能力位**（§2.5 Z1/Z2/Z6/Z11/Z12/Z13/Z14）。
- **客户端信任模型只能算纵深防御**：Zed 的 Restricted Mode 会拦项目级 server 的「安装与启动」，
  但全局 server 不受门控、且该机制与转发列表的关系未确认（§4.9 补充段）。
- **最难的不是连接，是安全**：`mcpServers` 能到达仓库级配置，等价于「clone 一个仓库 → 执行命令」，
  而现有 folder trust 不覆盖这条路径——这是 D1 门控必须存在的原因。
- **路径**：P0 立基线（不改行为，抓一次真实 Zed 报文）→ P1 只做 stdio（含信任门控、可见诊断、
  三入口同一函数、强杀兜底）→ P2 上 http/sse 与能力位 → P3 收口观测与实机验证。
- **§5 决策已定稿**（D1–D7，均带 Δ 依据）；D1 的门控默认值已按「拒绝 + 诊断」落地
  （`acp_mcp_host.go`：未信任工作区不启动进程 + stderr 诊断 + `acp.mcp.audit` 审计行），P2（http/sse）已落地。
- **完整性**：§11 给出 9 条本轮补齐项与 9 条仍存缺口；其中 O6（Windows 垫片 `resolveStdioCommand`）
  与 O7（审计落点 `acpMCPAuditf`）已在 P1 内关闭，其余按原结论保留。

---

## 13. 实施记录与实测证据（第三轮，2026-09-20）

### 13.1 落地清单

| 期 | 状态 | 关键落点 |
|---|---|---|
| P0 | ✅ 已落地 | `scripts/acp_e2e_mcp_local.go`（正向基线）、`acp_e2e_mcp_client_supplied_negative.go`（负向基线）、`docs/acp/README.md` 三层口径、G6 实测（§7.4-1） |
| P1 | ✅ 已落地 | `internal/acp/mcp.go`（`DecodeMCPServers` 逐条容错）、`cmd/aicli/commands/agent_stdio_mcp.go`（`toMCPConfig` + 会话级装配与回收）、`acp_mcp_host.go`（信任门控 + 审计 + 可见诊断）、`--acp-mcp=off\|client\|local\|merge`、Windows 强杀兜底 guard |
| P2 | ✅ 已落地 | `MCPConfig.Headers`（`internal/mcp/config/types.go`）+ `streamable.go` / `websocket.go`「Headers 优先、Env 兜底」；`mcpCapabilities{http,sse}` 置 true |
| P3 | 🟡 部分 | 工具面刷新已实施（§4.7 实施修正）；观测复用 `MCPStatus` + `AddLifecycleObserver`；多会话并发 / 隔离 / 回收已实测（`acp_e2e_mcp_multi_session.go`）；**待做**：Zed 实机报文回填（§7.3） |

### 13.2 工具面断点（本轮的决定性发现）

1. **现象**：ACP 会话里 MCP server 已连接（`mcp.connected` / `mcp.tools.loaded` 有日志），
   但模型工具面里没有它的工具；`session/new` 之后才连上的本地配置链工具同样缺席。
2. **根因**：`buildLocalChatToolPolicy` 在 agent 构建期把当时的 `toolSurface.ListTools()`
   **快照**成合成 allowlist；MCP 握手总在 bootstrap 之后完成，迟到工具被策略层拒绝，且
   `shouldRefreshStableToolSurface` 读同一策略 → 稳定工具面永不重建（两个断点同源）。
3. **修复**：`chat_tool_policy_sync.go` 的 `syncLocalChatToolPolicyAllowlist`，在每轮
   `localChatPrepareRunHook` 里把 live 工具面增量并入**合成**策略（幂等；原地更新同一 policy 指针，
   模型面与执行面同时生效）。
4. **边界（不扩权）**：仅当 `session.ToolPolicy == nil`、`policy.AllowlistEnabled`、
   `len(session.PermissionsOverlay.AllowTools) == 0` 且 `!session.DisableTools` 时生效；
   显式 `DeniedTools` 优先；显式 profile 策略与 overlay 收窄一律不触碰（表驱动单测覆盖）。

### 13.3 验证命令与结果

| 命令（cwd=`backend/`） | 结果 |
|---|---|
| `go build ./...` | exit 0 |
| `go vet ./cmd/aicli/commands/` | exit 0 |
| `go test ./cmd/aicli/commands/ -run "SyncLocalChatToolPolicy\|ACP\|MCP" -count=1` | `ok`（1.596s） |
| `go run ./scripts/acp_e2e_mcp_local.go bin/aicli-e2e.exe` | PASS：`OK prompt #1 upstream_tools=46 mcp_tool_visible=true callable=true`；`first_visible_prompt=1`；helper 标记文件在 `session/new` 后 absent、prompt #1 后 present |
| `go run ./scripts/acp_e2e_mcp_client_stdio.go bin/aicli-e2e.exe` | PASS：客户端下发 stdio server 启动（pid=30020）→ 工具 visible + callable → `session/close` 后 tasklist/proc 探针确认已回收 |
| `go run ./scripts/acp_e2e_mcp_session_load.go bin/aicli-e2e.exe` | PASS：`session/new`（无 mcpServers，零进程）→ `session/close` → `session/load` 装配客户端下发 server → `prompt #1 upstream_tools=46 load_tool_visible=true callable=true` → 回收（pid 探针） |
| `go run ./scripts/acp_e2e_mcp_parent_kill.go bin/aicli-e2e.exe` | PASS：`taskkill /F /PID`（**不带 `/T`**）硬杀 agent → MCP 子进程 600–800ms 内被 Job Object（`KILL_ON_JOB_CLOSE`）回收 |
| `go run ./scripts/acp_e2e_mcp_multi_session.go bin/aicli-e2e.exe` | PASS：4 会话并发 `session/new`（773–1010ms）+ 并发 `prompt`（447–569ms）→ 每会话 `own_tool_visible=true foreign_tools=0 callable=true`；关 #0 只回收自身子进程；全部关闭后无孤儿 |
| `go test -race ./cmd/aicli/commands/ -run "SyncLocalChatToolPolicy\|ACP\|MCP" -count=1` | `ok`（6.990s） |

日志：`backend/e2e-local-run2.log`、`backend/e2e-client-run2.log`。

**第四轮（2026-09-20，Zed 实机报文核对）**：

| 验证 | 结果 |
|---|---|
| `go test ./internal/acp/ -run "DecodeMCPServers" -count=1` | `ok`：新增 `TestDecodeMCPServersZedLoadSampleShape`（无 `type` → stdio；`env: []` → 0 issue、空列表） |
| `go test ./cmd/aicli/commands/ -run "ZedLoadSampleShape\|ACPMCPServerToConfigMapping\|KeyValueMap\|PlanACPSessionMCPGating" -count=1` | `ok`：新增 `TestZedLoadSampleShapeIsRecognized`（Zed 原文 → 1 server / 0 issue / stdio / `Env=nil` / `WorkingDir=cwd` / `TrustLevel=local`） |
| `go run ./scripts/acp_e2e_mcp_session_load.go bin/aicli-e2e.exe`（**载荷改为 Zed 同形状**：无 `type`、`env: []`） | PASS：`session/load assembled (pid=19876)` → `prompt #1 upstream_tools=46 load_tool_visible=true callable=true` → `session/close` 后回收 |
| `gofmt -l`（改动的 3 个文件） | 干净 |

### 13.4 仍未完成

- P3：**报文回填已完成**（§7.3.1，2026-09-20 实机样本 + 单测 + E2E 同形状复跑）；
  仍开放的是「Zed GUI 内真实交互」（在 agent 面板实际调用该工具，需要一次人工会话）。
- §7.4 第 2 条（Zed Restricted Mode 是否过滤转发列表）仍开放；第 1 / 3 / 4 / 6 条已关闭，
  第 5 条 Windows 侧已关闭（Unix 例外）。
- §11.3 的其余缺口（O1 / O2 / O4 / O5 / O9 等）按原结论保留。
