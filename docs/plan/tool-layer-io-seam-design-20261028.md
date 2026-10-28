# 工具层 I/O seam 与 envd 增量能力方案

- 文档编号：`tool-layer-io-seam-design-20261028`
- 状态：**待评审（v1 草案）**
- 日期：2026-10-28
- 作者：Qoder（基于 2026-10-28 envd 收编完成后的实测事实撰写）
- 前置文档：
  - `tool-layer-abstraction-design-20261022.md`（Phase 0–6 工具层抽象，行 1542–1574 为 Phase 2/3 定义）
  - `aicli-envd-bridge-merge-plan-20261028.md`（envd 收编方案，§8.1–§8.9 为进度与偏差记录）
  - `docs/architecture/envd-capabilities.md`、`envd-implementation-report.md`（envd RPC 实测矩阵）
- 本文性质：**新增一层抽象的设计方案**，不是对既有设计的修补（§2.2 给出证明），也不是 Phase 0–6 的替代。

---

## 0. TL;DR

envd 收编已经完成（M0–M4，M5 shim 取消），三档工具面在真机上达到 **readonly 6 / standard 13 / ops 19**，`pty` 交互面 6/6 通过。但"一套外壳"只做到了**策略、档位、schema、输出折叠**四层；**文件 I/O 这一层仍然是两套实现**：

- 共享核（`viewcore`/`lscore`/`searchcore`/`mutatecore`，合计 **13,486 行**）直接调 `os.*` 读本机文件，实测 **60 处 `os.*` 调用分布在 19 个文件**；
- envd 后端因此另写了 **1,824 行**文件面（`envd_backend.go` 736 + `envd_tools.go` 396 + `envd_edit.go` 338 + `envd_patch.go` 354），走 `cat`/`printf`/`find`/远端 `rg|grep`。

这两套实现今天**结果一致是靠人对齐出来的，不是靠结构保证的**。§8.9 记录的两个残留缺口（`view` 远端窗口、`grep` 参数面）以及活测发现并修复的两处真实漂移（父目录语义 `36caf76f`、结果文案 `b65918eb`）都证明：只要 I/O 有两个实现体，漂移就是**稳态**而不是意外。

本方案提出 `Access` 接口（工具层唯一的文件 I/O seam）+ 能力位降级模型，把"核读本机文件"这个隐含假设显式化，让 local / ssh / http / envd 四个传输形态共用**一个核**、最多 **2 个 I/O 实现**（`localAccess`、`envdAccess`）。

同时回答本次触发问题——**envd 能否支持增量处理（grep / view）**，结论分层：

| 层 | envd 实测能力 | 结论 |
|---|---|---|
| 传输层 | `process.Process/Start` 是 server-streaming（`Start/Data/End/KeepAlive`），`StreamReader.Recv` 已在 `pty`/`watch_dir` 使用 | **已具备**，只是 `client.Run()` 把流攒完（且 `DefaultMaxBufferedOutput = 8 MiB` 以上丢弃） |
| 计算层 | `filesystem.*` 无 `ReadFile/WriteFile`、无字节区间；但可在目标机跑 `sed -n 'A,Bp'` / `tail -c +N` / `dd` | **grep 可早停、view 可让目标机算窗口**；envd 本身不提供随机读 |
| 呈现层 | MCP 一次调用一个结果 | 真"边跑边显示"需 progress notification 贯通，**是另一条工程线**，与 envd 无关 |
| 新发现 | `GET /files` 路由存在，实测 `400 username required` → 带 `username` 后 **`401 missing signature query parameter`** | envd 唯一的原生文件传输端点，**签名可用则解锁真流式读 + 破 1 MiB argv 写上限**；算法未验证，列为调研项 |

推荐顺序：**先做 §10 三个近项（不需等 seam）→ 并行调研 `/files` 签名 → 再按 §6 分五段落 seam**，每段独立可回滚。

---

## 1. 背景：收编完成后的真实形状

### 1.1 已经统一的部分（不再重复投入）

| 统一层 | 落点 | 实测证据 |
|---|---|---|
| 档位/策略 | 单一 `envdExtensionTiers`（`watch_dir`→readonly 扩展、`pty_*`→ops 扩展、`shell`→共享 ops 阶梯） | 工具面 6/13/19 |
| schema | 复用 `mutatecore.{Write,Append,Edit,ApplyPatch}Parameters()` | 同名校验键一致 |
| 输出折叠 | `toolcoreoutput.ShapeText` + `ModelVisibleCap`（`envd_tools.go:12-13` 明确"折叠不在此处做"） | 一份预算规则 |
| 结构化错误 | `envdFailure/envdFailureMeta/envdToolError` 发布 `error_code` + `failure_class`（`8ec067cb`） | `ambiguous_edit`/`stale_context`/`missing_file`/`binary_target`/`file_too_large`/`payload_too_large`/`binary_payload`/`missing_parent` + jail `PATH_DENIED` |
| 启动形态 | 展开默认 `aicli-remote-tools serve --stdio --backend envd`，独立 bridge 删除（`60ba62be`，净 −3,175 行） | 同二进制 local 后端 ssh-stdio 15/15 |

### 1.2 仍然分裂的部分（本方案要解决的唯一问题）

```
                 ┌─────────────────────────────────┐
   工具调用 ───▶ │  共享核（13,486 行，os.* 直读）   │  ← local / ssh / http 用这条
                 └─────────────────────────────────┘
                 ┌─────────────────────────────────┐
   工具调用 ───▶ │  envd 文件面（1,824 行，cat/printf）│ ← envd 用这条（策略统一，实现独立）
                 └─────────────────────────────────┘
```

关键事实（实测 `internal/remotetools/server.go:66-90`、`registry.go:30/42/91`）：**现存代码里只有一个后端分支**——`switch cfg.Backend { case BackendEnvD: … default: … }`，注释写明二者"differ only in where files and processes actually live"。语义是：

- **ssh-stdio / ssh-http / http**：server 进程**跑在目标机上**（`registry.go:91`：ssh-stdio 的配置块转发给 target-side server），所以核读的"本机文件系统"**就是目标机**——这条腿不需要新实现；
- **envd**：server 与目标分离，I/O 必须走 Connect-RPC（`registry.go:42`）——这是唯一需要替代实现的地方。

因此引入 `Access` 之后，**实现体只有两个**：`localAccess`（把核内 60 处 `os.*` 搬进去，同时服务 ssh/http 形态）和 `envdAccess`（把 1,824 行塌缩成约 300 行原语实现）。这不是"再写一套工具代码"，是**减少一套**，并把 `server.go:73` 那个"选两套注册表"的分支降为"选一个 `Access` 实现"。

