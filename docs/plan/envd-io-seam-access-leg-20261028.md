# envd 执行腿设计笔记：`toolcore.Access` 的远端实现能做到什么（2026-10-28）

## 0. 为什么现在写这份

S5c 之后 `searchcore` 的直接宿主读归零（`os.*` 24→0、walker 4→0）。但全仓扫实现签名得到的
事实是：**生产代码里唯一实现 `toolcore.Access` 的包只有 `localio`**，其余全是测试 fake
（`rg` 命中：localio + 各包的 `*_test.go`）。

也就是说这条 seam 目前只是"核心不再自作主张"的**可能性**，远端还没有人应答。S6"退役
`Searcher` 拦截、让远端 grep/glob 走共享核"的真正前置不是删代码，而是**先造出 envd 这条
执行腿**。本笔记把"造得出什么、造不出什么、造不出的部分会撞到哪条既定约束"用实读的代码
钉下来，供决策；不含任何实现。

## 1. envd 现有能力对四个 `Access` 方法的映射（逐条来自源码）

| `Access` 方法 | envd 侧依据 | 能否诚实应答 | 代价 |
|---|---|---|---|
| `Lstat` | `Client.Stat` 返回的 `Entry.Type` 可为 `FILE_TYPE_SYMLINK`，并带 `SymlinkTarget`（`envdclient/filesystem.go:33-44,109`） | **能**，几乎是直译 | 1 次 RPC |
| `Stat`（跟随） | 同上，但 envd 的 `Stat` 对链接报的是链接本身 | **能**，但要先解析（见 §2 的 resolve 机制） | 非链接 1 次；链接 +1 |
| `ReadDir` | `Client.ListDir(path, depth)`，`depth` 置 1 即单层（`filesystem.go:124-150`） | **能** | 1 次 RPC |
| `Open`（可 Seek 的流） | `Client.ReadRange(ctx, path, off, size)` 返回 `Data/Total/StatusCode`，`ProbeCapabilities` 已能证明区间读被真实遵守（`rangefile.go:34,114-141`） | **能**，且 M2 的行窗口就是建在这上面 | 按窗口多次 |

`ListDir` 的自带注释值得抄进实现体的理由：**"结果不排序、不过滤，dotfile 照给"**。这正是
S5a 在 `walkViaAccess` 里显式 `sort.Slice` 的原因——排序在被测代码手里，不是继承宿主的礼貌
（§4b 第 12 条）。远端一旦接上，这条断言从"测试里的构造"变成"真实协议"。

## 2. 两个可选能力：一个已有机制可复用，一个真的做不到

### `SymlinkResolver`（follow 模式的环路终止）——**已有先例**

`remotetools/envd_backend.go:305-318` 的 `canonicalize` 已经在目标上解析绝对路径：跑
`/bin/sh -c <envdResolveScript> aicli-envd-resolve <target>`，依赖目标的 `realpath`/`readlink`，
并且**失败即关闭**（非零退出、空输出、相对结果都算错误，不回落到词法路径）。

所以 `EvalLinkPath` 不需要新协议，照这个既有、已测的机制应答即可。调用频次也可控：只有
`follow=true` 且**真的遇到指向目录的链接**时才问（S5b 定的最小必要时机）。

### `FilesystemIdentifier`（`one_file_system` 的边界）——**做不到**

`Entry` 的字段是 `Name/Type/Path/Size/Mode/Permissions/Owner/Group/ModifiedTime/SymlinkTarget`，
**没有 `dev`，也没有 inode**（`filesystem.go:33-44`）。文件通道给不出文件系统身份。

理论上还能走进程通道补（`Client.Run` 已在能力探测里被用来跑 `/bin/sh -c`，见 `rangefile.go:143-160`），
但那是**每个新目录一次进程启动**，而 `one_file_system` 恰好按目录询问——用一条为探测而生的
慢通道去做遍历期的常规判断，代价不成比例。

## 3. 这一处会撞到既定约束，且必须先讲清"今天到底对不对"

约束写的是"工具面与档位表在任何一段都不得变化"。于是必须先弄清今天远端 `one_file_system`
的真实行为：

- 目标机上**装了 rg**：参数被转发给目标机 rg，边界由 rg 在自己的进程里判（
  `grep_mode_conformance_test.go:242-250` 明写"注入的 seam 到不了独立进程"；
  `TestRemoteGrepOneFileSystemFlagReachesRipgrep` 钉的是 argv）。**今天是对的。**
