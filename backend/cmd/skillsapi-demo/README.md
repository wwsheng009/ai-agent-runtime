# skillsapi-demo

`skillsapi-demo` 是仓库里最小可运行的 `skills API` / `pkg/skillsapi` client 示例程序。

当前覆盖两类用法：

- `chat`
  - 调用 `AgentChat` / `AgentChatStream`
- `session-agent`
  - 调用轻量 child-agent 控制面：
    - `SpawnSessionAgent`
    - `GetSessionAgentStatus`
    - `SendSessionAgentInput`
    - `WaitSessionAgents`
    - `ListSessionAgentEvents`
    - `ListSessionAgentControlMailbox`
    - `CloseSessionAgent`
    - `ResumeSessionAgent`
- `agent-control`
  - 调用统一控制面 task graph 入口：
    - `ListAgentControlTasks`
    - `ListAgentControlTaskDependencies`
    - `ListAgentControlTaskGraphEvents`
    - `CreateAgentControlTaskDependency`

## 构建

```bash
go build ./cmd/skillsapi-demo
```

## Chat 模式

非流式：

```bash
go run ./cmd/skillsapi-demo -url http://127.0.0.1:8101 -message "plan this task"
```

流式：

```bash
go run ./cmd/skillsapi-demo -url http://127.0.0.1:8101 -message "stream this task" -stream -planning-mode planner_preferred
```

常用参数：

- `-url`
- `-message`
- `-session-id`
- `-user-id`
- `-workspace-path`
- `-planning-mode`
- `-stream`
- `-timeout`

## Session-Agent 模式

### 1. Spawn

若不传 `-parent-session-id`，demo 会先自动创建一个父 session。

```bash
go run ./cmd/skillsapi-demo \
  -mode session-agent \
  -agent-action spawn \
  -url http://127.0.0.1:8101 \
  -agent-type explorer \
  -fork-context
```

也可以在创建 child 时直接附带首条消息：

```bash
go run ./cmd/skillsapi-demo \
  -mode session-agent \
  -agent-action spawn \
  -url http://127.0.0.1:8101 \
  -parent-session-id <parent> \
  -agent-type explorer \
  -message "Summarize the parent context first."
```

### 2. Status

```bash
go run ./cmd/skillsapi-demo \
  -mode session-agent \
  -agent-action status \
  -url http://127.0.0.1:8101 \
  -parent-session-id <parent> \
  -agent-id <child>
```

### 3. Input

```bash
go run ./cmd/skillsapi-demo \
  -mode session-agent \
  -agent-action input \
  -url http://127.0.0.1:8101 \
  -parent-session-id <parent> \
  -agent-id <child> \
  -message "Summarize progress"
```

如果 child 正忙，需要显式中断：

```bash
go run ./cmd/skillsapi-demo \
  -mode session-agent \
  -agent-action input \
  -url http://127.0.0.1:8101 \
  -parent-session-id <parent> \
  -agent-id <child> \
  -message "Stop current work and summarize progress." \
  -interrupt
```

### 4. Wait

单个 child：

```bash
go run ./cmd/skillsapi-demo \
  -mode session-agent \
  -agent-action wait \
  -url http://127.0.0.1:8101 \
  -parent-session-id <parent> \
  -agent-id <child> \
  -agent-timeout-ms 10000
```

批量 child：

```bash
go run ./cmd/skillsapi-demo \
  -mode session-agent \
  -agent-action wait \
  -url http://127.0.0.1:8101 \
  -parent-session-id <parent> \
  -agent-ids child-a,child-b,child-c \
  -agent-timeout-ms 10000
```

### 5. Events

```bash
go run ./cmd/skillsapi-demo \
  -mode session-agent \
  -agent-action events \
  -url http://127.0.0.1:8101 \
  -parent-session-id <parent> \
  -agent-id <child> \
  -after-seq 0 \
  -limit 20 \
  -wait-ms 5000
```

父 session 的 AgentControl mailbox control rows 可通过独立入口读取，不会转换成 `mailbox_received` runtime event:

```bash
go run ./cmd/skillsapi-demo \
  -mode session-agent \
  -agent-action control-mailbox \
  -url http://127.0.0.1:8101 \
  -parent-session-id <parent> \
  -after-seq 0 \
  -limit 20 \
  -wait-ms 5000
```

### 6. Close / Resume

```bash
go run ./cmd/skillsapi-demo \
  -mode session-agent \
  -agent-action close \
  -url http://127.0.0.1:8101 \
  -parent-session-id <parent> \
  -agent-id <child>

go run ./cmd/skillsapi-demo \
  -mode session-agent \
  -agent-action resume \
  -url http://127.0.0.1:8101 \
  -parent-session-id <parent> \
  -agent-id <child>
```

## Agent-Control 模式

列出统一 task read-model：

```bash
go run ./cmd/skillsapi-demo \
  -mode agent-control \
  -control-action tasks \
  -url http://127.0.0.1:8101 \
  -team-id <team> \
  -limit 20
```

通过 AgentControl task update seam 更新 task summary：

```bash
go run ./cmd/skillsapi-demo \
  -mode agent-control \
  -control-action update \
  -url http://127.0.0.1:8101 \
  -team-id <team> \
  -task-id <task> \
  -message "updated summary"
```

读取 task dependency graph：

```bash
go run ./cmd/skillsapi-demo \
  -mode agent-control \
  -control-action dependencies \
  -url http://127.0.0.1:8101 \
  -team-id <team> \
  -task-id <task> \
  -include-dependents
```

读取 AgentControl task graph event cursor：

```bash
go run ./cmd/skillsapi-demo \
  -mode agent-control \
  -control-action events \
  -url http://127.0.0.1:8101 \
  -team-id <team> \
  -after-seq 0 \
  -limit 20
```

通过 AgentControl task graph seam 写入依赖边：

```bash
go run ./cmd/skillsapi-demo \
  -mode agent-control \
  -control-action add-dependency \
  -url http://127.0.0.1:8101 \
  -team-id <team> \
  -task-id <task> \
  -depends-on-id <dependency-task>
```

读取 runtime/session 与 team 组合后的 AgentControl mailbox registry：

```bash
go run ./cmd/skillsapi-demo \
  -mode agent-control \
  -control-action mailbox \
  -url http://127.0.0.1:8101 \
  -team-id <team> \
  -session-id <session> \
  -after-seq 0 \
  -limit 20
```

## 输出风格

`skillsapi-demo` 默认输出面向终端阅读的摘要，而不是原始 JSON：

- chat 模式输出 `kind/source/output/usage/planning` 等摘要
- session-agent 模式输出 `parent_session/agent_session/status/output/events` 等摘要
- agent-control 模式输出 `agent_control_tasks`、`agent_control_task_updated`、`agent_control_dependencies`、`agent_control_mailbox` 或依赖写入摘要

如果你需要 HTTP 字段级契约，请看：

- `docs/skill_runtime/session_agent_api.md`
- `docs/skill_runtime/skills_api_client.md`

## 认证

可选认证参数：

- `-admin-token`
- `-bearer-token`

例如：

```bash
go run ./cmd/skillsapi-demo \
  -url http://127.0.0.1:8101 \
  -message "hello" \
  -bearer-token <jwt>
```
