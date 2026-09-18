# SQLite 会话快照审计与加固方案（P2.13：`SnapshotSession` / `Snapshot`）

- 版本：v1.0（2026-09-18 初稿）
- 状态：待评审。§6 决策记录为默认执行基线
- 日期：2026-09-18
- 覆盖项：P2.13
- 分册索引：`docs/plan/runtime-store-hardening-followup-index-20260918.md`
- 范围说明：本项针对 `internal/chat.SQLiteSessionStorage`（会话历史库，非 runtime store）；其单连接池与 runtime store 是两套独立句柄

---

## 0. 现状与代码事实

### 0.1 调用链

- 接口：`SessionStorageSessionSnapshotter.SnapshotSession(ctx, sessionID, destinationPath)`（`backend/internal/chat/session_storage_factory.go:169`）。
- 惰性包装：`lazy_session_storage.go:392-398` 透传。
- 实现：`backend/internal/chat/sqlite_storage.go:238-295`。
- 生产调用方：`backend/cmd/aicli/commands/chat_debug_archive.go:283-322`（`/debug archive` 流程，用于给归档包附带一致性会话快照）。

### 0.2 实现事实

`SnapshotSession`（`sqlite_storage.go:238-295`）：

1. `sessionID` 清洗 + `prepareSnapshotDestination`（`:246`，实现 `:297-315`）：`os.MkdirAll` + `os.Stat` 判存在 + 返回绝对路径。
2. `connection, err := s.db.Conn(ctx)`（`:250-254`）——从会话存储连接池取连接。
3. defer 清理（`:257-265`）：失败时 `ROLLBACK`（若已 attach）+ `DETACH DATABASE snapshot`（用 `context.Background()`）+ 删除 destination 文件（`!committed` 时）。
4. `ATTACH DATABASE ? AS snapshot`（`:266-269`）→ `BEGIN`（`:270-272`）→ `SELECT 1 FROM sessions WHERE id=?`（首次读，建立读事务快照，`:273-279`）→ 建快照 schema（`:280-282`，`sqliteSessionSnapshotSchema` 静态定义，`:485-523`）→ `copySQLiteSessionSnapshot`（`:283`，实现 `:525+`，逐表 `INSERT INTO snapshot.x SELECT ... FROM main.x WHERE session_id=?`）→ `COMMIT`（`:286-288`）→ `DETACH`（`:289-292`）。
5. 全库快照 `Snapshot`（`:201-233`）走 `VACUUM INTO ?`（`:228`）。

会话存储连接池：`sqlite_storage.go:133-134` `SetMaxOpenConns(1)` / `SetMaxIdleConns(1)`；WAL + `wal_autocheckpoint=256`、`journal_size_limit=16MiB`（`:387-403`）。

调用方的上下文与错误处理（`chat_debug_archive.go`）：

- `SnapshotSession(context.Background(), ...)`（`:307`）与 `Snapshot(context.Background(), ...)`（`:309`）——**无 deadline**。
- 失败即中止整个归档并返回错误（`:311-314`），快照被视为必需项。

---

## 0.3 审计发现（按严重级）