### 1.3 为什么现在必须决定

§8.9 的结论"差异要写明不掩盖"是文档纪律，但每写一条"envd 与 local 在此处不同"，都是在给未来的模型行为增加分支。**只有结构能终止这种累积**。活测已经两次证明：同一份工具语义在两个实现体上会走偏（父目录、文案），且都是靠人肉发现。

---

## 2. 问题定义与既有设计的关系

### 2.1 一句话问题

共享核里"读文件"这个动作**没有接口**，它是直接的系统调用。因此在非本机文件系统上运行核，唯一办法是**另写一套核**。envd 就是这种情况。

### 2.2 这不是既有设计的遗漏，而是新增一层（重要）

我在写本方案前先验证过 `tool-layer-abstraction-design-20261022.md` 是否已经预留了这个缝，结论是**没有预留**：

1. **零命中检索**：在该文档中搜 `RemoteFS|FileSystem|IO seam|可插 I/O|exec 抽象` → **0 命中**。
2. **Phase 2/3 的注入面不含 I/O**（原文行 1549、1563）：
   - Phase 2 交付："`toolcore/viewcore`：行窗口、编码、二进制/图片、长行 clamp、batch"，adapter "只注入 `PathResolver`/`OutputShaper`"；
   - Phase 3 交付："本地/远程 adapter 注入 `PathResolver` + `PermissionChecker`"。
   即：**它假设核读的就是本机文件**，远程只做路径/权限/输出的适配。
3. **风险节只写了一句占位**：§16.1 风险 3 "远程后续通过接口适配"——没有接口定义、没有落点、没有验收。
4. **成本表**（行 2275–2276）Phase 2 与 Phase 3 各 3–5 天，均已完成；本方案的 seam 工作**不在这张表里**。

`toolcore` 现有接口清单（实测 `rg '^type [A-Z].* interface'`）：`toolcore` 包 **22 个**（`SymbolResolver`/`ContentRenderer`/`Handler`/`Runtime`/`PathResolver`/`PermissionChecker`/`CommandPolicy`/`OutputShaper`/`CaptureBudgetProvider`/`MutationObserver`/`MutationGuard`/`ReadObserver`/`PathHinter`/`KindHinter`/`BackupPolicy`/`ArtifactSink`/`SessionState`/`ExtensionStore`/`EnvProvider`/`Clock`/`IDGenerator` + `ArtifactOriginProvider`），`searchcore` **2 个**（`Searcher`/`BackendReporter`）。

**其中没有任何一个定义"读出一个文件的字节"。** 连 `PathResolver.Resolve` 也是在本机做 `filepath`/symlink 判定——它本身就是 local-biased 的。

### 2.3 `Searcher` 是本项目已验证过的正确形状

`searchcore/handler.go:65-79` 的 `Searcher`（`Glob`/`Grep`，返回 `(result, handled bool, err)`，核在 `handled=false` 时回落到纯 Go walker）与 `BackendReporter`（公布实际用的引擎/命令）是**同一家族的成功先例**：

- 能力以**可选接口**表达，不实现就有明确降级；
- 降级判断**写在核里一次**；
- 实际用了哪个引擎**对外可见**（这正是 envd grep "报告哪个引擎跑了，因为两种方言不同"的动机，`envd_tools.go:227-229`）。

本方案的 `Access` 沿用这三条，只是把缝从"搜索引擎"下移到"文件系统"。

### 2.4 与收编方案（`aicli-envd-bridge-merge-plan-20261028.md`）的边界

| 主题 | 归属 |
|---|---|
| 档位表、工具面、schema、启动形态、bridge 删除 | 收编方案（已完成 M0–M4） |
| 结构化错误 metadata、文案对齐、父目录语义 | 收编方案 §8.8/§8.9（已完成） |
| **文件 I/O 接口化、能力位降级、两实现体收敛** | **本方案** |
| `/files` 签名、远端窗口、流式消费 | 本方案 §5、§10 |

收编方案里"envd 文件面独立实现"是被**明确接受的过渡态**（`envd_tools.go:5-10` 的注释就是这个决定的书面记录）。本方案是那笔过渡债的**偿还计划**，不是对收编的否定。

---

## 3. 实测证据（本文所有数字的来源）

> 复现命令见附录 A。以下每一项都在 2026-10-28 于本机工作区与真机 `172.18.3.238:49983` 上验证过。

### 3.1 搬家清单：核内的 `os.*` 直接调用

`rg '\bos\.(ReadFile|WriteFile|Stat|Open|OpenFile|Create|Remove|RemoveAll|MkdirAll|Rename|ReadDir|Glob|WalkDir|Getwd|EvalSymlinks|Lstat|Chmod|Truncate)\(' backend/internal/toolcore backend/internal/tools -g '!*_test.go'` → **60 处，分布在 19 个文件**：

| 文件 | 处数 | 归属核 |
|---|---|---|
| `mutatecore/atomic.go` | 8 | 写（原子替换） |
| `mutatecore/handler.go` | 8 | 写/编辑/patch handler |
| `searchcore/grep.go` | 8 | 搜索（纯 Go 回落路径） |
| `searchcore/grep_ignore.go` | 5 | ignore 文件读取 |
| `mutatecore/mutate.go` | 4 | write/append/edit 落地 |
| `mutatecore/patch.go` | 4 | apply_patch 落地 |
| `tools/code_index_resolver.go` | 3 | 索引（**不在本方案范围**，见 §4.7） |
| `mutatecore/backup.go` | 3 | 备份 |
| `searchcore/handler.go` | 3 | 搜索 handler |
| `viewcore/read.go` / `viewcore/handler.go` / `viewcore/refusals.go` | 2 / 2 / 1 | 读取与拒绝判定 |
| `searchcore/{glob,ripgrep_engine}.go` | 2 / 2 | glob 与 rg runner |
| `searchcore/{grep_context,grep_binary,grep_sort,filesystem_unix}.go` | 各 1 | 上下文/二进制/排序/系统判定 |
| `lscore/handler.go` | 1 | 目录列表 |

按核聚合（非测试代码行数，实测）：`viewcore` 1,924 / `searchcore` 6,382 / `lscore` 356 / `mutatecore` 4,824 = **13,486 行**。

对应 envd 侧文件面（实测）：`envd_backend.go` 736 / `envd_tools.go` 396 / `envd_edit.go` 338 / `envd_patch.go` 354 = **1,824 行**。另有 `envd_process.go` 221 / `envd_pty.go` 447 = **668 行**属进程/PTY 面，**本就是 envd 专属实现，保留不动**。

