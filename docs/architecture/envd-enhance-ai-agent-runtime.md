# 用 envd 增强 ai-agent-runtime：集成设计与落地路线

> 分析对象：本地仓库 `ai-agent-runtime`（`E:\projects\ai\ai-agent-runtime`，VERSION `v0.5.0`，Go module `github.com/wwsheng009/ai-agent-runtime`，go 1.26.0）
> 增强组件：`envd`（E2B 开源执行代理，v0.2.11，Apache-2.0，Connect-RPC over HTTP/2，单端口 49983）
> 文档性质：架构设计与可行性评估，所有代码事实均来自本地仓库实测读取

---

## 0. 结论摘要

1. **envd 值得引入，但只应作为「传输与执行底座」的第三种形态，不能替代现有的策略层与编排层。**
2. 项目已经有一套成熟的远端工具架构（`remotetargets` + `remotetools`，单二进制双传输），envd 的正确接入姿势是**新增一个 `envd` 传输实现**，复用现有的 Profile 分级、path jail、Limits、audit，而不是另起一套并行体系。
3. envd 真正补上的三块能力是项目当前缺失的：
   - **流式进程输出**（现有 shell 工具是一次性捕获）
   - **PTY 统一抽象**（项目仅有零散的 conpty 探测与驱动）
   - **远程文件变更监听**（项目本地只有编辑钩子，无真监听；远程完全没有）
4. envd **不能**提供：路径 jail、命令白名单、审计记录、节点租约。这些必须留在本地侧，否则会打开一个安全缺口。
5. 本地单机场景**不需要** envd —— 现有 `executor` 本地执行已够用，引入反而多一跳。envd 的价值只在「跨机 / 多租户 / 需要流式与 PTY」时才成立。

---

## 1. 现状盘点（基于代码实测）

### 1.1 三层结构

```text
aicli (CLI)  ──┐
runtime-server ─┼─→ backend/internal/*
Web console    ─┘
```

- 后端：Go，`backend/`；前端：React + Vite，`frontend/`。
- 默认路径是 CLI（local-first），HTTP/Web 是可选增强。

### 1.2 执行层：`internal/executor`

`SandboxConfig` 是全部策略的载体（`executor/sandbox.go`）：

| 字段 | 语义 |
|---|---|
| `Enabled` | 应用层沙箱总开关 |
| `MaxExecutionTime` | 单次执行超时 |
| `Profile` | `off` / `workspace` / `read-only` / `strict` |
| `BlockNetwork` | strict profile 下禁出网 |
| `OSSandbox` | `off` / `auto` / `require`，底层走 bwrap（`os_sandbox_bwrap_plan.go`） |
| `AllowedPaths` / `DeniedPaths` / `ReadOnlyPaths` | 路径围栏 |
| `AllowedCommands` / `DeniedCommands` | 命令白名单/黑名单 |
| `EnvWhitelist` | 环境变量白名单 |
| `AllowedHosts` / `DeniedHosts` | 网络目标围栏 |

配套：`process_guard.go`（进程组回收）、`output_capture.go`（输出折叠）、`parallel_executor.go`、`detach.go`（后台脱离）。

**这是项目的安全核心。envd 无法替代它。**

### 1.3 工具层：`internal/toolkit/tools`

`bash`、`execute_shell_command`、`edit`、`multiedit`、`apply_patch`、`append_write`、`atomic_write`、`glob`、`grep`、`ls`、`read`、`download`、`fetch`、`code_*`（符号检索/引用/导航）、`git_worktree`、`todos`、`artifact_*`。

注意 `change_notify.go` 的注释写得很清楚：它是 **agent edit hook 的同步标记侧**，只做去重入队，交给知识层 debounce 后索引。**它不是文件系统监听器**。

### 1.4 远端层：`internal/remotetargets` + `internal/remotetools`

`remotetargets`（客户端侧配置展开）：

- 传输常量：`ssh-stdio` / `ssh-http` / `http`
- 能力分级 `SpecDepth`：`readonly`（四个读工具）/ `standard`（加写）/ `ops`（加 shell）
- 默认端口 `DefaultSpecPort = 18765`
- 单字符串语法：
  ```text
  http://10.0.0.9:18765            # HTTP，token 从环境取
  http://10.0.0.9:18765#ops        # ops 授权
  ssh://ops@10.0.0.9               # SSH stdio，只读
  ssh://ops@10.0.0.9:2222#standard # 非默认端口
  ```

