# Demo Testing with `spawn_agent` / `spawn_subagents`

> 面向开发者 / 维护者：如何在本地用 `aicli` 做多 agent demo 测试。
> 相关实现：`backend/internal/toolbroker`（`spawn_agent`）、`backend/internal/agent`（`spawn_subagents`）。
> 相关概念：portable AgentDefinition、`agent_type`、permission 模式 —— 详见 [`aicli/agents.md`](./aicli/agents.md)。

本文介绍如何用 `spawn_agent` 与 `spawn_subagents` 做**演示 / 验证性**的多 agent 测试，包括一个可直接运行的最小示例。这些测试用于验证子 agent 输出隔离、并行调度、等待语义与中断语义，不修改任何源码。

---

## 1. 适用场景

- **验证多 agent 输出隔离**：确认子 agent 的输出与事件各自独立，不串扰。
- **验证并行调度**：一次派发多个子 agent，检查并发与 `difficulty` 路由。
- **验证等待 / 中断语义**：`wait_agent` 收敛、`Ctrl+C` 取消活跃子 agent。
- **CI / headless 回归**：用 `aicli exec` / `aicli chat --no-interactive` 驱动，JSON 输出便于断言。

---

## 2. 前提

1. `aicli` 已安装并在 `PATH` 中：

   ```powershell
   aicli version
   ```

2. 至少一个 provider 已配置并通过探活：

   ```powershell
   aicli doctor provider
   ```

3. 如需工具调用，显式开启（headless 默认关闭 tools/skills）：

   ```powershell
   aicli exec --enable-tools "..."
   ```

---

## 3. 工具一览

| 工具 | 用途 | 备注 |
| --- | --- | --- |
| `spawn_agent` | 派发单个子 agent 会话 | 异步执行；返回 `agent_id` |
| `spawn_subagents` | 批量派发多个子 agent 任务 | 异步；返回 batch handle + 幂等键 |
| `wait_agent` | 等待一个或多个子 agent 就绪 | `ready_count` / `status` |
| `read_agent_events` | 读取子 agent 的事件流 | `session_start` → `assistant_message` → `session_end` |
| `list_agents` | 列出当前子 agent 会话 | 用于发现已 spawn 的 id |
| `close_agent` | 关闭子 agent 会话 | 释放资源 |
| `subagent_status` | 带 supervision 行的状态快照 | `action_required` / `allowed_actions` |
| `subagent_inspect_task` | 深入查看单个子任务 | bounded result projection |

> `spawn_agent` 与 `spawn_subagents` 都是**异步派发**：调用后父 agent 不阻塞，需随后用 `wait_agent` / `read_agent_events` 收敛。

---

## 4. `spawn_agent` — 单个子 agent 示例

### 4.1 最小调用

```json
{
  "agent_type": "explore",
  "message": "Find where permission ModePlan is enforced"
}
```

- `agent_type` 引用 portable AgentDefinition（builtin `explore` / `plan` / `general`，或 `.agents/agents/*.md`）。
- 未显式给出的字段由 def 补齐：`permission_mode`（explore → `plan`）、`read_only`（sandbox read-only → true）、`model` / `provider` / `reasoning_effort`。
- **显式参数永远赢**：

```json
{
  "agent_type": "explore",
  "permission_mode": "default",
  "read_only": false,
  "message": "..."
}
```

### 4.2 驱动命令（非交互）

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli.exe chat --provider opencode.ai --model deepseek-v4.1-flash --reasoning-effort max --no-interactive --request-timeout 420s --message "请使用 spawn_agent 启动一个子 agent，difficulty 设为 normal，让它总结当前目录的结构。spawn_agent 后调用 wait_agent 等待，再调用 read_agent_events 读取事件，最后用不超过 120 字中文汇总。"
```

---

## 5. `spawn_subagents` — 批量子 agent 示例

`spawn_subagents` 一次派发多任务，调度器信号量并发（默认每批 4）。每个任务条目支持 `agent_type`、`message`、`difficulty`、`budget_tokens`、`timeout` 等。

### 5.1 批量调用

```json
{
  "tasks": [
    {
      "agent_type": "explore",
      "message": "列出 backend/internal 下的 top-level 包",
      "difficulty": "normal"
    },
    {
      "agent_type": "explore",
      "message": "找到 spawn_agent 的 entrypoint",
      "difficulty": "normal"
    }
  ]
}
```

- 解析失败不会拒绝整批：任务照跑，并在回执 `route_warnings` 写入 `agent_type_not_found:<name>`。
- 显式 `tools_whitelist` / `model` / `provider` / `reasoning_effort` 永远赢于 def 默认。

### 5.2 驱动命令（非交互）

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
.\aicli.exe chat --provider opencode.ai --model deepseek-v4.1-flash --reasoning-effort max --no-interactive --request-timeout 420s --message "请使用 spawn_subagents 并行启动 2 个子 agent（difficulty=normal），分别完成：A 总结当前目录结构；B 定位 spawn_agent entrypoint。两个子任务都不要读取文件。spawn_subagents 后调用 wait_agent 等待，再调用 read_agent_events 读取两个子 agent 事件，最后 parent 用不超过 120 字中文汇总。"
```