### 3.2 envd 原语能力矩阵（真机 + 源码双向核对）

| 能力 | envd 端点 / 客户端方法 | 实测状态 | 对 seam 的含义 |
|---|---|---|---|
| 元数据 | `filesystem.Filesystem/Stat`（`envdclient/filesystem.go:109`） | ✅ | `Access.Stat` 有原生实现 |
| 目录 | `ListDir(path, depth)`（:142） | ✅ | `Access.ReadDir` 原生 |
| 建目录 | `MakeDir`（:158） | ✅ | `Access.MkdirAll` 原生 |
| 删除 | `Remove`（:170） | ✅ | `Access.Remove` 原生 |
| 移动 | `Move`（:180） | ✅ | `Access.Rename` 原生 |
| 事件流 | `WatchDir`（:248，返回 `*StreamReader`） | ✅ 已用于 `watch_dir` | 流式原语可用 |
| watcher 管理 | `CreateWatcher`/`GetWatcherEvents`/`RemoveWatcher` | ✅ 路由存在 | — |
| **读文件字节** | 无 | ❌ **不存在**（实现报告 §2.4 探测 404；`envd_tools.go:8-9` 记录"reads run `cat`"） | `Access.Read` 必须靠 spawn 进程 |
| **写文件字节** | 无 | ❌ **不存在**（"writes run `printf` 内容作位置参数"，因此受 argv 上限 `envdMaxWriteBytes = 1<<20`） | `Access.Write` 有 **1 MiB 硬上限** |
| **字节区间读** | 无 | ❌ | `RandomRead` 能力位为 false（原生）；可用远端 `sed`/`tail` 模拟 |
| 进程执行 | `process.Process/Start`（`process.go:308` 返回 `*StreamReader`） | ✅ server-streaming | `Access.Stream` 原生可用 |
| 缓冲执行 | `Client.Run`（:326） | ✅ 但 `DefaultMaxBufferedOutput = 8<<20`，超出**丢弃且继续消费**（:324-326,345-346,353-358） | 今天的 `grep`/`view` 走这条 → **不增量** |
| 提前终止 | `SendSignal`（:417-420）、`Update`、`Connect` | ✅ unary | 早停能力齐备 |
| 交互式输入 | `StreamInput`（client-streaming，需 HTTP/2） | ⚠️ 客户端未支持 | 不影响本方案 |
| **`GET /files`** | 未在客户端封装 | ⚠️ **实测存在**（见 §3.3） | 潜在真流式读/大文件写路径 |

> 修正记录：`CreateSymlink`/`GetFilesHash` 在 `envd-capabilities.md` 的实测表中**没有**出现，因此**不得作为本方案任何设计前提**。若后续需要 symlink 能力，先补真机探测再落文档。

### 3.3 `GET /files` 探测原始记录（2026-10-28）

```
https://172.18.3.238:49983/health                     -> 204
https://172.18.3.238:49983/metrics                   -> 401
https://172.18.3.238:49983/files                      -> 400  "Query argument username is required, but not found"
https://172.18.3.238:49983/files?path=/etc/hostname    -> 400  （同上）
https://172.18.3.238:49983/files?path=...&username=n9e -> 401  {"code":401,"message":"missing signature query parameter"}
https://172.18.3.238:49983/content                     -> 401
https://172.18.3.238:49983/download                    -> 401
```

三点结论：

1. `/files` **路由已注册**（400 参数校验先于鉴权），且**不吃 `X-Access-Token`**，吃的是**签名查询参数**——与 `envdclient/client.go:68-75` 注释完全吻合（"every path except `GET /health` and the `/files` endpoints answers 401 without it"）。
2. **不是安全洞**：未签名一律 401，我未能（也不会尝试）绕过签名读取文件。
3. **是能力钥匙**：envd 唯一的原生文件传输通道。若签名可本地计算，则 `view` 获得真流式/区间读、`write` 破 1 MiB argv 上限；若不可计算，本方案其余部分照常推进（列为 §11 未决问题 U1）。

### 3.4 档位与工具面一致性（收编成果，作为 seam 的约束保留）

| 后端/形态 | readonly | standard | ops |
|---|---|---|---|
| envd（真机 19/19 + pty 6/6） | 6 | 13 | **19** |
| ssh / local（同一二进制，实测 15/15） | — | 8（`view ls glob grep write edit append_write apply_patch`） | 9（+`shell`） |

`remote_x__{view,ls,glob,grep,host_metrics,watch_dir}` / `{write,edit,append_write,apply_patch,mkdir,remove,move}` / `{shell,pty_start,pty_write,pty_read,pty_resize,pty_close}`。**seam 改造不得改变这张表**——工具名、档位归属、schema 是对外契约，`Access` 只是内部落点。

---

## 4. 设计：`Access`（工具层唯一的文件 I/O seam）

### 4.1 三条不可退让的纪律

这三条是"会不会又长出两套工具代码"的唯一防线，写进验收门：

1. **`Access` 只表达**能力，不表达**策略。** 接口里没有 tier、没有 permission、没有 tool name。策略（档位、jail 判定、权限、预算、折叠）全部留在核与既有 adapter。违反这条 = 第二套策略，直接否决。
2. **核内禁止 `if backend == "envd"`。** 唯一允许的分支是**能力位判断**，且同一判断在核里**只允许出现一次**（收进 `resolveCapability()` 一类函数，其余调用点读结论）。用 lint/评审清单把这条变成可检查项（§7.4）。
3. **共享 conformance 套件跑两个实现。** 同一张用例表，`localAccess` 与 `envdAccess` 都必须全绿；不一致要么改实现，要么显式登记为差异并写进文档（沿用 §8.9 纪律，但登记项应随 seam 推进**单调减少**）。

### 4.2 接口定义

