建议把这件事落成一个 **Go 的“Agent Runtime”**，而不是先做“几个 agent 互相聊天”的框架。原因很直接：Claude 的 Messages API 是**无状态**的，你必须自己构造每一轮请求、管理历史消息、执行工具循环；而 Anthropic 公开的 Agent SDK 设计重点恰恰是 **agent loop、hooks、subagents、context 管理**，只是现成 SDK 主要提供在 Python / TypeScript。也就是说，在 Go 里最稳的路线是：**直接用 Messages API，自建 Runtime / ContextManager / Scheduler / Tool Gateway**。([Claude 开发者平台][1])

## 一、先定落地目标

你的系统不要追求“像 Claude Code 的界面”，而要追求下面 4 个系统能力：

1. **长任务不中断**：大工具输出不直接塞进上下文，原文外置，按需召回。
2. **主从协同稳定**：主 agent 只管规划、分派、综合；subagent 独立上下文执行，最后只交结构化报告。
3. **工具接入可治理**：本地工具、MCP 工具、内部 API 全部走同一个网关，统一做权限、审计、压缩和缓存。
4. **上下文可操作**：Context Window 不再是 append-only transcript，而是“热工作集 + 温记忆 + 冷存储”的分层系统。Anthropic 把这类问题称为 **context engineering**：上下文不只是 prompt，而是系统提示、工具、MCP、外部数据和消息历史的整体编排。([Anthropic][2])

---

## 二、推荐的整体框架

先做 **模块化单体**，不要一开始拆微服务。Go 在单进程里就很适合做高并发调度、沙盒执行、SQLite、队列和 HTTP API，调试成本也低。

```text
[HTTP / CLI / IDE]
        |
        v
+------------------------+
| Session API / Gateway  |
+------------------------+
        |
        v
+----------------------------------------------+
| Agent Runtime                                |
|  - Planner / Root Agent Loop                 |
|  - Scheduler / Supervisor                    |
|  - Context Manager                           |
|  - Policy / Hooks                            |
+----------------------------------------------+
        |                    |                    |
        |                    |                    |
        v                    v                    v
+---------------+   +----------------+   +-------------------+
| Model Adapter |   | Tool Broker    |   | Memory / Recall   |
| (Messages API)|   |                |   |                   |
+---------------+   | - Local tools  |   | - SQLite FTS5     |
                    | - MCP Gateway  |   | - checkpoints     |
                    | - Internal APIs|   | - decision ledger |
                    +----------------+   +-------------------+
                             |
                             v
                    +-------------------+
                    | Output Gateway    |
                    | - raw -> artifact |
                    | - reducer         |
                    | - envelope        |
                    +-------------------+
                             |
                             v
                    +-------------------+
                    | Artifact Store    |
                    | - blob/files      |
                    | - metadata        |
                    | - chunk index     |
                    +-------------------+
```

这套分层和 Anthropic 公开的设计意图是一致的：
Messages API 侧由你自己构造 turn 和 tool loop；Agent SDK 侧公开的关键能力是 loop、hooks、subagents、context window 管理；subagents 的价值是**隔离上下文、并行处理、只回最终结果**。([Claude 开发者平台][3])

---

## 三、每个模块该做什么

### 1) Model Adapter

只负责和 Claude API 通信，不掺杂调度逻辑。

职责：

* `messages.create`
* `messages.count_tokens`
* streaming 解析
* 将内部消息结构转换成 Claude 消息结构

这样做的原因是 Messages API 本身无状态，你需要完全掌握“本轮送什么 token 给模型”。Token Count API 还可以在发送前预估消息 token 数，输入结构和消息创建接口一致。([Claude 开发者平台][1])

### 2) Agent Runtime

这是主循环：

1. 取 session 当前状态
2. 让 Context Manager 组装本轮上下文
3. 调模型
4. 解析 `text` / `tool_use` / `stop_reason`
5. 如需工具，交给 Tool Broker
6. 工具原始输出进 Output Gateway
7. 把精简后的 `ToolEnvelope` 放回上下文
8. 继续下一轮直到完成

Claude 官方工具流也是这个模式：模型返回 `tool_use`，你执行工具，再把 `tool_result` 放回新的 `user` 消息继续循环。([Claude 开发者平台][4])

### 3) Context Manager

这是系统核心，不是附属件。

职责：

