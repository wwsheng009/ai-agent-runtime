# aicli exec 子命令增强方案

> 参考 codex exec 的架构设计，为 aicli 增加专用的非交互式执行子命令

## 1. 背景与动机

### 1.1 现状分析

当前 aicli 已具备的非交互能力：

| 能力 | 实现位置 | 状态 |
|------|----------|------|
| `--no-interactive` 标志 | `chat` 命令 | ✅ 已有 |
| `pipe` 子命令 | `commands/pipe.go` | ✅ 已有（单轮 stdin→LLM） |
| `test` 子命令 | `commands/test.go` | ✅ 已有（端点测试） |
| JSON 输出 | `--output json` | ✅ 已有 |
| 会话恢复 | `/resume` 斜杠命令 + `--session` | ✅ 已有（仅交互模式） |
| 工具执行 | actor/executor 框架 | ✅ 已有 |
| 审批策略 | `--permission-mode` / `--yolo` | ✅ 已有 |
| Skills 集成 | skills runtime binding | ✅ 已有 |
| MCP 集成 | MCP manager | ✅ 已有 |

### 1.2 与 codex exec 的差距

| codex exec 能力 | aicli 现状 | 差距说明 |
|-----------------|------------|----------|
| 专用 `exec` 子命令入口 | 分散在 `chat --no-interactive` / `pipe` | 缺乏统一的 headless 入口 |
| JSONL 事件流输出 | 仅有最终结果 JSON | 缺少过程事件流（工具调用、进度等） |
| `review` 子命令 | 无 | 缺少代码审查专用命令 |
| `resume` CLI 子命令 | 仅交互模式内 `/resume` | CLI 层面无法直接恢复会话 |
| 输出 Schema 校验 | 无 | 无法约束模型输出格式 |
| 事件处理器模式 | 硬编码输出逻辑 | 缺少输出抽象层 |
| `--output-last-message` | 无 | 无法将最后消息写入文件 |
| 配置覆盖 `-c key=val` | root 已使用 `-c/--config` | 需要提供不冲突的细粒度配置覆盖入口（首版用 `-C/--config-override`） |

## 2. 设计目标

1. **统一 headless 入口**：`aicli exec` 作为非交互执行的标准入口
2. **JSONL 事件流**：`--json` 模式输出结构化过程事件
3. **会话恢复**：`aicli exec resume` CLI 层面恢复历史会话
4. **代码审查**：`aicli exec review` 专用审查命令
5. **输出控制**：`--output-last-message` 将最终消息写入文件
6. **配置覆盖**：`--config-override/-C key=value` 细粒度配置覆盖；由于 root 命令已使用 `-c/--config` 表示配置文件路径，首版不复用 `-c`
7. **输出 Schema 校验**：支持 `--output-schema` 约束最终 assistant 消息，失败时返回结构化错误
8. **向后兼容**：不破坏现有 `chat --no-interactive` 和 `pipe` 行为

## 3. 架构设计

### 3.1 命令结构

```
aicli exec [OPTIONS] [PROMPT]           # 非交互执行
aicli exec [OPTIONS] <COMMAND> [ARGS]   # 子命令
  ├── resume [SESSION_ID] [--last]     # 恢复会话
  └── review [--uncommitted|--base|--commit]  # 代码审查
```

### 3.1.1 与现有代码的集成点

exec 命令复用现有 chat 基础设施，关键集成点：

| 现有模块 | 文件 | exec 复用方式 |
|----------|------|---------------|
| `chatCommandOptions` | `chat_options.go` | exec 构造等价的 opts 传入 |
| `prepareChatPersistence()` | `chat_bootstrap.go` | 直接调用，获取 SessionManager |
| `prepareChatRuntimeState()` | `chat_bootstrap.go` | 直接调用，解析 provider/model |
| `bootstrapChatSession()` | `chat_setup.go` | 调用内部的 `buildChatSession()` |
| `chatRuntimeEventBridge` | `chat_runtime_events.go` | exec 注册自己的事件监听器或复用其事件处理点 |
| `aicliChatExecutor` | `chat_core.go` | 复用 actor/shared executor |
| `runtimepolicy.Mode` | `internal/policy/modes.go` | 类型复用 |
| `ui.Theme` | `cmd/aicli/ui/theme.go` | 人类输出使用 Theme 颜色 |

### 3.2 模块架构

```
┌─────────────────────────────────────────────────────────┐
│                    aicli exec 入口                       │
│              cmd/aicli/commands/exec.go                  │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  │
│  │  exec.go     │  │ exec_resume  │  │ exec_review  │  │
│  │  (主命令)     │  │  .go         │  │  .go         │  │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘  │
│         │                 │                 │           │
│         ▼                 ▼                 ▼           │
│  ┌─────────────────────────────────────────────────┐   │
│  │           exec_event_processor.go               │   │
│  │        (事件处理器抽象层)                         │   │
│  ├─────────────────────────────────────────────────┤   │
│  │  ┌─────────────────┐  ┌─────────────────────┐  │   │
│  │  │ exec_event_     │  │ exec_event_         │  │   │
│  │  │ human.go        │  │ jsonl.go            │  │   │
│  │  │ (人类可读输出)   │  │ (JSONL 事件流)      │  │   │
│  │  └─────────────────┘  └─────────────────────┘  │   │
│  └─────────────────────────────────────────────────┘   │
│                          ▲                              │
│                          │ (事件桥接)                    │
│  ┌─────────────────────────────────────────────────┐   │
│  │           exec_event_bridge.go                  │   │
│  │   (将 actor/executor 运行时事件转为 exec 事件)    │   │
│  └──────────────────────┬──────────────────────────┘   │
│                          │                              │
│                          ▼                              │
│  ┌─────────────────────────────────────────────────┐   │
│  │     ChatSession + aicliChatExecutor             │   │
│  │   (复用现有会话和工具执行框架)                    │   │
│  │   chat_core.go / chat_actor_executor.go         │   │
│  └─────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────┘
```

### 3.3 JSONL 事件模型

参考 codex exec 的 `ThreadEvent` 设计，完整类型定义见 [4.5 exec_events.go](#45-exec_eventsgo)。

事件流概览：

```
thread.started → turn.started → [item.started → item.updated* → item.completed]* → turn.completed
                                                                                  → turn.failed (异常)
```

事件类型：
- `thread.started`: 线程启动，包含 model/provider/session 信息
- `turn.started/completed/failed`: 一次完整的 LLM 交互轮次
- `item.started/updated/completed`: 轮次内的具体操作（工具调用、消息、文件变更）
- `error`: 全局错误

### 3.4 CLI 兼容面

`aicli exec` 的目标不是替代 `pipe` 的轻量 HTTP 调用路径，而是成为 `chat --no-interactive` 的标准 headless 入口。因此首版必须覆盖现有非交互 chat 的关键能力：

| 能力 | exec flag | 对应现有能力 | 首版要求 |
|------|-----------|--------------|----------|
| profile/agent | `--profile`, `--agent` | `chat --profile/--agent` | 必须支持，避免绕过 profile prompt、tool policy、runtime config |
| provider/model | `--provider`, `--model` | `chat --provider/--model` | 必须支持，并复用 model mapping |
| stream 偏好 | `--stream` | `chat --stream` | 默认使用配置/会话偏好；JSON/JSONL 模式禁止直接文本流混入 |
| reasoning | `--reasoning-effort` | `chat --reasoning-effort` | 必须支持，复用 `resolveChatReasoningChoice` |
| tools/skills | `--disable-tools`, `--skills-dir`, `--skills-top-k`, `--skills-mode`, `--skills-debug` | chat 同名 flag | 必须支持，保持 headless 与 chat 行为一致 |
| approval | `--permission-mode`, `--approval-reuse`, `--yolo` | chat 同名 flag | 必须支持；headless 下默认拒绝需要交互输入的审批，除非策略允许 |
| session | `--session-dir`, `--title`, `--ephemeral` | chat 会话持久化 | 必须定义清楚是否创建 runtime session |
| timeout/retry | `--request-timeout`, `--timeout`, `--fail-fast`, `--debug-http` | chat 请求超时和重试 | 必须支持；`--timeout` 是整次 exec wall-clock 上限 |
| attachments | `--image` | chat 图片附件 | 必须支持 |

### 3.5 输出契约

输出必须固定，方便 CI 和脚本消费：

| 模式 | stdout | stderr | 说明 |
|------|--------|--------|------|
| 默认 text | 最终 assistant 文本 | 配置摘要、工具进度、warning、debug | 与 `chat --no-interactive` 保持接近 |
| `--output json` | 单个 JSON 对象 | warning/debug | 输出最终结果 envelope，不输出过程事件 |
| `--json` | JSONL 事件流 | warning/debug 仅在无法序列化为事件时使用 | 每行必须是一个完整 JSON；不得混入人类可读文本 |
| `--output-last-message <file>` | 不改变 stdout | 写文件失败也必须返回 warning/error | human/JSON/JSONL 三种模式都必须写入最终 assistant 文本 |

`--json` 与 `--output json` 不等价：前者是过程事件流，后者是最终结果 JSON。若两者同时出现，命令应报参数冲突，避免脚本误判。

### 3.6 会话持久化语义

- 默认 `aicli exec` 创建或恢复 runtime session，并按现有 chat 机制同步历史。
- `--ephemeral` 必须跳过 runtime session 创建和写入；允许保留进程内日志或临时 artifact，但不能在默认 session 目录落持久会话文件。
- `aicli exec resume` 不支持 `--ephemeral`，因为 resume 的语义依赖持久化会话。
- `aicli exec review` 默认 `--ephemeral=true`，除非用户显式传入 `--persist-review-session`（首版可不实现该 flag，只需记录未来扩展）。

### 3.7 错误与退出码

| 场景 | exit code | JSONL 事件 | JSON 输出 |
|------|-----------|------------|-----------|
| 参数/配置错误 | 2 | `error` | envelope error |
| Provider/API/工具执行失败 | 1 | `turn.failed` + `error` | envelope error |
| 超时 | 124 | `turn.failed`，`code=TIMEOUT` | envelope error |
| 用户中断 | 130 | `turn.completed`，`status=interrupted` | envelope error |
| Schema 校验失败 | 3 | `turn.failed`，`code=SCHEMA_VALIDATION_FAILED` | envelope error |

Go 端实现可先统一返回 error，由命令入口集中转换退出码；但文档和测试必须固定上述契约。

## 4. 实施计划

### Phase 1: 核心 exec 子命令框架

**目标**：建立 `aicli exec` 基础框架，支持基本的非交互执行

#### 4.1 新增文件

```
backend/cmd/aicli/commands/
├── exec.go                      # exec 主命令定义和入口
├── exec_common_flags.go         # exec / resume / review 共享 flag 定义
├── exec_options.go              # exec 命令选项结构
├── exec_run.go                  # exec 核心执行逻辑
├── exec_event_processor.go      # 事件处理器接口定义
├── exec_event_bridge.go         # 运行时/chatcore 事件到 exec 事件的桥接层
├── exec_event_human.go          # 人类可读事件处理器
├── exec_event_jsonl.go          # JSONL 事件处理器
├── exec_events.go               # 事件类型定义
├── exec_output_schema.go        # 最终消息 schema 校验
├── exec_resume.go               # resume 子命令
├── exec_review.go               # review 子命令
├── exec_config_override.go      # -C/--config-override key=value 配置覆盖解析
└── exec_test.go                 # 测试
```

#### 4.2 exec.go 核心实现

```go
// exec.go

package commands

import (
    "github.com/spf13/cobra"
    config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// NewExecCommand 创建 exec 子命令
func NewExecCommand(getCfg func() *config.Config) *cobra.Command {
    cmd := &cobra.Command{
        Use:   "exec [OPTIONS] [PROMPT]",
        Short: "非交互式执行 Codex 代理",
        Long: `以非交互模式运行 AI 代理，适用于 CI/CD、脚本集成等场景。

支持两种用法：
  aicli exec [OPTIONS] [PROMPT]        # 发送提示并获取结果
  aicli exec [OPTIONS] <COMMAND> [ARGS] # 子命令（resume/review）`,
        Example: `  # 基本用法
  aicli exec "解释这段代码的作用"
  
  # 管道输入
  cat main.go | aicli exec -p "分析代码质量"
  
  # JSONL 事件流
  aicli exec --json "创建一个 Hello World 程序"
  
  # 恢复会话
  aicli exec resume --last
  aicli exec resume <session-id>
  
  # 代码审查
  aicli exec review --uncommitted
  aicli exec review --base main`,
        RunE: func(cmd *cobra.Command, args []string) error {
            return runExec(cmd, getCfg(), args)
        },
    }

    // 注册子命令
    cmd.AddCommand(newExecResumeCommand(getCfg))
    cmd.AddCommand(newExecReviewCommand(getCfg))

    // 注册 flags
    registerExecFlags(cmd)

    return cmd
}