```go
// package toolcore（新文件 access.go）
//
// Access 是工具层唯一的文件 I/O seam。它描述“这个后端能做到什么”，
// 不描述“这个工具该不该做”。ctx 一律贯穿，为后续流式与超时留缝。
type Access interface {
    // —— 路径身份（今天 local 用 filepath+EvalSymlinks，envd 用 resolve RPC）——
    Resolve(ctx context.Context, raw string) (Target, error)
    Relative(ctx context.Context, t Target) string

    // —— 元数据 / 目录 ——
    Stat(ctx context.Context, t Target) (Entry, error)
    ReadDir(ctx context.Context, t Target, depth int) ([]DirEntry, error)

    // —— 整文件读：唯一在所有后端都必须存在的数据能力 ——
    // maxBytes 由核给（尺寸门 + 预算），实现必须在超限时明确区分
    // “文件更大”与“读失败”，供核发布 file_too_large。
    Read(ctx context.Context, t Target, maxBytes int64) ([]byte, error)

    // —— 写：策略位（createParents / append / expectedRevision）由核传入，
    // 但“是否支持原子替换”是能力，由实现声明。
    Write(ctx context.Context, t Target, data []byte, opts WriteOpts) (WriteResult, error)

    // —— 结构操作 ——
    MkdirAll(ctx context.Context, t Target) error
    Remove(ctx context.Context, t Target, opts RemoveOpts) error
    Rename(ctx context.Context, from, to Target) error

    // —— 能力声明（核据此降级；降级逻辑只写在核里一次）——
    Capabilities() Capability
}

type Capability struct {
    RandomRead      bool  // 能否只取 [off, off+n)（local: pread；envd 原生: 否）
    AtomicReplace   bool  // 能否同目录 rename 替换（local: 是；envd printf 重定向: 否）
    NativeAppend    bool  // 能否不读回就追加（envd: printf >> 是）
    StreamingExec   bool  // 能否 spawn 进程并流式取字节（envd: 是，pty/shell 已在用）
    Watch           bool  // 事件流（envd: 是）
    MaxWriteBytes   int64 // 0 = 无上限；envd 当前为 1<<20（argv 限制，非策略）
    MaxReadBytes    int64 // 核侧尺寸门，实现可更低
}

// —— 可选能力：沿用 Searcher 的“不实现即降级”模式，不塞进主接口 ——
type RandomAccessor interface {
    RandomRead(ctx context.Context, t Target, off, n int64) ([]byte, error)
}
type ExecStreamer interface {
    // 供 view 远端窗口 / grep 流式与早停使用；返回逐块流 + 取消/信号句柄。
    Stream(ctx context.Context, spec ExecSpec) (ByteStream, error)
}
type Reviser interface {
    // 并发前置条件：local 用 mtime+size 或内容哈希，envd 用内容哈希。
    Revision(ctx context.Context, t Target) (string, error)
}
type Watcher interface {
    Watch(ctx context.Context, t Target) (EventStream, error)
}
```

**刻意不放进接口的东西**：`Glob`/`Grep`（那是 `searchcore.Searcher` 的职责，属"引擎"层，不是"文件系统"层）、`Diff`/`Receipt`/文案（核的职责）、jail 与 tier（adapter 的职责）。

### 4.3 能力位 → 核内降级矩阵（这张表是"只写一次"的全部内容）

| 核的需求 | `RandomRead=true` | `RandomRead=false, StreamingExec=true` | 两者都无 |
|---|---|---|---|
| `view` 带 offset/limit | `pread` 直接取窗口 | 远端 `sed -n 'A,Bp'` / `tail -c +N` 取窗口（§5.2） | 整读 + 核侧 clamp（**今天的行为**） |
| `view` 大文件（超尺寸门） | 只读头 + 采样判二进制 | 远端读头（`head -c`）+ 采样 | 整读后判定，或按今天直接拒 |
| `grep` 命中上限 | 本地 walker 自限（已有） | 远端 `rg -m` / 达到上限后 `SendSignal`（§5.3） | 缓冲全量（**今天的行为**） |
| `write` > `MaxWriteBytes` | 不适用 | 明确拒 `payload_too_large` + `max_file_bytes`（**已实现**，`8ec067cb`） | — |
| `edit`/`apply_patch` 需要原子性 | `AtomicReplace=true` 走 rename | 降级为"读-改-写 + 期望修订校验"，并在结果 metadata 标注 | 拒 |

要点：**降级不是"envd 的行为"，是"无此能力时核的统一行为"**。今天在 `envd_tools.go` 里写死的"读整文件再算窗口"，本质是这条矩阵第三列，但它被写在了实现体里，于是 local 无法复用、envd 无法升级——这就是漂移的来源。

### 4.4 落点

```
backend/internal/toolcore/access.go              # 接口 + Capability + resolveCapability()（新）
backend/internal/toolcore/localio/access.go      # localAccess：承接核内 60 处 os.*（搬家）
backend/internal/remotetools/envd_access.go      # envdAccess：承接 1,824 行文件面的 I/O 部分（塌缩目标 ~300 行）
backend/internal/toolcore/{viewcore,lscore,searchcore,mutatecore}/*.go
                                                 # os.* 调用点改为 access.X；纯逻辑一律不动
```

- **注入方式**：给 `Runtime` 增加可选扩展 `AccessProvider interface { FileAccess() Access }`。**未实现即取 `localAccess`**——这意味着迁移可以逐核推进，未改造的核行为字节级不变，回滚只需删掉一行注入（§6 每段的回滚点都建立在此）。
- **`localAccess` 不是新写的抽象层，是搬家**：把 `os.ReadFile` → `localAccess.Read`，逐点替换，不改语义。搬家后核内应当 `rg '\bos\.(ReadFile|WriteFile|Stat|MkdirAll|Rename|Remove|ReadDir)'` 在四个核目录下**只剩 0 处**（这条可以直接作为验收门）。
- **`envdAccess` 的组成**：`Stat/ListDir/MakeDir/Remove/Move/WatchDir` → 原生 RPC；`Read/Write` → `process.Start` + `cat`/`printf`（保持"内容作位置参数、绝不拼接"的现有纪律，`envd_backend.go:41-45`）；`RandomRead` 若 §5.2 验证通过则实现为远端窗口，否则能力位置 false。
- **不动的部分**：`envd_process.go`、`envd_pty.go`（进程面本就是 `ExecStreamer` 的实现来源）、`toolcoreoutput` 折叠、`envdExtensionTiers`、所有 schema 与文案。

### 4.5 收益核算（按行数）

| 项 | 现状 | 目标 |
|---|---|---|
| 文件面 I/O 实现体 | 2 套（核 13,486 行内含 60 处 os.*；envd 1,824 行） | 1 套核 + 2 个 `Access` 实现 |
| envd 文件面 | 1,824 行 | `envd_access.go` ≈ 300 行 + 编辑/patch 复用 `mutatecore` 纯函数 |
| `mutatecore` 可直接复用的纯函数（实测导出面，均与 I/O 无关） | `MatchEditStrings`/`MatchEditStringsWhitespaceTolerant`/`RebuildEditReplacement`/`ApplyHunks`/`UnifiedDiff`/`PatchSummaryMessage`/`FormatEditSuccessContent`/`StaleEditContextNextAction`/`EditOccurrenceLineNumbers`/`FormatEditOccurrenceLines`/`FindClosestEditSnippet*`/`PatchHunkDiagnostic`/`DetectEncoding`/`Encode`/`LooksBinary`/`TruncateDiagnosticText`/`*Parameters()` | 同一批函数，成为核与 envd 唯一的语义来源 |

