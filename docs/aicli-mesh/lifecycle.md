# 生命周期与所有权（档案 / 心跳 / 租约 / 接管 / 停止 / GC）

> 本文是 [../aicli/mesh-cli.md](../aicli/mesh-cli.md) §4 的语义补充：**为什么**节点会显示成
> `live`/`stale`、`owner`/`peer`/`conflict`，以及崩溃之后靠什么对账。
> 设计期完整推导见 [../plan/aicli-mesh-architecture.md](../plan/aicli-mesh-architecture.md) §4。

## 1. 全景图（一个节点从生到死）

```text
进程启动 ──► 写节点档案 + 周期心跳 ──► 会话激活（session.activated）──► 抢会话租约（lease.acquired）
   │                                              │
   │ 正常运行：心跳翻新、busy 翻转、跨进程调用        │ 显式接管：租约易主，自己下次心跳标 orphaned（不被杀）
   ▼                                              ▼
正常退出（node.stopped，档案删除或留 stopped）   崩溃/断电（心跳停更 → 判活失败 → stale）
   │                                              │
   └──────────────► aicli-mesh gc --apply ◄────────┘
                    （只回收「可证已死」的档案/租约/日志；存活进程永不删，R3）
```

网格的任何读取都**不要求进程在线**：`ls` / `show` / `watch` 只读文件系统，
崩溃之后的残留正是它们要展示的内容（`stale`），由 `gc` 对账清除。

## 2. 数据文件职责

| 文件 | 谁写 | 内容 | 谁读 |
|------|------|------|------|
| `nodes/<node_id>.json` | 节点自己（心跳翻新） | pid、端点、令牌、会话、工作区、心跳、状态 | `ls` / `show` / `call` / `spawn` / HTTP 视图 |
| `bindings/<session_id>.json` | 节点（地址变更时） | 上次地址（host/port）与工作区 | `url`、`open` 的端口偏好、Web「上次地址」 |
| `leases/<purpose>-<key>.lock` | 节点（获取/续租/释放） | owner 节点/pid、过期时间、TTL | 归属判定（`owner`/`peer`/`conflict`）、`spawn` 单飞 |
| `journal/<node_id>.ndjson` | 节点自己（只写自己那个文件） | 事件流（下表） | `watch`、`show --events N`、`ls` 的日志尾部 |

红线：网格目录只承载「谁在跑、跑的是谁、地址在哪、最近发生什么」——**不承载会话内容，不承载令牌副本**
（令牌只在活动节点档案里；`ls`/HTTP 视图输出侧只给 `token_hint`，`show` 与 `url --with-token` 是本机披露面）。

## 3. 状态词汇（一张表看懂所有视图）

| 维度 | 取值 | 含义 |
|------|------|------|
| 节点状态 `state` | `live` | 心跳新鲜（默认 TTL 内）且进程在 |
| | `stale` | 进程已退，或心跳过期 |
| | `stopped` | 进程自己声明退出（正常收尾） |
| | `unknown` | 档案不可读 / schema 未知（**从不改写**，宁可展示「看不懂的档案」） |
| 探活 `reachability` | `skipped` | 未开 `--probe`，或探活预算内没答复（**不算 unreachable**） |
| | `ok` / `unreachable` | 仅在 `--probe` 时可能出现 |
| 归属 `ownership` | `owner` | 本节点（`SelfNodeID`）在服务该会话 |
| | `peer` | 其它**活**节点在服务 |
| | `conflict` | **两个活节点**声明同一会话（设计要暴露的状态，不是 bug） |
| | `-`（none） | 无归属（例如纯 TUI 节点、会话已停） |
| 会话状态 `session.state` | `running` | 会话活动、不在轮次中 |
| | `busy` | 正在跑一轮（单飞锁被占） |
| | `idle` | 无活动会话 |
| | `orphaned` | 租约被接管：进程还在跑，但已不拥有它服务的会话 |
| | `unknown` | 档案缺信息 |

## 4. 判活口径（Windows 上的坑）

判活 = **pid 是否还活着** + **心跳是否新鲜**，两者都要看：

- 只看心跳：崩溃后要等 TTL 才转 `stale`（可接受）；
- 只看 `OpenProcess` 是否成功：**Windows 上会误判**——只要还有句柄指向进程对象，PID 就不回收，
  已退出的进程照样「打开成功」。正确口径是读退出码（`GetExitCodeProcess == STILL_ACTIVE`），
  Windows 特例见 [../e2e/mesh-e2e.md](../e2e/mesh-e2e.md) §7 与 `process_alive_windows_test.go`。

