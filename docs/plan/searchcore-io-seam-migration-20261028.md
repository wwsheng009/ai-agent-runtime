# searchcore 接入 I/O seam 的施工方案（含三个必须先定的设计问题）

日期：2026-10-28　关联：`tool-layer-io-seam-design-20261028.md`（§4.2 seam、§6 Phase 2.5b 第 2 项）、`envd-view-line-window-design-20261028.md`（M2 行窗口）

---

## 0. 结论先行

1. **不需要签名级联**（这是好消息，也是本方案能小步走的原因）：`Grep(ctx, opts GrepOptions)` 与 `GlobWithOptions(ctx, opts GlobOptions)` 已经是"选项结构体 + ctx"入口，按 `viewcore.Options.Access` 的同形加一个字段即可贯穿，**导出签名一字不改**。
2. 但**必须先回答三个设计问题**（§2），否则会把三类语义"搬"成静默错误：symlink 解析、文件身份（inode）、全局 gitignore 的"谁的 HOME"。这三处都不是 `os.Stat` 那种机械替换，**盲搬等于把 local 的假设硬塞给远端**。
3. 因此分五步（§4），每步只封一个**完整关注点**（不出现"同一关注点两种真值来源"），每步带实测棘轮差量与回滚点。
4. 终局收益：`handler.go:180` 那个 `call.Runtime.(Searcher)` 拦截（远端自带 grep/glob 实现）是**本 seam 要消灭的第二套实现**；S6 才是把它退役，§3 说明为什么不能提前。

---

## 1. 实测清单（2026-10-28，生产代码，`rg` 按函数归属）

守卫计数口径：`FilesystemCalls` 只匹配 `os` 选择器；`WalkerStyleCalls` 匹配 `filepath.WalkDir/Walk/Glob/EvalSymlinks` 与 `os.DirFS`。当前棘轮：`searchcore` os.* **24**、walker **4**。

| 文件 | 函数 | 现场 | 谁计数 |
|---|---|---|---|
| grep.go | `Grep` | `os.Stat(roots[0])`（文件还是目录）、`os.Stat(path)`、`os.Open(path)`（模式文件）、`os.Stat(root)` | os.* ×4 |
| grep.go | `grepWalkRoot` | `filepath.WalkDir(root, onEntry)` | walker ×1 |
| grep.go | `walkGrepFollowing` | `os.Stat(root)`、`filepath.EvalSymlinks(root)`、`os.ReadDir(frame.dir)`、`os.Stat(path)`、`filepath.EvalSymlinks(path)` | os.* ×3 + walker ×2 |
| grep.go | `readGrepPatternFile` | `os.Open(path)` | os.* ×1 |
| glob.go | `GlobWithOptions` | `os.Stat(base)` | os.* ×1 |
| glob.go | `walkGlobMatches` | `filepath.WalkDir(base, …)` | walker ×1 |
| glob.go | `sortGlobMatchesByModTime` | `os.Stat(match)`（排序要 mtime） | os.* ×1 |
| grep_ignore.go | `ValidateGrepIgnoreFiles` | `os.Stat(file)`、`os.ReadFile(file)` | os.* ×2 |
| grep_ignore.go | `newGrepIgnoreRules` | `os.Stat(root)`、`os.Stat(absPath)` | os.* ×2 |
| grep_ignore.go | `loadGrepIgnoreFileWithCase` | `os.ReadFile(filePath)` | os.* ×1 |
| grep_ignore.go | `globalIgnoreRulesWithCase` | `os.Getenv("HOME")`、`os.Getenv("USERPROFILE")`、`os.UserHomeDir()` | **两个守卫都不数** |
| ripgrep_engine.go | `ripgrepSearchArgs` / `ripgrepGrepRoots` | `os.Stat(base)`、`os.Stat(roots[0])` | os.* ×2 |
| grep_binary.go | `grepFileIsBinary` | `os.Open(path)`（首块嗅探） | os.* ×1 |
| grep_context.go | `expandGrepContextFile` | `os.Open(file)`（重读上下文） | os.* ×1 |
| grep_sort.go | `sortGrepCandidates` | `os.Stat(candidate.path)`（mtime 排序） | os.* ×1 |
| handler.go | `NewGlobHandler` / `grepIgnoreFilesFromArgs` / `grepPatternFilesFromArgs` | `os.Stat(base)` + `os.IsNotExist` + 两处 `os.Stat(anchor)` | os.* ×4（**这里 `call.Runtime` 在手，最容易先做**） |
| filesystem_unix.go | `filesystemIdentity` | `os.Stat(path)` 后取 `Sys().(*syscall.Stat_t)` 的 dev+ino | os.* ×1 + **平台专属** |