`remotetools`（服务端侧实现）：

- **一个二进制服务两种传输**：`serve --stdio`（SSH 每连接拉起）/ `serve --http`（长驻守护）
- 工具面、schema、path jail、输出预算、错误码、审计记录**完全共享**，只有进程生命周期不同 —— 这个设计让两种传输可在测试中做等价性对比（`*_local_remote_equivalence_test.go` 就是干这个的）
- `Profile`：`readonly` / `standard` / `ops` / `custom`
- `Limits`：`MaxOutputBytes`（legacy）/ `MaxBytesPerCall` / `CaptureLimit` / `ModelVisibleCap`
- 已实现模块：`audit.go`、`accesslog.go`、`metrics.go`（进程内单调计数，`doctor --json` 读取）、`jobs_tools.go`、`artifact.go`、`sandbox.go`、`roots.go`、`commandmatch.go`（命令匹配）

**这一层已经和 envd 高度同构。** envd 的接入必须复用它的外壳，不是重写。

### 1.5 其它相关模块

| 模块 | 能力 |
|---|---|
| `internal/sshclient` | `conn` / `session` / `sftp` / `auth` / `hostkey` / `proxy` / `agent`（unix+windows），SSH 与 SFTP 已完备 |
| `internal/mesh` | heartbeat 30s、TTL 90s、capabilities（`manifest`/`screen`/`status`/`events`/`invoke`/`input`/`sessions`/`mesh`）、`lease`、`fanin`、`filecache`、`journal` |
| `internal/filetransport` | 文件传输服务 |
| `internal/filebrowse` / `gitbrowse` | 远端浏览 |

`remotetargets/registry.go` 的注释点明了一个关键设计约束：

> a target carries SSH credentials, a forward port and a capability grant, none of which belong in the generic MCP editor

**凭据与授权不进通用配置面** —— envd 的 token 也必须遵守这条。

---

## 2. envd 能力对照

envd 的方法清单来自沙箱内 `/usr/bin/envd` 二进制的字符串与构建信息实测（16 个 Connect 方法，分属 `process` / `filesystem` / `metrics` 三族，单端口 49983，`X-Access-Token` 鉴权）。

| envd 能力 | 项目现状 | 差距 | 增强价值 |
|---|---|---|---|
| `process.Start` 流式输出（Server-Stream） | `bash` 一次性捕获，`output_latency.go` 只做延迟测量 | **无真正的流式通道** | ★★★ 长任务可边跑边看，模型不必等全量 |
| `process.Connect` + stdin 写入 | 无（后台任务走 `jobs_tools.go` 轮询） | **无交互式 stdin** | ★★★ 可跑 REPL / 交互式 CLI |
| PTY（`creack/pty`，支持 resize） | 仅 `cmd/conpty-probe`、`cmd/goal-conpty-driver` 零散使用 | **无统一 PTY 抽象** | ★★★ 终端类工具（vim/top/python REPL）真正可用 |
| `filesystem.Stat` / `ListDir` | `ls` / `view` 已实现且更完善（分页、编码、去重） | 项目更强 | ☆ 无需替换 |
| `filesystem.MakeDir` / `Remove` / `Move` | 项目通过写工具与 shell 完成 | 无独立 CRUD 端点 | ★ 可作为原子文件操作的底层 |
| `filesystem.WatchDir` 流 | 仅本地编辑钩子（非真监听），远程无 | **无远程 watch** | ★★★ 远程改动可即时感知 |
| 写文件 / 读文件流 | 项目 `view`/`read`/`write` 已覆盖，且带 path jail | 项目更强 | ☆ 无需替换 |
| `metrics`（cpu_count/mem_total/disk） | `remotetools/metrics.go` 只有调用计数，无主机资源 | **缺主机侧指标** | ★★ 可做目标健康度与容量感知 |
| Connect-RPC 单端口多路复用 | `http` 传输已是单端口，但需自建 server 与守护 | 已有等价物 | ★ 部署更轻 |
| 文件监听内建（`e2b-dev/fsnotify`） | 本地用 `fsnotify/fsnotify v1.9.0` | 版本不同源 | ★ 统一 inotify 语义 |

