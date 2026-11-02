# 动态目标挂载设计：基于 runtime-server 基座

> 目标：在界面上勾选一个或多个目录，把它们挂载到目标服务器或沙箱上，供 Agent 像本地目录一样读写。
> 基座：`ai-agent-runtime` 的 `runtime-server`（即本仓库 `backend/cmd/runtime-server`）。
> 参考实现：E2B 沙箱内 `/mnt/local/<folder>`（`tabbit-fuse` + 反向 WebSocket），见 `docs/architecture/fuse-local-mount.md`。
> 日期：2026-10-29

---

## 0. 一句话结论

**现有 tabbit 的 FUSE 方案不能直接搬到 runtime-server，因为"守护进程住在沙箱里"这个前提在本基座不成立。**
正确做法是把它拆成**控制面**（放在 runtime-server，新包 `internal/mountctl`）与**数据面**（复用已有的 SFTP / envd 两条通道），
并优先落地**路径级虚拟挂载**（零内核依赖），把真正的 FUSE/WinFsp 内核挂载降级为可选后端。

---

## 1. 现状盘点：基座里已有什么

这一节全部来自实际代码检索，是设计的地基。

| 能力 | 位置 | 现状 |
|---|---|---|
| 远程目标声明 | `internal/remotetargets/registry.go` | `Target` 支持 4 种传输：`ssh-stdio` / `ssh-http` / `http` / `envd` |
| 目标侧狱目录 | `remotetargets.RemoteConfig` | 已有 `Workspace` + `AllowedRoots []string` |
| envd 文件元数据 | `internal/envdclient/filesystem.go` | `Stat` / `ListDir` / `MakeDir` / `Remove` / `Move` / `WatchDir` |
| envd 文件内容 | `internal/envdclient/files.go` | `ReadFile(maxBytes)` / `WriteFile(data)` —— **整文件语义** |
| SFTP 客户端库 | `backend/go.mod` | `github.com/pkg/sftp v1.13.11` 已在依赖中 |
| SSH 客户端 | `internal/sshclient/` | 已存在 |
| 路径作用域与越界校验 | `internal/fsscope/scope.go` | `workspace:<id>` / `session:<id>` / `cwd`；`pathWithin()` 实现根校验；成套错误码 |
| 目录信任门控 | `internal/foldertrust/` | `decide.go` / `feature.go` / `store.go` 已具备判定与落库 |
| 文件读写服务接口 | `internal/filetransport/service.go` | `ReadFile` / `WriteFile` / `AppendFile`（当前仅 `LocalService`） |
| 文件浏览 HTTP 面 | `internal/api/runtimeapi/fs_browser_routes.go` | `/fs/roots` `/fs/list` `/fs/search` `/fs/stat` `/fs/preview` `/fs/download` `/fs/upload/*` |
| 远程工具后端 | `internal/remotetools/envd_backend.go` | 已实现 `BackendEnvD`，并在本地进程内执行 jailed IO |
| 主程序 | `backend/cmd/runtime-server/` | 存在，是本文的挂载宿主 |

**关键发现：基座已经有"目标"（remotetargets）、"路径安全边界"（fsscope + AllowedRoots）、"信任判定"（foldertrust）、"两条传输"（SFTP 库 + envd 客户端）。缺的只有中间那层调度。**

---

## 2. 决定性技术缺口：offset 读写

FUSE 的内核契约要求 `read(offset, size)` / `write(offset, data)`，**必须支持按偏移分块**。基座两条通道的能力对比如下：

| 通道 | offset 读 | offset 写 | 结论 |
|---|---|---|---|
| **SFTP**（`pkg/sftp`，已在 go.mod） | ✅ `File.ReadAt` | ✅ `File.WriteAt` | **可直接支撑 FUSE** |
| **envd `/files`**（`envdclient`） | ⚠️ 服务端支持 `Range`，但客户端只发 `bytes=0-N` 前缀 | ❌ `POST /files` 是 `O_TRUNC` 整文件 | **读需扩接口，写需扩能力** |
| 反向 WebSocket（`tabbit-fuse` 私有 RPC） | ✅ | ✅ | 协议未公开、实现不在本仓库 |

**由此得到本设计的核心取舍：**

1. 走 **SFTP** 的路径，今天就能做出真 FUSE 挂载，无需改任何远端。
2. 走 **envd** 的路径（沙箱场景），必须先给 envd 客户端扩展 offset 能力，否则只能做整文件搬运——那样就不是挂载，而是同步。
3. 不应试图在本仓库复刻 `tabbit-fuse` 的私有协议；沙箱场景应通过"扩展 envd"或"复用平台既有隧道"两种正当路径解决。

