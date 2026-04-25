# Runtime Docs

This directory hosts the runtime-side documentation migrated from `ai-gateway`.

Migration notes:

- `skill_runtime/` and `multi-agents/` are now maintained in `ai-agent-runtime`.
- `ai-gateway` keeps only stub pointers for these doc trees.
- Many historical design and analysis documents still mention old paths such as `internal/runtime/*` or `internal/api/skills/*`.
- In this repository, read those old paths as the corresponding locations under `backend/internal/*` unless a document explicitly says it is describing a historical state.

Main sections:

- `aicli/` - CLI behavior, tool output rendering, metadata propagation, and provider integration notes
- `codex/` - Codex provider behavior, native tool exposure, and image generation diagnostics
- `skill_runtime/` - runtime APIs, governance, contracts, search, persistence, and design notes
- `multi-agents/` - multi-agent design, profile, team, and rollout plans
- `working/` - point-in-time debugging notes and implementation snapshots

Recommended starting points:

- `aicli/README.md`
- `skill_runtime/README.md`
- `multi-agents/README.md`
- `working/README.md`