* 维护 **热上下文**：本轮真正发给模型的 token 集
* 管理 **温记忆**：决策摘要、待办、结论、开放问题
* 管理 **冷存储引用**：artifact、日志、快照、长文档
* 压缩旧历史
* 触发 page-in / recall

Anthropic 明确把 context engineering 定义为：在有限上下文里持续策展最优 token 集，而不仅是写 prompt。([Anthropic][2])

### 4) Tool Broker

统一接所有工具：

* 本地工具：读文件、grep、git、测试、Playwright、内部命令
* MCP Gateway：MCP server tools/resources
* 内部服务：数据库、代码搜索、日志平台、制品仓库

不要让模型直接“看到”真实工具返回；真实返回先经过 Output Gateway。

### 5) Output Gateway

这是你提出的 Context Mode 的真正落地点。

职责：

* 接收工具**原始输出**
* 将原始输出写入 Artifact Store
* 运行**确定性 reducer**
* 产出一个轻量 `ToolEnvelope`
* 决定哪些内容允许进入热上下文

这和 Anthropic 工具文档的能力点是对齐的：工具结果可以在回给 Claude 之前被**拦截、检查、修改、转换**；这对“大量文档搜索结果”一类大输出尤其重要。([Claude 开发者平台][5])

### 6) Scheduler / Supervisor

管理主 agent 与 subagents。

职责：

* 子任务拆分
* 并发控制
* 深度限制
* 成本预算
* 结果汇总
* 重试与失败恢复

Anthropic 对 subagents 的定义是：**独立 agent 实例**，可以隔离上下文、并行执行，并且只把最终结果返回父级。([Claude 开发者平台][6])

### 7) Policy / Hooks

把安全与审计从 prompt 里移到系统层。

可做的点：

* `PreToolUse`：拦截危险操作
* `PostToolUse`：清洗输出、追加元数据
* `SubagentStart/Stop`：生命周期审计
* `SessionStart/End`：加载和写回 checkpoint
* Approval：写文件、发请求、调用内部写接口前要求审批

Anthropic 的 hooks 文档明确支持：阻止危险操作、记录审计、变换输入输出、要求人工批准。([Claude 开发者平台][7])

---

## 四、核心数据对象

建议先把数据模型定下来，后面代码会非常稳。

### Session

```go
type Session struct {
    ID              string
    UserID          string
    RootTaskID      string
    Model           string
    State           string
    CreatedAt       time.Time
    UpdatedAt       time.Time
}
```

### Task

```go
type Task struct {
    ID              string
    SessionID       string
    ParentTaskID    *string
    AgentRole       string   // root / researcher / tester / writer / verifier
    Goal            string
    SuccessCriteria string
    AllowedTools    []string
    Status          string
    BudgetTokens    int
    BudgetUSD       float64
    MaxTurns        int
}
```

### ToolRawResult

```go
type ToolRawResult struct {
    ToolName        string
    Stdout          string
    Stderr          string
    ExitCode        int
    Files           []string
    Metadata        map[string]any
}
```

### ToolEnvelope

这是唯一允许进入主上下文的工具回执格式：

```go
type ToolEnvelope struct {
    Summary         string
    KeyFacts        []string
    Warnings        []string
    ArtifactRefs    []string
    RecallHints     []string
    SuggestedNext   []string
    IsError         bool
}
```

### Artifact / Chunk / MemoryEntry

* `Artifact`: 原始输出、截图、日志、HTML、DOM、测试 JSON
* `ArtifactChunk`: 索引片段，供 FTS5 检索
* `MemoryEntry`: 决策、结论、开放问题、计划、已验证事实

### SubagentReport

```go
type SubagentReport struct {
    TaskID          string
    Status          string
    Summary         string
    Findings        []string
    EvidenceRefs    []string
    OpenQuestions   []string
    NextActions     []string
}
```

---

## 五、两条最重要的数据流

### A. 普通主循环数据流

```text
User Request
 -> Session Load
 -> ContextManager.Build()
 -> CountTokens()
 -> Messages API
 -> tool_use ?
    -> Tool Broker Exec
    -> Output Gateway Reduce
    -> Artifact Store Save Raw
    -> ContextManager.Admit(ToolEnvelope)
 -> final answer / next turn
```

关键点：

* 发模型前先 `count_tokens`
* 工具原始结果**先落盘**
* 回给模型的不是原始结果，而是 `ToolEnvelope`
* 老上下文不是一直 append，而是不断 compact / replace / page-out

