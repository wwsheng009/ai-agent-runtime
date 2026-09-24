# aicli-mesh 文档

> 适用版本：内置多进程网格（`internal/mesh`）之后的 aicli 构建。
> 本目录是 **aicli-mesh**（一台机器上的多进程网格运维）的按主题说明书；
> 完整参数表与 JSON 契约的**权威参考**是 [../aicli/mesh-cli.md](../aicli/mesh-cli.md)，本目录不复制其表格。

## 1. 它解决什么问题

一台机器上同时跑着多个 aicli 进程（多窗口、多 Agent、多工作区）时，aicli-mesh 负责三件事：

| 能力 | 一句话 | 命令 |
|------|--------|------|
| 发现 | 有哪些节点、谁活着、各自在哪个会话/工作区 | `aicli-mesh ls`（默认不发网络请求） |
| 定位与调用 | 拿地址、跨进程跑 prompt、读屏幕 | `url` / `show` / `send` / `screen` / `call` |
| 生命周期 | 复用或拉起、停止、清理、复盘、体检 | `open` / `stop` / `gc` / `watch` / `doctor` |

两种等价入口（**同一份实现**，参数/输出/退出码完全一致）：

- `aicli-mesh <子命令>` —— 独立二进制（`pwsh scripts/build.ps1 -Tools aicli-mesh` 或 `make aicli-mesh`）；
- `aicli mesh <子命令>` —— `aicli` 的内置别名（只部署单个二进制的场景也能用）。

## 2. 文档地图

| 文档 | 回答什么问题 |
|------|--------------|
| [quickstart.md](./quickstart.md) | 从零到第一次跨进程调用：构建 → 起两个节点 → 发现 → 调用 → 复盘 → 收尾 |
| [spawn-and-binaries.md](./spawn-and-binaries.md) | `open`/`spawn` 拉起的是哪个 aicli？改名部署怎么办？（可执行文件解析专题） |
| [lifecycle.md](./lifecycle.md) | 档案/心跳/租约/接管/停止/GC 对账：一个节点从生到死的全过程 |
| [troubleshooting.md](./troubleshooting.md) | 出错了看哪里：退出码、doctor 检查项、`mesh_*` 错误码、现象表 |
| [../aicli/mesh-cli.md](../aicli/mesh-cli.md) | **权威 CLI 参考**：全部子命令、参数、JSON 契约、退出码 |
| [../aicli/web-remote-api.md](../aicli/web-remote-api.md) §9 | 每节点 HTTP 控制面：`/web/api/mesh/*` + `/web/api/health` |
| [../plan/aicli-mesh-architecture.md](../plan/aicli-mesh-architecture.md) | 设计期方案：命名、目录、数据模型、API、实时性（§7 草稿以 mesh-cli.md 为准） |
| [../plan/aicli-mesh-implementation-plan.md](../plan/aicli-mesh-implementation-plan.md) | 落地计划与偏差记录（D5–D13） |
| [../e2e/mesh-e2e.md](../e2e/mesh-e2e.md) | E2E-DEBUG-03：多进程控制面验收（M1–M12 断言） |

## 3. 快速开始

```powershell
# 0) 准备工具（任选其一）
pwsh scripts/build.ps1 -Tools aicli-mesh      # 独立二进制
# 或直接用内置别名：aicli mesh <子命令>

# 1) 起两个节点（--pprof 开启回环控制面，端口自选）
aicli chat --yolo --pprof
aicli chat --yolo --pprof

# 2) 看网格（毫秒级，不发请求）
aicli-mesh ls

# 3) 定位 + 调用（把 <会话> 换成 ls 里的 SESSION）
aicli-mesh show <会话>
aicli-mesh screen <会话> --tail 20                        # 只读
aicli-mesh send <会话> "只回复两个字：收到" --allow-write   # 写：需要显式允许

# 4) 复盘与收尾
aicli-mesh watch --since 5m
aicli-mesh gc                            # 先看计划（dry-run）
aicli-mesh doctor                        # 体检；problems > 0 → 退出码 5
```

## 4. 六条核心不变量