func registerExecFlags(cmd *cobra.Command) {
    registerExecSharedFlags(cmd, nil)
}

func registerExecSharedFlags(cmd *cobra.Command, exclude map[string]bool) {
    flags := cmd.Flags()
    has := func(name string) bool { return exclude != nil && exclude[name] }

    // 基本选项
    flags.String("profile", "", "profile 名称或目录路径")
    flags.String("agent", "", "profile 内 agent 标识")
    flags.StringP("model", "m", "", "指定模型名称")
    flags.StringP("provider", "P", "", "指定 provider 名称")
    flags.IntP("max-tokens", "t", 0, "最大输出 tokens（0=使用模型默认值）")
    if !has("prompt") { flags.StringP("prompt", "p", "", "提示词（可与 stdin 组合使用）") }
    flags.Bool("stream", false, "使用流式模型输出（human text 模式可见；JSON/JSONL 模式不直接混入文本）")
    flags.String("reasoning-effort", "", "当前模型支持的 reasoning_effort 值")

    // 输出控制
    flags.Bool("json", false, "以 JSONL 事件流格式输出")
    flags.String("output", "", "输出格式（text|json）")
    flags.StringP("output-last-message", "o", "", "将最后消息写入文件")
    flags.String("output-schema", "", "最终 assistant 消息的 JSON Schema 文件路径或内联 JSON")
    flags.Bool("envelope", false, "JSON 输出使用 envelope 结构（继承 root --envelope）")

    // 会话控制
    flags.Bool("ephemeral", false, "不持久化会话文件")
    flags.String("session-dir", "", "chat 会话持久化目录")
    if !has("title") { flags.String("title", "", "设置当前 exec 会话标题") }
    if !has("image") { flags.StringSlice("image", nil, "附加图片文件路径") }
    flags.String("request-timeout", "", "单次 LLM 请求超时（如 60s, 2m），留空使用配置")
    flags.Duration("timeout", 0, "整次 exec 执行超时时间（如 5m, 30s），0 表示无限制")

    // 权限和沙箱
    flags.String("permission-mode", "default", "权限模式")
    flags.String("approval-reuse", "session_readonly_shell", "审批复用策略（off|session_readonly_shell|team_readonly_shell）")
    flags.Bool("yolo", false, "快捷模式：等价于 --permission-mode bypass_permissions")

    // Tools / Skills
    flags.Bool("disable-tools", false, "禁用 tools/skills 暴露")
    flags.StringSlice("skills-dir", nil, "附加外部 skills 目录（可重复指定）")
    flags.Int("skills-top-k", 0, "暴露给模型的候选 skills 数量（0=使用配置默认值）")
    flags.String("skills-mode", "auto", "skills 暴露模式（auto|prefer|only）")
    flags.Bool("skills-debug", false, "打印 skill route 候选和暴露结果")

    // 配置覆盖
    flags.StringSliceP("config-override", "C", nil, "配置覆盖 key=value（-c 已被 root --config 使用）")

    // 调试
    flags.Bool("debug-http", false, "HTTP 调试输出")
    flags.Bool("fail-fast", false, "禁用自动重试")
}
```

#### 4.3 exec_options.go

```go
// exec_options.go

package commands

