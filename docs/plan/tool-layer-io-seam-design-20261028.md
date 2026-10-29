# 工具层 I/O seam 与 envd 增量能力方案

- 文档编号：`tool-layer-io-seam-design-20261028`
- 状态：**部分已落地（v8）**。逐版：v1 结构草案；v2 用 C-1 真机 spike 实测替换 §5.2/§5.4/§10/§11 的假设并新增 §3.5 安全发现；v3 回填 §13（N0b+N0 交付 `a8ec5aaf`）；v4 记录 `552bf850`（`--max-bytes-per-call` 缺陷修，含一处对外可见的行为变更）与 `29d6e334`（N2 grep 流式 + 上限早停）；v5 把 §7.4 三条反漂移检查做成 `backend/internal/driftguard` 硬门测试；v6 记录 `d936b751`（写侧 `POST /files` + 可写闸门 + 两条协议陷阱：raw body 在 0.2.11 返回 500、字段名非 `file` 时 `200 []` 而什么都不写）；v7 记录 `c0226f10`（读闸门下沉到唯一入口 `readGuarded`，含绕开闸门的反向验证）；**v8 记录 `4302b807`：NUL/尺寸拒绝改由通道能力判定，UTF-16 编辑往返恢复，并写明 JSON 传输无法携带非 UTF-8 字节这条我们修不了的边界。§8.9 现只剩 `view` 行窗口一项未关。**
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
| 计算层 | `filesystem.*` 无 ReadFile/WriteFile/区间读；**但 `GET /files` 提供真随机读**（实测 `Range` → 206 逐字节精确）；`process` 可跑 `rg`/`sed` | **view 已可 O(窗口) 而非 O(文件)**；grep 可早停。远端 `sed` 造窗降为回落路径 |
| 呈现层 | MCP 一次调用一个结果 | 真"边跑边显示"需 progress notification 贯通，**是另一条工程线**，与 envd 无关 |
| **`/files`（已 spike，读写均已启用）** | header token 即可（不需签名）；200/206/416/400/404 全部实测；`POST` 无 argv 上限 | ✅ **`Access.Read/Write` 的首选通道**；但 `Last-Modified` 是请求时间、无 ETag、gzip 在 0.2.11 不生效（**条件读不可用**），**无 jail 且以 root 身份读写**（§3.5），另有两条协议陷阱：raw body 在 0.2.11 返回 500（必须 multipart），multipart 字段名非 `file` 时**返回 `200 []` 却什么都没写**（§3.3.3） |

推荐顺序：**§10 近项（N0b+N0 已交付）→ N2 → 写侧 → 再按 §6 分五段落 seam**；每段独立可回滚。**§3.5 的可读性闸门是安全项，优先级高于性能项——它与通道同批交付。**

**进度的事实修正（v2 写下的顺序有误）**：N0b 不能先于 N0 交——单独先交闸门会留下一段**没有消费者的死代码**（今天 `cat` 已受内核约束，无提权可堵），先交通道则先上提权。因此两者已**同批落地**（`a8ec5aaf`，真机差分见 §13）。

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
| **读文件字节** | `GET /files`（HTTP，非 RPC） | ✅ **已启用**（`a8ec5aaf` 读、`c0226f10` 全部读入口）：`view`/`edit`/`apply_patch` 均经 `readGuarded`，`cat` 降为兜底 | `Access.Read` 首选 `/files`，**闸门在唯一入口里**（不是每个调用点） |
| **写文件字节** | `POST /files`（multipart） | ✅ **已启用**（`d936b751`）：`envdclient.WriteFile`，content 不再是 argv 元素，**2,450,000 字节实测逐字节落盘**（旧的 argv 天花板 1 MiB 不再适用）；**NUL/UTF-16 写也已恢复**（`4302b807`，按通道能力判定），两个协议陷阱已成文（见下） | `Access.Write` 经 `/files` 不再受 argv 上限与 NUL 限制 |
| **字节区间读** | `GET /files` + `Range` | ✅ **实测 206 且逐字节精确**（§3.3） | `RandomRead` 在 envd 上**为 true**（探测得，非硬编码） |
| 条件读（304/ETag） | `GET /files` + `If-*` | ❌ **不可用**：`Last-Modified` 回的是**请求时间**，`ETag` 不发（§3.3 实测） | 禁止把 revision 建立在 `Last-Modified` 上，仍用内容哈希 |
| 传输压缩 | `Accept-Encoding: gzip` | ❌ 部署版 **0.2.11 不生效**（`main` 分支源码已有该路径） | 能力必须探测，不得假设版本 |
| 进程执行 | `process.Process/Start`（`process.go:308` 返回 `*StreamReader`） | ✅ server-streaming | `Access.Stream` 原生可用 |
| 缓冲执行 | `Client.Run`（:326） | ✅ 但 `DefaultMaxBufferedOutput = 8<<20`，超出**丢弃且继续消费**（:324-326,345-346,353-358） | 今天的 `grep`/`view` 走这条 → **不增量** |
| 提前终止 | `SendSignal`（:417-420）、`Update`、`Connect` | ✅ unary | 早停能力齐备 |
| 交互式输入 | `StreamInput`（client-streaming，需 HTTP/2） | ⚠️ 客户端未支持 | 不影响本方案 |
| **`GET/POST /files`** | 未在客户端封装（`envdclient` 内零引用） | ✅ **实测可用**（header token 即可，见 §3.3） | envd 唯一的原生文件通道，**`Access.Read/Write` 的首选实现** |

> 修正记录（两条，均为我自己写错后被实测推翻）：
> 1. `CreateSymlink`/`GetFilesHash` 在 `envd-capabilities.md` 的实测表中**没有**出现，因此**不得作为本方案任何设计前提**。
> 2. 本文 v1 曾写"`/files` 不吃 `X-Access-Token`，吃签名查询参数"——**错了**，那是只观察到"未认证 → 401"得出的误推断。`auth.go:116-123` 与本轮真机都证明**头里带令牌直接放行**，签名只是预签名 URL 用的替代路径（§3.3）。

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

以上是**阶段一（外部、未认证）**的记录，它只能证明“路由存在 + 非开放”。**阶段二（真机 spike，已认证）**于同日完成，这才是本方案的依据：

### 3.3.1 C-1 spike 结果（envd **0.2.11** @ `172.18.3.238:49983`，全程只读，产物已清理）