### 关键不对称（必须正视）

envd 侧**没有**：

- 路径 jail（它信任调用方传的绝对路径）
- 命令白名单 / 黑名单
- 审计记录
- 节点心跳 / 租约

项目侧**必须保留**：`SandboxConfig` 全套、`remotetools/audit.go`、`mesh` 的 heartbeat/lease。

**结论：envd 是「手脚」，项目的 executor + remotetools 是「神经与免疫系统」。手术方案是换手脚，不是拆免疫系统。**

---

## 3. 三种集成模式与取舍

### 模式 A：envd 作为第四种传输（推荐）

在 `remotetargets.Transport` 中新增 `TransportEnvD`，`remotetools` 复用现有工具面，底层改由 `internal/envdclient` 走 Connect-RPC。

```text
aicli / runtime-server
        │
   remotetargets（凭据、Depth 授权、Profile）
        │
   remotetools（工具面 / jail / Limits / audit）  ← 全部保留
        │
   ┌────┴────┬─────────┬──────────┐
 stdio     ssh-http   http     envd（新增）
                                    │
                              Connect-RPC over HTTP/2 :49983
                                    │
                                  envd ─→ process / filesystem / metrics
```

- 改造量：中（新增一个传输适配器 + 客户端包）
- 收益：直接获得流式输出、stdin、PTY、远程 watch、主机指标；复用全部安全外壳
- 风险：低（不触碰现有路径）

### 模式 B：envd 作为 mesh 节点后端

`mesh` 的 `invoke` 能力目前落在本地 loopback server。理论上可让远端 mesh 节点的执行下沉到 envd。

- 改造量：高
- 收益：与现有 mesh 发现/租约天然协同
- 风险：中高（mesh 的 capability 语义与 envd 方法族不对齐，需要一层映射）
- 判定：**作为模式 A 的下游消费者，而不是并列方案**。先有 `envdclient`，mesh 才能引用。

### 模式 C：envd 替换本地 `OSSandbox`（bwrap）

- 改造量：高
- 收益：几乎为零（envd 不提供比 bwrap 更强的隔离，反而少了路径围栏）
- 风险：高（丢掉 `AllowedPaths` 等全部应用层策略）
- 判定：**不推荐**。

### 取舍表

| 维度 | 模式 A | 模式 B | 模式 C |
|---|---|---|---|
| 改造量 | 中 | 高 | 高 |
| 新增能力 | 流式/PTY/watch/指标 | 同 A + 编排 | 无 |
| 安全影响 | 无（外壳保留） | 无 | **负面** |
| 建议 | ✅ 采用 | ⏳ 二期 | ❌ 否决 |

---

## 4. 模式 A 详细设计

### 4.1 Spec 语法扩展

沿用现有单串约定，新增 scheme：

```text
envd://<host>:49983                    # 只读，token 从 envd 环境或 AICLI_ENVD_TOKEN
envd://<host>:49983#standard           # 加写
envd://<host>:49983#ops                # 加 shell/process
envd://<host>:49983?workspace=/home/user/project&token_env=AICLI_ENVD_TOKEN#ops
```

解析落在 `remotetargets/spec.go` 的 `ParseSpec`，与现有 `http://` / `ssh://` 分支并列。`Depth` 语义保持不变，`DefaultSpecPort` 对 envd 取 `49983`。

> **实现口径（2026-10-28）**：token 只接受**环境变量名**（`token_env=<NAME>`，默认 `AICLI_ENVD_TOKEN`）；内联写法 `?token=<t>` 会被**拒绝**；该 token 以 `X-Access-Token` 头发送，envd 经 `/init` 安装访问令牌后强制校验（未启用令牌时该头被忽略）。

> 安全对齐：token 与 `registry.go` 注释的要求一致 —— 属于凭据，必须走环境变量或独立 secret 源，不进 `mcp.yaml`。

### 4.2 新增 `internal/envdclient`