import (
    "time"
    runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// ExecOptions exec 命令选项
type ExecOptions struct {
    // 基本选项
    Prompt         string
    PromptFlag     string // -p 指定的提示词（与 stdin 组合时作为指令）
    ProfileFlag    string
    AgentFlag      string
    ProviderFlag   string
    ModelFlag      string
    MaxTokens      int
    StreamFlag     bool
    StreamChanged  bool
    ReasoningEffortFlag string

    // 输出控制
    JSONMode       bool   // --json: JSONL 事件流
    OutputFormat   string // text|json
    OutputLastMsg  string // --output-last-message: 最后消息输出文件
    OutputSchema   string // --output-schema: JSON Schema 路径或内联 JSON
    JSONEnvelope   bool   // root --envelope

    // 会话控制
    Ephemeral      bool
    SessionDir     string
    SessionTitle   string
    ImagePaths     []string
    RequestTimeout string        // --request-timeout: 单次请求超时
    Timeout        time.Duration // --timeout: 整次 exec 超时

    // 权限和沙箱
    PermissionMode runtimepolicy.Mode
    ApprovalReuse  chatApprovalReuseMode
    YoloMode       bool

    // Tools / Skills
    DisableTools   bool
    CLISkillDirs   []string
    CLISkillsTopK  int
    CLISkillsMode  string
    CLISkillsDebug bool

    // 配置覆盖
    ConfigOverrides []string // -C/--config-override key=value 列表

    // 调试
    HTTPDebug      bool
    FailFast       bool

    // 子命令相关
    Command        string // "resume" | "review" | ""
    ResumeArgs     *ExecResumeArgs
    ReviewArgs     *ExecReviewArgs
}

// ExecResumeArgs resume 子命令参数
type ExecResumeArgs struct {
    SessionID string
    Last      bool
    All       bool
    Prompt    string
    Images    []string
}

// ExecReviewArgs review 子命令参数
type ExecReviewArgs struct {
    Uncommitted bool
    BaseBranch  string
    CommitSHA   string
    CommitTitle string
    Prompt      string
}
```

### Phase 2: 事件处理器抽象层

**目标**：实现事件处理器模式，支持人类可读和 JSONL 两种输出

#### 4.4 exec_event_processor.go

```go
// exec_event_processor.go

package commands

import (
    "io"
    "os"
)

// ExecEventProcessor 事件处理器接口
type ExecEventProcessor interface {
    // PrintConfigSummary 打印配置摘要
    PrintConfigSummary(opts *ExecOptions, model string, provider string)
    
    // OnThreadStarted 线程启动事件
    OnThreadStarted(event ThreadStartedEvent)
    
    // OnTurnStarted 轮次启动事件
    OnTurnStarted(event TurnStartedEvent)
    
    // OnTurnCompleted 轮次完成事件
    OnTurnCompleted(event TurnCompletedEvent)
    
    // OnTurnFailed 轮次失败事件
    OnTurnFailed(event TurnFailedEvent)
    
    // OnItemStarted 项目启动事件（工具调用等）
    OnItemStarted(event ItemStartedEvent)
    
    // OnItemUpdated 项目更新事件
    OnItemUpdated(event ItemUpdatedEvent)
    
    // OnItemCompleted 项目完成事件
    OnItemCompleted(event ItemCompletedEvent)
    
    // OnError 错误事件
    OnError(event ErrorEvent)
    
    // OnWarning 警告事件（本地生成）
    OnWarning(message string)
    
    // OnStreamDelta 流式文本增量（仅人类模式使用）
    OnStreamDelta(delta string)
    
    // PrintFinalOutput 打印最终输出并写入 --output-last-message
    PrintFinalOutput(opts *ExecOptions) error
    
    // SetFinalMessage 设置最终 assistant 消息（所有输出模式都必须调用）
    SetFinalMessage(message string)
    
    // GetFinalMessage 获取最终消息（用于 --output-last-message）
    GetFinalMessage() string
}

// NewExecEventProcessor 创建事件处理器。jsonMode writer 必须是 stdout；human writer 通常是 stderr。
func NewExecEventProcessor(jsonMode bool, writer io.Writer, lastMessageFile string) ExecEventProcessor {
    if writer == nil {
        if jsonMode {
            writer = os.Stdout
        } else {
            writer = os.Stderr
        }
    }
    if jsonMode {
        return newJSONLEventProcessor(writer, lastMessageFile)
    }
    return newHumanEventProcessor(writer, lastMessageFile)
}
```

#### 4.5 exec_events.go

```go
// exec_events.go

package commands

import "encoding/json"

// ThreadEvent JSONL 事件顶层结构
type ThreadEvent struct {
    Version   int             `json:"version"`
    Sequence  int64           `json:"sequence"`
    Timestamp string          `json:"timestamp"`
    ThreadID  string          `json:"thread_id,omitempty"`
    Type      string          `json:"type"`
    Data      json.RawMessage `json:"data,omitempty"`
}

// 事件类型常量
const (
    EventTypeThreadStarted = "thread.started"
    EventTypeTurnStarted   = "turn.started"
    EventTypeTurnCompleted = "turn.completed"
    EventTypeTurnFailed    = "turn.failed"
    EventTypeItemStarted   = "item.started"
    EventTypeItemUpdated   = "item.updated"
    EventTypeItemCompleted = "item.completed"
    EventTypeWarning       = "warning"
    EventTypeError         = "error"
)

// ThreadStartedEvent 线程启动事件
type ThreadStartedEvent struct {
    ThreadID  string `json:"thread_id"`
    SessionID string `json:"session_id,omitempty"`
    Model     string `json:"model"`
    Provider  string `json:"provider"`
    Ephemeral bool   `json:"ephemeral,omitempty"`
}

// TurnStartedEvent 轮次启动事件
type TurnStartedEvent struct {
    TurnID string `json:"turn_id"`
    Prompt string `json:"prompt,omitempty"`
}

// TurnCompletedEvent 轮次完成事件
type TurnCompletedEvent struct {
    TurnID       string     `json:"turn_id"`
    Status       string     `json:"status"` // completed|failed|interrupted
    Usage        TokenUsage `json:"usage"`
    DurationMs   int64      `json:"duration_ms,omitempty"`
}

// TurnFailedEvent 轮次失败事件
type TurnFailedEvent struct {
    TurnID string `json:"turn_id"`
    Error  string `json:"error"`
}

// TokenUsage Token 使用统计
type TokenUsage struct {
    InputTokens         int `json:"input_tokens"`
    CachedInputTokens   int `json:"cached_input_tokens,omitempty"`
    OutputTokens        int `json:"output_tokens"`
    ReasoningTokens     int `json:"reasoning_tokens,omitempty"`
    TotalTokens         int `json:"total_tokens"`
}

// ItemStartedEvent 项目启动事件
type ItemStartedEvent struct {
    ItemID   string      `json:"item_id"`
    ItemType string      `json:"item_type"` // tool_call|message|file_change
    Details  interface{} `json:"details,omitempty"`
}

// ItemUpdatedEvent 项目更新事件
type ItemUpdatedEvent struct {
    ItemID   string      `json:"item_id"`
    ItemType string      `json:"item_type"`
    Details  interface{} `json:"details,omitempty"`
}

// ItemCompletedEvent 项目完成事件
type ItemCompletedEvent struct {
    ItemID   string      `json:"item_id"`
    ItemType string      `json:"item_type"`
    Status   string      `json:"status"` // success|failed
    Details  interface{} `json:"details,omitempty"`
}

// ErrorEvent 错误事件
type ErrorEvent struct {
    Message string `json:"message"`
    Code    string `json:"code,omitempty"`
}

// ExecFinalResult --output json 的最终结果结构
type ExecFinalResult struct {
    Status  string `json:"status"`
    Message string `json:"message"`
}

// ToolCallDetails 工具调用详情
type ToolCallDetails struct {
    ToolName   string      `json:"tool_name"`
    Args       interface{} `json:"args,omitempty"`
    Result     interface{} `json:"result,omitempty"`
    DurationMs int64       `json:"duration_ms,omitempty"`
}

// MessageDetails 消息详情
type MessageDetails struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}

// FileChangeDetails 文件变更详情
type FileChangeDetails struct {
    Path   string `json:"path"`
    Action string `json:"action"` // create|modify|delete
    Diff   string `json:"diff,omitempty"`
}
```

#### 4.6 exec_event_jsonl.go

```go
// exec_event_jsonl.go

package commands

import (
    "encoding/json"
    "fmt"
    "io"
    "os"
    "sync"
    "time"
)

// JSONLEventProcessor JSONL 事件处理器
type JSONLEventProcessor struct {
    writer          io.Writer
    lastMessageFile string
    finalMessage    string
    threadID        string
    sequence        int64
    mu              sync.Mutex
}

func newJSONLEventProcessor(writer io.Writer, lastMessageFile string) *JSONLEventProcessor {
    return &JSONLEventProcessor{
        writer:          writer,
        lastMessageFile: lastMessageFile,
    }
}

func (p *JSONLEventProcessor) emit(eventType string, data interface{}) {
    p.mu.Lock()
    defer p.mu.Unlock()
    
    var rawData json.RawMessage
    if data != nil {
        rawData, _ = json.Marshal(data)
    }
    
    p.sequence++
    event := ThreadEvent{
        Version:   1,
        Sequence:  p.sequence,
        Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
        ThreadID:  p.threadID,
        Type:      eventType,
        Data:      rawData,
    }
    
    bytes, err := json.Marshal(event)
    if err != nil {
        return
    }
    
    fmt.Fprintln(p.writer, string(bytes))
}

func (p *JSONLEventProcessor) PrintConfigSummary(opts *ExecOptions, model, provider string) {
    // JSONL 模式不输出配置摘要（保持输出纯净）
}

func (p *JSONLEventProcessor) OnThreadStarted(event ThreadStartedEvent) {
    p.mu.Lock()
    p.threadID = event.ThreadID
    p.mu.Unlock()
    p.emit(EventTypeThreadStarted, event)
}

func (p *JSONLEventProcessor) OnTurnStarted(event TurnStartedEvent) {
    p.emit(EventTypeTurnStarted, event)
}

func (p *JSONLEventProcessor) OnTurnCompleted(event TurnCompletedEvent) {
    p.emit(EventTypeTurnCompleted, event)
}

func (p *JSONLEventProcessor) OnTurnFailed(event TurnFailedEvent) {
    p.emit(EventTypeTurnFailed, event)
}

func (p *JSONLEventProcessor) OnItemStarted(event ItemStartedEvent) {
    p.emit(EventTypeItemStarted, event)
}

func (p *JSONLEventProcessor) OnItemUpdated(event ItemUpdatedEvent) {
    p.emit(EventTypeItemUpdated, event)
}

func (p *JSONLEventProcessor) OnItemCompleted(event ItemCompletedEvent) {
    p.emit(EventTypeItemCompleted, event)
}

func (p *JSONLEventProcessor) OnError(event ErrorEvent) {
    p.emit(EventTypeError, event)
}

func (p *JSONLEventProcessor) OnWarning(message string) {
    p.emit(EventTypeWarning, ErrorEvent{Message: message})
}

func (p *JSONLEventProcessor) OnStreamDelta(delta string) {
    // JSONL 模式不输出流式增量（事件流已包含完整信息）
}

func (p *JSONLEventProcessor) PrintFinalOutput(opts *ExecOptions) error {
    if p.lastMessageFile != "" && p.finalMessage != "" {
        if err := os.WriteFile(p.lastMessageFile, []byte(p.finalMessage), 0644); err != nil {
            p.emit(EventTypeError, ErrorEvent{
                Message: fmt.Sprintf("写入最后消息文件失败: %v", err),
            })
            return err
        }
    }
    return nil
}

func (p *JSONLEventProcessor) SetFinalMessage(message string) {
    p.mu.Lock()
    defer p.mu.Unlock()
    p.finalMessage = message
}

func (p *JSONLEventProcessor) GetFinalMessage() string {
    p.mu.Lock()
    defer p.mu.Unlock()
    return p.finalMessage
}
```

#### 4.7 exec_event_human.go

```go
// exec_event_human.go

package commands