| 探针 | 结果 | 判定 |
|---|---|---|
| `GET /files?path=/home/n9e/.bashrc&username=n9e` + `X-Access-Token` 头 | **200**，3814B，`accept-ranges: bytes` | ✅ **签名不需要**（`auth.go:116-123` 实证） |
| 裸 curl（无任何令牌）/ 空值头 / 错误令牌 | 均 **401** | ✅ 不是开放通道 |
| `Range: bytes=0-9` | **206** `content-range: bytes 0-9/3814`，10B，**与 `head -c 10` 逐字节一致** | ✅ 真随机读 |
| 尾窗 64B / 中窗 37-52 | 206 `3750-3813/3814`、`37-52/3814`，均 **EXACT-MATCH** | ✅ 任意窗口 |
| **10 MiB 文件**取头/中/尾各 1 KiB | 三次 206，**每次只传 1024B** | ✅ **`view` 成本从 O(文件) 降到 O(窗口)** |
| `Range: bytes=99999-100100`（越尾） | **416** `content-range: bytes */3814` | ✅ 可映射为核侧错误码 |
| `Range: bytes=0-9,20-29`（多区间） | 206 `multipart/byteranges; boundary=…`，368B | ⚠️ 客户端**只用单区间** |
| 目录 / 不存在 | **400** / **404**（json body） | ✅ 与现有 `view` 拒目录、`missing_file` 语义一致 |
| `ETag` / `If-None-Match` | **不发 ETag**；空标签请求返回 200 全量 | ❌ 条件读不可用 |
| `If-Modified-Since: 2099` | 304；但 `Last-Modified` 为**请求时间**（相隔 2s 两次探测分别为 14:20:32 / 14:20:34，而文件真实 mtime 是 2025-04-20） | ❌ **真实客户端永远拿不到 304** |
| `Accept-Encoding: gzip` | 200 identity，3814B（`file` 判为 ASCII text） | ❌ 0.2.11 不压缩 |
| `POST /files` 新建 / 同名覆盖 | 200 / 内容变为新值（`O_TRUNC`，`upload.go:95`） | ✅ **无 1 MiB argv 上限** |
| `POST /files` 父目录不存在 | **200 + 自动建父目录**（`EnsureDirs`，`upload.go:59`） | ⚠️ 必须核侧先拦，否则 `36caf76f` 的 `missing_parent` 语义会被通道差消灭 |
| `username=nosuchuser` | 401 `error looking up user` | ℹ️ 仅用于 `~` 展开与写入 chown |
| `/files/?path=…`（尾斜杠） | 404 `404 page not found`（19B） | ℹ️ 路由无重定向，客户端 URL 要写死 `/files` |

### 3.3.2 阶段二推翻的三条结论（包括本文 v1 的一条自错）

1. **U1 关闭**：`/files` **用我们已有的 `X-Access-Token` 头就能用**；签名（`auth.go:88-105`，`v1_` + SHA256(`path:operation:username:token`[`:exp`])）是预签名 URL 用的替代路径。**客户端不应自己实现签名**——上游用 `subtle.ConstantTimeCompare`（:155），自研版很容易在这个细节上退化，而 header token 已足够。
2. **原结论“`/files` 不吃 access token”是错的**：我当时只观察到“未认证就 401”就反推了。本轮裸 curl/空头/错令牌都是 401，带令牌是 200，**不存在开放文件读**。
3. **区间/流式读真的能省带宽，但“条件读省带宽”省不了**：因为 `Last-Modified` 不是文件 mtime。**不得把缓存/并发检测建在 `Last-Modified` 上**，仍用内容哈希（`Reviser`）。

### 3.3.3 写侧的两条协议陷阱（`d936b751` 实测）

这两条都是**先读源码会写错**的类型，所以单独记录：

| 陷阱 | 实测行为 | 因此实现成 |
|---|---|---|
| raw body 上传 | 0.2.11 把每个 POST 都按 multipart 解析，`Content-Type: application/octet-stream` 得到 **`500 error parsing multipart form`**（尽管上游 `main` 已支持 raw 分支） | 只用 **multipart**；注释写明“跟随跑着的产物，而不是更新的分支” |
| 字段名 | multipart 的 part 字段**必须恰好是 `file`**。字段 `file` → 200 且落盘 15B；字段 `files` → **200 `[]`，什么都没写**（`upload.go:220` 对其它字段名直接 `continue`） | 不只依赖状态码：校验响应里列出的路径，新增错误码 `FilesNothingWritten`；**它故意不算通道级问题**（`IsFilesChannelIssue=false`），否则一次失败的写会被静默重试成另一种“成功” |
| 额外收益 | `POST` 走 chown（`upload.go:83`）→ 实测文件变成 `n9e:n9e 0644` | 无需额外 chown；也说明**写入归属与 `printf` 路径一致** |

### 3.4 档位与工具面一致性（收编成果，作为 seam 的约束保留）

| 后端/形态 | readonly | standard | ops |
|---|---|---|---|
| envd（真机 19/19 + pty 6/6） | 6 | 13 | **19** |
| ssh / local（同一二进制，实测 15/15） | — | 8（`view ls glob grep write edit append_write apply_patch`） | 9（+`shell`） |

`remote_x__{view,ls,glob,grep,host_metrics,watch_dir}` / `{write,edit,append_write,apply_patch,mkdir,remove,move}` / `{shell,pty_start,pty_write,pty_read,pty_resize,pty_close}`。**seam 改造不得改变这张表**——工具名、档位归属、schema 是对外契约，`Access` 只是内部落点。

### 3.5 真机安全发现（必须成为 `Access` 的边界条件）

本轮最重的发现不是性能，而是**权限非对称**（读写两个方向都是）：

```
GET  /files?path=/etc/shadow&username=n9e   -> 200, 1401 bytes      # envd 以自身进程身份（root）读
n9e@target$ head -c 1 /etc/shadow          -> Permission denied    # 目标用户本身读不了
GET  /files?path=/etc/shadow&username=root  -> 200, 1401 bytes      # username 不造成降权
WRITE /files -> /root/<user-writable-nothing>                      # 同非对称的写方向，后果更重
```

配合两个已确认的上游事实：`auth.go:27-32` 把 `GET/files`、`POST/files` 列入 `authExcludedPaths`（中间件不拦，由 handler 内 `validateSigning` 兜底）；`permissions/path.go:38` 对绝对路径**直通返回，无任何 jail**。

**四条结论（写进设计，不可讨论）：**

1. **`/files` 只能做核内实现细节**，绝不暴露为工具、绝不接受 raw path：`Access` 收到的必须是**已过 jail 的已解析目标**（与 §4.1 纪律 1/4 一致）。
2. **必须加“目标用户可访问闸门”，读写两面都要**（原 U7，已定为做；`a8ec5aaf` 读向、`d936b751` 写向已落地）：否则同一个 `view`/`write` 工具经 `/files` 能看到/覆盖 `shell` 工具碰不了的文件，这是**相对提权**。实现上**绝不在 Go 里重实现 POSIX 权限判定**（易错且遇 ACL/组/namespace 就错），而是用**内核自己回答**：读 `test -r "$1"`；写 `test -w "$1"`，且写新文件时**先向上走到第一个存在的祖先**再判（创建是目录的问题，而 `apply_patch` 合法地整棵树新建）。
    **成本控制取代 v2 的写法**：v2 说过“按目录缓存结论”，那个设计是错的——一个文件的权限不能由它所在目录的结论推得。现在的规则是**只在能确定放行时免探测**：`Stat` 已 world-readable → 读不探；目标由本用户拥有且 `u+w`，或新建时其目录由本用户拥有且 `u+w|u+x` → 写不探。“确定 yes”才跳，任何不确定一律问内核。效果：自有工作区里的常规写入**零额外 spawn**（测试以 `writableChecks == 0` 断言），而回退 `printf` 时**不做**探测（内核已经在管，多探是纯成本）。
    ⚠️ 本项**不是假想风险**：目标机上 `~/.aicli/remote-238.yaml` 的 `allowedRoots` 就含 `/var/log`，那里大量文件是 `0640 root:<group>`，`n9e` 读不到——**jail 内就会发生**。写向的真机证据：对 `/root/` 下一个 root 拥有的 0644 文件发起 `write`，闸门拒绝后**由 root 复核内容仍是原值**——“报了拒绝”与“确实没写进去”是两件事，只证前者不够。