一处**新盲点**（本方案顺带查出，与 ls 那次同类）：`globalIgnoreRulesWithCase` 用环境变量/`os.UserHomeDir()` 决定去**哪台机器的哪个家目录**读全局 ignore 文件，两个守卫都看不见它。它是远端化时最容易"看起来没事"的一处。

---

## 2. 必须先定的三个设计问题

### Q1 `filepath.EvalSymlinks` 放哪儿？
`walkGrepFollowing` 靠它决定"这个软链指向的真实目录是否仍在授权根内"。这不是一次数据读取，而是**路径身份**问题——而 seam 方案 §4.2 明确把 `Resolve/Relative` **留在** `Paths()/Permissions()` 这条唯一入口，不再造第二权威。

结论：**不进 `Access`**。它是 `follow=true` 这条可选策略的一部分，正确形态是给 `GrepOptions` 一个能力门（远端已有 `aicli-envd-resolve` 这条 RPC，`ProbeCapabilities` 可测）。若在 seam 里再造 `EvalSymlinks`，同一个"这路径真的是哪儿"的问题就会有两套答案，而这正是 envd 分叉当初的病根。

### Q2 文件身份（inode）
`filesystemIdentity` 在 unix 上用 `dev+ino` 做"同一文件去重"。envd 的 `Entry` **没有 inode**，SSH 后端也没有。硬搬的后果是远端把两个不同文件当成同一个（或反之），而去重结果直接影响 grep 命中列表。

结论：抽成**可选接口**（`IdentityResolver`，与 `RandomAccessor` 同族），**不实现即降级为不去重**，并在结果里说明降级——沿用 §4.2"不实现即降级"的既有模式。绝不为了"看起来一致"而用 `path+size+mtime` 伪造一个 inode 等价物：那是把弱判断伪装成强判断。

### Q3 全局 gitignore 读"谁的 HOME"
local 读当前用户家目录；envd 上守护进程的家目录**不是请求者的**，读它会应用用户从未设置过的 ignore 规则——**静默少结果**，比拒绝更坏。

结论：远端**默认禁用**全局 ignore（能力位 `GlobalIgnore=false`，实测/配置得来，不按版本猜），并让调用方可以显式提供规则文件路径。同时把 `os.Getenv`/`UserHomeDir` 这一类"决定去哪台机器"的现场**纳入守卫可见范围**（否则第 4 步做完，`searchcore` 会显示 0 而实际上还留着一个远端会出错的假设）。

---

## 3. 贯穿方式：零导出签名改动

```go
// GrepOptions / GlobOptions 各加一个字段（nil = 本地文件系统，与 viewcore 同形）
Access toolcore.Access
```

- 入口 `Grep`/`GlobWithOptions`/`Glob`：解析一次 `access := opts.Access; if access == nil { access = localio.Access }`，作为**参数**传给包内 helper（`walkGrepFollowing`、`readGrepPatternFile`、`grepFileIsBinary`、`expandGrepContextFile`、`sortGrepCandidates`、`sortGlobMatchesByModTime`、ignore 三函数）。包内改动，不外溢。
- `handler.go` 的 4 处直接用 `localio.For(call.Runtime)`，与 viewcore/lscore 完全同形（**这是 S1，最小、最安全、可立刻验证**）。
- 注意保留两条已验证过的教训：**①** 遍历顺序即截断语义（ls 那次 limit 检查顺序的坑），**②** 判"是否目录"用于决定下降时必须用 `Lstat`，不能用跟随链接的 `Stat`（ls 符号链接逃逸那次）。`walkGrepFollowing`/`grepWalkRoot` 两处都涉及下降决策，属同一风险面。

`Searcher` 拦截（`handler.go:180`、`remoteRuntime.Grep/Glob`、`rg_search.go`）在 S1–S5 期间**必须保留**：它是远端现在唯一的 grep/glob 通路。S6 才在 envd/ssh 拿到可用的 `Access` 后退役它——提前退役就是把工具面砍了（违反"工具面 6/13/19 与档位表在任何一段都不得变化"）。