| 编号 | 级别 | 发现 | 证据 | 影响 |
| --- | --- | --- | --- | --- |
| F1 | S1 | 快照在会话存储的**唯一连接**上执行；AI CLI 的会话读写（消息追加、标题更新）在快照期间全部排队 | `sqlite_storage.go:133-134, 250` | 大库/慢盘上用户可见卡顿；与 runtime store P0 同类问题 |
| F2 | S1 | 调用方使用 `context.Background()`；实现内部也未加 deadline | `chat_debug_archive.go:307,309`；`sqlite_storage.go:238` | 快照可无限期挂起（锁竞争、慢盘、巨大库），归档命令无法取消 |
| F3 | S2 | destination 存在性检查是 stat-then-create（TOCTOU）；失败清理无条件 `os.Remove(destinationPath)` | `sqlite_storage.go:297-315`（`SnapshotSession`）/ `:223-227`（`Snapshot`）；清理 `:262-264` / `:229` | 竞态下可能删掉「非本次创建」的文件；多进程/并发调试归档时风险放大 |
| F4 | S2 | 无容量/磁盘预检；快照大小与库大小同阶 | `sqlite_storage.go:283`（全量复制该会话行） | 磁盘写满时失败并清理，但无预警；大附件型会话可能写爆临时盘 |
| F5 | S2 | 快照失败会使整个 debug archive 失败（即便快照只是「附带增强」） | `chat_debug_archive.go:311-314` | 可诊断性被单点拖累：用户最需要诊断时归档不可用 |
| F6 | S3 | 快照 schema 是静态定义，无版本/漂移门禁 | `sqlite_storage.go:485-523` | 会话表迁移后快照可能缺列/缺表，读取快照的工具链在后期才报错 |
| F7 | S3 | 无一致性校验（行数/用户版本）与可观测量（耗时、字节、行数） | `sqlite_storage.go:283-294` | 快照「成功」但内容缺失时无法及时发现 |
| F8 | S3 | 多库事务不变量未文档化：主库只读、destination 为新建普通库（非 WAL），依赖「主库在事务内不被写」 | `sqlite_storage.go:266-288` | 后续维护者若在事务内写主库，会引入跨库原子性/锁语义风险 |

> 附注（非缺陷，需保持）：当前 `BEGIN`（deferred）+ 首条 `SELECT` 建立读事务后，`snapshot.*` 的写入发生在 attached 的新库上；SQLite 对「主库只读 + 目的库写」不涉及多库原子提交问题——但这是**依赖前提**，必须写入代码注释与测试，避免被无意破坏（F8）。

---

## 1. 目标 / 非目标 / 成功度量

### 1.1 目标

| 编号 | 目标 |
| --- | --- |
| G1 | 快照期间**不阻塞**会话存储的用户可见读写（F1） |
| G2 | 快照全过程有 deadline、可取消、可观测（F2、F7） |
| G3 | destination 有明确归属权（只清理自己创建的文件），关闭 TOCTOU（F3） |
| G4 | 磁盘/容量预检与明确的失败语义（F4） |
| G5 | 快照失败默认降级为「归档带告警、快照标记不可用」，不再拖垮整个归档（F5，决策见 D3） |
| G6 | 快照 schema 与会话 schema 的漂移有门禁（F6）与一致性校验（F7） |
| G7 | 多库事务不变量写入注释与测试（F8） |

### 1.2 非目标

- 不改快照的**内容契约**（同一套表、按 session 过滤、单事务一致视图）。
- 不改 `Snapshot`（全库 `VACUUM INTO`）的输出格式；只改其执行连接与超时。
- 不引入新的压缩/加密格式；归档打包（zip）逻辑不在本项范围。
- 不改会话库 schema 与迁移历史。

### 1.3 成功度量

| 指标 | 目标 | 观测方式 |
| --- | --- | --- |
| 快照期间会话存储写延迟 P99 | ≤ 基线 +10ms（现状：整个快照时长） | 专项并发测试：快照进行中持续 `SaveMessage` |
| 快照取消响应 | ≤ 2s（ctx 取消到文件清理完成） | 单测故障注入 |
| 快照失败影响面 | 归档仍产出（快照项标记 unavailable） | 集成测试注入快照失败 |
| 归属权安全 | 0 次「删除非本次创建文件」 | 单测 + 代码评审 |
| schema 漂移门禁 | 迁移新增表/列导致快照缺项时 CI 必红 | 契约测试 |
| 可观测 | 每次快照一条结构化日志（耗时/字节/行数/校验结果） | 日志断言 |

---

## 2. 加固设计

### 2.1 G1：专用快照连接池（消除用户可见阻塞）