3. **沿用上游的 fail-closed 姿态**：`401 envd not initialized`（`auth.go:65-81`，live-upgrade pre-init 窗口故意把 `/files` 排除在允许列表外）一律视为后端不可用并**回落 `process` 通道**，绝不静默返回空。
4. **令牌与日志卫生**：`/files` 的 URL 含 `path`/`username`，审计与日志**只记方法 + 状态码 + 长度**，不记完整 URL、不记令牌；本轮探针已做到（令牌只存在于目标机 shell 变量与 600 权限的 curl 配置文件，全程未回显）。

另需记一笔**已测得的具体偏差**（§8.9 式登记）：`POST /files` 的 `EnsureDirs`（`upload.go:59`）会自动建父目录，与 local 面的 `missing_parent` 拒语义不同。**该差异已在核侧消除**（`d936b751`）：`storeBytes` 在进 `Access`/通道**之前**先查父目录，`write` 依旧拒（`missing_parent`），只有 `apply_patch add`（`createParents=true`）建树；测试断言“被拒的写不得落盘”。不得依赖传输层行为，这条仍是铁律。

---

## 4. 设计：`Access`（工具层唯一的文件 I/O seam）

### 4.1 三条不可退让的纪律

这三条是"会不会又长出两套工具代码"的唯一防线，写进验收门：

1. **`Access` 只表达**能力，不表达**策略。** 接口里没有 tier、没有 permission、没有 tool name。策略（档位、jail 判定、权限、预算、折叠）全部留在核与既有 adapter。违反这条 = 第二套策略，直接否决。
2. **核内禁止 `if backend == "envd"`。** 唯一允许的分支是**能力位判断**，且同一判断在核里**只允许出现一次**（收进 `resolveCapability()` 一类函数，其余调用点读结论）。用 lint/评审清单把这条变成可检查项（§7.4）。
3. **共享 conformance 套件跑两个实现。** 同一张用例表，`localAccess` 与 `envdAccess` 都必须全绿；不一致要么改实现，要么显式登记为差异并写进文档（沿用 §8.9 纪律，但登记项应随 seam 推进**单调减少**）。
4. **策略绝不沉入 `Access`：jail、目标用户可读性闸门、档位、尺寸门全部在核侧先行。**（§3.5）`Access` 只接受已解析、已过闸的目标，因此它无法成为越权通道——本轮实测的 `/etc/shadow` 提权风险就是这条纪律存在的理由。

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
    RandomRead      bool  // 能否只取 [off, off+n)（local: pread；envd: `GET /files` + `Range`，实测 206 精确）
    AtomicReplace   bool  // 能否同目录 rename 替换（local: 是；envd printf 重定向: 否）
    NativeAppend    bool  // 能否不读回就追加（envd: printf >> 是）
    StreamingExec   bool  // 能否 spawn 进程并流式取字节（envd: 是，pty/shell 已在用）
    Watch           bool  // 事件流（envd: 是）
    MaxWriteBytes   int64 // 0 = 无上限；`/files` 通道：0（实测 POST 无 argv 上限）；`process+printf` 回落：1<<20
    MaxReadBytes    int64 // 核侧尺寸门，实现可更低
    ConditionalRead bool  // 能否用 If-* 做 304（envd 0.2.11: **false**，Last-Modified 是请求时间且不发 ETag）
    GzipEncoding    bool  // 能否 `Accept-Encoding: gzip`（envd 0.2.11: **false**，实测不生效）
}

// ⚠️ 以上位**必须由探测得出，不得按版本号硬编码**。`ConditionalRead`/`GzipEncoding`
// 在部署版 0.2.11 与 `main` 分支源码之间已经存在差异（§3.3.2），版本推导必错。

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
| `view` 带 offset/limit | local: `pread`；envd: `GET /files` 单区间 `Range`（**实测 206 逐字节精确**） | 回落：远端 `sed -n 'A,Bp'` / `dd skip` 造窗（**仅 `/files` 不可用时**） | 整读 + 核侧 clamp（**今天的行为**） |
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
- **`envdAccess` 的组成（`/files` 优先，`process` 回落）**：`Stat/ListDir/MakeDir/Remove/Move/WatchDir` → 原生 RPC；`Read/RandomRead` → `GET /files` 单区间 `Range`（探测 206 可用）→ 失败则 `process.Start` + `cat`；`Write` → `POST /files`（multipart，**破 1 MiB argv 上限**）→ 失败则 `printf` 且保留 argv 上限报错；**进入 `/files` 前必须已完成 jail + 父目录检查 + 可读性闸门**（§3.5、§4.1 纪律 4），保持"内容永不拼接进脚本、只走 multipart 字段或位置参数"的现有纪律（`envd_backend.go:41-45`）。
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
- ❌ 不把 HTTP 语义放进接口：`Access` 不出现 `Range`/`Content-Range`/状态码/ETag 字样（`/files` 是 `envdAccess` 的内部实现细节，换成 gRPC 流也不影响上层；§3.3 已证实 `Range` 可用，但这仍不进接口）。
- ❌ 不改工具名、档位归属、schema、输出文案（§3.4 那张表是硬约束）。

---

## 5. envd 增量能力专章（本方案的触发问题）

> 问题原话：**"envd 服务是否能支持增量处理，比如 grep、view 之类"**。答案是分三层的，混在一起谈会得出错误结论。

### 5.1 "增量"的三个层

| 层 | 定义 | envd 现状（实测） | 结论 |
|---|---|---|---|
| **传输增量** | 结果是否能在目标机产出时就到达客户端 | ✅ `process.Start` 返回 `*StreamReader`（`process.go:308`），`Recv` 逐事件；`Data{stdout,stderr,pty}`/`End`/`KeepAlive` 已在 `pty_*`、`watch_dir` 用过 | **能力已在**，是我们主动放弃了它 |
| **计算增量** | 目标机是否只处理被请求的那部分数据 | ✅ `GET /files` + 单区间 `Range`（**实测 10 MiB 文件只传 1024B**）；另有 `process` 可跑远端 `rg`/`sed` | **envd 本身就能做到计算增量**（靠 HTTP 区间读），不需我们造窗口 |
| **呈现增量** | 模型/TUI 能否边到达边看到 | ❌ MCP 语义是"一次调用一个结果" | 与 envd 无关，需要 progress notification 贯通 manager/渲染层，**独立工程线** |

今天我们实际落在最差组合上：`remote_x__view`/`grep` 走 `Client.Run`（`process.go:326`），它**把整条流攒完再返回**，且 `DefaultMaxBufferedOutput = 8 MiB` 之后**丢弃但仍继续消费**（:324-326, 345-346）。也就是说：**传输是流式的，被我们的缓冲层吃掉了。**