- 目标机上**没有 rg**：只能回落纯 Go 遍历器。而 S5 之前，那套遍历器和它问的每一个文件问题
  都在**本机**——也就是说这一条路径今天给出的不是"少一个能力"，而是**错答案**（在本机挑文件、
  按本机的文件系统边界划界）。

因此接入执行腿后的差别要这样陈述才诚实：装了 rg 的场景不变；没装 rg 的场景，`one_file_system`
从"静默给错结果"变成"明确拒绝并给出路（去掉该标志）"。**这是把错答案换成拒绝，不是把好能力换成
没有**。但两者都仍是"表面看起来能用"与"实际能用"的差别，需要显式决定，不能由我默认掉。

## 4. 待决（按我的推荐排序）

先修正一条代价估计：**envd daemon 不在本仓**。`docs/architecture/envd-source-and-rebuild.md` 的
证据是沙箱内 `/usr/bin/envd` 的 Go buildinfo，且它是 `e2b-dev` 那份 fsnotify/envd 的 **fork**，在
仓外以 `go build ./cmd/envd` 重建。所以"加个字段"不是本仓的一次提交，而是**改 fork + 重新构建 +
重新部署到每台目标机 + 客户端与 wire 同步**，还要等新版本铺开才生效。据此重排：

1. **B｜保留"rg 优先"的现状，只让无 rg 的目标走共享核并明确拒绝 `one_file_system`（推荐先做）**：
   零新增协议、零跨仓依赖，语义变化被限制在"本来就是错答案的那条路径"（见 §3）。
   配套要求：远端能力从此取决于目标机是否装 rg，这条事实必须在 doctor / 能力探测里可见——
   `ProbeCapabilities`（`rangefile.go:114`）已经是干这个的地方，不要另造一个探测。
2. **A｜给 envd 的 `Entry` 加 `dev`（正确的终局，但跨仓）**：这是唯一让远端 `one_file_system` 在
   "无 rg 目标"上**真的能用**、且不新增第二条策略道路的做法。前置条件是先解决
   `wire.MountLabels` 那类"字段停在他人未提交工作区"的问题（`upstream-broken-commits` 案例 1），
   否则连干净检出都编不过，跨仓改动更无法自证。建议顺序：B 先落地并拿到真机数据，再判断是否
   值得为 A 付一次 fork 版本升级。
3. **C｜用进程通道补 `FilesystemIdentifier`**（每目录一次 spawn + 缓存）：**不推荐**。
   为了一个标志把遍历成本乘上目录数，且引入"探测通道"与"文件通道"两套真相。
4. **D｜接受远端不支持 `one_file_system`**：只有在 A/B 都被否掉时才谈，且必须写进用户文档
   与档位说明，不能靠运行时报错让员工自己发现。

## 5. 下一步（不含争议部分）

`Access` 的四个方法里，`Lstat / ReadDir / Open(ReadRange) / Stat(解析后)` 与
`SymlinkResolver(复用 canonicalize)` 都能**自证、不改工具面、不碰他人热区**地先做出来：
新文件 + 以假 transport 为对象的单测，**先不接线**（接线才需要动 `remotetools` 的 runtime，
而那里 `registry.go`/`tools.go` 他人正在提交）。这样 §4 的决策可以在一个"已经能跑、
只是没接上"的执行腿上面做，而不是在纸面上猜。

已按此落地：`remotetools/envd_access.go`（含 9 条契约测试与 9/9 反向验证）。落地时比本节
多走了一步——链接解析**没有**复用 `canonicalize`，因为读实现发现 envd 只把末段报成
`FILE_TYPE_SYMLINK`、中间组件由服务端自己跟随，于是逐跳 `SymlinkTarget` 就够，不必开进程；
代价是这条假设至今**只有假目标证明过**。

关掉它的工具已经写好（只读、不写目标机、任一假设不成立即非零退出）：

```
$env:AICLI_ENVD_TOKEN = '<access token>'
go run ./scripts/envd_seam_access_verify.go -addr https://<host>:49983 `
   -user <user> -workspace /home/<user> -ca-file <tls.crt>
```

它逐条打印 PASS/FAIL/SKIP：workspace 是否还像目录（`Mode().IsRegular()` 必须为假）、清单
自一致性、`Lstat` 保住末段链接而 `Stat` 跟随、`EvalLinkPath` 给出绝对存在的目标、穿过可能被
链接的中间目录能否 Stat、seam 读到的字节与 `/files` 原生整读是否逐字节相同且回绕后一致、缺失
归为不存在、（可选）权限拒绝不被说成不存在、以及适配器和 `FilesystemIdentifier` 的**不**实现。
跑完这条就是接线前的最后一道门。
