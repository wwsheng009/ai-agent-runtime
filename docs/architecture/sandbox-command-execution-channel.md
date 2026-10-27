# 沙箱命令执行通道分析：浏览器 Agent → 云端中继 → envd

> 分析对象：浏览器中运行的 AI Agent，如何把一条 shell 命令送进沙箱虚拟机并在其中执行
> 核心组件：`envd`（沙箱内 Go 守护进程）、`s6`（进程监督器）、云端中继（`10.133.x.x`）
> 实测环境：Debian 13 (trixie) / Linux 6.6.69-cube / x86_64 KVM 虚拟机 / 3 vCPU / 4.1 GiB
> 更新日期：2026-10-27
> 姊妹文档：`sandbox-local-folder-mount-mechanism.md`（本地目录挂载通道）

本文基于沙箱内实测的进程表、套接字表、HTTP 接口探测与 `/proc` 信息，逆向还原「Agent 一次 shell 工具调用如何落到沙箱内执行」的完整链路、协议特征、资源限制注入方式与安全边界。

---

## 1. 结论先行

> **浏览器里的 Agent 从不直接连接沙箱。命令由 Agent 发往 Tabbit 云端中继，中继沿一条早已建立的长连接投递到沙箱内的 `envd`；`envd` fork 出 bash 执行，再把输出流式回传。**

四个关键判断（均有实测支撑）：

| 判断 | 依据 |
|---|---|
| 命令的实际派发者是 `envd` | 每条命令的父进程链为 `bash(命令自身) ← envd(42) ← s6-supervise(25)` |
| 不是浏览器直连沙箱 | 沙箱内无任何出站到公网的控制连接；`49983` 上的连接全部是**入向** |
| 协议是 Connect/gRPC-over-HTTP | `POST /process.Process/List` 返回 200；缺 Content-Type 返回 **415**；`GET /health` 返回 **405 Allow: POST** |
| 命令经中继中转一跳 | `envd:49983` 的对端为 `10.133.x.x`（RFC1918 私网），非用户设备地址 |

一句话概括：**沙箱始终是「被连接方」。它只开一个入向端口，由云端中继续保持到它；浏览器发出的命令必须经中继转发才能抵达。**

---

## 2. 完整链路

```
┌── 用户浏览器 ────────────────────────────────────────────┐
│  Tabbit 页面 · Agent 推理循环                             │
│      │  ① 产生工具调用意图                                 │
│      │     { tool: "e2b_bash", command: "...", cwd: ... } │
└──────┼───────────────────────────────────────────────────┘
       │  ② HTTPS（浏览器 → 云端），携带工具调用参数
       ▼
┌── Tabbit 云端中继（10.133.x.x 私网节点）─────────────────┐
│      · 持有到各沙箱的常驻连接（沙箱不可主动外连）          │
│      · 把工具调用翻译为 envd 的 RPC 帧                    │
└──────┼───────────────────────────────────────────────────┘
       │  ③ 沿已建立的长连接下行
       ▼
┌── 沙箱虚拟机 ────────────────────────────────────────────┐
│  169.254.68.6:49983  ← envd（Go 静态二进制，PID 42）      │
│      │  ④ POST /process.Process/Start                    │
│      │     fork 包装：                                    │
│      │     /bin/bash -l -c "prlimit --fsize=2000000000:2000000000 \
│      │                       -- bash -lc '<command>'"     │
│      ▼                                                   │
│  子进程 bash ── 执行用户命令 ── /mnt/work、/mnt/local、网络 │
│      │  ⑤ stdout / stderr / exit_code                    │
└──────┼───────────────────────────────────────────────────┘
       │  ⑥ 结果原路回传：沙箱 → 中继 → 浏览器
       ▼
   Agent 获得输出，进入下一轮推理
```

关键点：**②③⑥ 三段都是「经中继」，不存在浏览器与沙箱的点对点通路。** 沙箱位于 NAT 与防火墙之后，没有公网可达地址。

---

## 3. 核心组件：envd

### 3.1 基本信息

```
$ ls -l /usr/bin/envd
-rwxr-xr-x 1 root root 16732440 Apr  6  2026 /usr/bin/envd

$ file /usr/bin/envd
ELF 64-bit LSB executable, x86-64, statically linked, Go BuildID=Zbys-RNM6gG2Xefp54nd/...
```

