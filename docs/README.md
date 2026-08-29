# Runtime Docs

This directory hosts the runtime-side documentation migrated from `ai-gateway`.

Current source sync note (2026-05-09):

- Treat `backend/internal/api/skills/handler.go` as the HTTP route source of truth.
- The current standalone runtime exposes `POST /api/agent/chat` plus `/api/runtime/*`.
- Skill management, search, usage, governance, sessions, teams, agent-control, logs, background jobs, generated images, and runtime config are under `/api/runtime/...`.
- Historical references to `/api/skills/*`, `/api/skills/runtime/*`, or `/api/skills/teams/*` are gateway-era or older runtime notes unless a document explicitly marks them as current compatibility behavior.
- System skills now live under `.agents/skills`, not `docs/skill_runtime/skills`.

Migration notes:

- `skill_runtime/` and `multi-agents/` are now maintained in `ai-agent-runtime`.
- `ai-gateway` keeps only stub pointers for these doc trees.
- Many historical design and analysis documents still mention old paths such as `internal/runtime/*` or `internal/api/skills/*`.
- In this repository, read those old paths as the corresponding locations under `backend/internal/*` unless a document explicitly says it is describing a historical state.
- For current `aicli` behavior, start with `docs/aicli/quickstart.md`, then `docs/aicli/install.md` / `docs/aicli/faq.md`; these cover first-use setup, starter config bootstrap, `aicli login`, chat preferences, session/resume, slash commands, shell/background, MCP, and troubleshooting.
- For headless `aicli exec`, portable agents, and image generation, see `docs/aicli/exec.md`, `docs/aicli/agents.md`, and `docs/aicli/tool_image_generate.md`.
- For current background jobs HTTP operations, see `docs/skill_runtime/runtime_operations_api.md`.

Main sections:

- `aicli/` - CLI behavior, default `aicli` -> `chat` startup, headless exec, portable agents, image generation, tool output rendering, metadata propagation, and provider integration notes
- `codex/` - Codex provider behavior, native tool exposure, and image generation diagnostics
- `skill_runtime/` - runtime APIs, governance, contracts, search, persistence, and design notes
- `multi-agents/` - multi-agent design, profile, team, and rollout plans
- `working/` - point-in-time debugging notes and implementation snapshots
- Provider config field notes live under `backend/docs/config/` (for example `enable_image_generation`)

Recommended starting points:

- `development-guidelines.md`
- `aicli/quickstart.md`
- `aicli/README.md`
- `aicli/install.md`
- `aicli/faq.md`
- `aicli/exec.md`
- `aicli/agents.md`
- `aicli/tool_image_generate.md`
- `skill_runtime/README.md`
- `multi-agents/README.md`
- `working/README.md`
