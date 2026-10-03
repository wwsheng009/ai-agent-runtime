# TUI 回合延迟根因：工具面把一次知识库全表聚合放大成 56 次

- 日期：2026-10-03
- 状态：已定位，修复见 [knowledge-read-path-optimization-20261003.md](./knowledge-read-path-optimization-20261003.md)
- 观测会话：`session_20261003104638_AjTcRXUV`（进程 `aicli-2x.exe`，构建于 10:46:31，**未含**本轮修复）
- 观测环境：Windows / 14 GB 内存；工作区 `E:\projects\ai\ai-agent-runtime`
- 知识库：`.aicli/knowledge/knowledge.db`，355 MiB，files 5,277 / symbols 56,855 / refs 507,557

## 1. 现象与口径

用户的原始描述是"从请求记录看网络响应非常快，但是从 aicli tui 上用户输入到输出响应非常慢"。
两句话都可以量化，而且必须分开量：

| 口径 | 来源 | 实测 |
|---|---|---|
| LLM 网络耗时 | `GET /web/api/cache/overview` | 4 次请求，596–2,393 ms，首 token 412–1,888 ms，缓存命中 99.4% |
| 回合总时长 | `agent.turn.finished` 的 `elapsed_ms` | **153,927 ms / 374,353 ms** |
| 输入 → 首次 LLM 请求 | `events/runtime-events.jsonl` 时间戳差 | **144 s / 133 s** |
| 第三回合 | 同上 | 163 s 内**没有发出任何 LLM 请求**，被用户中断 |

即：回合墙钟时间的 **98.1% / 99.4%** 不在网络上（153.927 s 里 LLM 只占 2.989 s；
374.353 s 里只占 2.336 s），而在"模型调用之前"和"两次模型调用之间"。
用户的感知是准确的，只是"慢"的位置不在他以为的地方。

### 时间线（本地时间，Asia/Shanghai）

```
turn_55aa1ad2  10:47:44 turn.started
               10:50:08 llm.request.started      ← 空白 144 s
               10:50:10 llm.request.finished     (2.39 s)
               10:50:14 tool.requested → 10:50:17 completed (0.85 s)
               10:50:17 llm.request.started (step 2) → 10:50:18 finished (0.60 s)
               10:50:18 turn.finished elapsed_ms=153927

turn_c0dcbbff  10:50:44 user_submitted / 10:50:46 turn.started
               10:52:59 llm.request.started      ← 空白 133 s
               10:53:01 llm.request.finished     (2.31 s)
               10:53:05 tool.requested ×2 → completed (1 ms / 22 ms，工具本身是瞬时的)
               10:53:48 用户中断；随后该回合的 step 2 请求在 10:57:01 才发出并立即取消
               turn.finished elapsed_ms=374353

turn_8e7a226d  10:54:38 turn.started → 10:57:21 中断，全程无 llm.request.started
```

注意 10:53:05 那两条 `tool.completed`：工具执行只要 1 ms 和 22 ms。
**空白不在工具里，也不在模型里。**

## 2. 证据链

### 2.1 CPU profile：90.7% 的采样落在同一条栈上

抓取方式（20 s 窗口，抓的时候回合正在跑）：

```powershell
curl.exe -s -o cpu.pb.gz "http://127.0.0.1:62764/debug/pprof/profile?seconds=20"
go tool pprof -top -cum cpu.pb.gz
```

结果：`Duration: 20s, Total samples = 21.27s (106.35%)` —— 单个进程把**一个核打满**。
其中 19.30 s（90.7%）集中在一条调用栈：

```
ReActLoop.runLoop
 └─ ReActLoop.run → think → resolveAvailableTools → computeAvailableTools
     └─ tools.(*Manager).ListTools
         └─ tools.(*codeToolGate).sync → evaluate
             └─ tools.newCodeIndexResolverWithShared.func1
                 └─ knowledge.(*sqliteStore).Stats
                     └─ database/sql.(*Rows).Next
                         └─ go-sqlite3-wasm _sqlite3VdbeExec
                             └─ _sqlite3BtreeTableMoveto / _sqlite3BtreeNext
                                 └─ vfsRead → os.File.ReadAt → pread
```

同一份 profile 的 `flat` 视图：`runtime.cgocall 14.44s (67.89%)`（Windows 上 `pread`
走的系统调用）、`syscall.setFilePointerEx 2.76s`。
即成本实质是**通过页读取做全表扫描**。

### 2.2 空转对照：不是后台死循环

同一进程空转时抓 30 s profile，只有 `1.53 s` 采样（≈5% CPU），且热点是 mesh 的
peer sync 与输入轮询。**说明这条重活只在请求路径上被触发**，不是常驻循环。

### 2.3 探针定量：一次 `Stats` 到底多贵

用生产同款驱动（`internal/sqlitedriver`，纯 Go WASM 引擎）只读打开真实库：

```
OpenStore(ro)       4 ms
FindWorkspace       1 ms
ListActiveFiles    23 ms   (5,134 文件)
Stats #1        2,554 ms
Stats #2        2,524 ms   ← 可复现，不是冷缓存
```

子查询拆解（同样是生产驱动）：

