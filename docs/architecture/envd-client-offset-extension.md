# envd 客户端扩展方案：支撑 FUSE 挂载的 offset 语义

> 目标：让 `internal/envdclient` 具备 FUSE 所需的按偏移读写能力，使 envd/沙箱目标可以作为挂载数据面。
> 基座代码：`backend/internal/envdclient/`（client.go / files.go / filesystem.go / process.go）
> 关联设计：`docs/architecture/fuse-dynamic-mount-design.md` §2、§12
> 日期：2026-10-29

---

## 0. 先纠正一个前提

上一版设计文档 §2 写的是"envd 仅整文件语义，读也无 offset"。**这个判断只对了一半，必须纠正。**

读了 `envdclient/files.go` 的实测注释后，事实是：

| 能力 | 实际情况 | 证据 |
|---|---|---|
| **offset 读** | ✅ **已存在**，`/files` 原生支持 `Range` | `files.go:27-29` 记录的实测：`GET + Range: bytes=0-9` → `206 + Content-Range: bytes 0-9/3814` |
| **offset 写** | ❌ 不存在，`POST /files` 是 `O_TRUNC` 整文件 | `files.go:33` 实测注释：`POST /files (multipart) 200, O_TRUNC, and it creates parents` |
| **稀疏写在原地** | ❌ | `WriteFile` 全量写，无 seek |

**结论修正**：读侧不需要新协议，只需给客户端加一个能指定起始偏移的方法（现在是硬编码 `bytes=0-N`）；写侧才是真正缺的部分。

> 这个纠正很重要——它把工作量从"改 envd 服务端"降到"改客户端 + 选一个写侧兜底"，性质完全不同。

---

## 1. 现状精读：三个必须尊重的既有约束

这些是 `files.go` 里已经用实测写死的经验，扩展时不能违背。

### 1.1 `/files` 是唯一的数据通道，且不是 RPC

```go
// files.go:18-23
// filesystem.Filesystem exposes no ReadFile or WriteFile at all (probed 404)
```

`filesystem.Filesystem` 服务**没有** ReadFile/WriteFile。传文件只能走 `GET/POST /files` 这条普通 HTTP 端点，或者"spawn 进程"。任何想加 offset RPC 的想法，都会撞上"上游服务没有这个方法"。

### 1.2 条件读永远拿不到 304

```go
// files.go:42-45
// There is no ETag, and Last-Modified is the *request* time
```

**这直接否掉了最省事的一种 FUSE 实现思路**：靠 `If-Modified-Since` / ETag 做属性缓存失效。FUSE 的 `GETATTR` 拿不到可信 mtime，只能每次真查 `Stat`，或者不缓存。这条约束决定了 attr 缓存策略只能"短 TTL 或不做"。

### 1.3 写成功 ≠ 真写了

```go
// files.go:73-80
// deployed envd skips any multipart part whose field is not exactly "file"
// and still answers `200 []`
```

`WriteFile` 已经为此做了 manifest 校验（`files.go:348-367`）。任何新增的写路径**必须**保留这个"2xx 不当作成功"的校验习惯，否则会出现"挂载看起来正常、文件其实没落盘"。

---

## 2. 能力缺口矩阵

FUSE 需要 9 类操作，对照 envd 现有能力：

| FUSE op | 语义要求 | envd 现状 | 缺口 | 方案 |
|---|---|---|---|---|
| `LOOKUP` | 路径→inode | `Stat` ✅ | — | 直接映射 |
| `GETATTR` | size/mode/mtime | `Stat` ✅ | mtime 走 `Entry.ModifiedTime` | 用 `modifiedTime` 字段，不用 `Last-Modified` |
| `READDIR` | 列目录 | `ListDir` ✅ | 无分页 | 直接映射，大目录注意 |
| `OPEN` | 打开句柄 | 无句柄概念 | 客户端自建句柄表 | §3.2 |
| **`READ`** | **`read(off,size)`** | **`Range` ✅** | **客户端只能发 `bytes=0-N`** | **§3.1 新增 `ReadRange`** |
| **`WRITE`** | **`write(off,data)`** | **❌ O_TRUNC 全量** | **无 offset 写** | **§4 三个候选** |
| `CREATE` | 新建 | `WriteFile` 可创建父目录 ✅ | — | 用空字节写 |
| `UNLINK`/`RMDIR` | 删除 | `Remove` ✅ | 是否递归由 envd 决定 | 递归需客户端自己走 |
| `RENAME` | 重命名 | `Move` ✅ | — | 直接映射 |
| `TRUNCATE` | 截断 | ❌ | 无 | §4 兜底一并处理 |
| `FSYNC`/`FLUSH` | 刷盘 | 语义不存在 | — | 返回 nil（见 §6.2） |