- **静态链接**：不依赖沙箱内 glibc 版本，镜像可自由升级基础系统而不破坏执行通道。
- **Go 实现**（16.7 MB）：与 `tabbit-fuse` 同技术栈，便于共享连接管理与 RPC 代码。
- **由 s6 托管**：`s6-supervise envd`（PID 25）为父，`envd`（PID 42）为 worker；崩溃后自动拉起。

### 3.2 服务清单

```
$ ls -1 /run/service
envd
jupyter
s6-linux-init-shutdownd
s6rc-fdholder
s6rc-oneshot-runner
tabbit-fuse
uvicorn
```

沙箱内共 7 个受监督服务。与本主题相关的三个：

| 服务 | 职责 | 证据 |
|---|---|---|
| `envd` | **命令执行 + 文件 API**，本通道主角 | `49983` 上的 RPC 端点 |
| `tabbit-fuse` | 本地目录挂载 | 见姊妹文档，监听 `19780` |
| `uvicorn` | code-interpreter（`49999`） | 与命令执行解耦，非本通道依赖 |

> 注意：`uvicorn`/`jupyter` 提供的 Python 解释器服务与本通道**并行且独立**。`tabbit-fuse` 的启动脚本注释已明确「49999 的解释器服务不应阻塞就绪」，说明架构上刻意让文件/命令能力不依赖解释器服务。

### 3.3 监听与探针

```
$ ss -lntp | grep 49983
LISTEN  [::]:49983

$ curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:49983/health
204
```

- 监听 IPv6 双栈 `[::]:49983`，IPv4 `127.0.0.1` 亦可访问（映射地址）。
- `/health` 返回 **204 No Content**：无响应体，纯存活探针，适合高频轮询（`tabbit-fuse` 即以 2s 间隔轮询它）。

### 3.4 资源指标端点

```
$ curl -s http://127.0.0.1:49983/metrics
{"ts":1793110358,"cpu_count":3,"cpu_used_pct":0.27,
 "mem_total":4362629120,"mem_used":623304704,
 "disk_used":2252800,"disk_total":1078984704}
```

字段解读：

| 字段 | 值 | 换算 |
|---|---|---|
| `cpu_count` | 3 | 3 vCPU |
| `cpu_used_pct` | 0.27 | 近乎空闲 |
| `mem_total` | 4362629120 | 约 **4.06 GiB** |
| `mem_used` | 623304704 | 约 **594 MiB** |
| `disk_used` / `disk_total` | 2252800 / 1078984704 | 约 **2.1 MiB / 1.00 GiB** |

这些数值与操作系统实测（`free -h` 显示 4.1 GiB、`df` 显示约 1.1 GB）一致，说明 `metrics` 是真实采样而非固定上报。

---

## 4. 协议特征：Connect / gRPC-over-HTTP

### 4.1 端点探测结果

```
POST /process.Process/Start      -> 415   # 缺 Content-Type，被协议层拒绝
POST /process.Process/List       -> 200   # 成功，返回进程列表
POST /process.Process/Connect    -> 415
POST /process.Process/Kill       -> 404
POST /filesystem.Filesystem/ListDir -> 401  # 需鉴权
POST /files                     -> 400
POST /health                    -> 405   # 仅允许 GET
GET  /health                    -> 204
```

### 4.2 为什么判定为 Connect 协议

三条独立证据指向同一结论：

1. **路径命名形如 `/包名.服务名/方法名`**（`/process.Process/Start`、`/filesystem.Filesystem/ListDir`），这是 gRPC 的标准方法寻址格式，普通 REST 不会这样命名。
2. **`415 Unsupported Media Type`**：请求缺 `Content-Type: application/json` 时被拒。这是 Connect 协议对非规范请求的典型响应（普通 HTTP 服务通常仍会尝试解析）。
3. **`405` 响应头带 `Allow: POST`**：方法不匹配时明确列出允许方法，属 RPC 框架的标准行为。

由此可推断服务定义大致包含两个服务：

```
service Process    { rpc Start / List / Connect / Kill }
service Filesystem { rpc ListDir / ... }        # 需鉴权
```