- 为 `SQLiteSessionStorage` 增加 `snapshotDB *sql.DB`（惰性打开，`MaxOpenConns(1)`），与主池（用户读写）分离：
  - DSN 复用主库路径的 `file:` URI，附 `_pragma=busy_timeout(...)&_pragma=cache_size(...)&_pragma=temp_store(FILE)&_pragma=mmap_size(0)`；
  - **不加** `query_only`（快照事务要写 attached 的 destination 库）；
  - 与主池共享同一文件与 WAL，读事务不阻塞写；快照自身与其它快照串行（1 连接，足够且可控）。
- `SnapshotSession`/`Snapshot` 改用 `snapshotDB.Conn(ctx)`；
- 打开失败 → 降级回主池（记录告警 + 计数），行为与现状一致（不因新特性导致功能不可用）；
- 关闭顺序：`CloseStorage` 先关 `snapshotDB` 再关主池（主池关闭前的 `wal_checkpoint(TRUNCATE)` 不应被快照读者阻塞——与 runtime store P1.7 的 §3.4 同一原则）。
- 复用性：该模式与 P1.7（runtime store 双池）共享「读池 DSN + 降级 + 关闭顺序」设计，建议抽同一套 helper（`internal/chat/sqlite_pool_helpers.go`），避免两份实现漂移。

### 2.2 G2：超时与取消

- 新增配置：`SessionSnapshotTimeout`（默认 `min(BusyTimeout*6, 60s)`；`0` = 不额外加（沿用调用方 ctx），默认非 0）。
- 实现：`SnapshotSession` 入口 `ctx, cancel := context.WithTimeout(ctx, cfg.SessionSnapshotTimeout)`；失败的清理路径（`ROLLBACK/DETACH/os.Remove`）使用**独立短超时**（如 2s）而不是 `context.Background()`（`:257-265` 当前用 Background，可能再次挂起）。
- 调用方：`chat_debug_archive.go:307/309` 改为传入带预算的 ctx（或依赖 store 侧默认值），并把「快照耗时」计入归档进度输出。

### 2.3 G3：destination 归属权（关闭 TOCTOU）

替换 `prepareSnapshotDestination` 的 stat-then-create：

1. `resolved := filepath.Abs(destination)`；`os.MkdirAll(dir)`；
2. 以 `os.OpenFile(resolved, O_CREATE|O_EXCL, 0o600)` **原子创建**空文件并立刻关闭；`os.IsExist(err) → 明确报错`（destination 已存在）；其它错误原样返回。
3. SQLite 对零长度文件按空库处理 → `ATTACH` 成功（需在测试中锁定这一行为）。
4. 归属权随路径确定：清理 defer 仅在「本次 O_EXCL 创建成功」时执行 `os.Remove`（`createdOwnership=true`）；`ATTACH` 失败且文件是我们创建的 → 删除，否则不动。
5. 忽略符号链接：`O_EXCL` 不跟随已存在 symlink（已存在即失败），保持简单即可。

### 2.4 G4：容量与磁盘预检

- 在复制前估算该会话的数据量：

  ```sql
  SELECT COALESCE(SUM(byte_count), 0) FROM session_messages WHERE session_id = ?;
  ```

  加上 `session_prompt_messages` 与索引/页开销余量（×1.3）。
- 预检目标盘可用空间（Windows：`GetDiskFreeSpaceEx`；跨平台可借助 `golang.org/x/sys` 或退化为「尝试写入直到失败」的现状语义）。
- 新增 `SessionSnapshotMaxBytes`（默认 0 = 不限制）：超过阈值直接返回「快照过大，已跳过」的可解释错误（配合 §2.5 决策降级）。
- 预检失败/超限：不创建 destination（或创建后立即删除），不触发长事务。

### 2.5 G5：失败语义（快照非致命）