### 5.2 `view`：路线已被 C-1 spike 重新排序（v1 的 R1→升为现 **R0** 主路径；v1 的 R2/R3→降为现 R1/R2 回落）

**候选配方（按实测后的优先级）**

| # | 配方 | 得到什么 | 代价 | 状态 |
|---|---|---|---|---|
| **R0** | **`GET /files` + 单区间 `Range`（header token）** | **真随机读**：O(窗口) 传输、`Content-Range` 带总长度、越界给 416、目录 400、不存在 404 | 需客户端封装一个 HTTP 通道（非 Connect-RPC） | ✅ **本轮已实测可用** |
| R1 | 行窗口：远端 `sed -n 'A,Bp' -- "$1"` | 无 HTTP 依赖时的行语义 | 一次 spawn；行/字节换算 | ⬇️ **降为回落**（仅 R0 不可用时） |
| R2 | 字节窗口：`tail -c +N` / `dd bs=1 skip= count=` | 抽样与二进制判定 | 行/字节换算 | ⬇️ **降为回落** |
| R3 | 现状：远端 `cat` 整文件 | 简单、已知可靠 | 2 MiB 门拒大文件、大仓库慢 | ✅ 在用（仍作兵底） |

**因为 R0 成立，§5.2 的语义陷阱清单从“九类”收收敛为三类**（这是把窗口从 shell 回到 HTTP 的最大收益：**不再由目标机的文本工具决定行/字节语义**）：

1. ~~行区间 ≠ 字节区间~~ → **消失**：`Range` 给字节，行/解码/clamp 全在核里做，与 local 同一套代码。
2. ~~`sed` 对 CRLF/BOM/UTF-16/无末换行的行为需对拍~~ → **消失**：不再产生第二套行语义。
3. **保留：二进制/NUL 判定**仍须 `Stat` 尺寸 + 头部采样 + `LooksBinary`（纯函数），**不得让 `sed`/`file` 决定**。
4. **保留：行长度 clamp 与输出折叠仍在核里**（`envd_tools.go:12-13` 既有纪律，不因换通道而变）。
5. **保留：jail / 可读性闸门 / 父目录检查在进 `/files` 前完成**（§3.5）——**这是换通道后新增的唯一语义义务**。
6. **新增：多区间会返回 `multipart/byteranges`（实测 368B）**——客户端**只发单区间**，不要自己解 multipart。
7. **新增：416 （越尾）必须映射为核侧的可诊断错误**，不能当 5xx 抛（实测 `content-range: bytes */3814`）。

**验收**：R0 与本地 `viewcore` 逐字节对拍（§7.2），样本集从 9 类缩至：二进制/NUL / 超长行 / 空文件 / 目录(400) / 不存在(404) / 越尾(416) / jail 内越权文件（§3.5 闸门用例）七类——**条数少了，但反而多了一个安全项**。

### 5.3 `grep`：最接近"免费"的真增量（**已落地 `29d6e334`**）

1. **逐块消费**（传输增量）：✅ 已实现——`envdGrep` 从 `client.Run` 改为 `client.Start` + `Recv`（`envd_backend.go` 的 `runCapped`），按帧累积、达上限即停。
   ⚠️ 一个只有真机才暴露的细节：停止读取**不等于**遵守上限。输出以帧到达，单帧可以远超上限，首版因此返回了 4210 字节（cap=512）；必须在返回前把缓冲剪到 `capBytes`。
2. **上限早停**（计算增量）：✅ 已实现，但**用的是取消 ctx + 关闭流**而不是下面设想的两种做法——`process.go` 对 `Start` 的文档写明"cancel ctx 是受支持的停止方式，envd 在流客户端消失时回收子进程"，比另发一次 `SendSignal` 少一个往返。真机验证早停确实发生（桩侧把后续写失败记为 `streamCut`）。
   - `rg -m/--max-count`（每文件上限）与核的"总命中上限"语义**不等价**，因此没有采用——保留核侧的字节/命中预算作为唯一上限来源；
   - **更正一处旧断言**：本文 v1 与对话早期我说过"`| head -n N` 触发 SIGPIPE 使 `rg` 早退，今天已隐式在用"。核对代码后这是**错的**：envd 的 grep 脚本里没有任何管道，那是我从假设当成了事实。截断语义现在由 `runCapped` 明确产出（`is_truncated` + 一行说明），不依赖 SIGPIPE 的退出码歧义。
3. **结构化流**（未做）：`rg --json` 可边到边解析行号/列/文本，与 `searchcore` 结果模型对齐度高于解析纯文本；⚠️ 但目标机 `rg` 版本不可控（`envd-capabilities.md` 已记录版本探测待做），必须**能力探测 + 回落**，且引擎归属仍要对外公布。本轮已把引擎标记 `__TOOL__=` **移到输出的首行**：截断发生在流中间时，尾部标记会连同它描述的那段结果一起丢失，而 rg 与 `grep -rnI` 的 pattern 方言不同——引擎归属必须在截断后仍然可知；标记缺失时如实报 `unknown`，不猜。
4. **参数面缺口**（§8.9 登记项，未做）：`glob`/`type`/`-i`/多 `patterns` 在远端方言下的映射仍未补齐。这些是**语义补齐**，与增量正交，但两者落在同一段代码上——建议合并到 Phase 2.5b 一起消除（§6）。

### 5.4 `/files` 路线（**U1 已解决**：2026-10-28 C-1 spike，源码 + 真机双向确证）

原三个调研问题现在都有答案：

1. **签名派生方式** → `auth.go:88-105`：`v1_` + SHA256(`path:operation:username:token`[`:exp`])，即**共享密钥普通哈希，不是 HMAC**。**但我们不需要它**：`auth.go:116-123` 先看 `X-Access-Token` 头，匹配即 `return nil`；真机实测带令牌头 → **200**。**结论：客户端不实现签名**（上游用 `subtle.ConstantTimeCompare`，自研易在该细节上退化），签名仅用于“递给第三方的短时 URL”。
2. **`Range`/分块与写通道** → `download.go:123-172` 显式处理 `Range`/`If-Range`/`If-Modified-Since`/`If-None-Match` 并交给 `http.ServeContent`；真机 **206 逐字节精确**，越尾给 416。写侧是 `POST`（非 `PUT`）：multipart + `O_TRUNC` + chown（`upload.go:95,83`）→ **破 1 MiB argv 上限**；但 `EnsureDirs`（:59）会自动建父目录，**必须核侧先拦**（§3.5）。
3. **与 jail 的关系** → **无 jail**：`permissions/path.go:38-39` 对绝对路径直通返回；真机 `path=/etc/shadow&username=n9e` → **200（1401B）**，而同用户本地 `head -c 1 /etc/shadow` → **Permission denied**。原定“**若绕过 `--allowed-roots` 则一律不启用**”这个条件**已触发**。

