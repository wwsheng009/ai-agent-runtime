# WIP Commit Split Guide

Date: 2026-07-26  
Purpose: logical commit boundaries for the large uncommitted working tree after
Grok harness productization + session backtrack residuals.

This is a **split plan only**. Do not auto-commit unless explicitly requested.

## Recommended commit order

### 1. `feat(agent): pure-advisory system-reminder strip-on-persist (R3 residual)`

- `backend/internal/agent/system_reminder.go`
- `backend/internal/agent/system_reminder_test.go`
- related durable history helpers under `backend/internal/agent/*` if only used by R3

### 2. `feat(chat): session user-turn backtrack core + audit tombstones`

- `backend/internal/chat/*` backtrack / history identity / tombstone paths
- `backend/internal/api/skills/*` HTTP backtrack endpoints
- `backend/pkg/skillsapi/*` client methods
- related events/types only when required by backtrack

### 3. `feat(aicli): /backtrack slash + Esc select UI`

- `backend/cmd/aicli/commands/chat_backtrack_command.go`
- `backend/cmd/aicli/commands/chat_backtrack_command_test.go`
- `backend/cmd/aicli/commands/chat_backtrack_select.go`
- slash catalog/completion wiring under `backend/cmd/aicli/commands/*`
- composer Esc / interactive input glue

### 4. `feat(frontend): backtrack restore panel + dialog + transcript UX`

- `frontend/src/api/runtime/*`
- `frontend/src/types/runtime.ts`
- `frontend/src/hooks/workspace/use-session-backtrack*`
- `frontend/src/hooks/workspace/use-runtime-checkpoints*`
- `frontend/src/components/workspace/message-*`
- `frontend/src/components/workspace/artifact-panel*`
- `frontend/src/pages/workspace-page.tsx`
- `frontend/src/components/workspace/workspace-shell.tsx`

### 5. `fix(frontend): plan-mode reload event-key dedupe`

- `frontend/src/hooks/workspace/use-runtime-plan-mode.ts`
- `frontend/src/hooks/workspace/use-runtime-plan-mode.test.ts`
- `frontend/src/components/workspace/artifact-panel.tsx` (runtimeEventCount pass-through only)

Keep this separate from backtrack so the reload-loop fix remains reviewable.

### 6. `docs: backtrack + harness residual status`

- `docs/plan/session-user-turn-backtrack-plan.md`
- `docs/plan/grok-harness-productization-implementation-plan.md`
- `docs/analysis/grok-build-architecture-learning.md` if status tables changed
- product notes only when they document delivered residuals

### 7. `chore: leave out local binaries`

- do **not** commit `aicli.exe` or other build artifacts

## Suggested commands (manual)

```powershell
# inspect themes first
git status --short
git diff --stat

# example: stage only plan-mode fix
git add frontend/src/hooks/workspace/use-runtime-plan-mode.ts `
  frontend/src/hooks/workspace/use-runtime-plan-mode.test.ts `
  frontend/src/components/workspace/artifact-panel.tsx
git commit -m "fix(frontend): plan-mode reload event-key dedupe"
```

## Notes

- Some backend files currently mix harness residuals and backtrack changes.
  Prefer path-scoped staging (`git add -p` / path lists) over bulk `git add .`.
- If a file mixes two themes and is hard to split, attach it to the dominant
  theme and mention the secondary touch in the commit body.
