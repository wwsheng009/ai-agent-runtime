# envd 增强 ai-agent-runtime：实施报告

> 实施日期：2026-10-27
> 目标仓库：`github.com/wwsheng009/ai-agent-runtime` v0.5.0
> 状态：**已完成并通过验证**

本文记录**实际写入了什么代码、验证到什么程度、哪些是实测结论、哪些限制仍在**。设计依据见 `envd-enhance-ai-agent-runtime.md`，架构定位见 `envd-architecture-position.md`。

---

## 1. 交付清单

### 1.1 新增：`internal/envdclient`（7 个文件）

envd 的 Connect-RPC 客户端。**纯标准库**，不引入 protobuf 运行时或 connect-go。

| 文件 | 职责 |
|---|---|
| `envelope.go` | Connect 流式信封编解码（1B flag + 4B 大端长度 + payload）、`ConnectError` 类型 |
| `client.go` | `Dial` / 地址归一化 / unary 调用 / 流式 `Stream` + `StreamReader` |
| `process.go` | `Start` / `Run` / `List` / `SendSignal` / `SendInput` / `UpdatePty`，事件联合体解码 |
| `filesystem.go` | `Stat` / `ListDir` / `MakeDir` / `Remove` / `Move` / `WatchDir`，`Entry` 类型 |
| `metrics.go` | `Metrics` / `Ping`，`HostMetrics` |
| `util.go` | `readLimited`，统一响应体上限 |
| `client_test.go` | 21 个测试（15 离线 + 6 live，后者由环境变量开关） |

### 1.2 新增：`cmd/aicli-envd-bridge`（2 个文件）

MCP stdio ↔ envd Connect-RPC 桥接进程。**这是必要的，不是可选抽象**：envd 不讲 MCP，无法直接登记为 MCP server 条目。

| 文件 | 职责 |
|---|---|
| `main.go` | 参数解析、MCP JSON-RPC 循环、profile 分级、**路径 jail（`resolve`）**、工具清单 |
| `tools.go` | 10 个工具实现 + TLS 加载 + 错误翻译 |

### 1.3 修改：`internal/remotetargets`（2 个文件）

| 文件 | 改动 |
|---|---|
| `spec.go` | 新增 `DefaultEnvDPort = 49983`；`Spec` 加 `EnvDTokenEnv` 字段；`ParseSpec` 加 `case "envd"`；`Expand` 加 `case TransportEnvD` |
| `registry.go` | 新增 `TransportEnvD = "envd"` 常量；`expandOne` 加 `case TransportEnvD` 分支 |
| `envd_spec_test.go` | 新增 4 个测试 |

**新增 9 个文件，修改 2 个文件，未删除任何代码。**

---

## 2. 关键实现决策（含实测依据）

### 2.1 为什么必须有桥接进程

设计文档假设「envd 作为第四种传输」，实施时发现更具体的事实：

- `remotetargets.expandOne` 的输出类型是 `mcpconfig.MCPConfig`（`Type`/`Command`/`Args`）；
- envd 是 Connect-RPC daemon，**不提供 MCP 端点**；
- 因此 `Type: "stdio"` + `Command: <bridge>` + `Args: [...envd 地址、user、workspace、profile...]`。

桥接进程因此成为**唯一能施加策略的位置**。这一点在代码注释里反复写明，因为它是本方案最容易出错的地方。

### 2.2 鉴权：HTTP Basic，不是自定义头

**实测结论**（推翻了最初的假设）：

```
无 Authorization  → 401 {"code":"unauthenticated","message":"no user specified"}
Authorization: Basic base64("user:") → 200 + 真实数据
```

- 用户名 = envd 要采用的**系统用户**（不是 token 之前的占位符）；
- 密码槽位在 envd 未启用 token 时**不校验**；
- 报错文案说 "user" 而非 "token"，所以之前猜 `X-Access-Token` / `X-User` / `Username` 等头全部无效。

代码里 `Config.User` 因此是**必填**，`Dial` 会在本地先拒绝空 user，把「运行到第一次工具调用才 401」提前成配置错误。

### 2.3 流式事件：外层 `{"event":{...}}`，输出是 base64

**实测结论**（推翻了「字段名不可知」的保守设计）：

```json
{"event":{"start":{"pid":645}}}
{"event":{"data":{"stdout":"aGVsbG8tZW52ZAo="}}}   ← base64，解码为 "hello-envd\n"
{"event":{"end":{"exited":true,"status":"exit status 0"}}}
```

