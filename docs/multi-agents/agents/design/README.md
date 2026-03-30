# 多代理架构设计文档索引

本文档总结了多代理系统设计过程中形成的重要架构思想与实施路线，帮助快速理解整体设计方向。

## 当前实现状态

**优先阅读**：
- `docs/multi-agents/design/current.md` - 当前架构设计
- `docs/skill_runtime/current_architecture.md` - Skills Runtime 实现

---

## 核心架构思想

### 1. Agent Runtime 而非聊天框架

系统的本质是一个 **Agent Runtime**，而非多个 agent 在公共会话中互聊的框架。

**关键设计**：
- **Supervisor 驱动的 Agent Loop**：主代理通过 `spawn_subagents` 工具将子任务交给调度器
- **子代理独立上下文**：每个子代理有独立的会话、工具白名单、预算
- **摘要回传**：父代理只接收子代理的最终结果摘要，不消费完整 transcript

**参考**：`idea1.md`、`idea3.md`

### 2. Context OS - 上下文操作系统

将 Context Window 管理升级为系统级能力，类似虚拟内存管理。

**三层上下文**：

| 层级 | 说明 | 内容 |
|------|------|------|
| **Hot Context** | 每轮真正发给模型 | system prompt、当前任务、最近对话、decision ledger、必要工具定义 |
| **Warm Memory** | 默认不发，按需注入 | 历史决策、已验证事实、失败尝试、子代理结论 |
| **Cold Store** | 永不直接入模型 | Playwright snapshot、长日志、git log 原文、测试 JSON |

**类比**：
- Context window = working set / L1 活跃工作集
- Artifact store + FTS5 = page file + file system
- Reducer / compactor = log compaction
- Recall / page-in = page fault handler

**参考**：`idea2.md`、`idea5.md`

### 3. Output Gateway - 工具输出网关

所有工具输出不直接进入模型上下文，而是经过 Output Gateway 处理。

**数据流**：
```
raw output → classify → store raw → reduce → envelope → admit/reject
```

**核心组件**：
- **Reducer**：确定性摘要器，不调用 LLM
- **ToolEnvelope**：唯一允许进入上下文的工具回执格式

**第一批 Reducer**：
- `git_log_reducer`：commit 数、高频作者、触达目录、可疑 revert
- `go_test_json_reducer`：失败包、失败测试、panic 入口、stack trace
- `playwright_snapshot_reducer`：URL/title、console error、failed requests、screenshot ref

**参考**：`idea6.md`、`..\..\..\docsArchive\multi-agents\design\idea12.md`

### 4. MCP Gateway - 不要 JSON-RPC 直通

MCP 是 host-client-server 架构，不应让模型直接面对 JSON-RPC 返回。

**设计原则**：
- MCP 放在 Gateway 后面，不直接暴露给模型
- `list_tools` 结果进本地工具索引，不全量进 prompt
- `call_tool` 结果先入 Output Gateway，再 reduce 成 envelope
- 所有 MCP 输出统一落入 Artifact Store

**参考**：`idea2.md`、`idea13.md`

### 5. Tool Search - 按需加载工具

工具数量膨胀时，不应把全部工具定义塞进上下文。

**官方数据**：
- 多 MCP server 场景下，工具定义可能先占约 55K tokens
- Tool Search 常能把这部分降低 85%+
- 只加载 3-5 个相关工具

**实现**：
- 本地维护 `tool_catalog` 索引
- 用 task goal 驱动工具搜索
- 第一版可用 BM25 / SQLite LIKE

**参考**：`idea3.md`、`idea11.md`

### 6. 主代理与子代理协同

**角色分工**：
- **Root/Planner**：理解目标、制定计划、决定是否派子任务、综合结果
- **Researcher/Reader**：搜索、读文件、读日志、只读权限
- **Tester/Verifier**：跑测试、lint、build、不写代码
- **Writer**：唯一允许写文件的 agent

**协同硬规则**：
1. many readers, single writer
2. child-child 不直接通信
3. 父级只消费 `SubagentReport`，不消费 child transcript

**参考**：`idea1.md`、`idea3.md`、`idea5.md`

---

## 实施路线

与 `docs/multi-agents/plan/roadmap.md` 对齐，按 P0-P3 分阶段推进并标注当前状态。