Claude 的工具使用 token 成本不仅来自消息本身，还来自 `tools` 参数、`tool_use` 块、`tool_result` 块；而 Tool Search 的官方文档也明确指出，大型多工具场景下，光工具定义就可能先吃掉约 55K tokens。([Claude 开发者平台][4])

### B. 主 agent 指挥 subagent 的数据流

```text
Root Agent
 -> decide to delegate
 -> create TaskPacket[]
 -> Scheduler starts child sessions
 -> each child runs its own loop
 -> child writes artifacts + memory
 -> child returns SubagentReport
 -> parent only ingests report
 -> parent synthesizes / plans next step
```

Anthropic 的公开多代理研究系统采用的是 **orchestrator-worker** 模式：lead agent 负责策略与委派，subagents 并行探索不同方向，再把压缩后的结果交还给 lead agent。官方 subagents 文档也强调：每个 subagent 跑在自己的 fresh conversation 中，中间工具结果不会污染父上下文。([Anthropic][8])

---

## 六、Context Window 管理要怎么落地

这是你系统里最该花精力的部分。

### 1) 做三层上下文，不要一个 transcript 打天下

**热层 Hot Context**：真正送给模型
包含：

* 系统提示
* 当前任务目标
* 最近少量对话
* decision ledger
* open questions
* 最近少量高价值 `ToolEnvelope`
* 当前子任务回执

**温层 Warm Memory**：不总是发给模型
包含：

* 历史决策摘要
* 已验证事实
* 失败尝试
* 计划版本
* 子任务结论摘要

**冷层 Cold Store**：永不直接入模
包含：

* Playwright snapshot
* git log 原文
* 测试 JSON
* HTML / DOM / diff / 长日志
* 大文档全文
* MCP 工具原始响应

### 2) 设计硬性的 Admission Rule

所有工具输出默认都走这条：

```text
raw output
 -> store raw
 -> classify
 -> reduce
 -> envelope
 -> admit? yes/no
```

不是所有输出都值得进 context。Anthropic 明确强调，context 是有限资源，系统提示、工具、MCP、历史消息都在争抢这个窗口。([Anthropic][2])

### 3) 每个工具都配一个“确定性 reducer”

不要额外调用 LLM 来总结工具输出，先做 deterministic reduction。

示例：

* `playwright_snapshot_reducer`

  * 页面标题
  * 可见关键文本 top-N
  * console error
  * network 失败
  * DOM 变更摘要
  * screenshot path

* `git_log_reducer`

  * commit 数
  * 触达目录
  * 高频作者
  * merge/revert 检测
  * 最近 5 个高相关 commit

* `go_test_json_reducer`

  * 失败用例
  * panic 行号
  * 首个 stack
  * 失败包
  * 重现命令

### 4) Page-in 采用“查询式召回”

不要把 artifact 全文回灌。只回：

* 相关片段
* 相关截图引用
* 相关文件路径
* 相关 stack trace

这里就用 SQLite FTS5 做第一层检索，外加 metadata 过滤：

* `session_id`
* `task_id`
* `tool_name`
* `artifact_type`
* `path`
* `timestamp`

### 5) 预算管理建议

建议在 Context Manager 里固定一个预算分配，而不是让 agent 自己决定。

一个实用版本：

* 10%：system / safety / policy
* 15%：current plan / objective / ledger
* 20%：recent dialogue
* 15%：tool definitions
* 25%：retrieved evidence / child reports
* 15%：headroom

再加两条规则：

* 超过 70% 时触发 compact
* 超过 85% 时禁止再注入大块证据，只允许摘要或片段

在工具很多时，用 Tool Search / client-side tool search 动态装载工具，不要全量暴露给模型。官方文档明确说它就是为“几百上千工具”和 context 膨胀设计的，并且支持 client-side 实现。([Claude 开发者平台][9])

---

## 七、主 agent 与 subagent 的协同机制

### 推荐角色分工

不要让所有 agent 什么都干。

* **Root / Planner**

  * 理解用户目标
  * 制定计划
  * 决定是否派子任务
  * 综合最终结果

* **Researcher / Reader**

  * 搜索、读文件、读日志、读网页
  * 只读权限

* **Tester / Verifier**

  * 跑测试、lint、build、benchmark
  * 可执行命令，但不写代码