1. **文件系统即真相**：状态全在网格根目录（`nodes/` `bindings/` `leases/` `journal/`），读命令不要求任何进程在线；
2. **默认只读**：唯一的删除路径是 `gc --apply`，且**存活进程的档案永不删除**（规则 R3）；工具不提供任何隐式杀进程；
3. **fail-closed**：网格根目录解析不到时**不回退**当前目录——读取返回空视图（`ls`/`gc` 退出码 0，不算错误），
   `doctor` 报 1 个问题（退出码 5）；
4. **写操作显式**：跨进程写（`invoke`/`input`/`cancel`/`sessions.resume`）必须 `--allow-write`；
   停止节点必须**目标进程自己**开 `--mesh-allow-stop=true`——「谁能停我」由被停者决定；
5. **令牌单披露**：常规输出只有 `token_hint`（前 4 位 + `…`）；令牌原文只出现在 `url --with-token`
   与 `open`/`spawn` 返回的窗口 URL 里；
6. **同源聚合**：CLI 与 HTTP 消费同一份 `internal/mesh.BuildView`（`aicli-mesh ls --json` 与
   `GET /web/api/mesh/peers` 的节点集合、会话 ID、`base_url` 完全一致），不各写一套口径。

## 5. 术语表

| 术语 | 含义 |
|------|------|
| 节点（node） | 一个注册进网格的 aicli 进程；ID 形如 `node-<pid>-<时间戳>Z` |
| 会话（session） | 节点正在服务的会话；同一时刻只应有一个 owner |
| 绑定（binding） | `bindings/<session_id>.json`：会话 ↔ 上次地址/工作区（取代旧的 `web-ports/`） |
| 租约（lease） | `leases/<purpose>-<key>.lock`：单飞锁与归属凭据 |
| 网格日志（journal） | `journal/<node_id>.ndjson`：每个进程只写自己那一个文件，进程全退也能复盘 |
| 网格根（mesh root） | 解析顺序 `AICLI_MESH_DIR` → `AICLI_HOME/mesh` → `<主目录>/.aicli/mesh` |
| 拉起（spawn） | `open` / `POST /web/api/mesh/spawn`：复用活节点或启动新进程 |
| 接管（takeover） | 显式回收某会话的租约后拉起新节点；旧节点**不会被杀**，下次心跳把自己标 `orphaned` |
| 归属（ownership） | 节点对某会话的身份：`owner`（本节点）/ `peer`（其它活节点）/ `conflict`（双占用）/ `-`（无） |
| 单飞（single-flight） | 同一时刻只允许一个 `invoke` 等待；冲突返回 `busy`（HTTP 409） |

## 6. 目录布局

```text
<网格根>/
├── nodes/<node_id>.json          # 节点档案：pid/端点/令牌/会话/工作区/心跳/状态
├── bindings/<session_id>.json    # 会话绑定：上次地址与工作区（取代 web-ports/）
├── leases/<purpose>-<key>.lock   # 租约：单飞与归属
└── journal/<node_id>.ndjson      # 网格日志：事件流（含轮转代 .ndjson.1）
```

- 网格根由 `AICLI_MESH_DIR`（最高优先级，测试/多套隔离）、`AICLI_HOME/mesh`、`<主目录>/.aicli/mesh` 依次解析；
- 档案里的 `schema_version` 与 CLI `--json` 的 `schema_version` **独立演进**；未知主版本标记 `unknown` 且从不改写。

## 7. 与其它组件的关系

| 组件 | 关系 |
|------|------|
| `internal/mesh` | 唯一实现：CLI（`aicli-mesh` / `aicli mesh`）、HTTP 端点、E2E 全部复用它 |
| 节点内 HTTP 控制面 | `--pprof` / `--web-port` 开启；`/web/api/mesh/self\|peers\|events\|call\|spawn\|stop` 见 web-remote-api.md §9 |
| 微前端（`/web/`） | 会话页的「在新窗口打开」/停止走 spawn/stop；失败文案按 `code` 分支，不解析散文 |
| E2E harness | `scripts/test-aicli-debug-endpoints-e2e-mesh.ps1`（E2E-DEBUG-03，断言基线 13 条） |
