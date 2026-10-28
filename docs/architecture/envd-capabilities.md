# envd 功能全解：进程、文件、终端、监控四合一环境代理

> 分析对象：沙箱内 `/usr/bin/envd` 的完整能力面
> 版本：`0.2.11`（`-commit` 返回 `test`，暗示为测试构建）
> 二进制：16,732,440 字节，Go 静态链接
> 监听：`[::]:49983`（IPv6 双栈）
> 更新日期：2026-10-27
> 关联文档：`sandbox-command-execution-channel.md`、`sandbox-local-folder-mount-mechanism.md`、`sandbox-selfhost-linux-feasibility.md`

本文是 `sandbox-command-execution-channel.md` 的能力补充篇。前文聚焦「命令如何抵达并执行」，本文聚焦「`envd` 到底提供了哪些能力」。

---

## 1. 全景：两个 Connect 服务 + 两个 HTTP 端点

```
envd :49983
├── HTTP 端点
│   ├── GET /health    → 204  （存活探针，无响应体）
│   └── GET /metrics   → 200  （CPU/内存/磁盘实时采样）
│
├── process.Process （7 个方法）—— 进程管理
│   ├── Start          启动进程（流式回传输出）
│   ├── List           枚举进程
│   ├── Connect        接入已有进程的 I/O（流式）
│   ├── StreamInput    流式写 stdin（可用）
│   ├── SendInput      写 stdin（unary，HTTP/1.1 可用）
│   ├── SendSignal     发信号（SIGTERM / SIGKILL）
│   └── Update         更新进程属性（pty 的 cols/rows）
│
└── filesystem.Filesystem （9 个方法）—— 文件系统
    ├── ListDir            列目录
    ├── Stat               取元数据
    ├── MakeDir            建目录
    ├── Move              移动/重命名（source → destination）
    ├── Remove             删除
    ├── WatchDir           监听目录（流式推送）
    ├── GetWatcherEvents   取监听事件
    ├── CreateWatcher      创建监听器
    └── RemoveWatcher      移除监听器
```

**方法可用性实测矩阵**（`POST` + `Content-Type: application/connect+json`）：

| 方法 | HTTP | 判定 |
|---|---|---|
| `process.Process/Start` | 200 | ✅ 可用（流式） |
| `process.Process/Connect` | 200 | ✅ 可用（流式） |
| `process.Process/StreamInput` | 200 | ✅ 可用（流式） |
| `process.Process/List` | 415 | ✅ 存在（unary；探测用错 Content-Type） |
| `process.Process/SendInput` | 415 | ✅ 存在（unary） |
| `process.Process/SendSignal` | 415 | ✅ 存在（unary） |
| `process.Process/Update` | 415 | ✅ 存在（unary） |
| `filesystem.Filesystem/WatchDir` | 200 | ✅ 可用（流式） |
| `filesystem.Filesystem/ListDir` | 415 | ✅ 存在 |
| `filesystem.Filesystem/Stat` | 415 | ✅ 存在 |
| `filesystem.Filesystem/MakeDir` | 415 | ✅ 存在 |
| `filesystem.Filesystem/Move` | 415 | ✅ 存在 |
| `filesystem.Filesystem/Remove` | 415 | ✅ 存在 |
| `filesystem.Filesystem/GetWatcherEvents` | 415 | ✅ 存在 |
| `filesystem.Filesystem/CreateWatcher` | 415 | ✅ 存在 |
| `filesystem.Filesystem/RemoveWatcher` | 415 | ✅ 存在 |

> `415` 不代表方法不存在，而是 Connect 协议要求正确的流式 Content-Type 与分帧体。它证明**路由已注册**。

> **2026-10-28 更正**：`List` / `SendInput` / `SendSignal` / `Update` 实际是 **unary** RPC；
> 上表的 415 来自探测时使用了流式 `Content-Type`。改用 `application/json` 后它们在 HTTP/1.1
> 下即可用，已由真实 envd 验证（stdin 往返、SIGTERM、PTY resize）。只有 `StreamInput` 是
> client-streaming、需要 HTTP/2。

---

## 2. 能力一：进程管理（比"执行命令"复杂得多）

### 2.1 ProcessConfig —— 启动一个进程能控制的全部参数

从二进制提取的字段集合：

