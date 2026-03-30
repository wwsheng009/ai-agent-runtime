可以，下一步不要再扩概念了，直接收敛成一个**能按周落地的实施计划**。

先把边界再钉死一次：Go 侧就按 **Messages API + 自建 Runtime** 来做，因为 Claude 的多轮消息本身是无状态的，需要你每轮自己重发历史并管理上下文；Claude Code 那套公开出来的关键能力其实是 **agent loop、hooks、subagents、context management**，这些理念可以照着实现，但不需要等 Go 版 Agent SDK。工具结果在回给模型前可以被拦截和修改，subagents 的价值是独立上下文和并行处理，Tool Search 的价值是按需加载工具而不是把全部工具定义塞进上下文。([Claude 开发者平台][1])

## 先定第一个可交付版本

我建议把第一版目标定成：

**V0：单代理 + 工具网关 + Context Mode + FTS5 检索**
先不做真正写代码的多代理，也先不接复杂 MCP。目标是先证明三件事：

1. 长任务可以连续跑 30 到 50 轮，不因上下文膨胀而重启。
2. 大工具输出不会直接塞进模型，上下文里只有摘要和引用。
3. 需要时能从 SQLite FTS5 把相关片段 page-in 回来。

做到这三条后，再上 **read-only subagents**。这比一开始就做多代理稳得多，因为 Claude 的工具循环本质上还是“模型决定调用工具，你执行，再把结果送回去”的回合制机制；如果单代理的 tool loop 和 context manager 还没稳，多代理只会把问题放大。([Claude 开发者平台][2])

---

## 6 周落地计划

### 第 1 周：把骨架跑起来

这周只做 4 件事：

* 建仓库和模块边界
* 打通 `messages.create` 和 `count_tokens`
* 做最小 tool loop
* 把 session、message、tool call 记到数据库

这周的目标不是“智能”，而是“可回放”。任何一步失败，都能从数据库重放这一轮的输入、输出、token、耗时。Token Count API 可以在发送前估算消息、工具、文档等输入的 token 数，所以这一步应该一开始就接上。([Claude 开发者平台][3])

这周完成后，你应该能跑这个流程：

```text
用户请求
-> 组装 messages
-> count_tokens
-> 调 model
-> 返回 text 或 tool_use
-> 执行本地工具
-> 把 tool_result 送回 model
-> 得到最终回复
```

验收标准：

* 一轮完整调用可跑通
* 支持 3 个只读工具：`read_file`、`grep_repo`、`run_command_readonly`
* 所有回合可重放

---

### 第 2 周：实现 Output Gateway 和 Artifact Store

这周开始切断“工具原始输出直进上下文”的路径。

要做的事：

* 设计 `ToolRawResult`
* 落盘原始输出到 artifact store
* 为每个工具实现 deterministic reducer
* 生成统一的 `ToolEnvelope`

建议先做 3 个 reducer：

* `git_log_reducer`
* `go_test_json_reducer`
* `playwright_snapshot_reducer`

工具结果回给模型前允许被修改，这正是你实现 Output Gateway 的制度依据。大结果先外置，再只把摘要送入上下文，是现在官方工具使用文档明确支持的模式。([Claude 开发者平台][4])

统一回执格式建议这样定：

```go
type ToolEnvelope struct {
    Summary       string
    KeyFacts      []string
    Warnings      []string
    ArtifactRefs  []string
    RecallHints   []string
    SuggestedNext []string
    IsError       bool
}
```

验收标准：

* 任何超过阈值的工具输出都不直接进入上下文
* 原始输出都可通过 artifact id 找回
* 平均压缩比达到 10x 以上

---

### 第 3 周：做 Context Manager

这一周是系统成败的关键。

你要把上下文拆成三层：

* **Hot**：本轮真正送给模型
* **Warm**：摘要型记忆，不总是发送
* **Cold**：artifact、日志、快照、长文档，只存引用

Anthropic 把 context engineering 定义为对有限上下文做持续策展，而不是单纯堆历史消息；这和你要做的 Hot/Warm/Cold 分层是高度一致的。([Anthropic][5])

这周要落的规则：

* 超过 70% token 预算时触发 compact
* 超过 85% 时停止注入大证据，只允许摘要
* 所有旧历史压成 `decision ledger + open questions + artifact refs`
* page-in 只能走检索式召回，不允许全量回灌

