# Project permissions (R1 product surface)

Versioned project rules for the aicli permission pipeline.

## File location

Resolve order under the workspace/project root:

1. `.aicli/permissions.yaml`
2. `.aicli/permissions.yml`

Missing file is OK (no project overlay).

## Schema

```yaml
version: 1
# Hard tool denylist (ToolExecutionPolicy + engine deny rules)
deny_tools: [shell, aicli_exec]
# Optional hard allowlist. Empty / omitted = no allowlist gate from project.
allow_tools: []
rules:
  - name: deny-network
    tools: [web_search, fetch, download]
    decision: deny          # allow | deny | ask
    reason: project_blocks_network
  - name: ask-writes
    tools: [write, edit, apply_patch]
    capabilities: [write_fs] # optional
    decision: ask
    reason: review_writes
  - name: allow-readonly
    tools: [view, grep, glob, ls]
    decision: allow
```

## CLI product flags

```bash
aicli chat --deny-tool shell --allow-tool view
aicli exec --deny-tool download --enable-tools --prompt "..."
```

- `--deny-tool` may be repeated; hard deny + first-match engine rule.
- `--allow-tool` may be repeated; participates in allowlist + engine allow rules.

## Merge precedence

**Engine rules (first match wins):**

1. CLI `--deny-tool`
2. Project `deny_tools` + `rules`
3. CLI `--allow-tool`

**ToolExecutionPolicy hard gates:**

- `DeniedTools` = union(CLI deny, project `deny_tools`)
- When either CLI allow or project `allow_tools` is non-empty, enable allowlist:
  - If profile already had an allowlist → **intersect**
  - Else → use the product allow list

CLI deny always wins over a project `allow` rule for the same tool.

## Wiring

- Loaded after profile `ToolPolicy` in chat/exec session setup (cwd bootstrap).
- Re-applied when local actor host resolves the absolute workspace root.
- Engine rules applied in `buildLocalChatAgent` and after plan-mode prepare hooks.
- Direct `/call` `/tool` `/skill` uses the same overlay rules.
- Session banner + `/debug` show permission sources / summary.

## Notes

- Overlay is **not** a second pipeline: it feeds existing `policy.Engine.Rules` and `ToolExecutionPolicy`.
- `bypass_permissions` still cannot bypass hard deny lists / hook deny (core A4 behavior).
- Child agent denylists / read-only derive remain separate narrowing layers on top of the base policy.
- **Read-only child sessions** use `ReadOnlyChildCapabilities` (`read_only` + `network` + `ask_user` + `agent_management`): control-plane tools (`spawn_agent`, plan mode, ask user, collab) stay usable, while write/shell/`background_task` stay blocked via ReadOnly + capability scope. Max-depth still hides spawn via `DeniedTools`.
- **Runtime-owned agent essentials** (`enter_plan_mode`, `exit_plan_mode`, `ask_user_question`, collab/team control tools, `todos`/`get_goal`/`update_goal`, `search_tool`, …) bypass the hard **allowlist** gate so a narrow `--allow-tool` / `allow_tools` list cannot brick the agent control plane. They still honor:
  - explicit `deny_tools` / `--deny-tool`
  - capability scope (e.g. a narrowed child without `agent_management` still cannot `spawn_agent`)
  - read-only / sandbox gates