`edit`/`apply_patch` 的语义（歧义匹配、stale context、closest snippet、CRLF 容错）今天**已经在 envd 侧靠调用这些纯函数实现**（这正是 `8ec067cb`/`b65918eb` 能对齐全集的原因）。seam 只是把"取字节"和"落字节"这两个还没统一的口子收掉——**改动面比看上去小**。

### 4.6 明确不做（防止范围膨胀）

- ❌ 不做通用 FS 协议抽象（不承诺 POSIX 全语义：权限位、属主、xattr、sparse、mmap）。
- ❌ 不抽 artifact store / session store（`ArtifactSink`、`ExtensionStore` 保持原样，它们是宿主态，不是文件视图）。
- ❌ 不覆盖 `internal/tools/code_index_resolver.go` 的 3 处 `os.*`（索引器是宿主本地进程，语义上不属于工具 I/O 面）。
- ❌ 不把 `Access` 暴露到 MCP schema 层（对模型不可见）。
- ❌ 不承诺 `Range`/HTTP 语义或 `If-Match`（除非 §11 U1 的 `/files` 路线成立，那也是实现细节，不进接口）。
- ❌ 不改工具名、档位归属、schema、输出文案（§3.4 那张表是硬约束）。

---

## 5. envd 增量能力专章（本方案的触发问题）

> 问题原话：**"envd 服务是否能支持增量处理，比如 grep、view 之类"**。答案是分三层的，混在一起谈会得出错误结论。

### 5.1 "增量"的三个层

| 层 | 定义 | envd 现状（实测） | 结论 |
|---|---|---|---|
| **传输增量** | 结果是否能在目标机产出时就到达客户端 | ✅ `process.Start` 返回 `*StreamReader`（`process.go:308`），`Recv` 逐事件；`Data{stdout,stderr,pty}`/`End`/`KeepAlive` 已在 `pty_*`、`watch_dir` 用过 | **能力已在**，是我们主动放弃了它 |
| **计算增量** | 目标机是否只处理被请求的那部分数据 | ⚠️ `filesystem` 无区间读；但可以在目标机跑 `sed`/`tail`/`rg` | **要靠"让目标机算窗口"实现**，不是靠 envd 提供随机读 |
| **呈现增量** | 模型/TUI 能否边到达边看到 | ❌ MCP 语义是"一次调用一个结果" | 与 envd 无关，需要 progress notification 贯通 manager/渲染层，**独立工程线** |

今天我们实际落在最差组合上：`remote_x__view`/`grep` 走 `Client.Run`（`process.go:326`），它**把整条流攒完再返回**，且 `DefaultMaxBufferedOutput = 8 MiB` 之后**丢弃但仍继续消费**（:324-326, 345-346）。也就是说：**传输是流式的，被我们的缓冲层吃掉了。**

### 5.2 `view`：可行路线与语义陷阱

**候选配方（按优先级）**

| # | 配方 | 得到什么 | 代价 | 状态 |
|---|---|---|---|---|
| R1 | `/files` 签名下载（若可本地算签名） | 真流式 + 可能 `Range` | 依赖未验证的签名算法 | ❓ 待调研（§11 U1） |
| R2 | 行窗口：远端 `sed -n 'A,Bp' -- "$1"` | 与本地 `offset`/`limit` **天然对齐**（行语义） | 一次 spawn；无 `rg` 依赖 | 未实现，**可立刻做** |
| R3 | 字节窗口：`tail -c +N -- "$1"` 再 `head -c M`；或 `dd bs=1 skip=N count=M` | 大文件抽样、二进制判定用 | 行/字节需换算 | 未实现，可立刻做 |
| R4 | 现状：远端 `cat` 整文件 | 简单、已知可靠 | 2 MiB 门拒大文件、大仓库慢 | ✅ 在用 |

**R2/R3 必须做对的语义清单**（任一条不核对就是新漂移源）：

1. **行区间 ≠ 字节区间**：`sed -n '100,140p'` 的行号以 `\n` 计，而核的 `offset` 语义建立在"解码后的行序列"上。CRLF 文件在 Go 侧与 `sed` 侧行数一致，但 **UTF-16/BOM** 文件不一致（`mutatecore.DetectEncoding`/`Decode` 能处理，`sed` 不能）→ 编码非 UTF-8 时必须回落到 R4 整读。
2. **末行无换行**：`sed` 对 EOF 无 `\n` 的最后一行行为与 Go `strings.Split` 不同，需对拍。
3. **二进制/NUL**：`sed` 在 NUL 上行为不可依赖 → 判定必须走 `Stat` 尺寸 + 头部采样（R3），且用 `LooksBinary`（纯函数，已有）。
4. **行长度 clamp**：核的长行处理在**解码之后**；远端窗口给的是原始字节，必须保证 clamp 仍在核里做（**不许在 envd 侧折叠**，`envd_tools.go:12-13` 的既有纪律）。
5. **symlink / jail**：仍走 `be.resolvePath` 一次，窗口命令只收**已解析的绝对路径**，且**只做位置参数**（`"$1"`），绝不拼接用户数据（现有脚本常量 `envd_backend.go:43-45` 就是这个模式）。
6. **`view` 的 batch/files 语义**：批量读多文件时，窗口配方要么每文件一次 spawn（往返爆炸），要么在核侧决定"batch 一律整读"——这是**能力位判断该写在核里**的又一个例子（§4.3）。

**验收**：R2 必须与本地 `viewcore` 逐字节对拍（§7.2 差分对拍），覆盖 CRLF / UTF-8 BOM / UTF-16 / 无末换行 / NUL / 超长行 / 空文件 / 目录 / 不存在 / jail 越界 9 类样本。

### 5.3 `grep`：最接近"免费"的真增量

1. **逐块消费**（传输增量）：把 `envdGrep` 从 `client.Run` 改为 `client.Start` + `Recv`，按块累积、达到上限即停。**改动很小，收益确定**。
2. **上限早停**（计算增量）：
   - `rg -m/--max-count`（每文件上限）与核的"总命中上限"语义**不等价**——不能直接换写，需在核里保留总上限判定，远端只做粗限；
   - 管道 `| head -n N` 触发 SIGPIPE 使 `rg` 早退（**今天已隐式在用**），但退出码变成 SIGPIPE 语义，需在结果里区分"截断"与"无更多匹配"——核已有 `OutputShaper`/截断标注，属同一族；
   - 精确终止：记住 `Start` 返回的 pid，达上限后 `SendSignal`（`process.go:417-420`，实测路由存在）。
