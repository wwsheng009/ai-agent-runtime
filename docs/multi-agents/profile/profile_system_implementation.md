# Profile System Implementation Plan

> Status: implementation-ready  
> Date: 2026-03-14  
> Scope: system-level profile / agent / workspace runtime topology
>
> Migration note (2026-03-30):
>
> - The runtime HTTP/API side described here now lives in `E:\projects\ai\ai-agent-runtime\backend`.
> - In that repo, many old paths under `internal/runtime/<subpkg>` have been flattened to `internal/<subpkg>`.
> - This document remains useful as an ownership/design reference, but old gateway-local runtime paths should be read as historical names.

## 1. Why This Is a System Capability

`profile` should not be implemented as an `aicli`-only feature.

The same resolved runtime topology is needed by at least four entry surfaces:

1. `aicli chat`
2. optional dedicated `aicli agent` surface reusing the same runtime host
3. `/api/agent/chat`
4. runtime/bootstrap and offline tooling

If the implementation stays inside `cmd/aicli`, we will duplicate:

- directory convention parsing
- profile / agent / workspace merge rules
- skills/MCP/runtime/session path resolution
- prompt file loading
- tool policy loading
- validation and error messages

The correct architecture is:

1. a system-level `internal/profile` domain module resolves profile topology
2. runtime packages consume the resolved topology
3. CLI/API only translate input flags or request fields into resolver options

## 2. Design Goals

1. Make profile resolution reusable across CLI, API, and background runtime code.
2. Keep directory conventions explicit and centralized.
3. Separate discovery/merge from execution.
4. Preserve backward compatibility when no profile is selected.
5. Allow phased adoption without rewriting the existing runtime bootstrap.

## 3. Non-Goals

1. Do not replace `configs/config.yaml` as the global gateway config.
2. Do not move skill runtime bootstrap logic into profile resolution.
3. Do not mix profile file prompts with agent subagent prompt-builder logic.
4. Do not introduce multi-agent orchestration semantics into the profile layer.

## 4. Proposed Ownership Boundaries

### 4.1 `internal/profile`

Responsible for:

- parsing `profile.yaml`, `agent.yaml`, `workspace/workspace.yaml`, `tools/policy.yaml`
- resolving directory conventions
- merging profile -> agent -> workspace overrides
- selecting runtime/MCP/session/skills/prompt/tool-policy paths
- validating required files and reporting deterministic errors
- returning a stable `ResolvedAgent` model

Not responsible for:

- building runtime managers
- executing tools
- creating prompt messages
- running MCP
- opening chat sessions

### 4.2 `internal/runtime/prompt`

Responsible for:

- loading `prompts/system.md`, `prompts/role.md`, `prompts/tools.md`
- composing prompt fragments into runtime-consumable strings

### 4.3 `internal/runtime/policy`

Responsible for:

- generic tool allow/deny policy
- sandbox policy composition
- filtering tool definitions and validating tool calls

This is intentionally runtime-level, not profile-level.  
Profile only declares policy inputs; runtime enforces them.

### 4.4 `internal/runtime/bootstrap`

Responsible for:

- consuming resolved inputs
- wiring runtime config, skills registry, LLM runtime, MCP adapter, session manager

### 4.5 `cmd/aicli` / API handlers

Responsible for:

- input collection (`--profile`, `--agent`, request fields)
- calling resolver
- displaying user-facing errors
- passing resolved values downstream

## 5. Target Directory Structure

### 5.1 New System-Level Modules

```text
internal/
  profile/
    spec.go
    paths.go
    loader.go
    merge.go
    resolved.go
    resolver.go
    validate.go
    *_test.go

  runtime/
    prompt/
      files.go
      compose.go
      *_test.go

    policy/
      tool_policy.go
      filter.go
      *_test.go
```

### 5.2 Existing Packages That Should Consume It

```text
cmd/aicli/commands/
internal/api/skills/
internal/runtime/bootstrap/
internal/runtime/agent/
```

### 5.3 Global Config Extension

Keep this minimal:

```text
internal/config/
  profiles_config.go
```

This file should only hold:

- `profiles_root`
- `default_profile`
- optional named profile aliases

It should not duplicate the full profile schema.

## 6. Canonical Runtime Model

The profile system should expose a stable resolved model.

### 6.1 Raw Spec Types

`internal/profile/spec.go`

- `ProfileSpec`
- `ProfileMetaSpec`
- `AgentSpec`
- `WorkspaceSpec`
- `ToolPolicySpec`
- `ProviderOverridesSpec`

These types mirror YAML files and may remain incomplete while the feature is phased in.

### 6.2 Resolved Types

`internal/profile/resolved.go`

- `ResolvedProfile`
- `ResolvedAgent`
- `ResolvedPaths`
- `ResolvedPromptFiles`
- `ResolvedToolPolicy`

`ResolvedAgent` is the cross-entry-surface contract.

Suggested shape:

```go
type ResolvedAgent struct {
    ProfileName string
    ProfileRoot string
    AgentID     string

    Provider string
    Model    string

    Paths     ResolvedPaths
    Prompts   ResolvedPromptFiles
    ToolPolicy ResolvedToolPolicy

    RuntimeConfigPath string
    MCPConfigPath     string
    SkillDirs         []string
}
```

## 7. Path Resolution Rules

Assume profile root:

```text
profiles/<profile>/
```

### 7.1 Required

- `profile.yaml`

### 7.2 Optional by Convention

- `runtime.yaml`
- `mcp.yaml`
- `skills/`
- `agents/<agent>/agent.yaml`
- `agents/<agent>/skills/`
- `agents/<agent>/workspace/`
- `agents/<agent>/workspace/workspace.yaml`
- `agents/<agent>/workspace/skills/`
- `agents/<agent>/workspace/mcp.yaml`
- `agents/<agent>/prompts/system.md`
- `agents/<agent>/prompts/role.md`
- `agents/<agent>/prompts/tools.md`
- `agents/<agent>/tools/policy.yaml`
- `agents/<agent>/sessions/`
- `agents/<agent>/memory/`

### 7.3 Canonical Selection Rules

1. Agent ID
   - explicit `--agent`
   - else `profile.default_agent`
   - else error

2. Runtime config path
   - `profiles/<profile>/runtime.yaml` if exists
   - else global `configs/runtime.yaml`

3. MCP config path
   - `agents/<agent>/workspace/mcp.yaml` if exists
   - else `profiles/<profile>/mcp.yaml` if exists
   - else global MCP config

4. Skills dirs priority
   - `agents/<agent>/workspace/skills`
   - `agents/<agent>/skills`
   - `profiles/<profile>/skills`
   - global skills dirs

5. Session dir
   - `profiles/<profile>/agents/<agent>/sessions`
   - no-profile mode keeps `~/.aicli/sessions`

6. Prompt files
   - `agents/<agent>/prompts/system.md`
   - `agents/<agent>/prompts/role.md`
   - `agents/<agent>/prompts/tools.md`

## 8. Merge Rules

Merge order from low to high:

1. global config
2. `profile.yaml`
3. `agents/<agent>/agent.yaml`
4. `agents/<agent>/workspace/workspace.yaml`
5. CLI/API explicit overrides

### 8.1 Scalar Fields

Use last-non-empty-wins:

- provider
- model
- default agent

### 8.2 Tool Policy

Merge into one resolved policy:

- allowlist: union, then dedupe
- denylist: union, then dedupe
- read-only: high-priority explicit override wins
- sandbox: higher layer overrides conflicting scalar fields, list fields dedupe

### 8.3 Skills Dirs

Do not merge by config field path.

Only use convention-based discovered directories plus global configured dirs.

### 8.4 Prompts

Prompts are not merged inline in the profile resolver.  
Resolver only returns existing prompt file paths.  
Prompt loading/composition happens in `internal/runtime/prompt`.

## 9. Package-Level API

### 9.1 Resolver API

```go
type ResolveOptions struct {
    Root            string
    Agent           string
    GlobalSkillDirs []string
    GlobalMCPPath   string
    GlobalRuntimePath string
}

func Resolve(options ResolveOptions) (*ResolvedAgent, error)
```