产出与**启用方式的修正**：不启用 ≠ 安全；**不启用则我们永远只能用 root 降不到权的 `shell` 看文件，而 `/files` 仍然存在**——真正的控点在客户端。因此：**默认启用 `/files`，但“jail + 可读性闸门 + 父目录检查”三道在核侧先行，闸门不过就拒**（U6/U7 已定，见 §11）。能力位随之变为 `RandomRead=true`/`MaxWriteBytes=0`；`ConditionalRead=false`/`GzipEncoding=false` **仍由探测得出**，不得写死。**上层零改动**——这正是 seam 的价值验证点。

### 5.5 不要向用户承诺的东西

- ~~不承诺"envd 支持随机读"~~ → **本轮已实测支持**（`GET /files` + 单区间 `Range` → 206，与 `head -c`/`tail -c`/`dd` 逐字节一致）。仍不承诺的是：**envd 提供原生字节级区间 RPC**——`filesystem.*` 里就是没有，随机读来自 HTTP 通道。
- 不承诺**条件读能省带宽**：实测 `Last-Modified` 是请求时间、`ETag` 不发 → 304 在真实客户端下不可达（§3.3.1）。
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
  4. **已由 C-1 spike 定案**：`RandomAccessor` 走 `GET /files` + 单区间 `Range`（§5.2 R0），**不进 `Access` 接口**（属实现细节，§4.6）；`sed`/`dd` 窗口降为通道不可用时的回落。本段**同时必须交付 §3.5 的可读性闸门（U7）与父目录前置检查**，它们与本项同批提交，不得先上性能后补安全。
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

### Phase 4.5 —— 执行/流式统一（可选，取决于 U3；原“取决于 U1”已失效——U1 由 C-1 定案）

- 交付：`ExecStreamer` 归一到 `Access` 家族（`envd_process.go`/`envd_pty.go` 保留为传输实现，但由核统一决定超时/取消/信号）；`grep` 早停精确化（`SendSignal`）。~~若 `/files` 签名可用，`envdAccess.Read/Write` 升级为原生通道~~ → **该项已提前到 Phase 2.5b/3.5b**（C-1 已证实 header token 即可，不需签名），本段不再承担。
- 验收门：pty 面 6/6 不回归；shell 输出 8 MiB 丢弃行为改为**显式截断标注**（不再静默丢）。
- 成本：**3–5 天**（可拆到后续迭代，不阻塞前四段价值）。

### 依赖与并行建议

```
2.5a ──▶ 2.5b ──▶ 3.5a ──▶ 3.5b ──▶ (4.5)
         └─ 本段已含 N0 的 /files 区间读与 N0b 闸门（原 U1 调研项已由 C-1 定案）
§10 近项顺序：**N0b（安全闸门）→ N0（/files 区间读）→ N2（grep 流式）**；
它们可插在 2.5a 之后任意时刻，各自独立提交；完成后再进 2.5b 就是“把已验证的逻辑搬家”。
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

> 换用 `/files` 后这些样本**仍然必要**，但用途变了：CRLF/BOM/UTF-16 不再牵涉远端 `sed` 的行语义（窗口由 HTTP 给字节，解码全在核里），它们现在验的是**整读解码一致性**；另新增两类必测：jail 内越权文件（§3.5 闸门）与 416 越尾错误映射。

### 7.3 等价性回归（现有资产复用）

- `mutatecore` 纯函数已有充分单测 → seam 后**继续直接测纯函数**，不因接口化而削弱；
- `envd_failure_test.go` 的 `error_code`/`failure_class` 断言整体上移到共享核，成为跨后端的"错误契约"测试；
- `chat_remote_*_test.go` 系列（他人当前有未提交改动，**我只读不改**）作为端到端 surface 回归。

### 7.4 反漂移静态检查（**已上线**：`backend/internal/driftguard`，随 `make test` 运行）

原计划把三条检查写成 `backend/scripts/` 下的脚本。实现时改了主意，**原因必须记下来**：本仓库已有先例把结构性不变式做成普通测试（Makefile 里写明 `make test` 会跑 `cmd/contractgen` 的 `TestGeneratedFileIsUpToDate`，这正是生成物可信的原因）。需要有人"记得跑"的检查等于没有检查。因此三条现在是 `go test ./...` 里的测试，入口零新增。

已落地的三条：

1. **`TestSharedCoresDoNotAddHostFilesystemCalls`（棘轮）**：四个核目录内直接 `os.*` 文件系统调用的基线为**实测** `viewcore 5 / lscore 1 / searchcore 24 / mutatecore 27 = 57`；加上 `internal/tools/code_index_resolver.go` 的 3 处正好 60，与 §3.1 用 grep 得到的数字吻合。**两种独立方法得出同一数字**，这条基线才可信。计数用 AST（`go/ast`）而非正则：正则会把注释与字符串里的 `os.ReadFile` 也数进去，一个因别人改注释而报假回归的守卫一周内就会被关掉。
   - 超出基线 → 失败；**低于基线也失败**（要求把基线写下去），否则上限一直悬在那里，下一次重新加回调用无人报错。
   - 扫描目录不存在或不是目录 → 报错而不是返回 0："静默量不到东西"的守卫会永远绿。
2. **`TestSharedCoresNeverBranchOnBackendNames`（硬零）**：核目录内不得出现 `"envd"` / `"ssh"` / `"http"` / `"local"` 字符串字面量（注释里的散文不算，因为有 AST）。当前四个核均为 0。
3. **`TestCapabilityDecisionsStayCentralized`（当前上限 0）**：核目录内 `Capabilities()` 调用点必须为 0——seam 落地后也只允许集中在一个 resolver 里，抬高上限必须写明那个 helper 的名字。

**守卫本身也被测试**（另外 6 个用例）：字符串/注释里的提及、别名导入（`aux "os"`）、`os.Getenv`、`*_test.go` 均不得被计数；一个决策被复制到两处必须数出 2。守门人数错了比不设更糟。

**反向验证**：临时往 `mutatecore/atomic.go` 加一个 `os.Stat` 探针，检查确实变红（"28 calls, 1 above the recorded baseline of 27"）；回退后 `git diff` 为空且全绿。不经反向验证的守卫等于未验证。

辅助工具 `backend/scripts/drift_measure.go`（`//go:build ignore`）：`go run ./scripts/drift_measure.go` 打印当前计数与各调用点，用于**诚实地下调基线**，而不是改一个猜的数字。

这三条把 §4.1 的四条纪律从"口头承诺"变成 `go test` 事实，是**本方案不退化的唯一保障**，优先级高于任何功能交付。

---

## 8. 风险与缓解