| Phase | 目标 | 当前状态 | 关键交付 |
| --- | --- | --- | --- |
| P0 单代理 Runtime | 单代理可跑通 + 工具网关 + Context Mode | Partial | MessageBuilder；ToolRawResult → Artifact Store → Reducer → ToolEnvelope；SQLite/FTS5 基础表 |
| P1 Context OS | 三层上下文 + 预算 + admission + compaction + recall | Partial | Hot/Warm/Cold；Compaction（非 LLM 版）；Recall/Page-in |
| P2 子代理与 MCP | 子代理协同 + MCP Gateway + Tool Catalog | Partial | 子代理调度器；子代理任务包/回执协议；MCP Gateway/Host 化；Tool Catalog + 搜索 |
| P3 规模化治理 | 安全边界与规模化运维 | Partial | 单写者策略；Sandbox/只读执行；Policy/Hooks/EventBus；跨 agent 观测 |

---

## 核心接口定义

```go
// 模型客户端
type ModelClient interface {
    CountTokens(ctx context.Context, req MessageRequest) (int, error)
    CreateMessage(ctx context.Context, req MessageRequest) (*MessageResponse, error)
}

// 上下文管理器
type ContextManager interface {
    BuildRequest(ctx context.Context, s *Session) (MessageRequest, error)
    AdmitToolEnvelope(ctx context.Context, s *Session, env ToolEnvelope) error
    AdmitSubagentReport(ctx context.Context, s *Session, rep SubagentReport) error
    Compact(ctx context.Context, s *Session, reason string) error
}

// 工具调用结果摘要
type ToolEnvelope struct {
    Summary       string
    KeyFacts      []string
    Warnings      []string
    ArtifactRefs  []ArtifactRef
    RecallHints   []string
    SuggestedNext []string
    IsError       bool
}

// 子代理报告
type SubagentReport struct {
    TaskName      string
    Status        string
    Summary       string
    Findings      []string
    EvidenceRefs  []ArtifactRef
    OpenQuestions []string
    SuggestedNext []string
    CostTokens    int
    DurationMs    int64
}
```

**参考**：`idea7.md`、`idea8.md`

---

## 文档索引

| 文档 | 主要内容 |
|------|----------|
| `idea1.md` | Supervisor 驱动的 agent loop、子代理独立上下文、Go 骨架代码 |
| `idea2.md` | Context OS 概念、三层上下文、MCP Gateway 设计 |
| `idea3.md` | Agent Runtime 详细设计、分模块职责、数据流 |
| `idea4.md` | 6 周落地计划、P0/P1 阶段划分、核心接口 |
| `idea5.md` | Context Manager 详细设计、预算策略、Compaction |
| `idea6.md` | Output Gateway、Reducer 接口、首批 reducer 实现 |
| `idea7.md` | 详细实施蓝图、Go 项目结构、SQLite schema |
| `idea8.md` | 首批可实现文件：types.go、dto.go、mapper.go、budget.go |
| `idea9.md` | Runner 完整版、messages.go、memory.go、recall.go、child_runner.go |
| `idea10.md` | 启动层：main.go、server.go、handlers.go、anthropic client |
| `idea11.md` | run_command_readonly、MCP gateway、tool_catalog、compact 增强 |
| `..\..\..\docsArchive\multi-agents\design\idea12.md` | P1 关键实现：compact.go、go_test_json reducer、playwright reducer、MCP client |
| `idea13.md` | MCP 安全边界、Tool Search、roots 机制 |

---

## 关键引用来源

- Anthropic: [Effective context engineering for AI agents](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- Anthropic: [How we built our multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system)
- Claude API: [How the agent loop works](https://platform.claude.com/docs/en/agent-sdk/agent-loop)
- Claude API: [Subagents in the SDK](https://platform.claude.com/docs/en/agent-sdk/subagents)
- Claude API: [Tool search tool](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool)
- Model Context Protocol: [Architecture overview](https://modelcontextprotocol.io/docs/learn/architecture)

---

## 总结

本系列文档的核心主张：

> **在 Go 里实现"像 Claude Code 的 multi-agent"，最稳的方式是：Anthropic 官方 Go SDK 负责单代理的 Messages / tool loop；你自己的 Supervisor 负责 `spawn_subagents`、上下文隔离、预算控制、并发调度和安全沙箱。**

先把它做成 **"主代理 + 特殊 spawn 工具 + 独立子代理 fresh sessions + 只回传摘要"**，你就已经抓住最核心的机制了。