- `exit_code` 是 `omitempty`，**成功时整个字段消失**；
- 因此 `EndEvent.ExitStatus()` 在数值为 0 时回退解析 `status` 字符串里的 `"exit status N"`，否则非零退出会被误报为成功。这个回退有专门的测试 `TestProcessEventEndStatusNonZero`。

### 2.4 文件写入：envd 没有写 RPC

**实测结论**：`filesystem.Filesystem` 只有 `Stat` / `ListDir` / `MakeDir` / `Remove` / `Move` / `CreateWatcher` / `WatchDir` / `GetWatcherEvents` / `RemoveWatcher`。**没有 ReadFile / WriteFile**（探测返回 404）。

写入因此走进程，且因为明文 envd 只协商到 HTTP/1.1（stdin 通道需要 HTTP/2），内容**作为位置参数**传递：

```sh
sh -c 'set -e; d=$(dirname -- "$1"); [ -d "$d" ] || mkdir -p -- "$d"; printf "%s" "$2" > "$1"' \
   aicli-envd-write "$target" "$content"
```

这样**不做任何调用方数据的引号拼接**，避免命令注入。代价是 argv 上限，所以内容硬上限 1 MiB。

### 2.5 `Entry.Size` 是字符串

```json
{"name":"tmp","type":"FILE_TYPE_DIRECTORY","path":"/tmp","size":"4096","mode":511,"permissions":"dtrwxrwxrwx"}
```

`size` 是字符串 `"4096"`，`mode` 是数字 `511`。用普通 `int64` 字段会解码失败。`Entry.UnmarshalJSON` 同时接受字符串与数字两种形式，`SizeBytes()` 提供数值。

### 2.6 路径 jail 在桥接进程内

`bridge.resolve()` 是唯一收口点：

- 相对路径 → 拼到 workspace 再 `path.Clean`；
- 绝对路径 → 直接 `path.Clean`；
- 然后检查是否等于或前缀匹配 workspace / 任一 `--allow-root`；
- 不匹配即拒绝。

`Clean` 在前，所以 `workspace/../../etc` 会被折叠成 `/etc` 后被拒。**`move` 的两端都过 jail**（只查源端会允许写到外部）。`shell` 的 `cwd` 也过 jail。

### 2.7 profile 分级沿用现有语义

| profile | 暴露工具 |
|---|---|
| `readonly` | view, ls, glob, grep, host_metrics |
| `standard` | + write, mkdir, remove, move |
| `ops` | + shell |

`shell` 只在 `ops`，因为 envd 侧**没有任何命令过滤**。

---

## 3. 验证记录

### 3.1 类型检查（真实源码 + 桩依赖）

沙箱 go1.24.4，项目要求 go1.26.0，且无外网 → 无法整模块编译。做法：把真实源码复制到独立模块，只对 `gopkg.in/yaml.v3` 打一个**类型桩**（`Unmarshal`/`Marshal`/`Node`/`Decode`/Kind 常量），其余全部真实代码。

```
go vet ./...    → rc=0
go build ./...  → rc=0
```

覆盖：`internal/remotetargets`、`internal/mcp/config`、`internal/aiclipaths`、`internal/envdclient`、`cmd/aicli-envd-bridge`。

**这一步发现了 5 个真实错误**（全部修正）：

| 错误 | 性质 |
|---|---|
| `toolList` 与目录列举方法重名 | 编译冲突 |
| `Expand()` 被我当成返回切片（实为 `*Expanded`，含 `Servers` map） | API 误判 |
| `ExecutionScope` 常量实为 `mcpconfig.ExecutionScopeRemote` | 符号名错误 |
| `Spec` 已有 `TokenEnv()` 方法，字段不能同名 → 改 `EnvDTokenEnv` | 命名冲突 |
| 测试漏设 `Remote.Workspace`，`expandOne` 前置校验失败 | 测试缺陷 |

> 说明：**没有整模块 `go test ./...`**。桩只替代 yaml 的类型，不含其运行时行为；`remotetargets` 其余测试依赖 yaml 反序列化，因此只跑了 envd 相关测试。

### 3.2 envdclient 测试

**21 个测试（15 离线 + 6 live）全部通过。**

