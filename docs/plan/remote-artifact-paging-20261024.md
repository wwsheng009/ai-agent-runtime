# 远程输出裁剪与 artifact_read 的分页契约

## 远程确实有输出裁剪，而且是三层

| 层 | 位置 | 作用 |
|---|---|---|
| 抓取上限 | `Limits.MaxBytesPerCall` / `CaptureLimit` | 单次调用的**内存**闸门（不决定模型看到多少） |
| 工具预算 | `toolcore/output.ToolBudget(tool)` | 每个工具声明的模型可见窗口（view 32K / grep 32K / glob 16K / ls 16K / shell 32K / artifact_read 32K） |
| 远程硬顶 | `Limits.ModelVisibleCap`（默认 64K，硬上限 `BudgetCeiling`） | 信任边界，只能**收窄**不能放宽 |

`ResolveCap` 的算法：

```
hardCap = min(toolWindow, remoteHardCap)
cap     = declared > 0 ? min(declared, hardCap) : hardCap
```

shell 走的是特化的 `ShapeShellWindow`：head+tail 折叠，**保尾部**（测试结论、
最终报错都在尾部），中间的省略量、字节区间、页数全部写进 metadata，
并提示用 `artifact_read` 按 `offset`/`limit` 取回中段。

## 发现的缺陷：artifact_read 被二次折叠

`remoteOutput.Shape` 对所有 `KindText` 结果无条件调用 `ShapeText`，
**包括 `artifact_read`**。而 `artifact_read` 本身就是分页工具。

实测（`ModelVisibleCap=1024`）：

```
requested=4096    returned=1024   <-- FOLDED BY SHAPER
requested=32768   returned=1024   <-- FOLDED BY SHAPER
requested=61440   returned=1024   <-- FOLDED BY SHAPER
requested=65536   returned=1024   <-- FOLDED BY SHAPER
```

一个请求都拿不到请求的字节数。连带三个后果：

1. **请求与响应不一致**：要 4 KB 回 1 KB；
2. **内容丢失**：head+tail 折叠丢弃页面中段——正是调用方要读的字节；
3. **续读指针失真**：`suggested_next_offset` 按 `len(返回)` 计算，
   而正文已被折叠，指针描述的窗口与实际内容不再对应。

换句话说：**一个分页工具被折叠，等于它自己把自己要读的数据吃掉。**

## 修复

三处，缺一不可：

### 1. 分页工具不参与折叠（`remotetools/runtime.go`）

新增 `toolcoreoutput.IsSelfSizedWindowTool(tool)`，`artifact_read` 命中时
只标记 `skip_render_truncation` 与 `model_visible_budget_bytes`，直接返回原文。

### 2. 页大小由工具自己定（`toolcore/artifact_handler.go`）

原来默认 `64*1024`，比工具自身 32 KiB 预算还大——请求上限和声明上限自相矛盾。现在：

- 默认 = 自身窗口 − 头部预留（512 B）；
- 调用方传更大的 `limit` 会被**收敛**到该上限，而不是被折叠；
- 下限 1024 B，避免极端配置下返回空页。

### 3. 发布自己的续读契约

新增 `artifact_window_bytes` / `artifact_window_limit` / `artifact_eof` /
`artifact_next_offset`，与本地 `artifact_read` 的契约对齐；同时保留原有的
`eof` / `is_truncated` / `suggested_next_offset` 以免破坏既有调用方。

### 一个 import cycle 的处理

`toolcore/output` 导入 `toolcore`，所以 `toolcore` 不能反向导入 `output`。
预算常量与 metadata key 因此定义在 `artifact_handler.go` 内，
由 `output.ToolBudget("artifact_read")` 保持同值（注释里写明这层关系）。

## 修复后实测

`ModelVisibleCap=8192`（窗口 8 KiB）：

```
requested=4096    returned=4096     ← 完全遵守
requested=32768   returned=7680     ← 收敛到窗口 8192-512
requested=61440   returned=7680     ← 同上，不折叠
requested=65536   returned=7680     ← 同上，不折叠
```

**整份抓取逐页读回校验**：抓取 108894 字节，按 4096 逐页读回
**恰好 108894 字节**——无缺口、无重复。

## 固化为回归测试（`remotetools/artifact_page_test.go`）

- `TestRemoteArtifactReadPageIsNotFolded` —— 要 4096 必须回 4096，且正文不能出现折叠标记。
- `TestRemoteArtifactReadHonoursItsOwnWindow` —— 超限请求收敛到窗口，而不是折叠。
- `TestRemoteArtifactReadPagesCoverEveryByte` —— 逐页读完整份抓取，字节数必须相等。
- `TestRemoteShellFoldedOutputPointsAtArtifact` —— 折叠**只有**在省略中段可取回时才允许。