```go
package envdclient

type Client struct {
    baseURL string        // http://host:49983
    token   string        // X-Access-Token
    hc      *http.Client  // 强制 HTTP/2
}

// 生命周期
func Dial(ctx context.Context, spec remotetargets.Spec) (*Client, error)
func (c *Client) Close() error

// process 族
func (c *Client) StartProcess(ctx context.Context, req StartReq) (pid uint32, err error)
func (c *Client) StreamOutput(ctx context.Context, pid uint32) (<-chan Chunk, error) // Server-Stream
func (c *Client) SendInput(ctx context.Context, pid uint32, data []byte) error
func (c *Client) ResizePTY(ctx context.Context, pid uint32, cols, rows uint32) error
func (c *Client) Signal(ctx context.Context, pid uint32, sig string) error

// filesystem 族
func (c *Client) Stat(ctx context.Context, path string) (Entry, error)
func (c *Client) ListDir(ctx context.Context, path string) ([]Entry, error)
func (c *Client) WatchDir(ctx context.Context, path string) (<-chan Event, error) // Server-Stream

// metrics 族
func (c *Client) Metrics(ctx context.Context) (HostMetrics, error)
```

实现要点：

1. **协议**：Connect-RPC 是「POST + `application/connect+json`」的普通 HTTP 请求，`curl` 即可验证，不依赖 gRPC 运行时。实测过：缺 `Content-Type` 会返回 415，补上即正常。
2. **HTTP/2**：Server-Stream 依赖 `http2.Transport`（或 TLS + `ForceAttemptHTTP2`）。明文下需 `golang.org/x/net/http2` 的 `AllowHTTP` + 自定义 `DialTLS`。
3. **超时**：流式端点不能套 `MaxExecutionTime` 的 `context.WithTimeout`，否则长任务会被 client 侧砍断。超时控制改由 `process.Signal` + `process_guard` 逻辑承担。
4. **重连**：Server-Stream 断流是常态（实测沙箱侧隧道存在 TIME_WAIT 堆积，连接频繁重建）。`StreamOutput` 必须设计为可重入：携带 `pid` 与 `lastOffset` 重连，服务端按 pid 续传。

### 4.3 传输适配：实现同一 `ToolSurface`

`remotetools` 的价值在于「一个实现，两种传输，行为等价」。envd 传输应实现**同一个**接口，让 `readonly`/`standard`/`ops` 三档 Profile、`Limits`、`audit` 全部自动生效。

| 工具 | envd 后端实现 | 备注 |
|---|---|---|
| `view` | `Stat` 判断类型 + 读取（经 process `cat` 或 fs 读流） | 优先复用 envd 原生读，失败回落 shell |
| `ls` | `ListDir` + 本地排序/分页 | 分页、隐藏文件规则沿用本地实现 |
| `glob` | `ListDir` 递归 + 本地匹配 | envd 无 glob，需本地算 |
| `grep` | `process` 跑远端 `rg` | 依赖目标机有 ripgrep，无则回落 `grep` |
| `write` / `edit` / `apply_patch` | 读取 → 本地计算 diff → 回写 | 回写走 envd `process`（2026-10-27 实测：envd 无写文件 RPC；上限 1 MiB，见 §7 第 1 项） |
| `shell` | `StartProcess` + `StreamOutput` | ops 档才有 |
| `watch`（新增，可选） | `WatchDir` | 项目当前无此工具，属净增 |

**注意 `prefixedTool` 机制**：`registry.go` 已用前缀（如 `remote__view`）避免远端工具与本地内建撞名。envd 传输必须沿用同一前缀策略，否则 `tools.Manager` 会静默丢弃远端同名工具。

> **实际落地（2026-10-28）**：envd 传输**没有**按本节做成 `remotetools` 的完整适配器 —— 实际是 `remotetargets` 把 `envd://` 展开为指向本地 `aicli-envd-bridge`（MCP stdio）的条目；Profile、jail、输出上限在 bridge 内实现。**命令授权已复用** `remotetools.CommandPolicy`（`shell` / `pty_start`），audit 仍是 bridge 自己的 opt-in JSONL。详见 `envd-implementation-report.md`；**收敛计划（2026-10-28 决策）见本文 §10 与 `docs/plan/aicli-envd-bridge-merge-plan-20261028.md`。**

### 4.4 权限模型对齐