> 这是本方案与"直接照搬 tabbit 做法"最本质的差别，也是必须先讲清楚的地方。

---

## 3. 总体架构

### 3.1 三个角色

```
┌─────────────────────────────┐
│ 前端（目录选择器 / 挂载面板） │  选 N 个目录 + 选目标 + 选只读/读写
└──────────────┬──────────────┘
               │ HTTP /api/runtime/mounts
┌──────────────▼──────────────────────────────────────┐
│ runtime-server（本机）                                │
│                                                       │
│  internal/mountctl  ── 控制面 ──                       │
│    · 挂载规格校验（source 信任 / target jail / 冲突）   │
│    · 挂载注册表（mount id ↦ 状态）                     │
│    · 生命周期（建立 / 健康 / 重连 / 卸载 / 回收）        │
│                                                       │
│  transport 适配层  ── 数据面 ──                        │
│    · sftpBackend   → 目标 sshd                         │
│    · envdBackend   → 目标 envd（需 offset 扩展）        │
│    · mountHost     → 本机内核挂载（FUSE / WinFsp）      │
└──────────────┬──────────────────────────────────────┘
               │
     ┌─────────┴──────────┬─────────────────┐
     ▼                    ▼                 ▼
 SFTP → 目标服务器      envd → 沙箱      反向隧道 → 沙箱
（目标需 sshd）        （需扩 RPC）      （复用平台能力）
```

### 3.2 为什么控制面必须在 runtime-server

因为它同时持有三样只有它才有的东西：`remotetargets` 的目标清单、`foldertrust` 的信任判定、`fsscope` 的路径边界。把挂载调度放进沙箱或客户端，都会导致这三样被复制第二份——而这个仓库已经因"同一问题两份答案"栽过跟头（见 `remotetargets` 里 `deprecatedPolicy` 的注释，明确记录了"local per-target grant 与 session permission mode 互相矛盾"的教训）。

---

## 4. 两种挂载方向

用户说的"挂载到目标服务器或沙箱"实际含两个相反方向，必须分开设计：

| 方向 | 语义 | 数据面 | 内核挂载在哪一侧 |
|---|---|---|---|
| **Export（导出）** | 本机目录 → 目标 | 目标侧需要 FS 实现者 | 目标服务器/沙箱内 |
| **Import（导入）** | 目标目录 → 本机 | 本机直接读目标 | 本机（runtime-server 所在机器） |

**Priority 判断：Import 应当先做。** 理由：

1. Import 用 SFTP 即可完成，`pkg/sftp` 已在依赖里，零远端改造。
2. Import 的内核挂载发生在本机，runtime-server 本来就在本机，不需要跨网隧道。
3. Export 需要目标侧有常驻 FS 守护进程，是真正昂贵的那一半，且与平台既有隧道能力重叠。

---

## 5. 挂载后端的三档设计（重要）

不要一上来就写 FUSE。按"内核依赖"分三档，每档都能独立交付价值：

| 档 | 形态 | 内核依赖 | 跨平台 | 工作量 | 建议 |
|---|---|---|---|---|---|
| **T0 路径级虚拟挂载** | 把 `mount id` 变成 `fsscope` 的新 scope 种类，Agent 工具按 `mount:<id>/rel/path` 访问 | **无** | 全平台 | 小 | **首发** |
| **T1 用户态挂载（FUSE/WinFsp）** | 真内核挂载点 | Linux: libfuse；Windows: WinFsp/Dokan（需 CGO） | 分平台 | 大 | 二期 |
| **T2 目标侧导出** | 目标内起 FS 守护进程 + 反向隧道 | 目标侧 | 依目标 | 最大 | 三期 |

### 5.1 为什么 T0 必须先做

| 理由 | 说明 |
|---|---|
| 绕开 Windows FUSE 难题 | 本仓库有 `docs/aicli/windows7.md`、`windows7-build.md`，说明分发目标是**静态单二进制、含 Win7**。WinFsp 需要 CGO 与内核驱动，直接破坏这个分发形态。 |
| 复用既有安全模型 | `fsscope` 已有完整的越界校验与错误码；新增一种 scope 是 additive 改动，不动现有端点 |
| 语义已够用 | Agent 的读写、`/fs/*` 浏览、`remotetools` 的 jailed IO 全都走路径，不依赖内核挂载 |
| 失败面小 | 没有内核对象，卸载是幂等的、不会残留挂载点 |