* **Writer**

  * 唯一允许写文件的 agent
  * 接收已经收敛的修改计划

* **Reviewer**

  * 复核 patch、风险、风格和回归面

### Task Packet 必须结构化

主 agent 发给 subagent 的不是自然语言闲聊，而是任务包：

```json
{
  "goal": "定位 auth 模块回归来源",
  "success_criteria": [
    "给出最可能 commit",
    "给出证据文件或日志",
    "不要修改代码"
  ],
  "allowed_tools": ["grep", "read_file", "git_log", "git_show"],
  "artifact_refs": ["art_001", "art_008"],
  "budget_tokens": 12000,
  "max_turns": 6,
  "output_schema": "SubagentReport.v1"
}
```

Anthropic 在多代理研究文章里专门强调：orchestrator 必须把目标、输出格式、工具/来源范围、任务边界说清楚，否则子代理很容易重复劳动、遗漏信息或互相踩脚。([Anthropic][8])

### 三条硬规则

1. **many readers, single writer**
2. **child-child 不直接对话**，只通过父级或共享 artifact/ledger 协作
3. **父级只消费 child report，不消费 child transcript**

这三条基本能把复杂度压住。

---

## 八、MCP 该怎么接，才能不被 JSON-RPC 直通拖死

MCP 本身是 **host-client-server** 架构，基于 JSON-RPC 的**有状态 session 协议**，并不规定特定的用户交互模型；tools 也是“模型可控”，但协议没有要求“工具结果必须原样塞回模型”。最新规范里还引入了 experimental 的 `tasks`，用于 durable state machine 和 deferred result retrieval。([Model Context Protocol][10])

所以你的 Go 实现要这样接：

### MCP Gateway 的职责

1. 建立 MCP session，做 initialize / capability negotiation
2. 拉取 `list_tools` / `list_resources`
3. 本地建立工具索引
4. 根据 query 做 tool search，只把少量候选工具暴露给模型
5. `call_tool` 原始结果不直接进模型，而是走 Output Gateway
6. 对长任务优先使用 task 模式：先返回任务句柄，再轮询结果
7. 所有 MCP 输出统一落入 Artifact Store

### 最关键的一条

**不要把 MCP client 写成“拿到 JSON-RPC result 就拼成 tool_result 回给模型”。**
那样等于你主动放弃了 Output Gateway 和 Context Manager 的控制权。

---

## 九、建议的 Go 包结构

```text
/cmd/agentd
/internal/api
/internal/runtime
/internal/model/anthropic
/internal/context
/internal/context/compact
/internal/context/recall
/internal/scheduler
/internal/tools
/internal/tools/local
/internal/tools/mcp
/internal/tools/reducers
/internal/artifact
/internal/store/sqlite
/internal/store/blob
/internal/policy
/internal/hooks
/internal/evals
/internal/telemetry
```

### 最重要的几个接口

```go
type ModelClient interface {
    CreateMessage(ctx context.Context, req MessageRequest) (*MessageResponse, error)
    CountTokens(ctx context.Context, req MessageRequest) (int, error)
}

type ContextManager interface {
    BuildRequest(ctx context.Context, s *Session) (MessageRequest, error)
    AdmitToolEnvelope(ctx context.Context, s *Session, env ToolEnvelope) error
    AdmitSubagentReport(ctx context.Context, s *Session, r SubagentReport) error
    Compact(ctx context.Context, s *Session) error
}

type ToolBroker interface {
    Exec(ctx context.Context, tc ToolCall) (ToolRawResult, error)
}

type OutputGateway interface {
    Reduce(ctx context.Context, raw ToolRawResult) (ToolEnvelope, error)
}

type Scheduler interface {
    RunChildren(ctx context.Context, parent *Session, tasks []Task) ([]SubagentReport, error)
}
```

---

## 十、实施步骤

### 第 1 步：先跑通最小 Agent Loop

先不要上 subagent，也不要接 MCP。

先做：

* `messages.create`
* `messages.count_tokens`
* message history 持久化
* 单工具循环
* streaming 输出
* usage / cost / latency 记录

完成标志：

* 一个 session 能完成 “问答 -> 工具调用 -> tool_result -> 最终回答”

这一步对应 Claude API 最原始的 working model：你自己构造每一轮、自己维护状态、自己写 tool loop。([Claude 开发者平台][3])