- Store 层保持强错误返回（不吞）。
- 调用方 `attachChatDebugSessionSnapshot` 增加策略：
  - 默认 `degrade`：快照失败 → 记录告警 + 在归档清单中把 `session_file` 项标记 `unavailable`（附失败原因），归档继续产出；
  - 配置/参数 `--require-snapshot`（命名待定）保留严格模式；
  - 决策理由：debug archive 的语义是「尽可能收集证据」，快照失败不应让用户失去其余诊断材料（F5）。
- 归档 manifest 需能表达「session_file: unavailable(reason)」，避免读者把「文件缺失」误解为「会话为空」。

### 2.6 G6/G7：schema 漂移门禁与一致性校验

- **门禁测试**：读取主库 `PRAGMA table_info(<table>)` 与快照 schema 定义对比（至少覆盖 `sessions/session_messages/session_prompt_messages` 及迁移新增表）；迁移提交必须在同一 PR 内更新 `sqliteSessionSnapshotSchema`，否则 CI 红。
- **版本标记**：快照库写入 `PRAGMA user_version = <主库 user_version>`（同一事务内），供下游判断 schema 版本。
- **一致性校验**（事务内、COMMIT 前）：对 `sessions`(1) / `session_messages` / `session_prompt_messages` 分别比较主库与快照库的行数与 `COALESCE(SUM(byte_count),0)`；不一致即返回错误并回滚（destination 清理走 §2.3 的归属权路径）。
- **不变量注释 + 测试**（F8）：在事务代码处注释「主库在快照事务内必须只读；destination 为 O_EXCL 新建普通库；禁止在主库上执行任何写语句」；测试用「在快照事务期间尝试写主库」的故障注入不可行（代码层禁止），改为断言 `main` 连接上未执行任何 INSERT/UPDATE/DELETE（可用 driver trace 或代码评审 + grep 门禁）。

### 2.7 G7 可观测性

每次快照输出一条结构化日志/计数：

| 字段 | 说明 |
| --- | --- |
| `session_id`（脱敏规则沿用既有日志约定） | 目标会话 |
| `duration_ms` | 全程 |
| `wait_ms` | 取连接等待（快照池） |
| `bytes_written` | destination 文件大小 |
| `rows_sessions/messages/prompt_messages` | 行数（校验结果） |
| `degraded_to_main_pool` | 是否因快照池打开失败降级 |
| `result` | ok / timeout / schema_drift / too_large / io_error |

### 2.8 锁、检查点与残留清理（审查 R6）

- **快照读事务 vs 检查点**：快照事务在主库上是读事务（WAL 读标记），会阻止会话存储 `CloseStorage` 的 `PRAGMA wal_checkpoint(TRUNCATE)`（`sqlite_storage.go:321-324`）越过它；跨进程快照同理会拖住 runtime store `Close()` 的 TRUNCATE 降级路径。两处都已有 PASSIVE 兜底，但需计数（新增 `snapshot.checkpoint_blocked` 与 Close 降级计数），并在文档说明「TRUNCATE 降级属预期而非故障」。
- **锁序不变量**（与 D6 呼应）：快照事务**只读主库、只写 destination**（destination 为 O_EXCL 新建的普通 rollback-journal 库）。禁止在该事务内写主库；不能用 `PRAGMA query_only` 保护主库（它会同时禁止写 destination）。
- **`VACUUM INTO` 全库快照**同样持有主库读事务直到写完，也必须走专用快照池 + 超时（§2.1/§2.2）。
- **进程被杀残留**：`defer` 清理不会执行，`.aicli-session-snapshot-*` 临时目录与半成品 destination 会残留。缓解：启动时清扫超过 N 小时（建议 24h）的该前缀目录（仅限 aicli 自己创建的临时父目录），并在 runbook 说明；正常运行时的有界清理由 §2.2 的超时覆盖。

---

## 3. 兼容 / 迁移 / 回滚

