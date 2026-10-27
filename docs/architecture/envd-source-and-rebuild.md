# envd 源码溯源与复刻指南：它就是 E2B 的开源组件

> 结论：**不需要从零复刻，源码公开且可商用（Apache-2.0）。**
> 溯源证据：沙箱内 `/usr/bin/envd` 的 Go buildinfo
> 仓库：`github.com/e2b-dev/infra`（现重定向至 `e2b-dev/runtime`）→ `packages/envd`
> 许可证：Apache License 2.0（Copyright 2023 FoundryLabs, Inc.）
> 更新日期：2026-10-27

---

## 1. 决定性证据：二进制里的模块路径

不需要猜测。Go 二进制会嵌入完整的构建元数据，`go version -m` 可直接读出：

```bash
$ go version -m /usr/bin/envd

/usr/bin/envd: go1.26.1
    path    github.com/e2b-dev/infra/packages/envd
    mod     github.com/e2b-dev/infra/packages/envd  v0.0.0-20250905082429-862d9d00e3f4+dirty
    dep     connectrpc.com/authn        v0.1.0
    dep     connectrpc.com/connect      v1.18.1
    dep     connectrpc.com/cors         v0.1.0
    dep     github.com/creack/pty       v1.1.23
    dep     github.com/e2b-dev/fsnotify v0.0.1
    dep     github.com/go-chi/chi/v5    v5.2.2
    dep     github.com/rs/zerolog       v1.34.0
    dep     github.com/rs/cors          v1.11.1
    dep     github.com/shirou/gopsutil/v4  v4.25.6
    dep     github.com/oapi-codegen/runtime v1.1.1
    dep     github.com/dchest/uniuri    v1.2.0
    dep     google.golang.org/protobuf  v1.36.6
    build   -ldflags=-X=main.commitSHA=test
    build   CGO_ENABLED=0
    build   vcs.revision=862d9d00e3f48ec37e719438ffdd194df1116306
    build   vcs.time=2025-09-05T08:24:29Z
```

**四个关键信息**：

| 字段 | 值 | 含义 |
|---|---|---|
| `path` | `github.com/e2b-dev/infra/packages/envd` | **模块的完整路径，等于源码地址** |
| `CGO_ENABLED=0` | 纯 Go 静态链接 | 可跨发行版直接运行，无需 glibc |
| `vcs.revision` | `862d9d00e3f4...` | 精确到提交的源码版本 |
| `vcs.time` | `2025-09-05T08:24:29Z` | 该提交的时间 |

所以 `envd` **不是自研黑盒，而是 E2B（FoundryLabs）的开源基础设施组件**。

---

## 2. 三个直接答案

| 问题 | 答案 |
|---|---|
| 有没有源代码？ | ✅ 有，完全开源 |
| 在哪里？ | `https://github.com/e2b-dev/infra/tree/main/packages/envd` |
| 能商用吗？ | ✅ 能，Apache-2.0，只需保留版权与许可声明 |

---

## 3. 依赖清单反推实现原理

`go version -m` 的依赖列表，几乎逐条对应我们此前实测到的功能：

| 依赖 | 版本 | 对应能力（实测佐证） |
|---|---|---|
| `connectrpc.com/connect` | v1.18.1 | **Connect 协议**（`/process.Process/Start` 那套 RPC） |
| `connectrpc.com/authn` | v0.1.0 | **注入式身份鉴权**（`no user specified` → 401） |
| `connectrpc.com/cors` | v0.1.0 | 跨域支持 |
| `github.com/creack/pty` | v1.1.23 | **伪终端**（`ProcessConfig.pty`、`cols`/`rows`） |
| `github.com/e2b-dev/fsnotify` | v0.0.1 | **文件监听**（`WatchDir`、13 类 inotify 事件） |
| `github.com/go-chi/chi/v5` | v5.2.2 | HTTP 路由（`/health`、`/metrics`） |
| `github.com/shirou/gopsutil/v4` | v4.25.6 | **CPU/内存/磁盘指标**（`/metrics` 的 7 个字段） |
| `github.com/rs/zerolog` | v1.34.0 | 结构化日志 |
| `github.com/oapi-codegen/runtime` | v1.1.1 | OpenAPI 生成（HTTP 端点） |
| `github.com/dchest/uniuri` | v1.2.0 | 生成 `watcher_id` / 进程 tag 等短随机串 |