### 第 2 步：上 Output Gateway 和 Artifact Store

把“原始工具输出直接入上下文”的路径彻底切掉。

先实现 3 个 reducer：

* `git log`
* `go test -json`
* `playwright snapshot`

完成标志：

* 任何工具大输出都先落 artifact
* 回给模型的只有 `ToolEnvelope`

### 第 3 步：上 Context Manager

实现：

* 热/温/冷三层
* token 预算
* compaction
* decision ledger
* FTS5 recall

完成标志：

* 长会话不会因为 transcript 膨胀而只能重启
* 同一 session 能跨多轮继续工作

Anthropic 长任务研究的一个核心做法，就是用 compaction 和清晰 artifacts 在多个 context windows 之间延续任务。([Anthropic][11])

### 第 4 步：接入 Scheduler / Subagents

先只开放 **只读 subagents**。

推荐第一批：

* repo-reader
* test-runner
* log-investigator
* web-researcher

完成标志：

* 父级可并发创建子任务
* 子级独立 session
* 父级只接收 `SubagentReport`

Anthropic 的 subagent 模式核心就是 fresh conversation、并行化和只回最终结果。([Claude 开发者平台][6])

### 第 5 步：加入唯一 Writer

这一步前，不要让多个 agent 改代码。

做法：

* 只有 writer 有 `write/edit/apply_patch`
* 所有 reader/verifier 只交建议和证据
* writer 修改后交 verifier 回归

### 第 6 步：接 MCP Gateway

顺序：

1. lifecycle / initialize
2. tool listing / resource listing
3. 本地索引
4. client-side tool search
5. `call_tool` -> Output Gateway
6. task mode 支持
7. auth / origin / localhost 绑定 / 审计

MCP 生命周期、能力协商和 transport 细节都应该在 gateway 层完成，而不是让 agent 逻辑直接碰 JSON-RPC。([Model Context Protocol][12])

### 第 7 步：补齐安全与评估

上线前至少补齐：

* 危险命令 denylist / allowlist
* 文件路径约束
* 人工审批点
* 子代理并发上限
* session replay
* eval 集
* 每步审计日志

Hooks 非常适合承接这一层：拦截、变换、审计、审批。([Claude 开发者平台][7])

---

## 十一、第一版上线标准

第一版不用很大，但至少要做到这 6 条：

1. 主循环稳定
2. 大工具输出外置
3. FTS5 可检索历史 artifacts
4. 子代理独立上下文并发
5. 单 writer 机制
6. 全链路可回放、可审计、可评估

只要做到这里，你的系统已经不是“聊天机器人 + 工具”，而是一个真正可持续运行的 **Agent Runtime** 了。

## 十二、一句话收束

最稳的落地方式是：

**先做一个 Go 的模块化单体 Runtime：Messages API + Tool Loop + Output Gateway + Context Manager；等这四个稳定后，再加 Scheduler/Subagents，最后再把 MCP 放进统一网关。**

下一步最值得做的是先把 `ModelClient / ContextManager / OutputGateway / Scheduler` 四个接口和 `ToolEnvelope / SubagentReport` 两个数据契约定死。

[1]: https://platform.claude.com/docs/en/build-with-claude/working-with-messages "Using the Messages API - Claude API Docs"
[2]: https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents "Effective context engineering for AI agents \ Anthropic"
[3]: https://platform.claude.com/docs/en/home?utm_source=chatgpt.com "Documentation - Claude API Docs - Anthropic"
[4]: https://platform.claude.com/docs/zh-CN/agents-and-tools/tool-use/overview "Claude 的工具使用 - Claude API Docs"
[5]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/implement-tool-use "How to implement tool use - Claude API Docs"
[6]: https://platform.claude.com/docs/en/agent-sdk/subagents "Subagents in the SDK - Claude API Docs"
[7]: https://platform.claude.com/docs/en/agent-sdk/hooks "Intercept and control agent behavior with hooks - Claude API Docs"
[8]: https://www.anthropic.com/engineering/multi-agent-research-system "How we built our multi-agent research system \ Anthropic"
[9]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool "Tool search tool - Claude API Docs"
[10]: https://modelcontextprotocol.io/specification/2025-11-25/architecture "Architecture - Model Context Protocol"
[11]: https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents "Effective harnesses for long-running agents \ Anthropic"
[12]: https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle "Lifecycle - Model Context Protocol"