- 无磁盘格式变更；destination 仍是普通 SQLite 文件，归档/读取工具链不变。
- 新增配置全有默认值；快照池打开失败自动降级回主池（现状行为）。
- 快照失败从「中止归档」改为「降级 + 标记」是**用户可感知的行为变化**（D3），需在 release notes 与 debug archive 帮助文本中说明；严格模式保留。
- 回滚：关闭快照池（配置）→ 回主池路径；恢复严格失败语义（配置项）。

## 4. 测试与验收（DoD）

1. `TestSnapshotSessionDoesNotBlockSessionWrites`：快照进行中并发 `SaveMessage`，断言写 P99 增量 ≤10ms（阈值可按 CI 放宽，压测报告给绝对值）。
2. `TestSnapshotSessionCancelCleansUp`：ctx 取消 → ≤2s 返回、destination 不存在、无 `DETACH` 泄漏。
3. `TestSnapshotDestinationOwnership`：预置同名文件 → 报错且**不删除**该文件；并发两个快照 → 后者失败且不动前者的文件。
4. `TestSnapshotSessionPreflightTooLarge`：构造超过 `SessionSnapshotMaxBytes` 的会话 → 可解释错误、无残留文件。
5. `TestSnapshotSessionSchemaParity`：迁移后 schema 与快照 schema 逐列比对（门禁测试）。
6. `TestSnapshotSessionRowCountVerification`：注入「复制丢行」（测试后门）→ 返回 schema_drift/mismatch 错误、destination 被清理。
7. `TestSnapshotUserVersionCopied`：快照 `PRAGMA user_version` == 主库。
8. `TestSnapshotSessionDegradesWhenPoolUnavailable`：快照池 DSN 非法 → 走主池且功能正常，`degraded` 计数 +1。
9. `TestDebugArchiveDegradesOnSnapshotFailure`：注入快照失败 → 归档仍产出，manifest 标记 `session_file: unavailable(reason)`。
10. 既有回归：`go test ./internal/chat/ -run "Snapshot"`、`go test ./cmd/aicli/commands/ -run "DebugArchive|TestLocalHost"`、`go vet`。
11. `TestSnapshotSessionDoesNotBlockCheckpoint`：快照进行中触发 `CloseStorage`/WAL checkpoint，断言不被快照读标记拖过阈值（或降级计数按预期 +1）；审查 R6。
12. `TestSnapshotStaleTempDirSweep`：预置超龄 `.aicli-session-snapshot-*` 目录 → 启动清扫删除；新鲜目录保留。

验收门槛：1–12 全绿；F1–F8 每项有对应修复或明确的「不修复 + 理由」记录。

## 5. 里程碑与工作量

| 里程碑 | 内容 | 估算 |
| --- | --- | --- |
| M0 | 审计结论复核 + 快照日志/计数骨架 | 0.5d |
| M1 | 专用快照池 + 超时/取消（G1/G2） | 1.5d |
| M2 | 归属权 + 预检（G3/G4） | 1d |
| M3 | 失败语义降级 + manifest 标记（G5） | 0.5d |
| M4 | schema 门禁 + 一致性校验（G6） | 1d |
| M5 | 并发压测与验收报告 | 0.5d |

## 6. 决策记录（评审基线）

| 编号 | 决策 | 状态 |
| --- | --- | --- |
| D1 | 快照使用专用单连接池（非主池），打开失败降级主池 | 待评审（默认执行） |
| D2 | destination 用 `O_CREATE\|O_EXCL` 原子创建，仅清理自建文件 | 待评审（默认执行） |
| D3 | 快照失败默认降级为「归档继续 + manifest 标记 unavailable」，保留严格模式开关 | 待评审（默认执行） |
| D4 | 新增 `SessionSnapshotTimeout`（默认 `min(BusyTimeout*6,60s)`）与 `SessionSnapshotMaxBytes`（默认 0=不限） | 待评审（默认执行） |
| D5 | 快照 schema 纳入迁移门禁（改表必须同步改快照 schema） | 待评审（默认执行） |
| D6 | 事务不变量：主库只读 + destination 新建普通库，写入代码注释与 grep 门禁 | 待评审（默认执行） |
| D7 | 快照读事务只读主库、只写 destination；检查点降级计数化；启动清扫超龄快照临时目录（审查 R6） | 待评审（默认执行） |