最后一条是前三者的前提：如果 shell 折叠后不再给 artifact 指针，折叠就是静默丢数据。

## artifact_read 是否在默认 MCP 列表里

**不在**。它由 `Artifacts.Root` 控制，与 profile 无关：

```go
// tools.go, enabledTools()
if c.Artifacts.Root != "" {
    base = append(base, artifactTools...)
}
```

实测各场景（`remote_dev__artifact_read` 是否出现）：

| 场景 | 出现 |
|---|---|
| 默认配置（未设 artifact root） | ❌ |
| 设置 `Artifacts.Root` | ✅ |
| readonly profile + `Artifacts.Root` | ✅ |
| `ReadOnly=true` + `Artifacts.Root` | ✅ |
| custom profile，`Tools=[grep]` + `Artifacts.Root` | ❌ |
| custom profile，`Tools=[grep, artifact_read]`，**无** store | ✅（但调用必失败） |

三个要点：

1. **门槛是 store，不是 profile。** 没有 artifact store 时列出这个工具，
   每次调用都会返回「artifact store is not configured」——声明了一个必然失败的能力。
2. **read-only 不会砍掉它。** 解引用 artifact 是读操作，只读目标必须保留，
   否则 agent 生成的每个折叠输出都无法取回。`ReadOnly` 的硬过滤白名单里
   明确包含 `ToolArtifactRead`。
3. **custom profile 是唯一例外**：它以 `Tools` 为完整清单，所以显式写
   `artifact_read` 会被列出，即使没配 store。这是调用方明确的越权选择，
   失败信息是可行动的 `UNSUPPORTED`（不是 panic、不是空读取）。

## 固化为（`remotetools/artifact_listing_test.go`）

- `TestRemoteArtifactReadIsNotInTheDefaultSurface`
- `TestRemoteArtifactReadAppearsWhenStoreIsConfigured`
- `TestRemoteArtifactReadSurvivesReadOnlyGating`
- `TestRemoteArtifactReadWithoutStoreFailsCleanly`
- `TestRemoteArtifactReadIsNarrowableByToolList`

---

# ArtifactStore 依赖什么

## 载体形态：一个目录，每个产物一个 `.log` 文件

`ArtifactStore` 没有数据库、没有索引文件、没有对象存储。载体就是**普通目录**：

```
<artifacts.root>/
  art_b94d27b9934d3e08a52e52d7da7dabfa.log   11 bytes   -rw-------（0600）
  art_2a40b3dc29c93462d15333424cdddcd2.log   900 bytes  -rw-------
  ...
```

| 属性 | 值 |
|---|---|
| 目录权限 | `0700`（`NewArtifactStore` 建目录时设定） |
| 文件权限 | `0600` |
| 文件名 | `art_<32 hex>.log`，id 是 payload 的 SHA-256 前 16 字节 |
| 写入方式 | 临时文件 → `Chmod(0600)` → `os.Rename`（原子替换） |
| 内容寻址 | 是。同一 payload 重复归档得到同一 id，`Put` 检测到文件已存在就直接返回，不重写 |
| 读取 | `os.Open` + `file.ReadAt(offset)`，按字节窗口，起始位置会前移到 UTF-8 边界 |

## 依赖（没有外部服务）

| 依赖 | 说明 |
|---|---|
| 一个可写目录 | `artifacts.root`，启动时 `os.MkdirAll(0o700)` |
| 文件系统语义 | 需要支持 `os.Rename` 同一目录内的原子替换 |
| 系统时间 | mtime 用于 72h 保留期，每 6h 惰性清理一次 |

## `maxBytes` 的准确含义：单产物上限，**不是**总量配额

这一点容易误解，实测确认（把上限设为 1024 字节）：

```
dedupe: id1 == id2 (same payload -> same id, no rewrite)
oversize rejected: OUTPUT_LIMIT_EXCEEDED: payload is 2000 bytes, artifact limit is 1024

files on disk (6):
  art_2a40...log    900 bytes
  art_584f...log    900 bytes
  art_6def...log    900 bytes
  art_afd8...log    900 bytes
  art_b94d...log     11 bytes
  art_d401...log    900 bytes
total on disk: 4511 bytes (store cap was 1024)
```

**上限 1024，目录里却存了 4511 字节。** `maxBytes` 只在 `Put` 里做一件事：
拒绝超过该值的**单个** payload。目录总量没有任何约束，只受 72h 保留期影响。

命令行帮助写的是 `"per-artifact size limit"`，与实现一致；把它理解成磁盘配额是错的。

### 权限上的一处修正