| 层 | 归属 | envd 引入后 |
|---|---|---|
| 路径围栏 | `executor.SandboxConfig.AllowedPaths` / `remotetools` path jail | **保留，且在 envd 传输侧前置校验**（envd 自己不校验） |
| 命令白名单 | `AllowedCommands` / `commandmatch.go` | **保留，在发出 `StartProcess` 前判定** |
| 能力分级 | `SpecDepth` / `Profile` | 保留，映射到「哪些工具注册」 |
| 鉴权 | `X-Access-Token` | 新增一层，与 SSH key 并列 |
| 审计 | `audit.go` | **保留**，envd 无审计 |
| 运行身份 | — | envd 侧实测以 `uid=1000`（非 root）运行，天然降权，可作为额外防线 |

**红线：如果只做「把请求转发给 envd」而不做前置校验，等于绕过整个 SandboxConfig。这是本方案唯一的高危实现错误。**

> **实际落地（2026-10-28）**：envd 路径的 jail / Profile / 输出上限在 bridge 内实现；文件路径先经目标机 `realpath`/`readlink -f` 规范化再比对 jail（symlink 逃逸已堵）；命令白名单已通过共享 `remotetools.CommandPolicy` 落到 `shell` / `pty_start`（空 allowlist fail-closed），会话审计为 bridge 的 opt-in JSONL、非 `remotetools.audit`。偏差见 §4.3 注与 `envd-implementation-report.md` §2.1 / §8.3。

---

## 5. 分阶段路线图

### P0：只读打通（1–2 天）｜ ⚠️ 实际状态（2026-10-28）：部分完成（spec / 展开 / bridge / jail / metrics）

- `internal/envdclient` 骨架 + `Dial` / `Metrics`
- `remotetargets.Spec` 支持 `envd://`
- `view` / `ls` 两个工具走 envd
- **验收信号**：`aicli --remote 'envd://host:49983' "读一下 README 前 20 行"` 返回正确内容，且 audit 里有 `executionScopeRemote` 记录

### P1：流式执行 + PTY（3–5 天）｜ ❌ 实际状态（2026-10-28）：未完成

- `StartProcess` / `StreamOutput` / `SendInput` / `ResizePTY`
- `shell` 工具在 `ops` 档走流式路径
- **验收信号**：跑一个 60 秒的 `for i in $(seq 60); do echo $i; sleep 1; done`，前端/TUI 能看到逐行输出而不是 60 秒后一次性刷出

### P2：远程 watch（2–3 天）｜ ❌ 实际状态（2026-10-28）：未完成

- `WatchDir` 接入知识层 `MarkChanged`（复用现有 `toolctx.WithFileChangeNotifier` 入口）
- **验收信号**：在目标机 `touch` 一个被索引的文件，本地知识库在 debounce 后自动重索引

### P3（可选）：mesh 下沉

- mesh 节点的 `invoke` 在 `Capabilities` 里声明 `envd` 后端，走模式 B
- **验收信号**：`aicli-mesh` 能列出该节点并在其上执行工具

---

## 6. 什么时候不该用 envd

明确边界，避免过度设计：

1. **本地单机**：`executor` 已直连执行，加 envd 多一跳、多一个守护进程、多一个 token，纯亏损。
2. **只需要文件读写**：`sshclient` + SFTP 已完备，`remotetools` 的 http 传输已覆盖。
3. **需要强隔离**：bwrap（`OSSandbox=require`）比 envd 更严；envd 自己不做围栏。
4. **目标是 Windows 远端**：envd 是 Linux 侧组件（`CGO_ENABLED=0` 静态二进制，依赖 `/proc`、pty、inotify）。Windows 仍走 `ssh-stdio`。

**判定口径一句话：需要「流式 / 交互 / 监听 / 主机指标」四者之一，且目标是 Linux 远端，才引入 envd。**

---

## 7. 风险与待验证项

### 已知风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| envd 无路径围栏 | **越界读写** | 传输侧前置 jail 校验，不依赖 envd |
| 流式端点被 client 超时截断 | 长任务误判失败 | 流式路径禁用 `context.WithTimeout`，改由 Signal 控制 |
| 连接频繁重建 | 输出中断、重复 | 携带 `pid` + `lastOffset` 可重入重连 |
| envd 版本漂移 | 方法签名变化 | 锁 `v0.2.11`，`Dial` 时校验版本 |
| 与 mesh 租约语义重叠 | 状态双写 | mesh 保留唯一租约源，envd 不参与发现 |
| 目标机无 ripgrep | `grep` 降级 | 检测后回落 `grep`，结果标记降级 |

