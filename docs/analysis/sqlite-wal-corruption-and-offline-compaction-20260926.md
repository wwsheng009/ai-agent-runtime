# SQLite WAL 损坏根因与离线压缩（2026-09-26）

> 关联：`docs/plan/runtime-store-append-hotpath-optimization-plan-20260918.md`（P1.6，曾把
> `incremental_vacuum` 移入后台维护）、`docs/ANALYSIS-sqlite-lock-problem.md`（锁等待）。
> 本文记录事故结论与替代方案；历史计划文档保留原貌，不再回改。

## 1. 现象

`~/.aicli/sessions/` 下的库在运行中出现：

```
sqlite3: database disk image is malformed
*** in database main ***
Tree 10 page 177315 right child: Bad ptr map entry key=175644 expected=(5,177315) got=(5,178501)
Tree 10 page 175644 cell 5: Rowid 9202282 out of order
```

`artifacts.sqlite`（3.2GB）、`session_runtime.sqlite`（745MB）先后损坏；换用旧驱动后
`quick_check` 能恢复正常，说明**损坏发生在写入侧，而不是读侧**。

## 2. 根因：上游 Windows VFS 的 wal-index（`-shm`）拷贝实现

- 驱动：`github.com/ncruces/go-sqlite3 v0.32.0`（本仓库此前的锁定版本）。
- 该版本在 Windows 上用“按锁边界拷贝”的伪共享内存模拟 WAL-index：`vfs/shm_copy.go`。
  多连接/多进程并发写 + checkpoint 时，checkpointer 会用**陈旧副本**回填 `-shm`，
  丢弃未回填帧，直接产生 malformed / `not a database`。
- 上游记录：issue #404（复现器：**去掉 checkpoint 仍每次损坏**）、修复 PR #405 / #413，
  修复版本 **v0.35.3**；要求 Go ≥1.26，并要求 Windows 10 1803+/Server 2019+
  的 `VirtualAlloc2/MapViewOfFile3`（否则回退到旧的拷贝实现）。
- 因此：**去掉 `incremental_vacuum`/`auto_vacuum` 并不能单独避免损坏**；它们只是
  “放大器”（在线搬页 + 重写 ptrmap + 每次关库推进 WAL 代次），会把损坏概率和形态放大。
  根治只能升级驱动；本次已升级到 **v0.35.6**（见 `backend/go.mod`）。

## 3. 已落地的三层处理

| 层 | 动作 | 位置 |
|---|---|---|
| 根治 | 驱动 v0.32.0 → **v0.35.6**（引擎由 wasm/wazero 改为纯 Go 翻译实现，无启动编译预热） | `backend/go.mod`、`internal/sqlitedriver/` |
| 去放大器 | 删除 `PRAGMA auto_vacuum=INCREMENTAL`、后台 `PRAGMA incremental_vacuum(N)` 维护任务、`wal_checkpoint(TRUNCATE)`（Close 只做 `PASSIVE`）、会话历史清理后的 `incremental_vacuum(256)` | `internal/chat/session_runtime_store.go`、`sqlite_storage.go`、`runtime_store_append_stats.go` |
| 防御 | 打开文件前对账 `-wal/-shm`（陈旧 wal-index 会被采信为权威索引）；源码级护栏禁止上述 PRAGMA 回潮；并发 WAL 写完整性压测；artifact store 与 runtime store 共用可选健康探测 | `internal/sqliteutil`、`internal/artifact/store.go`、`internal/chat/sqlite_hygiene_test.go`、`runtime_store_wal_concurrency_test.go` |

**win7 构建**（Go 1.21.4 + `go.win7.mod` + v0.22.0）拿不到修复（PR #405 要求 Go ≥1.26，
且占位 API 在 Win7 不存在），因此 win7 走 `journal_mode=DELETE`（回滚日志，跨进程靠
数据库文件自身锁，不经 `-shm`）。功能无缺失，仅失去 WAL 的并发优势。

## 4. 替代在线页回收：`aicli storage compact`

在线 `incremental_vacuum` 移除后，删除产生的空闲页（freelist）不再归还文件系统。
唯一被认可的空间回收途径是**独占访问时的离线 `VACUUM`**：

```bash
# 全量（不存在的库自动跳过）
aicli storage compact

# 单个库 / 预演 / 机器可读
aicli storage compact --target runtime
aicli storage compact --db ~/.aicli/sessions/runtime/session_runtime.sqlite --dry-run
aicli storage compact --db <path> --json
```

