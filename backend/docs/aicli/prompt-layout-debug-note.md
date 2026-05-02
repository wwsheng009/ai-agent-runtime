# `aicli` prompt layout 调试记录

## 现象

终端在 `llm.request.started` 阶段会短暂输出类似下面的诊断行：

```text
[prompt] layers=unknown/system (instruction 912 / total 918 tokens, 3625 / 3634 chars)
```

这类信息会干扰主界面显示，不适合作为默认交互输出。

## 根因

问题来自 `backend/cmd/aicli/commands/chat_runtime_events.go` 中的 prompt layout 渲染分支。

该分支会读取 `prompt_layout_summary`、`instruction_tokens`、`total_tokens`、`prompt_layout_length`、`total_message_chars` 等字段，并将它们拼成 `[prompt] ...` 行直接写到终端。

## 处理

已经将 `chatLLMRequestPromptLayoutHint()` 改为直接返回空字符串。

处理后的效果是：

- 保留事件 payload，便于后续内部观测和排查；
- 不再向 CLI / TUI 默认输出 `[prompt] ...` 诊断行；
- 不影响上下文 token 统计和会话处理逻辑。

## 调试文件

现在运行时事件会把每轮的 prompt 布局摘要和 token 统计写入会话 `debug.log`，日志行以 `[llm-debug] request_started` 和 `[llm-debug] request_finished` 开头，便于在不污染主界面的前提下排查：

- prompt layout summary
- prompt layout token/char 统计
- 每轮 `usage_total_tokens`
- 缓存 token / reasoning token 等附加 usage 信息

## 验证

已通过以下测试：

```bash
go test ./cmd/aicli/commands -count=1
```