## 7. 开放问题

- Q1：`SessionSnapshotMaxBytes` 默认 0（不限）是否需要更保守的默认（如 512MiB）？取决于目标用户会话规模分布。
- Q2：磁盘预检是否值得引入 `golang.org/x/sys` 依赖，或以「写前检查 + 失败清理」替代？
- Q3：manifest 的 `unavailable` 表达需要前端/工具链配合（workspace UI 是否消费 debug archive manifest）？
- Q4：`Snapshot`（全库 `VACUUM INTO`）是否也要纳入「归档降级」语义，还是维持严格？

---

## 8. 实施记录：P2.13（2026-09-18）

状态：**store 侧 + 归档降级已实施并回归通过**（分册 §6 决策 D1–D7 均按基线执行）。

### 8.1 改动清单

| 文件 | 内容 |
| --- | --- |
| `internal/chat/session_storage_factory.go` | 新增 `SessionSnapshotTimeout`（默认 `min(BusyTimeout*6, 60s)`；负值=沿用调用方 ctx）与 `SessionSnapshotMaxBytes`（0=不限） |
| `internal/chat/sqlite_storage_snapshot.go`（新增） | 专用快照池（惰性、单连接、打开失败降级主池并计数）；O_EXCL destination 预留/归属权清理；容量预检；复制后行数+字节一致性校验；`user_version` 复制；`SessionSnapshotStats` 计数与结构化日志（G1–G4/G6/G7） |
| `internal/chat/sqlite_storage.go` | `SnapshotSession`/`Snapshot` 重写为「专用池 + 全程 deadline + 归属权 + 校验」；`CloseStorage` 先关快照池，checkpoint 读取 `(busy,log,checkpointed)`，busy 时降级 PASSIVE 并计数（D7/R6）；快照路径不再使用 `context.Background()` 清理 |
| `cmd/aicli/commands/chat_debug_archive.go` | 快照失败默认降级：`session_file` 标记 `unavailable_reason` 且不打包未校验库；`--require-snapshot` 保留严格模式；启动清扫超龄 `.aicli-session-snapshot-*` 目录（D3/D7） |
| 测试 | `sqlite_storage_snapshot_hardening_test.go`（10 例）、`chat_debug_archive_degrade_test.go`（3 例） |

### 8.2 测试清单对照（分册 §4）

| # | 状态 | 说明 |
| --- | --- | --- |
| 1 | ✅（等价替换 + M5 数值） | 数字 P99 阈值在 CI 易抖；CI 用**确定性**测试：hook 暂停在复制阶段 → 并发 `AddMessage` 必须立即完成。数值压测见 M5（§8.4）：4 轮实测写 P99 增量 0.96–5.23ms（≤10ms 达标） |
| 2 | ✅ | 取消/超时 → destination 清理、`Timeouts` 计数 |
| 3 | ✅ | 预置同名文件内容原样保留 + 明确报错 |
| 4 | ✅ | `ErrSessionSnapshotTooLarge`、无残留、计数 |
| 5 | ✅ | `TestSnapshotSessionSchemaParity` 逐列比对三表（迁移门禁） |
| 6 | ✅ | `snapshotAfterCopyHook` 注入丢行 → `ErrSessionSnapshotDrift` + 清理 + 计数 |
| 7 | ✅ | 快照 `user_version` == 主库 |
| 8 | ✅ | 注入非法池 DSN → 降级主池、功能正确、`DegradedToMainPool=1` |
| 9 | ✅ | 快照失败 → 归档继续、manifest.skipped 带 `unavailable_reason`、无未校验库；严格模式仍中止 |
| 10 | ✅ | `go test ./internal/chat/ -run Snapshot`、`go test ./cmd/aicli/commands/ -run "DebugArchive\|TestLocalHost"`、`go vet` |
| 11 | ✅ | 快照读事务持有期间 `CloseStorage` 不阻塞，`checkpoint_blocked` 计数 |
| 12 | ✅ | 超龄 `.aicli-session-snapshot-*` 清扫，新鲜/无关目录保留 |

