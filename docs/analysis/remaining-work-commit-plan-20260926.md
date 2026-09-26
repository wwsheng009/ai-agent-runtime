# 剩余未提交工作：按主题提交清单

- 快照时间：2026-09-27 07:0x（+08:00）｜快照 HEAD：`7c6f6494`
- 快照规模：36 个已跟踪修改 + 57 个未跟踪（合计 93 条）
- 快照时刻 `go build ./...` ✅（并发会话仍在写；每笔提交前请重新跑验证）
- 说明：清单按**主题**分组，每组一笔提交（conventional + 中文，与仓库近期风格一致）。
  组内文件互相依赖，跨组依赖见「提交顺序」。

## 提交顺序与依赖

```
P0 后台任务 wait/task_kill ──►（独立）
P1 后台任务终态监管投影 ────►（独立）
P2 provider schema/参数修复（独立）
P3 read/view 家族 ─────────► P4（图片直通依赖 toolresult/image.go）
P5 写路径安全/路径守卫（独立）
P6 grep/glob 分页（独立）
P7 shell/executor 迁移（独立）
P8 web_search domain（独立）
P9 分析文档（最后）
```

---

## P0 `feat(background): task_output 等待语义与 task_kill`

- M `backend/internal/background/manager.go`
- M `backend/internal/background/detached_test.go`
- M `backend/internal/toolbroker/broker.go`（wait 参数、task_kill 定义与执行路径）
- M `backend/internal/toolbroker/broker_arg_audit.go`（task_output/task_kill 参数键）
- M `backend/internal/toolbroker/broker_arg_kinds.go`（job_id/wait/timeout_ms/task_id/reason）
- ?? `backend/internal/toolbroker/broker_task_wait.go`
- ?? `backend/internal/toolbroker/broker_background_wait_test.go`
- M `backend/internal/policy/capability.go`（task_kill → CapBackgroundTask）
- M `backend/internal/policy/taxonomy.go`（task_kill Kind=Control）
- M `backend/internal/policy/tool_policy.go`
- M `backend/internal/policy/grants.go`

验证：`go test ./internal/background/ ./internal/toolbroker/ -count=1`
（重点：`TestBrokerTaskOutputWait*`、`TestBrokerTaskKill*` 等）

## P1 `feat(supervision): 后台任务终态投影为 inbox 通知 + 自动唤醒来`

- M `backend/internal/supervision/types.go`
- ?? `backend/internal/supervision/background_job.go`
- ?? `backend/internal/supervision/background_job_test.go`
- M `backend/internal/api/runtimeapi/handler.go`（handleBackgroundEvent 接线）
- ?? `backend/internal/api/runtimeapi/background_supervision.go`
- ?? `backend/internal/api/runtimeapi/background_supervision_test.go`
- ?? `backend/cmd/aicli/commands/chat_actor_background_supervision.go`（host 装配 relay）
- ?? `backend/cmd/aicli/commands/chat_actor_background_supervision_test.go`

验证：`go test ./internal/supervision/ ./internal/api/runtimeapi/ -count=1`；
`go test ./cmd/aicli/commands/ -run 'Background' -count=1`

## P2 `fix(tools): provider 联合 schema 降级与参数修复披露`

- ?? `backend/internal/toolschema/unions.go`
- ?? `backend/internal/toolschema/unions_test.go`
- M `backend/internal/llm/mcp_meta_tools_convert.go`（非 Codex 协议走 ScalarizeUnions）
- M `backend/internal/tools/argument_aliases.go`（toolkitArgRepairNotes）
- ?? `backend/internal/tools/argument_aliases_repair_test.go`
- M `backend/internal/tools/manager.go`（executeLocalToolkitTool 统一入口）
- ?? `backend/internal/tools/union_contract_test.go`

验证：`go test ./internal/toolschema/ ./internal/tools/ ./internal/llm/ -count=1`

## P3 `feat(tools/view): 文档/图像/notebook 读取与去重账本`

- M `backend/internal/toolkit/tools/view.go`、`view_test.go`
- ?? `view_document.go(+test)`、`view_image.go(+3 tests)`、`view_notebook.go(+test)`
- ?? `view_binary_note_test.go`、`view_read_hardening_test.go`、`view_spelling_heal_test.go`*
- ?? `read_dedup.go(+test)`、`read_ledger.go`、`document_guard.go(+test)`
- ?? `backend/internal/docread/`（detect/convert/render + 2 tests）
- ?? `backend/internal/ipynb/`（ipynb.go + test）
- ?? `backend/internal/toolresult/image.go`
- M `backend/internal/imageprep/imageprep.go`（坐标乘数披露）
- ?? `backend/internal/imageprep/imageprep_multiplier_test.go`