单元测试（离线）：信封往返、截断头不当成 EOF、超长长度前缀被拒、start/data/end 三种事件解码、base64 解码、`exit_code` 缺省回退、entry 两种 size 形式、真实 payload 解码、ConnectError 类型化、地址归一化、空 user 被拒。

集成测试（对真实 envd `127.0.0.1:49983`，6 项全过）：

```
TestLiveMetrics              → 3 cpus, 4159 MiB ram, disk 数据
TestLiveRunEcho              → "hello-envd"，exit 0
TestLiveRunExitCode          → exit 7 被正确报告
TestLiveStatAndList          → /tmp 是目录，permissions 非空，列出真实条目
TestLiveNotFound             → not_found ConnectError
TestLiveBadUserIsUnauthenticated → unauthenticated ConnectError
```

### 3.3 remotetargets 测试

```
TestParseSpecEnvD            → 4 个子用例（默认端口/显式端口/workspace 路径/非法端口）全过
TestExpandSpecEnvD           → 解析出的 spec 能展开成 remote scope 的 server
TestExpandOneEnvDRequiresUser → 无 user 时在展开阶段失败
TestExpandOneEnvDBridge      → 生成 stdio 条目、命令是本地 bridge、参数含地址/user/profile、warning 声明 envd 不做检查
```

### 3.4 端到端（真实 envd + 真实桥接二进制）

用 MCP JSON-RPC 驱动编译出的 `aicli-envd-bridge`：

```
initialize                    → 返回 protocolVersion / capabilities / serverInfo
tools/list                    → 按 profile 返回工具清单
ls {"path":"."}               → 真实目录列表
host_metrics                  → 真实 CPU/内存/磁盘
write sub/hello.txt           → 写入成功，回读 Stat 确认字节数
view sub/hello.txt            → 内容正确
shell "id -un; pwd"           → 真实用户与工作目录
```

### 3.5 安全验证

| 尝试 | 结果 |
|---|---|
| `view {"path":"../../../etc/passwd"}` | **拒绝**：outside the workspace |
| `shell {"command":"echo hi","cwd":"/etc"}` | **拒绝**：cwd outside the workspace |

---

## 4. 已知限制（不要当成已解决）

### 4.1 明文 envd 只支持 HTTP/1.1

`SendInput` / `SendSignal` / `UpdatePty` 是 client-streaming，需要 HTTP/2。沙箱内 h2c 协商失败（实际仍是 HTTP/1.1），服务端对这些方法返回 **501**。

客户端**显式检测** `ProtoMajor()` 并返回可操作错误，而不是挂起：

> `envdclient: SendInput requires HTTP/2 but the connection negotiated HTTP/1; enable TLS or use an h2c-capable transport`

**影响**：交互式 stdin（REPL）、信号中断、PTY resize 目前不可用。桥接因此没有暴露 `stdin` 类工具。

**解法**：给 envd 配 TLS（`--ca-file` + `Config.TLS`），HTTP/2 会自动协商。这条路径代码已就绪，但**未在真机验证过**（沙箱内 envd 是明文）。

### 4.2 WatchDir 事件结构未确认

`WatchDir` 是长流，读到超时只能证明连接保持，**拿不到事件体的字段名**。`WatchEvent` 因此保留 `Raw`，只暴露约定的 `Name`/`Path`/`Op`/`Type`，注释里写明「以 Raw 为准」。

**未暴露 `watch` 工具**：结构不确定时不对外提供工具，比提供一个会瞎报的工具好。

### 4.3 写入上限 1 MiB

受 argv 限制。超限明确报错并建议改用 `shell`。若需大文件写入，正确解法是启用 TLS/HTTP2 后走 stdin。

### 4.4 没有整模块回归

见 3.1 说明。合入后应在本机（go1.26 + 有网络）跑一次 `go test ./...`。

### 4.5 未做：工具名前缀的实际注册

`--tool-prefix` 已透传，桥接会在 `tools/list` 里返回带前缀的名字。但**未验证 `tools.Manager` 是否接受**这一前缀形式（需要整模块运行）。当前默认不传前缀（空字符串）。

---

## 5. 使用方式

### 5.1 目标机准备

```bash
# 目标机安装 envd（E2B 开源，Apache-2.0）
# 默认监听 49983，以某个系统用户运行
```

### 5.2 本机配置

用 `envd://` 单串描述目标：