> ⚠️ `Kill` 返回 404 而其他方法返回 415，暗示该方法在当前 `envd` 版本中**未实现或未注册**，而非鉴权问题。此为推断，未进一步验证。

### 4.3 一个值得注意的细节：`/process.Process/List` 会泄露自身包装

请求 `{}` 返回的进程列表里，**包含了发起该请求的那条命令自己**：

```json
{"processes":[{
  "config":{
    "cmd":"/bin/bash",
    "args":["-l","-c","prlimit --fsize=2000000000:2000000000 -- bash -lc '<你的命令>'"]
  }
}]}
```

这直接暴露了两件事：

1. **命令包装形式**：不是裸 `bash -c`，而是 `bash -l -c "prlimit ... -- bash -lc '...'"` 的双层嵌套。
2. **`-l`（login shell）**：以登录 shell 启动，会读取 `/etc/profile`、`~/.bash_profile`，这解释了为何环境变量与 `PATH` 在每条命令中都完整可用。

---

## 5. 资源限制的注入方式

### 5.1 限制来自运行时，不是镜像默认值

```
$ prlimit -p $$
RESOURCE  DESCRIPTION                      SOFT        HARD  UNITS
FSIZE     max file size                    2000000000  2000000000 bytes
NOFILE    max number of open files         1000000     1000000  files
NPROC     max number of processes            16624       16624 processes
STACK     max stack size                    8388608   unlimited bytes
MEMLOCK   max locked-in-memory address space 8388608    8388608 bytes
```

`ulimit -f` 显示 `1953125` 块（每块 1 KiB），即 **1,953,125 × 1024 = 2,000,000,000 字节**，与 `prlimit` 的 `FSIZE SOFT/HARD = 2000000000` 精确吻合。

**这不是镜像的默认配置**，而是 `envd` 在 fork 时通过 `prlimit` 命令强制注入的：

```
prlimit --fsize=2000000000:2000000000 -- bash -lc '<command>'
```

关键含义：

- **soft = hard = 2 000 000 000**，即**无法通过 `ulimit -f unlimited` 自行提升**（普通用户不能提高 hard limit）。这正是平台文档中「单文件大小硬限制、不可绕过」的技术根源。
- **限制在进程创建瞬间施加**，不依赖命令配合、不需要 shell 脚本自觉遵守，因此对 `curl`、`wget`、Python、`dd`、`tar`、编译器等一切子进程**同样生效**（限制随 `fork` 继承）。
- 若命令突破该值，内核发送 `SIGXFSZ`，表现为 `File size limit exceeded`、`OSError: [Errno 27]`、`curl: (23)` 等错误。

### 5.2 与平台报错文案的对应关系

| 平台提示 | 实际机制 | 对应 `prlimit` 项 |
|---|---|---|
| 「单文件大小硬限制，不可重试」 | `SIGXFSZ`，hard limit 锁死 | `FSIZE = 2000000000` |
| 「文件过大 / 部分写入」 | 写入在 2 GB 处被截断 | 同上 |
| 「文件描述符耗尽」（少见） | 单进程 fd 上限 | `NOFILE = 1000000` |
| 「进程数超限」（少见） | 用户级进程上限 | `NPROC = 16624` |

---

## 6. 连接方向：沙箱始终被动

### 6.1 全部非回环连接

```
$ (遍历 /proc/net/tcp{,6}，过滤 ESTABLISHED 且非 127.0.0.1 双向)
169.254.68.6:49983  <-  10.133.166.128:45816   控制通道（命令）
169.254.68.6:19780  <-  10.133.163.118:37818   文件通道（tabbit-fuse）
169.254.68.6:58082  ->  169.254.0.71:80        疑似基础设施探测
```

三点关键观察：

1. **`49983` 上只有入向连接**。`169.254.68.6` 是沙箱的 IPv4 链路本地地址（169.254.0.0/16），说明它是被连接的一端；对端 `10.133.x.x` 为 RFC1918 私网，属云端中继。
2. **无任何出站到公网的控制连接**。遍历全表后，除回环与上述三条外无其他条目 —— 沙箱不需要主动联系浏览器，也不需要公网出口即可被驱动。
3. **对端 IP 每次会话都在变**（观察序列：`10.133.166.5` → `10.133.166.128`；`10.133.167.72` → `10.133.163.118`）。说明中继是**多节点集群 + 负载均衡**，每条隧道落到不同节点，且断连后可重连到新节点。