3. **结构化流**：`rg --json` 可边到边解析行号/列/文本，与 `searchcore` 的结果模型对齐度高于解析纯文本；⚠️ 但目标机 `rg` 版本不可控（`envd-capabilities.md` 已记录版本探测待做），必须**能力探测 + 回落**，且引擎归属仍要对外公布（`BackendReporter`，`envd_tools.go:227-229` 的既有动机）。
4. **参数面缺口**（§8.9 登记项）：`glob`/`type`/`-i`/多 `patterns` 在远端方言下的映射仍未补齐。这些是**语义补齐**，与增量正交，但两者都落在同一段代码上——建议合并到 Phase 2.5b 一起消除（§6）。

### 5.4 `/files` 路线的调研任务（U1，0.5–1 天，纯只读）

需要回答三个问题，且**答案必须来自源码或真机，不许推测**：

1. 签名派生方式：输入包含哪些字段（path / username / op / 过期时间），HMAC 密钥来源是 access token 还是 envd 内部密钥。→ 从 E2B `runtime` 源码（我们有 `fcf15acb` 的构建记录，见 `envd-source-and-rebuild.md`）定位。
2. 是否支持 `Range`/分块，以及 `GET` 之外是否有 `PUT`（决定能否破 1 MiB argv 写上限）。
3. 与 jail 的关系：`/files` 是否受 access token 之外的路径约束？若它能绕过我们的 `--allowed-roots` 语义，**则一律不启用**（安全优先于性能）。

产出：一段可复现的真机探测记录 + 是否值得把它做成 `envdAccess.Read/Write` 的决策；若启用，`RandomRead`/`MaxWriteBytes` 能力位随之变化，**上层零改动**（这正是 seam 的价值验证点）。

### 5.5 不要向用户承诺的东西

- 不承诺"envd 支持随机读"——它不支持，我们是在目标机上**算**窗口。
- 不承诺"结果会流式出现在 TUI"——那是 §11 U3（progress notification 贯通），独立评审。
- 不承诺 `grep` 在 envd 与 local 上返回**完全相同**的字节：方言差异（`rg` vs `grep -rnI`）必须继续对外公布，直到有硬证据一致。

---

## 6. 阶段计划（插在既有 Phase 0–6 的缝里，故编号 2.5/3.5/4.5）

**全局规则**：每段一个（或一组）独立提交；每段结束时 `go build ./...` + 相关单测 + 真机活测（172.18.3.238）全绿；每段都有明确回滚点；**工具面 6/13/19 与档位表在任何一段都不得变化**。

### Phase 2.5a —— seam 骨架 + 只读核搬家（风险最低，先建结构）

- 交付：
  1. `toolcore/access.go`：`Access`/`Capability`/`RandomAccessor`/`Reviser` + `AccessProvider` 可选扩展；
  2. `toolcore/localio/access.go`：把 `viewcore`（5 处）与 `lscore`（1 处）的 `os.*` **原样搬入**，语义零变化；
  3. 核内改造点：`viewcore/handler.go`、`viewcore/read.go`、`viewcore/refusals.go`、`lscore/handler.go` 改为经 `Access` 取字节/取目录；
  4. **未实现 `AccessProvider` 的 Runtime 自动取 `localAccess`**（行为等价性由构造保证）。
- 验收门：`viewcore`/`lscore` conformance 全绿；`rg '\bos\.(ReadFile|Stat|ReadDir|Open)'` 在 `viewcore/lscore` 目录 = 0；现有 view/ls 单测**一字不改**通过（这是"搬家未改语义"的最强证据）。
- 回滚点：删掉 `AccessProvider` 注入即可回到 100% 旧路径。
- 成本：**2–3 天**。

### Phase 2.5b —— envd 只读侧接入 + 真增量（本方案的收益兑现点）

- 交付：
  1. `remotetools/envd_access.go` 实现 `Stat`/`ReadDir`/`Read`（RPC + `cat`），`Capabilities` 如实声明（`RandomRead=false`、`StreamingExec=true`、`Watch=true`）；
  2. `searchcore` 的 25 处 `os.*` 搬入 `localAccess`（含 `grep_ignore.go` 的 ignore 读取、`ripgrep_engine.go` 的 runner）；
  3. envd 的 `view`/`ls`/`glob`/`grep` **改走共享核**，删除对应的 envd 自实现；
  4. 若 §5.2 R2/R3 对拍通过：为 envd 提供 `RandomAccessor`（远端窗口）；若未通过，本段先只做整读，窗口留到 4.5。
- 验收门：
  - envd 侧 `remote_x__{view,ls,glob,grep}` 输出与共享核 conformance 对拍一致（差异按 §8.9 登记，目标：登记项数量 ≤ 现状）；
  - grep 流式改造后**大目录扫描字节量显著下降**（给出前后 `Run` vs `Start` 的实测抓取字节数）；
  - 真机 readonly 档 6/6。
- 回滚点：`envd_access.go` 与旧 `envd_tools.go` 并存一版（按 backend 选路一次，作为**过渡开关**，在 3.5a 结束时必须删除）。
- 成本：**4–6 天**（含 §5.2 九类样本对拍）。

### Phase 3.5a —— 写核搬家（纯 local 侧，先不动 envd）

- 交付：`mutatecore` 的 19 处 `os.*`（`atomic.go` 8 / `handler.go` 8 / `mutate.go` 4 / `patch.go` 4，含 `backup.go` 3）搬入 `localAccess.Write/MkdirAll/Remove/Rename` + `Reviser`。
- 验收门：`mutatecore` 现有单测与 `chat_*` 相关集成测试全绿；`WriteAtomic` 的 rename 语义在 `localAccess` 内保持原子（同目录临时文件 + rename）。
- 回滚点：同 2.5a（构造性等价）。
- 成本：**3–4 天**。

### Phase 3.5b —— envd 写侧接入（漂移彻底终结）

- 交付：`envdAccess.Write/MkdirAll/Remove/Rename`（`printf`/`mkdir -p`/RPC `MakeDir`/`Remove`/`Move`），如实声明 `AtomicReplace=false`、`NativeAppend=true`、`MaxWriteBytes=1<<20`；`edit`/`apply_patch` 完全走共享核（envd 侧仅保留 `envd_edit.go`/`envd_patch.go` 中真正不可替代的 argv 包装，目标删除 ≥ 600 行）。
- 验收门：
  - 已修漂移**不得复发**：父目录语义（local 拒 / envd 不 `mkdir -p`，`36caf76f`）、结果文案（`Created/Updated <相对> (N bytes).`、`Replaced N occurrence(s)…`、`Appended N bytes … (total M)`，`b65918eb`）、结构化 metadata 同名键（`8ec067cb`）三类用例进入 conformance 套件常驻；
  - 真机 standard 档 13/13、ops 档 19/19。
