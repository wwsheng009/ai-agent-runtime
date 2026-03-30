# Profile / Workspace / Agent Hierarchy Design (AICLI + Skill Runtime)

> Status: design draft  
> Date: 2026-03-13  
> Scope: aicli chat and skills runtime bootstrap
>
> Migration note (2026-03-30):
>
> - This document is now maintained with the runtime repo under `E:\projects\ai\ai-agent-runtime\docs`.
> - Runtime implementation references should prefer `E:\projects\ai\ai-agent-runtime\backend\...`.
> - References to old `internal/runtime/*` paths should be read as the corresponding `backend/internal/*` locations.

## 1. Goals

1. Introduce a Profile root that encapsulates all runtime assets for a scenario.
2. Model a clear hierarchy:  
   Profile -> Agents -> Workspace -> Skills / Tools / MCPs / Resources
3. Add `aicli chat --profile <dir>` to load a profile directory.
4. Provide one entry YAML in the profile root that declares:
   - agents (workspace resolved by convention)
   - skills / tools / MCP configuration
5. Directory locations follow default conventions; **no file path fields** in config.
5. Allow each agent to have its own workspace directory, with agent-specific resources such as:
   - memory
   - prompts
   - tool policy
6. Keep alignment with current implementation:
   - `backend/configs/config.yaml` (provider config + aicli defaults)
   - `skills_runtime.config_file` (default: `configs/runtime.yaml` when running from `backend/`)
   - `backend/internal/bootstrap/manager.go`
   - `backend/cmd/aicli/commands/*`

## 2. Non-Goals

- Not replacing the gateway config format.
- Not redesigning skills runtime internals.
- Not changing the existing `aicli chat` request path unless `--profile` is used.
- Not introducing multi-agent static topology beyond aicli usage (agents are still created at runtime).

## 3. Proposed Hierarchy Model

```
Profile
  ├─ Agents (0..N)
  │    ├─ Workspace (1 per agent)
  │    └─ Agent resources
  ├─ Skills / Tools / MCP (profile defaults)
  └─ Runtime / Provider / Policy defaults
```

### Resolution Order (highest priority first)

1. CLI flags (`--model`, `--provider`, `--skills-*`, `--profile`)
2. Agent workspace configuration (`agents/<id>/workspace/workspace.yaml`, if present)
3. Agent configuration (`profile.yaml` + `agents/<id>/agent.yaml`, if present)
4. Profile defaults (from `profile.yaml`)
5. Global config (`backend/configs/config.yaml`)
6. Runtime defaults (`skills_runtime.config_file`, default `configs/runtime.yaml` under `backend/`)

This order preserves existing behavior while allowing profile-scoped overrides.

## 4. Profile Directory Layout (Default Rules)

```
profiles/
  dev/
    profile.yaml               # entry config (required)
    runtime.yaml               # optional override for skills runtime (by convention)
    mcp.yaml                   # optional profile MCP config (by convention)
    skills/                    # profile-scoped skills (by convention)
    tools/                     # profile-scoped tool configs (optional)
    agents/
      coder/
        agent.yaml             # optional agent config (overrides profile)
        skills/                # agent-scoped skills (by convention)
        workspace/
          workspace.yaml       # optional workspace config
          skills/              # workspace-scoped skills
          mcp.yaml             # optional workspace MCP config
        prompts/
          system.md
          role.md
          tools.md
        memory/
          memory.json
          ledger.db
          artifacts.db
        sessions/              # agent session store (aicli / runtime session JSONs)
        tools/
          policy.yaml
        context/
          notes.md
      reviewer/
        agent.yaml
        workspace/
          workspace.yaml
        prompts/...
        memory/...
        sessions/...
```
Each agent owns exactly one workspace directory under its agent folder.  
If you need a shared workspace, use a symlink or a shared directory under `profiles/<name>/shared/` and reference it in tooling—not in config.

## 5. Entry Config File: `profile.yaml`

The profile entry config declares agents (with their workspace) and resource layers.

### Minimal Example

```yaml
profile:
  name: "dev"
  default_agent: "general"

runtime:
  # Optional runtime settings (no path here).
  # runtime.yaml is loaded by convention if present under profile root.

providers:
  # Optional: override or alias at profile scope
  # (providers are still defined in backend/configs/config.yaml)
  # If you choose to inline providers here, use the same schema as backend/configs/config.yaml (providers / providers.items).
  default_provider: "nvidia"

mcp:
  # Optional. mcp.yaml is loaded by convention if present under profile root.

skills:
  # profile/skills is used by convention

agents:
  general:
    model: "z-ai/glm4.7"
    provider: "nvidia"            # optional
    prompts:
      # prompts directory is resolved by convention under agents/general/prompts
    tools:
      allowlist: ["read_file", "search_docs", "fetch_url_content"]
```

### Profile Schema (logical)

```yaml
profile:
  name: string
  default_agent: string

runtime:
  # optional inline overrides (future)
  overrides: {...}

providers:
  default_provider: string         # optional; maps to backend/configs/config.yaml
  model_aliases: { alias: provider } # optional alias map (profile scope)
  # Optional inline provider definitions:
  # providers.items.<name> follows the same format as backend/configs/config.yaml.

mcp:
  merge_strategy: "override|merge" # optional

skills:
  # profile/skills is used by convention

tools:
  allowlist: [string]              # optional
  denylist: [string]               # optional
  sandbox: {...}                   # optional (aligns with runtime sandbox config)

agents:
  <id>:
    model: string
    provider: string
    prompts:
      # resolved by convention in agents/<id>/prompts
    memory:
      # resolved by convention in agents/<id>/memory
    tools:
      allowlist: [string]
      denylist: [string]
```