建议热上下文固定预算：

* 10% system/policy
* 15% current plan
* 20% recent dialogue
* 15% tools
* 25% evidence
* 15% headroom

验收标准：

* 单 session 可持续 30+ 轮
* 中途不需要“重开会话”
* 人工检查时，最近决策和待办不会丢

---

### 第 4 周：上 SQLite FTS5 检索和 Page-in

这周把“虚拟内存”类比真正做出来。

实现：

* `artifacts` 表
* `artifact_chunks` FTS5 表
* `recall(query, filters)` 接口
* page-in 策略：只回 top-k 片段，不回全文

建议 chunk metadata 至少包括：

* `session_id`
* `task_id`
* `tool_name`
* `artifact_type`
* `source_path`
* `created_at`

召回流建议固定成：

```text
模型提出需要更多证据
-> recall(query, session/task/tool filters)
-> FTS5 top-k
-> 组装 snippets
-> 再走一次 reducer / budget filter
-> 注入 hot context
```

验收标准：

* 能通过关键字或语义近似词找回日志片段
* top-3 召回片段足以回答大多数追问
* page-in 后 token 增量受控

---

### 第 5 周：上 read-only subagents

这时再引入主从协同。

不要做“几个 agent 在群聊里互相说话”，而要做：

* root agent 负责计划、分派、综合
* child agent 用独立 session 执行子任务
* child 只返回 `SubagentReport`
* parent 不读取 child transcript

subagents 官方文档强调的价值就是隔离上下文、并行运行、专门指令，不让主对话被中间搜索结果污染。([Claude 开发者平台][6])

第一批子代理建议只有这三种：

* `repo-reader`
* `test-runner`
* `log-investigator`

统一任务包：

```go
type TaskPacket struct {
    Goal            string
    SuccessCriteria []string
    AllowedTools    []string
    ArtifactRefs    []string
    MaxTurns        int
    BudgetTokens    int
    OutputSchema    string
}
```

统一回执：

```go
type SubagentReport struct {
    Status        string
    Summary       string
    Findings      []string
    EvidenceRefs  []string
    OpenQuestions []string
    NextActions   []string
}
```

验收标准：

* 3 个子任务可以并发跑
* 每个子代理都是 fresh context
* root 只消费报告也能继续工作

---

### 第 6 周：接 MCP Gateway，但不要直通

MCP 这一步一定放到 gateway 后面，而不是让模型直接“面对” MCP 返回。

原因是现在 MCP 规范是有状态 session 协议，基于 JSON-RPC；tools 是能力之一，tasks 机制目前还是 experimental。协议本身不强制把工具结果原样交给模型，所以真正决定 token 是否失控的，是你的 client/gateway 设计。([Model Context Protocol][7])

这周做：

* MCP lifecycle
* tool/resource listing
* 本地工具索引
* client-side tool search
* `call_tool -> raw result -> output gateway -> envelope`
* task mode 适配

之所以要做 client-side tool search，是因为官方 Tool Search 文档已经明确把它定位成“面对上百上千工具、避免把全部工具定义提前塞进上下文”的机制。([Claude 开发者平台][8])

验收标准：

* MCP 工具定义不会全量注入 prompt
* MCP 工具大输出仍经过 artifact store 和 reducer
* 至少一个 MCP server 能跑通 end-to-end

---

## 现在就该定下来的 8 个接口

别再拖，先把这些接口定死，后面实现会顺很多：

```go
type ModelClient interface {
    CreateMessage(ctx context.Context, req MessageRequest) (*MessageResponse, error)
    CountTokens(ctx context.Context, req MessageRequest) (int, error)
}

type ContextManager interface {
    BuildRequest(ctx context.Context, s *Session) (MessageRequest, error)
    AdmitToolEnvelope(ctx context.Context, s *Session, env ToolEnvelope) error
    AdmitSubagentReport(ctx context.Context, s *Session, rep SubagentReport) error
    Compact(ctx context.Context, s *Session) error
}

type ToolBroker interface {
    Exec(ctx context.Context, call ToolCall) (ToolRawResult, error)
}

type OutputGateway interface {
    Reduce(ctx context.Context, raw ToolRawResult) (ToolEnvelope, error)
}

type ArtifactStore interface {
    PutRaw(ctx context.Context, raw ToolRawResult) ([]string, error)
    Recall(ctx context.Context, q RecallQuery) ([]ArtifactSnippet, error)
}

type Scheduler interface {
    RunChildren(ctx context.Context, parent *Session, tasks []TaskPacket) ([]SubagentReport, error)
}

type PolicyEngine interface {
    AllowTool(ctx context.Context, call ToolCall) error
}

type EventBus interface {
    Emit(ctx context.Context, evt Event) error
}
```