**一句话**：9 类操作里 7 类拿现成的就能用，**只有 `WRITE` 是真缺口**，`READ` 是"能力有、接口没暴露"。

---

## 3. 读侧扩展：新增 `ReadRange`

### 3.1 设计

现有 `ReadFile(ctx, path, maxBytes)` 只发 `bytes=0-(maxBytes-1)`。新增一个接受起始偏移的方法，**复用同一条 `readFile` 内核**：

```go
// ReadRange fetches bytes [off, off+size) of one file.
//
// Negative off means "read from the end" (FUSE does this for tail reads),
// resolved against Total from a Stat when needed.
func (c *Client) ReadRange(ctx context.Context, path string, off, size int64) (*FileRead, error)
```

实现要点（每一条都对应一个已有坑）：

| 要点 | 理由 |
|---|---|
| 复用 `c.readFile`，只改 Range 头的构造 | 保持 `Accept-Encoding: identity`、`Content-Range` 解析、错误分类全部一致 |
| Range 写成 `bytes=off-(off+size-1)` | 与现有前缀写法同构，只多一个起始值 |
| `416` 时不许重试整文件 | 现有 `ReadFile` 在 416 时会退回无 Range 全量读（`files.go:163-165`）。**`ReadRange` 绝不能这么做**——若 caller 要的是文件中间一段，退回全量会把内存打爆。416 应直接返回 `FilesRangeUnsatisfiable` |
| `off >= Total` 直接短路 | 避免一次必然 416 的往返 |
| 保留 `Partial` 语义 | caller 需要知道"后面还有没有" |

### 3.2 句柄表（为什么必须有）

`OPEN`/`RELEASE` 在 envd 侧不存在。客户端需自建：

```go
type fileHandle struct {
    path    string
    size    int64      // 打开时快照，用于检测并发截断
    writes  []writeOp  // 仅当走 §4.3 的缓冲策略时需要
    dirty   bool
}
```

句柄表放在 `mountctl` 的 backend 实现里，**不放 envdclient**——envdclient 保持无状态，符合它现有"每个方法自己构造请求"的并发安全约定（`client.go:91-93`）。

---

## 4. 写侧扩展：三个候选，按侵入性排序

**这是本方案的核心决策点。** 三个候选的根本差别是"改谁"。

### 候选 A：扩展 envd 服务端（加 Range 写 / PATCH）

```
POST /files + Content-Range: bytes 100-199/1000
```

| 优点 | 缺点 |
|---|---|
| 语义最干净，一次往返完成一段写 | **要改 envd 镜像**，而 envd 是上游组件，不在本仓库 |
| 与已有 Range 读对称 | 部署受阻时整个方案卡死 |

**判定：不可作为主线。** 但值得作为**向上游提的 RFC**——若 upstream 接受 `Content-Range` 写，本项目的客户端改动可以立刻受益。

### 候选 B：进程通道 + `dd`（推荐作为主兜底）

已有 `Start` + `SendInput` 可以起一个进程并向其 stdin 流式写入：

```go
// 伪代码：一次 offset 写
pid, _ := c.Start(ctx, StartRequest{
    Cmd: "dd of=<path> bs=1 seek=<off> conv=notrunc status=none",
})
for _, chunk := range chunks {
    c.SendInput(ctx, pid, chunk)   // process.go:436
}
```

| 优点 | 缺点 |
|---|---|
| **零服务端改动**，今天就能用 | 每字节一次 `bs=1` 太慢，须用大 bs + `seek` 换算 |
| stdout/stderr/exit code 齐全，失败可判定 | 引入进程生命周期，需管 PID 回收 |
| 与现有 `SendInput` 一处依赖 | **数据经 argv 之外的 stdin，无 1 MiB argv 限制**（这是 `files.go:306-309` 已论证的优势） |