### 8.3 有意偏离与理由

- **快照池基线用 Exec 逐条 PRAGMA**（而非 DSN `_pragma=`）：与主池 `init` 完全同源；池恒为 `MaxOpenConns(1)/MaxIdleConns(1)` 且随存储关闭，逐连接确定性成立。
- **VACUUM INTO 的 O_EXCL 语义**：SQLite 要求目标不存在，故先 O_EXCL 预留（关闭 TOCTOU 判定）再删除空文件交给 `VACUUM INTO`；归属权凭据仍用于失败清理。
- **`CloseStorage` 不再因 checkpoint 失败返回错误**：TRUNCATE 被跨进程读事务阻塞是预期分支，降级 PASSIVE + 计数（D7/R6）；仅当主库 `Close` 失败才返回错误。
- **严格模式命名**：`--require-snapshot`（文档中“命名待定”落地值）。

### 8.4 遗留

- ~~M5：并发压测给出写延迟 P99 的绝对数值~~ **已于 2026-09-18 达成**：新增门控压测 `TestSnapshotSessionConcurrentWriteLatency`（`AICLI_RUNTIME_SNAPSHOT_LOAD_TEST=1`，4000 条种子消息 ≈2.1MB，快照循环与 400 次写并发）。实测（Windows 16 核，本机，4 轮）：

  | 轮次 | 基线 P99 | 并发 P99 | **ΔP99** | 快照次数/平均耗时 | 快照失败 |
  | --- | --- | --- | --- | --- | --- |
  | 1 | 10.90ms | 13.78ms | **2.88ms** | 8 / 193ms | 0 |
  | 2 | 13.73ms | 14.69ms | **0.96ms** | 8 / 180ms | 0 |
  | 3 | 12.11ms | 16.54ms | **4.42ms** | 8 / 195ms | 0 |
  | 4 | 10.11ms | 15.33ms | **5.23ms** | 9 / 207ms | 0 |

  结论：**写 P99 增量最大 5.23ms，满足 ≤ 基线+10ms 目标**；单次最大延迟在负载段出现 97ms 尖刺（基线也出现 53.8ms），属 OS/磁盘抖动，P99 口径内不构成阻塞。复现：`$env:AICLI_RUNTIME_SNAPSHOT_LOAD_TEST=1; go test ./internal/chat -run TestSnapshotSessionConcurrentWriteLatency -v -timeout 900s`。
- Q2：未引入磁盘剩余空间预检（依赖 `golang.org/x/sys`）；当前以 `SessionSnapshotMaxBytes` + 写失败清理承担，符合分册“可退化”选项。**维持不做的决策（2026-09-18 复核）**：快照体积已由 `SessionSnapshotMaxBytes` 预检钳制、失败路径有 O_EXCL 归属权 + 清理 + 计数（`Failed`/`Timeouts`），加平台相关 syscall 预检的收益（更早失败）小于其可移植性成本；若后续要做，双实现点为 `unix.Statfs` / Windows `GetDiskFreeSpaceEx`。
- Q3 **已答复（2026-09-18）**：归档 manifest 的 `unavailable_reason`（`manifest.skipped[]`）**目前没有 workspace UI 消费点**——对 `frontend/**/*.{ts,tsx,js,jsx,vue}` 全量检索 `unavailable_reason` / `manifest.json` / `debug archive` 零命中；该字段面向外部脚本与问题排查（写入侧见 `chat_debug_archive.go` 的 `manifest.skipped` 分支）。是否在 UI 展示归档降级属产品排期，不在本分册范围。