| # | 风险 | 触发条件 | 缓解 | 残留 |
|---|---|---|---|---|
| R1 | **抽象本身成为第二套词汇**（有人往 `Access` 里塞 tier/权限） | 接口评审缺失 | §4.1 纪律 1 写进验收门；`Access` 参数不出现 tool name/tier 类型 | 低 |
| R2 | **核内长出一堆 `if cap.X` 分支**，比今天的两套实现更难读 | 降级判断散落 | §7.4 检查 3（能力判断单点）+ §4.3 矩阵作为唯一权威 | 中 |
| R3 | 搬家过程中**悄悄改语义**（尤其 `WriteAtomic` 的 rename 时序、`backup.go` 的落点） | 人手替换 os 调用 | 2.5a/3.5a 要求**现有单测一字不改通过**；改动只允许"搬家 + 传参" | 低 |
| R4 | envd 侧改造后**性能变差**（往返次数上升而字节并未减少） | 小文件走 `/files` Range 仍是一次 HTTP 往返；回落路径多一次 spawn | 核侧规则：**小文件一律整读**（阈值由 `Capability.MaxReadBytes` + 核预算定）；上线前实测“(往返数, 抓取字节数)”对比基线（§7.4 可扩展为第 4 检查） | 中 |
| R5 | `/files` 在**某些部署**不可用（未装令牌 / 前置反代剥 `X-Access-Token` 头 / 版本无该路由 / mTLS 环境） | 非本机部署差异（本轮只证了一台） | 探测 + **显式降级**（U6）：`RandomRead=false`、`MaxWriteBytes=1<<20`，走 `process` 通道；**绝不静默降级** | 低 |
| R6 | 真机活测不可复现/凭据缺失导致验收卡住 | 目标机变更 | 桩套件（fake envd）在 CI 必跑，真机 gated；沿用 `scripts/remotetools_e2e.go` 既有模式 | 中 |
| R7 | **与他人未提交改动冲突**（当前工作区有 35+ 个未提交文件，含 `chat_remote_*`） | 并行会话 | 本方案只碰 `toolcore/*`、`remotetools/envd_*`、`scripts/`；**不碰** `cmd/aicli/commands/*`、`gateway/console/*`、`docs/user-guide/aicli-remote-tools.md` | 低 |
| R8 | 过渡开关（2.5b 的双路径）忘记删除 → **第三套实现** | 3.5b 未收口 | 2.5b 明确"此开关在 3.5b 删除"，并加入 §7.4 检查 | 高（必须盯） |
| **R9** | **`/files` 以 root 身份读，造成 `view` 与 `shell` 的权限非对称（相对提权）** | 已实测触发（§3.5：`/etc/shadow` → 200） | **U7 闸门（必做）+ 纪律 4（策略不下沉）**；conformance 新增“jail 内越权文件”用例；日志只记方法/状态/长度 | 中（闸门上线即降为低） |

## 9. 成本汇总

| 段 | 成本 | 累计 | 兑现的价值 |
|---|---|---|---|
| Phase 2.5a | 2–3 天 | 3 | 结构就位，零行为变化 |
| Phase 2.5b | 4–6 天 | 9 | **只读面统一 + view/grep 真增量**（用户本次问题的落地） |
| Phase 3.5a | 3–4 天 | 13 | 写核可插 I/O |
| Phase 3.5b | 4–5 天 | 18 | **漂移终结，删除 ≥600 行 envd 自实现** |
| Phase 4.5 | 3–5 天 | 23 | 执行面统一、8 MiB 静默丢弃修正、可选 `/files` |
| §10 近项（N0b 安全闸门 + N0 `/files` 区间读 + N2 grep 流式） | 2–3 天 | — | 不等 seam 的即时收益，**且含一个安全项** |

对照：`tool-layer-abstraction-design-20261022.md` 成本表（行 2275–2276）Phase 2 与 Phase 3 各 3–5 天已完成。**seam 的总成本约等于把 Phase 2+3 再做一遍的 2 倍**，收益是把"远程适配"从人肉对齐变成结构保证——这与我们已经付出的两次漂移修复（`36caf76f`、`b65918eb`）相比，仍是正收益，因为那类修复会**持续发生**。

## 10. 不需等 seam 的近项（建议立刻做，且不会被 seam 浪费）

| # | 项 | 成本 | 落点 | seam 后归属 |
|---|---|---|---|---|
| ~~N0b~~ | ✅ **已完成**（`a8ec5aaf`）：可读性闸门——`test -r "$1"` 由目标内核回答，仅对非 world-readable 探测 | — | 核侧 `refuseIfTargetCannotRead`（`envd_backend.go`） | `Access` 前置闸门，**不下沉**（纪律 4） |
| ~~N0~~ | ✅ **已完成**（`a8ec5aaf`）：`view` 走 `GET /files` 单区间 `Range` | — | `envdclient/files.go` + `readLimited` | 可直接作为 `envdAccess.Read/RandomRead` 的实现体 |
| N1 | ~~`view` 远端窗口（`sed`/`dd` 造窗）~~ | — | — | **作废**：降为 R0 不可用时的回落，不单独立项 |
| ~~N2~~ | ✅ **已完成**（`29d6e334`）：`grep` 流式消费 + 上限早停 | — | `envd_backend.go` `runCapped` + `envdGrep` | `envdAccess` 的 `ExecStreamer` 形状 |
| ~~N4~~ | ✅ **已完成**（`d936b751`）：写侧 `POST /files` + 可写性闸门 + 逐字节落盘 | — | `envdclient.WriteFile` + `storeBytes` | 可直接作为 `envdAccess.Write` 的实现体 |
| ~~N3~~ | `/files` 签名调研 | — | — | **已完成**（本轮 C-1，结论见 §5.4） |

执行顺序：~~N0b → N0~~（**均已完成，见 §13**）→ **N2（`grep` 流式 + 早停，下一个）** → 写侧 `POST /files`（必须与 `missing_parent` 前置检查同批）。各项独立提交、独立回滚；即便后续走 seam，它们的**逻辑原封不动搬家**（这正是 seam 设计要检验的第一件事：如果近项代码搬不进 `Access`，说明接口设计错了）。本轮已验证：`readLimited`/`refuseIfTargetCannotRead` 就是 `envdAccess.Read` + 闸门的形状。

## 11. 未决问题（需要拍板，本文不替决策）

- ~~**U1 `/files` 签名可否本地计算？**~~ → **已关闭**（§5.4）：不需签名，header token 即可；无 jail，因此启用方式改为“核侧三道先行闸门”。
- **U6（已定 + 已落地 `a8ec5aaf`）`/files` 通道的启停与回落策略**：实现为“默认启用 + 探测一次缓存 + 显式降级带原因”，并额外交付了运维 kill switch `--no-envd-files` / `EnvDConfig.FilesChannelDisabled`（零值为启用，旧配置不会意外关闭）。降级不静默：失败原因进 `read_channel_note`。实际搬运字节的通道以 `metadata.read_channel=files|process` 公布，沿用 `envdGrep` 公布引擎的同一条纪律。
- **U7（已定）是否加“目标用户可读性闸门”**：**加**。`/files` 以 envd 进程身份（实测 root）读文件，与 `shell`/`process` 通道（以目标用户身份执行）存在**权限非对称 = 相对提权**；不闸门则 `view` 能看到 `shell` 看不到的文件（§3.5）。做法：`process.Start(["/usr/bin/test","-r",<已解析路径>])` 让**内核自己回答**，绝不在 Go 里重实现 POSIX 位/ACL/组判定；仅在 mode 显示可能拒时探测，并按目录缓存。
- **U2 是否接受 2.5b 的过渡双路径（一版）？** 不接受则 2.5b/3.5b 合并为一次大改（回滚粒度变粗，R8 风险消失但风险集中）。
- **U3 呈现层增量（progress notification 贯通到 manager/TUI）是否立项？** 与 envd 无关，独立评审；不立项则本文的"增量"只到计算/传输层为止。
- **U4 `grep` 参数面缺口（§8.9：`glob`/`type`/`-i`/多 patterns 的远端方言映射）是否并入 2.5b？** 同一段代码，建议并入，代价 +1–2 天。
- **U5 envd 形态下 `PathResolver` 是否也归入 seam？** 今天 `PathResolver.Resolve` 在本机做 `filepath`/symlink 判定，envd 侧靠 `be.resolvePath`（RPC）。本文按"归入 `Access.Resolve`"设计；若评审希望保持 `PathResolver` 独立，则 `Access` 去掉 `Resolve`，改为 `envdAccess` 自带 resolver——需决策，因为它决定 2.5a 的改动半径。