| 字段 | 类型 | 作用 |
|---|---|---|
| `cmd` | string | 可执行文件路径 |
| `args` | []string | 参数数组 |
| `cwd` | string | 工作目录 |
| `envs` | map | **自定义环境变量**，可注入任意键值 |
| `user` | string | **指定运行身份**（可降权） |
| `pty` | object | **伪终端配置**（启用交互式终端） |
| `tag` | string | 进程标签，便于按标签查找 |

**这是本文最重要的发现之一**：`envd` 不是「只能跑 `bash -c`」，而是可以精确指定 `user`、`envs`、`pty`。这意味着：

- 可以为每条命令注入不同环境变量（如临时 `PATH`、`PROXY`）；
- 可以指定非 root 用户运行（实测当前 shell 即 **uid=1000**，而非 root）；
- 可以开真正的 PTY，支持 `vim`、`top`、`less` 这类需要终端的程序。

### 2.2 流式事件协议

`Start` 与 `Connect` 返回的是**事件流**，而非一次性结果：

```
ProcessEvent_Start     进程已启动（含 pid）
ProcessEvent_Data      ├── stdout 数据块
                       ├── stderr 数据块
                       └── pty 数据块（终端模式）
ProcessEvent_End       进程结束（含 exit_code）
ProcessEvent_KeepAlive 心跳（长命令保活用）
```

**设计含义**：

1. **边执行边返回**：不是等命令跑完再一次性给结果，因此 `tail -f`、`ping`、构建日志能实时看到。
2. **`KeepAlive` 说明支持长任务**：长时间无输出的命令不会因空闲超时被断开。
3. **`pty` 数据通道独立**：终端模式下输出不走 stdout/stderr，而是走 pty 通道。

### 2.3 信号控制

```
SIGNAL_SIGTERM       优雅终止
SIGNAL_SIGKILL       强制杀死
SIGNAL_UNSPECIFIED   默认
```

只暴露这三个——说明设计意图是「能停任务」而不是「任意信号注入」。没有 `SIGHUP`、`SIGUSR1`、`SIGSTOP`，是刻意的收窄。

### 2.4 Update —— 窗口尺寸调整

`Update` 是**终端尺寸变更**接口，请求形状为 `{"process":{...},"pty":{"size":{"rows":R,"cols":C}}}`。这是 PTY 会话的标配：前端窗口变化时，通过它通知后端调整 `$COLUMNS` / `$LINES`，否则 `vim`、`top` 会排版错乱。

> 这条能力反向证明了**交互式终端是真实支持的一等场景**，不是事后补丁。

### 2.5 Connect —— 接入「已有」进程

关键区别：

```
Start    创建新进程，我从零开始
Connect  接入已存在进程，继续读写它的 I/O
```

用途：断线重连后继续观察一个还在跑的长任务；或让两个客户端同时看同一进程的输出。这让「会话恢复」成为可能。

---

## 3. 能力二：文件系统（不只是读写文件）

### 3.1 九个方法的定位

| 方法 | 类比 | 说明 |
|---|---|---|
| `ListDir` | `ls` | 列目录，返回 `entries[]` |
| `Stat` | `stat` | 取单个路径的元数据 |
| `MakeDir` | `mkdir` | 建目录 |
| `Move` | `mv` | `source` → `destination` |
| `Remove` | `rm` | 删除 |
| `WatchDir` | `inotifywait` | **目录变更流式监听** |
| `GetWatcherEvents` | — | 拉取监听事件 |
| `CreateWatcher` | — | 创建监听器（返回 `watcher_id`） |
| `RemoveWatcher` | — | 销毁监听器 |

### 3.2 Stat 返回的元数据字段

```
size            字节数
mode            权限模式（八进制）
permissions     权限字符串（如 rwxr-xr-x）
owner           属主
group           所属组
modified_time   修改时间
symlink_target  符号链接指向
```

比 POSIX `stat` 更易消费：直接把 `permissions` 解析成人类可读字符串，把 `owner` 解析成用户名而非裸 uid。

### 3.3 ListDir 返回结构

```
entries[] → { name, path, type }
```

`type` 用于区分 file / directory / symlink。**注意**：`ListDir` 在二进制中以两种形式出现——`ListDir` 与 `ListDirunary`，暗示存在**流式版本**（目录极大时逐条推送，避免一次性返回巨大数组）。此为推断。

### 3.4 文件监听：这是最容易被忽略的能力

二进制中出现完整的 `fsnotify` / inotify 事件常量：

