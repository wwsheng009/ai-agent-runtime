# Skill Runtime Docs

> 迁移说明（2026-03-30）：
> - 本目录描述的 runtime/skills/session/team 能力，当前已由独立仓库 `E:\projects\ai\ai-agent-runtime\backend` 承载
> - `ai-gateway` 已不再挂载 `/api/agent`、`/api/skills`
> - 本目录仍保留运行时设计、契约和历史分析文档；凡出现旧 `internal/runtime/*`、`internal/api/skills/*` 路径，请按 `ai-agent-runtime/backend/internal/...` 理解

## Overview

`skill_runtime` in this repository is no longer just an experimental `coding agent` idea.

Its current target is a general AI platform built around:

- `skill`
- `agent`
- `tool`
- `workflow`
- `mcp`
- `llm`
- `session`

The current implementation status is:

- main runtime path is available
- standalone `runtime-server` is available
- gateway-hosted runtime integration has been removed
- `skills API` is available in `ai-agent-runtime`
- `aicli chat` can expose skills as AI-callable functions
- governance, usage/quota, search monitoring, and source layering are documented

执行入口说明：

- 独立 runtime 业务侧统一入口：`POST /api/agent/chat`
- `POST /api/skills/agent/chat` 为 compatibility alias
- `POST /api/skills/{name}/execute` 为 admin/debug（非主入口）
- Team task outcome 统一入口：`POST /api/skills/teams/{id}/tasks/{task_id}/outcome`
- `POST /api/skills/teams/{id}/tasks/{task_id}/complete|fail|block` 为 compatibility alias

推荐启动方式：

```bash
cd E:\projects\ai\ai-agent-runtime\backend
go run ./cmd/runtime-server --listen 127.0.0.1:8081
```

For the latest implementation status, start with:

- [Current Architecture](./current_architecture.md)
- [Progress](./plan/PROGRESS.md)
- [Implementation TODO](./plan/IMPLEMENTATION_TODO.md)
- [Sandbox Execution Plan](./sandbox_execution_plan.md)

## Recommended Reading Order

### If you want the fastest orientation

1. [Current Architecture](./current_architecture.md)
2. [Progress](./plan/PROGRESS.md)
3. [Platform Runtime Roadmap](./platform_runtime_roadmap.md)
4. [Sandbox Execution Plan](./sandbox_execution_plan.md)

### If you want to use the runtime

1. [Skills API Result Contract](./skills_api_result_contract.md)
2. [Skills API Stream Contract](./skills_api_stream_contract.md)
3. [Session Agent HTTP API](./session_agent_api.md)
4. [Skills API Client](./skills_api_client.md)
5. [Team Task Outcome Contract](./team_task_outcome_contract.md)
6. [Skill Invocation Mechanism](./skill_invocation_mechanism.md)
7. [AICLI Skills Usage](./aicli_skills_usage.md)

### If you want to operate or govern it

1. [Governance API Guide](./governance_api_guide.md)
2. [Skills Usage Quota](./skills_usage_quota.md)
3. [Search Monitoring Guide](./search_monitoring_guide.md)
4. [Search Monitoring Playbook](./search_monitoring_playbook.md)
5. [Skill Sources And Persistence](./skill_sources_and_persistence.md)

### If you want the original design context

- [Design Drafts](./design/)
- [Consolidated Design](./design2/skills_runtime_design.md)

## Document Map

### Architecture And Planning

- [Current Architecture](./current_architecture.md)
  - Current runtime layering, component relationships, startup flow, execution flow, and design alignment.
- [Platform Runtime Roadmap](./platform_runtime_roadmap.md)
  - Current platform direction and next-stage priorities.
- [Progress](./plan/PROGRESS.md)
  - Detailed implementation history and milestone log.
- [Implementation TODO](./plan/IMPLEMENTATION_TODO.md)
  - Remaining work and plan tracking.