T0 的代价是"不是真的出现在文件系统里"，`cat`、`gcc` 这类**外部进程**看不到。但 Agent 自己的工具链看得到——而这正是本场景的主要消费者。

> 判断依据：`/fs/*` 与 `remotetools` 已证明"路径级作用域"足以支撑 Agent 的文件操作。内核挂载的增量价值主要是"让任意外部进程透明访问"，那是 T1 的目标，不是 MVP 的目标。

---

## 6. 数据模型

### 6.1 挂载规格（与 `remotetargets` 对齐）

新增配置块 `mounts:`，**通过 `target` 字段引用 `remoteTargets` 里的名字**，而不是重复写一遍 SSH 凭据：

```yaml
mounts:
  - id: mt_proj                     # 稳定标识，前端与 API 主键
    source:
      kind: local                   # local | target
      path: E:\projects\ai\proj     # 本机绝对路径（kind=local）
      readonly: true
    target: sandbox-a               # 引用 remoteTargets 中的 target 名
    mountpoint: /mnt/in/proj        # 目标侧路径，必须落在 AllowedRoots 内
    mode: ro                        # ro | rw
    lifetime: session               # session | persistent
    backend: auto                   # auto | sftp | envd | virtual
```

设计要点：

| 决定 | 理由 |
|---|---|
| 引用 target 名而非内联凭据 | 凭据只存在一处；`remotetargets` 的设计注释已明确"target 声明故意不进 mcp.yaml，因为含 SSH 凭据" |
| `mountpoint` 必须落 `AllowedRoots` | 直接复用已有的 jail 语义，不引入第二套边界 |
| `backend: auto` | 按目标传输能力自动选 sftp/envd/virtual，避免用户理解传输细节 |
| `id` 独立于路径 | 因为 basename 会撞（`D:\a\proj` 与 `E:\b\proj` 同名），tabbit 正是踩了这个坑才需要独立 mount id |

### 6.2 运行时状态

```go
type Status string // pending | mounting | ready | degraded | unmounting | failed

type Mount struct {
    ID         string
    Spec       Spec
    Status     Status
    Backend    string    // 实际选中的数据面
    Mountpoint string    // 生效路径
    Since      time.Time
    LastError  string
    Metrics    Metrics   // 见 §10
}
```

---

## 7. HTTP 接口

挂在 `/api/runtime/mounts` 下，与既有 `/fs/*` 同级（路由注册可参照 `fs_browser_routes.go`）：

| 方法 | 路径 | 用途 |
|---|---|---|
| `POST` | `/mounts` | 建立挂载（body = Spec），返回 id + 初始状态 |
| `GET` | `/mounts` | 列出全部挂载及状态 |
| `GET` | `/mounts/{id}` | 单个挂载详情 |
| `DELETE` | `/mounts/{id}` | 卸载（幂等：404 与已卸载都返回 200） |
| `GET` | `/mounts/{id}/events` | 状态流（SSE），供前端显示进度与错误 |
| `GET` | `/mounts/targets/{name}/probe` | 探测目标能力（是否有 sshd / envd / offset 支持） |

**错误码复用 `fsscope` 的既有风格**（`scope_invalid` / `path_outside_scope` 等），新增：

| 码 | HTTP | 含义 |
|---|---|---|
| `mount_source_untrusted` | 403 | source 目录未经 `foldertrust` 信任 |
| `mount_target_unknown` | 404 | target 名不存在 |
| `mountpoint_outside_jail` | 400 | mountpoint 不在 AllowedRoots |
| `mount_backend_unsupported` | 409 | 目标传输不支持所需能力（如 envd 无 offset） |
| `mount_conflict` | 409 | mountpoint 已被占用 |
| `mount_limit_exceeded` | 429 | 超出并发上限 |

---

## 8. 关键时序

### 8.1 Import（目标 → 本机，T1 SFTP 路径）

```
① POST /mounts {source: target:b/some/dir, mountpoint: 本机路径}
② mountctl 校验：target 存在 → mountpoint 在 AllowedRoots → foldertrust 判定
③ 选后端：probe 目标，ssh 可达 → sftpBackend
④ 打开 SFTP 会话（复用 sshclient + pkg/sftp）
⑤ mountHost 创建挂载点，注册 FUSE/WinFsp 适配器
⑥ 每个 FUSE op → sftp 的 ReadAt/WriteAt/Stat/ReadDir
⑦ 状态推 ready → SSE 通知前端
⑧ 卸载：先 detach 挂载点，再关 SFTP，最后清状态
```