```
envd://user@10.0.0.9                  # 只读，默认端口 49983
envd://user@10.0.0.9#ops              # ops 授权
envd://user@10.0.0.9:51000/srv/app    # 显式端口 + workspace
```

- `user` 是**目标机上的系统用户**（envd 用它执行）；
- token 从环境变量取（默认 `AICLI_ENVD_TOKEN`，可用 `token_env=` 指定变量名），**不写进 spec**，作为 `X-Access-Token` 头发送（envd 未启用令牌时该头被忽略）；
- workspace 来自 spec 路径，也可用 target 配置覆盖。

> 注（2026-10-28）：envd 目标的 workspace **必须为绝对路径**；无 spec 路径且 target 覆盖也未给出时默认 `.`，展开会直接报错；bridge 启动时同样校验（fail-closed）。

### 5.3 构建

```bash
cd backend
go build ./cmd/aicli-envd-bridge     # 桥接（需在 PATH 或配置 Remote.Binary 指向）
```

### 5.4 直接运行桥接（调试）

```bash
aicli-envd-bridge \
  --addr 10.0.0.9:49983 \
  --user dev \
  --workspace /srv/app \
  --profile readonly
```

然后按 MCP stdio 协议发 JSON-RPC（每行一条）。

---

## 6. 合入检查清单

- [x] `internal/envdclient` 7 文件，gofmt 干净
- [x] `cmd/aicli-envd-bridge` 2 文件，gofmt 干净
- [x] `remotetargets` 2 处修改 + 1 个测试文件
- [x] 类型检查通过（vet + build，桩 yaml）
- [x] 21 个 envdclient 测试通过（15 离线 + 6 live）
- [x] 4 个 remotetargets envd 测试通过
- [x] 端到端真机验证通过
- [x] 路径穿越与越界 cwd 均被拒
- [ ] **待办**：本机 go1.26 环境跑 `go test ./...` 全量回归
- [ ] **待办**：TLS/HTTP2 路径真机验证（解锁 stdin/signal/pty）
- [ ] **待办**：确认 `tools.Manager` 对 prefixed tool 名的处理

---

## 7. 一句话总结

按设计实施了，但在实施中**用真实 envd 推翻了三个设计假设**（鉴权方式、写文件方式、事件字段名），并按实测结果落地；`envd` 现在是 `remotetargets` 的第四种传输，策略由本地桥接进程强制，envd 只负责执行。

---

## 8. 2026-10-28 复核与修订

本节为 2026-10-28 的复核补记；§1–§7 保留 2026-10-27 实施时的原始记录。

### 8.1 复核结果（本机 go1.27.1）

```text
go build（envdclient / remotetargets / aicli-envd-bridge / aicli） → 通过
go test ./internal/envdclient         → 16 PASS + 6 live SKIP
go test ./internal/remotetargets      → 27 PASS
go test ./cmd/aicli-envd-bridge       → 10 PASS（2026-10-28 新增）
────────────────────────────────────────────────────────────
合计                                    53 PASS / 0 FAIL / 6 SKIP
gofmt -l（envdclient / bridge / remotetargets 相关文件） → 干净
```

- live envd 在 `127.0.0.1:49983` **不可达**（复核环境无沙箱隧道），6 个 live 测试全部 **SKIP**；live 测试由 `ENVD_TEST_ADDR` 门控，只有设为目标主机地址时才会实际执行。§3.2 记录的「6 项全过」是 2026-10-27 沙箱内环境的结果。
- 测试计数更正：§1.1、§3.2 与 §6 清单原写「24 个测试」，实为 **21 个（15 离线 + 6 live）**，正文已更正；2026-10-28 修复后为 16 离线 + 6 live，另加 bridge 10 个。
- 提交状态：复核时上述改动（`internal/envdclient`、`cmd/aicli-envd-bridge`、`internal/remotetargets`）在仓库中均**未提交**；复核后已**单独提交**，不含其它并行改动。

### 8.2 复核期间新增的修复