---

## 数据库先这样建

用 SQLite 就够了，第一版不要急着上外部数据库。

核心表：

* `sessions`
* `messages`
* `tasks`
* `tool_calls`
* `artifacts`
* `artifact_chunks`（FTS5）
* `memory_entries`
* `events`

其中 `memory_entries` 最好有 `kind` 字段：

* `decision`
* `fact`
* `open_question`
* `plan`
* `failure`
* `child_summary`

这样 compact 时不是总结成一大段 prose，而是总结成结构化 ledger。

---

## 第一批必须写的 reducer

这是最容易被低估、但最值钱的部分。

### `git_log_reducer`

输出：

* commit 数
* 高频作者
* 触达目录
* 最近可疑 revert/merge
* 与当前问题最相关的 5 个 commit

### `go_test_json_reducer`

输出：

* 失败包
* 失败测试名
* panic/stack 入口
* 首个高价值错误
* 重现命令

### `playwright_snapshot_reducer`

输出：

* 页面标题
* 当前 URL
* console error
* network 失败
* 关键可见文本
* screenshot ref

这三类工具最容易产生超大输出，也是最容易把上下文塞爆的来源。

---

## 先不要做的东西

这几样现在先别做：

* 多个 writer agent 同时改代码
* child 与 child 直接聊天
* 向量数据库
* 微服务拆分
* 自动长期记忆学习
* 一上来就接十几个 MCP server

原因很简单：现在最核心的问题不是“功能不够多”，而是“上下文和回合控制是否稳定”。

---

## 每周验收指标

建议你从第一周就把这 6 个指标打出来：

* 单任务平均轮数
* 上下文利用率
* 工具原始输出总字节数
* 压缩后进入上下文的 token 数
* FTS5 召回命中率
* 子代理并发带来的总耗时下降

我会特别盯两个指标：

1. **压缩比**：`raw bytes / admitted tokens`
2. **可继续率**：任务在不重启 session 的前提下能否持续推进

---

## 明天就能开工的清单

按顺序做，不要并行乱开：

1. 建 repo 骨架和 8 个核心接口
2. 先接 `messages.create` 和 `count_tokens`
3. 跑通单工具 loop
4. 把 `ToolRawResult` 和 `ToolEnvelope` 定下来
5. 落 SQLite 基础表
6. 先实现 `git_log_reducer`
7. 加第一版 `ContextManager.BuildRequest()`
8. 补回放和事件日志

---

## 一句话版本

下一步的正确推进顺序是：

**先做单代理 Runtime，再做 Output Gateway，再做 Context Manager，再做 FTS5 Recall，最后才上 read-only subagents 和 MCP Gateway。**

如果你愿意，我下一条可以直接给你一版 **Go 项目目录 + 核心接口代码骨架 + SQLite 表结构 SQL**。

[1]: https://platform.claude.com/docs/zh-CN/build-with-claude/working-with-messages?utm_source=chatgpt.com "使用 Messages API - Claude API Docs"
[2]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview?utm_source=chatgpt.com "Tool use with Claude - Claude API Docs"
[3]: https://platform.claude.com/docs/en/api/messages/count_tokens?utm_source=chatgpt.com "Count tokens in a Message - Claude API Reference"
[4]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/implement-tool-use?utm_source=chatgpt.com "How to implement tool use - Claude API Docs"
[5]: https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents?utm_source=chatgpt.com "Effective context engineering for AI agents \ Anthropic"
[6]: https://platform.claude.com/docs/en/agent-sdk/subagents?utm_source=chatgpt.com "Subagents in the SDK - Claude API Docs"
[7]: https://modelcontextprotocol.io/specification/draft/basic/utilities/tasks?utm_source=chatgpt.com "Tasks - Model Context Protocol"
[8]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool?utm_source=chatgpt.com "Tool search tool - Claude API Docs"