目标：`all|runtime|history|artifacts|analytics|team|agent-control|background`
（路径由 `sessionruntime.ResolvePaths`（CLI-local）+ `aiclipaths` 默认值解析，与运行时一致）。

安全性质（均有单测覆盖，`cmd/aicli/commands/storage_compact_test.go`）：

- 只执行 `VACUUM`（可附 `PRAGMA auto_vacuum=NONE` 把历史库转出 INCREMENTAL 模式）；
  绝不触碰 `incremental_vacuum` / `wal_checkpoint(TRUNCATE)`；
- 打开前后各做一次 `PRAGMA quick_check`：**损坏库只报告、不改动**（退出码 2，
  明细截断到前 6 行 + 总行数）；
- 短 `busy_timeout`（默认 3s，`--busy-timeout` 可调）：库里还有活跃写事务/快照读者时
  立刻 `in_use` 退出（退出码 2），不排队、不长时间持锁；
- `--require-exclusive`：打开前做操作系统句柄级独占探测——Windows 上以零共享方式
  打开主库文件，任何其它进程/连接（含空闲句柄）存在即判定 `in_use`（退出码 2）；
  非 Windows 平台没有等价可靠实现，会显式报不支持。默认只靠 BUSY 锁冲突判断，
  覆盖不了"其它进程打开着库但空闲"的形态。
- 退出码沿用 stats 约定：`0` 成功（含 `absent`/`skipped`）、`1` 参数错误、
  `2` 确定性错误（占用/损坏/不可读）。

实测：

- 对 745MB 的损坏副本执行：`quick_check` 直接给出上文的 ptrmap 明细，`VACUUM` 被跳过，
  文件哈希前后完全一致（**未改坏库**）；
- 对 96MB 的真实库副本执行：`24644→24538` 页、回收 424KiB、`quick_check=ok`，退出码 0。

## 5. 打开前健康探测（可选，2026-09-26 追加）

> **默认关闭**：不设置任何环境变量时与从前行为完全一致，探测零开销跳过。

设置环境变量 `AICLI_SQLITE_HEALTH_PROBE=quick` 后，runtime store 和文件型
artifact store 会在第一次打开、执行迁移之前运行只读 `PRAGMA quick_check`：

- 返回 `ok` → 正常继续；
- 非 `ok` 或探测执行失败 → 打开直接失败（fail-closed），进程以可见错误退出，
  不再在损坏库上继续写入；
- 未设置或其它值 → **完全跳过（零开销，默认关闭）**；只有显式设置 `quick` 才开启。

默认关闭的原因：`quick_check` 需要全量扫描库文件（807MB 库实测约 3s，数 GB 库可达
分钟级），不适合每次 CLI 启动都做；建议在 runtime-server、维护脚本或排障会话中
显式开启。一次性只读体检也可以直接用 `aicli storage compact --dry-run`
（quick_check 前置、损坏库退出码 2、不改写主库；打开前仍会做陈旧 wal-index 对账）。

## 6. 已损坏库的处置（不能由 VACUUM 自动修复）

`quick_check` 报告 `Freelist: size is ...`、`btreeInitPage()`、`bad ptr map`
等明细时，损坏已经落在主库数据页；`-shm` 对账只能处理“陈旧索引”，不能还原
已经写坏的 B-tree。此时 `storage compact` 拒绝执行 `VACUUM` 是预期的 fail-closed
行为，**不要使用 `--no-integrity-check` 强行重建**。

先退出所有 aicli/runtime-server 进程，并保留原文件及副文件。若需要尽量抢救
artifact 行，可使用系统 SQLite CLI 的只读 `.recover` 生成新库（绝不在原库上
`.read`）：

```powershell
$db = "$HOME\.aicli\sessions\runtime\artifacts.sqlite"
$sql = "$HOME\.aicli\sessions\runtime\artifacts.recovered.sql"
$new = "$HOME\.aicli\sessions\runtime\artifacts.recovered.sqlite"

sqlite3 -readonly $db ".recover --ignore-freelist" > $sql
sqlite3 $new ".read '$sql'"
sqlite3 -readonly $new "PRAGMA quick_check"
```

`.recover` 只能尽量提取仍可读取的页，必须核对 `quick_check=ok`、表结构以及
关键 artifact/checkpoint 数量后，才可以在备份原库的前提下替换。若抢救结果
不可用，最安全的降级是将主库、`-wal`、`-shm` 一起改名留证，再让 artifact
store 创建空库；这会丢失无法从其它来源重建的 artifact 数据，但不会继续在坏库
上写入。当前运行时代码不自动执行该动作，避免把潜在可恢复数据静默删除。
