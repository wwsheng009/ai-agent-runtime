# `aicli` Docs

本目录存放 `aicli` 相关的实现说明、设计约束和调试笔记。

当前文档：

- `tool_output_contract.md`：工具输出在 runtime / CLI / LLM 三条链路中的契约，包括 `output_kind`、`tool_source`、JSON 文本化和 CLI 截断策略。
- `prompt-layout-debug-note.md`：`llm.request.started` 阶段 `[prompt] ...` 诊断行的关闭记录与验证结果。