关键实现细节：

- `bs` 不能是 1。用 `bs=65536 seek=$((off/65536))`，但 `off` 未必对齐——**要求上层（FUSE）按 64 KiB 对齐下发**，或者 `dd` 拆两次（对齐段 + 尾部 `bs=1`）。
- **`conv=notrunc` 不可省**。少了它 `dd` 会先把文件截断到 seek 位置，等于静默破坏数据。
- 退出码必须检查。`dd` 失败时仍可能返回 0 以外但无输出，需靠 `exit code != 0` 判定。

### 候选 C：整文件读-改-写（保底，行为明确可预期）

```
ReadFile(whole) → 内存中 splice → WriteFile(whole)
```

| 优点 | 缺点 |
|---|---|
| 逻辑最简单，天然支持 `TRUNCATE` | **O(n) 流量**，1 MB 文件改 1 字节要传 2 MB |
| 无新依赖 | 并发写会互相覆盖；大文件爆内存 |

**判定：只在"文件小于阈值"时启用**（建议 4 MiB），超过阈值明确报错，绝不静默降级为候选 C 而不告知。

### 4.4 三者的选择策略

```go
func (b *envdBackend) chooseWriteStrategy(size int64) (strategy, error) {
    switch {
    case b.probe.hasRangeWrite:        // 候选 A，若上游支持
        return rangeWrite, nil
    case b.probe.hasProcess:           // 候选 B，进程通道可用
        return ddWrite, nil
    case size <= b.cfg.SmallFileLimit: // 候选 C
        return readModifyWrite, nil
    default:
        return nil, ErrOffsetWriteUnsupported
    }
}
```

**关键：探测而非假设。** `files.go:46-48` 已写明"Capabilities must be probed, never derived from a version string"。同样原则适用于写策略——`ProbeFiles` 已示范了如何探测（`files.go:393`），照此新增 `ProbeWriteCapabilities`。

---

## 5. 接口设计

### 5.1 新增文件 `envdclient/rangefile.go`

不散落到 `files.go`（那里已经很密集），单开一个文件承载 offset 语义：

```go
// ReadRange / OpenRead / OpenWrite / Truncate 与 §4 的策略选择
```

### 5.2 与 `mountctl.Backend` 的对接

`fuse-dynamic-mount-design.md` §12 定义的 `Backend` 接口要求 `ReaderAt`/`WriterAt`。envdclient 的 `*Client` **不应直接实现它**，中间要有一层：

```
mountctl.Backend (offset 语义)
      ↓
envdBackend (策略选择、句柄表、大小限制)
      ↓
envdclient.Client (无状态、整文件 + ReadRange)
```

理由：`Client` 现在无状态且并发安全，把句柄表塞进去会破坏这个性质；而策略选择（走 dd 还是 RMW）本质是挂载层的决策，不该污染通用客户端。

### 5.3 必须显式暴露的能力标志

```go
type Capabilities struct {
    RangeRead      bool  // 几乎总是 true
    RangeWrite     bool  // 仅候选 A 时 true
    ProcessChannel bool  // 决定候选 B 是否可用
    MaxReadBytes   int64 // 从 Content-Range 反推，用于分块
}
```

`mountctl` 在建挂载阶段就调用 `ProbeCapabilities`，**不支持就在建挂载时返回 `409`**，而不是等运行期千次失败（这是上一版设计 §12 已定的原则）。

---

## 6. 两个必须正面处理的语义陷阱

### 6.1 mtime 不可信

`files.go:42-45` 说 `Last-Modified` 是**请求时刻**。但 `filesystem.Stat` 返回的 `Entry.ModifiedTime` 是真实 mtime（`filesystem.go:42`）。

**结论**：`GETATTR` 必须走 `Stat`，**绝不能**从 `/files` 的响应头取时间。这是两个能力之间的一个陷阱，很容易写错。

### 6.2 `FLUSH`/`FSYNC` 要返回 nil

FUSE 在 `close()` 时会发 `FLUSH`，期望它把缓存刷到持久存储。envd 没有这个语义，**但如果返回错误，就会复现 tabbit 的 close 阶段 EIO 缺陷**（`fuse-local-mount.md` §8 记录的正是 `RELEASE`/`FLUSH` 返回 EIO 导致 `cat` 退出码异常）。