| 子查询 | 耗时 | 说明 |
|---|---|---|
| files COUNT | 2 ms | 走 `idx_files_ws_deleted` |
| symbols COUNT | 376 ms | 走 `idx_symbols_qualified(workspace_id, qualified_name)`，索引项带文本列，跨页多 |
| **refs JOIN files COUNT** | **2,027 ms** | 占绝对多数 |
| index_jobs MAX | 5 ms | |
| 对照：refs COUNT 不 JOIN | 197 ms | 说明 JOIN 是主要成本 |

查询计划：

```
SEARCH r USING INDEX idx_refs_from (workspace_id=?)
SEARCH f USING INDEX sqlite_autoindex_files_1 (id=?)
```

即 SQLite 自选的顺序是"扫 50 万行 refs，每行回探一次 files 主键"。

**原生 sqlite3 CLI 跑同一条 SQL 是 1,761–1,967 ms。** 也就是说纯 Go 引擎只比原生
慢约 1.35 倍 —— **"WASM 引擎慢"不是本次的主因，重复执行才是**。（这一点很重要：
它把优化方向从"换驱动"扳回了"别重复跑"。）

## 3. 放大链（代码级根因）

`computeAvailableTools` 的结构（`backend/internal/agent/loop.go`）：

```go
for _, mt := range loop.agent.mcpManager.ListTools() {   // 85 个工具
    ...
    if source := resolveToolSourceForRequest(loop.agent, mt.Name); source != "" {
        definition.Metadata[toolresult.SourceKey] = source
    }
}
```

`resolveToolSourceForRequest`（`backend/internal/agent/loop.go:3928`）的归类顺序是：

1. `list_mcp_resources` → 直接返回 `meta`；
2. broker 工具 → 直接返回 `broker`；
3. **`agent.mcpManager.(toolSourceResolver)` 类型断言** → 走便宜路径；
4. 兜底：`agent.mcpManager.FindTool(toolName)`。

关键在于第 3 步失败：当时的 `agent.mcpManager` 是
`cmd/aicli/commands.(*skillToolSurface)`（由
`chat_actor_host.go` 的 `wrapSkillToolSurface(session, runtimeMCP)` 包在最外层），
而 **`skillToolSurface` 没有实现 `ResolveToolSource`** —— 类型断言必然失败，
于是 85 个工具逐个落到第 4 步。

而 `FindTool` 的兜底代价极高（`backend/internal/tools/agent_adapter.go:59`）：

```go
func (a *AgentAdapter) FindTool(toolName string) (skill.ToolInfo, error) {
    ...
    for _, info := range a.ListTools() {   // 为了找一个工具，重建整张工具表
```

`a.ListTools()` → `tools.(*Manager).ListTools()`（`manager.go:114`）→ 首行就是
`m.codeGate.sync(context.Background())` → 解析器闭包 → `knowledge.Stats`。

于是每个回合的工具面构建成本是：

```
(1 次 ListTools 自身 + 56 次 FindTool 兜底) × ~2.4 s ≈ 137 s
```

其中 56 = 85 − 27（broker）− 1（`list_mcp_resources`）− 1（`ListTools` 自身已计）。
与实测的 133 s / 144 s 空白吻合。

`fullCatalogForSearch`（`backend/internal/agent/tool_list.go:150`，`search_tool`
执行路径）是**同样的 1+N 结构**，所以模型一调 `search_tool` 会再付一遍。

### 3.1 并发时会互相放大

第三回合（10:54:38 起）与前一个回合的 step 2 同时在跑工具面构建：两者抢同一个核
（profile 显示单核 106%），于是前者的 step 2 从 ~140 s 拖到 **240 s**，后者在
163 s 内连第一次 LLM 请求都没发出。这解释了为什么"越用越卡"。

## 4. 排除掉的可能

| 假设 | 证据 | 结论 |
|---|---|---|
| 网络/供应商慢 | LLM 请求 0.6–2.4 s，缓存命中 99.4% | 排除 |
| TUI 渲染慢 | 空转 profile 仅 1.53 s/30 s，热点是输入轮询与 mesh | 排除为主因 |
| 工具执行慢 | `tool.completed duration_ms` = 1 / 22 / 848 ms | 排除 |
| 纯 Go SQLite 引擎太慢 | 原生 CLI 同查询也要 1.8 s，仅差 1.35× | 排除为主因 |
| 后台循环烧 CPU | 空转 profile 无热点循环 | 排除 |
| **工具面构建次数与工具数成正比** | profile 90.7% 在 `computeAvailableTools → Stats`；算式与实测空白吻合 | **确认** |

## 5. 定位结论

一句话：**用户输入后，agent 先把工具面构建跑了 57 遍，每一遍都要在 355 MiB 的
知识库上做一次 50 万行的聚合；模型还没被调用，两分钟已经过去了。**

修复方向因此有三层，按收益排序：

1. 断开放大链（1+N → 1）—— 直接影响最大；
2. 让"每次解析"只取真正需要的那个标量（`IndexedAt`），而不是四个聚合；
3. 让 `Stats` 本身变便宜（join 顺序 + 覆盖索引）。

三层都做完了，实施与实测见
[knowledge-read-path-optimization-20261003.md](./knowledge-read-path-optimization-20261003.md)。
