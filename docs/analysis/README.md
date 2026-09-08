# Analysis 分析归档

本目录存放故障现场分析、复盘（post-mortem）、方案评审与专项调研文档。现场原始资料（如 goroutine dump）与对应分析报告存放在一起，便于复现核查。

## 文件清单

| 文件 | 主题 | 日期 |
| --- | --- | --- |
| `renderer-stall-analysis-20260901.md` | 会话主屏幕统一渲染器停止更新（scrollback 冻结死锁）现场分析 | 2026-09-01 |
| `goroutine-dump-20260901-1904.txt` | 上述分析的 goroutine dump 原始数据（数据源） | 2026-09-01 |
| `aicli-resume-recovery-backoff-postmortem.md` | resume 恢复回退（recovery backoff）三层架构缺陷复盘 | 2026-08-31 |
| `aicli-resume-input-not-reaching-llm-analysis.md` | resume 后输入 prompt 不进入 LLM 请求交互：架构分析 | 2026-08-31 |
| `aicli-chat-unified-rendering-survey.md` | aicli 聊天统一渲染链路梳理：事件流标识与块缩进 | 2026-08-08 |
| `aicli-tui-owned-render-simplification-plan-review.md` | aicli TUI owned 渲染简化方案评审 | 2026-08-06 |
| `grok-build-architecture-learning.md` | Grok Build 架构分析与对本项目的学习建议 | 2026-07-25 |
| `aicli-session-optimization-plan.md` | aicli 会话失败分析与优化方案 | 2026-07 |
| `aicli-chat-model-switch-runtime-analysis.md` | aicli chat 运行时切换模型与 reasoning_effort 可行性分析 | 2026-04-29 |
| `openssh-proxycommand-implementation.md` | OpenSSH ProxyCommand 实现分析与 Go 版 ssh/sftp 移植参考 | - |

## 关联文档（其他目录）

- `docs/plan/aicli-chat-unified-render-stall-analysis-and-hardening.md` — 统一渲染卡住加固方案与实施记录（对应 `renderer-stall-analysis-20260901.md` 的修复）
- `docs/architecture/aicli-chat-unified-renderer-architecture.md` — 统一渲染器架构总览
- `docs/aicli/debug-chat-status.md` — `/debug/chat/status` 调试端点说明
- `docs/unified-renderer-band-analysis.md` — Active Band 丢失问题分析

## 注意事项

- `.backups/` 为编辑器自动备份目录，不入库（见根 `.gitignore`）。