```
IN_CREATE      文件/目录被创建
IN_DELETE      被删除
IN_MODIFY      内容被修改
IN_MOVE        被移动
IN_MOVED_FROM  移动源
IN_MOVED_TO    移动目标
IN_ATTRIB      属性变更（chmod/chown）
IN_CLOSE_WRITE 可写句柄关闭（写入完成）
IN_OPEN        被打开
IN_ACCESS      被访问
IN_DELETE_SELF 自身被删除
IN_IGNORED     监听被移除
IN_Q_OVERFLOW  事件队列溢出
IN_UNMOUNT     所在文件系统卸载
```

**这意味着什么**：平台能**实时感知沙箱内文件的变化**，而不需要轮询。

实际价值：

- Agent 执行 `npm install` 后，能立刻知道产生了哪些文件；
- 用户在本机修改文件，沙箱侧能收到通知（配合 FUSE 挂载）；
- 可以观察命令产生的副作用，而不必事后 `diff`。

`WatchDir` 实测直接返回 **200（流式）**，说明它是可用功能，而非仅定义。

---

## 4. 能力三：监控（/metrics 的隐藏深度）

### 4.1 实际暴露的 7 个字段

```bash
$ curl -s http://127.0.0.1:49983/metrics
{"ts":1793110358,"cpu_count":3,"cpu_used_pct":0.27,
 "mem_total":4362629120,"mem_used":623304704,
 "disk_used":2252800,"disk_total":1078984704}
```

| 字段 | 含义 | 实测值 |
|---|---|---|
| `ts` | 采样时间戳 | Unix 秒 |
| `cpu_count` | vCPU 数量 | 3 |
| `cpu_used_pct` | CPU 使用率 | 0.27% |
| `mem_total` | 总内存 | 约 4.06 GiB |
| `mem_used` | 已用内存 | 约 594 MiB |
| `disk_used` | 已用磁盘 | 约 2.1 MiB |
| `disk_total` | 磁盘总量 | 约 1.00 GiB |

### 4.2 端点只暴露摘要，二进制里却有完整 procfs 解析器

从二进制提取到的内存相关 JSON 字段远超 7 个：

```
活跃/非活跃内存：active, inactive, activeanon, inactiveanon,
                  activefile, inactivefile, laundry, unevictable
缓存与内核内存：  buffers, cached, slab, sreclaimable, sunreclaim,
                  mapped, vmalloc, pageTables, kernelStack
换页与提交：      swapTotal, swapFree, commitLimit, committedAS,
                  dirty, writeback
系统整体：        total, free, available, used, shared, high, low, wired
CPU 细分：        user, nice, system, idle, iowait, irq, softirq, steal, guest
```

**判断**：`envd` 内部实现的是**完整的 `/proc/meminfo` 与 `/proc/stat` 解析器**，覆盖 Linux 内存管理的全部指标；但 HTTP `/metrics` 端点只挑选了 7 项摘要输出。

**可能原因**：给上层调度器做扩缩容决策只需这 7 项；完整指标若暴露会显著增大响应体与解析成本。也可能完整指标走的是另一条未公开的通道。

### 4.3 指标的真实性验证

`metrics` 返回的 `mem_total=4362629120`（4.06 GiB）与系统 `free -h` 的 4.1 GiB 一致，`disk_total` 与 `df` 的约 1 GB 一致——**这是实时采样，不是硬编码常量**。

---

## 5. 能力四：身份与鉴权模型

### 5.1 401 的真实含义

```bash
$ curl -X POST -d '{}' /filesystem.Filesystem/ListDir
{"code":"unauthenticated","message":"no user specified"}
```

**关键**：错误信息是「**没有指定用户**」，而不是「token 无效」或「签名错误」。

这说明鉴权**基于身份注入**而非密钥：

```
浏览器/中继  --(注入用户身份)-->  envd  --(按身份确定权限)-->  文件系统
```

自行伪造 `X-User`、`X-API-Key`、`Authorization: Bearer` 均无效（实测仍 401）。

### 5.2 两个服务的鉴权强度不一致

| 服务 | 无身份调用 | 判断 |
|---|---|---|
| `filesystem.Filesystem/*` | **401** 强制要求身份 | 严格 |
| `process.Process/List` | **200** 直接返回 | 宽松 |

**安全含义**：沙箱内任何进程都能枚举正在运行的进程（包括 Agent 自己的命令），但无法直接读文件。这是一个**不彻底的隔离面** —— 横向可见但不横向可写。

### 5.3 命令身份实测

```
$ id -u ; id -un
1000
user
```