\* `view_spelling_heal_test.go` 依赖 pathrepair；若先合 P5，本文件可跟随 P3（按实际 import 决定）。

验证：`go test ./internal/toolkit/tools/ ./internal/docread/ ./internal/ipynb/ ./internal/toolresult/ -run 'View|Read|Document|Notebook|Image|Dedup' -count=1`

## P4 `feat(agent): 工具结果图片直通为图像消息`

- M `backend/internal/agent/message_builder.go`
- ?? `backend/internal/agent/tool_result_images.go`
- ?? `backend/internal/agent/tool_result_images_test.go`
- ?? `backend/internal/agent/tool_result_images_batch_test.go`

依赖：P3 的 `internal/toolresult/image.go`。验证：`go test ./internal/agent/ -run 'ToolResultImage|MessageBuilder' -count=1`

## P5 `feat(tools): 原子写与路径守卫（编码/拼写/设备路径/编辑歧义）`

- ?? `atomic_write.go`、`file_encoding.go`、`write_safety_test.go`、`edit_ambiguity_test.go`
- ?? `path_guard.go(+test)`、`path_spelling.go(+test)`、`device_path_deny_test.go`
- M `write.go`、`append_write.go`、`edit.go`、`multiedit.go`
- ?? `backend/internal/pathrepair/pathrepair.go`
- M `backend/internal/toolexec/preflight.go`、?? `preflight_spelling_test.go`
- M `backend/internal/toolkit/tools/sandbox_support.go`（设备/流路径拒绝门）

验证：`go test ./internal/toolkit/tools/ ./internal/pathrepair/ ./internal/toolexec/ -run 'Write|Path|Spelling|Device|Edit|Preflight' -count=1`

## P6 `feat(tools): grep/glob 分页、部分结果与输出预算`

- M `grep.go`、`glob.go`、`tool_output_budget.go(+test)`、`tool_budget_l4_render_test.go`
- ?? `grep_pagination_test.go`、`glob_pagination_test.go`、`grep_partial_test.go`

验证：`go test ./internal/toolkit/tools/ -run 'Grep|Glob|Budget' -count=1`

## P7 `feat(shell): 退出码/编码处理与 shell→grep 迁移`

- M `backend/internal/toolkit/tools/bash.go`、`execute_shell_command.go`、`aicli_exec.go`
- ?? `backend/internal/toolkit/tools/bash_exit_status_test.go`
- M `backend/internal/executor/shell_detect.go`、?? `shell_detect_test.go`
- M `backend/cmd/aicli/functions/shell.go`（PowerShell UTF-8 前缀）
- ?? `docs/tools/shell-grep-migration.md`
- M `docs/aicli/install.md`（提交前确认改动内容确属本主题）

验证：`go test ./internal/executor/ ./internal/toolkit/tools/ ./cmd/aicli/functions/ -run 'Shell|Bash|Exec' -count=1`

## P8 `feat(web_search): 域解析与结果处理`

- M `backend/internal/toolkit/tools/web_search.go`
- ?? `web_search_domains.go`、`web_search_domains_test.go`

验证：`go test ./internal/toolkit/tools/ -run 'WebSearch' -count=1`

## P9 `docs(analysis): commandcode 工具设计借鉴与 subagent 会话 postmortem`

- ?? `docs/analysis/commandcode-read-tool-design-borrowing-20260926.md`
- ?? `docs/analysis/commandcode-tools-design-borrowing-20260926.md`
- ?? `docs/analysis/session-20260926205017-subagent-runtime-postmortem-20260926.md`

---

## 执行注意

1. **并发写入**：另一会话仍在提交（快照后 HEAD 已多次前进）。每笔提交前重跑
   `git status` 与对应包测试，确认没有新改动混入同文件。
2. **禁止 `git add -A`**：本清单只列应提交路径；其余路径（若有新增）先归类再提交。
3. **每笔提交后**：`gofmt -l <改动文件>`、对应包 `go test -count=1`、必要时 `go build ./...`。
4. 本文件本身可单独作为 `docs(analysis): 剩余工作提交清单` 提交，或直接删除。