### 8.2 Export（本机 → 目标，T2）

```
① POST /mounts {source: local:E:\proj, target: sandbox-a, mountpoint: /mnt/in/proj}
② 校验 foldertrust（导出本机目录风险最高，必须显式信任）
③ 选后端：目标是否已有 FS 守护进程？
   ├─ 有（平台隧道）→ 反向 WS 注册
   └─ 无（envd 目标）→ 若要求真挂载则返回 mount_backend_unsupported
④ 目标侧守护进程 mountpoint 建立
⑤ 状态推 ready
```

**注意 ③ 的诚实处理**：envd 目标当前**无法**做真挂载（无 offset RPC）。要么扩展 envd，要么明确返回不支持，**不要用"定时同步"伪装成挂载**——那会让用户以为写入是实时的。

---

## 9. 权限与安全

### 9.1 复用而非新造

| 关注点 | 复用件 | 用法 |
|---|---|---|
| source 是否可信 | `foldertrust` | Export 场景**必须**先过信任判定 |
| mountpoint 边界 | `remotetargets.RemoteConfig.AllowedRoots` | 越界一律 400 |
| 路径规范化与越界比较 | `fsscope.pathWithin` / `NormalizeRelPath` | 拒绝 `..`、符号链接逃逸 |
| 错误呈现 | `fsscope.Error` + `CodeOf/StatusOf` | 前端统一渲染 |

### 9.2 必须固化的默认值

| 项 | 默认 | 理由 |
|---|---|---|
| 新挂载 `mode` | **`ro`** | tabbit 的经验教训：`rw` + `allow_other` 让沙箱内任意代码可删用户文件，且无快照兜底 |
| Export 的 source 范围 | 单目录，不含父目录 | 最小授权粒度 |
| 并发上限 | 16（与 tabbit 一致，可配） | 有先例、可预期 |
| 超大文件 | 拒绝（返回明确错误） | 避免误当存储用 |

### 9.3 T1 特有的两个风险（不同于 T0）

| 风险 | 说明 | 处置 |
|---|---|---|
| `allow_other` 等价问题 | Linux FUSE 若开 `allow_other` + root 属主，等于对全机暴露 | 默认**不开** `allow_other`，仅在显式配置时开启 |
| close 阶段 EIO | tabbit 实测存在 `RELEASE`/`FLUSH` 报 EIO 的缺陷，会让 `cat` 退出码非零 | 传输层实现 `Flush` 返回 nil 兜底 + 记录指标，不让收尾错误污染数据通路 |

---

## 10. 可观测性

每个挂载至少 6 个指标，否则线上无法定位：

| 指标 | 用途 |
|---|---|
| `mount_op_total{op,backend}` | 各 FUSE 操作次数 |
| `mount_op_duration{op}` | P50/P99，识别慢操作 |
| `mount_op_error{op,code}` | 按错误码分布，EIO 单独计数 |
| `mount_bytes{dir}` | 上下行流量 |
| `mount_reconnect_total` | 隧道/SFTP 重连次数 |
| `mount_status{id}` | 状态机当前值（gauge） |

外加**本地 JSONL 审计**（与 `RemoteConfig.AuditLog` 同思路，审计文件留在本机）：记录 mount id、source、target、mode、操作类型、结果。这样"谁把公司代码导到哪台机器"可追溯。

---

## 11. 失败与降级

| 场景 | 行为 |
|---|---|
| SFTP 断连 | 状态 `degraded`，指数退避重连；重连前所有 op 返回 `EIO` 但不卸载 |
| 单 op 超时 | 30s（与 tabbit 同值），超时计数告警，不阻塞其他挂载 |
| 目标不支持所需能力 | 建挂载时即拒绝（`409`），不进入半死状态 |
| mountpoint 冲突 | 建挂载时拒绝，附建议替换名 |
| 卸载时 op 进行中 | 先置 `unmounting` 拒绝新 op，等待 in-flight 归零再 detach |
| runtime-server 重启 | `lifetime: persistent` 的挂载按注册表恢复；`session` 的清理掉 |

---

## 12. 代码落位建议