**这解释了此前所有实测现象**：

- 为什么是 `/process.Process/Start` 而非 REST → `connectrpc.com/connect`；
- 为什么缺 Content-Type 返回 415 → Connect 协议规范；
- 为什么 401 说「没有指定用户」而非「token 无效」→ `connectrpc.com/authn` 的身份中间件；
- 为什么 `WatchDir` 返回 200 流式 → `e2b-dev/fsnotify` 的 fork，专为流式推送改造；
- 为什么 `envd` 是静态二进制 → `CGO_ENABLED=0`。

---

## 4. 仓库结构（envd 相关）

```
e2b-dev/infra  (现 e2b-dev/runtime)
├── LICENSE                      Apache-2.0
├── packages/
│   ├── envd/                    ★ 本文主角
│   │   ├── spec/                ← API 定义（proto + OpenAPI）
│   │   ├── pkg/
│   │   │   ├── version.go       ← 版本号（release-please 自动维护）
│   │   │   └── ...              进程 / 文件系统 / 指标实现
│   │   ├── Makefile             ← make generate / start-docker / run-debug
│   │   └── ...
│   ├── orchestrator/            ← 编排层（Firecracker 微虚拟机）
│   ├── api/                     ← 控制面 API
│   ├── client-proxy/            ← 客户端代理（对应我们看到的「中继」）
│   ├── shared/                  ← 共享库
│   └── ...
├── iac/                         ← Terraform（GCP / AWS）
└── spec/                        ← 全局 API 规范
```

从仓库 README 可确认的关键工作流：

```bash
make init-generate    # 安装生成 server stubs 的依赖
make generate         # 改完 ./spec/ 后重新生成服务端代码
make start-docker     # 构建 envd 并在 Docker 容器中启动
make run-debug        # 带调试启动，用 Delve 连 2345 端口
```

> README 原文：*"Daemon that runs inside a sandbox that allows interacting with the sandbox via calls from the SDK."*
> —— 这正是我们实测到的定位：**运行在沙箱内、供 SDK 远程调用的守护进程**。

---

## 5. 复刻路径：三条，按工作量排序

### 路线 A：直接用（推荐，30 分钟）

源码现成，构建即得：

```bash
# 1) 克隆（仓库较大，建议浅克隆）
git clone --depth 1 https://github.com/e2b-dev/infra.git
cd infra/packages/envd

# 2) 看当前版本
cat pkg/version.go

# 3) 直接在 Docker 里跑起来
make start-docker

# 4) 验证
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:49983/health   # 期望 204
curl -s http://127.0.0.1:49983/metrics
```

这就是「复刻」——因为你复刻的对象本来就是它。

### 路线 B：只取 envd，不要整套基础设施（半天）

`envd` 是独立 Go module，可单独构建：

```bash
cd infra/packages/envd

# 纯 Go 构建，无需 CGO
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags="-s -w -X=main.commitSHA=$(git rev-parse HEAD)" \
  -o envd ./cmd/envd

# 检查产物
file envd          # 应为 statically linked
./envd --help
```

放进任意 Linux 主机或容器：

```bash
# 直接跑（注意需要 root，因为它要 setuid 到指定 user）
sudo ./envd --help

# 或用 systemd 托管（对应原架构的 s6）
cat > /etc/systemd/system/envd.service <<'EOF'
[Unit]
Description=envd sandbox daemon
After=network.target

[Service]
ExecStart=/usr/local/bin/envd
Restart=always
RestartSec=2
LimitNOFILE=1000000

[Install]
WantedBy=multi-user.target
EOF
systemctl enable --now envd
```

### 路线 C：自研等价实现（1–2 周）

若不想用 Go，或只需部分能力，按 proto 自己实现。核心就是两个 Connect 服务：

```proto
syntax = "proto3";
package process;

service Process {
  rpc Start   (StartRequest)   returns (stream StartResponse);
  rpc List    (ListRequest)    returns (ListResponse);
  rpc Connect (ConnectRequest) returns (stream ConnectResponse);
  rpc SendInput   (SendInputRequest)   returns (SendInputResponse);
  rpc StreamInput (stream StreamInputRequest) returns (StreamInputResponse);
  rpc SendSignal  (SendSignalRequest)  returns (SendSignalResponse);
  rpc Update      (UpdateRequest)      returns (UpdateResponse);
}
```