---

## 6. 最小可运行示例（端到端）

以下示例在 `aicli chat --no-interactive` 下驱动父 agent 完成一次完整的 spawn → wait → read → close 流程。

```powershell
cd E:\projects\ai\ai-agent-runtime\backend

.\aicli.exe chat `
  --provider opencode.ai `
  --model deepseek-v4.1-flash `
  --reasoning-effort max `
  --no-interactive `
  --request-timeout 420s `
  --message "请按以下步骤操作：
1) 使用 spawn_agent 启动一个子 agent，agent_type=explore，difficulty=normal，message='用一句话描述当前目录的作用'。
2) 调用 list_agents 查看子 agent。
3) 调用 wait_agent 等待其完成。
4) 调用 read_agent_events 读取子 agent 的事件流。
5) 最后用不超过 80 字中文汇总子 agent 的输出。
不要向用户提问。"
```

**预期输出要点**：

- `spawn_agent` 返回 `agent_id`（运行时自动分配）。
- `list_agents` 显示 1 个活跃子会话。
- `wait_agent` 返回 `ready_count=1`，`status=idle`。
- `read_agent_events` 显示 `session_start → assistant_message → session_end`，`turn success=true`。
- 父 agent 给出 ≤80 字汇总。

---

## 7. headless / CI 建议

- **JSONL 事件流**：`aicli exec --json "..."`，每行一个 JSON 事件，便于脚本解析 `item.completed` / `turn.completed`。
- **最终 JSON**：`aicli exec --output json "..."`，返回单个 JSON 对象（含 `status` / `message` / `usage`）。
- **退出码约定**：`aicli exec` 遵循稳定退出码约定（`0` 成功、`3` schema 校验失败）；详见 [`aicli/exec.md`](./aicli/exec.md#九退出码)。
- **超时**：`--timeout` 控制整次 wall-clock，`--request-timeout` 控制单次 LLM 请求。
- **临时会话**：`--ephemeral` 不写持久化 session，适合 CI 回归。

```powershell
# CI 回归：批量子 agent + JSON 输出
aicli exec --json --ephemeral --timeout 10m "使用 spawn_subagents 并行启动 2 个 explore 子 agent，分别完成任务 A 和 B，随后 wait_agent + read_agent_events，最后汇总。"
```

---

## 8. 常见陷阱

| 现象 | 原因 / 解决 |
| --- | --- |
| `spawn_agent` 后父 agent 阻塞 | 子 agent 异步执行，必须显式 `wait_agent` 收敏 |
| 子 agent 无产出 | provider 不可达或 `reasoning_effort` 超能力；检查 `route_warnings` 与 `agent_control.sqlite` |
| `agent_type` 未找到 | 解析失败不拒绝整批，回执中 `route_warnings` 会写 `agent_type_not_found:<name>`；检查 `.agents/agents/` 与 `aicli agents list` |
| 权限被钉住 | 父 `plan`/`dont_ask` 模式会钉住子 agent；`bypass` 父会钉住 `default`/`accept_edits` 子 |
| 输出串扰 | 应为隔离设计；如遇请检查 `read_agent_events` 的 `thread_id` / `session_id` |

---

## 9. 相关文档

- Portable AgentDefinition 与 `agent_type` 详解：[`aicli/agents.md`](./aicli/agents.md)
- headless exec 输出契约与退出码：[`aicli/exec.md`](./aicli/exec.md)
- 多 agent 架构设计：[`multi-agents/README.md`](./multi-agents/README.md)
- 历史真实 provider 验证记录：[`working/multi-agent-real-terminal-validation-*.md`](./working/)