- [Sandbox Execution Plan](./sandbox_execution_plan.md)
  - Incremental plan for turning archived sandbox design into runtime implementation without overstating MCP isolation.

### API And Integration

- [Skills API Result Contract](./skills_api_result_contract.md)
  - Non-streaming response schema for `ExecuteSkill` and `AgentChat`.
- [Skills API Stream Contract](./skills_api_stream_contract.md)
  - SSE event contract for streaming agent/skill execution.
- [Session Agent HTTP API](./session_agent_api.md)
  - Lightweight child-agent control plane for session-scoped spawn/input/wait/events/close/resume.
- [Skills API Client](./skills_api_client.md)
  - Go client usage for `skills API`.
- [Team Task Outcome Contract](./team_task_outcome_contract.md)
  - Canonical `/outcome` API, compatibility aliases, and broker outcome reporting rules.
- [Skill Invocation Mechanism](./skill_invocation_mechanism.md)
  - How AI becomes aware of skills, how skills are routed or exposed, and how `prompt` is consumed across the invocation path.
- [AICLI Skills Usage](./aicli_skills_usage.md)
  - How `aicli chat` exposes skills as functions, including `skills-top-k`, `skills-mode`, and `skills-debug`.

### Governance, Delivery, And Operations

- [Governance API Guide](./governance_api_guide.md)
  - Mutation policy, auth policy, usage policy, and governance monitoring surfaces.
- [Skills Usage Quota](./skills_usage_quota.md)
  - Usage tracking, quota model, scope resolution, and policy behavior.
- [Search Monitoring Guide](./search_monitoring_guide.md)
  - Search telemetry, runtime admin endpoints, optional external metrics export, and operational checks.
- [Search Monitoring Playbook](./search_monitoring_playbook.md)
  - Troubleshooting guide for search admin and semantic search issues.
- [Skill Sources And Persistence](./skill_sources_and_persistence.md)
  - `system / external / runtime` source layering, persistence rules, and reload behavior.

### Skill Packs

- [System Skills](./skills/README.md)
  - Rules for what should and should not live in the built-in system skill directory.

Current built-in system skills:

- [skill_runtime_smoke](./skills/skill_runtime_smoke/skill.yaml)
- [run_shell_command](./skills/run_shell_command/skill.yaml)
- [fetch_url_content](./skills/fetch_url_content/skill.yaml)
- [view_file_content](./skills/view_file_content/skill.yaml)

### Design History

- [Design Folder](./design/)
  - Split design drafts `agent_skill_runtime-1.md` to `agent_skill_runtime-16.md`.
- [skills_runtime_design.md](./design2/skills_runtime_design.md)
  - Large consolidated design reference.

## How The Docs Fit Together

- `current_architecture.md` answers: what exists now
- `platform_runtime_roadmap.md` answers: where it is going
- `plan/PROGRESS.md` answers: what has already landed
- `plan/IMPLEMENTATION_TODO.md` answers: what still remains
- `skills_api_*` answers: how clients integrate
- `session_agent_api.md` answers: how to control lightweight child sessions over HTTP
- `team_task_outcome_contract.md` answers: how Team task outcomes are reported consistently across HTTP and broker tools
- `skill_invocation_mechanism.md` answers: how AI sees skills and how a skill call is actually executed
- `governance_*`, `skills_usage_quota.md`, and `search_monitoring_*` answer: how operators manage and observe it
- `skill_sources_and_persistence.md` answers: where skills come from and how they are stored
- `aicli_skills_usage.md` answers: how the CLI path works today

## Notes

- There are two layers of design material:
  - `design/` is the split historical design set
  - `design2/skills_runtime_design.md` is the larger consolidated design
- The `platform_runtime_roadmap.md`, `plan/`, `design/`, and `design2/` documents now live under `docs/skill_runtime/` so the active doc tree matches the links exposed by this README.
- The repository does not treat coding-only capabilities as the whole platform anymore; they are considered vertical packs on top of the runtime.
