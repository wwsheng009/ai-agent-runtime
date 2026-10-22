# aicli 远程工具服务设计：SSH / HTTP 双传输（Remote Tools）

> 状态：**P0–P4 已实施（2026-10-21）**
> 目标：本地 aicli TUI / runtime-server 在**无外网、仅内网可达目标机**的场景下，调用远端文件读取、搜索、命令执行等工具。
> 结论先行：**做 Remote Tools，不做 Remote Agent；一个远端二进制，同时支持 SSH stdio 与 Streamable HTTP 两种接入方式。**

## 实施状态（2026-10-21）

| 阶段 | 状态 | 落点 |
|---|---|---|
| B1/B2 治理字段与 fs_owner | ✅ | `internal/tools/agent_adapter.go`、`internal/skill/mcp_adapter.go` |
| B3/B4 ssh-client 参数与 stdin | ✅ | `cmd/ssh-client/main.go`、`internal/sshclient/session.go` |
| B5 MCP 输出契约 | ✅ | `internal/toolkit/mcp_server.go` |
| B6 executionScope 贯通 | ✅ | `internal/mcp/config/types.go`、`internal/policy/tool_policy.go` |
| P1 只读远端服务 | ✅ | `cmd/aicli-remote-tools`、`internal/remotetools` |
| P2 写与命令 | ✅ | `internal/remotetools/{write,apply_patch,shell,sandbox}.go` |
| P3 远端 job / artifact / 多目标 / 指标 | ✅ | `internal/remotetools/{jobs,jobs_tools,artifact,metrics}.go`、`internal/remotetargets` |
| P4 mTLS / Windows 路径 / MCP Roots | ✅ | `internal/mcp/transport/tls_client.go`、`internal/remotetools/{platform,roots}.go` |
| P4 gateway 隧道 | ❌ 评估后不采用 | 见 §11.3.1；改用 `ssh -R` |
| 遗留：MCP Roots 传输接线 | ✅ | `internal/remotetools/roots_transport.go` |
| 遗留：子代理 target 授权（MT-R1） | ⛔ 已按需求移除 | 曾实现 per-target + per-role 授权；2026-10-22 判定为 mode 之外的重复控制层，整条链路删除。子代理继承父会话工具面与 mode，`DeriveChildForTask` 收成只读 |
| 真实场景验证：ConPTY + 模型自主调用 | ✅ | `scripts/remote_task_conpty.go`；暴露并修复 L1/L2（§10.5） |
| 零预置验证：远端装 Deno + 建站 + 跑起来 | ✅ | 模型自主完成；本地零源码、远端初始为空目录 |
| 双 transport 实测（真实 ssh-client） | ✅ | `scripts/ssh_transports_e2e.go`；同时发现并修复 L3 |
| 遗留：`remoteTargets` 接入 chat | ✅ | `cmd/aicli/commands/chat_remote_targets.go` |
| 遗留：`job_start` 绕过 `allowShell` | ✅ 换实现 | 原用 `toolWantsRemoteShell` 补救；现改为**按服务端声明的裸名分类**（`mcp_raw_tool_name` → `background_task`），补丁表已删除 |
| 遗留：`ssh-stdio` 的 `--` 分隔符 | ✅ | `cmd/ssh-client/main.go` 的 `parseFlags` |
| 真实部署场景：1.0.0 → 2.0.0 升级 + 回滚 | ✅ | prompt 模板见 `aicli-remote-deploy-prompt-template.md` |
| 权限模型文档化 | ✅ 三层 | `aicli-remote-tools-permissions.md`；L3 由 per-target 授权表改为会话 mode（2026-10-22） |
| 从零配置指南 | ✅ | `aicli-remote-tools-setup-guide.md`（含三种拓扑与排错表） |
| 独占模式可行性（`--mcp-only`） | ✅ 分析完成，待接线 | `aicli-mcp-only-exclusive-mode-analysis.md`；`NewRemoteOnlyManager`、`remotetargets.ParseSpec` 已实现 |
| profile 裁剪方案（零代码） | ✅ 实测可用 | `mcp.use_servers` + `tools.denylist`；审计见 `scripts/profile_surface_audit.go` |
| 同名远程工具加载期拒绝 | ✅ 已实现 | `internal/tools/manager.go` 的 `validateRemoteToolNameCollision` |
| 列表/执行判定合并（根治分裂） | ✅ 已实现 | 快速通道收窄到读族；`shouldPreferLocalToolkitTool` 已删除 |
| 内置 `remote` profile 模板 | ✅ 已实现 | `internal/profile/templates/remote/`；`profile create x --template remote` |
| 剩余缺口清单 | 📋 已梳理 | `aicli-remote-tools-gaps.md`（G1–G12） |
| **`aicli chat --remote <spec>`** | ✅ 已实现 | 无需 mcp.yaml，与已有 MCP 共存；`chat_remote_flag.go` |

---

## 目录

