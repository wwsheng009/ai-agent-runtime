# skillsapi-demo 使用手册

> 对应程序：`cmd/skillsapi-demo`（`skillsapi-demo` / `skillsapi-demo.exe`）
> 作用：Skills API 的交互式演示客户端，用于测试 runtime-server 的 Skills API 端点（Chat / Session Agent / Agent Control）。

---

## 目录

1. [概述](#1-概述)
2. [模式与用法](#2-模式与用法)
3. [参数](#3-参数)
4. [模式详解](#4-模式详解)
5. [示例](#5-示例)
6. [相关文档](#6-相关文档)

---

## 1. 概述

`skillsapi-demo` 是面向 `runtime-server` Skills API 的 CLI 演示工具，支持三种模式：

| 模式（`--mode`） | 说明 |
|------------------|------|
| `chat` | 发送一次对话消息（单轮，非流式或流式） |
| `session-agent` | 子 Agent 生命周期操作：spawn / status / input / wait / events / control-mailbox / close / resume |
| `agent-control` | 父 Agent 控制操作：mailbox / tasks / update / events / dependencies / add-dependency |

---

## 2. 用法

```text
skillsapi-demo --mode chat|session-agent|agent-control [options]
```

---

## 3. 参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--mode` | `chat` | 演示模式：`chat`、`session-agent`、`agent-control` |
| `--url` | `http://127.0.0.1:8101` | Skills API 基础 URL |
| `--message` | `""` | 用户消息内容 |
| `--session-id` | `""` | 已有会话 ID |
| `--parent-session-id` | `""` | session-agent 模式的父会话 ID |
| `--agent-id` | `""` | session-agent 模式的子 agent ID |
| `--agent-ids` | `""` | 逗号分隔的子 agent ID 列表（批量 wait） |
| `--agent-action` | `spawn` | session-agent 动作：spawn/status/input/wait/events/control-mailbox/close/resume |
| `--control-action` | `tasks` | agent-control 动作：mailbox/tasks/update/events/dependencies/add-dependency |
| `--agent-type` | `""` | 子 agent 类型（spawn 时） |
| `--agent-model` | `""` | 子 agent 模型（spawn 时） |
| `--team-id` | `""` | agent-control 的 team ID |
| `--task-id` | `""` | agent-control 的任务 ID |
| `--depends-on-id` | `""` | agent-control 的依赖任务 ID |
| `--user-id` | `skillsapi-demo` | 用户 ID |
| `--tenant-id` | `""` | 租户 ID |
| `--project-id` | `""` | 项目 ID |
| `--workspace-path` | `""` | 工作区路径 |
| `--admin-token` | `""` | 管理员令牌 |
| `--bearer-token` | `""` | Bearer 令牌 |
| `--planning-mode` | `""` | 规划模式（agent-control spawn 时） |
| `--max-steps` | `0` | 最大 agent 步数（0=无限制） |
| `--timeout` | `60s` | 请求超时 |
| `--agent-timeout-ms` | `30000` | 传递给 session-agent wait 的超时（毫秒） |
| `--after-seq` | `0` | session-agent events 的起始事件序列号 |
| `--limit` | `20` | session-agent events 结果数 |
| `--wait-ms` | `0` | session-agent events 长轮询等待毫秒数 |
| `--include-dependents` | `false` | agent-control 依赖关系读取时包含依赖边 |
| `--stream` | `false` | 启用流式模式 |
| `--fork-context` | `false` | spawn 子 agent 时复制父会话历史 |
| `--interrupt` | `false` | 发送新输入前中断繁忙的子 agent |
| `--enable-routing` | `true` | 启用技能路由 |
| `--execute-planned-subagents` | `false` | 自动执行已规划的子 agent |
| `--allow-write-planned-subagents` | `false` | 允许已规划的写入型子 agent |

---

## 4. 模式详解

### 4.1 Chat 模式（`--mode chat`）

发送一次对话消息，输出 LLM 的回复。可选 `--stream` 启用流式输出。

```bash
skillsapi-demo --mode chat --message "你好" --stream
```

### 4.2 Session Agent 模式（`--mode session-agent`）

子 Agent 生命周期操作，通过 `--agent-action` 指定：

| 动作 | 必填参数 | 说明 |
|------|----------|------|
| `spawn` | `--parent-session-id` | 创建子 Agent |
| `status` | `--parent-session-id`, `--agent-id` | 查询子 Agent 状态 |
| `input` | `--parent-session-id`, `--agent-id`, `--message` | 向子 Agent 发送消息 |
| `wait` | `--parent-session-id` | 等待子 Agent 就绪 |
| `events` | `--parent-session-id` | 读取子 Agent 事件 |
| `control-mailbox` | `--parent-session-id` | 读取控制 mailbox 事件 |
| `close` | `--parent-session-id`, `--agent-id` | 关闭子 Agent |
| `resume` | `--parent-session-id`, `--agent-id` | 恢复子 Agent |

### 4.3 Agent Control 模式（`--mode agent-control`）

父 Agent 控制操作，通过 `--control-action` 指定：

| 动作 | 必填参数 | 说明 |
|------|----------|------|
| `mailbox` | — | 读取 mailbox |
| `tasks` | — | 列出任务 |
| `update` | `--task-id`, `--message` | 更新任务状态 |
| `events` | — | 读取事件 |
| `dependencies` | `--task-id` | 读取任务依赖 |
| `add-dependency` | `--task-id`, `--depends-on-id` | 添加任务依赖 |

---

## 5. 示例

```bash
# Chat 单轮对话
skillsapi-demo --mode chat --message "Hello, what can you do?" --stream

# 创建子 Agent
skillsapi-demo --mode session-agent --agent-action spawn --parent-session-id "parent-1"

# 向子 Agent 发送消息
skillsapi-demo --mode session-agent --agent-action input \
  --parent-session-id "parent-1" --agent-id "child-1" \
  --message "请列出当前目录"

# 读取子 Agent 事件
skillsapi-demo --mode session-agent --agent-action events --parent-session-id "parent-1"

# 读取 mailbox
skillsapi-demo --mode agent-control --control-action mailbox

# 列出任务
skillsapi-demo --mode agent-control --control-action tasks
```

---

## 6. 相关文档

- [docs/skill_runtime/session_agent_api.md](../skill_runtime/session_agent_api.md) — Session Agent API 文档
- [docs/skill_runtime/runtime_operations_api.md](../skill_runtime/runtime_operations_api.md) — Runtime 操作 API
- [runtime-server 使用手册](runtime-server.md) — 启动服务端