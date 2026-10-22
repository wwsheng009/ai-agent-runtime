# 方案审查：aicli 远程工具服务（SSH / HTTP 双传输）

> 审查对象：[aicli-remote-tools-ssh-http-design-20261021.md](./aicli-remote-tools-ssh-http-design-20261021.md)
> 审查日期：2026-10-21
> 审查方式：文档逐章核对 + 代码路径核对（file:line）+ 2 个可执行探针（pflag 解析、二进制体积）
> 结论：**方向成立，但暂不具备按当前 P0 排期开工的条件。** 需要先修 6 个阻塞项、收敛 6 个设计点，再做 1 次端到端 spike。

---

## 目录

1. [总体结论](#1-总体结论)
2. [阻塞项（必须先修）](#2-阻塞项必须先修)
3. [设计需收敛项](#3-设计需收敛项)
4. [事实性更正](#4-事实性更正)
5. [遗漏的改动点](#5-遗漏的改动点)
6. [安全审查补充](#6-安全审查补充)
7. [性能与资源](#7-性能与资源)
8. [测试补充](#8-测试补充)
8A. [Skill + CLI 路线专项审查](#8a-skill--cli-路线专项审查)
9. [分期与排期修正](#9-分期与排期修正)
10. [逐章对照](#10-逐章对照)
11. [建议的最小修订清单](#11-建议的最小修订清单)
12. [待确认问题](#12-待确认问题)
13. [多目标方案审查](#13-多目标方案审查)
14. [审查证据附录](#14-审查证据附录)

---

## 1. 总体结论

### 1.1 方向判断

| 判断项 | 结论 | 说明 |
|---|---|---|
| Remote Tools vs Remote Agent | **正确** | 目标机无外网时，LLM 留在本地是唯一低风险路径 |
| 一个二进制支持 SSH stdio + HTTP | **正确** | 与 `classfang/ssh-mcp-server` 的成熟做法一致 |
| 复用 MCP 协议与现有客户端 | **正确** | 传输层、配置层、工具适配层都已存在 |
| 默认只读、写/命令显式开启 | **正确** | 符合最小权限 |
| 第一版不做 OAuth | **正确** | 内网无浏览器，收益低 |
| 当前文档可直接进入实现 | **不成立** | 6 个阻塞项未收口，其中 2 个会让“远程权限”形同虚设 |

### 1.2 分项评分

| 维度 | 评分 | 主要依据 |
|---|---|---|
| 问题定义 | A | 场景、边界、非目标清晰 |
| 方案选型 | A- | Remote Tools 定位准确，备选方案有对照 |
| 双传输设计 | B | 方向对，但生命周期差异被“行为一致性契约”掩盖 |
| 安全模型 | B- | 有分层意识，但漏了 stdio 信任根与现有 policy 字段断链 |
| 现状事实准确性 | **C** | §2.1 两条关键结论与代码不符（见 B1/B2） |
| 可实施性 | B- | P0 漏了 pflag、配置回写、metadata 注入三处硬问题 |
| 测试与验收 | B | 矩阵较全，但缺治理字段、配置回写、图片、dedup 四类用例 |
| 运维与可观测 | B | 审计与 doctor 设计合理，缺版本协商与并发串行化 |

### 1.3 审查结论一句话

> 架构可以照做，但不能照当前文档的“现状描述”和“P0 工期”直接开工；应先把“现有远程防护到底有没有生效”这件事查清并修掉，否则第一版会在错误的基线上做安全设计。

---

## 2. 阻塞项（必须先修）

### B1. 主工具面路径丢失 MCP 治理字段，`BlockRemoteWrites` 实际不生效

**证据链**

1. `backend/internal/policy/tool_policy.go:354-359` 依赖两个字段：

```go
if p.BlockUntrustedMCP && tool.MCPTrustLevel == "untrusted_remote" && IsWriteLikeToolName(tool.Name) { ... }
if p.BlockRemoteWrites && tool.ExecutionMode == "remote_mcp" && IsWriteLikeToolName(tool.Name) { ... }
```

2. `backend/internal/tools/agent_adapter.go:58-63` 只有在 `lookupMCPTool` 成功时才填这两个字段；
3. 但 `lookupMCPTool`（同文件 `:131-139`）返回的 `skill.ToolInfo` **没有 `MCPTrustLevel`、也没有 `ExecutionMode`**：

```go
return skill.ToolInfo{
    Name:             info.Tool.Name,
    Description:      info.Tool.Description,
    InputSchema:      normalizeParameters(info.Tool.InputSchema),
    Metadata:         cloneMetadataMap(info.Metadata),
    MCPName:          info.MCPName,
    MaxParallelCalls: a.resolveMCPMaxParallelCalls(info.MCPName),
    Enabled:          info.Enabled,
}, true
```

4. 这两个宿主都走 `AgentAdapter`：
   - aicli TUI：`backend/cmd/aicli/commands/chat_actor_host.go:1338`
   - runtime-server：`backend/cmd/runtime-server/main.go:1758-1759`、`:1221-1223`

**影响**

- 在这两条主路径上，`BlockUntrustedMCP` 与 `BlockRemoteWrites` **永远不会触发**（字段是空串）；
- 只有 `skill.MCPAdapter` 路径（skill 执行、runtimeapi 的 MCP 管理面）会填这两个字段；
- 文档 §2.1 的“远程写默认拦截”这一行，对主路径是**不成立**的；
- 这不是“新方案的风险”，而是**现有代码的既有缺口**，任何远程 MCP 都会受影响，与 SSH/HTTP 无关。

**最小修复**

```go
// backend/internal/tools/agent_adapter.go
func (a *AgentAdapter) lookupMCPTool(toolName string) (skill.ToolInfo, bool) {
    ...
    status, _ := a.manager.mcp.GetMCPStatus(info.MCPName)
    trustLevel, executionMode := "", ""
    if status != nil {
        trustLevel = string(status.TrustLevel)
        executionMode = status.ExecutionMode
    }
    return skill.ToolInfo{
        ...
        MCPTrustLevel: trustLevel,
        ExecutionMode: executionMode,
    }, true
}
```

并新增回归测试：对任意 MCP 工具，`ListTools()` 返回的 `MCPTrustLevel` / `ExecutionMode` 必须非空，且与 `GetMCPStatus` 一致。

**为什么是阻塞项**：方案 §7.3 的远程闸门建立在这两个字段上；字段不修，后续“allow_write / allow_shell”设计得再细也不会生效。

---

### B2. `fs_owner=tool_server` 只在 skill 适配器上写入，主路径会把远端路径当本地路径校验

**证据链**

1. `backend/internal/skill/mcp_adapter.go:58-64` 写入：

```go
metadata[types.ToolMetadataFSOwnerKey] = types.ToolFSOwnerToolServer
```

2. 全仓搜索 `ToolFSOwnerToolServer`，只有 `skill/mcp_adapter.go`、`skill/executor.go`、`types` 与测试命中；`tools/agent_adapter.go`、`tools/manager.go` 都没有；
3. `backend/internal/toolexec/preflight.go:1497-1503` 与 `backend/internal/policy/tool_policy.go:501` 都以该字段决定“是否做本地 stat / 沙箱检查”；
4. `backend/internal/mcp/client/client.go:495-515` 的 `convertSDKTool` **只复制 Name/Description/InputSchema，不复制任何 metadata**；
5. 因此远端即使想声明 `fs_owner`，也到不了本地。

**影响**

- 远端 `view` / `grep` / `write` 的路径参数会被当成 `runtime` 归属：
  - 本地 preflight 会 `stat(/srv/app/...)`，在本地不存在 → 直接 false deny；
  - 本地沙箱会把它拼到本地 workspace 再校验 → 可能放行，也可能误拒；
  - 更危险的是 `rewritePathLikeArgs` 可能把远端路径“自动修复”成本地同名路径（虽然 `FSOwner` 判断会拦，但正是因为字段缺失才需要这条判断）。
- 这会让第一版远端只读工具**根本调用不通**，而且错误信息会误导为“路径不存在”。

**最小修复**

在 `tools.AgentAdapter.ListTools` / `lookupMCPTool` 返回的 `Metadata` 中补：

```go
if metadata == nil { metadata = map[string]interface{}{} }
metadata[types.ToolMetadataFSOwnerKey] = types.ToolFSOwnerToolServer
```

并加测试：`AgentAdapter.ListTools()` 中每个 `MCPName != ""` 的工具都必须带 `fs_owner=tool_server`。

**建议**：把“MCP 工具 → fs_owner=tool_server”收敛成一个共享函数，`skill.MCPAdapter` 与 `tools.AgentAdapter` 都调用它，避免第三次漂移。

---

### B3. `ssh-client` 的命令行解析会吞掉远端参数，文档里的 stdio 启动命令无法执行

**实测证据**

用仓库同版本 `pflag` 复现文档 §5.2 的参数：

```text
输入: -T deploy@dev.internal /usr/local/bin/aicli-remote-tools serve --stdio --workspace /srv/app
输出: PARSE_ERROR: unknown flag: --stdio
```

`ssh-client` 没有调用 `SetInterspersed(false)`（全文件检索无命中），pflag 默认开启交错解析，因此 host 之后的 `--stdio`、`--workspace` 会被当成 `ssh-client` 自己的 flag。

**加 `--` 后可行**：

```text
输入: -T deploy@dev.internal -- /usr/local/bin/aicli-remote-tools serve --stdio --workspace /srv/app
输出: ARGS: ["deploy@dev.internal" "/usr/local/bin/aicli-remote-tools" "serve" "--stdio" "--workspace" "/srv/app"]
```

**影响**

- 文档 §5.2 的 YAML 样例**逐字复制不可用**；
- 连现有的 `toolkit-mcp-server -tools shell,view` 也会被 `ssh-client` 解析成自己的 flag（`-tools` 不存在 → 报错）；
- 用户会得到“unknown flag”而不是“远端工具启动失败”，排查方向被带偏。

**最小修复（二选一，建议都做）**

1. `ssh-client` 增加 `fs.SetInterspersed(false)`，让“第一个位置参数之后全部视为远端命令”；
2. 在文档与样例中统一要求 `--` 分隔，并加一条 e2e：`ssh-client -T host -- <remote-binary> ...`。

注意：`-T` 在命令模式下其实是**无效果**的（见 F4），不要把“能跑通”寄托在它上面。

---

### B4. `ssh-client` 命令模式不转发 stdin，且命令字符串没有做 shell 转义

**证据链**

1. `backend/internal/sshclient/session.go:27-45` 的 `RunCommand` 只设置 `Stdout`/`Stderr`，没有 `session.Stdin`；
2. `backend/cmd/ssh-client/main.go:317-325` 用 `strings.Join(flags.command, " ")` 拼成单条远端命令；
3. `ssh.Session.Run(cmd)` 会把整条字符串交给远端 shell 解释。

**影响**

- stdio MCP 是双向 JSON-RPC，**没有 stdin 就无法工作**；
- `--workspace "/srv/my app"`、含 `$` 或引号的路径会被远端 shell 二次解释，轻则启动失败，重则执行到非预期路径；
- 这类错误很难在本地看出来，因为 `ssh-client` 本身退出码正常。

**最小修复**

1. `session.go` 增加 `RunCommandIO(ctx, client, command, stdin, stdout, stderr)`，命令分支改为传 `os.Stdin`；
2. 远端启动参数不要走“拼接字符串”。推荐两种：
   - `--config-b64 <base64>`：本地把结构化配置编码后传入，彻底避开 shell 语法层；
   - 或提供 `--exec-argv` 模式，由调用方以 argv 数组传入并逐项做 POSIX 单引号转义。
3. 加测试：workspace 含空格、含单引号、含 `$HOME` 字面量三种情况。

**注意**：`RunCommandIO` 修好 stdin 后，shell 解释问题仍然存在；两者必须一起修，否则只是“能连上但参数错”。

---

### B5. 复用 `ToolkitMCPServer` 会把 metadata 拼进模型正文，并丢弃图片内容

**证据链**

`backend/internal/toolkit/mcp_server.go:74-101`：

```go
if len(result.Metadata) > 0 {
    metadataJSON, _ := json.MarshalIndent(result.Metadata, "", "  ")
    content += fmt.Sprintf("\n\nMetadata:\n%s", string(metadataJSON))
}
contents = append(contents, &mcp.TextContent{Text: content})
```

同时整个函数只处理 `result.Content`，没有 `result.Data` / `MIMEType` 的分支。

**影响**

- 每个工具结果都会把元数据 JSON 再塞进模型正文，token 成本翻倍，且会把内部字段（`artifact_source_id`、`dedup_hit` 等）暴露给模型；
- 远端 `view` 读取图片时，`Data` 被丢弃，模型只看到空文本；
- 这与本地 `formatToolkitResultWithSource` 的口径不一致，破坏“本地/远端同名工具行为一致”的前提。

**最小修复**

1. metadata 只放 `CallToolResult.Meta`，**绝不**拼进 `Content`；
2. `Data` + `MIMEType` 映射为 `mcp.ImageContent`（若目标场景需要图片），或在远端明确拒绝图片并返回可解释错误；
3. 加契约测试：`Content` 中不得出现 `Metadata:`，`Meta` 中必须保留 `output_kind`；
4. 注意这是对现有 `toolkit-mcp-server` 的**行为变更**（见 F8），需要在变更记录中声明。

---

### B6. 新增 `executionScope` / `toolPrefix` / `remoteTargets` 会被现有配置写回链路静默丢弃

**证据链**

1. `backend/internal/mcp/admin/configfile.go:50-66` 的 `UpsertRequest` 没有这些字段；
2. 同文件 `Add/Update` 路径用请求体构造 `config.MCPConfig`；
3. CLI `/mcp add`（`backend/cmd/aicli/commands/chat_mcp_command.go`）、runtime API（`internal/api/runtimeapi/mcp_admin_handlers.go`）、微型 Web（`web_mcp_handlers.go`）都走同一个 admin 服务；
4. `admin.EnsureFile` 只会写 `mcpServers: {}` 或 `{"mcpServers":{}}`，不保留未知键。

**影响**

- 用户在 TUI / Web 里改一次 MCP 配置，`executionScope: remote` 就会消失；
- 一旦消失，`MCPConfig.ExecutionMode()` 会把 SSH stdio 重新判成 `local_mcp`（见 F2），远程写/命令重新绕过闸门；
- 这是“配置越改越不安全”的典型路径，必须在第一版前堵住。

**最小修复（二选一）**

1. 扩展 `UpsertRequest` / `MCPConfig` / admin 读写 / API / Web / CLI，并加 round-trip 测试；
2. **把远端目标放在独立文件**（如 `remote.yaml`），完全不走 `mcpadmin` 的写回链路；`mcpServers` 只由展开器生成、标记为“生成项”，禁止 UI 编辑。

建议选 2：远端目标包含 SSH 凭据/转发端口，本来就不该和通用 MCP 条目混在同一编辑面。

---

## 3. 设计需收敛项

### D1. 工具前缀放在远端还是本地，需要明确决策

文档当前选择“远端 `--tool-prefix`”，能解决 G4，但带来三个副作用：

| 问题 | 说明 |
|---|---|
| 多客户端 | HTTP daemon 的前缀是进程级固定的；Claude Desktop、另一个 aicli 想用不同前缀时无法区分 |
| 双前缀 | 本地 server 名若仍叫 `remote-dev`，canonical 名会变成 `mcp__remote_dev__remote_dev__view`；`registry.CanonicalToolName` 上限 64 字符，超长会被哈希截断 |
| 名称映射 | 如果改成客户端加前缀，本地显示名与线上 raw name 不一致，`ExecutionLookupName` 需要一张映射表 |

**建议**

- P1 保留远端前缀（实现最简单），但：
  - 本地 server 名用 `dev`，不要再用 `remote-dev`；
  - prefix 长度限制在 16 字符以内；
  - 加测试断言 `mcp__dev__remote_dev__view` 不超 64、不被哈希；
- 在文档中记录：若 P3 需要多客户端共享 HTTP daemon，前缀必须迁到客户端注册层（或改为“无条件 canonical 名 + 描述标注”）。

同时建议把 `target` 写进工具定义 metadata，并在 `toolsurface.Classify` / `observation.go` 中展示；否则 `/api/runtime/tools` 无法解释“这个工具属于哪台机器”。

---

### D2. 远程策略应落在 `ToolExecutionPolicy` 的 per-target map，而不是“策略副本”

文档 §7.3 写“对该 server 的工具构造一个策略副本”。问题是：

- `ToolExecutionPolicy` 是 **agent 级单对象**；
- `AllowToolInfo(tool)` / `AllowToolCallWithContext(ctx, tool, args)` 已经能拿到 `tool.MCPName`；
- 为每个 server 建副本会破坏 `Clone()`、`BuildToolSurfaceEligibilityKey`、profile 切换与审计的可解释性。

**建议结构**

```go
type ToolExecutionPolicy struct {
    ...
    RemoteTargets map[string]RemoteTargetPolicy // key: MCPName
}

type RemoteTargetPolicy struct {
    Scope            ExecutionScope // local | remote
    AllowRead        bool
    AllowWrite       bool
    AllowShell       bool
    RequireApproval  bool
}
```

- `skill.ToolInfo` 增加 `ExecutionScope`（由 `MCPConfig.ExecutionScope` → adapter 复制）；
- 判定时先看 `tool.ExecutionScope == remote`，再用 `tool.MCPName` 查 `RemoteTargets`；
- `BuildToolSurfaceEligibilityKey` 把 `RemoteTargets` 一并纳入，保证“改 allowWrite 后工具面/绑定键变化可解释”；
- **不要**用工具名或 `ExecutionMode` 推断远程，二者都会在 stdio-over-SSH 上失效。

---

### D3. “行为一致性契约”过度承诺，需要拆成“语义一致”与“生命周期不同”

文档 §5.4 把 stdio 与 HTTP 写成近似等价，但实际差异至少包括：

| 能力 | stdio | HTTP |
|---|---|---|
| 断线重连 | 不支持（进程结束） | 支持 `Mcp-Session-Id` 续连 |
| 长任务 | 受连接存活时间约束 | 可做远端 job |
| `view` 去重 | 进程重启即清空 | 服务端跨连接共享 |
| 取消 | 客户端取消 ctx **不会**自动关 stdin | 请求 ctx cancel 可终止 |
| 后台作业 | 不可用 | 可用 |

其中“取消”这一行尤其需要修正：MCP 客户端 `CallTool` 取消后，SDK 是否发送 `notifications/cancelled`、服务端是否把 handler ctx 取消，当前**未验证**。即使发送，远端 shell 也必须自己杀进程组才能生效。

**建议**

- 把契约表拆成两组：
  - **必须一致**：工具名、schema hash、错误码、路径 jail、输出预算、审计字段、权限判定；
  - **允许不同**：连接生命周期、重连、后台作业、去重状态、取消保证；
- 删除或降级“取消语义：stdio 关闭 stdin / 杀进程”的承诺，改成“stdio 模式仅保证服务端超时；客户端取消为 best-effort”；
- 在 UI/文档显式提示：需要断线续跑请用 HTTP。

---

### D4. HTTP 默认应有状态还是无状态，文档的理由不成立

文档 §5.3.1 用“审批回问与进度通知需要服务端发起请求”来论证有状态模式。但：

1. 远程写/命令的审批应发生在**本地策略层**（`tool_policy` / approval engine），不需要远端向客户端发 elicitation；
2. `backend/internal/mcp/client/client.go:121-127` 用 `mcp.NewClient(..., nil)` 建客户端，**没有声明 sampling/elicitation 能力**，也没有 handler；服务端即使发请求也会失败；
3. go-sdk 明确说明：stateless 模式下“请求上下文中的 server→client 通知仍可送达客户端”（`mcp/streamable_server.go:113-123`），进度通知可用。

**建议**

- P1 默认 **stateless**：无 `Mcp-Session-Id`、无粘滞会话、部署最简单；
- 保留 SSE（不要 `JSONResponse: true`），以便单次调用内的进度通知；
- 只有当 P3 引入“远端 job + 跨请求订阅”时，再切有状态 + `SessionTimeout`；
- 如果坚持有状态，必须补：`SessionTimeout`、`Mcp-Session-Id` 绑定 token/principal 的防劫持测试、负载均衡粘滞说明。

另外补一条部署注意：SDK 默认开启 localhost DNS rebinding 防护。经 `ssh -L` 访问时，URL 必须写 `http://127.0.0.1:<port>/mcp`；若写主机名，`Host` 头不是回环地址会被 403。

---

### D5. 远端 workspace 的信任根在两种传输下不同，文档只描述了 HTTP

文档 §6.3 说“远端 workspace 是服务端配置，不是客户端参数”。这对 HTTP 成立，对 **SSH stdio 不成立**：`--workspace` 就在本地拼出的 SSH 命令行里，客户端可以指向 SSH 用户能访问的任意目录。

**风险**

- 同一份“readonly”配置，HTTP 模式被服务端配置锁死，stdio 模式却由客户端决定根目录；
- 如果本地配置被篡改（或用户误配），远端工具会读到非预期目录；
- 这不是“越权到 root”，但破坏了“目标机是权限权威”的设计原则。

**建议**

- stdio 模式强制 `--config /etc/aicli/remote-tools.yaml`，`--workspace` 只允许作为 config 的覆盖项，且必须落在服务端 `allowedRoots` 内；
- 或保留 `--workspace`，但启动时校验它在服务端允许列表内，否则 fail-closed；
- `doctor` 输出实际生效的 workspace 与 allowedRoots，便于审计；
- 文档明确写出这个差异，不要用一句“服务端配置”覆盖两种传输。

---

### D6. 输出预算需要两层，`--max-output-bytes` 一层不够

现有本地链路是两层：

| 层 | 值 | 落点 |
|---|---|---|
| 捕获上限 | 256 KiB | `executor.DefaultRetainedOutputBytes`（`output_capture.go:17-24`） |
| 模型可见预算 | 12 KiB（可配） | `output.ModelToolTextByteBudget` / `shellOutputBudgetBytes` |
| 完整记录 | artifact | `ownShellOutputWindow` 先归档再折叠（`tool_output_budget.go:128-174`） |

文档只有一个 `--max-output-bytes 256 KiB`，直接返回给 MCP：

- 256 KiB 文本会整段进入模型上下文，远超 12 KiB 预算；
- 远端没有 artifact，折叠后中间部分**永久丢失**，模型无法像本地那样用 `artifact_read` 取回；
- 这会让“远端 shell”在第一版就产生不可恢复的信息损失。

**建议**

- 只读工具（P1）：沿用 `view` 的 32 KiB 窗口 + 分页、`grep` 的 output guard，不需要新的 artifact；
- shell（P2）：二选一
  - **A（推荐，简单）**：远端返回 head+tail 窗口 + 明确 `truncated=true` + “缩小命令/加过滤重跑”的 `next_action`，不承诺 artifact；
  - **B（完整）**：远端实现 artifact 存储 + `remote_dev__artifact_read`，与本地契约对齐；
- `--max-file-bytes 2 MiB` 对分页读取没有意义（`view` 本身就是流式窗口），建议改成 `--max-bytes-per-call`，避免误拒大文件。

---

### D7. 建议直接复用 `executor.Sandbox` 与 `shellrisk`，不要新写 `workspace.go` / `command_policy.go`

文档 §9.1 计划新增 `workspace.go`、`command_policy.go`。但仓库已经有：

- `backend/internal/executor/sandbox.go:25-49`：`AllowedPaths` / `DeniedPaths` / `ReadOnlyPaths` / `AllowedCommands` / `DeniedCommands` / `EnvWhitelist` / `AllowedHosts`；
- `Sandbox.CheckPermission`（`:104-136`）：词法检查 + symlink 解析后的物理路径复查，已覆盖路径逃逸；
- `Sandbox.ValidateCommand`：命令 allow/deny；
- `backend/internal/shellrisk/shellrisk.go`：`Assess` / `Segments` / `ResolveSegment` / 递归删除识别 / wrapper 解包（`:97-129`、`:846`）。

**建议**

- 远端二进制用 `executor.NewSandbox(config)` 构造策略，并通过工具已有的 `SetSandbox` 注入（与 `registerBuiltinToolkitTools` 完全同构），得到路径 jail + symlink 复查 + 命令白名单 + env 白名单；
- 危险命令判定复用 `shellrisk.Assess`，不要另起一套正则；
- 新增代码只保留：远端配置装配、审计、输出预算、双传输、doctor；
- 这样既减少代码量，也避免“第二套路径校验”引入的绕过风险。

---

## 4. 事实性更正

| 编号 | 文档位置 | 原文 | 实际情况 |
|---|---|---|---|
| F1 | §2.1 表格 | “远程写默认拦截” | 仅 `skill.MCPAdapter` 路径成立；`tools.AgentAdapter`（aicli TUI / runtime-server 主路径）两个字段为空，拦截不触发（B1） |
| F2 | §2.1 表格 | “自动打 `fs_owner=tool_server`” | 仅 `skill.MCPAdapter` 成立；主路径没有，且 `convertSDKTool` 不复制 metadata（B2） |
| F3 | §2.1 表格 | “Streamable HTTP 服务端验证已跑通” | 只验证了 `mcp.NewStreamableHTTPHandler` + 1 个工具 + `JSONResponse: true` 的测试场景，不等于 `toolkit-mcp-server` 能作为 HTTP 服务运行（G1 仍然成立） |
| F4 | §5.2 关键要求 3 | “`-T` 不分配 PTY” | 命令模式本来就不请求 PTY；`-T` 只影响 `interactiveShell` 分支（`main.go:341-377`）。在命令模式下写 `-T` 是无害但无效的 |
| F5 | §7.3 | “只有目标配置显式 `allow_write: true` 时……” | 目前没有这个配置字段，`mcpadmin` / `MCPConfig` / `UpsertRequest` 都没有；必须先加（B6） |
| F6 | §10.3 测试矩阵 | “带 `--remote` 的启动自检报错” | 不存在 `--remote` 标志；应统一为 `executionScope: remote` 或 `doctor --target` |
| F7 | §3 表格 | 星数 | 是 2026-10-21 的 GitHub API 快照，应标注日期与“近似值”，避免被当作长期事实 |
| F8 | §9.2 | “`toolkit-mcp-server/main.go` 不改” | 若按 B5 修 `ToolkitMCPServer`，本地 `toolkit-mcp-server` 的输出行为会改变（metadata 不再进正文）。需明确这是有意的兼容性修正，并补变更说明 |
| F9 | §9.2 | “`mcp/server/server.go` 支持 http.Handler” | 非必需。`ToolkitMCPServer.GetMCPServer()` 已暴露 `*mcp.Server`，新二进制可直接 `mcp.NewStreamableHTTPHandler(server.GetMCPServer(), opts)`，无需改共享 `BaseServer`，blast radius 更小 |

---

## 5. 遗漏的改动点

| 编号 | 文件/区域 | 为什么必须改 |
|---|---|---|
| M1 | `backend/internal/mcp/admin/configfile.go`（`UpsertRequest`、`Add`、`Update`） | 新字段的配置回写（B6） |
| M2 | `backend/internal/api/runtimeapi/mcp_admin_handlers.go`、`backend/cmd/aicli/commands/web_mcp_handlers.go`、`chat_mcp_command.go` | CLI / Web / API 三处编辑面对齐，否则字段被丢 |
| M3 | `backend/pkg/skillsapi/client.go:126,301-302` | 该包镜像了 `mcp_trust_level` / `execution_mode`；新增 `execution_scope` 后观测面要一致 |
| M4 | `backend/internal/toolsurface/toolsurface.go:57-70`、`observation.go` | 目前只按 `mcp_name` 分类；要展示 `target` 需扩展 |
| M5 | `backend/scripts/build.ps1`、`Makefile`、Win7 构建矩阵 | 新二进制需要构建/打包条目；`toolkit/mcp_server.go` 带 `!win7compat`，新 cmd 需同样处理 |
| M6 | `backend/internal/mcp/client/client.go:495-515` | `convertSDKTool` 丢弃 `Tool.Metadata`；若未来要让远端声明 `fs_owner`/`path_roles`，必须补复制 |
| M7 | `backend/internal/agent/tool_surface_binding.go:78-88` | `BuildToolSurfaceEligibilityKey` 未包含 remote target 策略；改 `allowWrite` 后绑定键不变，可解释性缺失 |
| M8 | `docs/user-guide/README.md`、`docs/user-guide/aicli-remote-tools.md`、`docs/aicli/install.md` | 新工具的安装、配置、排障入口 |
| M9 | `backend/internal/telemetry` 或 runtime events | 远端调用需要 `remote_target` / `transport` / `duration` 指标，便于定位慢链路 |
| M10 | `backend/internal/agent/tool_parallel_scheduler.go` | 远端写/命令应串行；目前并行度依赖 `MaxParallelCalls` 与工具 metadata，需要显式约束 |

---

## 6. 安全审查补充

### S1. stdio 的 workspace 信任根（见 D5）

必须把“客户端可指定 workspace”视为一条独立风险，不能与 HTTP 共用一句“服务端配置”。

### S2. HTTP token 的存放与传递

- 不要放 URL query（会进访问日志、代理日志、浏览器历史）；
- 不要放进程命令行（本机其他用户可见）；
- 推荐：环境变量或 0600 文件；`mcp-remote` 的 `--header-file` 是成熟先例；
- 若沿用 `headers: { Authorization: "Bearer ${TOKEN}" }`，需确认 `config.ExpandEnv` 覆盖 headers，并有“缺失即 fail-closed”的测试。

### S3. `doctor` 的 fail-closed 检查项

建议至少包括：

- HTTP 监听非 loopback 且未配置 token → 失败；
- token 长度 < 32 字节、或源码/配置中出现默认值 → 失败；
- workspace 不存在、不可读、或解析后落在 `allowedRoots` 之外 → 失败；
- audit 路径不可写、权限宽于 0600 → 失败；
- `--profile ops` 但 `allowedCommands` 为空 → 警告（当前设计是“为空即拒绝所有”，要显式提示否则用户以为能跑）。

### S4. 命令策略不要只靠元字符拒绝

`denyShellMetachars` 挡不住：

- `$IFS` 拼接；
- `printf`/`env`/`xargs` 作为 wrapper；
- 通配符展开到敏感路径；
- 解释器（`sh -c`、`python -c`、`perl -e`）绕过。

建议：

- 主路径 argv 化（`--config-b64` + 显式 argv），shell 仅作为显式开关；
- 拒绝解释器类命令，除非 allowlist 明确列出；
- 复用 `shellrisk.Assess` 做风险标注，而不是只用正则；
- allowlist 匹配“解析后的 argv[0] + 参数前缀”，不要匹配整条字符串。

### S5. 审计字段

除文档 §7.5 已有字段外，建议增加：

- `policy_version` / `prefix` / `execution_scope`：复盘时能确认当时生效的规则；
- `argv_hash` 与可选的 `argv_redacted`：只对 allowlist 命中的命令记录参数，避免泄露凭据；
- `session_id` / `tool_call_id` 必须来自 `_meta`，但要标注 `source=client`，不能作为授权依据。

### S6. 远端进程回收

- stdio：远端必须监听 stdin EOF，父进程退出后自杀；
- HTTP：命令必须放进独立进程组，超时杀整棵树；
- Linux 上建议 `setsid`；Windows 目标 P4 再处理 Job Object。

---

## 7. 性能与资源

| 项 | 实测/估算 | 结论 |
|---|---|---|
| 二进制体积 | 本机 `toolkit-mcp-server`（17 工具，Windows）**25.4 MB** | 可接受；只读子集因 `view`→`knowledge`→sqlite 依赖，体积不会显著下降 |
| SSH 冷启动 | 主要是握手；Go 进程启动毫秒级 | stdio 模式每次 MCP 连接一次，适合低频；高频切 HTTP |
| HTTP 会话 | 有状态会占用内存与粘滞要求 | P1 建议 stateless（D4） |
| 输出 | 单层 256 KiB 会撑爆上下文 | 必须两层预算（D6） |
| 并发 | 远端写/命令若并行会互相踩 | 服务端串行化写与 shell；`ops` profile 的 `MaxParallelCalls` 建议 1 |
| 大文件 | `view` 已分页 | 不要用 `--max-file-bytes` 一刀切（D6） |

---

## 8. 测试补充

在文档 §10.3 之外，至少补以下用例：

| 编号 | 用例 | 断言 |
|---|---|---|
| T1 | `ssh-client -T host remote-binary serve --stdio ...` | 当前失败；修复后必须成功（B3） |
| T2 | `AgentAdapter.ListTools()` 返回 MCP 工具 | `MCPTrustLevel` / `ExecutionMode` 非空且与 `GetMCPStatus` 一致（B1） |
| T3 | `AgentAdapter.ListTools()` 返回 MCP 工具 | `fs_owner=tool_server`（B2） |
| T4 | 远程 `write` 默认调用 | 在 **AgentAdapter 路径**上被拒绝（不是只在 skill 路径） |
| T5 | `allow_write=true` / `allow_shell=true` 分别开启 | 互不放大；`shell` 不受 `WriteLike` 影响（G5） |
| T6 | `ToolkitMCPServer` 成功结果 | `Content` 不含 `Metadata:`，`Meta.output_kind` 存在（B5） |
| T7 | 远端 `view` 图片 | 要么返回 ImageContent，要么返回明确“不支持图片”的错误（B5） |
| T8 | admin 新增/更新后重载 | `executionScope` / `toolPrefix` 保留（B6） |
| T9 | stdio vs HTTP | `tools/list` 名称、描述、schema hash 全等；错误码全等 |
| T10 | `view` 去重 | 明确 stdio/HTTP 行为差异并在测试中固化（D3） |
| T11 | 客户端取消 | 远端命令在 N 秒内被杀，或返回 `CANCEL_UNSUPPORTED`（D3） |
| T12 | 目标机无 `rg` | `grep` builtin fallback 可用，且性能在可接受范围 |
| T13 | 非交互 SSH | 无密码提示、无 host key 交互；缺 `known_hosts` 时错误可行动 |
| T14 | 路径含空格/引号/`$` | 远端 workspace 与文件参数不被打断（B4） |
| T15 | 远端 `view` 大文件 | 分页正常，不因 `--max-file-bytes` 误拒 |

---

## 8A. Skill + CLI 路线专项审查

> 对应设计文档新增的 §11.4「备选方案 D：Skill + 远端 CLI（不用 MCP）」。
> 结论：**可作为 P0 验证形态，不能作为目标架构。**

### 8A.1 机制核对

| 设计假设 | 代码核对 | 结论 |
|---|---|---|
| skill 能被模型看到并调用 | `skills_integration.go:30` `skillFunctionPrefix = "skill__"` | ✅ |
| workflow 能调用 `shell` 工具 | `skill/executor.go:206-248`，`step.Tool` → `FindTool` → `CallTool` | ✅ |
| skill 有权限声明 | `skill/skill.go:62` `Permissions []string`；`executor.go:1655` `checkPermissions` | ✅ 但只做字符串级授权 |
| 模型能表达结构化参数 | `skills_integration.go:543-562` 只有 `prompt/context/options` | ❌ |

### 8A.2 三个新增问题

**S-B1（高）：skill 参数面只有 `prompt`，结构化远程调用退化为字符串拼接**

`SkillFunction.Parameters()` 固定为 `prompt` / `context` / `options`（`skills_integration.go:543-562`）。
模型无法传 `offset` / `limit` / `glob` / `pattern`，只能把整段自然语言写进 `prompt`，
再经 `{{prompt}}` 替换进 `shell.command`。

后果：

- 远端 CLI 收到的是一条自由文本命令行，不是结构化参数；
- 参数校验只能靠远端 CLI 自己解析 argv，模型无法从 schema 得到约束；
- prompt 注入直接落到命令行层（本地 shell 白名单只能部分缓解）。

**S-B2（中）：返回只有文本，没有工具级契约**

`skill/executor.go:222-246` 的 observation 只有 `Output` 与成功/失败；
`formatOutput`（`:1620-1653`）把多步结果拼成文本。因此远端结果没有：

- `is_truncated` / `suggested_next_offset`；
- `file_path` / `line` / `exit_code`；
- `output_kind` / metadata。

模型无法据此续读、精确重试或区分错误类型。

**S-B3（中）：skill `permissions` 与远端 policy 是两套词表**

`checkPermissions` 只比对 `req` 里授予的字符串权限（`collectGrantedPermissions`），
与远端 `remoteTools.policy.allowRead/allowWrite/allowShell` 没有映射。
典型错配：skill 声明 `permissions: [shell]`，但远端是只读 profile。

### 8A.3 适用边界

| 场景 | 适合 Skill + CLI | 适合 MCP |
|---|---|---|
| 固定多步流程（部署/巡检/构建） | ✅ workflow 天然表达 DAG | 需要模型逐步调用 |
| 人工/CI 复用同一命令 | ✅ 可直接脚本化 | 需要 MCP 客户端 |
| 细粒度探索（grep → view → 改） | ❌ 参数无法结构化 | ✅ |
| 需要逐次审批 | ❌ 审批对象是整条 shell | ✅ 按工具/路径审批 |
| 目标机不能开端口 | ✅ 纯 SSH exec | ✅ 也可 stdio |

### 8A.4 结论与建议

- 不推荐用 Skill + CLI 替代 MCP 作为主架构；
- 推荐把它做成**同一二进制的第二种入口**：`aicli-remote-tools run <tool> ...`，与 `serve` 共享 workspace / policy / audit；
- 若需要先落地，P0 用 Skill + CLI 验证链路与用户价值是合理的，但必须在文档中标注“过渡形态”，并明确后续升级到 MCP 的迁移路径。

---

## 9. 分期与排期修正

### 9.1 建议的 P-1：先做端到端 spike（0.5–1 天）

在写任何新代码前，用**现有二进制**验证最危险的假设：

```text
本地 MCP 客户端
  → ssh-client -T host -- /path/to/toolkit-mcp-server -tools shell,view,grep
  → 远端进程
```

目标：

1. 证明 stdin/stdout JSON-RPC 可以穿过 `ssh-client`；
2. 证明 pflag `--` 或 `SetInterspersed(false)` 足够；
3. 记录冷启动、单次 `view`、单次 `shell` 的延迟；
4. 记录远端 stderr 是否污染协议流。

如果这一步失败，后面的双传输、策略、审计都无从谈起。**这一步不需要新二进制**，是最高性价比的风险消除。

### 9.2 修正后的阶段

| 阶段 | 内容 | 原估 | 建议估 | 原因 |
|---|---|---|---|---|
| **P-1** | 端到端 spike（现有二进制 + `ssh-client --`） | 无 | 0.5–1 天 | 先证明最危险假设 |
| **P0** | B1/B2 治理字段与 fs_owner；B3 pflag；B4 stdin + 参数编码；B5 metadata/图片；B6 配置回写方案 | 1–2 天 | **3–4 天** | 五项跨共享代码，且每项都要回归测试 |
| **P1** | 只读远端二进制：双传输、prefix、path jail、token、审计、doctor | 3–5 天 | **5–7 天** | 增加 D1/D4/D5 的决策与 parity 测试 |
| **P2** | 写与命令：gate、命令策略、审批、输出预算 | 3–5 天 | 4–6 天 | 复用 `executor.Sandbox` + `shellrisk` 可抵消部分增量 |
| **P3** | 远端 job、artifact、多目标、指标、重连 | 5–8 天 | 不变 | — |
| **P4** | gateway 隧道、mTLS/OAuth、Roots、Windows 目标 | 按需 | 不变 | — |

### 9.3 建议的开工顺序

1. 修 B1/B2（最小、独立、可立即回归）；
2. 修 B3/B4（`ssh-client`，可独立测试）；
3. 做 P-1 spike；
4. 定 D1/D4/D5 三个决策；
5. 再写 `aicli-remote-tools` 骨架；
6. 最后接策略与审批（B6）。

---

## 10. 逐章对照

| 文档章节 | 结论 | 说明 |
|---|---|---|
| §1.1 场景 | 通过 | 与仓库现状吻合 |
| §1.2 边界 | 通过 | 非目标写得清楚 |
| §1.3 为什么不是 Remote Agent | 通过 | 论据充分 |
| §2.1 已具备能力 | **部分不成立** | B1/B2/F3 三处需要修正 |
| §2.2 G1 | 通过 | `BaseServer.Start` 确实只支持 stdio |
| §2.2 G2 | 通过，但不完整 | 还漏了 pflag 交错解析与参数拼接（B3/B4） |
| §2.2 G3 | 通过 | `protocol.CallToolParams` 确实没有 Meta |
| §2.2 G4 | 通过 | `localToolkitPriorityTools` 确实会静默压掉同名工具 |
| §2.2 G5 | 通过，但低估 | 主路径连字段都没有（B1），不只是“不对称” |
| §2.2 G5b | 通过 | `ExecutionMode()` 对 stdio 恒为 `local_mcp` |
| §2.2 G6 | 通过 | artifact/job 确实没有远端等价物 |
| §3 GitHub 调研 | 通过 | 选型与结论合理；建议标注快照日期 |
| §4 目标架构 | 通过 | 组件边界清晰 |
| §4.2 两种部署形态 | 通过 | 但需补 D5 的信任根差异 |
| §4.4 与 gateway 的关系 | 通过 | 定位准确 |
| §5.1 统一入口 | 通过 | — |
| §5.2 SSH stdio | **阻塞** | B3/B4；另外 workspace 信任根需补 |
| §5.2.1 系统 OpenSSH | 通过 | 三个注意点正确 |
| §5.3 HTTP | 通过 | — |
| §5.3.1 有状态/无状态 | **需修改** | D4：理由不成立，P1 建议 stateless |
| §5.4 一致性契约 | **需修改** | D3：拆成“语义一致”与“生命周期不同” |
| §5.5 不直接暴露 toolkit-mcp-server | 通过 | 安全论证正确 |
| §6.1 命名空间 | **需决策** | D1：双前缀、64 字符、多客户端 |
| §6.2 `_meta` 透传 | 通过 | 链路设计正确 |
| §6.3 工作区语义 | **部分不成立** | D5：stdio 下由客户端决定 |
| §6.4 工具描述 | 通过 | 建议默认开启 |
| §7.1 身份认证 | 通过 | — |
| §7.2 授权档位 | 通过 | 默认不暴露网络工具是正确的 |
| §7.3 远程闸门 | **需重写** | B1/B6 + D2：落到 per-target map |
| §7.4 命令策略 | **建议复用** | D7：`executor.Sandbox` + `shellrisk` |
| §7.5 审计 | 通过 | 补 S5 字段 |
| §7.6 资源与稳定性 | 部分 | 补“远端写/命令串行化” |
| §8.1 远端配置 | 通过 | — |
| §8.2 本地配置 | **需调整** | B6：`remoteTargets` 与 `mcpadmin` 的关系 |
| §8.3 工具面成本 | 通过 | 建议补 `target` 账本 |
| §9.0 复用工具构造器 | 通过，但有副作用 | B5：`ToolkitMCPServer` 需先修 |
| §9.1 新增文件 | **可精简** | D7：workspace/command_policy 可复用现有包 |
| §9.2 修改清单 | **不完整** | M1–M10；F9 建议删掉 `mcp/server` 改动 |
| §9.3 代码草图 | 基本可用 | `RunCommandIO` 正确；参数编码需补 |
| §10.1 阶段 | **需重排** | §9 的 P-1 / P0 3–4 天 |
| §10.2 P1 范围 | 通过 | 但需明确“图片不支持”或补 ImageContent |
| §10.3 测试矩阵 | 需补 | T1–T15 |
| §10.4 验收标准 | 基本通过 | 补“AgentAdapter 路径”“配置回写”“图片”三项 |
| §11 风险 | 通过 | 建议补 B1/B2/D5 三条 |
| §12 待决策 | 通过 | 与本文 D1–D7 合并 |

---

## 11. 建议的最小修订清单

按优先级排序，可直接对设计文档打补丁：

1. **改 §2.1**：把“远程写默认拦截”“自动打 fs_owner”改成“仅 skill 适配器路径成立；主路径存在缺口（见审查 B1/B2）”。
2. **改 §2.2 G2**：补 pflag 交错解析与命令字符串转义两个子问题。
3. **改 §5.2**：样例参数加 `--`；或声明 `ssh-client` 将设置 `SetInterspersed(false)`；删除“`-T` 防 PTY”的误导。
4. **改 §5.3.1**：P1 默认 stateless + SSE；说明审批在本地、进度通知在请求上下文可用。
5. **改 §5.4**：拆成“必须一致 / 允许不同”两张表；取消语义降级为 best-effort。
6. **改 §6.3**：明确 stdio 的 workspace 由客户端命令行决定，需服务端 allowlist 复核。
7. **改 §7.3**：用 `RemoteTargets map[string]RemoteTargetPolicy` 替代“策略副本”；`ExecutionScope` 同时进 `skill.ToolInfo`。
8. **改 §7.4**：改为复用 `executor.Sandbox` + `shellrisk`，删除自写 `command_policy.go`。
9. **改 §8.2**：`remoteTargets` 放独立文件，或明确 admin 写回扩展；二选一。
10. **改 §9.0**：先修 `ToolkitMCPServer` 的 metadata 注入与图片丢弃，再谈复用。
11. **改 §9.2**：补 M1–M10；删除 `mcp/server/server.go` 改动（用 `GetMCPServer()` 即可）。
12. **改 §10.1**：加 P-1 spike；P0 调整为 3–4 天。
13. **改 §10.3/10.4**：补 T1–T15 与三项验收。

---

## 12. 待确认问题

请设计作者明确以下决策，评审后才能进入实现：

1. **前缀位置**：远端固定前缀（简单、单租户），还是本地注册层前缀（多客户端友好、需要名称映射）？
2. **HTTP 模式**：P1 是否有状态？若坚持有状态，谁需要服务端发起的请求？
3. **stdio workspace**：允许命令行指定，还是强制读服务端配置文件？
4. **远端 shell 输出**：head+tail + 重跑提示，还是实现远端 artifact？
5. **策略复用**：是否接受复用 `executor.Sandbox` 与 `shellrisk`，放弃新增 `workspace.go` / `command_policy.go`？
6. **`toolkit-mcp-server` 行为变更**：是否接受“metadata 不再进正文”的兼容性修正？
7. **版本协商**：P1 是否要求远端二进制与本地 schema 版本匹配？不匹配时是拒绝还是降级？
8. **Windows 目标**：确认 P4，还是要进 P1/P2？
9. **审批粒度**：远端写/命令默认逐次审批，还是 per-target 一次授权？
10. **多目标工具面**：默认只读 4 工具是否够用，还是需要 profile 级“按需展开”？

---

## 13. 多目标方案审查

> 对应设计文档新增的第 13 章「多目标部署（Multi-Target）」。
> 结论：**多目标方向正确，且现有代码已具备大部分基础；但设计中“三层区分”只完成了名字层，元数据层与策略层都缺实现，且漏了子代理越权与 canonical 名截断两个真实风险。**

### 13.1 现有能力核对

| 设计声称 | 代码核对 | 结论 |
|---|---|---|
| 每个 server 一个 client | `mcp/manager/manager.go:132` `clients map[string]client.Client` | ✅ 成立 |
| 同名工具不互相覆盖 | `mcp/registry/registry.go:71` key = `mcpName + NUL + toolName` | ✅ 成立 |
| 短名歧义 fail-closed | `registry.go:48-60`、`:606-613` | ✅ 成立 |
| 按目标独立并发闸门 | `agent/tool_parallel_scheduler.go:142-167` | ✅ 成立 |
| 配置是 map | `mcp/config/types.go:23` | ✅ 成立 |
| 工具元数据带 `remote_target` | 无 | ❌ 设计新增，未实现 |
| 策略按目标隔离 | 无 | ❌ 依赖 D2 的 `RemoteTargets` |

### 13.2 新发现的三个风险

#### MT-R1（高）：子代理会继承全部远程目标

`subagent_spawn` / `subagent_spawn_batch` 的能力继承走 `policy.CapabilitiesForTask`，只按 role / read_only / toolNames / writePaths 计算，**没有 target 维度**。结果是：

- 父会话只读 `prod`；
- 子代理拿到 `remote_prod__write` 与 `remote_prod__shell`（如果工具面已注册）；
- 父的“只读”约束不会自动下传。

这与仓库已有的 `BlockDelegation`、`read_only` 继承是同一类问题，但 target 维度完全没有覆盖。

**建议**：把 target 作为 `CapabilitiesForTask` 的独立输入，或在 `subagent_spawn` 的参数里显式 `allowed_remote_targets`，默认取父会话的交集；`prod` 这类目标默认 `deny`。

#### MT-R2（中）：canonical 名 64 字符上限会静默截断

`registry.go:229-237`：

```go
canonical := "mcp__" + portableNamePart(mcpName) + "__" + portableNamePart(toolName)
if len(canonical) <= 64 { return canonical }
suffix := "_" + shortIdentityHash(...)
return canonical[:64-len(suffix)] + suffix
```

设计文档推荐的 `server=dev` + `prefix=remote_dev` 得到 `mcp__dev__remote_dev__view`（30 字符），安全。
但如果目标名或工具名偏长（例如 `server=k8s-prod-cluster-1`、`prefix=remote_k8s_prod_cluster_1`），会触发哈希截断，导致：

- 工具名对模型不可读；
- 日志与审计难以对应；
- 文档里的“名字即作用域”假设失效。

**建议**：

- 展开配置时校验 `len(canonical) <= 56`（留 8 字符余量），超限直接报错而不是截断；
- 目标名 hard limit 12 字符，前缀 hard limit 16 字符；
- 加测试：最长允许组合必须不触发 hash。

#### MT-R3（中）：`localToolkitPriorityTools` 仍会让远端 `view/ls/glob/grep` 不可见

设计说“强制 prefix 解决 G4”，但只解决“名字相同”的情况。如果 prefix 之后名字变成 `remote_dev__view`，确实不再与本地 `view` 冲突。**但**：

- `tools.Manager.ListTools()` 先加 MCP 工具，再加本地工具；
- `shouldPreferLocalToolkit` 只在 `toolName` 完全相同时才跳过；
- 所以 `remote_dev__view` 不会被压掉——这一条设计是对的。

真正的残余风险是：**模型可能优先选本地 `view` 而不是 `remote_dev__view`**，因为本地名更短、更常见。这不是覆盖问题，而是**选择偏差**。

**建议**：

- 在远端工具描述里明确写“这是 remote:dev，不是本地文件系统”；
- 在系统提示或工具使用指引中加入“当用户提到某台服务器时，使用 `remote_<target>__*`”；
- 若观察面能拿到调用分布，监控“用户提到服务器但模型调了本地工具”的比率。

### 13.3 与 D2 的关系

设计 §13.2 的第三层“策略”依赖 D2 的 `RemoteTargets map[string]RemoteTargetPolicy`。两处必须用同一个结构：

- 第 7 章讲“远程闸门怎么判”；
- 第 13 章讲“多个目标怎么各配一套”。

建议把 `RemoteTargets` 的定义只放在第 7 章，第 13 章引用，避免两处不一致。

### 13.4 多目标相关的新增改动点

| 编号 | 文件/区域 | 改动 |
|---|---|---|
| M11 | `backend/internal/policy/capability_scope.go` | `CapabilitiesForTask` 增加 target 维度，或在 spawn 参数中传 allowlist |
| M12 | `backend/internal/agent/loop.go`（`subagent_spawn` 构造） | 把父会话的 allowed remote targets 注入子任务 |
| M13 | `backend/internal/mcp/registry/registry.go` 或配置展开器 | canonical 长度校验，禁止静默截断 |
| M14 | `backend/internal/toolsurface/observation.go` | metadata 增加 `remote_target` / `transport` |
| M15 | `backend/internal/mcp/config/types.go`（`MCPStatus`） | 增加 `RemoteTarget` / `Transport` / `ExecutionScope` / `LastLatencyMS` |

### 13.5 多目标验收补充

在设计 §13.9 的 MT1–MT9 之外，补三条：

| 编号 | 用例 | 断言 |
|---|---|---|
| MT10 | 子代理 spawn | 默认拿不到 `prod` 的写/命令工具；显式 allow 后才可见 |
| MT11 | 超长目标名 | 配置展开期 fail-closed，不产生哈希截断的工具名 |
| MT12 | 模型选择 | 在同时存在本地 `view` 与 `remote_dev__view` 时，工具描述能区分作用域（人工评估 + 提示词检查） |

### 13.6 多目标审查结论

- “一个 Agent 连多台服务器”**成立**，现有 MCP 架构原生支持；
- “如何区分”需要三层同时具备，当前设计只完成了第一层（名字），第二、三层需要按 D2 / M11–M15 补齐；
- 最大的未识别风险是**子代理继承全部目标**，必须在 P1 就处理，不能留到 P3。

---

## 14. 审查证据附录

### 14.1 代码坐标

| 结论 | 证据 |
|---|---|
| B1 治理字段缺失 | `backend/internal/tools/agent_adapter.go:58-63`、`:131-139`；`backend/internal/policy/tool_policy.go:354-359` |
| B2 fs_owner 缺失 | `backend/internal/skill/mcp_adapter.go:58-64`；`backend/internal/toolexec/preflight.go:1497-1503`；`backend/internal/policy/tool_policy.go:501`；`backend/internal/mcp/client/client.go:495-515` |
| B3 pflag | `backend/cmd/ssh-client/main.go:144-155`（无 `SetInterspersed`）；实测输出见 13.2 |
| B4 stdin/拼接 | `backend/internal/sshclient/session.go:27-45`；`backend/cmd/ssh-client/main.go:317-325` |
| B5 metadata/图片 | `backend/internal/toolkit/mcp_server.go:74-101`、`:82-85` |
| B6 配置回写 | `backend/internal/mcp/admin/configfile.go:50-66`；`backend/internal/mcp/config/types.go:258-296` |
| D1 命名 | `backend/internal/mcp/registry/registry.go:229-237`（`mcp__server__tool` + 64 字符） |
| D2 策略 | `backend/internal/policy/tool_policy.go:19-67`；`backend/internal/agent/tool_surface_binding.go:78-88` |
| D3 生命周期 | `backend/internal/mcp/client/client.go:163-170`（background ctx）；`backend/internal/toolkit/tools/read_dedup.go:75-96` |
| D4 无状态 | `go-sdk@v1.4.0/mcp/streamable_server.go:113-123`；`backend/internal/mcp/client/client.go:121-127` |
| D5 信任根 | `backend/cmd/ssh-client/main.go:319`；`backend/internal/mcp/config/types.go:286-296` |
| D6 预算 | `backend/internal/executor/output_capture.go:17-24`；`backend/internal/toolkit/tools/tool_output_budget.go:141-174` |
| D7 复用 | `backend/internal/executor/sandbox.go:25-49`、`:104-136`；`backend/internal/shellrisk/shellrisk.go:97-129`、`:846` |
| F4 `-T` | `backend/cmd/ssh-client/main.go:341-377` |
| F9 BaseServer | `backend/internal/mcp/server/server.go:65-73`；`backend/internal/toolkit/mcp_server.go:114-117` |

### 14.2 pflag 探针输出

```text
interspersed=true  args=["deploy@dev.internal" "/usr/local/bin/aicli-remote-tools" "serve"] err=unknown flag: --stdio
interspersed=true  args=["deploy@dev.internal" "/usr/local/bin/aicli-remote-tools" "serve" "--stdio" "--workspace" "/srv/app"] err=<nil>   # 使用 --
```

### 14.3 二进制体积

```text
toolkit-mcp-server (Windows, 17 tools) = 25.4 MB
```

### 14.4 未验证项

以下结论来自代码阅读，尚未做运行验证，建议在 P-1 spike 中确认：

1. MCP 客户端取消 `CallTool` 时，go-sdk 是否发送 `notifications/cancelled`，服务端是否取消 handler ctx；
2. `ssh-client` 在无 TTY 且未提供密码时是否会阻塞在交互提示；
3. 目标机无 `rg` 时 `GrepTool` builtin fallback 的实际性能；
4. `config.ExpandEnv` 是否覆盖 `headers`（token 注入路径）；
5. `mcp.NewStreamableHTTPHandler` 在 `-L` 转发且 URL 使用主机名时的 Host 头行为。