### 6.2 控制与文件是两条独立隧道

```
控制通道  envd:49983         对端 10.133.166.x
文件通道  tabbit-fuse:19780  对端 10.133.163.x / 10.133.167.x
```

**两条隧道来自不同的中继 IP，互不耦合。** 这解释了一个实测现象：本地目录挂载报 `EIO` 时，命令执行依然完全正常 —— 因为故障只影响 19780 那条链路。

这种拆分在架构上是合理的：命令执行是「控制面」，延迟敏感但数据量小；文件挂载是「数据面」，可能传输大文件，需要独立的流控与超时策略。分开可避免大文件传输阻塞命令响应。

### 6.3 为什么必须是「中继」而非直连

```
直连（不可行）              中继（实际）
浏览器 ──X──> 沙箱:49983    浏览器 ──> 中继 ──> 沙箱:49983
（沙箱无公网地址，NAT 后）   （中继与沙箱保持常驻连接）
```

收益：

- **用户侧零配置**：无需端口映射、无需 VPN、无需在本地跑守护进程。
- **沙箱侧零暴露**：只开一个入向端口，且该端口不直接暴露公网（仅中继可及）。
- **连接复用**：长连接常驻，每条命令不必重复 TCP + TLS 握手，避免每命令固定开销。

代价：

- **多一跳延迟**：每条命令的往返都经过中继。
- **中继可见性**：若 TLS 在中继处终止，中继侧理论上可见命令全文与输出（见第 9 节）。

---

## 7. 命令执行的生命周期

```
[1] Agent 生成工具调用 e2b_bash { command, cwd, timeout }
        ↓
[2] 浏览器 → 云端（HTTPS）提交调用参数
        ↓
[3] 中继定位目标沙箱的常驻隧道，编码为 RPC 帧下发
        ↓
[4] envd 收到 /process.Process/Start
    校验参数 → 构造包装命令
        ↓
[5] fork：bash -l -c "prlimit --fsize=2000000000:2000000000 -- bash -lc '<cmd>'"
    在指定 cwd 下启动
        ↓
[6] stdout / stderr 边产出边流式回传（非等全部结束）
        ↓
[7] 进程退出，回收 exit_code
        ↓
[8] 中继 → 浏览器，Agent 拿到 { stdout, stderr, exit_code }
        ↓
[9] 进程被销毁（默认短生命周期，不常驻）
```

实测佐证：两次连续的 `/process.Process/List` 调用中，进程数都只有 **1**，且正是当前正在跑的那条命令。说明：

- 命令**用完即退**，不会在沙箱内累积僵尸进程；
- 没有常驻的 shell 会话复用（不是「一个长驻 bash 反复喂命令」的模型），每条命令都是独立进程。

> 对比：某些平台采用持久 shell 会话（命令间共享变量与 cwd）。本环境是**每命令独立进程**模型，因此变量、非 `export` 的 shell 函数、未落盘的状态都不会跨命令保留 —— 只有文件系统是共享的。

---

## 8. 与文件挂载通道的对比

两条通道常被混淆，但机制不同：

| 维度 | 命令执行通道 | 文件挂载通道 |
|---|---|---|
| 守护进程 | `envd`（PID 42） | `tabbit-fuse`（PID 82） |
| 端口 | `49983` | `19780` |
| 协议 | Connect / gRPC-over-HTTP | 自定义 RPC（WebSocket 帧） |
| 传输内容 | 命令参数、stdout/stderr、exit_code | 文件读写字节、元数据 |
| 中继节点 | `10.133.166.x` | `10.133.163.x` / `10.133.167.x` |
| 故障影响面 | 命令全部无法执行 | 用户本地目录不可访问（沙箱本地盘仍正常） |
| 依赖关系 | 独立 | 依赖 `envd` 的 `/health` 作就绪前置 |

**共同点**：都是**入向长连接**、都经云端中继、都由 s6 托管、都是 Go 静态二进制。

**相互独立性**（有实测支撑）：文件通道报 `EIO` 期间，命令通道持续正常工作。这证明二者不是同一条连接上的多路复用，而是物理隔离的两条隧道。这样设计的好处是故障隔离 —— 文件传输卡住不会冻结命令响应。