- **工具名前缀**：bridge 产出的工具名改为 `prefix__name`（双下划线），与 `remotetools` 的前缀形式一致（对 §4.5 遗留项的实现侧修正）。
- **token 传递**：新增 `token_env` query 参数与 `remotetargets.DefaultEnvDTokenEnv` 常量；内联 `?token=` 被**明确拒绝**（实现只接受环境变量名）；token 以 `X-Access-Token` 头发送，envd 经 `/init` 安装访问令牌后即以该头为唯一凭证（2026-10-28 实测确认）。
- **信号退出状态**：进程因信号终止时按 `128 + signum` 解析退出码，不再误报 0。
- **workspace 必须为绝对路径**：envd 目标的 workspace 为相对路径时，`Expand` 阶段直接报错；bridge 启动时也做同样校验，默认 `.` 会 fail-closed。
- **调用超时可配置**：bridge 新增 `--call-timeout`（默认 10m，取代硬编码 60s）；spec 支持 `?call_timeout=30m`，`remote.callTimeout` 透传并在 `Expand` 阶段校验，非法值 fail-closed。
- **输出内存上限**：`envdclient.Run` 每流最多保留 8 MiB（`DefaultMaxBufferedOutput`），超出部分丢弃但继续消费流，避免远端大输出撑爆 bridge 进程。
- **目标名/前缀**：`Spec.TargetName()` 覆盖 envd（取 `sanitize(host)`）；真实巡检暴露的 `remote_remote__*` 前缀已修复为 `remote_<host>__*`（提交 `4a9492f9`）。
- **metrics 展示**：旧版 envd 缺失 `available` 字段时显示 `n/a`，不再误报 `0 MiB`。

### 8.3 保留的限制

- **symlink 可绕过文本级 jail**：jail 只做 `path.Clean` + 前缀匹配，bridge 无法在本地解析远端软链；
- **无 `remotetools` 的 audit 与命令白名单**（envd 路径不经过 remotetools 外壳，见 §2.1）；
- **无法做 envd 版本校验**：envd 没有版本端点；
- **TLS / HTTP/2 未真机验证**（承接 §4.1）；
- **写入硬上限 1 MiB**（承接 §4.3）；
- **watch 事件结构未确认**（承接 §4.2）。

---

## 9. 真实部署与业务场景验证（2026-10-28）

> 操作手册见 `docs/aicli/envd-selfhost-deploy.md`。本节只记录结果与证据。

### 9.1 目标机部署

- Ubuntu 24.04.2 LTS / 4 vCPU / 32 GiB；envd 0.2.11，由公开提交 `fcf15acb` 交叉编译（静态 ELF，sha256 `1688…fb67`）。
- systemd 常驻 + `/init` 访问令牌（`/etc/envd/envd.env`，600 root）+ `ExecStartPost` 重启自动重装；令牌轮换 = 改 env 文件后重启。
- iptables 仅放行回环与运维工作站（`/32`）：实测临时移除放行后客户端访问超时，恢复后 401/200 语义正确；回环始终 204。
- live 集成测试 **6/6 PASS**（Metrics / RunEcho / RunExitCode / StatAndList / NotFound / BadUserIsUnauthenticated）。
- 离线回归：**59 PASS / 0 FAIL / 6 SKIP**（envdclient 18、remotetargets 30、bridge 10，含子用例）。

### 9.2 aicli agent 真实巡检场景

- 方式：`aicli chat --no-interactive --remote 'envd://n9e@<host>:49983/home/n9e?binary=<bridge>#ops'`，`--permission-mode bypass_permissions`，仅使用远端 envd 工具（未使用本地 shell/文件工具）。
- 运行：275 s，`session_20261028074725_2JIY3hBU`，provider workbuddy / deepseek-v4.1-flash（reasoning=max），消耗 **1,019,489 tokens**。
- 交付：远程报告 `/home/n9e/reports/n9e-inspection-20261028-0750.md`（14,049 字节），经 envd `view` 回读校验并通过 bridge 取回本地。
- 关键发现：4 核负载 `3.02/3.34/3.08`；`n9e v2` 迁移 `Error 1091` + 6–8 s 慢 SQL + `17001` 被旧实例占用导致 panic；系统 `degraded`（`systemd-networkd-wait-online`）；`syslog` 4.74 GiB 且持续增长。
- 暴露缺陷：工具前缀退化为 `remote_remote__*`；已修复并加回归测试（见 §8.2）。

### 9.3 成本提示

- 单次巡检约 102 万 tokens，主要来自 reasoning=max 与多轮命令编排。日常巡检建议：固定采集脚本 + 低 reasoning/预算门控，agent 仅做分析与写报告。

---

*所有协议结论均来自对运行中 envd（v0.2.11）的实测探测，非文档推测。*