## 5. 租约与接管

- **单飞**：同一会话同一时刻只应有一个 owner。两个 `live` 节点同时服务同一会话时，
  视图显示 `conflict`，`doctor` 的 `ownership` 检查报 **problem**；
- **接管（takeover）**：`aicli-mesh open <会话> --takeover`（或 Web 侧带 `takeover`）显式回收租约后拉起新进程。
  旧节点**不会被杀**：它继续运行，下次心跳发现租约易主，把自己的会话段标成 `orphaned` 并提示操作者
  （`aicli-mesh show <会话>`）；
- **租约视图**：`show --json` 的 `leases[]` 额外带 `owner_alive` / `expired`，脚本不必自己判活；
- **回收**：租约过期，或持有者进程已退出且续租时间超过 `--stale-ttl` 时，由 `gc` 回收。

## 6. 停止（治理动作，默认关闭）

| 模式 | 做法 | 收尾 |
|------|------|------|
| 默认 `graceful` | 把 `/exit` 投给目标的 `/web/api/input`，等进程自己消失 | 目标自己保存会话、注销档案、释放租约 |
| `--force` | 直接终止进程（`TerminateProcess` / `SIGKILL`） | **无**——残留档案由 `gc` 按「可证已死」回收 |

四条硬规则：

1. **开关在被停的进程上**：目标必须显式 `--mesh-allow-stop=true`（默认关闭），否则一律
   `refused` + `mesh_stop_not_allowed`（退出码 6）——「谁能停我」由被停者决定；
2. **不自杀**：目标是本进程（或等于调用方）→ `refused` + `mesh_stop_self_refused`；停自己用 `/exit`；
3. **幂等**：目标已不在运行 → `status=stopped` + `code=mesh_stop_already_stopped`，退出码 0，重试安全；
4. **等待预算**：`--wait` 默认 30s；graceful 投递成功但进程没消失 → `timeout`（退出码 3），
   提示加大 `--wait` 或改用 `--force`。

## 7. GC 对账（唯一的删除路径）

不加 `--apply` 只打印计划（dry-run）；**计划在两种模式下由同一段代码生成**，逐项一致。

| 动作 | 触发条件 |
|------|----------|
| `node-record` | 进程已退出（pid 判定）**且**心跳超过 `--stale-ttl` 的陈旧档案；**存活进程永不删**（R3）；不可读/未知 schema 一律跳过 |
| `lease` | ①已过期；②持有者进程已退出且续租时间超过 `--stale-ttl` |
| `journal` | 文件 mtime 超过 `--keep-days`；孤儿日志（无对应节点档案）用同一窗口 |
| `binding` | 仅 `--prune-bindings`：没有任何节点档案引用该会话，且文件超过 `--keep-days` |
| `legacy-dir` | 仅 `--purge-legacy`：`$AICLI_HOME/web-ports`（旧端口档案目录，已被绑定取代）整棵删除 |

删除失败（占用、权限）不中断整轮：失败项进 `errors[]`，`--apply` 结束时退出码 5。

## 8. journal 事件表（`watch` 能看到什么）

| kind | 含义 |
|------|------|
| `node.started` / `node.stopped` | 节点上线 / 正常收尾 |
| `session.activated` / `session.deactivated` | 会话激活 / 退出 |
| `lease.acquired` / `lease.released` / `lease.reclaimed` / `lease.degraded` | 租约获取 / 释放 / 回收（接管）/ 降级 |
| `mesh.call.sent` / `mesh.call.received` / `mesh.call.completed` | 跨进程调用的发送方 / 接收方 / 完成（审计） |
| `mesh.spawn.requested` / `mesh.spawn.completed` | 拉起请求 / 完成 |
| `mesh.stop.requested` / `mesh.stop.completed` | 停止请求 / 完成（调用方与被调方各写一行） |
| `mesh.peer.observed` | 观察到 peer（扇入订阅等） |

`watch` 的语义要点：`--since 0` 回放磁盘上全部（含轮转代 `<node>.ndjson.1`）；
按 `(node_id, seq)` 去重；**不消费半行**（writer 正在追加、还没换行的那一行留到下一次轮询）；
`--once` 只回放后退出（脚本/CI 安全，不会挂住）。