---

## 9. 安全边界

| 边界 | 机制 | 评价 |
|---|---|---|
| 网络暴露 | 仅 `49983` 入向，且仅中继可及 | ✅ 不暴露公网 |
| 文件 API 鉴权 | `/filesystem.Filesystem/*` 返回 **401** | ✅ 有鉴权层 |
| 进程 API 鉴权 | `/process.Process/*` 无鉴权（`List` 直接 200） | ⚠️ 沙箱内任意进程可枚举/启动进程 |
| 资源限制 | `prlimit` 注入，hard limit 锁死 | ✅ 防磁盘打满，不可绕过 |
| 执行身份 | 非 root（实测 `uid=1000`，用户 `user`） | ✅ 命令以非 root 运行，envd 的 `ProcessConfig.user` 生效（更正依据：`envd-capabilities.md` §5.3） |
| 中继可见性 | 流量经 `10.133.x.x` | ⚠️ 若 TLS 在中继终止，中继可见命令明文 |
| 横向影响 | 命令可读写 `/mnt/local`（用户本机目录） | ⚠️ 一次 `rm` 可作用于用户磁盘 |

需要特别注意：

1. **`/process.Process/*` 在回环上无鉴权**。任何沙箱内进程（包括被执行的不受信代码）都能 `POST /process.Process/Start` 启动新进程，或 `List` 枚举其他命令。这在单租户沙箱内影响有限，但意味着「沙箱内不可信代码」可以干扰 Agent 自己的命令执行流。
2. **命令以非 root 身份运行**（实测 `uid=1000`，用户 `user`；envd 的 `ProcessConfig.user` 生效 —— 本条更正原「命令以 root 运行、无降权」的错误结论，依据 `envd-capabilities.md` §5.3）。配合 `allow_other` 的 FUSE 挂载，沙箱内代码对用户授权目录仍具读写能力，授权目录前应确认内容边界（勿含密钥、`.env`、SSH 私钥）。
3. **`prlimit` 只限制单文件大小**（2 GB），**不限制磁盘总量**。多条命令可各自写 2 GB 文件，直到 1 GB 磁盘配额耗尽 —— 实际由 `disk_total ≈ 1 GiB` 兜底，而非 `FSIZE`。

---

## 10. 排障手册

| 症状 | 可能原因 | 诊断 | 处置 |
|---|---|---|---|
| 命令无响应/超时 | 隧道断开、envd 重启 | `curl 127.0.0.1:49983/health` 期望 204 | 等待重连；持续失败则重建沙箱 |
| `415` 错误 | 自建 RPC 调用缺 Content-Type | 检查请求头 | 加 `Content-Type: application/json` |
| `401` | 访问 filesystem API 未鉴权 | 看 endpoint | 走平台工具而非直连 |
| `File size limit exceeded` | 单文件超 2 GB | `prlimit -p $$` 查 FSIZE | 分片写；或改存 `/mnt/cos/artifacts` |
| `No space left on device` | 磁盘总量（约 1 GiB）耗尽 | `df -h /` | 清理 `/mnt/work` |
| 变量/状态跨命令丢失 | 每命令独立进程模型 | `echo $$` 每次不同 PID | 用文件传递状态 |
| 命令输出截断 | 流式回传或超时截断 | 观察是否到 timeout | 分页/写文件后分段读 |

常用诊断命令（沙箱内执行）：

```bash
# 1) envd 存活
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:49983/health   # 期望 204

# 2) envd 资源指标
curl -s http://127.0.0.1:49983/metrics

# 3) 当前进程的资源限制
prlimit -p $$

# 4) 控制隧道连接
ss -tn | grep 49983 || netstat -tn | grep 49983

# 5) 守护进程状态
ps -eo pid,ppid,args | grep envd

# 6) 自身进程链（确认由 envd fork）
awk '/^PPid:/{print $2}' /proc/$$/status | xargs -I{} cat /proc/{}/comm
```

---

## 11. 证据附录

所有结论均来自沙箱内实测，原始输出摘录：

