# Prompt Cache Layout

This note documents the current **provider prompt-cache friendly** layout for
message history and tool schemas in `ai-agent-runtime`.

The goal is higher cache hit rate and stable long-session behavior:

1. Do not mutate the conversation **prefix** mid-session.
2. Keep mutable context in a **dynamic tail** after raw history.
3. Freeze the **tool surface** early, with a fixed turn prompt budget, and keep
   it immutable for the rest of that turn (and, on the chat path, across turns
   once a stable surface is persisted).

## Why this matters

Most OpenAI-compatible providers cache shared **prompt prefixes**. Cache breaks
when earlier bytes change, for example:

| Anti-pattern | Effect |
|---|---|
| Insert fact ledger / memory / workspace **before** history | Rewrites every prior turn prefix |
| Re-select or re-compact tools **every ReAct step** | Tool-schema prefix churns each model call |
| Freeze tools from first-step `remainingBudget` only | Tools may still be oversized for later steps, forcing mid-turn re-compact or preflight failure |

## Message layout (`contextmgr.Manager.Build`)

Authoritative assembly lives in `backend/internal/contextmgr/manager.go`.

### Final order

```text
[system messages]
[profile]                 # context_stage=profile        (stable cold)
[ledger / compaction]     # context_stage=ledger|compaction (stable cold)
[raw conversation history]  # append-only prefix; never receive forward inserts
[fact_ledger]             # context_stage=fact_ledger    (dynamic tail)
[observation / warm]      # context_stage=observation... (dynamic tail)
[recall]                  # context_stage=recall         (dynamic tail)
[workspace]               # context_stage=workspace      (dynamic tail)
[team]                    # context_stage=team           (dynamic tail)
[+ other mutable stages]  # e.g. todo_state              (dynamic tail)
```

Key comments in `Build`:

- Snapshot facts **before** compaction, but **append them after raw history**.
- Raw history is append-only after the stable session prefix.
- Mutable layers must not reorder or split the conversation prefix.

### Stage classification (`splitManagedMessages`)

| Stage | Bucket | Notes |
|---|---|---|
| (role=system) | system | Always first |
| `ledger`, `correction`, `profile`, `compaction` | stable | May sit after system, before raw history |
| `fact_ledger`, `workspace`, `team`, `recall`, `warm_memory`, `observation`, `todo_state` | dynamic | Always after raw history |
| other / missing stage | raw | Conversation turns |

Trim / reassemble **must** preserve:

```text
system → stable → raw → dynamic
```

Never reintroduce dynamic content in front of raw history.

### Fact ledger policy

Implementation: `backend/internal/contextmgr/facts.go` (`buildFactLedgerMessage`).

- Load only **active** facts for the current goal (`factledger.ListActive` with
  `GoalID` / session / workspace scope).
- Inject only when there is something authoritative to show.
- Suppressed while the active user turn already has tool/assistant replay
  (`activeTurnHasReplay`), so mid-turn rebuilds do not thrash the tail.
- Position: **after history**, never before.

### Trim policy under pressure

When `EnablePromptCompaction` is on:

1. Prefer dropping **dynamic tail** messages first (from the end of the dynamic
   list while shrinking count/token budget).
2. Then drop **unpinned raw history from the front**, as whole turn units
   (assistant+tool groups stay intact when possible).
3. Keep **pinned active-turn raw messages** and stable cold layers as long as
   budget allows.

Practical drop preference for dynamic content under token pressure:

```text
dynamic tail first → older raw history → keep active turn + system/stable
```

## Tool surface freeze (`agent` + `chat`)

### Problem that was fixed

Freezing after step 1 **without** lean compaction left large tool schemas
(e.g. ~600 tokens including `spawn_subagents`). Later steps then either:

- re-compacted tools every step (cache-hostile), or
- failed preflight when active-turn replay grew
  (`prompt tokens > context_max_prompt_tokens`).

### Intended policy

| Scope | Behavior |
|---|---|
| First freeze in a turn | Select tools, then `freezeToolSurfaceForTurn` |
| Freeze budget | Fixed turn prompt budget (`resolveContextBuildPromptBudget`), **not** step `remainingBudget` |
| Tool share | ≈ `PromptBudget / 4`; if larger, deterministic `compactToolDefinitionAnnotations` |
| Later steps same turn | Load frozen tools only; **no mid-turn rewrite** |
| Chat session path | Persist `StableToolSurface` + `FrozenTurnTools`; load prefers stable surface |

### Code anchors

| Piece | Location |
|---|---|
| Install in-memory snapshot at run start | `ensureTurnToolSurfaceSnapshot` in `agent/loop.go` `run()` |
| Load / freeze / save in think | `ReActLoop.think` tool path in `agent/loop.go` |
| Compute without premature save | `getAvailableTools` → `computeAvailableTools` |
| Budget-aware freeze | `freezeToolSurfaceForTurn` |
| Annotation compaction | `compactToolDefinitionAnnotations` |
| Freeze-once in-memory snapshot | `agent/tool_surface_context.go` `SaveTurnToolSurface` |
| Durable chat snapshot | `chat/turn_tool_surface_snapshot.go` |
| Runtime state fields | `chat/runtime_state.go` `StableToolSurface*` / `FrozenTurnTools*` |