```
backend/internal/mountctl/
    spec.go            // Spec 解析与校验
    registry.go        // mount id ↦ Mount 状态机
    controller.go      // 建立/卸载/健康检查/重连
    backend.go         // Backend 接口（见下）
    backend_sftp.go    // pkg/sftp 实现
    backend_envd.go    // envdclient 实现（受 offset 缺口限制）
    backend_virtual.go // 纯 fsscope 路径映射（T0）
    routes.go          // HTTP 端点

backend/internal/mountctl/mounthost/
    host_linux.go      // build tag linux，libfuse
    host_windows.go    // build tag windows，WinFsp（CGO，默认不启用）
    host_stub.go       // 其他平台：返回 "unsupported"，编译仍通过
```

**后端接口的最小契约（这是全篇最要紧的一段代码）：**

```go
// Backend 是数据面。刻意只暴露 offset 语义，
// 让"不支持 offset"的后端在编译期就无处可藏。
type Backend interface {
    Stat(ctx context.Context, rel string) (Entry, error)
    ListDir(ctx context.Context, rel string) ([]Entry, error)
    OpenRead(ctx context.Context, rel string) (ReaderAt, error)
    OpenWrite(ctx context.Context, rel string) (WriterAt, error)
    Create(ctx context.Context, rel string, mode os.FileMode) error
    Remove(ctx context.Context, rel string) error
    Rename(ctx context.Context, from, to string) error
    Close() error
}

type ReaderAt interface {
    ReadAt(ctx context.Context, p []byte, off int64) (int, error)
}
type WriterAt interface {
    WriteAt(ctx context.Context, p []byte, off int64) (int, error)
    Truncate(ctx context.Context, size int64) error
}
```

`backend_envd.go` 在 `Size > 阈值` 或需要 offset 时返回 `ErrOffsetUnsupported`，由 controller 在**建挂载阶段**就报错，而不是运行期千次失败。

---

## 13. 分期

| 期 | 内容 | 可验证成果 |
|---|---|---|
| **P0** | T0 路径级虚拟挂载：`fsscope` 新增 `mount:<id>` scope + `mountctl` 注册表 + `POST/GET/DELETE /mounts` | 界面上勾目录→挂到目标→Agent 通过 `mount:<id>/a.txt` 读到内容 |
| **P1** | `backend_sftp.go` + `mount_host`（Linux FUSE） | 本机能 `cat` 到远端目录 |
| **P2** | envd offset RPC 扩展（`ReadRange`/`WriteRange`） | envd/沙箱目标支持真挂载 |
| **P3** | Export 方向 + 反向隧道接入 | 本机目录出现在沙箱内 |
| **P4** | Windows WinFsp、多挂载并发、审计与配额 | 全平台一致体验 |

**每期的验收都必须是"可见行为"，不能是"代码已写"。** P0 的验收口径：一次挂载后，`GET /fs/stat?scope=mount:<id>&path=a.txt` 返回正确 size，且 `path=../etc/passwd` 返回 `path_outside_scope`。

---

## 14. 未决问题（需要在动手前定）

| # | 问题 | 为什么卡住 |
|---|---|---|
| 1 | envd 是否可扩展 offset RPC？ | 决定 P2 是否存在；若 envd 属外部镜像不可改，沙箱场景只能走平台既有隧道 |
| 2 | T0 的 `mount:<id>` 是否要暴露给 `/fs/*` 全部端点 | 涉及 `fsscope.ParseScope` 的兼容性，需确认前端已按 `scope` 字符串透传 |
| 3 | Win7 + 静态二进制的分发约束是否仍有效 | 若有效，T1 在 Windows 上永久不可用，只能 T0 |
| 4 | Export 的信任判定交互形态 | `foldertrust` 现有可能面向 workspace，导出到远端是更高风险，需确认交互与文案 |
| 5 | 是否需要只读挂载的强保证 | 若需要，SFTP 侧可用只读账号 + 服务端二次校验，而非仅靠 `mode: ro` 标志 |

---

## 15. 小结

在 runtime-server 基座上做动态目标挂载，**不是把 tabbit 的 FUSE 抄过来**，而是：

1. 认识到基座已有目标声明、路径边界、信任判定、两条传输——缺的是调度层；
2. 认识到 FUSE 的硬门槛是 **offset 读写**，而这恰好是 envd 目前没有、SFTP 天然有的；
3. 因此先做**无内核依赖的路径级虚拟挂载**（复用 `fsscope`），再做 SFTP 内核挂载，最后才碰 envd 扩展与导出方向；
4. 全程复用 `AllowedRoots` 做挂载点狱目录、`foldertrust` 做导出授权，**不引入第二套安全模型**。

这样每一期都有独立可用价值，且不会在 Windows 静态二进制的分发约束上翻车。