import (
    "encoding/json"
    "fmt"
    "io"
    "os"
    "sync"

    "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// HumanEventProcessor 人类可读事件处理器
type HumanEventProcessor struct {
    writer          io.Writer
    lastMessageFile string
    finalMessage    string
    theme           *ui.Theme
    isTTY           bool
    mu              sync.Mutex
}

func newHumanEventProcessor(writer io.Writer, lastMessageFile string) *HumanEventProcessor {
    isTTY := false
    if f, ok := writer.(*os.File); ok {
        stat, err := f.Stat()
        if err == nil {
            isTTY = stat.Mode()&os.ModeCharDevice != 0
        }
    }

    return &HumanEventProcessor{
        writer:          writer,
        lastMessageFile: lastMessageFile,
        theme:           ui.GetTheme(ui.ThemeAuto),
        isTTY:           isTTY,
    }
}

func (p *HumanEventProcessor) PrintConfigSummary(opts *ExecOptions, model, provider string) {
    if !p.isTTY {
        return
    }
    fmt.Fprintf(p.writer, "\n%s %s/%s\n",
        p.theme.MetaLabelColor.Sprint("Model:"),
        provider,
        model,
    )
    if opts.PermissionMode != "" {
        fmt.Fprintf(p.writer, "%s %s\n",
            p.theme.MetaLabelColor.Sprint("Permission:"),
            string(opts.PermissionMode),
        )
    }
    fmt.Fprintln(p.writer)
}

func (p *HumanEventProcessor) OnThreadStarted(event ThreadStartedEvent) {
    // 人类模式下不输出线程启动事件（减少噪音）
}

func (p *HumanEventProcessor) OnTurnStarted(event TurnStartedEvent) {
    fmt.Fprintf(p.writer, "%s\n", p.theme.MutedColor.Sprint("Thinking..."))
}

func (p *HumanEventProcessor) OnTurnCompleted(event TurnCompletedEvent) {
    p.mu.Lock()
    defer p.mu.Unlock()

    if p.isTTY {
        fmt.Fprintf(p.writer, "\r\033[K\r") // ANSI clear line
    }
}

func (p *HumanEventProcessor) OnTurnFailed(event TurnFailedEvent) {
    fmt.Fprintf(p.writer, "\n%s %s\n",
        p.theme.ErrorColor.Sprint("Error:"),
        event.Error,
    )
}

func (p *HumanEventProcessor) OnItemStarted(event ItemStartedEvent) {
    p.mu.Lock()
    defer p.mu.Unlock()

    switch event.ItemType {
    case "tool_call":
        if details, ok := event.Details.(*ToolCallDetails); ok {
            fmt.Fprintf(p.writer, "%s %s\n",
                p.theme.ToolColor.Sprint(p.theme.CommandIcon),
                details.ToolName,
            )
        }
    case "file_change":
        if details, ok := event.Details.(*FileChangeDetails); ok {
            fmt.Fprintf(p.writer, "%s %s %s\n",
                p.theme.WarningColor.Sprint(p.theme.ShellIcon),
                details.Action,
                details.Path,
            )
        }
    }
}

func (p *HumanEventProcessor) OnItemUpdated(event ItemUpdatedEvent) {
    // 人类模式下通常不输出更新事件
}

func (p *HumanEventProcessor) OnItemCompleted(event ItemCompletedEvent) {
    // 可选：输出完成状态
}

func (p *HumanEventProcessor) OnError(event ErrorEvent) {
    fmt.Fprintf(p.writer, "\n%s %s\n",
        p.theme.ErrorColor.Sprint("Error:"),
        event.Message,
    )
}

func (p *HumanEventProcessor) OnWarning(message string) {
    fmt.Fprintf(p.writer, "%s %s\n",
        p.theme.WarningColor.Sprint("Warning:"),
        message,
    )
}

func (p *HumanEventProcessor) OnStreamDelta(delta string) {
    fmt.Fprint(p.writer, delta)
    p.finalMessage += delta
}

func (p *HumanEventProcessor) PrintFinalOutput(opts *ExecOptions) error {
    if opts != nil && opts.OutputFormat == "json" {
        payload := ExecFinalResult{
            Status: "completed",
            Message: p.finalMessage,
        }
        if err := json.NewEncoder(os.Stdout).Encode(payload); err != nil {
            return err
        }
    } else if p.finalMessage != "" {
        fmt.Fprintln(os.Stdout, p.finalMessage)
    }
    if p.lastMessageFile != "" && p.finalMessage != "" {
        if err := os.WriteFile(p.lastMessageFile, []byte(p.finalMessage), 0644); err != nil {
            fmt.Fprintf(p.writer, "%s 写入最后消息文件失败: %v\n",
                p.theme.WarningColor.Sprint("Warning:"),
                err,
            )
            return err
        }
    }
    return nil
}

func (p *HumanEventProcessor) SetFinalMessage(message string) {
    p.mu.Lock()
    defer p.mu.Unlock()
    p.finalMessage = message
}

func (p *HumanEventProcessor) GetFinalMessage() string {
    p.mu.Lock()
    defer p.mu.Unlock()
    return p.finalMessage
}
```

### Phase 3: exec 主逻辑实现

**目标**：实现 exec 命令的核心执行逻辑，包括会话构建、工具调用循环、事件桥接

#### 4.8.0 与 Actor/Executor 事件桥接

exec 模式不能只创建一个本地 `execEventBridge` 对象；桥接层必须接入现有执行路径，否则 JSONL 只能输出 turn 级事件，无法覆盖工具调用、文件变更和 warning。

首版按现有两条执行路径分别接入：

1. shared chatcore 路径：在 `chat_core.go` 复用或新增 renderer/sink，将 `runtimechatcore.ChatEvent` 映射为 `ExecEventProcessor` 调用。
2. actor executor 路径：订阅 `LocalRuntimeHost.EventBus` 或复用 `chat_runtime_events.go` 中的运行时事件处理点，将 actor/runtime 事件转换为 exec item 事件。

实现约束：

- `item.started` 和 `item.completed` 必须使用同一个 `item_id`。工具事件优先使用 runtime 提供的 tool call id；如果事件源没有稳定 id，桥接层维护 `tool_call_id -> item_id` 映射表，不能在 start/complete 时分别生成新 id。
- JSONL 模式下，assistant token delta 只能写入 `item.updated` 或专用 stream 事件，不能混入 stdout 的最终文本。
- warning 应输出为 `warning` 事件；只有无法序列化为事件时才写 stderr。
- 桥接层测试必须覆盖 actor 路径和 shared chatcore 路径，避免只验证一个模拟接口。

辅助 id 函数可保留在 `exec_events.go` 或 `exec_event_processor.go` 中：

```go
func generateThreadID() string { return "thread_" + uuid.New().String()[:8] }
func generateTurnID() string   { return "turn_" + uuid.New().String()[:8] }
func generateItemID() string   { return "item_" + uuid.New().String()[:8] }
```

#### 4.8 exec_run.go

```go
// exec_run.go

package commands

import (
    "context"
    "fmt"
    "io"
    "os"
    "os/signal"
    "strings"
    "syscall"
    "time"

    "github.com/spf13/cobra"
    config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
    runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
    runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// ExecSession exec 会话状态
type ExecSession struct {
    Options       *ExecOptions
    Config        *config.Config
    Processor     ExecEventProcessor
    ChatSession   *ChatSession
    SessionMgr    *runtimechat.SessionManager
    SessionID     string
    StartTime     time.Time
}

// runExec 执行 exec 命令
func runExec(cmd *cobra.Command, cfg *config.Config, args []string) error {
    opts, err := parseExecOptions(cmd, args)
    if err != nil {
        return err
    }

    // 应用配置覆盖
    if len(opts.ConfigOverrides) > 0 {
        if err := applyConfigOverrides(cfg, opts.ConfigOverrides); err != nil {
            return fmt.Errorf("配置覆盖失败: %w", err)
        }
    }

    // 创建事件处理器。JSONL 写 stdout；human 进度写 stderr，最终输出另写 stdout。
    processor := NewExecEventProcessor(opts.JSONMode, nil, opts.OutputLastMsg)

    // 创建会话
    session, cleanup, err := buildExecSession(cfg, opts, processor)
    if err != nil {
        return err
    }
    defer cleanup()

    // 信号处理 + 超时
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    if opts.Timeout > 0 {
        ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
        defer cancel()
    }

    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    go func() {
        <-sigChan
        cancel()
    }()

    // 执行
    return executeExec(ctx, session)
}

// parseExecOptions 解析命令选项
func parseExecOptions(cmd *cobra.Command, args []string) (*ExecOptions, error) {
    opts := &ExecOptions{}

    // 基本选项
    opts.ProfileFlag, _ = cmd.Flags().GetString("profile")
    opts.AgentFlag, _ = cmd.Flags().GetString("agent")
    opts.ProviderFlag, _ = cmd.Flags().GetString("provider")
    opts.ModelFlag, _ = cmd.Flags().GetString("model")
    opts.MaxTokens, _ = cmd.Flags().GetInt("max-tokens")
    opts.PromptFlag, _ = cmd.Flags().GetString("prompt")
    opts.StreamFlag, _ = cmd.Flags().GetBool("stream")
    opts.StreamChanged = cmd.Flags().Changed("stream")
    opts.ReasoningEffortFlag, _ = cmd.Flags().GetString("reasoning-effort")

    // 输出控制
    opts.JSONMode, _ = cmd.Flags().GetBool("json")
    outputFlag, _ := cmd.Flags().GetString("output")
    if opts.JSONMode && strings.TrimSpace(outputFlag) != "" {
        return nil, fmt.Errorf("--json 与 --output 不能同时使用；--json 是 JSONL 事件流，--output json 是最终结果 JSON")
    }
    outputFormat, err := resolveChatOutputFormat(true, outputFlag, false)
    if err != nil {
        return nil, err
    }
    opts.OutputFormat = outputFormat
    opts.OutputLastMsg, _ = cmd.Flags().GetString("output-last-message")
    opts.OutputSchema, _ = cmd.Flags().GetString("output-schema")
    opts.JSONEnvelope, _ = cmd.Flags().GetBool("envelope")

    // 会话控制
    opts.Ephemeral, _ = cmd.Flags().GetBool("ephemeral")
    opts.SessionDir, _ = cmd.Flags().GetString("session-dir")
    opts.SessionTitle, _ = cmd.Flags().GetString("title")
    opts.ImagePaths, _ = cmd.Flags().GetStringSlice("image")
    opts.RequestTimeout, _ = cmd.Flags().GetString("request-timeout")
    opts.Timeout, _ = cmd.Flags().GetDuration("timeout")

    // 权限和沙箱 — 使用现有的 parseChatPermissionMode 辅助函数
    permissionModeFlag, _ := cmd.Flags().GetString("permission-mode")
    opts.YoloMode, _ = cmd.Flags().GetBool("yolo")
    permMode, err := parseChatPermissionMode(permissionModeFlag, opts.YoloMode)
    if err != nil {
        return nil, err
    }
    opts.PermissionMode = permMode
    approvalReuseFlag, _ := cmd.Flags().GetString("approval-reuse")
    approvalReuseMode, err := parseChatApprovalReuseMode(approvalReuseFlag)
    if err != nil {
        return nil, err
    }
    opts.ApprovalReuse = approvalReuseMode

    // Tools / Skills
    opts.DisableTools, _ = cmd.Flags().GetBool("disable-tools")
    opts.CLISkillDirs, _ = cmd.Flags().GetStringSlice("skills-dir")
    opts.CLISkillsTopK, _ = cmd.Flags().GetInt("skills-top-k")
    opts.CLISkillsMode, _ = cmd.Flags().GetString("skills-mode")
    opts.CLISkillsDebug, _ = cmd.Flags().GetBool("skills-debug")

    // 配置覆盖
    opts.ConfigOverrides, _ = cmd.Flags().GetStringSlice("config-override")

    // 调试
    opts.HTTPDebug, _ = cmd.Flags().GetBool("debug-http")
    opts.FailFast, _ = cmd.Flags().GetBool("fail-fast")

    // 处理提示词：支持 args + stdin + -p 的组合
    opts.Prompt = buildExecPrompt(args, opts.PromptFlag)

    return opts, nil
}

// buildExecPrompt 构建最终提示词
// 支持三种输入源的组合：
//   - args: 命令行参数作为提示词
//   - promptFlag: -p 指定的指令（与 stdin 组合时作为处理指令）
//   - stdin: 管道输入作为上下文内容
//
// 组合规则：
//   aicli exec "prompt"                → prompt
//   echo "data" | aicli exec -p "指令"  → "指令\n\n---\ndata"
//   echo "data" | aicli exec           → data
func buildExecPrompt(args []string, promptFlag string) string {
    argPrompt := strings.Join(args, " ")
    stdinContent, _ := readStdinIfPiped()

    switch {
    case argPrompt != "":
        return argPrompt
    case promptFlag != "" && stdinContent != "":
        return fmt.Sprintf("%s\n\n---\n%s", promptFlag, stdinContent)
    case promptFlag != "":
        return promptFlag
    case stdinContent != "":
        return stdinContent
    default:
        return ""
    }
}

// readStdinIfPiped 如果 stdin 是管道则读取
func readStdinIfPiped() (string, error) {
    stat, err := os.Stdin.Stat()
    if err != nil {
        return "", err
    }
    if (stat.Mode() & os.ModeCharDevice) != 0 {
        return "", nil // 不是管道
    }
    data, err := io.ReadAll(os.Stdin)
    if err != nil {
        return "", err
    }
    return strings.TrimSpace(string(data)), nil
}

// buildExecSession 构建 exec 会话
// 复用现有 chat 基础设施：prepareChatPersistence → prepareChatRuntimeState → bootstrapChatSession
func buildExecSession(cfg *config.Config, opts *ExecOptions, processor ExecEventProcessor) (*ExecSession, func(), error) {
    // 构造等价的 chatCommandOptions
    chatOpts := &chatCommandOptions{
        ProfileFlag:    opts.ProfileFlag,
        AgentFlag:      opts.AgentFlag,
        ProviderFlag:   opts.ProviderFlag,
        ModelFlag:      opts.ModelFlag,
        StreamFlag:     opts.StreamFlag,
        NoInteractive:  true,
        Message:        opts.Prompt,
        ImagePaths:     opts.ImagePaths,
        PermissionMode: opts.PermissionMode,
        ApprovalReuseMode: opts.ApprovalReuse,
        DisableTools:   opts.DisableTools,
        HTTPDebug:      opts.HTTPDebug,
        FailFast:       opts.FailFast,
        CLISkillDirs:   opts.CLISkillDirs,
        CLISkillsTopK:  opts.CLISkillsTopK,
        CLISkillsMode:  opts.CLISkillsMode,
        CLISkillsDebug: opts.CLISkillsDebug,
        ReasoningEffortFlag: opts.ReasoningEffortFlag,
        SessionDirFlag: opts.SessionDir,
        SessionTitleFlag: opts.SessionTitle,
        OutputFormat:   opts.OutputFormat,
        JSONOutput:     opts.JSONMode,
        JSONEnvelope:   opts.JSONEnvelope,
        RequestTimeoutFlag: opts.RequestTimeout,
    }

    // 1. 解析 profile defaults
    profileState, err := resolveChatProfileState(cfg, chatOpts)
    if err != nil {
        return nil, nil, err
    }
    applyProfileDefaultsToChatOptions(chatOpts, profileState)

    chatOpts.SessionFeaturesRequested = strings.TrimSpace(opts.SessionDir) != "" ||
        strings.TrimSpace(opts.SessionTitle) != "" ||
        profileState != nil

    // 2. 持久化状态
    persistenceState, err := prepareExecPersistence(chatOpts, opts)
    if err != nil {
        return nil, nil, err
    }

    // 3. 运行时状态（provider/model/adapter 解析）
    runtimeState, _, err := prepareChatRuntimeState(cfg, chatOpts, persistenceState.loadedRuntimeSession)
    if err != nil {
        return nil, nil, err
    }

    // 4. 构建 ChatSession（无 UI 组件，因为 NoInteractive=true）
    chatSession, cleanupSession, err := bootstrapChatSession(cfg, chatOpts, profileState, persistenceState, runtimeState)
    if err != nil {
        return nil, nil, err
    }

    session := &ExecSession{
        Options:     opts,
        Config:      cfg,
        Processor:   processor,
        ChatSession: chatSession,
        SessionMgr:  persistenceState.runtimeSessionManager,
        SessionID:   currentRuntimeSessionID(chatSession),
        StartTime:   time.Now(),
    }

    cleanup := func() {
        if persistenceState.runtimeSessionManager != nil {
            persistenceState.runtimeSessionManager.Stop()
        }
        if cleanupSession != nil {
            cleanupSession()
        }
    }

    return session, cleanup, nil
}

// prepareExecPersistence 处理 exec 的会话持久化语义
func prepareExecPersistence(chatOpts *chatCommandOptions, opts *ExecOptions) (*chatPersistenceState, error) {
    if opts != nil && opts.Ephemeral {
        if strings.TrimSpace(opts.SessionDir) != "" || strings.TrimSpace(opts.SessionTitle) != "" {
            return nil, fmt.Errorf("--ephemeral 不能与 --session-dir 或 --title 同时使用")
        }
        return &chatPersistenceState{}, nil
    }
    return prepareChatPersistence(chatOpts)
}

// executeExec 执行 exec 会话（含工具调用循环）
func executeExec(ctx context.Context, session *ExecSession) error {
    opts := session.Options
    processor := session.Processor
    chatSession := session.ChatSession

    // 打印配置摘要
    processor.PrintConfigSummary(opts, chatSession.Model, chatSession.ProviderName)

    // 线程启动事件
    threadID := generateThreadID()
    processor.OnThreadStarted(ThreadStartedEvent{
        ThreadID:  threadID,
        Model:     chatSession.Model,
        Provider:  chatSession.ProviderName,
        Ephemeral: opts.Ephemeral,
    })

    // 提示词验证
    if opts.Prompt == "" {
        processor.OnError(ErrorEvent{
            Message: "未提供提示词。请通过参数、-p 或 stdin 提供提示词。",
            Code:    "NO_PROMPT",
        })
        return fmt.Errorf("未提供提示词")
    }

    // 执行轮次（含工具调用循环）
    turnID := generateTurnID()
    processor.OnTurnStarted(TurnStartedEvent{
        TurnID: turnID,
        Prompt: opts.Prompt,
    })

    startTime := time.Now()

    // 使用现有的 aicliChatExecutor 执行（自动处理工具调用循环）
    executor := ensureChatExecutor(chatSession)
    response, err := executor.Execute(ctx, chatSession, opts.Prompt)

    duration := time.Since(startTime)

    if err != nil {
        // 区分超时和其他错误
        if ctx.Err() == context.DeadlineExceeded {
            processor.OnTurnFailed(TurnFailedEvent{
                TurnID: turnID,
                Error:  fmt.Sprintf("执行超时（%v）", opts.Timeout),
            })
        } else if ctx.Err() == context.Canceled {
            processor.OnTurnCompleted(TurnCompletedEvent{
                TurnID:     turnID,
                Status:     "interrupted",
                DurationMs: duration.Milliseconds(),
            })
        } else {
            processor.OnTurnFailed(TurnFailedEvent{
                TurnID: turnID,
                Error:  err.Error(),
            })
        }
        return err
    }

    if strings.TrimSpace(opts.OutputSchema) != "" {
        if err := validateExecFinalMessageSchema(opts.OutputSchema, response); err != nil {
            processor.OnTurnFailed(TurnFailedEvent{
                TurnID: turnID,
                Error:  fmt.Sprintf("schema 校验失败: %v", err),
            })
            processor.OnError(ErrorEvent{
                Message: err.Error(),
                Code:    "SCHEMA_VALIDATION_FAILED",
            })
            return err
        }
    }

    // 完成事件
    processor.OnTurnCompleted(TurnCompletedEvent{
        TurnID:     turnID,
        Status:     "completed",
        DurationMs: duration.Milliseconds(),
        Usage: TokenUsage{
            InputTokens:  chatSession.ContextTokenCount,
            OutputTokens: chatSession.TokenCount - chatSession.ContextTokenCount,
            TotalTokens:  chatSession.TokenCount,
        },
    })

    // 输出最终响应
    processor.SetFinalMessage(response)
    if err := processor.PrintFinalOutput(opts); err != nil {
        return err
    }

    return nil
}
```

#### 4.8.1 exec_config_override.go

```go
// exec_config_override.go

package commands

import (
    "fmt"
    "strings"

    config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// applyConfigOverrides 解析并应用 -C/--config-override key=value 配置覆盖
// 支持的 key 格式：
//   model              → 覆盖当前默认 provider 的默认模型（进程内临时生效）
//   provider.base_url  → 覆盖当前默认 provider 的 base_url
//   provider.api_key   → 覆盖当前默认 provider 的 api_key
//   provider.forward_url → 覆盖当前默认 provider 的 forward_url
//
// 不在首版伪装支持 temperature/system_prompt；如需要，应新增明确 flag 或 profile 配置。
func applyConfigOverrides(cfg *config.Config, overrides []string) error {
    for _, override := range overrides {
        parts := strings.SplitN(override, "=", 2)
        if len(parts) != 2 {
            return fmt.Errorf("无效的配置覆盖格式: %q（期望 key=value）", override)
        }
        key := strings.TrimSpace(parts[0])
        value := strings.TrimSpace(parts[1])

        if err := applySingleConfigOverride(cfg, key, value); err != nil {
            return fmt.Errorf("应用配置覆盖 %q 失败: %w", override, err)
        }
    }
    return nil
}

func applySingleConfigOverride(cfg *config.Config, key, value string) error {
    switch {
    case key == "model":
        // 覆盖默认 provider 的 model
        if defaultProvider, ok := cfg.Providers.Items[cfg.Providers.DefaultProvider]; ok {
            defaultProvider.DefaultModel = value
            cfg.Providers.Items[cfg.Providers.DefaultProvider] = defaultProvider
        }
    case strings.HasPrefix(key, "provider."):
        subKey := strings.TrimPrefix(key, "provider.")
        return applyProviderConfigOverride(cfg, subKey, value)
    default:
        return fmt.Errorf("不支持的配置 key: %q", key)
    }
    return nil
}

func applyProviderConfigOverride(cfg *config.Config, subKey, value string) error {
    providerName := cfg.Providers.DefaultProvider
    if providerName == "" {
        return fmt.Errorf("未设置默认 provider")
    }
    provider, ok := cfg.Providers.Items[providerName]
    if !ok {
        return fmt.Errorf("provider %q 不存在", providerName)
    }

    switch subKey {
    case "base_url":
        provider.BaseURL = value
    case "api_key":
        provider.APIKey = value
    case "forward_url":
        provider.ForwardURL = value
    default:
        return fmt.Errorf("不支持的 provider 配置 key: %q", subKey)
    }

    cfg.Providers.Items[providerName] = provider
    return nil
}
```

#### 4.8.2 exec_output_schema.go

`--output-schema` 只校验最终 assistant 消息，不校验 JSONL 事件本身。首版支持两种输入：

- 文件路径：`--output-schema ./schema.json`
- 内联 JSON：`--output-schema '{"type":"object","required":["summary"]}'`

实现要求：

1. 在 `executeExec` 获取最终 `response` 后调用 `validateExecFinalMessageSchema(opts.OutputSchema, response)`。
2. 若 schema 不为空，最终消息必须是合法 JSON；否则返回 `SCHEMA_VALIDATION_FAILED`。
3. 校验失败时仍写出 `turn.failed` 和 `error` 事件，不写 `turn.completed`。
4. schema 校验成功后再调用 `processor.SetFinalMessage(response)` 和 `processor.PrintFinalOutput(opts)`。

推荐实现使用成熟 JSON Schema 库，例如 `github.com/santhosh-tekuri/jsonschema/v6`。如果不想首版引入依赖，可把 `--output-schema` 标记为 Phase 2 功能，但不能同时在目标中声明“已支持”。

### Phase 4: resume 和 review 子命令

**目标**：实现会话恢复和代码审查子命令

#### 4.9 exec_resume.go

```go
// exec_resume.go

package commands

import (
    "context"
    "fmt"
    "os"
    "strings"
    "time"

    "github.com/spf13/cobra"
    config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
    runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// newExecResumeCommand 创建 resume 子命令
func newExecResumeCommand(getCfg func() *config.Config) *cobra.Command {
    cmd := &cobra.Command{
        Use:   "resume [SESSION_ID] [PROMPT]",
        Short: "恢复之前的会话",
        Long: `恢复之前的历史会话继续对话。

可以指定会话 ID 或使用 --last 恢复最近的会话。
没有 PROMPT 时只加载会话并输出摘要/最后一条 assistant 消息，不发送新 turn。`,
        Example: `  # 恢复最近的会话
  aicli exec resume --last

  # 恢复指定会话
  aicli exec resume <session-id>

  # 恢复后发送新消息
  aicli exec resume --last "继续上次的任务"`,
        RunE: func(cmd *cobra.Command, args []string) error {
            return runExecResume(cmd, getCfg(), args)
        },
    }

    // 注册 flags
    cmd.Flags().Bool("last", false, "恢复最近的会话")
    cmd.Flags().Bool("all", false, "显示所有会话（禁用目录过滤）")
    cmd.Flags().StringSliceP("image", "i", nil, "附加图片文件路径")
    cmd.Flags().StringP("prompt", "p", "", "恢复后发送的提示词")

    // 继承 exec 的通用 flags（排除 resume 自己定义的 prompt/image）
    registerExecSharedFlags(cmd, map[string]bool{"prompt": true, "image": true})

    return cmd
}

// runExecResume 执行 resume 子命令
func runExecResume(cmd *cobra.Command, cfg *config.Config, args []string) error {
    // 解析参数
    last, _ := cmd.Flags().GetBool("last")
    all, _ := cmd.Flags().GetBool("all")
    images, _ := cmd.Flags().GetStringSlice("image")
    prompt, _ := cmd.Flags().GetString("prompt")
    ephemeral, _ := cmd.Flags().GetBool("ephemeral")
    if ephemeral {
        return fmt.Errorf("exec resume 不支持 --ephemeral")
    }

    var sessionID string
    switch {
    case last:
        // --last 下 positional args 全部是 prompt，不做 session id 猜测
        if len(args) > 0 {
            prompt = strings.Join(args, " ")
        }
    case len(args) > 0:
        sessionID = args[0]
        if len(args) > 1 {
            prompt = strings.Join(args[1:], " ")
        }
    default:
        return fmt.Errorf("请指定 SESSION_ID，或使用 --last 恢复最近会话")
    }

    // 复用 exec 通用选项解析，再覆盖 resume 专用参数
    opts, err := parseExecOptions(cmd, nil)
    if err != nil {
        return err
    }
    opts.Prompt = prompt
    opts.ImagePaths = images
    opts.Command = "resume"
    opts.ResumeArgs = &ExecResumeArgs{
        SessionID: sessionID,
        Last:      last,
        All:       all,
        Prompt:    prompt,
        Images:    images,
    }

    // 创建事件处理器
    processor := NewExecEventProcessor(opts.JSONMode, nil, opts.OutputLastMsg)

    // 创建会话（带恢复）
    session, cleanup, err := buildExecSessionWithResume(cfg, opts, processor)
    if err != nil {
        return err
    }
    defer cleanup()

    // 执行：无 prompt 时只输出恢复摘要/最后消息，不发送新 turn
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    if opts.Timeout > 0 {
        ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
        defer cancel()
    }

    if strings.TrimSpace(opts.Prompt) == "" {
        return printExecResumeSummary(ctx, session)
    }
    return executeExec(ctx, session)
}

// buildExecSessionWithResume 构建带会话恢复的 exec 会话
func buildExecSessionWithResume(cfg *config.Config, opts *ExecOptions, processor ExecEventProcessor) (*ExecSession, func(), error) {
    resumeArgs := opts.ResumeArgs

    // 构造 chatCommandOptions 并启用 resume
    chatOpts := &chatCommandOptions{
        ProfileFlag:          opts.ProfileFlag,
        AgentFlag:            opts.AgentFlag,
        ProviderFlag:         opts.ProviderFlag,
        ModelFlag:            opts.ModelFlag,
        StreamFlag:           opts.StreamFlag,
        StreamChanged:        opts.StreamChanged,
        NoInteractive:        true,
        Message:              opts.Prompt,
        ImagePaths:           opts.ImagePaths,
        PermissionMode:       opts.PermissionMode,
        ApprovalReuseMode:    opts.ApprovalReuse,
        DisableTools:         opts.DisableTools,
        HTTPDebug:            opts.HTTPDebug,
        FailFast:             opts.FailFast,
        CLISkillDirs:         opts.CLISkillDirs,
        CLISkillsTopK:        opts.CLISkillsTopK,
        CLISkillsMode:        opts.CLISkillsMode,
        CLISkillsDebug:       opts.CLISkillsDebug,
        ReasoningEffortFlag:  opts.ReasoningEffortFlag,
        SessionDirFlag:       opts.SessionDir,
        SessionTitleFlag:     opts.SessionTitle,
        OutputFormat:         opts.OutputFormat,
        JSONOutput:           opts.JSONMode,
        JSONEnvelope:         opts.JSONEnvelope,
        RequestTimeoutFlag:   opts.RequestTimeout,
        ResumeFlag:           resumeArgs.Last,
        SessionIDFlag:        resumeArgs.SessionID,
        SessionFeaturesRequested: true,
    }

    profileState, err := resolveChatProfileState(cfg, chatOpts)
    if err != nil {
        return nil, nil, err
    }
    applyProfileDefaultsToChatOptions(chatOpts, profileState)

    // resume 必须使用持久化状态（会自动加载指定 session 或最近 session）
    persistenceState, err := prepareChatPersistence(chatOpts)
    if err != nil {
        return nil, nil, err
    }

    // 如果 --last 且没有指定 session ID，查找最近的会话
    if resumeArgs.Last && resumeArgs.SessionID == "" && persistenceState.loadedRuntimeSession == nil {
        latestSession, err := findLatestResumableSession(persistenceState.runtimeSessionManager, persistenceState.sessionUserID, resumeArgs.All)
        if err != nil {
            return nil, nil, fmt.Errorf("查找最近会话失败: %w", err)
        }
        if latestSession == nil {
            return nil, nil, fmt.Errorf("没有可恢复的会话")
        }
        persistenceState.loadedRuntimeSession = latestSession
    }

    if persistenceState.loadedRuntimeSession == nil {
        return nil, nil, fmt.Errorf("未找到指定的会话（ID: %s）", resumeArgs.SessionID)
    }

    // 运行时状态
    runtimeState, _, err := prepareChatRuntimeState(cfg, chatOpts, persistenceState.loadedRuntimeSession)
    if err != nil {
        return nil, nil, err
    }

    // 构建会话
    chatSession, cleanupSession, err := bootstrapChatSession(cfg, chatOpts, profileState, persistenceState, runtimeState)
    if err != nil {
        return nil, nil, err
    }

    session := &ExecSession{
        Options:     opts,
        Config:      cfg,
        Processor:   processor,
        ChatSession: chatSession,
        SessionMgr:  persistenceState.runtimeSessionManager,
        SessionID:   persistenceState.loadedRuntimeSession.ID,
        StartTime:   time.Now(),
    }

    cleanup := func() {
        if persistenceState.runtimeSessionManager != nil {
            persistenceState.runtimeSessionManager.Stop()
        }
        if cleanupSession != nil {
            cleanupSession()
        }
    }

    return session, cleanup, nil
}

// printExecResumeSummary 输出已恢复会话摘要，不执行新的模型请求
func printExecResumeSummary(ctx context.Context, session *ExecSession) error {
    if session == nil || session.ChatSession == nil {
        return fmt.Errorf("会话未初始化")
    }
    // 实现时从 loaded runtime session 或 ChatSession 历史中提取最后一条 assistant 消息。
    // human/text 模式输出摘要到 stderr、最后消息到 stdout；JSONL 模式输出 thread.started + turn.completed(status=resumed)。
    lastMessage := lastAssistantMessage(session.ChatSession)
    session.Processor.SetFinalMessage(lastMessage)
    return session.Processor.PrintFinalOutput(session.Options)
}

// findLatestResumableSession 查找最近可恢复的会话
func findLatestResumableSession(mgr *runtimechat.SessionManager, userID string, all bool) (*runtimechat.Session, error) {
    if mgr == nil {
        return nil, fmt.Errorf("会话管理器未初始化")
    }
    // 使用现有的 SessionManager API 查找最近会话
    sessions, err := mgr.ListSessions(context.Background(), userID)
    if err != nil {
        return nil, err
    }
    if len(sessions) == 0 {
        return nil, nil
    }
    // sessions 已按时间倒序排列，返回第一个
    return sessions[0], nil
}

```

#### 4.10 exec_review.go

```go
// exec_review.go

package commands

import (
    "context"
    "fmt"
    "os"
    "os/exec"
    "strings"
    "time"

    "github.com/spf13/cobra"
    config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
    runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// newExecReviewCommand 创建 review 子命令
func newExecReviewCommand(getCfg func() *config.Config) *cobra.Command {
    cmd := &cobra.Command{
        Use:   "review",
        Short: "运行代码审查",
        Long: `对当前代码仓库运行代码审查。

支持多种审查目标：
  - --uncommitted: 审查未提交的更改
  - --base: 审查相对于基础分支的更改
  - --commit: 审查特定提交的更改
  - positional args: 作为自定义审查指令；不指定审查目标时默认审查未提交更改`,
        Example: `  # 审查未提交的更改
  aicli exec review --uncommitted

  # 审查相对于 main 分支的更改
  aicli exec review --base main

  # 审查特定提交
  aicli exec review --commit abc1234

  # 自定义审查指令
  aicli exec review "检查安全漏洞"`,
        RunE: func(cmd *cobra.Command, args []string) error {
            return runExecReview(cmd, getCfg(), args)
        },
    }

    // 注册 flags
    cmd.Flags().Bool("uncommitted", false, "审查未提交的更改")
    cmd.Flags().String("base", "", "审查相对于基础分支的更改")
    cmd.Flags().String("commit", "", "审查特定提交")
    cmd.Flags().String("commit-title", "", "提交标题（用于 --commit）")

    // 继承 exec 的通用 flags（排除 prompt/title，避免读取 stdin 或和提交标题语义冲突）
    registerExecSharedFlags(cmd, map[string]bool{"prompt": true, "title": true})

    return cmd
}

// runExecReview 执行 review 子命令
func runExecReview(cmd *cobra.Command, cfg *config.Config, args []string) error {
    // 解析参数
    uncommitted, _ := cmd.Flags().GetBool("uncommitted")
    baseBranch, _ := cmd.Flags().GetString("base")
    commitSHA, _ := cmd.Flags().GetString("commit")
    commitTitle, _ := cmd.Flags().GetString("commit-title")
    customPrompt := strings.Join(args, " ")

    // 验证审查目标冲突。customPrompt 是审查指令，不参与目标互斥。
    flagCount := 0
    if uncommitted { flagCount++ }
    if baseBranch != "" { flagCount++ }
    if commitSHA != "" { flagCount++ }

    if flagCount == 0 {
        uncommitted = true
    }
    if flagCount > 1 {
        return fmt.Errorf("--uncommitted, --base 和 --commit 不能同时使用")
    }

    // 获取 diff 内容
    diff, err := getReviewDiff(uncommitted, baseBranch, commitSHA)
    if err != nil {
        return fmt.Errorf("获取 diff 失败: %w", err)
    }
    if strings.TrimSpace(diff) == "" {
        return fmt.Errorf("没有可审查的更改")
    }

    // 构建审查 prompt
    reviewPrompt := buildReviewPrompt(diff, customPrompt, commitTitle, uncommitted, baseBranch, commitSHA)

    // 复用 exec 通用选项解析，再覆盖 review 专用默认值。
    // review 允许用户显式传入 --ephemeral；首版不实现 --persist-review-session，因此最终固定为 true。
    opts, err := parseExecOptions(cmd, nil)
    if err != nil {
        return err
    }
    opts.Prompt = reviewPrompt
    opts.PermissionMode = runtimepolicy.ModePlan // review 默认只读模式
    opts.Ephemeral = true                        // review 默认不持久化
    opts.Command = "review"
    opts.ReviewArgs = &ExecReviewArgs{
        Uncommitted: uncommitted,
        BaseBranch:  baseBranch,
        CommitSHA:   commitSHA,
        CommitTitle: commitTitle,
        Prompt:      customPrompt,
    }

    // 创建事件处理器
    processor := NewExecEventProcessor(opts.JSONMode, nil, opts.OutputLastMsg)

    // 构建会话
    session, cleanup, err := buildExecSession(cfg, opts, processor)
    if err != nil {
        return err
    }
    defer cleanup()

    // 执行
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    if opts.Timeout > 0 {
        ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
        defer cancel()
    }

    return executeExec(ctx, session)
}

// getReviewDiff 获取审查目标的 diff 内容
func getReviewDiff(uncommitted bool, baseBranch, commitSHA string) (string, error) {
    switch {
    case uncommitted:
        return getUncommittedReviewDiff()
    case baseBranch != "":
        // 相对于基础分支的更改
        output, err := exec.Command("git", "diff", baseBranch+"...HEAD").Output()
        return string(output), err
    case commitSHA != "":
        // 特定提交的更改
        output, err := exec.Command("git", "show", "--format=", commitSHA).Output()
        return string(output), err
    default:
        return "", fmt.Errorf("未指定审查目标")
    }
}

func getUncommittedReviewDiff() (string, error) {
    output, err := exec.Command("git", "diff", "HEAD", "--").Output()
    if err != nil {
        return "", fmt.Errorf("git 命令执行失败: %w", err)
    }
    untracked, err := exec.Command("git", "ls-files", "--others", "--exclude-standard").Output()
    if err != nil {
        return "", fmt.Errorf("获取 untracked 文件失败: %w", err)
    }
    diff := string(output)
    for _, file := range strings.Fields(string(untracked)) {
        // 简化伪代码：真实实现应按文件大小、二进制文件和总字节数限制读取。
        content, err := os.ReadFile(file)
        if err != nil {
            diff += fmt.Sprintf("\n# untracked: %s（读取失败: %v）\n", file, err)
            continue
        }
        diff += fmt.Sprintf("\ndiff --git a/%s b/%s\nnew file mode 100644\n--- /dev/null\n+++ b/%s\n%s\n",
            file, file, file, string(content))
    }
    return truncateReviewDiffWithWarning(diff), nil
}

// buildReviewPrompt 构建代码审查提示词
func buildReviewPrompt(diff, customPrompt, commitTitle string, uncommitted bool, baseBranch, commitSHA string) string {
    var sb strings.Builder

    // 审查指令
    if customPrompt != "" {
        sb.WriteString(customPrompt)
    } else {
        sb.WriteString("请对以下代码变更进行审查。关注：\n")
        sb.WriteString("1. 潜在的 bug 和逻辑错误\n")
        sb.WriteString("2. 安全漏洞（注入、XSS、敏感信息泄露等）\n")
        sb.WriteString("3. 性能问题\n")
        sb.WriteString("4. 代码风格和可维护性\n")
        sb.WriteString("5. 边界条件和错误处理\n")
    }

    // 上下文信息
    sb.WriteString("\n\n")
    switch {
    case uncommitted:
        sb.WriteString("## 审查目标：未提交的更改\n\n")
    case baseBranch != "":
        sb.WriteString(fmt.Sprintf("## 审查目标：相对于 %s 分支的更改\n\n", baseBranch))
    case commitSHA != "":
        title := commitSHA
        if commitTitle != "" {
            title = fmt.Sprintf("%s (%s)", commitTitle, shortCommitSHA(commitSHA))
        }
        sb.WriteString(fmt.Sprintf("## 审查目标：提交 %s\n\n", title))
    }

    // Diff 内容
    sb.WriteString("```diff\n")
    sb.WriteString(diff)
    sb.WriteString("\n```\n")

    return sb.String()
}

func shortCommitSHA(sha string) string {
    if len(sha) <= 8 {
        return sha
    }
    return sha[:8]
}

func truncateReviewDiffWithWarning(diff string) string {
    // 首版按字节和文件数做保守截断；实际实现应同时发出 warning 事件。
    const maxReviewDiffBytes = 512 * 1024
    if len(diff) <= maxReviewDiffBytes {
        return diff
    }
    return diff[:maxReviewDiffBytes] + "\n\n[warning] diff 已截断，审查结果可能不完整。\n"
}
```

### Phase 5: 注册到主命令

**目标**：将 exec 命令注册到 aicli 主命令

#### 4.11 修改 main.go

```go
// 在 main.go 的 rootCmd 初始化部分添加

// exec 子命令
execCmd := commands.NewExecCommand(func() *config.Config {
    return cfg
})
rootCmd.AddCommand(execCmd)
```

## 5. 与现有功能的兼容性

### 5.1 向后兼容策略

| 现有功能 | 兼容策略 |
|----------|----------|
| `chat --no-interactive` | 保持不变，作为交互式 chat 的非交互模式 |
| `pipe` | 保持不变，作为简单的 stdin→LLM 管道 |
| `test` | 保持不变，作为端点测试工具 |
| `/resume` 斜杠命令 | 保持不变，作为交互模式内的会话恢复 |

### 5.2 功能映射

```
现有功能                    →  exec 等价物
─────────────────────────────────────────────
chat --no-interactive "..."  →  aicli exec "..."
pipe -p "..."               →  aicli exec -p "..." (stdin)
chat (交互) + /resume       →  aicli exec resume --last
```

### 5.3 渐进式迁移

1. **Phase 1-2**：新增 `exec` 命令，不影响现有命令
2. **Phase 3**：实现核心逻辑，复用现有 ChatSession
3. **Phase 4**：实现子命令，扩展功能
4. **Phase 5**：文档更新，推荐新用法
5. **未来版本**：考虑将 `chat --no-interactive` 标记为 deprecated

## 6. 测试计划

### 6.1 单元测试

```
backend/cmd/aicli/commands/
├── exec_test.go                     # exec 命令注册和 flag 解析测试
├── exec_options_test.go             # 选项解析测试（含 buildExecPrompt 组合逻辑）
├── exec_config_override_test.go     # -C/--config-override key=value 解析和应用测试
├── exec_event_processor_test.go     # 事件处理器接口测试
├── exec_event_jsonl_test.go         # JSONL 序列化和输出格式测试
├── exec_event_human_test.go         # 人类输出格式测试（TTY/非 TTY）
├── exec_events_test.go              # 事件类型 JSON 序列化测试
├── exec_event_bridge_test.go        # 事件桥接转换测试
├── exec_resume_test.go              # resume 子命令测试
├── exec_review_test.go              # review 子命令测试（含 diff 获取和 prompt 构建）
└── exec_review_prompt_test.go       # buildReviewPrompt 单元测试
```

### 6.2 集成测试

```bash
# 基本执行
aicli exec "echo hello" --output json

# 管道输入 + 指令组合
echo "func add(a, b int) int { return a - b }" | aicli exec -p "找出 bug"

# 超时控制
aicli exec --timeout 10s "执行一个长任务"

# JSONL 事件流
aicli exec --json "创建一个 Hello World 程序"

# 会话恢复
aicli exec resume --last --json
aicli exec resume --last "继续上次的任务"

# 代码审查
aicli exec review --uncommitted --json
aicli exec review --base main -o review_result.md

# 配置覆盖
aicli exec -C model=gpt-4 -C provider.base_url=http://localhost:8080 "test"
```

### 6.3 JSONL 输出验证

```bash
# 验证 JSONL 格式（每行一个有效 JSON）
aicli exec "test" --json 2>stderr.log | while read line; do echo "$line" | jq . > /dev/null || echo "INVALID: $line"; done

# 验证事件类型完整性
aicli exec "test" --json 2>stderr.log | jq -r '.type' | sort -u
# 期望输出：
# thread.started
# turn.started
# turn.completed (或 turn.failed)

# 验证工具调用事件（需要触发工具调用的 prompt）
aicli exec --yolo "创建文件 /tmp/test_exec.txt 内容为 hello" --json 2>stderr.log | jq 'select(.type | startswith("item."))'
```

### 6.4 必测契约

- `--json` 与 `--output json` 同时出现时返回参数错误，exit code 为 2。
- `--output-last-message` 在默认 text、`--output json` 和 `--json` 三种模式下都写入最终 assistant 文本。
- `--ephemeral` 不创建、不写入 runtime session；`exec resume --ephemeral` 返回参数错误。
- `--profile/--agent`、`--provider/--model`、`--reasoning-effort`、`--approval-reuse` 与 `chat --no-interactive` 行为等价。
- `exec resume --last` 无 prompt 时只输出恢复摘要/最后 assistant 消息，不发送新 turn；有 prompt 时继续执行一轮。
- `exec review "检查安全漏洞"` 默认审查未提交更改，并把字符串作为审查指令；`--uncommitted/--base/--commit` 三者互斥。
- review 未提交变更测试同时覆盖 staged、unstaged 和 untracked 文件，并覆盖 diff 截断 warning。
- `--output-schema` 校验失败输出 `turn.failed` + `error`，exit code 为 3。
- JSONL stdout 每行都是合法 JSON；stderr 不参与 JSONL 校验。

## 7. 实施时间表

| 阶段 | 内容 | 预计工作量 | 依赖 |
|------|------|------------|------|
| Phase 1 | 核心 exec 框架 | 2-3 天 | 无 |
| Phase 2 | 事件处理器抽象层 | 2 天 | Phase 1 |
| Phase 3 | exec 主逻辑实现 | 3 天 | Phase 1, 2 |
| Phase 4 | resume/review 子命令 | 2-3 天 | Phase 3 |
| Phase 5 | 注册和文档 | 1 天 | Phase 4 |
| **总计** | | **10-12 天** | |

## 8. 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| 与现有 `chat` 命令逻辑重复 | 维护成本高 | 复用 `bootstrapChatSession`，不重新实现会话构建 |
| JSONL 事件格式不稳定 | 下游依赖破坏 | 版本化事件格式，保持向后兼容 |
| 会话恢复失败 | 用户体验差 | 提供友好的错误信息和回退策略 |
| 性能问题（大量事件） | 响应延迟 | 异步事件处理，缓冲输出 |
| `chatCommandOptions` 字段不完全匹配 | exec 无法传递所有参数 | 仅映射 exec 需要的字段，其余使用零值（NoInteractive=true 路径已验证） |
| 事件桥接与 actor executor 耦合 | 重构 actor 时需同步更新 | 桥接层使用接口隔离，actor 变更不影响事件格式 |
| review 子命令依赖 git CLI | 非 git 仓库无法使用 | 启动时检测 git 可用性，提前报错 |
| 大 diff 超出模型上下文窗口 | 审查结果不完整 | 自动截断 diff 并警告，或分段审查 |

## 9. 未来扩展

1. **`aicli exec batch`**：批量执行多个提示词
2. **`aicli exec watch`**：监控文件变化并自动执行
3. **`aicli exec pipeline`**：多阶段执行管道
4. **事件回放**：记录和回放执行过程
5. **远程执行**：通过 API 远程执行