---

## 4. 分步（每步一个完整关注点）

| 步 | 封住的关注点 | 触及 | 棘轮预期（**每步实施前重新实测，不预先填数**） | 回滚 |
|---|---|---|---|---|
| S1 ✅ | handler 层的搜索根存在性 + anchor 判定（**实测 os.* 24→21**；4 个调用点里有 1 个是 `os.IsNotExist`，不计入守卫） | handler.go | 21（本表原先写"24→20"是把 IsNotExist 也数了进去，实测纠正） | 删 3 行注入 |
| **S2a ✅** | **候选文件的字节与关于该字节的判断**：内容读取、NUL 二进制嗅探、regular/尺寸门 | grep.go、grep_binary.go | **实测 21→18**（三处） | 单次 revert |
| S2b | 模式文件读取（`readGrepPatternFile`，需先决定 `GrepPatternsWithFiles` 是否加 ctx） | grep.go | 实测填写 | 同上 |
| S2c | 上下文重读（`expandGrepContextFile`，`ExpandGrepContext` 是导出函数，需扩参数或改由调用方供句柄） | grep_context.go | 实测填写 | 同上 |
| S3 | 排序所需的 mtime（glob 与 grep 两处） | glob.go、grep_sort.go | os.* 17→15 | 同上 |
| S4 | ignore 文件读取（含 Q3 决定 + 守卫可见性补齐） | grep_ignore.go | os.* 15→11；**新增一项守卫**覆盖 HOME 类现场 | 同上 |
| S5 | 遍历器本体：`grepWalkRoot`/`walkGrepFollowing`/`walkGlobMatches` + Q1/Q2 | grep.go、glob.go | os.* 11→? 、walker 4→? （Q1/Q2 未定则为剩余量并写明原因） | 单文件回滚 |
| S6 | 退役 `Searcher` 拦截，让远端 grep/glob 走共享核 | remotetools + handler | 目标是**删代码**（与 bridge 那轮 −3175 行同类收益） | 整步独立可撤 |

每步验收门（与既有纪律逐条对齐）：
1. 既有 `searchcore` 单测**一字不改**通过；
2. 棘轮主动报降级并要求锁死，**同笔**下调并写明理由；
3. 遍历类改动必须有**差分对拍**（以改造前的 `filepath.WalkDir` 实现为 oracle，跑遍 limit×depth 组合），并把任何故意分歧用 `diverges` 显式钉住——ls 那一步证明这是唯一能抓住顺序 bug 的手段；
4. 反向验证：摘掉新守卫/判据，对应测试必须**红**（且必须是测试红，不能是编译失败）；
5. 工具面与档位表零变化；不新造错误码；`gofmt` 只作用本轮具体文件；
6. 真机：涉及远端通路的步骤（S4/S5/S6）跑 envd 三套（现 16 + 8 + 8 项）。

---

## 5. 风险（按已发生的先例写，不写抽象担忧）

| 风险 | 先例 | 对策 |
|---|---|---|
| 遍历顺序变化改变截断存活行 | ls 的 `limit` 检查顺序（差分当场抓到） | S5 强制差分 + 顺序写在注释里作为契约 |
| 用 `Stat` 判"是否下降"导致越出授权根 | ls 的符号链接根逃逸（差点被当搬家细节混过去） | 下降判定一律 `Lstat`；用例含"符号链接根/树内链接/悬空链接"三类（真机才有） |
| 把 local 假设硬塞给远端（HOME、inode） | 本轮 Q2/Q3 | 能力门 + 降级明示，禁止伪造等价物 |
| 半封关注点造成两种真值来源 | 台账里"读闸门唯一入口"那条 | 分步边界=关注点边界（§4 每行都是一个完整关注点） |
| 上游半提交使 HEAD 不可构建，验证要靠临时借文件 | M2 现状：`8f325893` 漏交 `wire` 字段、`4c2f0f56` 漏交 `importSessionResult` 字段 | 见 seam 文档 §13.6 合并清单；S1 若落在 remotetools 之外的包，构建不受此阻塞影响（`searchcore` 不依赖 `remotetools`），**可先行** |

最后一行值得注意：**S1–S5 全在 `internal/toolcore/searchcore`，不依赖那个坏包**，所以这一步不被上游阻塞；阻塞的只有 M2 的合并与 S6。