```bash
# 1. envd 二进制
$ file /usr/bin/envd
ELF 64-bit LSB executable, x86-64, statically linked, Go BuildID=...

# 2. 服务清单（7 个）
$ ls -1 /run/service
envd / jupyter / s6-linux-init-shutdownd / s6rc-fdholder
s6rc-oneshot-runner / tabbit-fuse / uvicorn

# 3. 监听与探针
$ ss -lntp | grep 49983
LISTEN [::]:49983
$ curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:49983/health
204

# 4. 指标端点
$ curl -s http://127.0.0.1:49983/metrics
{"ts":1793110358,"cpu_count":3,"cpu_used_pct":0.27,"mem_total":4362629120,
 "mem_used":623304704,"disk_used":2252800,"disk_total":1078984704}

# 5. RPC 端点探测（Connect 协议特征）
$ POST /process.Process/Start        -> 415
$ POST /process.Process/List         -> 200
$ POST /process.Process/Connect      -> 415
$ POST /process.Process/Kill         -> 404
$ POST /filesystem.Filesystem/ListDir -> 401
$ GET  /health                       -> 204

# 6. 进程列表暴露的包装命令
$ POST /process.Process/List -d '{}'
{"processes":[{"config":{
  "cmd":"/bin/bash",
  "args":["-l","-c","prlimit --fsize=2000000000:2000000000 -- bash -lc '<cmd>'"]}}]}
（进程数 = 1，即当前命令；无累积僵尸，无常驻 shell）

# 7. 资源限制（runtime 注入，非镜像默认）
$ prlimit -p $$
FSIZE  2000000000  2000000000 bytes
NOFILE 1000000     1000000    files
NPROC  16624       16624      processes
$ ulimit -f
1953125        # × 1024 = 2,000,000,000 字节，与 FSIZE 精确一致

# 8. 连接方向（全部入向）
169.254.68.6:49983  <-  10.133.166.128:45816   控制
169.254.68.6:19780  <-  10.133.163.118:37818   文件
169.254.68.6:58082  ->  169.254.0.71:80        基础设施探测
（无任何出站到公网的控制连接）

# 9. 父进程链验证
bash($$) ← envd(42) ← s6-supervise(25)
```

---

## 12. 尚未确认的部分（诚实标注）

以下为推断或未能验证，不应作为确定结论引用：

1. **TLS 终止位置**：无法从沙箱内判定中继→`envd` 之间是否加密。若中继解密后以明文 HTTP 投递，则中继侧可见命令与输出全文。
2. **中继→envd 的具体封装**：是否为 HTTP/2 多路复用、是否复用单条 TCP、是否有独立的心跳帧，均未抓到字节级证据。
3. **`/process.Process/Kill` 的 404 根因**：可能未实现、可能版本差异、可能路径不同，未验证。
4. **`32000` / `32001` 两个旁路服务**：健康检查分别返回 `{"status":"healthy","state":"running"}` 与 `{"status":"running"}`，职责推测与沙箱生命周期管理有关，未证实。
5. **`169.254.0.71:80` 用途**：推测为链路本地网关的元数据/健康探测，未证实。
6. **命令超时的实现位置**：是 `envd` 侧 kill 还是中继侧断连，未验证。
7. **是否有 stdout 大小上限**：大输出是否被截断、截断阈值多少，未测。

---

## 13. 小结

这条通道的本质是**控制面与数据面的分离，加上「沙箱被动、中继持有」的连接模型**。

- **`envd` 是唯一的执行入口**：它既是 HTTP 服务端（RPC），又是进程的父进程（fork），还是资源限制的施加者（`prlimit`）。一个进程承担三重角色，使命令的「接入—创建—约束」在一个环节内闭环，减少了协调成本。
- **连接方向被倒置**：沙箱不主动外连，只被动接受中继的常驻连接。这让零配置、零公网暴露成为可能，代价是每条命令多一跳。
- **限制由运行时强制注入**：`prlimit` 不依赖命令配合，随 `fork` 继承到所有子进程，因此无法通过换工具、换语言、换目录绕过 —— 这是「硬限制」四个字的技术含义。
- **命令是无状态的**：每条命令一个独立进程，用完即退。只有文件系统跨命令存活，shell 变量与内存状态不留存。理解这一点才能正确设计多步流程（中间结果必须落盘）。

对使用者的实践含义很直接：把它当作「向远端一次性派发命令并取回结果」的通道，而不是一个可以 `cd` 之后继续操作的交互式终端。