**处置**：`Flush` 返回 nil（若走候选 C，则在此处真正落盘），并单独计数 `flush_called_total`。让收尾错误不污染数据通路。

### 6.3 并发写冲突

候选 C 是最危险的：两个写者各自读全量、各自写全量，后者覆盖前者。**必须**在 `mountctl` 层对同一 mountpoint 串行化写（per-mount write lock），不能靠 envd 侧。

---

## 7. 测试策略

现有测试文件是可复用的模板（`files_test.go` / `filesystem_test.go` 都用 httptest 打桩），新增测试照此：

| 测试 | 断言什么 | 为什么必须有 |
|---|---|---|
| `TestReadRange_MidFile` | `Range: bytes=100-199`，返回 100 字节，`Partial=true` | 验证不是从 0 开始 |
| `TestReadRange_416NoRetry` | 416 时不发第二次全量请求 | 防止内存打爆，这是与 `ReadFile` 的关键行为差异 |
| `TestReadRange_OffBeyondEOF` | `off >= size` 直接短路，无网络请求 | 性能路径 |
| `TestWriteStrategy_Fallback` | 无 RangeWrite、有 Process → 选 dd | 验证策略选择 |
| `TestWriteDd_Notrunc` | 生成命令含 `conv=notrunc` | **少了它会静默损坏文件，必须用测试钉死** |
| `TestWrite_RMWLimit` | 超阈值返回 `ErrOffsetWriteUnsupported` | 防止静默降级 |
| `TestFlush_ReturnsNil` | `Flush` 不返回错误 | 防复现 close EIO |
| `TestGetattr_UsesStatNotHeader` | mtime 来自 Stat 而非响应头 | 防踩 §6.1 |

---

## 8. 分期

| 步 | 内容 | 可独立验证 |
|---|---|---|
| **E0** | `ReadRange` + `ProbeCapabilities` | 能从中间读一段，且 416 不重试 |
| **E1** | `envdBackend` 骨架 + 只读挂载 | 沙箱环境能 `ls` + `cat` |
| **E2** | 候选 C（小文件 RMW）+ per-mount 写锁 | 小文件可写、`git commit` 可用 |
| **E3** | 候选 B（dd 进程通道） | 大文件可随机写 |
| **E4** | 向上游提候选 A 的 RFC | 若被接受，E3 可退役 |

**E0 立刻可用**：即使写侧一步没做，仅靠 `ReadRange` + 现有 `Stat/ListDir/Remove/Move`，就能支撑**只读挂载**——而只读正是上一版设计 §9.2 定的默认模式。

---

## 9. 风险与未决

| # | 风险 | 说明 |
|---|---|---|
| 1 | envd 是否可改 | 若 envd 属外部镜像不可动，候选 A 永久不可用，写侧只能停在 B/C |
| 2 | `dd` 在目标镜像中是否存在 | 极简镜像可能没有 coreutils。需 `ProbeCapabilities` 里实测 |
| 3 | `SendInput` 的流控 | 大块写入可能压满 stdin 缓冲，需分块 + 背压，不能一次灌完 |
| 4 | 大目录 `READDIR` | `ListDir` 无分页，十万文件的目录会一次性返回 |
| 5 | 并发 `READ` 的连接数 | 每块读一次 HTTP 请求，FUSE 并发读会放大连接数，需限流 |

---

## 10. 小结

扩展 envd 客户端支撑挂载，**真实工作量比预想小、但比预想的更不对称**：

- **读侧几乎不用做**——`Range` 已经在 envd 里，只是客户端接口没暴露。加一个 `ReadRange` 即可，但要守住"416 不退回全量"这条线。
- **写侧是唯一硬骨头**——`POST /files` 是 `O_TRUNC` 整文件，没有 offset。三个候选里，进程通道（`dd conv=notrunc`）是唯一"零服务端改动 + 支持任意大小"的路径，应作为主兜底；整文件读改写只用于小文件。
- **两个陷阱必须提前防**：mtime 只能取 `Stat`（响应头是假的）；`Flush` 必须返回 nil（否则复现 tabbit 的 close EIO）。

按 E0→E4 推进，**E0 单独就能交付"只读挂载"**，那时写侧一步都还没做——这正是把风险前置、让每步都有可见产出的排法。