```proto
service Filesystem {
  rpc ListDir  (ListDirRequest)  returns (ListDirResponse);
  rpc Stat     (StatRequest)     returns (StatResponse);
  rpc MakeDir  (MakeDirRequest)  returns (MakeDirResponse);
  rpc Move     (MoveRequest)     returns (MoveResponse);
  rpc Remove   (RemoveRequest)   returns (RemoveResponse);
  rpc WatchDir (WatchDirRequest) returns (stream WatchDirResponse);
  rpc CreateWatcher   (CreateWatcherRequest)   returns (CreateWatcherResponse);
  rpc GetWatcherEvents(GetWatcherEventsRequest) returns (GetWatcherEventsResponse);
  rpc RemoveWatcher   (RemoveWatcherRequest)   returns (RemoveWatcherResponse);
}
```

> 上面的 proto 是根据**实测方法名与字段**反推的近似定义，字段编号与选项需以仓库 `packages/envd/spec/` 为准。

用 Go 实现的最小骨架（`Start` 为例）：

```go
func (s *processServer) Start(ctx context.Context, req *connect.Request[pb.StartRequest],
	stream *connect.ServerStream[pb.StartResponse]) error {

	cfg := req.Msg.Process
	cmd := exec.Command(cfg.Cmd, cfg.Args...)
	cmd.Dir = cfg.Cwd
	cmd.Env = mergeEnv(cfg.Envs)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if cfg.Pty != nil {              // PTY 模式
		ptmx, _ := pty.Start(cmd)
		defer ptmx.Close()
		buf := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				stream.Send(&pb.StartResponse{
					Event: &pb.StartResponse_Data{Data: &pb.Data{Output: &pb.Data_Pty{Pty: buf[:n]}}},
				})
			}
			if err != nil { break }
		}
	} else { ... }                   // 管道模式：stdout / stderr 分两路

	stream.Send(&pb.StartResponse{Event: &pb.StartResponse_End{End: &pb.End{ExitCode: int32(exitCode)}}})
	return nil
}
```

自研时**必须对齐的三件事**（否则语义不等价）：

| 要点 | 原因 |
|---|---|
| `bash -l -c`（login shell） | 才会加载 `/etc/profile`，`PATH` 才完整 |
| `prlimit --fsize=...` 包装 | 资源硬限制随 fork 继承，命令无法绕过 |
| `Setpgid: true` | 独立进程组，超时才能整组 kill 干净 |

---

## 6. 完整架构里其他组件的源码位置

`envd` 只是沙箱内的一环。若要复刻**整套**（含快照、调度、中继），对应关系：

| 我们的实测组件 | 仓库中的位置 | 作用 |
|---|---|---|
| `envd`（PID 42） | `packages/envd` | 沙箱内执行代理 |
| 中继 `10.133.x.x` | `packages/client-proxy` | 客户端接入代理 |
| 云端控制面 | `packages/api` | 沙箱生命周期 API |
| Firecracker 编排 | `packages/orchestrator` | 微虚拟机调度与快照 |
| 数据面/模板 | `packages/shared` | 共享库 |
| 基础设施 | `iac/` | Terraform（GCP/AWS） |

`orchestrator` 的官方描述直接印证了我们此前的推测：

> *"Firecracker microVMs that resume from a snapshot, run untrusted agent code, and pause when the agent stops."*

—— 与 `sandbox-selfhost-linux-feasibility.md` 里「快照池 + 预热」的判断一致。

---

## 7. 许可证与合规要点

**Apache License 2.0**，Copyright 2023 FoundryLabs, Inc.

可以：

- ✅ 商业使用
- ✅ 修改、二次分发
- ✅ 私有化部署
- ✅ 作为 SaaS 对外提供服务（不触发 copyleft）

必须：

| 义务 | 具体做法 |
|---|---|
| 附许可副本 | 分发时带上 `LICENSE` 文件 |
| 标注修改 | 改过的文件加显著说明「已修改」 |
| 保留声明 | 留存原版权、专利、商标、归属声明 |
| NOTICE 文件 | 若原仓库有 `NOTICE`，需一并转达 |
| 不得用其商标 | 不能用 "E2B" 作为自己产品名 |