`os.MkdirAll(dir, 0o700)` 的 mode **只作用于它新建的目录**。运维若指定一个已存在的
目录（例如 `/var/lib/...` 由打包脚本以 0755 建好），权限不会被收紧——
而 artifact 文件是 shell 输出的逐字节副本，可能含密钥。

已修：`NewArtifactStore` 在 `MkdirAll` 之后显式 `os.Chmod(abs, 0o700)`，
不依赖调用方的 umask 或预先存在的权限。

## 配置来源

```yaml
artifacts:
  root: /var/lib/aicli-remote/artifacts   # 空 = 禁用
  maxBytes: 67108864                     # 单个产物的上限，默认 64 MiB；不是磁盘配额
```

或命令行 `--artifacts-root` / `--artifacts-max-bytes`。启动时用
`probeWritableDir` 探测可写性，`--check` 会把它列为 problem。

## 代码依赖

- `internal/toolcore` 的 `ArtifactRef` / `ArtifactSink` 接口（scope 校验、id 形状）
- `writeFileAtomicRaw`（同包内）
- 无第三方依赖

## 与本地 artifact 的关系

两者**互相独立**：远程 store 是目标机上的短期缓存（72h），本地 store 是长期副本。
id 空间也不同（远程带 `remote:<target>:` 前缀，本地无前缀），
`ResolveBareArtifactRef` 负责把裸 id 解析成当前 surface 的 scope。

## 一个必须记住的前提

**没有 store 时 shell 仍然折叠，但省略的中段不会被保存。**

修复前，折叠提示会照抄“The complete capture is archived: read the middle with
artifact_read ...”，而该 surface 上：

- `artifact_read` 根本没注册；
- 108421 字节的中段从未写盘。

这比单纯丢数据更糟：它指示模型去调用一个不存在的工具，并给出指向从未存在过的数据的
offset。现在 `ShapeShellWindowWithArchive(..., archived)` 会按实际能力渲染提示，
无 store 时明确写 “NOT kept and cannot be read back”，并打上
`output_window_archived=false`。

部署结论：**要使 artifact 指针有意义，必须配置 `artifacts.root`**。
否则 shell 折叠会成为不可恢复的截断（只是提示现在是诚实的）。

---

# 没有写权限时会怎样

分两种情形，行为不同：

## 情形 A：目录建不出来（启动期）

`NewArtifactStore` 里 `MkdirAll` 失败 → 返回 error → `registry.go` 直接向上抛，
**server 拒绝启动**。

```
create artifact root: mkdir /var/lib/.../artifacts: permission denied
```

这是 fail-fast，正确：与其带着一个半坏的 surface 跑起来，不如在启动时就说清楚。

## 情形 B：目录存在但不可写（运行期）

能启动（目录存在），但每次需要归档的调用都会失败，返回
`ARTIFACT_WRITE_FAILED`。

这条路径上发现并修掉一个**让错误变得不可诊断**的缺陷。

### 缺陷：错误码到不了模型

`buildMCPCallToolResult` 对失败结果只发送 `result.Error`，**完全丢弃 `result.Content`**：

```go
} else {
    errMsg := "工具执行失败"
    if result.Error != nil {
        errMsg = result.Error.Error()
    }
    contents = append(contents, &mcp.TextContent{Text: errMsg})
}
```

而 shell 的三条失败路径（超时、命令失败、归档失败）都把稳定错误码写在
`metadata["error_code"]` 里、把捕获的输出写在 `Content` 里 —— 两处都不会到达调用方。

实测模型当时只看到：

```
artifact archive failed: write artifact: open /tmp/.../.aicli-art-xxx: permission denied
```

没有错误码，没有 next action，无法区分"磁盘满了/只读"与"命令被拒"。

### 修法

`shellFailureError(cause, code, nextAction)` 把三者拼进 `Error` 文本 ——
这是唯一能穿过 MCP 投影的字段：

```
ARTIFACT_WRITE_FAILED: artifact archive failed: write artifact: ... permission denied
 (next: reduce the command output or fix the target artifact store)
```

底层 cause 保留，便于诊断。已覆盖超时、命令失败、归档失败三条路径。

### 未改的部分

`buildMCPCallToolResult` 丢弃 `Content` 是**全局**行为，影响所有工具的错误呈现。
本次不动它：改动面超出 artifact 范围，且现有工具大多已把码写进 `Error`。
若要统一，应作为独立变更评估（例如：失败时同时发送 Content 与 Error）。

## 固化为

- `TestRemoteArtifactRootUnwritableFailsAtStartup`
- `TestRemoteShellArchiveFailureCarriesStableCode`（断言正文含 code、`next:`、以及底层 cause）