## 12. 建议的最小评审动作（本文通过后）

1. 在 `tool-layer-abstraction-design-20261022.md` 的 §16.1 风险 3 与 Phase 2/3 处加一行交叉引用（指向本文），把"远程后续通过接口适配"这句占位**兑现成接口定义**。
2. 在 `aicli-envd-bridge-merge-plan-20261028.md` §8.9 后追加一节，把"envd 文件面独立实现"标记为**过渡态，偿还计划见本文**。
3. ✅ **已完成**：§7.4 的三条反漂移检查已作为 `backend/internal/driftguard` 测试上线（随 `make test` / `go test ./...` 自动执行，无需额外 CI 步骤）。原写的是“先只跑报表、不作硬门”，实际落地为**硬门**：它们只会因新增违规而红，不会拦截现有代码（基线即现状），因此没有“先软后硬”的必要。
4. 在 `docs/architecture/envd-capabilities.md` 补一节 **`GET/POST /files`（HTTP，非 Connect-RPC）**，记录本轮实测：header token 即可、`Accept-Ranges`/206/416、无 ETag、`Last-Modified` 为请求时间、0.2.11 无 gzip、`POST` 的 `EnsureDirs` 自动建父目录、**无 jail 且以 envd 进程身份读**。它今天只列了 RPC 与 `/health`、`/metrics`，**遗漏了这个最重要的文件通道**。
5. 顺手更正 `docs/architecture/envd-source-and-rebuild.md:133` 的 clone 地址：本轮实测 `e2b-dev/infra` 与 `e2b-dev/runtime` 是**同一代码库的跳转**（API 响应里 `html_url` 指向 `e2b-dev/runtime`），两个都可用，文档建议写 `runtime` 并注一个重定向备注，避免后人怀疑找错仓。
6. **凭据卫生（建议立即做）**：本轮为跑 spike 使用了 sudo 口令与 envd 访问令牌，两者均未回显、未落盘、远端临时产物已清零，但口令已进入会话记录——建议**换发 envd 令牌**（改 `/etc/envd/envd.env` → `systemctl restart envd` → `/init` 重装，步骤见 `docs/aicli/envd-selfhost-deploy.md` §令牌引导）并更换 sudo 口令。

---

## 13. 落地记录（2026-10-28：`a8ec5aaf` + `552bf850` + `29d6e334`）

**交付**：`envdclient/files.go`（`/files` 通道 + 探测 + `FilesError` 分类）、`envdView` 改为“jail → Stat → 尺寸/目录门 → 可读性闸门 → readLimited（优先 `/files`，不可用则 `/bin/cat`）”、`--no-envd-files` kill switch、`scripts/envd_live_verify.go` 真机差分驱动。

**关键设计执行细节**（与本文原稿不同或原稿未预见）：

1. **N0b 与 N0 必须同批交付**。原计划把闸门排在通道之前，但单独先交闸门会留下一段**没有消费者的死代码**（今天 `cat` 已受内核约束，没有提权可堵）；而先交通道就是先上提权。所以两者一次提交，测试断言“被闸门拒的路径**从不出现在 `/files` 请求里**”。
2. **文件级失败不重试到另一条通道**：JSON 404 与 mux 404 必须**分开编码**（`FilesNotFound` vs `FilesChannelMissing`），否则“此文件不存在”会被当成“端点不可用”而去走 `cat`，把真实错误裹成意外结果。
3. **探测必须缓存**：一次后端实例一次，否则每次读都先交一个失败请求的费。
4. **测试用精确计数器而非总数**：jail 第二阶段本来就会 spawn 一次 `resolve`，用 `processCalls` 断言“没有起进程”会得出错误结论——这一点在开发中被测试当场抓到，改为 `catCalls` / `readableChecks` 两个具名计数器。

**真机差分（envd 0.2.11，二进制交叉编译到目标机原地跑，TLS 用真 CA 校验，令牌与证书未离开目标机，产物已清零）**：

```
PASS native-channel run: 13 tools advertised under prefix "remote_live"
PASS native read: /home/n9e/.ssh/authorized_keys (213 bytes)      # 0600 自有：探测放行 + /files 服务
PASS permission gate: permission denied reading /etc/shadow: user n9e cannot open it (-rw-r----- root:shadow; cannot open /etc/shadow)
PASS channel parity: 213 bytes identical through /files and through /bin/cat
ALL CHECKS PASSED
```

第三条把 `/etc` 临时加入 `--allowed-roots`，**目的是绕过 jail 单独测闸门**：证明“守护进程能读而目标用户不能读”的文件会被拒且从未取用。13 个工具与 §3.4 档位表一致（standard 档未因本改动变化）。

**第二轮：grep 流式与上限（`29d6e334` + `552bf850`）的同一驱动输出**：

```
PASS capped grep: engine reported and truncation disclosed (612 bytes returned under the 512 cap)
```

这一行本身带出一个只有真机才能发现的缺陷：首版只做到“累计 ≥ 上限就停止读取”，
而 envd 的输出来自**帧**，单帧可以远超上限，于是 cap=512 时实际返回了 **4210 字节**。
“被末帧超出的捕获上限”不是上限——修正为返回前把缓冲剪到 `capBytes`（stderr 同样收口）。
该防线经过**反向验证**：临时撤掉剪枝那行，测试确实变红（573 > 512）；不依赖时序的
socket 早停仅作日志证据，不作断言。

**曾发现的一处配置缺陷（历史记录，已由 `552bf850` 修）**：`--max-bytes-per-call N` 对读工具**静默无效**。`main.go:286` 只写 `Limits.MaxBytesPerCall`，而 `DefaultConfig`（`config.go:300`）已将 `CaptureLimit` 填为非零，于是 `config.go:450` 的 `if CaptureLimit <= 0` 永不触发，而 `envdBackend.viewLimit()` 优先读 `CaptureLimit`。证据：本提交的尺寸门测试必须显式设 `CaptureLimit` 才生效（已在测试注释里记下这个坑）。建议修法：显式传入该 flag 时同时覆盖 `CaptureLimit`（或在 normalize 里区分“用户显式设置”与“默认值”）——因为它改的是一个已文档化 flag 的含义，且不局限于 envd 分支，所以没放进本提交。该缺陷影响 ssh/http 后端的 `Limits` 解读，**不属于 seam 范围，但会误导任何调尺寸的人**。