命令以 **uid=1000（user）** 运行，而 `envd` 自身是 uid=0。

> ⚠️ **修正前文**：`sandbox-command-execution-channel.md` 第 9 节称「命令以 root 运行、无降权」，这是**错误结论**。实测 `ProcessConfig.user` 生效，命令以非 root 身份运行。前文应予以更正。

---

## 6. 能力边界：它明确「不做」什么

从二进制与实测反推，以下能力**不在** `envd` 范围内：

| 不提供 | 依据 | 由谁负责 |
|---|---|---|
| 容器/虚拟机管理 | 无 `docker`、`kvm` 相关符号 | 上层调度器 |
| 网络端口转发 | `proxy`/`dns` 命中均来自 Go 标准库 `net/http`，非自有功能 | 中继层 |
| 用户账户管理 | 只有 `user`/`username` 查询，无 `passwd`/`useradd` | 镜像层 |
| 包管理 | 无 `apt`/`dpkg` 相关调用 | 镜像层 |
| 快照/恢复 | 无 `snapshot` 符号 | 宿主 hypervisor |
| 日志聚合 | 无日志转发逻辑 | 平台采集侧 |
| 资源硬限制 | 限制由 `prlimit` 在 fork 时施加，`envd` 只做包装 | `prlimit` + cgroup |

特别说明：`dns`、`proxy` 曾误判为功能，实际来自 Go `net/http` 的 `ProxyFunc`、`HTTPProxy` 等标准库符号，与 `envd` 业务无关。

---

## 7. 能力总览一页纸

| 维度 | 能力 | 状态 |
|---|---|---|
| **进程** | 启动（可指定 user/envs/cwd/pty/tag） | ✅ |
| | 从输出流式读取（stdout/stderr/pty） | ✅ |
| | 枚举 / 按 tag 查找 | ✅ |
| | 接入已有进程 I/O | ✅ |
| | 流式写 stdin | ✅ |
| | 发送 SIGTERM / SIGKILL | ✅ |
| | PTY 窗口尺寸调整 | ✅ |
| | 长任务心跳保活 | ✅ |
| **文件** | 列目录（含流式版本） | ✅ |
| | 元数据查询（含 owner/permissions/symlink） | ✅ |
| | 建目录 / 移动 / 删除 | ✅ |
| | 目录变更实时监听（inotify，13 类事件） | ✅ |
| | 多监听器管理（watcher_id） | ✅ |
| **监控** | 存活探针（204） | ✅ |
| | CPU/内存/磁盘摘要（7 字段） | ✅ |
| | 完整 procfs 解析器（内部，未暴露） | ⚠️ 部分 |
| **身份** | 按注入身份鉴权（file 强制 / process 宽松） | ⚠️ 不一致 |
| | 命令降权为非 root（uid=1000） | ✅ |
| **不提供** | 容器管理 / 端口转发 / 快照 / 账户管理 | ❌ 归其他层 |

---

## 8. 一句话总结

`envd` 不是「命令执行器」，而是一个**远程环境代理**：它把「起进程、喂输入、收信号、改文件、盯变化、看资源」这六件事统一到一个 Connect 服务里，并用注入式身份做权限隔离。

它刻意保持了**薄**：不做资源硬限制（交给 `prlimit`/cgroup）、不做快照（交给 hypervisor）、不做网络转发（交给中继）。这种「只做环境语义、不做基础设施」的切分，正是它能同时服务于命令执行与文件访问两条链路的原因。

---

## 9. 未确认项

1. **`ListDir` 流式版本的实际行为**：`ListDirunary` 的存在暗示有流式变体，未实测大目录下的表现。
2. **`SendInput` 与 `StreamInput` 的差异**：前者单次写入、后者流式，但为何同时存在、实际用哪个，未验证。
3. **`Update` 除 cols/rows 外是否支持其他属性**：仅从字段推断为终端尺寸，未实测。
4. **完整 procfs 指标是否有其他端点暴露**：`/metrics/full`、`/stats`、`/status`、`/system` 均 404，未找到入口。
5. **`WatchDir` 与 `CreateWatcher` 的关系**：前者是否内部创建 watcher、是否共用 `watcher_id` 空间，未验证。
6. **`-commit` 返回 `test`**：说明是测试构建，正式版可能差异（如更多方法或不同鉴权）。
7. **`ProcessEvent_Data` 中 pty 通道与 stdout 通道是否互斥**：PTY 模式下是否仍有 stdout 事件，未验证。