## 6. Workspace and Agent Resource Files

### Workspace Files (by convention)

- `workspace.yaml` (optional overrides)
- `skills/` (workspace-scoped skills)
- `mcp.yaml` (workspace MCP config)

### Agent Files

- `agent.yaml` (optional overrides)
- `prompts/system.md` (system prompt)
- `prompts/role.md` (role prompt)
- `prompts/tools.md` (tool usage hints)
- `memory/`
  - `memory.json` (process memory snapshot for aicli)
  - `ledger.db` / `artifacts.db` (artifact store)
- `sessions/`
  - `*.json` (session files, one per session)
- `tools/policy.yaml` (allowlist / denylist)

## 7. How aicli chat Uses Profiles

### Proposed CLI

```
aicli chat --profile <dir> [--agent <id>]
```

### Resolution Flow (aicli)

1. Load global config: `backend/configs/config.yaml`
2. Load profile entry: `<profile>/profile.yaml`
3. Resolve agent:
   - `--agent` else `profile.default_agent`
4. Resolve workspace path by convention:
   - `profiles/<profile>/agents/<agent>/workspace`
5. Load agent/workspace overrides (if present):
   - `profiles/<profile>/agents/<agent>/agent.yaml`
   - `profiles/<profile>/agents/<agent>/workspace/workspace.yaml`
6. Build runtime config:
   - base: `skills_runtime.config_file` (default `configs/runtime.yaml` when running from `backend/`)
   - override: `profiles/<profile>/runtime.yaml` if present (by convention)
7. Resolve skills dirs (priority high → low):
   - `profiles/<profile>/agents/<agent>/workspace/skills/` (optional)
   - `profiles/<profile>/agents/<agent>/skills/` (optional)
   - `profiles/<profile>/skills`
   - `skills_runtime.skill_dir`
8. Resolve MCP config:
   - base: `aicli.mcp.config_file` (global)
   - override: `profiles/<profile>/mcp.yaml` if present (by convention)
   - override: `profiles/<profile>/agents/<agent>/workspace/mcp.yaml` if present (by convention)
9. Apply tool policy:
   - workspace (`workspace.yaml`) -> agent (`tools/policy.yaml` or inline `agents.*.tools`) -> profile (`profile.tools`)
10. Initialize skills runtime:
   - `bootstrap.Manager` with resolved runtime config
   - provider configs from `backend/configs/config.yaml`
11. Resolve session storage:
   - `profiles/<profile>/agents/<agent>/sessions` (when profile is used)
12. Run `aicli chat` with:
   - agent model (overrides runtime default)
   - workspace path (for context pack)
   - session dir resolved to `profiles/<profile>/agents/<agent>/sessions`

## 8. Model / Provider Resolution

Current system behavior:

- LLM runtime selects a provider by model string.
- Provider aliases are registered from:
  - provider name
  - default model
  - supported models
  - model mappings

### Proposed Rules

1. If `agent.provider` is set, use:
   - `model = agent.model` if provided
   - else `model = agent.provider`
2. If `agent.model` is set and maps to a provider alias, use it directly.
3. If both are empty, fall back to runtime default model.

This aligns with `backend/internal/llm/runtime.go` and `backend/internal/bootstrap/manager.go`.

## 9. Skills / Tools / MCP Layering

| Layer | Skills | MCP | Tools |
| --- | --- | --- | --- |
| Agent | `agents/<id>/skills/` | — | `agents/<id>/tools/policy.yaml` |
| Agent workspace | `agents/<id>/workspace/skills/` | `agents/<id>/workspace/mcp.yaml` | `agents/<id>/workspace/workspace.yaml` |
| Profile | `profiles/<profile>/skills` | `profiles/<profile>/mcp.yaml` | `profile.tools.*` |
| Global | `skills_runtime.skill_dir` | `aicli.mcp.config_file` | runtime defaults |

Merge strategy:

1. Skills: append directories in priority order (agent workspace -> agent -> profile -> global).
2. MCP: override by file (agent workspace > profile > global), with optional future "merge by server name".
3. Tools: allowlist/denylist are merged, with workspace > agent > profile precedence.

## 10. Backward Compatibility

If `--profile` is not specified:

- `aicli chat` behaves exactly as today.
- Skills runtime uses `skills_runtime.config_file` (default `configs/runtime.yaml` when running from `backend/`).
- MCP uses `aicli.mcp.config_file`.
- aicli session storage stays in `~/.aicli/sessions` when no profile is used.

## 11. Implementation Plan (Minimal)

1. Profile loader
   - Parse `profile.yaml`
   - Resolve paths relative to profile root
2. aicli flags
   - Add `--profile`, `--agent`
3. Runtime boot
   - Use resolved runtime config file (profile/runtime.yaml if present)
   - Append skills dirs based on profile + agent workspace + agent
4. MCP selection
   - Use `profiles/<profile>/mcp.yaml` if present, otherwise keep global config
5. Prompt / memory wiring
   - Load prompt files and inject as system context
   - Use per-agent memory/artifact paths for aicli session logs
6. Session storage wiring
   - When profile is used, set session dir to `agents/<agent>/sessions`

## 12. Open Questions

- Should MCP configs be merged (by server name) or replaced?
- Should agent-specific skills be loaded into a dedicated registry (per agent) or shared?
- Do we need a new API to expose profile metadata to clients?