- 回滚点：过渡开关在此段**删除**（此后 envd 写侧只有一条路径）。
- 成本：**4–5 天**。

### Phase 4.5 —— 执行/流式统一（可选，取决于 U1/U3）

- 交付：`ExecStreamer` 归一到 `Access` 家族（`envd_process.go`/`envd_pty.go` 保留为传输实现，但由核统一决定超时/取消/信号）；`grep` 早停精确化（`SendSignal`）；若 `/files` 签名可用，`envdAccess.Read/Write` 升级为原生通道。
- 验收门：pty 面 6/6 不回归；shell 输出 8 MiB 丢弃行为改为**显式截断标注**（不再静默丢）。
- 成本：**3–5 天**（可拆到后续迭代，不阻塞前四段价值）。

### 依赖与并行建议

```
2.5a ──▶ 2.5b ──▶ 3.5a ──▶ 3.5b ──▶ (4.5)
              └── 可与 U1(/files 调研) 并行 ──┘
§10 三个近项可插在 2.5a 之后任意时刻，独立提交
```

`3.5a` 与 `2.5b` 互不依赖（不同核），若有并行算力可交错，但**写侧搬家必须在 envd 只读接入之后**，否则同时改两套实现体会撞车。

---

## 7. conformance 测试策略

### 7.1 一套用例，两个实现（核心机制）

新文件 `backend/internal/toolcore/access_conformance/access_conformance_test.go`：

```go
// Suite 是所有 Access 实现必须通过的用例表。核的行为只由能力位决定，
// 因此每个用例都带一个“能力前提”：实现声明不具备时，用例改断言降级路径。
type Case struct {
    Name       string
    Requires   toolcore.Capability // 能力前提
    Setup      func(a toolcore.Access, root string)
    Run        func(a toolcore.Access, root string) (got any, err error)
    Want       any                 // 声明能力时的期望
    WantWhenLacks any              // 未声明时的降级期望
}
func RunSuite(t *testing.T, implName string, a toolcore.Access)

// local: 临时目录
// envd:  真机 fixture 根（--allowed-roots 限定）或本地 fake envd 服务
```

- `localAccess` 在 CI 必跑（临时目录，无网络）。
- `envdAccess` 分层：**fake envd（桩服务，沿用 `envd_backend_test.go` 的 `envdStub`/`writeEnvDFrame`/`resolve:` 机制）在 CI 必跑**；**真机套件按现有 `scripts/remotetools_e2e.go`、`scripts/sshstdio_e2e.go` 模式做 gated 活测**，不进 CI 关键路径。

### 7.2 差分对拍（防"两套实现各自单测都绿但结果不同"）

同一棵 fixture 树（≥ 9 类样本：CRLF / UTF-8 BOM / UTF-16 / 无末换行 / 含 NUL / 超长行 / 空文件 / 符号链接 / 越界路径），对每个工具分别跑 local 与 envd，比较**归一化后的结果结构**（content + metadata 同名键），差异必须命中"已登记差异表"，否则失败。登记表随阶段推进单调收缩。

### 7.3 等价性回归（现有资产复用）

- `mutatecore` 纯函数已有充分单测 → seam 后**继续直接测纯函数**，不因接口化而削弱；
- `envd_failure_test.go` 的 `error_code`/`failure_class` 断言整体上移到共享核，成为跨后端的"错误契约"测试；
- `chat_remote_*_test.go` 系列（他人当前有未提交改动，**我只读不改**）作为端到端 surface 回归。

### 7.4 反漂移静态检查（可执行、可进 CI）

三条用脚本落地，放在 `backend/scripts/`（与既有 audit 脚本同族）：

1. **搬家完整性**：四个核目录（`viewcore`/`lscore`/`searchcore`/`mutatecore`）内 `os.(ReadFile|WriteFile|Stat|Open|Create|Remove|MkdirAll|Rename|ReadDir)` 计数必须为 0（阶段表里写明当前值，逐段下降）。
2. **禁止后端分支**：核目录内禁止出现字面量 `"envd"`（除注释与测试）。
3. **能力判断单点**：`Capabilities()` 的调用点在核目录内出现次数 ≤ 约定阈值（每类需求一处），超出即评审拦截。

这三条把 §4.1 三条纪律从"口头承诺"变成 CI 事实，是**本方案不退化的唯一保障**，优先级高于任何功能交付。

---

## 8. 风险与缓解

| # | 风险 | 触发条件 | 缓解 | 残留 |
|---|---|---|---|---|
| R1 | **抽象本身成为第二套词汇**（有人往 `Access` 里塞 tier/权限） | 接口评审缺失 | §4.1 纪律 1 写进验收门；`Access` 参数不出现 tool name/tier 类型 | 低 |
| R2 | **核内长出一堆 `if cap.X` 分支**，比今天的两套实现更难读 | 降级判断散落 | §7.4 检查 3（能力判断单点）+ §4.3 矩阵作为唯一权威 | 中 |
| R3 | 搬家过程中**悄悄改语义**（尤其 `WriteAtomic` 的 rename 时序、`backup.go` 的落点） | 人手替换 os 调用 | 2.5a/3.5a 要求**现有单测一字不改通过**；改动只允许"搬家 + 传参" | 低 |
| R4 | envd 侧改造后**性能变差**（多一次 spawn 取窗口，往返成本高于一次 `cat`） | R2/R3 窗口配方用于小文件 | 核侧规则：**小文件一律整读**（阈值由 `Capability.MaxReadBytes` + 核预算决定），窗口只在大文件生效；上线前实测每类样本往返次数 | 中 |
| R5 | `/files` 路线**不可用**（签名算不出，或它绕过我们的 jail） | U1 调研结果 | 方案不依赖它：§5.2 R2/R3 + §5.3 全部可独立成立 | 低 |
| R6 | 真机活测不可复现/凭据缺失导致验收卡住 | 目标机变更 | 桩套件（fake envd）在 CI 必跑，真机 gated；沿用 `scripts/remotetools_e2e.go` 既有模式 | 中 |
| R7 | **与他人未提交改动冲突**（当前工作区有 35+ 个未提交文件，含 `chat_remote_*`） | 并行会话 | 本方案只碰 `toolcore/*`、`remotetools/envd_*`、`scripts/`；**不碰** `cmd/aicli/commands/*`、`gateway/console/*`、`docs/user-guide/aicli-remote-tools.md` | 低 |
| R8 | 过渡开关（2.5b 的双路径）忘记删除 → **第三套实现** | 3.5b 未收口 | 2.5b 明确"此开关在 3.5b 删除"，并加入 §7.4 检查 | 高（必须盯） |