> Apache-2.0 **不是** copyleft，不需要开源你自己的修改；仅在分发二进制/源码时附带义务。SaaS 内部使用无额外义务。

---

## 8. 与此前文档的印证关系

源码证据**反向验证**了此前基于黑盒实测得出的所有结论：

| 此前实测结论 | 源码/依赖佐证 | 是否吻合 |
|---|---|---|
| 协议是 Connect/gRPC-over-HTTP | `connectrpc.com/connect v1.18.1` | ✅ |
| 鉴权是「身份注入」而非 token | `connectrpc.com/authn v0.1.0` | ✅ |
| 支持 PTY 交互终端 | `github.com/creack/pty v1.1.23` | ✅ |
| 文件监听基于 inotify | `github.com/e2b-dev/fsnotify v0.0.1` | ✅ |
| `/metrics` 来自 procfs 解析 | `github.com/shirou/gopsutil/v4` | ✅ |
| `-commit` 返回 `test` | `-ldflags=-X=main.commitSHA=test` | ✅ |
| 静态二进制、无 glibc 依赖 | `CGO_ENABLED=0` | ✅ |

**零矛盾**。黑盒分析路径与源码事实完全一致，说明此前的推断方法可靠。

---

## 9. 最小可行验证清单

克隆后 30 分钟内可完成的验证：

```bash
# 1) 确认源码版本与沙箱内一致
cd infra/packages/envd && cat pkg/version.go        # 期望看到 0.2.11

# 2) 构建
CGO_ENABLED=0 go build -o envd ./cmd/envd

# 3) 对比构建信息（应与沙箱内高度相似）
go version -m ./envd | head -5

# 4) 启动并验证端点
./envd & sleep 2
curl -s -o /dev/null -w "health=%{http_code}\n" http://127.0.0.1:49983/health    # 204
curl -s http://127.0.0.1:49983/metrics | head -c 200

# 5) 验证 Connect 协议特征（缺 Content-Type 应 415）
curl -s -o /dev/null -w "no-ct=%{http_code}\n" -X POST \
  -H "Content-Type: text/plain" -d '{}' http://127.0.0.1:49983/process.Process/List   # 415
```

---

## 10. 未确认项

1. **仓库是否已从 `infra` 改名为 `runtime`**：搜索结果显示 `e2b-dev/runtime` 与 `e2b-dev/infra` 并存，后者可能重定向；两个地址都应尝试。
2. **沙箱内版本 `0.2.11` 是否为最新**：二进制构建于 2025-09-05，当前主分支可能更新。`pkg/version.go` 为权威来源。
3. **`+dirty` 标记**：模块版本带 `+dirty`，说明构建时工作区有未提交改动，沙箱镜像可能打了本地补丁。无法确认补丁内容。
4. **`spec/` 目录的确切文件结构**：未直接读取目录列表，proto 文件命名与分包方式需以仓库为准。
5. **Tabbit 是否还加了额外的自研层**：从端口（49983）、方法名、依赖看，`envd` 本身与上游一致；但 `tabbit-fuse`（19780）**不在 E2B 仓库中**，那一层应为自研或另用他库。
6. **`packages/envd` 是否可单独 `go get`**：它是 monorepo 子模块，`go get` 可能需要完整克隆。

---

## 11. 一句话总结

**别复刻，直接用。** `envd` 是 `github.com/e2b-dev/infra/packages/envd`，Apache-2.0，`make start-docker` 就能跑起来。

需要自研的只有 `tabbit-fuse` 那一层（19780 端口，FUSE + 反向 WebSocket）——它不在 E2B 仓库里，而这层的开源替代品（SSHFS / frp / Tailscale）已经足够成熟，见 `sandbox-selfhost-linux-feasibility.md` 第 4 节。

---

## 12. 关联文档

- `envd-capabilities.md` —— 16 个方法的功能全解
- `sandbox-command-execution-channel.md` —— 命令通道链路
- `sandbox-local-folder-mount-mechanism.md` —— FUSE 挂载机制
- `sandbox-selfhost-linux-feasibility.md` —— 自建整体方案
