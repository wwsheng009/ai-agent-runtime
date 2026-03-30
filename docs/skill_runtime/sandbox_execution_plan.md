# Skill Runtime Sandbox Execution Plan

## Why This Document Exists

The sandbox design for `skill_runtime` was previously scattered across:

- `docs/skill_runtime/design/agent_skill_runtime-*.md`
- `docs/skill_runtime/design2/skills_runtime_design.md`
- `docs/skill_runtime/plan/IMPLEMENTATION_TODO.md`
- `docs/skill_runtime/platform_runtime_roadmap.md`

Those documents now live in the active `docs/skill_runtime/` tree, but the implementation is still behind the design. This document narrows the sandbox work into an incremental plan that matches the current codebase.

## Current State

Today the runtime has:

- execution-time `skill.permissions` checks
- trusted-host shortcuts for `aicli` and admin/loopback API requests
- direct local shell execution for built-in toolkit shell tools
- MCP-backed workflow steps forwarded to the configured MCP server

Today the runtime does not have:

- a unified sandbox layer around all tool calls
- filesystem or environment isolation enforced by `skill_runtime`
- network isolation for tool execution
- a truthful way to claim that remote MCP tools run inside a runtime-owned sandbox

This distinction matters:

- local toolkit tools can be sandboxed by this repository
- remote MCP tools can only be governed, not truthfully sandboxed, unless the MCP server itself provides isolation

## Implementation Principles

1. Separate local sandboxing from remote MCP governance.
2. Do not label remote MCP execution as sandboxed unless the MCP server enforces it.
3. Prefer explicit allow/deny policies over heuristic blacklists.
4. Start with path, command, and environment controls before claiming process isolation.
5. Integrate incrementally so existing `skill_runtime` behavior stays debuggable.

## Delivery Plan

### Phase 1: Sandbox Foundation

Goal:

- land a reusable `internal/runtime/executor/sandbox.go` primitive

Scope:

- path allow/deny checks
- read-only path enforcement for write/delete operations
- command allow/deny checks
- environment filtering
- command execution wrapper for future integrations

Status:

- implemented in this round as a standalone foundation with unit tests

### Phase 2: Runtime Configuration

Goal:

- make sandbox behavior configurable from runtime config

Scope:

- add `sandbox` section to `internal/runtime/config.RuntimeConfig`
- extend `configs/runtime.yaml`
- define safe defaults and explicit opt-in for restrictive mode

Not in Phase 2:

- container isolation
- host-level namespace controls

Status:

- completed in this round

### Phase 3: Local Toolkit Integration

Goal:

- apply sandbox checks to repository-owned local tools

Initial targets:

- `view`
- `write`
- `edit`
- `multiedit`
- `glob`
- `grep`
- `ls`

Notes:

- these tools expose explicit paths, so policy checks are reliable
- shell tools should only be partially integrated here because raw shell strings are not equivalent to full sandboxing

Status:

- file-oriented local tools completed in this round

### Phase 4: Local Shell Guardrails

Goal:

- improve local shell execution without overstating guarantees

Scope:

- allowed/denied executable policy
- working-directory checks
- environment whitelist
- timeout consolidation
- denial/error observability

Non-goal:

- claiming full shell sandboxing without OS/container enforcement

Status:

- first-pass command/env/workdir policy completed in this round
- still not equivalent to process or container isolation

### Phase 5: Skill Executor Integration

Goal:

- make workflow execution aware of sandbox/governance mode

Scope:

- distinguish local-tool execution from MCP-backed execution
- surface denial errors as structured runtime errors
- map `skill.permissions` to sandbox/governance decisions instead of only metadata checks

### Phase 6: MCP Governance

Goal:

- govern remote tools honestly

Scope:

- trust classification for MCP servers
- policy flags such as `local`, `trusted_remote`, `untrusted_remote`
- audit log fields for remote tool execution
- explicit docs that remote MCP isolation belongs to the MCP server

## Acceptance Criteria

The sandbox initiative can be treated as materially implemented only when all of the following are true:

- runtime config can express sandbox policy
- local file-oriented tools enforce path policy
- local shell tools enforce at least command/env/workdir policy
- skill execution surfaces structured denial errors
- docs distinguish local sandboxing from MCP governance
- tests cover allow, deny, and regression cases

## Immediate Next Steps

1. Add metrics/logging for sandbox denials.
2. Integrate sandbox-aware denial handling into skill executor and API error contracts.
3. Keep MCP execution on the governance track, not the sandbox track.
4. Evaluate whether process isolation belongs in runtime core or a future coding pack.
