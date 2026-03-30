# Refactor Roadmap + Migration Strategy

> Date: 2026-03-13  
> Scope: `/api/agent/chat` (canonical) + `/api/skills/agent/chat` (compat) unification with multi‑agent target architecture  
> Reference: `docs/multi-agents/plan/architecture.md` (Target Architecture)

## 1. Why Refactor

The target architecture positions **agent orchestration above skills**, with:

- a single runtime entry,
- subagents as first‑class execution units,
- skills/tools executed as agent‑scheduled tool calls,
- Output Gateway + Context OS + Artifact Store governing the lifecycle.

Current implementation still has:

- multiple public execution paths (`/api/skills/{name}/execute`, `enable_react`, direct LLM fallback),
- client‑driven control of single vs multi‑agent,
- partial routing of skill execution outside the agent loop.

The refactor goal is a **single orchestration path** where the **LLM decides** to run single or multi‑agent, and skills are **always scheduled by the agent**.

## 2. Target State (Aligned With Architecture)

1. **Single Entry (Agent-Centric)**
   - Canonical entry: `/api/agent/chat` (agent-first naming).
   - `/api/skills/agent/chat` kept as compatibility alias.
   - `/api/skills/{name}/execute` becomes admin/debug only.
2. **LLM‑Decided Orchestration**
   - `planning_mode=auto` (default) decides between:
     - direct answer,
     - skill tool call,
     - planned subagents.
3. **Skills Executed by Agent**
   - skill execution flows through tool calls (e.g., `skill__*`).
4. **Governed Execution**
   - Output Gateway + reducers + artifact store always applied.
   - Context OS always used for admission/compaction/recall.
5. **Streaming**
   - Streaming is consistent across single/multi‑agent, with structured events.

## 3. Refactor Roadmap

### Phase 0 — Baseline & Safety (Immediate)
**Purpose:** Ensure we can refactor without loss of behavior.

- Inventory current entry paths and behavior:
  - `/api/skills/agent/chat` (enable_react, planner_preferred, etc.)
  - `/api/skills/{name}/execute`
  - streaming modes
- Add metrics counters:
  - `agent_chat.entry_mode`
  - `agent_chat.enable_react`
  - `agent_chat.execute_planned_subagents`
  - `agent_chat.llm_fallback`
- Add compatibility tests for:
  - non‑stream / stream
  - with/without skills routing

**Exit:** Confidence baseline exists; regressions detectable.

### Phase 1 — Unified Orchestration Path (Core Refactor)
**Purpose:** All agent chat goes through orchestrator.

- Make `Orchestrate` the single path in `AgentChat`.
- Introduce `planning_mode=auto` (default):
  - LLM decides whether to plan subagents or answer directly.
- Keep `planner_preferred` and `enable_react` as **legacy overrides**.
- Ensure Output Gateway + Context OS used in all paths.
- Add `/api/agent/chat` route alias and update docs/client defaults to prefer it.

**Exit:** `/api/agent/chat` uses one path (compat alias preserved); legacy flags still work.

### Phase 2 — Skills as Agent Tool Calls (Architectural Alignment)
**Purpose:** Skills are never executed outside agent loop for normal usage.

- Convert internal skill execution to tool‑call style:
  - agent selects `skill__*` and runs via `skill.Executor`.
- `/api/skills/{name}/execute` marked as **admin/debug**:
  - add warning header or log
  - optionally require admin token

**Exit:** Normal usage only goes through agent. Direct execute is guarded.

### Phase 3 — Multi‑Agent as Default Capability (Auto Planning)
**Purpose:** LLM decides single vs multi‑agent in one flow.

- In `planning_mode=auto`:
  - LLM decides if plan/subagents are needed.
  - no explicit `execute_planned_subagents` required by client.
- `execute_planned_subagents` becomes a governance allow/deny switch.

**Exit:** single/multi‑agent decision is fully model‑driven.

### Phase 4 — Streaming Unification
**Purpose:** Same streaming protocol for single and multi‑agent.

- Standard SSE event set:
  - `planning` → `subagent` → `tool` → `result`
- Ensure all event payloads share:
  - `trace_id`, `session_id`, `agent_id`
- Remove "static SSE only" special cases.

**Exit:** Streaming behaves the same across all paths.

### Phase 5 — CLI Alignment
**Purpose:** aicli uses the same unified architecture.

- Keep `aicli chat` as the default local actor-first orchestration host.
- Ensure CLI local orchestration and `/api/agent/chat` share the same runtime concepts:
  - profile/runtime/tool bootstrap
  - planning/tool/subagent event rendering
  - team run metadata and permission mode propagation
- If a dedicated `aicli agent` command is added later, make it a thin surface over the same runtime instead of a replacement for `aicli chat`.

**Exit:** CLI mirrors runtime architecture.

## 4. Migration Strategy

### Compatibility Mode (Default)
- Keep existing query params working:
  - `enable_react`
  - `planning_mode=planner_preferred`
  - `execute_planned_subagents`
- Add warnings when legacy flags are used.

### Deprecation Timeline
1. **Phase 1:** add logs for legacy usage, publish `/api/agent/chat` as preferred.
2. **Phase 2:** require explicit admin token for `/execute`.
3. **Phase 3:** update docs to call `/agent/chat` only; keep `/api/skills/agent/chat` as compatibility.

### Testing Strategy
- Regression tests for:
  - legacy path (enable_react)
  - planner_preferred
  - auto mode
- End‑to‑end tests with:
  - no skills matched
  - skill matched
  - subagent execution
  - streaming vs non‑stream

## 5. Risks & Mitigations

| Risk | Impact | Mitigation |
| --- | --- | --- |
| Legacy clients depend on `/execute` | High | Keep endpoint but gate with admin token + warning |
| LLM fails to plan in auto mode | Medium | Fallback to heuristic plan or direct response |
| Streaming contract breaks | Medium | Add `version` field in SSE payload |
| Tool policies block needed tools | Medium | Ensure tool policy errors are surfaced in response |

## 6. Alignment With Target Architecture

This roadmap aligns to:

- **Main Agent Env** as the single execution entry.
- **Subagent Envs** as orchestrated children.
- **Context OS + Output Gateway + Artifact Store** used on all paths.
- **Scheduler / Policy / EventBus** in control plane.

## 7. Minimal Implementation Map

**Primary Files**

- `internal/api/skills/handler.go`  
  Unify entry path, introduce `planning_mode=auto`, deprecate legacy flags.
- `internal/runtime/agent/orchestrator.go`  
  Auto planning logic + decision boundary.
- `internal/runtime/agent/loop.go`  
  Ensure tool execution uses Output Gateway & Context OS.
- `internal/runtime/skill/executor.go`  
  Skill execution via agent tool path (no direct API use).

## 8. Exit Criteria Checklist

- ✅ Only one public path for execution (`/api/agent/chat`, with `/api/skills/agent/chat` as compatibility)
- ✅ LLM decides single vs multi‑agent
- ✅ Skills executed via agent tool calls
- ✅ Streaming event contract unified
- ✅ `/execute` becomes admin/debug only