## 9. 成本汇总

| 段 | 成本 | 累计 | 兑现的价值 |
|---|---|---|---|
| Phase 2.5a | 2–3 天 | 3 | 结构就位，零行为变化 |
| Phase 2.5b | 4–6 天 | 9 | **只读面统一 + view/grep 真增量**（用户本次问题的落地） |
| Phase 3.5a | 3–4 天 | 13 | 写核可插 I/O |
| Phase 3.5b | 4–5 天 | 18 | **漂移终结，删除 ≥600 行 envd 自实现** |
| Phase 4.5 | 3–5 天 | 23 | 执行面统一、8 MiB 静默丢弃修正、可选 `/files` |
| §10 近项 | 1.5–2.5 天 | — | 不等 seam 的即时收益 |

对照：`tool-layer-abstraction-design-20261022.md` 成本表（行 2275–2276）Phase 2 与 Phase 3 各 3–5 天已完成。**seam 的总成本约等于把 Phase 2+3 再做一遍的 2 倍**，收益是把"远程适配"从人肉对齐变成结构保证——这与我们已经付出的两次漂移修复（`36caf76f`、`b65918eb`）相比，仍是正收益，因为那类修复会**持续发生**。

## 10. 不需等 seam 的近项（建议立刻做，且不会被 seam 浪费）

| # | 项 | 成本 | 落点 | seam 后归属 |
|---|---|---|---|---|
| N1 | `view` 远端窗口（§5.2 R2/R3 + 9 类样本对拍） | 0.5–1 天 | `remotetools/envd_tools.go` | 迁入 `envdAccess.RandomRead` |
| N2 | `grep` 流式消费 + 上限早停（`Run`→`Start`+`Recv`，达上限 `SendSignal`） | 0.5 天 | `envdGrep`（`envd_tools.go:227+`） | 迁入 `envdAccess` 的 `ExecStreamer` |
| N3 | `/files` 签名调研（§5.4 三问，只读探测） | 0.5–1 天 | 无代码，产出记录进本文 §11 U1 | 决定 `envdAccess.Read/Write` 是否升级 |

三项各自独立提交、独立回滚；即便后续走 seam，它们的**逻辑原封不动搬家**（这正是 seam 设计要检验的第一件事：如果近项代码搬不进 `Access`，说明接口设计错了）。

## 11. 未决问题（需要拍板，本文不替决策）

- **U1 `/files` 签名可否本地计算？** 若可，是否引入原生读写通道；若它绕过 `--allowed-roots` 语义，**默认不启用**。→ N3 调研后定。
- **U2 是否接受 2.5b 的过渡双路径（一版）？** 不接受则 2.5b/3.5b 合并为一次大改（回滚粒度变粗，R8 风险消失但风险集中）。
- **U3 呈现层增量（progress notification 贯通到 manager/TUI）是否立项？** 与 envd 无关，独立评审；不立项则本文的"增量"只到计算/传输层为止。
- **U4 `grep` 参数面缺口（§8.9：`glob`/`type`/`-i`/多 patterns 的远端方言映射）是否并入 2.5b？** 同一段代码，建议并入，代价 +1–2 天。
- **U5 envd 形态下 `PathResolver` 是否也归入 seam？** 今天 `PathResolver.Resolve` 在本机做 `filepath`/symlink 判定，envd 侧靠 `be.resolvePath`（RPC）。本文按"归入 `Access.Resolve`"设计；若评审希望保持 `PathResolver` 独立，则 `Access` 去掉 `Resolve`，改为 `envdAccess` 自带 resolver——需决策，因为它决定 2.5a 的改动半径。

## 12. 建议的最小评审动作（本文通过后）

1. 在 `tool-layer-abstraction-design-20261022.md` 的 §16.1 风险 3 与 Phase 2/3 处加一行交叉引用（指向本文），把"远程后续通过接口适配"这句占位**兑现成接口定义**。
2. 在 `aicli-envd-bridge-merge-plan-20261028.md` §8.9 后追加一节，把"envd 文件面独立实现"标记为**过渡态，偿还计划见本文**。
3. 立 `backend/scripts/` 的三个 §7.4 检查（可先只跑检查 1 的"计数下降"报表，不作硬门）。

---

## 附录 A：本文数字的复现命令

```powershell
# 3.1 搬家清单（60 处 / 19 文件）与核行数
cd backend
rg --no-heading -g '*.go' -g '!*_test.go' `
  '\bos\.(ReadFile|WriteFile|Stat|Open|OpenFile|Create|Remove|RemoveAll|MkdirAll|Rename|ReadDir|Glob|WalkDir|Getwd|EvalSymlinks|Lstat|Chmod|Truncate)\(' `
  internal/toolcore internal/tools
(Get-ChildItem internal/toolcore/viewcore -Filter *.go | ? Name -notlike '*_test.go' | % { Get-Content $_ } | Measure-Object -Line).Lines

# 2.2 toolcore 接口清单（22 + 2，无文件 I/O）
rg -n '^type [A-Z][A-Za-z]* interface' -g '!*_test.go' internal/toolcore

# 3.2 envd 原语矩阵
rg -n 'func \(c \*Client\)' internal/envdclient/filesystem.go
rg -n 'DefaultMaxBufferedOutput|func \(c \*Client\) (Start|Run|SendSignal)' internal/envdclient/process.go

# 3.3 /files 探测
curl -sk "https://<target>:49983/files?path=/etc/hostname&username=<user>" -o - -w "`nCODE=%{http_code}"

# 4.4 现存唯一后端分支（seam 的落点）
# internal/remotetools/server.go:66-90  “differ only in where files and processes actually live”
# internal/remotetargets/registry.go:30/42/91  ssh-stdio 在目标机侧执行 / envd 走 Connect-RPC
```

## 附录 B：术语

| 术语 | 本文含义 |
|---|---|
| **seam** | 可插入替代实现的位置；本文特指"核取字节/落字节"的那一处 |
| **能力位** | `Capability` 字段/可选接口，描述后端**能做到什么**，非**该做什么** |
| **降级** | 能力缺失时核的**统一**行为（不是后端的私有行为） |
| **漂移** | 同一工具在两个实现体上给出不同结果/文案/错误码 |
| **执行腿** | 工具实际在目标机上执行 I/O 的那条通路（local = 进程在目标机；envd = Connect-RPC 到目标机守护进程） |
| **登记差异** | §8.9 式纪律：不一致必须写明，不做表面糊合 |