### Flow

```text
run()
  └─ ensureTurnToolSurfaceSnapshot(ctx)   # in-memory, or chat actor wrapper

think() step N
  ├─ LoadTurnToolSurface
  │    ├─ cached=true  → use frozen tools (skip freeze)
  │    └─ cached=false → computeAvailableTools (do not save yet)
  ├─ if !frozen:
  │    freezeToolSurfaceForTurn(tools)    # fixed PromptBudget/4
  │    SaveTurnToolSurface(tools)         # freeze-once
  └─ enforcePromptPreflightWithTools(...) # message compact only; tools stable
```

### Chat actor nuances

`runtimeTurnToolSurfaceSnapshot`:

- **Load**: if the active turn already has `FrozenTurnToolsSet`, return that
  turn-local freeze (mid-turn eligibility changes must not rewrite tools).
- **Load**: otherwise, if `StableToolSurfaceSet` and the stored eligibility
  binding still matches (permission mode + tool policy + pre-freeze catalog),
  reuse the session-stable surface.
- **Load**: binding mismatch clears `StableToolSurface*` so the next freeze
  rebinds under the new policy / MCP set. Legacy empty binding keeps reusing
  until the next Save rewrites it with a key.
- **Save**: freeze-once for the active turn; writes both `StableToolSurface`
  and `FrozenTurnTools`, plus `StableToolSurfaceBinding` /
  `StableToolSurfaceFingerprint`.
- `resetFrozenTurnTools` clears only the turn-local freeze flags/fields; it does
  not by itself re-open a session-stable surface.

Standalone / embedded agent runs without a chat actor still get an **in-memory
turn snapshot** for multi-step turns.

## Do not regress

1. **Fact ledger after history** — never prepend dynamic fact content.
2. **Active-goal-only facts** — do not inject unrelated / global fact noise into
   the prompt tail.
3. **Turn-stable tools after first freeze** — no per-step tool re-compact solely
   to chase remaining budget.
4. **Freeze against fixed turn prompt budget** — not first-step message size.
5. **Preflight regressions** with small `context_max_prompt_tokens` (1300/1400)
   and large tool results must keep passing:
   - `TestReActLoop_Run_PromptBudgetCompactsActiveTurnReplayBeforeThirdRequest`
   - `TestReActLoop_RunWithSession_PromptOnlyActiveTurnCompactionDoesNotPersist`
   - `TestReActLoop_RunWithSession_AutoCompactionRecoveryContinuesAfterPromptPreflightFailure`
   - `TestFreezeToolSurfaceForTurnUsesFixedPromptBudgetShare`
   - `TestReActLoop_Run_FreezesToolSurfaceForEntireTurn`
   - `TestBuildKeepsConversationPrefixStableWhenFactLedgerInjected`
   - `TestBuildDoesNotPrependFactLedgerBeforeHistory`

## Verification

```powershell
go test ./internal/contextmgr/ -count=1
go test ./internal/agent/ -count=1
# when including chat durable surface work:
go test ./internal/chat/ -count=1
```

Optional live inspection: session `debug.log` lines
`[llm-debug] request_started` / `request_finished` carry prompt layout and
usage (including cache tokens when the provider reports them). See
`docs/aicli/prompt-layout-debug-note.md`.

## Invalidation policy

Session-stable tools are bound to an eligibility key:

- permission mode (`RunMeta.PermissionMode`)
- tool execution policy (allow/deny/readonly/MCP write guards/capabilities)
- pre-freeze tool catalog fingerprint (MCP + broker + subagent tools, sorted)

**Not** in the binding: goal projection (session stability is for schema
continuity; per-goal shrink only applies on first freeze).

Rules:

1. **Turn-local freeze wins** once `FrozenTurnToolsSet` for the current turn.
2. Across turns, a binding mismatch clears `StableToolSurface*` so the next
   think step re-freezes under the new key.
3. Legacy persisted states without a binding keep reusing the surface until the
   next freeze writes a binding.

## Observability

| Signal | Where |
| --- | --- |
| `tool_surface_fingerprint` | freeze events (`context.tool_schema.compacted` / `.frozen`), LLM request metadata / `tool_surface.fingerprint`, aicli `[llm-debug] request_started` |
| `tool_schema_before` / `tool_schema_after` | `context.tool_schema.compacted` when freeze lean-compacted tools |
| `usage_cached_tokens` | `llm.request.finished` when the provider reports cache reads |
| `usage_cache_hit_ratio` | `llm.request.finished` = `cached / prompt`; aicli usage panel shows `cache_hit=xx.x%` |

## Remaining optional follow-ups

1. Keep unrelated WIP (`LoadRuntimeStateForInspection`, goal no-op,
   `aicli_exec`, compactruntime provider window) in separate commits from the
   prompt-cache core path.
2. Collect long-session provider `cached_tokens` / `cache_hit` before-vs-after
   samples in live runs to quantify cache gains.