### 待验证项（不要当成已确定）

1. ~~**envd 的 filesystem 写端点具体方法名**~~ **已解决（2026-10-27 实测）**：envd **没有写文件 RPC**（`ReadFile` / `WriteFile` 探测返回 404），写入只能走 `process`。实现把内容作为位置参数交给 `sh -c 'printf "%s" "$2" > "$1"' ...`，不做调用方数据的引号拼接；代价是 argv 上限，内容硬上限 1 MiB。详见 `envd-implementation-report.md` §2.4。
2. **Connect-RPC 流的帧格式**：Server-Stream 的 envelope（flag + length + payload）需按 Connect 规范实现，建议直接引 `connectrpc.com/connect v1.18.1`（与 envd 同版本，实测其依赖清单里有），避免手写帧解析。
3. **PTY 与 `process_guard` 的交互**：PTY 子进程的进程组归属需实测，否则孤儿进程回收会失效。

---

## 8. 最小验证命令

不写代码也能先确认 envd 可用：

```bash
# 1) 主机指标（无需 token 时）
curl -s http://<host>:49983/metrics

# 2) 方法探测（Connect-RPC：必须带 Content-Type，否则 415）
curl -s -o /dev/null -w '%{http_code}\n' \
  -X POST http://<host>:49983/process.Process/Start \
  -H 'Content-Type: application/connect+json' \
  -H 'X-Access-Token: <token>' \
  -d '{}'

# 3) 二进制元信息（版本锁定用）
go version -m /usr/bin/envd | head
```

---

## 9. 一页总结

- **做什么**：给 `remotetargets` 加 `envd://`，给 `remotetools` 加一个 envd 传输适配器，新增 `internal/envdclient`。
- **为什么**：补上项目明确缺失的流式进程输出、PTY、远程 watch、主机指标四块。
- **不做什么**：不替换 `executor.SandboxConfig`，不替换 `audit.go`，不替换 `mesh` 的租约与发现。
- **最大风险**：忘了 envd 不做路径围栏，导致策略层被绕过。
- **第一步**：P0 只读打通，用 `curl` 把 filesystem 写端点验清楚再动手写 `write`。

---

## 10. 收敛计划（2026-10-28 决策）

§4.3/§4.4「实际落地」注中记录的偏差（envd 未做成 remotetools 的可复用适配器、外壳由 bridge 重写、edit 三件套缺失）进入**收敛**：

- **形态**：`aicli-remote-tools serve --stdio --backend envd`（本机运行、以 Connect-RPC 为腿；仅 stdio）。`cmd/aicli-envd-bridge` 退役（过渡期可留兼容 shim）。
- **一份外壳**：MCP 循环 / Profile 档位 / audit / Limits / CommandPolicy / prefix·registry 全部来自 `remotetools`；执行后端两条（local-os / envd）。envd 专属的执行实现（目标侧 realpath jail、spawn `find`/`rg` 的合成、PTY 会话）保留在 envd 后端内——即本节上方 §4.3 表的原始形态。
- **工具面对齐**：`standard` 档位补 `edit/append_write/apply_patch`（与 ssh/http 同语义）；envd 扩展（mkdir/remove/move、host_metrics/watch_dir、pty_*）按计划文档 §2 档位表归位；档位计数 6/13/19（现为 6/10/16）。
- **对 §9 的澄清**：第 1 条「给 `remotetools` 加一个 envd 传输适配器」的落地形态确定为「**同一个 server 二进制 + `--backend envd` 执行后端**」——envd 不是第四种监听传输，而是 stdio 载体 + envd 执行后端。
- **迁移步骤、验收门与估时**：见 `docs/plan/aicli-envd-bridge-merge-plan-20261028.md`（M0–M5，估 5.5–10.5 人日，M0 后收敛）。

---

*本文档所有代码引用均来自本地仓库 `ai-agent-runtime` v0.5.0 实测读取；envd 方法族来自沙箱内二进制分析，标注为「待验证」的条目未经运行时确认。*