### 9.2 Loader API

```go
func LoadProfile(root string) (*ProfileSpec, error)
func LoadAgent(root, agent string) (*AgentSpec, error)
func LoadWorkspace(root, agent string) (*WorkspaceSpec, error)
func LoadToolPolicy(root, agent string) (*ToolPolicySpec, error)
```

### 9.3 Prompt API

```go
type Files struct {
    System string
    Role   string
    Tools  string
}

func LoadFiles(files Files) (*LoadedFiles, error)
func Compose(loaded *LoadedFiles) string
```

## 10. Migration of Existing Code

### 10.1 `cmd/aicli/commands`

Current problems:

- session dir resolution is local to chat code
- MCP config lookup is local to CLI
- skills dir resolution is local to CLI
- runtime config path resolution is local to CLI

Migration target:

- CLI only parses `--profile` / `--agent`
- CLI calls `profile.Resolve(...)`
- CLI uses returned values directly

Files impacted:

- `cmd/aicli/commands/chat_options.go`
- `cmd/aicli/commands/chat_bootstrap.go`
- `cmd/aicli/commands/chat_session.go`
- `cmd/aicli/commands/skills_integration.go`
- `cmd/aicli/commands/mcp_integration.go`

### 10.2 `internal/runtime/agent`

Current `tool_policy.go` is generic runtime governance, not agent-specific topology parsing.

Migration target:

- move or copy policy logic into `internal/runtime/policy`
- let agent consume the runtime policy package

### 10.3 `internal/runtime/bootstrap`

No major ownership change required.

It should continue to accept already-resolved inputs:

- runtime config
- skill dirs
- MCP manager
- provider configs

### 10.4 API Surface

Future API changes should not parse profile files directly in handlers.

Handlers should:

1. resolve `profile` and `agent`
2. obtain `ResolvedAgent`
3. inject resolved runtime config / prompt / policy / session paths

## 11. Implementation Phases

### Phase 1: System Skeleton

Deliverables:

- `internal/profile` package
- raw spec types
- path resolver
- resolved model
- basic merge rules
- tests for default agent, explicit agent, path resolution, missing files

No CLI or API integration yet.

### Phase 2: CLI Adoption

Deliverables:

- `aicli chat --profile --agent`
- session dir uses resolved profile path
- skills/MCP/runtime config use resolver output
- clear CLI error messages

### Phase 3: Runtime Prompt + Policy Adoption

Deliverables:

- `internal/runtime/prompt`
- `internal/runtime/policy`
- `aicli` and runtime agent use shared policy/prompt packages

### Phase 4: API Adoption

Deliverables:

- `/api/agent/chat` accepts profile/agent selection
- resolved profile wiring into runtime bootstrap
- governance and status endpoints expose resolved profile metadata

## 12. Testing Strategy

### 12.1 Unit Tests

`internal/profile/*_test.go`

Cover:

- missing `profile.yaml`
- `default_agent` fallback
- explicit agent override
- missing agent error
- runtime/MCP path precedence
- skills dir ordering
- tool policy merge
- prompt file detection

### 12.2 Integration Tests

Later phases should add:

- `aicli chat --profile` resolution tests
- API request scoped by profile/agent
- backward compatibility without profile

### 12.3 Backward Compatibility Tests

Must preserve:

- existing `configs/runtime.yaml` flow
- existing global MCP config flow
- existing `~/.aicli/sessions` behavior when profile is absent

## 13. Acceptance Criteria

The system-level profile implementation is complete when:

1. profile resolution is implemented in `internal/profile`
2. CLI and API consume the same resolved topology
3. no entry surface reimplements profile directory parsing
4. prompts/policies are resolved through shared runtime packages
5. no-profile mode remains backward compatible

## 14. Immediate Next Step

Execute Phase 1 first:

1. add `internal/profile` with spec/resolver/paths/merge/tests
2. keep runtime/bootstrap unchanged
3. avoid partial CLI-only shortcuts

This keeps the architecture stable and allows CLI/API adoption in later patches.