1. [问题与边界](#1-问题与边界)
2. [现状与代码级缺口](#2-现状与代码级缺口)
3. [GitHub 同类方案调研](#3-github-同类方案调研)
4. [目标架构](#4-目标架构)
5. [双传输设计：SSH stdio 与 Streamable HTTP](#5-双传输设计ssh-stdio-与-streamable-http)
6. [工具命名、上下文与工作区语义](#6-工具命名上下文与工作区语义)
7. [权限与安全模型](#7-权限与安全模型)
8. [配置设计](#8-配置设计)
9. [对现有代码的改动清单](#9-对现有代码的改动清单)
10. [实施阶段与验收](#10-实施阶段与验收)
11. [风险与备选方案](#11-风险与备选方案)
12. [待决策问题](#12-待决策问题)
13. [多目标部署（Multi-Target）](#13-多目标部署multi-target)
14. [参考](#14-参考)

---

## 1. 问题与边界

### 1.1 场景

- `aicli` TUI、`runtime-server` 运行在本地电脑，本地具备外网/模型访问能力；
- 目标服务器位于公司内网，本地可 SSH 到达，但目标机**不能访问公网**，也不应安装完整 Agent；
- 需要让本地 Agent 读取目标机文件、搜索代码、执行命令、运行构建/测试；
- 部分环境还要求目标机**不监听任何对外端口**。

### 1.2 设计边界

| 项 | 决定 |
|---|---|
| LLM 推理位置 | **始终在本地**，目标机不需要模型访问 |
| Agent 循环位置 | **始终在本地**，只有工具调用跨机器 |
| 目标机部署物 | **一个静态编译的小二进制**，不部署完整 `aicli` / `runtime-server` |
| 协议 | MCP（Model Context Protocol），复用现有 MCP 客户端与服务端适配层 |
| 传输 | 同一二进制支持 `stdio`（经 SSH exec）与 `streamable HTTP`（直连或 SSH 本地转发） |
| 默认能力 | 只读：`view` / `ls` / `glob` / `grep` |
| 写与命令执行 | 必须 per-target 显式开启，并有独立策略、审批与审计 |
| 不做 | 不在第一版做远端自治 Agent、不做 OAuth 动态注册、不做跨机会话同步 |

### 1.3 为什么不是 Remote Agent

| 维度 | Remote Tools | Remote Agent（完整 Agent 在远端） |
|---|---|---|
| 目标机是否需要模型访问 | 不需要 | 需要，内网无外网时还要部署模型网关 |
| 复用现有能力 | 复用 Agent 循环、审批、会话、上下文管理 | 会话、审批、结果同步都要跨机器重做 |
| 延迟 | 只有工具 RPC 跨网 | 模型请求也可能跨网，且远端要排队 |
| 权限粒度 | 本地可见每次工具调用 | 本地只看到最终结果，难以逐次审批 |
| 运维成本 | 一个小二进制 | 完整运行时、配置、升级、密钥、日志 |
| 适用 | 读文件、跑命令、构建测试 | 长时间值守、断连续跑、远端自治批处理 |

结论：当前需求用 Remote Tools 足够；Remote Agent 作为 P4 备选，不与第一版混做。

---

## 2. 现状与代码级缺口

### 2.1 已经具备的能力

| 能力 | 落点 | 说明 |
|---|---|---|
| MCP 客户端多传输 | `backend/internal/mcp/transport/transport.go` | `stdio` / `sse` / `websocket` / `streamable` 均已实现 |
| Streamable HTTP 客户端 | `backend/internal/mcp/transport/streamable.go` | 支持 headers 与 OAuth token 注入 |
| MCP 配置 | `backend/internal/mcp/config/loader.go` | `command`+`args`（stdio）与 `url`（远程）均校验通过 |
| 工具暴露为 MCP | `backend/internal/toolkit/mcp_server.go` | `ToolkitMCPServer` 可注册全部内置工具 |
| Streamable HTTP 服务端验证 | `backend/internal/toolkit/mcp_server_test.go` | 已用 `mcp.NewStreamableHTTPHandler` 跑通测试 |
| SSH 客户端 | `backend/cmd/ssh-client`、`backend/internal/sshclient` | 命令执行、交互 shell、`-L`/`-R` 转发、断线重连、ProxyCommand |
| SFTP | `backend/internal/sshclient/sftp.go`、`cmd/sftp-client` | 文件上传/下载/列目录 |
| 远端 MCP 工具归属标记 | `backend/internal/skill/mcp_adapter.go` | 自动打 `fs_owner=tool_server`，本地不做远端路径 stat |
| 远程写默认拦截 | `backend/internal/policy/tool_policy.go` | `BlockUntrustedMCP` / `BlockRemoteWrites` 默认 `true` |
| 工具合并与来源账本 | `backend/internal/tools/manager.go`、`internal/toolsurface` | 本地/MCP 工具统一列表，按来源分类 |

### 2.2 七个必须先解决的缺口

#### G1. `toolkit-mcp-server` 只能 stdio，不能作为网络服务

`backend/internal/mcp/server/server.go:65-73`：

```go
func (s *BaseServer) Start(ctx context.Context, transport any) error {
	switch t := transport.(type) {
	case *mcp.StdioTransport:
		return s.server.Run(ctx, t)
	default:
		return fmt.Errorf("不支持的传输类型: %T", transport)
	}
}
```

`backend/cmd/toolkit-mcp-server/main.go` 也只调用 `Start(ctx)`，没有监听地址、认证、工作区、限流、审计。

#### G2. `ssh-client` 命令模式不转发 stdin，无法承载 stdio MCP

`backend/cmd/ssh-client/main.go:317-325` 走：

```go
cmdStr := strings.Join(flags.command, " ")
code, err := sshclient.RunCommand(sshConn, cmdStr, os.Stdout, os.Stderr)
```

而 `backend/internal/sshclient/session.go:27-45` 的 `RunCommand` 只设置 `Stdout`/`Stderr`，**没有设置 `session.Stdin`**。stdio MCP 是双向 JSON-RPC，必须把本地 stdin 持续写入远端进程 stdin；当前命令模式做不到。

同时，`interactiveShell` 的分支语义容易误用：`-T` 时不请求 PTY 但走的是 `session.Shell()`
（远端登录 shell），不带 `-T` 时会 `term.MakeRaw` 并请求 PTY（`main.go:341-377`）。
这两条都不是“把本地 stdin 原样喂给远端某个具体命令”的管道模式；
help 里宣称的 pipe mode 与实际命令模式的行为存在缺口。

#### G3. MCP 客户端不传 `_meta`，会话/工作区/追踪上下文无法过网

`backend/internal/mcp/protocol/message.go:108-112`：

```go
type CallToolParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}
```

没有 `Meta`。`backend/internal/mcp/client/client.go:32` 的 `CallTool(ctx, name, args)` 也没有 meta 参数。结果是 `toolctx` 里的 `SessionID` / `TurnID` / `ToolCallID` / `WorkspaceRoot` 全部丢失。

MCP SDK 本身支持：`mcp.CallToolParams` 有 `Meta`（见 `go-sdk@v1.4.0/mcp/protocol.go:44-54`），服务端 handler 可以通过 `req.Params.Meta` 读取。因此扩展点是明确的，不是协议不支持。

#### G4. 本地与远端同名工具会静默互相覆盖

`backend/internal/tools/manager.go:60-65` 把 `view` / `grep` / `ls` / `glob` 列为本地优先：

```go
var localToolkitPriorityTools = func() map[string]struct{} {
	names := toolregistry.CallableNames("ls", "glob", "grep", "view")
	...
}()
```

后果：

- 远端 server 暴露 `view` → 被本地 `view` 压掉，模型看不到远端工具；
- 远端 server 暴露 `shell` → 可能反过来压掉本地 `shell`，模型以为在跑本地命令，实际在远端执行。

这不是“冲突提示”，而是**静默语义错位**，必须用命名空间消除。

#### G5. 远程写策略与 shell 策略不对称

`backend/internal/policy/tool_policy.go:69-82`：

```go
policy := &ToolExecutionPolicy{
	ReadOnly:          readOnly,
	BlockUntrustedMCP: true,
	BlockRemoteWrites: true,
	...
}
```

`:354-359`：

```go
if p.BlockUntrustedMCP && tool.MCPTrustLevel == "untrusted_remote" && IsWriteLikeToolName(tool.Name) {
	return fmt.Errorf("untrusted remote MCP cannot execute write-like tool: %s", tool.Name)
}
if p.BlockRemoteWrites && tool.ExecutionMode == "remote_mcp" && IsWriteLikeToolName(tool.Name) {
	return fmt.Errorf("remote MCP execution mode blocks write-like tool: %s", tool.Name)
}
```

而 `backend/internal/toolregistry/registry.go:143` 中 `shell` 只声明了 `ShellLike: true`，**没有 `WriteLike`**。因此：

- 远程 `write` / `edit` / `apply_patch` 默认被拦；
- 远程 `shell` 反而不走 `BlockRemoteWrites`，可能直接放行。

这是上线前必须收口的高危不对称。远程命令执行的破坏力不低于写文件。

#### G5b. SSH stdio 会把“远端”归类成本地

`backend/internal/mcp/config/types.go:258-296`：

```go
func (m *MCPConfig) ResolvedTrustLevel() MCPTrustLevel {
	if m.TrustLevel != "" {
		return m.TrustLevel
	}
	switch m.Type {
	case "stdio":
		return MCPTrustLevelLocal
	default:
		return MCPTrustLevelUntrusted
	}
}

func (m *MCPConfig) ExecutionMode() string {
	switch m.Type {
	case "stdio":
		return "local_mcp"
	default:
		return "remote_mcp"
	}
}
```

这条规则对本地子进程是合理的，但**通过 SSH 拉起的远端进程在本地配置里同样是 `type: stdio`**，
于是会被判定为：

- `MCPTrustLevel = local`（除非显式写 `trustLevel`）；
- `ExecutionMode = local_mcp`（**无法通过配置覆盖**）。

直接后果：

1. `BlockRemoteWrites` 对 SSH stdio 工具完全失效（`ExecutionMode` 不是 `remote_mcp`）；
2. 如果部署者忘记写 `trustLevel`，`BlockUntrustedMCP` 也一并失效；
3. 远端 shell 被当作本地 shell，走本地命令分类与本地审批语义。

因此本方案要求：

- MCP 配置新增显式字段 `executionScope: local | remote`（或等价的 `remoteTarget: <name>`）；
- 只要声明为 `remote`，无论传输是 stdio 还是 streamable，都统一进入远程策略闸门；
- `executionScope` 由宿主配置写入，**不从工具名或工具自述推断**；
- 未声明时保持现有行为（兼容），但 `aicli-remote-tools` 展开出的条目必须显式声明。

#### G6. 输出、artifact、后台作业没有远端等价物

- 本地工具的大输出会落到 session artifact，并通过 `artifact_read` 读取；
- 远端的 `ToolResult` 只是 MCP 文本，没有 artifact 落盘约定；
- 本地 `job_start` / `job_output` 绑定 `internal/background.Manager`，不会自动变成远端作业；
- SSH stdio 模式下，连接断开即进程结束，长任务无法续跑。

第一版接受“同步执行 + 输出上限 + 截断元数据”；P3 再补远端 artifact 与 job。

---

## 3. GitHub 同类方案调研

> 星数为 2026-10-21 通过 GitHub API 查询的近似值，仅用于判断方案成熟度，不代表质量背书。

| 项目 | 星数 | 形态 | 可借鉴点 | 不采用的原因 |
|---|---:|---|---|---|
| [classfang/ssh-mcp-server](https://github.com/classfang/ssh-mcp-server) | ~967 | 本地 MCP → SSH 远端命令/文件；stdio + Streamable HTTP | **与本方案最接近**：双 transport、`exec`/`shell` 模式、命令白/黑名单、SFTP 读写、后台 job、路径白名单、HTTP Bearer token、默认只监听 `127.0.0.1` | Node/TS 实现；凭据与连接在 MCP 进程内管理，我们需要 Go 原生与现有 `sshclient` 复用 |
| [tufantunc/ssh-mcp](https://github.com/tufantunc/ssh-mcp) | ~816 | SSH 控制类 MCP | 工具粒度、连接参数校验 | 只做 SSH 控制面，不解决“远端工具服务 + 工作区” |
| [punkpeye/mcp-remote](https://github.com/punkpeye/mcp-remote) | ~1639 | stdio 客户端 ↔ 远程 MCP 代理 | `--header-file`、transport strategy（http-first/sse-first）、keep-alive、`--ignore-tool`、OAuth/回调、超时分层 | 它是客户端侧代理；我们已有 Streamable HTTP 客户端，不需要再套一层 Node 代理 |
| [sparfenyuk/mcp-proxy](https://github.com/sparfenyuk/mcp-proxy) | ~2798 | Streamable HTTP ↔ stdio 桥 | 双向桥的会话与生命周期处理 | 同上，且会引入额外进程与故障点 |
| [supercorp-ai/supergateway](https://github.com/supercorp-ai/supergateway) | ~2918 | 把 stdio MCP 暴露为 HTTP/SSE | 单进程多 transport、监听地址与安全默认值 | 通用网关，不含工作区/路径 jail/远程目标语义 |
| [mathematic-inc/claude-tools-mcp](https://github.com/mathematic-inc/claude-tools-mcp) | ~17 | Go + MCP SDK，把 bash/read/write/edit/glob/grep 暴露为 HTTP | **stateless HTTP 模式**、路径校验、10MB 文件上限、~100k token 输出上限、`ReadHeaderTimeout`/`IdleTimeout`、优雅关闭 | 没有 SSH 传输、没有 per-target 策略、没有命令审批 |
| [mark3labs/mcp-filesystem-server](https://github.com/mark3labs/mcp-filesystem-server) | ~692 | Go 文件系统 MCP | 路径规范化、目录边界 | 只有文件，不含命令与工作区生命周期 |
| [agent-infra/sandbox](https://github.com/agent-infra/sandbox) | ~6200 | 容器化一体化沙箱（Browser/Shell/File/MCP/VSCode） | 沙箱边界、资源限制思路 | 需要 Docker/容器，内网老服务器不一定具备 |
| [fawdyinc/shellguard](https://github.com/fawdyinc/shellguard) | ~24 | 只读 shell over SSH | **只读命令分类**与最小权限思路 | 能力过窄，无文件/写工具 |
| [SiliCAD/EDA_MCP](https://github.com/SiliCAD/EDA_MCP) | ~30 | 本地 Agent → SSH 远端 Linux 集群 | 行业集群场景的连接与命令封装 | 领域专用，不适合通用运行时 |
| [octelium/octelium](https://github.com/octelium/octelium) | ~4107 | 零信任访问平台 / MCP gateway | 远程访问的认证与策略分层 | 重量级基础设施，远超当前需求 |

### 3.1 调研结论

1. **“MCP over SSH”已有成熟先例**，但多数是“本地 MCP server 持有一组 SSH 凭据”，而不是“远端部署一个工具服务”。
2. **双传输是共识**：`classfang/ssh-mcp-server` 已经用 `--mcp-transport stdio|http` 在同一进程内切换；`supergateway`/`mcp-proxy` 证明 stdio↔HTTP 桥可行。
3. **安全默认值高度一致**：HTTP 只监听 loopback、必须 Bearer token、命令白名单、路径白名单、输出上限、审计。
4. **没有项目同时解决**：per-target 工具命名空间、MCP `_meta` 上下文透传、本地 `BlockRemoteWrites` 与远端策略的联动、远端 workspace 与本地 session 的绑定。这些需要我们自己做。
5. **第一版不需要 OAuth/DCR**：内网场景用 SSH 密钥 + 静态 Bearer 足够；OAuth 会显著增加回调、浏览器、令牌存储复杂度（`mcp-remote` 的 README 本身就是复杂度证明）。

---

## 4. 目标架构

### 4.1 组件图

```text
┌────────────────────────────── 本地电脑 ──────────────────────────────┐
│ aicli TUI / runtime-server                                           │
│                                                                      │
│  Agent Loop ── Tool Surface ── MCP Client                            │
│      │             │              │                                  │
│      │             ├─ 本地工具：shell/view/grep（本地 workspace）     │
│      │             └─ 远端工具：remote_dev__view / __shell ...        │
│      │                            │                                  │
│      │                  ┌─────────┴──────────┐                       │
│      │                  │                    │                       │
│      │            SSH stdio 模式      SSH 本地转发 + HTTP 模式        │
│      │            （按会话拉起）      （常驻 daemon）                  │
│      │                  │                    │                       │
│      │          ssh-client exec      ssh-client -N -L 18765:...       │
│      │                  │                    │                       │
└──────┼──────────────────┼────────────────────┼───────────────────────┘
       │                  │ SSH（密钥/证书、host key、加密、审计）      │
┌──────▼──────────────────▼────────────────────▼───────────────────────┐
│ 目标服务器                                                            │
│                                                                      │
│  aicli-remote-tools                                                  │
│    ├─ stdio 模式：由 sshd 按连接拉起，进程随连接结束                   │
│    └─ http  模式：常驻，监听 127.0.0.1:18765（或 Unix socket）        │
│                                                                      │
│  同一套内部实现：                                                     │
│    Tool Registry（按 profile 裁剪）                                   │
│    Workspace Resolver + Path Jail                                     │
│    Command Policy（allowlist/denylist/审批）                          │
│    Output Budget + Audit + Metrics                                    │
│    Executor（复用 internal/execution / internal/executor）            │
└──────────────────────────────────────────────────────────────────────┘
```

### 4.2 两种部署形态，同一份代码

| 形态 | 目标机状态 | 启动方式 | 适用 |
|---|---|---|---|
| **A. SSH stdio** | 无守护进程，二进制放磁盘即可 | 每个 MCP 连接由 sshd 执行 `aicli-remote-tools serve --stdio ...` | 零运维、临时接入、严格禁止监听端口 |
| **B. HTTP daemon** | 常驻进程（systemd/Windows Service） | `aicli-remote-tools serve --http --listen 127.0.0.1:18765` | 长期使用、多会话并发、后台作业、需要指标 |
| **B'. HTTP + SSH 隧道** | 同 B，但不直接暴露 | 本地 `ssh-client -N -L 18765:127.0.0.1:18765` | **公司内网首选**：不开放端口，仍享受 daemon 能力 |

三者的**工具面、schema、错误码、审计格式必须完全一致**，差异只在传输与进程生命周期。

### 4.3 为什么保留两种传输

- SSH stdio 的优势是**零入站端口**、认证直接复用 sshd、部署最简单；
- 但 stdio 的 MCP 连接生命周期 = 进程生命周期，**不适合长任务、后台作业、多会话共享**；
- HTTP daemon 支持并发、作业、指标、热更新，但需要监听端口与 token；
- 用 SSH 本地转发把两者结合：**端口只绑 loopback，认证仍由 SSH 完成**，是内网场景的最优折中。

### 4.4 与现有 gateway 的关系

`backend/internal/gateway` 是**会话控制面反向代理**（`/web/*`、`/api/runtime/*`），不是工具 RPC 通道。它解决的是“浏览器/移动端访问本机会话”，不解决“本地 Agent 调远端工具”。

只有在目标机**无法被本地主动连接**（例如目标在 NAT 后、只能出站）时，才考虑用 gateway 的出站 WebSocket 隧道承载 MCP。这属于 P3 备选，不作为第一版依赖。

---

## 5. 双传输设计：SSH stdio 与 Streamable HTTP

### 5.1 统一入口

```text
aicli-remote-tools serve --stdio [options]
aicli-remote-tools serve --http  [options]
aicli-remote-tools doctor
aicli-remote-tools tools list
```

公共参数：

```text
--workspace <path>              必填；相对路径解析根，也是默认 jail 根
--allowed-roots <p1,p2>         可选；额外允许访问的绝对路径
--profile readonly|standard|ops 能力档位
--tool-prefix <prefix>          工具名前缀，例如 remote_dev
--config <path>                 YAML 配置
--max-output-bytes <n>          单次工具输出上限，默认 256 KiB
--max-file-bytes <n>            单文件读取上限，默认 2 MiB
--command-timeout <duration>    默认 120s
--audit <path>                  审计 NDJSON 路径
--log-level <level>
```

### 5.2 SSH stdio 模式

目标机侧：

```bash
/usr/local/bin/aicli-remote-tools serve --stdio \
  --workspace /srv/app \
  --profile readonly \
  --tool-prefix remote_dev
```

本地 MCP 配置（`mcp.yaml`）：

```yaml
mcpServers:
  remote-dev:
    type: stdio
    command: ssh-client
    args:
      - "-T"
      - "-o"
      - "ServerAliveInterval=15"
      - "-o"
      - "ServerAliveCountMax=3"
      - "deploy@dev.internal"
      - "/usr/local/bin/aicli-remote-tools"
      - "serve"
      - "--stdio"
      - "--workspace"
      - "/srv/app"
      - "--profile"
      - "readonly"
      - "--tool-prefix"
      - "remote_dev"
    trustLevel: trusted_remote
    executionScope: remote      # 必须显式声明，见 G5b
    timeout: 120s
```

关键要求：

1. **stdout 必须只承载 JSON-RPC**；所有日志、banner、告警写 stderr。
2. **stdin 必须透传**；这是当前 `ssh-client` 的缺口（见 G2）。
3. **禁止交互式 shell 初始化污染 stdout**：远程命令必须是非交互 `exec`，不加载会打印内容的 `.bashrc`；必要时用 `env -i` 或 `sh -c 'exec ...'`。
4. **进程树治理**：连接断开时远端进程必须收到 SIGHUP/EOF 并退出，避免孤儿进程。
5. **不支持断线续跑**：stdio 连接断 = MCP session 结束；需要续跑请用 HTTP 模式。

**参数拼接风险**：`ssh-client` 命令模式当前用 `strings.Join(flags.command, " ")`
（`main.go:319`）把参数拼成一条远端命令字符串，**不做 shell 引号转义**。
如果 `--workspace` 路径含空格、引号或 `$`，会被远端 shell 二次解释。
因此本方案要求：

- 远端路径必须做 POSIX 单引号转义后再拼接；
- 或者给 `ssh-client` 增加 `--exec-argv` 模式，把 `flags.command` 序列化为
  `sh -c` 之外的安全形式（推荐：直接构造 `ssh` 协议 exec 请求，不做本地拼接）；
- 服务端 `--workspace` 在启动时规范化，含空格路径必须在文档中给出显式示例。

优点：

- 目标机不监听端口；
- 认证、加密、host key、审计全部由 sshd 承担；
- 部署只需要拷贝一个二进制；
- 与现有 `ssh-client`、`known_hosts`、`ssh-agent`、证书体系天然兼容。

缺点：

- 每次 MCP 连接一个进程，冷启动有开销；
- 无法跨连接共享缓存/作业；
- 连接断开后长任务丢失；
- 需要修 `ssh-client` 的 stdin 转发与 TTY 行为。

#### 5.2.1 备选载体：系统 OpenSSH

如果目标环境已经强制使用系统 `ssh`（例如公司统一管控的客户端），stdio 配置可以把
`command` 换成 `ssh`，其余参数不变：

```yaml
mcpServers:
  remote-dev:
    type: stdio
    command: ssh
    args:
      - "-T"
      - "deploy@dev.internal"
      - "/usr/local/bin/aicli-remote-tools"
      - "serve"
      - "--stdio"
      - "--workspace"
      - "/srv/app"
```

采用系统 OpenSSH 时必须额外确认三点：

1. `-T` 不分配 PTY，否则 JSON-RPC 流会混入回显与控制字符；
2. `~/.ssh/config` 中的 `LocalCommand`、`PermitLocalCommand`、`ProxyCommand` 不能向
   stdout 输出内容；
3. 首次连接的 host key 提示必须能在非交互环境完成（预置 `known_hosts` 或使用
   `StrictHostKeyChecking=accept-new`），否则 MCP 连接会在握手阶段挂起。

本仓库的 `ssh-client` 仍是首选载体：它已经支持 `known_hosts` 校验、`ssh-agent`、
用户证书、`ProxyCommand` 与保活，且行为可测试；系统 OpenSSH 只作为兼容退路。

### 5.3 Streamable HTTP 模式

目标机侧：

```bash
AICLI_REMOTE_TOKEN='<long-random-token>' \
/usr/local/bin/aicli-remote-tools serve --http \
  --listen 127.0.0.1:18765 \
  --workspace /srv/app \
  --profile readonly \
  --tool-prefix remote_dev \
  --audit /var/log/aicli-remote-tools/audit.ndjson
```

本地（直连，仅在网络可达且已做 TLS/ACL 时）：

```yaml
mcpServers:
  remote-dev:
    type: streamable
    url: https://dev.internal:18765/mcp
    headers:
      Authorization: "Bearer ${AICLI_REMOTE_TOKEN_DEV}"
    trustLevel: trusted_remote
    executionScope: remote
    timeout: 120s
```

> 直连 `https://` 时必须在目标机配置可信证书；公司内网常见的是自签 CA。
> 不要把 `InsecureSkipVerify` 作为默认值——MCP 客户端目前也没有暴露该开关，
> 需要信任内网 CA 时应通过系统证书库或 `SSL_CERT_FILE` 注入。

本地（**推荐**：SSH 本地转发，目标机不暴露端口）：

```powershell
ssh-client -N `
  -L 18765:127.0.0.1:18765 `
  -o ServerAliveInterval=15 `
  -o ServerAliveCountMax=3 `
  deploy@dev.internal
```

```yaml
mcpServers:
  remote-dev:
    type: streamable
    url: http://127.0.0.1:18765/mcp
    headers:
      Authorization: "Bearer ${AICLI_REMOTE_TOKEN_DEV}"
    trustLevel: trusted_remote
    executionScope: remote
    timeout: 120s
```

服务端实现要点：

```go
handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
	return toolkitServer.GetMCPServer()
}, &mcp.StreamableHTTPOptions{
	SessionTimeout: 30 * time.Minute, // 有状态模式，见 5.3.1
})

srv := &http.Server{
	Addr:              listen,
	Handler:           authMiddleware(handler),
	ReadHeaderTimeout: 10 * time.Second,
	IdleTimeout:       120 * time.Second,
}
```

认证中间件：

- 仅接受 `Authorization: Bearer <token>`；
- token 从环境变量或 0600 文件读取，不落命令行、不进日志；
- 常量时间比较；
- 未配置 token 且监听非 loopback → **拒绝启动**（fail-closed）；
- 可选 `--tls-cert` / `--tls-key`；没 TLS 时只允许 loopback。

#### 5.3.1 有状态还是无状态

go-sdk 的 `StreamableHTTPOptions` 提供两种模式（`mcp/streamable.go:130-177`）：

| 模式 | 行为 | 对本项目的含义 |
|---|---|---|
| 默认（有状态） | 维护 `Mcp-Session-Id`，支持服务端向客户端发请求、SSE 续传（需 `EventStore`） | 支持进度通知、取消、未来的 elicitation |
| `Stateless: true` | 不校验 session id，每个请求临时建会话；**服务端不能向客户端发请求** | 更简单，但不能做审批回问、采样与流式进度 |

第一版建议：**用有状态模式 + `SessionTimeout`**，因为远程写/命令的审批回问与进度通知都需要服务端发起请求。

```go
&mcp.StreamableHTTPOptions{
	JSONResponse:   false,          // 保留 SSE，便于流式输出
	SessionTimeout: 30 * time.Minute,
}
```

不要使用 `JSONResponse: true` 作为默认值：它会把多段流合并成单次 JSON，长任务进度与流式输出全部退化为“等结束”。
`mcp_server_test.go:24` 用 `JSONResponse: true` 只是为了让测试简单，不代表生产配置。

另外，SDK 默认启用 **localhost DNS rebinding 防护**（`DisableLocalhostProtection` 默认为 `false`）：
当请求经 loopback 到达但 `Host` 头不是本地地址时会返回 403。这与“只监听 127.0.0.1”叠加，是正确的默认值，
**不要为了省事关掉**。

### 5.4 两种传输的行为一致性契约

| 契约 | 要求 |
|---|---|
| 工具清单 | 同一 profile 下 `tools/list` 的名字、描述、schema hash 完全一致 |
| 工具名前缀 | 两种传输都应用同一个 `--tool-prefix` |
| 错误码 | 同一错误返回同一 `code` / `retriable` / `next_action` |
| 路径解析 | 都由远端 `--workspace` 解析，都不信任客户端绝对路径 |
| 输出上限 | 同一 `--max-output-bytes` |
| 审计字段 | 同一 NDJSON schema，仅 `transport` 字段不同 |
| 取消语义 | stdio：关闭 stdin / 杀进程；HTTP：请求 context cancel |
| 长任务 | stdio：最长受连接存活时间约束；HTTP：可用远端 job（P3） |
| 会话恢复 | stdio：不支持；HTTP：支持 `Mcp-Session-Id` 续连（不跨重连保留工具状态） |

验收时必须做 **transport parity test**：同一组调用分别经 stdio 与 HTTP，结果（除耗时/传输字段）逐字段相等。

**注意**：不要把“两种传输都支持”理解为“功能完全等价”。等价的是**工具语义与安全判定**，
不等价的是**连接生命周期**。这一点必须在文档与 UI 中明确，否则用户会在 stdio 模式下期待断线续跑。

### 5.5 为什么不直接把 `toolkit-mcp-server` 暴露出去

`backend/cmd/toolkit-mcp-server/main.go:23-41` 默认注册 17 个工具，包含：

```text
shell, bash, view, apply_patch, edit, write, append_write, glob, grep, ls,
download, fetch, multiedit, todos, artifact_read, sourcegraph, web_search
```

它没有：

- 工作区绑定与路径 jail；
- 命令白名单/黑名单；
- 输出上限；
- 认证；
- 审计；
- 远端作业；
- 工具前缀。

直接暴露等于把一台服务器的 shell 和文件系统交给任何能连上端口的人。只能作为本地开发调试工具，不能作为远程服务。

---

## 6. 工具命名、上下文与工作区语义

### 6.1 工具命名空间（解决 G4）

**规则**：远端 server 在注册工具时统一加前缀。

```text
remote_<target>__<tool>
```

示例：

```text
remote_dev__view
remote_dev__ls
remote_dev__glob
remote_dev__grep
remote_dev__write
remote_dev__shell
```

实现方式：

- 远端 `--tool-prefix remote_dev` 直接改写 MCP 工具名；
- 本地 `mcpServers.remote-dev` 的 server 名保持 `remote-dev`，用于来源账本与诊断；
- 禁止在本地做“同名时静默覆盖”，增加一条装配期不变量：**远端 MCP 工具名不得与本地内置工具名相同**，违反时 fail-closed 并给出明确错误。

这样：

- 本地 `view` 仍是本地文件；
- `remote_dev__view` 明确是远端；
- 模型从名字即可知道作用域，减少“以为在本地”的错误；
- 多目标时天然隔离：`remote_dev__view`、`remote_prod__view`。

### 6.2 上下文透传（解决 G3）

MCP 的 `_meta` 是传输无关的扩展点。建议字段：

```json
{
  "_meta": {
    "aicli/session_id": "session_...",
    "aicli/turn_id": "turn_...",
    "aicli/tool_call_id": "call_...",
    "aicli/agent_depth": 0,
    "aicli/workspace_id": "remote-dev",
    "aicli/trace_id": "trace_...",
    "aicli/read_only": false
  }
}
```

规则：

- `_meta` 只用于**路由、审计、诊断**；
- **不能**用 `_meta` 里的路径或权限位做授权；
- 远端授权只来自：SSH 登录用户 / HTTP token 绑定的 principal + 服务端配置；
- 本地传 `workspace_id` 只用于选择目标，不用于选择路径根。

需要改动的链路：

```text
toolctx (context.Context)
  → agent/loop 执行工具时构造 meta
  → tools.Manager / skill.MCPAdapter
  → mcp/client.Client.CallToolWithMeta
  → protocol.CallToolParams.Meta
  → MCP wire `_meta`
  → 远端 handler req.Params.Meta
```

### 6.3 工作区语义（解决 G5 之外的路径歧义）

远端 workspace 是**服务端配置**，不是客户端参数：

- `--workspace /srv/app` 是相对路径的解析根；
- `--allowed-roots /srv/app,/var/log/app` 是绝对路径白名单；
- 所有路径先做 `filepath.Clean` + `EvalSymlinks`，再做前缀校验；
- 拒绝 `..` 逃逸、符号链接逃逸、Windows 设备名、UNC 设备路径；
- 远端返回的路径统一为远端视角的绝对路径，并在结果 metadata 标注 `remote_workspace`；
- 本地 **不得** 把远端路径拼到本地 workspace，也不得对远端路径做 stat（现有 `fs_owner=tool_server` 已保证这一点）。

MCP Roots 可以作为**收窄提示**：客户端通过 `roots/list` 告知希望的根，服务端只能在自身配置的允许集合内取交集，不能因客户端声明而扩权。参考 [MCP Roots 规范](https://modelcontextprotocol.io/specification/2025-06-18/client/roots)。第一版可不实现 Roots，直接用服务端 `--workspace`。

### 6.4 工具描述要求

远端工具描述必须包含作用域，避免模型误判：

```text
[remote:dev] 在目标服务器 dev.internal 的 workspace /srv/app 中查看文件。
路径相对 workspace 解析；不是本地文件系统。
```

这段文字会增加 token，但能显著降低“本地/远端混淆”的工具误用。可通过 profile 的 `tool_description_style: compact|explicit` 控制。

---

## 7. 权限与安全模型

### 7.1 身份与认证

| 传输 | 身份来源 | 凭据 | 备注 |
|---|---|---|---|
| SSH stdio | sshd 登录用户 | SSH key / certificate / agent | 不需要额外 token；sshd 负责认证与加密 |
| HTTP 直连 | Bearer token → principal | 环境变量或 0600 文件 | 必须 TLS 或 loopback |
| HTTP + SSH 隧道 | SSH 用户 + Bearer token | 双因子 | 推荐；token 防本机其他用户蹭转发端口 |

不建议第一版做 OAuth：内网无浏览器、无动态注册、增加令牌存储与回调端口，收益低。

### 7.2 授权档位（profile）

| profile | 工具 | 默认 |
|---|---|---|
| `readonly` | `view` `ls` `glob` `grep` | ✅ 第一版默认 |
| `standard` | readonly + `write` `edit` `append_write` `multiedit` `apply_patch` | 需显式 `allow_write: true` |
| `ops` | standard + `shell` + `job_*` | 需显式 `allow_shell: true` + 命令策略 |
| `custom` | 显式工具 allowlist | 用于特殊服务器 |

**默认不暴露**：`fetch`、`download`、`web_search`、`sourcegraph`、`openai_image_generate`、`aicli_exec`、`todos`。

原因：

- 目标机可能无外网，`fetch`/`web_search` 无意义；
- `download` 是写盘 + SSRF 面；
- `aicli_exec` 会在远端再拉起 Agent，语义与权限都不可控；
- `todos` 是会话状态，不应由远端持有。

### 7.3 远程写与远程命令的闸门（解决 G5）

在本地策略层新增 per-target 闸门，而不是全局关闭 `BlockRemoteWrites`：

```go
type ExecutionScope string

const (
	ScopeLocal  ExecutionScope = "local"
	ScopeRemote ExecutionScope = "remote"
)

type RemoteTargetPolicy struct {
	AllowRemoteRead  bool
	AllowRemoteWrite bool
	AllowRemoteShell bool
	CommandPolicy    CommandPolicy // allowlist / denylist / approval
}
```

执行期判定顺序：

```text
deny（显式）
  → allowlist（profile）
  → read_only
  → capability scope
  → executionScope == remote ?
       是 → remote target gate（下表）
       否 → 本地既有判定
  → remote target gate:
       write-like && !AllowRemoteWrite  → 拒绝
       shell-like && !AllowRemoteShell  → 拒绝
  → sandbox
```

远程闸门的判定表：

| 工具类别 | `allow_read` | `allow_write` | `allow_shell` | 默认 |
|---|---|---|---|---|
| 只读（view/ls/glob/grep） | 必须 `true` | — | — | 允许 |
| 写型（write/edit/apply_patch/multiedit/append_write） | 必须 `true` | 必须 `true` | — | 拒绝 |
| 命令（shell/bash/execute_shell_command） | 必须 `true` | — | 必须 `true` | 拒绝 |
| 网络（fetch/download/web_search/sourcegraph） | 默认不注册 | — | — | 不可用 |

**判定依据是 `executionScope`，不是 `ExecutionMode`，也不是工具名。**
这样 stdio-over-SSH 与 streamable-HTTP 走完全同一套闸门，不会出现
“换传输就换权限”的行为差异。

关键修正：

- **`shell` 必须被纳入远程命令闸门**，不能只依赖 `IsWriteLikeToolName`；
- **SSH stdio 必须显式声明 `executionScope: remote`**，否则会被
  `ResolvedTrustLevel()`/`ExecutionMode()` 误判为本地；
- `BlockRemoteWrites` 保持默认 `true`，只有目标配置显式 `allow_write: true` 时，对该 server 的工具构造一个策略副本并只放开写；
- `allow_shell: true` 是独立开关，不与 `allow_write` 合并；
- 任何模型参数都不能修改这些开关（只从宿主配置进入）。

### 7.4 命令执行策略

远端 `shell` 工具至少支持：

| 控制 | 说明 |
|---|---|
| `allowed_commands` | 正则或 argv 前缀 allowlist；为空表示拒绝所有（fail-closed） |
| `denied_commands` | 恒优先 |
| `deny_shell_metachars` | 含 `;` `&` `|` `` ` `` `$()` `>` `<` 换行时拒绝，防拼接绕过 |
| `workdir_jail` | `workdir` 必须落在 workspace / allowed roots 内 |
| `timeout` | 单次命令上限，默认 120s |
| `max_output_bytes` | stdout+stderr 总上限，超限终止并返回 `OUTPUT_LIMIT_EXCEEDED` |
| `env_allowlist` | 只注入白名单环境变量，默认清空 |
| `no_pty` | 默认不分配 PTY，避免交互程序挂起 |
| `approval` | 写命令/危险命令可要求本地审批（P2） |

不要把“命令模板”做成字符串拼接的主路径；优先 argv 化执行，shell 仅作为显式兜底。

### 7.5 审计

每次调用写一行 NDJSON：

```json
{
  "ts": "2026-10-21T10:00:00.123+08:00",
  "principal": "deploy",
  "transport": "stdio",
  "remote_addr": "127.0.0.1",
  "session_id": "session_...",
  "tool_call_id": "call_...",
  "tool": "remote_dev__shell",
  "decision": "allow",
  "workspace": "/srv/app",
  "args_hash": "sha256:...",
  "paths": ["/srv/app/src/main.go"],
  "exit_code": 0,
  "duration_ms": 123,
  "bytes_out": 4096,
  "truncated": false
}
```

不记录完整命令正文与文件内容（防泄露）；需要复盘时记录 hash + 结构化摘要。审计文件必须 `0600`，支持轮转。

### 7.6 资源与稳定性

- 最大并发调用数（HTTP daemon）；
- 单次输出上限与单文件读取上限；
- 命令进程组治理（超时杀整棵树）；
- 请求体大小上限；
- `ReadHeaderTimeout` / `IdleTimeout`；
- 优雅关闭：停止接收新请求，等待在飞调用，超时强杀；
- 速率限制交给 sshd/前置代理，第一版不内置复杂限流。

---

## 8. 配置设计

### 8.1 远端配置（目标机）

```yaml
# /etc/aicli/remote-tools.yaml
workspace: /srv/app
allowedRoots:
  - /srv/app
  - /var/log/app

profile: readonly
toolPrefix: remote_dev

limits:
  maxOutputBytes: 262144
  maxFileBytes: 2097152
  commandTimeout: 120s
  maxConcurrentCalls: 8

shell:
  allowedCommands: []
  deniedCommands:
    - '^\s*rm\s+-rf\s+/'
    - '^\s*(shutdown|reboot|halt|poweroff)\b'
  denyShellMetachars: true
  envAllowlist: [PATH, HOME, LANG, LC_ALL]

http:
  listen: 127.0.0.1:18765
  path: /mcp
  tokenEnv: AICLI_REMOTE_TOKEN
  tlsCert: ""
  tlsKey: ""

audit:
  path: /var/log/aicli-remote-tools/audit.ndjson
  maxSizeMB: 100
  maxBackups: 5
```

命令行参数覆盖配置文件，配置文件覆盖内置默认值。

### 8.2 本地配置（`mcp.yaml` + 目标声明）

现有 `mcpServers` 保持兼容；新增 `remoteTargets` 作为上层声明，由宿主在装配期展开成 MCP 条目与策略：

```yaml
remoteTargets:
  dev:
    transport: ssh-stdio
    ssh:
      host: dev.internal
      user: deploy
      port: 22
      identityFile: ~/.ssh/id_ed25519
      knownHostsFile: ~/.ssh/known_hosts
      serverAliveInterval: 15s
      serverAliveCountMax: 3
    remote:
      binary: /usr/local/bin/aicli-remote-tools
      workspace: /srv/app
      profile: readonly
      toolPrefix: remote_dev
    policy:
      allowRead: true
      allowWrite: false
      allowShell: false

  prod:
    transport: ssh-http
    ssh:
      host: prod.internal
      user: ops
      localForward: 127.0.0.1:18765
    remote:
      listen: 127.0.0.1:18765
      workspace: /srv/app
      profile: ops
      toolPrefix: remote_prod
      tokenEnv: AICLI_REMOTE_TOKEN_PROD
    policy:
      allowRead: true
      allowWrite: true
      allowShell: true
      requireApprovalForWrite: true
      requireApprovalForShell: true
```

展开结果：

```yaml
mcpServers:
  remote-dev:
    type: stdio
    command: ssh-client
    args: [...]
    trustLevel: trusted_remote
    executionScope: remote
  remote-prod:
    type: streamable
    url: http://127.0.0.1:18765/mcp
    headers:
      Authorization: "Bearer ${AICLI_REMOTE_TOKEN_PROD}"
    trustLevel: trusted_remote
    executionScope: remote
```

### 8.3 工具面成本控制

多目标会线性增加工具数量。建议：

- 默认每个目标只开 `readonly` 的 4 个工具；
- 用 profile 的 `tools.allowlist` / `tools.denylist` 裁剪；
- 只在需要时启用 `standard`/`ops`；
- 工具描述使用 compact 模式；
- 在 `/api/runtime/tools` 与 TUI `/tools` 中显示 `surface_source=mcp:remote-dev` 与 `target=dev`，便于解释 token 开销。

---

## 9. 对现有代码的改动清单

### 9.0 复用边界：不重写工具实现

`aicli-remote-tools` 不复制 `internal/toolkit/tools` 的实现，而是复用同一批工具构造器：

```go
allTools := []toolkit.Tool{
	tools.NewViewTool(),
	tools.NewLsTool(),
	tools.NewGlobTool(),
	tools.NewGrepTool(),
	// P2 再按 profile 追加 write/edit/apply_patch/shell
}
```

与 `backend/cmd/toolkit-mcp-server/main.go:23-41` 的差异只有三点：

1. 注册前按 profile 与工具前缀改写名称；
2. 每个工具绑定远端 workspace 与 path jail（`SetBasePath` + 服务端路径校验）；
3. 输出与审计走远端预算层，而不是直接返回。

这样避免出现“第二套 view/grep 实现”，也保证本地与远端同名工具的行为一致；
后续 `internal/toolkit/tools` 的修复会自动被两端继承。

### 9.1 新增

| 路径 | 内容 |
|---|---|
| `backend/cmd/aicli-remote-tools/main.go` | 新二进制入口：`serve` / `doctor` / `tools` |
| `backend/internal/remotetools/server.go` | 双传输服务装配、鉴权、优雅关闭 |
| `backend/internal/remotetools/registry.go` | 工具注册与 prefix 改写 |
| `backend/internal/remotetools/workspace.go` | workspace 解析、path jail、symlink 校验 |
| `backend/internal/remotetools/command_policy.go` | 命令 allowlist/denylist、元字符拒绝、超时 |
| `backend/internal/remotetools/audit.go` | NDJSON 审计与轮转 |
| `backend/internal/remotetools/limits.go` | 输出/文件/并发上限 |
| `backend/internal/remotetools/transport_stdio.go` | stdio 生命周期、stdin EOF、退出码 |
| `backend/internal/remotetools/transport_http.go` | Streamable HTTP handler、Bearer、TLS |
| `backend/internal/remotetools/config.go` | YAML + 参数解析 |
| 对应 `*_test.go` | 见 §10.3 |

### 9.2 修改

| 文件 | 改动 | 原因 |
|---|---|---|
| `backend/internal/mcp/server/server.go` | `BaseServer.Start` 支持 `http.Handler`/Streamable HTTP；新增 `Handler()` | G1：HTTP 模式 |
| `backend/internal/mcp/config/types.go` | 新增 `ExecutionScope`（`local`/`remote`），进入 `MCPStatus` 与工具信息 | G5b：stdio-over-SSH 误判 |
| `backend/internal/mcp/protocol/message.go` | `CallToolParams` 增加 `Meta map[string]any` | G3：上下文透传 |
| `backend/internal/mcp/client/client.go` | 新增 `CallToolWithMeta`；保留 `CallTool` 兼容 | G3：透传链路 |
| `backend/internal/mcp/manager/manager.go` | 调用链透传 meta | G3 |
| `backend/internal/skill/mcp_adapter.go` | `CallToolWithMeta` 透传；工具名/来源标注 | G3/G4 |
| `backend/internal/tools/manager.go` | 远端工具不得覆盖本地工具；装配期不变量 | G4：命名冲突 |
| `backend/internal/policy/tool_policy.go` | 新增 remote target gate；shell 纳入远程命令闸门 | G5 |
| `backend/internal/agent/tool_surface_binding.go` | eligibility key 纳入 remote target / transport | 工具面变更可解释 |
| `backend/internal/toolregistry/registry.go` | 无需为远端工具登记；但需明确 `IsShellLike` 在远程 gate 中的使用 | G5 |
| `backend/cmd/ssh-client/main.go` | 命令模式设置 `session.Stdin = os.Stdin`；新增 `--stdio`/`-T` 明确语义 | G2 |
| `backend/internal/sshclient/session.go` | `RunCommand` 增加 stdin 变体或 `RunCommandIO` | G2 |
| `backend/cmd/toolkit-mcp-server/main.go` | 不改；保持本地调试兼容 | 兼容性 |
| `docs/user-guide/README.md` | 新增工具条目 | 文档 |

### 9.3 关键代码草图

#### 9.3.1 `ssh-client` 命令模式 stdin 透传

```go
// backend/internal/sshclient/session.go
func RunCommandIO(ctx context.Context, client *ssh.Client, command string,
	stdin io.Reader, wOut, wErr io.Writer) (int, error) {

	session, err := client.NewSession()
	if err != nil {
		return 255, fmt.Errorf("open session: %w", err)
	}
	defer session.Close()

	session.Stdin = stdin
	session.Stdout = wOut
	session.Stderr = wErr

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			client.Close()
		case <-done:
		}
	}()

	if err := session.Run(command); err != nil {
		var exitErr *ssh.ExitError
		if IsExitError(err, &exitErr) {
			return exitErr.ExitStatus(), nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 255, fmt.Errorf("run command interrupted: %w", ctxErr)
		}
		return 255, fmt.Errorf("run command: %w", err)
	}
	return 0, nil
}
```

`main.go` 命令分支改用 `RunCommandIO(ctx, sshConn, cmdStr, os.Stdin, os.Stdout, os.Stderr)`。

> 注意：`ssh.Session.Run(cmd)` 会把 `cmd` 作为**单条字符串**交给远端 shell 解释，
> 因此即使 stdin 修好了，参数拼接问题仍在。更彻底的修法是提供 argv 形态：
>
> ```go
> // 远端侧需要一个不接受 shell 解释的入口，例如：
> //   aicli-remote-tools serve --stdio --workspace=/srv/app
> // 本地侧把 argv 逐项 quote 后再交给 ssh：
> cmd := shellQuoteJoin(argv)
> ```
>
> 或者在 `aicli-remote-tools` 侧支持 `--config-b64 <base64>`，
> 由本地把结构化配置编码后传入，避免路径中的空格/引号进入 shell 语法层。
> 第一版推荐 `--config-b64`：它同时解决“参数过长”和“转义错误”两个问题。

#### 9.3.2 MCP 客户端 meta 透传

```go
// backend/internal/mcp/protocol/message.go
type CallToolParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
	Meta      map[string]any         `json:"_meta,omitempty"`
}
```

```go
// backend/internal/mcp/client/client.go
func (c *mcpClient) CallToolWithMeta(ctx context.Context, name string,
	args map[string]interface{}, meta map[string]any) (*protocol.CallToolResult, error) {
	return c.callTool(ctx, name, args, meta)
}
```

服务端读取：

```go
mcp.AddTool(server, tool, func(ctx context.Context, req *mcp.CallToolRequest,
	args map[string]interface{}) (*mcp.CallToolResult, any, error) {

	var meta map[string]any
	if req != nil && req.Params != nil {
		meta = req.Params.Meta
	}
	_ = meta // 只用于审计/路由，不用于授权
	...
})
```

#### 9.3.3 Streamable HTTP handler

```go
// backend/internal/remotetools/transport_http.go
func (s *Server) httpHandler() http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return s.mcpServer.GetMCPServer()
	}, &mcp.StreamableHTTPOptions{JSONResponse: true})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorize(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="aicli-remote-tools"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	})
}
```

#### 9.3.4 装配期冲突不变量

```go
// backend/internal/tools/manager.go
func (m *Manager) assertNoRemoteShadowing(remoteTools []ToolDescriptor) error {
	local := map[string]struct{}{}
	if m.toolkit != nil {
		for _, tool := range m.toolkit.List() {
			local[strings.ToLower(tool.Name())] = struct{}{}
		}
	}
	for _, tool := range remoteTools {
		if _, exists := local[strings.ToLower(tool.Name)]; exists {
			return fmt.Errorf(
				"remote tool %q shadows a local builtin; configure a distinct tool prefix",
				tool.Name)
		}
	}
	return nil
}
```

> 注意：这条不变量只对“远端 MCP 工具”生效，不改变现有 `toolkit` 本地 MCP 的优先级语义。

---

## 10. 实施阶段与验收

### 10.1 阶段划分

| 阶段 | 内容 | 预估 | 可独立交付 |
|---|---|---|---|
| **P0** | 管道修复：`ssh-client` stdin、MCP `_meta`、`executionScope`、HTTP handler 抽象 | 1–2 天 | 是 |
| **P1** | `aicli-remote-tools` 只读版：`view/ls/glob/grep`、双传输、prefix、path jail、token、审计 | 3–5 天 | 是 |
| **P2** | 写与命令：`write/edit/apply_patch`、`shell`、per-target gate、命令策略、审批 | 3–5 天 | 是 |
| **P3** | 运维能力：后台 job、artifact、多目标、重连、指标、日志轮转 | 5–8 天 | 是 |
| **P4** | 可选：gateway 反向隧道、mTLS/OAuth、MCP Roots、Windows 目标 | 按需 | 否 |

### 10.2 P1 最小可用范围

必须包含：

- `serve --stdio` 与 `serve --http`；
- 只读 4 工具；
- `--workspace` + `--allowed-roots` + path jail；
- `--tool-prefix`；
- HTTP Bearer token + loopback 默认；
- 输出/文件上限；
- 审计 NDJSON；
- `doctor` 自检（workspace 可读、token 已配置、审计可写、工具列表）。

明确不包含：

- 写文件；
- shell；
- 远端 job；
- 多目标并发；
- OAuth；
- Windows 目标。

### 10.3 测试矩阵

#### 10.3.1 真实环境端到端验证（WSL2 / Linux）

单元测试使用内存传输，无法证明真实进程、真实 HTTP、真实 SSH 通道下的行为。以下脚本补齐这一层，全部在 **WSL2 Ubuntu + Windows 主机**上实测通过。

| 脚本 | 覆盖 | 实测结果 |
|---|---|---|
| `backend/scripts/remotetools_e2e.go` | 真实 Streamable HTTP：tools/list、view/ls/glob/grep、路径 jail、write/append/edit/apply_patch、shell 白名单与拒绝、job 生命周期、artifact 分页、metrics | **21 项断言全 PASS**（Linux 侧与 Windows 侧各跑一次） |
| `backend/scripts/sshstdio_e2e.go` | 真实 SSH exec 通道承载 stdio MCP：进程内 sshd → `serve --stdio` → 工具调用 | **3 项断言全 PASS** |
| `backend/scripts/targetexpand_e2e.go` | 真实 YAML `remoteTargets` 展开：ssh-stdio / ssh-http / http 三种 transport、策略、子代理授权 | PASS |
| `backend/scripts/remote_task_conpty.go` | **真实 ConPTY + 真实模型**：在 PTY 里跑 `aicli chat`，由模型自主读源码、跑测试、改远端文件 | **PASS**（模型 1m37s 完成，远端 4 项测试全绿） |
| `backend/scripts/gatecheck.go` | 真实 `remote-targets.yaml` → 展开 → 权限判定，逐工具打印 ALLOW/DENY | 允许与拒绝两个方向均 PASS |
| `backend/scripts/remotetools_probe.go` | 裸 MCP 协议探测单个命令是否被目标策略接受（不经过模型） | PASS |
| `backend/scripts/remote_verify_only.go` | 在目标机上单独重跑验收脚本（模型回合结束后仍可复核） | PASS |
| `backend/scripts/ssh_transports_e2e.go` | **真实 `ssh-client` 二进制**驱动两种 transport：ssh-stdio（exec 通道）与 ssh-http（`-N -L` 本地转发） | **ALL PASS** |
| `backend/scripts/pflag_probe.go` | 复刻 `ssh-client` 的 flag 解析，观察 `--` 分隔符去向 | 定位 L3 的工具 |

运行方式（三个脚本均带 `//go:build ignore`，不参与常规构建）：

```bash
# 1) 在 WSL 内构建 Linux 二进制
wsl -d Ubuntu -- bash -lc 'cd /mnt/e/.../backend && go build -o /tmp/aicli-rt/aicli-remote-tools ./cmd/aicli-remote-tools'

# 2) 起服务端（真实 Linux 进程）
AICLI_REMOTE_TOKEN=<32字节以上的随机值> /tmp/aicli-rt/aicli-remote-tools serve --http --config /tmp/aicli-rt/config.yaml

# 3) 跑端到端
wsl -d Ubuntu -- bash -lc 'cd /mnt/e/.../backend && go run ./scripts/remotetools_e2e.go -url http://127.0.0.1:18765/mcp -token <token> -prefix remote_wsl'
```

**实测确认的事实**：

- **跨操作系统可用**：Windows 侧客户端经 WSL2 localhost 转发调用 Linux 服务端，21 项断言全过（`ExecutionScope=remote` 的工具在两端行为一致）。
- **SSH stdio 真实可用**：SSH exec 通道承载 JSON-RPC，`view` 与 jail 均正确；`--config-b64` 让含空格的 workspace 不经 shell 解析。
- **mTLS 生效**：OpenSSL 生成 CA + 服务端/客户端证书后，无客户端证书握手失败（curl `pct=000`），带证书返回 `200` 且**无需 Bearer token**。
- **审计不泄露内容**：35 条 NDJSON 记录，权限 `600`，grep 文件正文关键字命中数为 **0**。
- **指标准确**：`callsTotal=35 / callsDenied=7`，per-tool 覆盖 13 个工具。
- **能力开关有效**：未配置 `jobs.root` / `artifacts.root` 时，工具面从 14 个降为 9 个，`job_*` 与 `artifact_read` 完全不暴露。
- **fail-closed 生效**：`profile: ops` 但 `allowedCommands` 为空时，`doctor` 给出警告，shell 拒绝一切命令。

##### 真实任务场景：ConPTY + 模型自主调用远程工具

前面几个脚本是**脚本驱动**的（脚本自己构造 JSON-RPC）。它们证明了协议正确，但不能证明"模型在真实会话里会用这些工具完成工作"。为此补一个**无人代劳**的场景：

**环境隔离**：Windows 侧工作区只有 `.aicli/mcp.yaml` 和 `.aicli/remote-targets.yaml`，**没有任何源码**。待修代码只存在于 WSL 的 `/tmp/aicli-rt/ws/proj`。模型无法用本地工具蒙对答案——它必须真的走到远端。

**任务**：修好 `orders.py` 里三处真实缺陷（float 累加、税率舍入、Decimal 无法 JSON 序列化），使 4 个契约测试全绿；`test_orders.py` 不许改。

**驱动做的事**：开 ConPTY → 跑 `aicli chat` → 把任务描述写进真实行编辑器（CR 提交）→ 检测审批提示并按键同意 → 退出。**它不看模型的自述**，最终裁决直接去 WSL 上跑 `python3 -m unittest` 并读 `orders.py` 源码。

实测输出（2026-10-21）：

```
[driver] PASS TUI ready
[driver] task submitted (515 chars)
[driver] approved interactive prompt #1   ← remote_wsl__shell
[driver] approved interactive prompt #2   ← remote_wsl__shell
[driver] turn ended after 1m37s
[verify] Ran 4 tests ... OK
[verify] PASS remote file fixed and remote tests green
[driver] REMOTE TASK PASSED
```

模型的实际工具序列（来自 transcript，未经人工干预）：

```
remote_wsl__ls        → 看到 orders.py / test_orders.py / TASK.md
remote_wsl__view      → 读 orders.py
remote_wsl__view      → 读 test_orders.py
remote_wsl__shell     → python3 -m unittest -v（先失败，确认基线）
remote_wsl__view      → 读 TASK.md
remote_wsl__artifact_read → 被折叠的长输出（工具提示"已过期/重跑"）
remote_wsl__shell     → python3 --version（自检环境）
remote_wsl__shell     → 带过参数的 unittest（仍失败）
remote_wsl__job_start → 转用后台作业跑测试
remote_wsl__job_output→ 拿到完整输出
remote_wsl__apply_patch → 真正修改远端文件（成功）
remote_wsl__job_start → 复跑测试
remote_wsl__job_output→ 全绿
remote_wsl__view      → 复核改动
```

这个场景的价值不在"跑通了"，而在于它**暴露了两个纯单测无法发现的问题**（已修复，见 §10.4）。

##### 最严格场景：远端从零安装工具链并交付一个可运行站点

上一个场景的源码是我预先放好的，严格来说仍有"本地准备"的成分。为了排除这一点，做了一次**零预置**验证：

| 项 | 状态 |
|---|---|
| Windows 工作区 | 只有 `.aicli/mcp.yaml` + `.aicli/remote-targets.yaml`，**零源码** |
| 远端工作区 `/tmp/aicli-rt2/ws/deno-site` | **空目录** |
| 远端 Deno | **未安装**（`which deno` → 空） |
| 任务文件 `TASK.md` | 放在工作区之外，模型需用 `remote_wsl__view` 主动读取 |

任务：**在远端从零装好 Deno，写一个零依赖网站，跑起来并自测**。本地没有做过任何准备工作。

远端命令白名单刻意收得很窄，用来证明"安装也走远程工具"这件事可行且受控：

```yaml
shell:
  allowedCommands: [curl, python3, mkdir, mv, cp, chmod, deno, ls, cat, sleep]
  deniedCommands:  [rm, sh, bash]
  denyShellMetachars: true      # 管道/重定向/; /&&/$() 一律拒绝（引号内除外）
```

模型实际执行的序列（transcript 原文，**全部是 `remote_wsl__*`**）：

```
remote_wsl__ls        → 工作区为空
remote_wsl__shell     → curl -fsSL -o .../release-latest.txt https://dl.deno.land/release-latest.txt
remote_wsl__shell     → cat release-latest.txt            → v2.9.7
remote_wsl__shell     → curl -fsSL -o .../deno.zip https://dl.deno.land/release/v2.9.7/deno-x86_64-unknown-linux-gnu.zip
remote_wsl__shell     → mkdir -p .../bin
remote_wsl__shell     → python3 -c "import zipfile; ..."   ← 远端没有 unzip
remote_wsl__shell     → chmod 755 .../bin/deno
remote_wsl__shell     → .../bin/deno --version             → deno 2.9.7
remote_wsl__write     → server.ts   (4247 字节)
remote_wsl__write     → README.md   (1680 字节)
remote_wsl__job_start → deno run --allow-net=127.0.0.1:8787 server.ts
remote_wsl__job_output→ 启动日志
remote_wsl__shell     → curl -fsS http://127.0.0.1:8787/          → HTML
remote_wsl__shell     → curl -fsS .../api/health                  → JSON
remote_wsl__shell     → curl -fsS .../api/time                    → JSON
remote_wsl__shell     → curl -w "root HTTP %{http_code}"          → 200 ×3
remote_wsl__shell     → cat server.ts                             ← 自查
remote_wsl__grep      → 确认没有 jsr:/npm:/https: import
```

**验收结果**（目标机侧脚本，`remote_verify_only.go` 独立复核）：

```
== layout ==
present: server.ts (4247 bytes)
present: README.md (1680 bytes)
present: bin/deno (95830104 bytes)
== deno binary ==
deno executable: deno 2.9.7 (stable, release, x86_64-unknown-linux-gnu)
== no third-party imports ==
server.ts has no remote imports
== runtime endpoints ==
GET /api/health -> {"status":"ok","runtime":"deno","version":"2.9.7"}
GET /api/time   -> {"now":"2026-10-21T08:33:51.438Z","uptimeSeconds":144.787}
GET /           -> <!DOCTYPE html> ... <title>Deno Remote Site</title>
== server process ==
server is live on 127.0.0.1:8787
VERIFY_OK
```

进程与监听确认（这才是"真的在跑"）：

```
$ ps -eo pid,ppid,etime,args | grep "[d]eno run"
 1209    1208   03:02 /tmp/aicli-rt2/ws/deno-site/bin/deno run --allow-net=127.0.0.1:8787 .../server.ts
$ ss -ltnp | grep 8787
LISTEN 0 511 127.0.0.1:8787 0.0.0.0:*  users:(("deno",pid=1209))
```

**本地零残留**（证明代码没有"先在本机写好再传过去"）：

```
$ Get-ChildItem .tmp\rt2\ws -Recurse -Force
  .aicli\mcp.yaml
  .aicli\remote-targets.yaml
  aicli-pipe-debug.log          ← 驱动进程自身产生的空日志
```

审计账本也印证了工具分布：`shell ×22`、`ls ×2`、`write ×2`、`job_output ×2`、`job_start ×1`、`grep ×1` —— 共 30 条，全部落在远端目标。

##### 真实部署场景：已有服务从 1.0.0 升级到 2.0.0

这是运维最典型的用法。目标机预置了一个"已经在跑"的生产布局（release 目录 + 软链切换）：

```
<deploy-root>/
  releases/1.0.0/svc           ← 当前生产版本，进程在跑
  current -> releases/1.0.0
  svc -> current/svc
  VERSION                      1.0.0
  start.sh / stop.sh / status.sh
```

新产物由 CI 放到暂存目录。**`remote_wsl__write` 只能写文本，传不了二进制**，所以产物必须带外送达 —— 这是本方案的一条硬边界，prompt 必须写清。

任务里埋了一个真实的坑：**2.0.0 的内置默认端口从 8799 改成了 8800**。如果 agent 只是"重启服务"，端口会漂移，健康检查就挂了。prompt 明确要求"必须显式传 `--port 8799`"。

模型执行的关键步骤（transcript 原文）：

```
cp /tmp/aicli-rt4-incoming/svc-v2 <root>/releases/2.0.0/svc
sha256sum releases/2.0.0/svc          → 与 CI 期望值一致
ln -s releases/2.0.0 current.v2
mv -T current.v2 current              ← 原子切换，避免 current 短暂消失
python3 -c "open('VERSION','w').write('2.0.0')"
./stop.sh && ./start.sh
curl http://127.0.0.1:8799/healthz    → {"version":"2.0.0",...}
curl http://127.0.0.1:8799/metrics    → svc_requests_total（2.0.0 独有）
ss -ltn                               → 确认只监听 8799，没有跑到 8800
readlink -f current svc               → 确认软链一致性
sha256sum releases/1.0.0/svc releases/2.0.0/svc  ← 确认旧版本未被覆盖
```

**验收结果**（目标机侧脚本，15 项断言）：

```
== deployed version ==     VERSION=2.0.0
== release layout ==       releases/2.0.0/svc 存在；1.0.0 保留；current → releases/2.0.0
== artifact integrity ==   sha256 与 CI 产物一致
== the OLD release ==      1.0.0 的 sha256 仍不同（未被覆盖）
== process ==              running pid=1788
== listening port ==       8799=1，8800=0（端口没有漂移）
== runtime endpoints ==    healthz version=2.0.0；metrics 存在
VERIFY_OK
```

**回滚实测**：把 `current` 切回 1.0.0 并重启后，`healthz` 返回 `{"version":"1.0.0"}`，回滚路径真实可用。

不过这次手测也暴露了一个**部署脚本自身的坑**：回滚后 `VERSION` 文件仍写着 `2.0.0`，与实际运行的 `1.0.0` 不一致。这说明 `VERSION` 是独立于软链的第二份状态，回滚时必须一起改。凡是"多份状态描述同一件事"的部署布局，都要在 prompt 里点名让 agent 保持一致。

**关于审批**：这个场景里模型被执行了十几次审批交互，每次都消耗一轮完整的 UI 往返。结论不是"审批有问题"，而是**部署类任务不适合逐条交互审批**：应当用 `--allow-tool "remote_wsl__*"` 预授权工具面，或为运维任务单独配一个 `dont_ask` 的 profile，把人工确认放在"部署前 / 部署后"两个节点，而不是每条 `readlink` 都问一次。

| 层级 | 用例 | 断言 |
|---|---|---|
| 单元 | 路径 jail | `..`、绝对路径、symlink 逃逸、Windows 设备名全部拒绝 |
| 单元 | 命令策略 | allowlist 命中、denylist 优先、元字符拒绝、超时 |
| 单元 | 工具前缀 | 所有工具名带前缀；schema 不变 |
| 单元 | 输出上限 | 超限截断、`truncated=true`、`next_action` 可续读 |
| 单元 | Bearer | 缺失/错误/正确；非 loopback 无 token 拒绝启动 |
| 集成 | SSH stdio | 用 `x/crypto/ssh` 起测试 sshd，MCP client 走 stdio 完成 `tools/list` + `view` |
| 集成 | HTTP | `httptest.NewServer(mcp.NewStreamableHTTPHandler(...))`，完成同样调用 |
| 集成 | **parity** | 同一 profile 下 stdio 与 HTTP 的工具清单、schema hash、错误码逐字段相等 |
| 集成 | 断连 | stdio 关闭 stdin 后远端进程退出；HTTP cancel 后无泄漏 goroutine |
| E2E | Linux 容器 | sshd + 远端二进制 + 本地 `aicli` 真实调用 |
| E2E | Windows 本地 | `ssh-client` stdin 透传、CRLF、PowerShell 路径 |
| 负向 | 策略 | 远程 `write` 默认拒绝；`allow_write=true` 后放行；`shell` 同理 |
| 负向 | 冲突 | 远端 `view` 与本地 `view` 同名时装配期 fail-closed |
| 负向 | scope | SSH stdio 未声明 `executionScope` 时，带 `--remote` 的启动自检报错；显式声明后与 HTTP 走同一闸门 |

### 10.4 验收标准

1. 本地 `view` 与 `remote_dev__view` 可同时存在，调用互不串台。
2. 同一 profile 下，SSH stdio 与 HTTP 的 `tools/list` 完全一致（名称、描述、schema hash）。
3. 目标机在 HTTP 模式下**只监听 127.0.0.1**；无 token 时拒绝启动非 loopback。
4. 远端 `write`/`shell` 默认被本地策略拒绝；只有 per-target 显式开启后才可执行。
5. SSH stdio 与 HTTP 两种模式下，同一 target 的写/命令判定结果完全一致。
6. 远端路径逃逸 100% 拒绝，且有稳定错误码。
7. 单次输出超限不会把整段内容塞进模型上下文。
8. 审计可按 `session_id` / `tool_call_id` / `target` 检索。
9. 断开 SSH 后，本地得到明确 `REMOTE_UNAVAILABLE`，不是无限等待。

### 10.5 真实场景暴露并修复的缺陷

这两个缺陷都在**单元测试全绿**的情况下存在，只有把真实模型放进真实 ConPTY 才暴露。

| # | 缺陷 | 现象 | 根因 | 修复 |
|---|---|---|---|---|
| **L1** | `remoteTargets` 声明从未接入 `aicli chat` | 每次远程写/执行都被拒：`remote target "wsl" is not configured for this session`。整条链路只能靠 `mcp.yaml` 连上服务，但**无法被授权** | `internal/remotetargets` 只有库与测试，`go list -deps ./cmd/aicli` 里没有它。`buildLocalChatToolPolicy` 只从工具面合成 allowlist，没人把 `remote-targets.yaml` 的 grant 写进 `ToolExecutionPolicy.RemoteTargets` | 新增 `cmd/aicli/commands/chat_remote_targets.go`：按工作区读声明 → 展开 → 落到策略；并清掉 `BlockRemoteWrites`/`BlockRemoteShell` 这两个默认闸（否则声明了 `allowWrite` 也会被闸掉）。**nil 声明不清闸**，避免缺失文件反而放宽权限 |
| **L2** | `job_start` / `job_input` 绕过 `allowShell` | 目标声明 `allowShell: false`，`shell` 被正确拒绝，但 `remote_wsl__job_start` **照样能执行任意命令** | 门禁按 `IsShellLikeToolName` 判定，该函数是子串启发式（`shell`/`bash`/`exec`）。`job_start` 三个标记一个都不含，被判成"只读工具"。`job_start` 本质就是后台 shell | 新增 `toolWantsRemoteShell`：先走原启发式，再取 `__` 之后的裸工具名匹配 `job_start`/`job_input`。两种命名拼写（`remote_x__job_start`、`mcp__x__job_start`）归一到同一判定，改拼写绕不过去 |

> 这两个是**产品缺陷**。下面一条是**测试驱动自身的缺陷**，记在这里是因为它一度把成功的场景判成失败，很容易被误读成产品问题。

| # | 缺陷 | 现象 | 根因 | 修复 |
|---|---|---|---|---|
| **T1** | 验收脚本被二次解析 | 模型明明已经把站点跑起来，验收却输出 `stat: cannot statx '"/"'` / `command not found: ""` / `[: : integer expression expected`，判为 FAIL | 驱动用 `bash -lc "<script>"` 把整段脚本当成一个命令行参数传给 WSL；该 WSL 的登录 shell 是 **zsh**，脚本先被 zsh 解析一遍，`$(...)`、`$*`、引号被重写 | 改为 `bash -s` 从 **stdin** 读脚本。脚本正文不再出现在任何命令行里，只有一个解析器能看到它 |

| # | 缺陷 | 现象 | 根因 | 修复 |
|---|---|---|---|---|
| **L3** | `ssh-stdio` 目标**完全不可用**（产品缺陷） | 用真实 `ssh-client` 连接时远端立刻报 `/bin/sh: 0: Illegal option --`，MCP 握手超时 | `remotetargets` 为 ssh-stdio 生成的 argv 是 `ssh-client -T host -- <binary> serve --stdio --config-b64 <b64>`；而 `ssh-client` 为了让远端程序的 `--stdio` 不被本地解析，调用了 `pflag.SetInterspersed(false)`。该开关同时**关闭了 pflag 对 `--` 结束符的消费**，`--` 原样留在操作数列表里，最终成为远端命令串的第一个 token。`sh -c` 把 `--` 当成自己的选项，直接报 `Illegal option --` | `parseFlags` 在取到 `command` 后剥掉**开头的** `--`（只剥一个，保留命令内部的字面 `--`）。补 4 个回归测试：带分隔符、远端 flag 不被本地解析、普通命令不受影响、只剥前导 |

**L2 的影响是真实的提权**：一个"可读可写、禁止执行"的目标，本应只能改文件，实际却能启动任意进程——`AllowShell` 这个开关形同虚设。

两处均有回归测试钉死：

- `internal/policy/remote_job_gate_test.go` —— 4 个用例：无 shell 时拒绝（含 `mcp__` 拼写）、有 shell 时放行、只读 job 工具不受影响、未声明目标仍 fail-closed。
- `cmd/aicli/commands/chat_remote_targets_test.go` —— 5 个用例：真实 YAML 解析、无文件不发明授权、默认闸被正确清除、nil 声明不清闸、parse 出来的 grant 真的参与判定。
- `cmd/ssh-client/args_dashdash_test.go` —— 4 个用例：`--` 被剥离、远端 flag 不被本地解析、普通命令不变、只剥前导一个。

并在真实配置上做了双向验证（`scripts/gatecheck.go`）：

```
# allowShell: true
shell ALLOW  job_start ALLOW  job_input ALLOW

# allowShell: false
shell      DENY  (remote target "wsl" does not allow shell tool: remote_wsl__shell)
job_start  DENY  (remote target "wsl" does not allow shell tool: remote_wsl__job_start)
job_input  DENY  (remote target "wsl" does not allow shell tool: remote_wsl__job_input)
job_output ALLOW  job_list ALLOW  job_cancel ALLOW
```

> L1 的教训：**库写好≠接线完成**。P4 阶段为 `remoteTargets` 写了 12 个单测，全部通过，但它没有任何生产调用方——单测验证的是一段无人使用的代码。

---

## 11. 风险与备选方案

| 风险 | 影响 | 缓解 |
|---|---|---|
| SSH stdio 每次冷启动 | 首次工具调用慢 | 二进制做小、懒加载；高频场景切 HTTP daemon |
| stdio 连接断开丢长任务 | 构建/测试中断 | P3 远端 job；或直接用 HTTP + SSH 隧道 |
| 远端进程无人回收 | 目标机资源泄漏 | 远端监听 stdin EOF + 父进程退出；进程组治理；systemd `KillMode=control-group` |
| SSH stdio 被误判为本地 | 远程写/命令绕过闸门 | 强制 `executionScope: remote`；`--target remote` 启动自检；装配期不变量 |
| 同名工具被覆盖 | 误操作到错误机器 | 强制 prefix + 装配期 fail-closed |
| 远程 shell 过权 | 目标机被破坏 | 默认关闭；allowlist；审批；审计；只读命令分类 |
| 输出过大 | 上下文爆炸 | 服务端硬上限 + 本地 artifact 约定 |
| 多目标工具面膨胀 | token 成本上升 | 默认只读 4 工具；profile 裁剪；compact 描述 |
| 目标机无 `rg`/`git` | 工具降级 | 明确探测与降级；grep 内置 Go 实现可作为 fallback |
| Windows 目标路径/引号差异 | 命令失败 | P4 单独适配；第一版仅 Linux |
| 审计泄露敏感信息 | 合规风险 | 只记 hash 与结构化摘要；0600；轮转 |
| SSH banner 污染 stdout | MCP 解析失败 | 服务端日志强制 stderr；sshd `Banner none`；启动自检 |

### 11.1 备选方案 A：纯 SSH 薄封装（不做远端二进制）

新增 `remote_read` / `remote_shell` 两个本地工具，内部用 `internal/sshclient` + SFTP。

- 优点：零部署、最快验证；
- 缺点：没有工具发现、没有 schema、没有统一错误码、没有远端策略、扩展性差；
- 结论：只作为 P0 的链路验证，不作为目标架构。

### 11.2 备选方案 B：在目标机跑完整 `aicli exec`

- 优点：能力最全，远端可自治；
- 缺点：目标机需要模型访问；权限/审批/会话跨机；部署重；
- 结论：仅当目标机能访问模型且需要长时自治时考虑，属于 Remote Agent，不是本方案。

### 11.3 备选方案 C：gateway 反向隧道承载 MCP

- 优点：目标机只需出站，适合 NAT；
- 缺点：gateway 当前是 HTTP 反向代理，不是为长连接 MCP 设计；需要新增路由、能力位、会话粘滞；
- 结论：P3 评估，不阻塞第一版。

#### 11.3.1 P4 评估结论：不采用 gateway，改用 ssh -R

核对代码后，gateway 承载 MCP 有**三个结构性障碍**，不是"补一个路由"能解决的：

| 障碍 | 证据 | 为什么是结构性的 |
|---|---|---|
| 鉴权模型不匹配 | `cmd/gateway/main.go:738-745` 的 `/n/{node_id}/` 用 `owner.RequireSession`（浏览器 Cookie） | MCP 客户端只有 Bearer/证书，没有 owner 会话；要么给 MCP 开一条绕过 owner 的旁路（安全回退），要么让客户端模拟浏览器登录（脆弱） |
| 反向代理是一次请求-响应 | `internal/gateway/agent/forward.go:140-212` 的 `forwardStream` 读完 `resp.Body` 就 `writeStreamEnd` | MCP Streamable HTTP 需要长时间挂起的 SSE 流与双向 POST；代理层按"读完即结束"工作，会截断流 |
| 路由白名单是路径前缀 | `internal/gateway/tunnel/proxy.go:152-170` 的 `routeAllowed` | 它能放行 `/mcp`，但放行之后没有工具级授权概念；远程写/命令的 per-target 闸门无法在网关侧表达 |

**替代方案（推荐）**：`ssh -R` 反向端口转发。

```bash
# 在目标机上（只需出站 SSH）
ssh -N -R 18765:127.0.0.1:18765 user@jump-host

# 本地（或 jump-host）连到转发出来的端口
# mcp.yaml: url: http://127.0.0.1:18765/mcp
```

优势：

- 复用已实现的 SSH 认证、host key、重连（`ssh-client -N -R` 已有僵尸隧道自愈）；
- 端口只绑在接收端 loopback；
- 不需要给 gateway 增加 MCP 语义，也不破坏它的控制面定位。

`ssh-client` 已支持 `-R`（`cmd/ssh-client/forward.go:190-211`）与断线重连（`runForwardOnly`），因此这条路径**不需要新代码**，只需要文档与配置样例。

**结论**：gateway 隧道不进入 P4 实现范围。目标机无法被本地直连时，用 `ssh -R`；如果连 SSH 出站都不允许，那需要的是网络策略变更，不是应用层方案。

### 11.3.2 两种连接方式的最终状态（2026-10-21 实测）

答案是**都支持**，但两者的前提、暴露面和适用场景不同。下面全部由 `scripts/ssh_transports_e2e.go` 用**真实的 `ssh-client` 二进制**跑通。

| | `ssh-stdio` | `ssh-http` |
|---|---|---|
| 目标端进程 | 每次调用起一个 `serve --stdio` | 常驻 `serve --http` |
| 目标端监听端口 | **不监听任何端口** | 监听 `127.0.0.1:<port>` |
| 本地如何到达 | SSH **exec 通道**（stdin/stdout 承载 JSON-RPC） | `ssh-client -N -L <localPort>:127.0.0.1:<remotePort>` 本地转发 |
| 展开后的命令 | `ssh-client -T -p 22 --stdio host -- <bin> serve --stdio --config-b64 <b64>` | `ssh-client -N -L 18765:127.0.0.1:18765 host` + `url: http://127.0.0.1:18765/mcp` |
| 认证 | SSH（公钥 / agent / 密码 / keyboard-interactive） | SSH 承载传输 + 目标端 Bearer token（或 mTLS） |
| 冷启动开销 | 每次调用一个进程 | 只在首次连接 |
| 适合 | 目标机**禁止开监听端口**、调用频率低 | 交互频繁、需要长连接 / SSE、多会话并发 |
| 实测 | **PASS**（tools/list 8 个，view 正常，jail 拒绝） | **PASS**（两次连续握手，各 4 工具，view + jail 均正确） |

**为什么 ssh-stdio 是 8 个工具、ssh-http 是 4 个**：测试里前者用 `profile: standard`，后者用 `profile: readonly`。这是刻意选的两个档位，用来同时覆盖两种工具面 —— 不是 transport 的差异。两种 transport 共用一个 `remotetools` 实现，工具面只由 profile 决定。

**注意 `--config-b64` 的设计意图**：SSH exec 通道收到的是一条字符串，真实 sshd 会交给远端 shell 解析。若把 workspace 路径直接拼进命令行，含空格或引号的路径会被 shell 吃掉。`--config-b64` 把整个远端配置做 base64，天然不含 shell 元字符，是这条链路能安全承载任意路径的原因。

**ssh-http 的部署顺序**（README 与展开器都提示了这一点）：先 `ssh-client -N -L ...`，再让 agent 调用工具。转发没起来时工具调用会得到连接错误，而不是静默重试。

### 11.4 备选方案 D：Skill + 远端 CLI（不用 MCP）

不放 MCP server，只在目标机放一个**纯命令行**二进制，由本地 skill 的 workflow 调用它：

```yaml
# .agents/skills/remote_dev/skill.yaml
name: remote_dev
description: 在 dev.internal 上只读查看与搜索文件
execution_mode: model          # 或 document
tools:
  - shell
workflow:
  steps:
    - id: call
      tool: shell
      args:
        command: 'ssh-client -T dev.internal -- /usr/local/bin/aicli-remote-cli {{prompt}}'
```

远端 CLI 形态：

```bash
aicli-remote-cli view  --workspace /srv/app --path src/main.go --offset 1 --limit 200
aicli-remote-cli grep  --workspace /srv/app --pattern "TODO" --glob "*.go"
aicli-remote-cli shell --workspace /srv/app --timeout 60s -- "go test ./..."
```

**优点**

- 零协议：不需要 MCP server、schema 协商、会话管理；
- 最小改动：`ssh-client` 修好 stdin 后即可用；
- 模型只看得到 skill（`skill__remote_dev`），工具面不膨胀；
- 远端二进制更小（无 MCP SDK）；
- 与现有 skill 路由、`scripts/`、`SKILL.md` 体系天然一致。

**缺点（为什么不能作为目标架构）**

| 问题 | 说明 |
|---|---|
| 没有工具发现 | 模型不知道远端有哪些能力，只能靠 skill 描述猜 |
| 没有结构化 schema | 参数靠字符串拼接，`prompt` 直接进命令行，注入面大 |
| 输出契约弱 | CLI 只能返回文本；分页、截断、`artifact_read` 都要自己约定 |
| 错误不可分类 | 只能靠退出码；`view` 的"文件不存在"与"连接失败"难以区分 |
| 权限粒度粗 | 只能靠 shell 白名单；无法按工具做 `allow_read/allow_write/allow_shell` |
| 审计弱 | 只能记整条命令行，无法结构化记 `tool` / `path` / `exit_code` |
| 并行与限流 | 每次调用一个进程；没有 `MaxParallelCalls`、没有按目标的信号量 |
| 审批体验差 | 审批对象是 `shell` + 一整条命令，而不是"远端 view 这个文件" |
| 多目标区分弱 | 只能靠 skill 名（`remote_dev_view`），无法用工具名做作用域 |
| 跨平台差异 | 参数编码、引号、路径分隔符全在字符串层，Windows 目标更脆 |

**结论**：Skill + CLI **适合作为 P0 的验证形态**（比薄封装更接近最终结构，能复用 skill 体系），但**不适合作为目标架构**。推荐路径是"先用它验证链路与用户价值，再升级到 MCP"。

**混合形态（推荐）**

第一版可以两者并存，而不是二选一：

- **远端 MCP server**：提供结构化工具面，给主 Agent 用；
- **同一个二进制的 `cli` 子命令**：给 skill、脚本、CI、人工排查用；
- 两者共享同一套 `workspace` / `policy` / `audit` 实现。

```text
aicli-remote-tools serve --stdio    # MCP 模式
aicli-remote-tools serve --http     # MCP 模式
aicli-remote-tools run view ...     # CLI 模式（给 skill/脚本）
aicli-remote-tools run grep ...
aicli-remote-tools doctor
```

这样 skill 可以封装"一次多步远程操作"（例如 `remote_dev_build`：grep → view → shell），
而单步、需要审批、需要并行的操作仍走 MCP 工具面。

### 11.5 被否决的做法

| 做法 | 否决原因 |
|---|---|
| 直接把 `toolkit-mcp-server` 监听 `0.0.0.0` | 无认证、无 jail、默认全工具，等于远程 root |
| 全局关闭 `BlockRemoteWrites` | 放大所有远程 MCP 的写权限，违背最小权限 |
| 依赖工具名启发式判断远程 shell | `shell` 不是 `WriteLike`，会漏拦 |
| 让远端信任客户端传来的绝对路径 | prompt 注入即可越权读写 |
| 用 SSH 交互式 shell 承载 MCP | PTY 会混入提示符/控制字符，stdio 协议必坏 |
| 第一版就做 OAuth/DCR | 内网无浏览器、无动态注册，复杂度远大于收益 |
| 用 `ExecutionMode` 判断远程权限 | SSH stdio 恒为 `local_mcp`，会让远程写/命令绕过策略 |

---

## 12. 待决策问题

1. **二进制命名**：`aicli-remote-tools` 还是复用 `aicli` 子命令 `aicli remote serve`？
   - 建议独立二进制：目标机不加载 LLM/会话/Web UI，体积与攻击面更小。
2. **配置归属**：`remoteTargets` 放 `mcp.yaml` 还是独立 `remote.yaml`？
   - 建议独立文件，避免与现有 MCP 配置的写回逻辑耦合。
3. **审批交互**：远程写/命令是否需要本地逐次审批，还是 per-target 一次授权？
   - 建议默认逐次（`default` 模式），`accept_edits` 可按工具类型放宽。
4. **Windows 目标**：第一版是否支持？
   - 建议第一版只支持 Linux/macOS；Windows 目标在 P4 单独做路径与 shell 适配。
5. **远端 job**：是否复用 `internal/background` 的持久化格式？
   - 建议复用语义（`job_start/job_output/job_cancel`），但存储放远端，不跨机同步。
6. **工具描述长度**：显式标注 `[remote:dev]` 会增加 token，是否默认开启？
   - 建议默认开启；多目标场景下这是防止误用的关键信息。
7. **SSH stdio 的进程模型**：每个 MCP server 一个长期进程，还是每次调用一个进程？
   - MCP stdio 是长连接，必须一个 server 连接一个进程；不能用“每次调用一个进程”。
8. **是否支持 Unix socket**：HTTP 模式下监听 Unix socket 可进一步减少端口暴露。
   - 建议 P3 支持；Windows 命名管道另议。

---

## 13. 多目标部署（Multi-Target）

### 13.1 结论：一个 Agent 可以连接多个服务器，不是一对一

多目标是**原生支持**，不需要新增连接模型：

| 层 | 现状 | 说明 |
|---|---|---|
| MCP 管理器 | `manager.clients map[string]client.Client`（`mcp/manager/manager.go:132`） | 每个 server 一个 client，天然多实例 |
| 工具注册表 | `tools map[string]*ToolInfo`，key = `mcpName + NUL + toolName`（`mcp/registry/registry.go:71`） | 同名工具在不同 server 下互不覆盖 |
| 配置 | `mcpServers` 是 map（`mcp/config/types.go:23`） | 任意数量条目 |
| 并发闸门 | `buildParallelMCPGates` 按 `MCPName` 建独立信号量（`agent/tool_parallel_scheduler.go:142-167`） | 每个目标独立限流，互不阻塞 |
| 短名歧义 | `AmbiguousToolError`（`registry.go:48-60, 606-613`） | 同名工具 fail-closed，要求用 canonical 名 |

所以第一版就应支持 N 个目标，而不是“先做一个、以后再扩”。真正需要设计的是**如何区分**与**如何控制成本**。

### 13.2 三层区分：名字、元数据、策略

只靠“工具名”区分是不够的（会被本地工具压掉、会超 64 字符、无法表达权限）。建议三层同时存在：

#### 第一层：工具名（模型可见）

规则：

```text
remote_<target>__<tool>
```

约束（来自 `registry.CanonicalToolName`，`registry.go:229-237, 616-629`）：

- canonical 形态实际是 `mcp__<server>__<rawTool>`；
- 总长 ≤ 64 字符，超长会被 `_<6 位 hash>` 截断；
- 只允许 `[a-zA-Z0-9_-]`，非法字符会被替换并追加 hash 后缀。

因此：

| 约束 | 建议 |
|---|---|
| target 名长度 | ≤ 12 字符（如 `dev`、`prod`、`k8s1`） |
| 远端前缀 | ≤ 16 字符（如 `remote_dev`） |
| 禁止 | 在 server 名里再写一次 `remote`（`remote-dev` + `remote_dev` → `mcp__remote_dev__remote_dev__view`，浪费 20 字符） |

推荐命名：

```text
server 名: dev          → mcp__dev__remote_dev__view
server 名: prod         → mcp__prod__remote_prod__shell
```

#### 第二层：工具元数据（本地可审计）

在工具定义 metadata 中固定写入：

```json
{
  "mcp_name": "dev",
  "remote_target": "dev",
  "remote_host": "dev.internal",
  "remote_workspace": "/srv/app",
  "execution_scope": "remote",
  "transport": "stdio"
}
```

用途：

- `/api/runtime/tools`、TUI `/tools`、`toolsurface.Classify` 能回答“这个工具属于哪台机器”；
- 审计与故障定位；
- **不作为授权依据**（授权仍看 policy）。

#### 第三层：策略（真正决定能不能调）

```go
RemoteTargets map[string]RemoteTargetPolicy // key: MCPName
```

同一 Agent 下每个目标独立一套 `allow_read` / `allow_write` / `allow_shell` / 审批要求。
示例：`dev` 全开，`prod` 只读，`k8s1` 只允许 `view/grep`。

### 13.3 配置形态

```yaml
remoteTargets:
  dev:
    transport: ssh-stdio
    ssh: { host: dev.internal, user: deploy }
    remote:
      workspace: /srv/app
      profile: standard
      toolPrefix: remote_dev
    policy:
      allowRead: true
      allowWrite: true
      allowShell: false

  prod:
    transport: ssh-http
    ssh: { host: prod.internal, user: ops, localForward: 127.0.0.1:18765 }
    remote:
      workspace: /srv/app
      profile: readonly
      toolPrefix: remote_prod
      tokenEnv: AICLI_REMOTE_TOKEN_PROD
    policy:
      allowRead: true
      allowWrite: false
      allowShell: false

  k8s1:
    transport: ssh-stdio
    ssh: { host: k8s1.internal, user: ops }
    remote:
      workspace: /opt/manifests
      profile: readonly
      toolPrefix: remote_k8s1
    policy:
      allowRead: true
      tools:
        allowlist: [view, grep]   # 连 ls/glob 都不要
```

展开后得到 3 个 MCP server、12 个远端工具（按只读 4 个算），加上本地工具后模型可见面约 30–40 个。

### 13.4 工具面膨胀控制

多目标最大的成本不是连接，而是 **prompt token**。三种手段按优先级：

| 手段 | 效果 | 代价 |
|---|---|---|
| profile 只读 + 工具 allowlist | 每个目标默认 4 个工具 | 需要提前规划 |
| 工具描述 compact | 每个描述省 30–50 token | 可读性下降 |
| 按会话绑定目标子集 | 只加载本会话需要的 1–2 台 | 需要会话级配置 |

建议默认：

- 每个目标只开 `view` / `ls` / `glob` / `grep`；
- 工具描述用 compact 模式，但保留 `[remote:<target>]` 前缀；
- 目标数 ≥ 4 时，强制会话级目标选择（否则工具面不可控）。

### 13.5 并发与隔离

现有机制已按 `MCPName` 隔离，但需要明确几条规则：

| 规则 | 原因 |
|---|---|
| 每个目标独立信号量 | `buildParallelMCPGates` 已实现；不要把多个目标合并成一个池 |
| 远端写/命令串行化 | 同一目标上的写操作互相踩（同一 workspace）；`ops` profile 的 `MaxParallelCalls` 建议 1 |
| 不同目标的只读可并行 | 互不依赖，延迟收益明显 |
| 全局上限仍生效 | `MaxParallelToolCalls` 是总闸，防止 N 个目标各开 8 个并发 |
| 单目标故障不拖垮其他目标 | MCP 管理器已按 server 独立连接；一个 server 断开不影响其他工具 |

建议在 `remoteTargets.<name>.concurrency` 中显式声明：

```yaml
concurrency:
  maxParallelReads: 4
  maxParallelWrites: 1
  maxParallelShell: 1
```

### 13.6 子代理的目标范围

这是一个容易被忽略的越权路径：

- `subagent_spawn` 默认继承父会话工具面；
- 如果没有 target 级 allowlist，子代理会**自动获得所有远程目标**，包括 `prod` 的写/命令；
- 父代理的“我只读 prod”约束不会自动传递。

建议：

```yaml
remoteTargets:
  prod:
    policy:
      subagentTargets: deny        # 默认：任何子代理都不能访问
      # 或
      subagentTargets: [researcher]  # 只允许只读研究类子代理
```

并在 `subagent_spawn_batch` 的 capability 计算中把 target 作为一维输入：

```
子代理工具面 = 父面 ∩ 子角色 capability ∩ 允许的 remote targets
```

### 13.7 跨目标操作：不做自动编排

以下需求**不要**在第一版做：

| 需求 | 为什么不建议 |
|---|---|
| 从 A 机读文件写到 B 机 | 需要跨目标数据中转、大小限制、临时存储、审计链；语义上更像“文件传输”而不是工具调用 |
| 在 A 机执行命令、在 B 机执行下一步 | 需要分布式事务/补偿；失败时状态不一致 |
| 一次工具调用里指定 `target` 参数 | 会让模型自由选择目标，破坏“工具名即作用域”的可解释性，也让策略难以静态裁剪 |

正确做法：

- 模型显式调用 `remote_dev__view` → 得到内容 → 再调用 `remote_prod__write`；
- 跨机传输用独立的 `sftp-client` 或 `remote_transfer` 工具，有单独的权限与审计；
- 每一步都在模型可见的工具面上，可逐步审批。

### 13.8 可观测性

多目标后，以下问题必须能回答：

| 问题 | 落点 |
|---|---|
| 哪些目标已连接、各自延迟多少 | `/api/runtime/mcps` + target 扩展 |
| 某次工具调用打到哪台机器 | 审计 `target` 字段 + tool.completed metadata |
| 哪个目标最慢/最常失败 | 指标按 `mcp_name` / `remote_target` 维度聚合 |
| 目标断开后模型看到什么 | 明确的 `REMOTE_TARGET_UNAVAILABLE`，不是泛化的 tool error |

建议在 `MCPStatus` 中增加：

```go
RemoteTarget     string        `json:"remoteTarget,omitempty"`
Transport        string        `json:"transport,omitempty"`
ExecutionScope   string        `json:"executionScope,omitempty"`
LastLatencyMS    int64         `json:"lastLatencyMs,omitempty"`
RemoteVersion    string        `json:"remoteVersion,omitempty"`
```

### 13.9 验收补充（多目标）

| 编号 | 用例 | 断言 |
|---|---|---|
| MT1 | 配置 3 个目标 | `tools/list` 出现 3 组前缀且互不覆盖 |
| MT2 | 两个目标都有 `view` | 两个 canonical 名可同时调用，结果来自不同机器 |
| MT3 | 本地也有 `view` | 三者共存，本地 `view` 不被覆盖 |
| MT4 | `prod` 只读、`dev` 可写 | `remote_prod__write` 不存在或被拒绝；`remote_dev__write` 可用 |
| MT5 | 子代理 | 默认看不到 `prod` 的写/命令工具 |
| MT6 | 一个目标断开 | 其他目标工具仍可用；错误码为 `REMOTE_TARGET_UNAVAILABLE` |
| MT7 | 并发 | 两个目标并行只读，各自受自己的 `maxParallelReads` 限制 |
| MT8 | 同名短名调用 | `view` 不产生歧义（本地优先）；远端必须用 canonical 名 |
| MT9 | 4 个目标 | 工具面 token 在预算内，或强制会话级目标选择 |

### 13.10 多目标相关的待决策

1. **默认目标数上限**：是 3、5 还是不限制？（建议软上限 5，超过要求显式确认）
2. **会话级目标绑定**：P1 是否支持“本会话只加载 dev”？（建议 P2）
3. **子代理默认策略**：`deny` 还是“继承但只读”？（建议 `deny`，显式开启）
4. **跨目标传输**：是否需要独立的 `remote_transfer` 工具？（建议 P3）
5. **目标健康检查频率**：沿用 MCP `healthCheckInterval`，还是远端 `doctor` 主动探测？

---

## 14. 参考

### 14.1 仓库内代码

- `backend/internal/mcp/transport/transport.go` — MCP 多传输实现
- `backend/internal/mcp/transport/streamable.go` — Streamable HTTP 客户端
- `backend/internal/mcp/config/loader.go` — MCP 配置校验
- `backend/internal/mcp/server/server.go` — MCP 服务端基类（当前仅 stdio）
- `backend/internal/toolkit/mcp_server.go` — 工具转 MCP
- `backend/cmd/toolkit-mcp-server/main.go` — 现有 stdio MCP 二进制
- `backend/internal/tools/manager.go` — 本地/MCP 工具合并与优先级
- `backend/internal/policy/tool_policy.go` — 工具执行策略与远程写拦截
- `backend/internal/skill/mcp_adapter.go` — MCP 工具元数据与 `fs_owner`
- `backend/cmd/ssh-client/main.go`、`backend/internal/sshclient/session.go` — SSH 命令与转发
- `docs/user-guide/ssh-client.md`、`docs/tools/ssh-sftp-clients-usage.md`

### 14.2 外部资料

- [MCP Streamable HTTP 规范](https://modelcontextprotocol.io/specification/2025-03-26/basic/transports)
- [MCP Roots 规范](https://modelcontextprotocol.io/specification/2025-06-18/client/roots)
- [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk)
- [classfang/ssh-mcp-server](https://github.com/classfang/ssh-mcp-server)
- [punkpeye/mcp-remote](https://github.com/punkpeye/mcp-remote)
- [sparfenyuk/mcp-proxy](https://github.com/sparfenyuk/mcp-proxy)
- [supercorp-ai/supergateway](https://github.com/supercorp-ai/supergateway)
- [mathematic-inc/claude-tools-mcp](https://github.com/mathematic-inc/claude-tools-mcp)
- [mark3labs/mcp-filesystem-server](https://github.com/mark3labs/mcp-filesystem-server)