**与描述不符的一处实现选择**：N2 原写“达上限 `SendSignal`”，实现用的是**取消 ctx + 关闭流**。原因：`process.go` 对 `Start` 的文档就是“cancel ctx 是受支持的停止方式，envd 在流客户端消失时回收子进程”——这比“再发一个 unary RPC”少一次往返，不会变成“我们已经读够了，还要另发一道命令告诉对面停下”。`SendSignal` 仍然可用于未来需要区分“用户主动结束”与“读够了”的场景。

**上一轮记录的配置缺陷：已修（`552bf850`）**。修法不是“flag 到来时联动覆盖 `CaptureLimit`”，而是把 `DefaultConfig` 里预填的 `CaptureLimit` 改回 0，让 `Normalize` 里本来就写好的“未设则从 `MaxBytesPerCall` 派生”分支能够触发——因为错的是“未设”被默认值伪装成“已设”，而不是优先级本身。所有读取点本来就有 `if CaptureLimit > 0` 的兑底，所以未调 `Normalize` 的构造路径行为不变。

⚠️ **这是一个对外可见的行为变更**：过去把 `limits.maxBytesPerCall` 设得比默认值小的部署（包括 aicli 从 target `Limits` 块展开出的 `--max-bytes-per-call`），读工具**现在真的会按那个值收紧**。这正是该设置本来的含义，但发版说明应当点一句。

**过程纪律的一条（我自己违反了两次）**：`gofmt -w <目录>` 会把**未修改的既有文件**一起重排——本仓库里 `audit.go`/`metrics.go`/`platform.go`/`roots.go`/`server_test.go`/`ssh_e2e_test.go` 在 HEAD 中就不是 gofmt 规范形态，一次目录级格式化波及 7 个文件，被 `git checkout` 回退了**两次**（v3 轮一次、`d936b751` 轮再一次）。规则改为：**只对本轮实际改动的具体文件执行 gofmt**，绝不目录级；判断办法是提交前 `git status --short` 必须只剩自己动过的文件。写在这里是因为它会以"顺手清理"的名义反复发生，而不像错误。

**§8.9 缺口的当前状态**（三项）：

1. **envd `view` 的 `offset`/`limit` 行窗口**：**仍未完成**。`/files` 区间读已可用后，这件事从“需要 `sed` 对拍九类语义”降为“在核里用字节窗口映射行号”。
2. **写侧走 `/files` 并与 `missing_parent` 同批**：✅ 已完成（`d936b751`）——父目录与闸门都在进通道**之前**，2,450,000 字节逐字节落盘实测。
3. **`edit`/`apply_patch` 的读迁入守卫入口**：✅ 已完成（`c0226f10`）——三者共用 `readGuarded`，闸门不再靠每个调用点自觉。

**本轮新增的一条结构教训（比上一个改动更重要）**：闸门必须装在**唯一入口**里，不是每个调用点。
上一轮把可读性闸门装在 `envdView` 内，`edit`/`apply_patch` 读同一个文件就走另一条路——
当时**并没有泄漏**（它们走 `cat`，内核把关），但“漏装闸门”的概率等于“有人记得在第三个
调用点再写一遍”。`readGuarded` 把闸、通道选择、字节上限归一处后，这类遗漏不再取决于记忆；
测试以“两个工具各自必须被拒且**文件内容不得改变**”钉住，并用**绕开闸门的合法变异**证明
两条子例各自承担重量（绕开后 `edit` 与 `apply_patch` 真的读到了 0600 root 的文件，而 `view`
的测试仍绿）。

**第二条结构先例（`4302b807`，供 §4.3 直接引用）**：拒绝应由**能力**判定，不应由后端身份判定。
`binary_payload` 从"envd 拒绝 NUL"变成了"当前唯一可用通道是 argv，而 argv 放不下 NUL"——
同一份代码里不再存在"因为这是 envd 所以拒"这类分支，只存在"因为没有该能力所以降级或拒绝"。
这正是 §4.1 纪律 1（能力不表达策略）与 §4.3 降级矩阵要的形状，也是它第一次在真实代码里兑现。
配套要求：拒绝文案必须**归因到缺失的能力**（含探测到的原因），否则使用者会把它读成工具的规矩。
同轮记下一条**不属于本方案能力范围的边界**：MCP/JSON 字符串传输无法携带非 UTF-8 字节
（Go 的 `encoding/json` 替换非法 UTF-8），local 与 envd 完全相同，因此"写任意二进制"仍只能
走 `shell`/下载路径——把它写成"已支持"会是虚假的完工声明。

**剩余项（按价值/风险排序）：**

1. ~~NUL/UTF-16 写拒绝可以取消了~~ → **已完成（`4302b807`）**：拒绝现在由**通道能力**决定（`refuseUncarriableWrite`），`edit` 的 UTF-16 编码保持往返已恢复。**同时记下一条我们修不了、也不假装能修的边界**：真机首次跳测报 `0xFF` 变成了 U+FFFD——**MCP/JSON 字符串传输本身无法携带非 UTF-8 字节**（Go 的 `encoding/json` 会替换非法 UTF-8），**local 后端完全相同**。所以本次提升覆盖 NUL（JSON 能表达）与编码保持往返，**不覆盖“写任意二进制”**；想写二进制仍只能走 `shell`/下载路径。
2. **`apply_patch` 多文件时的闸门合并**：当前每个"祖先缺失"的文件各一次 `test -w`；一个 patch 内可以一次探测多个路径。待真实成本数据再决定，**不先优化**。
3. **`view` 行窗口**（§8.9 唯一未关项，见上）。

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

# 3.3.1 C-1 spike（只读；令牌建议只在目标机原地取用，避免外流）
T='<envd 访问令牌>'                       # 或：sudo cat /etc/envd/envd.env | sed -nE 's/^ENVD_ACCESS_TOKEN=//p'
B="https://127.0.0.1:49983/files"; F=/home/n9e/.bashrc; U=n9e
curl -sk -D - -H "X-Access-Token: $T" "$B?path=$F&username=$U" -o /dev/null   # 期望 200 + accept-ranges: bytes
curl -sk -D - -H "X-Access-Token: $T" -H 'Range: bytes=0-9' "$B?path=$F&username=$U" | head -4   # 期望 206 + content-range: bytes 0-9/3814
#   区间精确性对拍：把 body 与 `head -c 10 $F` 做 cmp -s（本轮已 EXACT-MATCH）
#   越尾：-H 'Range: bytes=99999-100100' → 期望 416 + content-range: bytes */N
#   多区间：-H 'Range: bytes=0-9,20-29' → 期望 multipart/byteranges（客户端只用单区间）
#   条件读陷阱：-H 'If-Modified-Since: <未来时间>' 得 304，但连发两次对比 last-modified 会看到它是请求时间
#   大文件成本：10MiB 文件取 1KiB 区间，核对 size_download == 1024
#   安全非对称：GET path=/etc/shadow（200）与同用户本地 `head -c 1 /etc/shadow`（Permission denied）对比
#   写/编辑/补丁：真机一次跑完 9 项，用 backend/scripts/envd_live_verify.go
#     （-write-path / -edit-path / -forbidden-path；每项均以目标用户身份直接读盘复核，
#      闸门用例另由 root 确认文件内容未被改动）

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
