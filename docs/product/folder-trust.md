# Folder trust (R2 product surface)

Opt-in gate for **project-scoped** plugins, hooks contributions, and MCP configs.

When disabled (default), aicli preserves prior behavior: project roots load without a trust prompt.

## Feature flag

```bash
# enable
set AICLI_FOLDER_TRUST=1          # Windows cmd
$env:AICLI_FOLDER_TRUST = "1"     # PowerShell
export AICLI_FOLDER_TRUST=1       # Unix

# disable (default): empty / 0 / false / off
```

Truth-y values: `1`, `true`, `on` (case-insensitive). Everything else is off.

## What is gated

| Surface | Trusted workspace | Untrusted workspace |
|---------|-------------------|---------------------|
| Project plugins (`.aicli/plugins`, `.agents/plugins`) | Loaded | **Skipped** (`SkipProjectRoot`) |
| User-home plugins (`~/.aicli/plugins`, `AICLI_HOME/plugins`) | Loaded | Loaded |
| Project agent defs (`.agents/agents`, `.aicli/agents`) | Loaded | **Skipped** (`agentdef.SkipProjectRoot`) |
| Builtin + user-home agents (`~/.aicli/agents`) | Loaded | Loaded |
| Project MCP config under project root | Allowed | **Blocked** |
| User-global MCP (`~/.aicli`, `~/.config/aicli`) | Allowed | Allowed |
| Plugin-contributed hooks (via project plugins) | Loaded with plugins | **Skipped** with project plugins |
| Project `permissions.yaml` (R1) | Not gated by folder trust | Not gated by folder trust |

Untrusted does **not** disable tools or permissions; it only skips **project-local** code-exec surfaces that can run unreviewed repo content.

## CLI

```bash
# Durable grant of the current workspace key, then run
aicli chat --trust
aicli exec --trust --prompt "..."

# Interactive session
/trust            # status
/trust status
/trust grant      # durable grant + refresh process/session gates
```

`--trust` / `/trust grant` write the workspace key into the durable store (see below). Unsafe roots (filesystem root, user home) are refused by the package.

## Decision order (package)

With feature **on**:

1. Store already trusts workspace key (or parent cascade) → trusted  
2. Key unrecordable / no trust-sensitive repo configs → trusted  
3. Interactive TTY → prompt  
4. Headless / non-interactive → **untrusted** (fail closed for project scope)

With feature **off** → always treated as trusted for gates (source `feature_off`).

Workspace key = git root when inside a repo, else absolute cwd.

## Durable store

Default path (via aicli home helpers):

- `~/.aicli/trusted_folders.yaml` (or under `AICLI_HOME` when set)

Parent trust cascades to child paths. Store APIs refuse unsafe roots.

## Process-level resolve

Folder trust is resolved **once per process**, early in:

- `aicli chat` (`HandleChat`, before profile/plugin discovery)
- `aicli exec` / resume / ACP stdio (`buildExecSession*`, before profile resolve)

Session bootstrap attaches the same resolution to `ChatSession.FolderTrust`.  
`/trust grant` re-resolves and updates both process cache and session so mid-session grants take effect without restart (plugin discovery is uncached).

## Debug / UX

- `/debug` prints folder trust summary (`FormatSummary`)
- `/trust` status shows feature, trusted, source, store path, and hints

## Wiring map

| Layer | Location |
|-------|----------|
| Pure decide/store/paths | `backend/internal/foldertrust/` |
| Process gate + `/trust` | `backend/cmd/aicli/commands/chat_folder_trust.go` |
| Plugin skip | `plugin_runtime.go` → `plugins.DiscoverOptions.SkipProjectRoot` |
| Agentdef skip | `agentdefDiscoverOptions` → `agentdef.DiscoverOptions.SkipProjectRoot` |
| MCP path gate | `mcp_integration.go` → `foldertrust.IsProjectScopedPath` |
| Flags | `chat --trust`, shared exec `--trust` |

## Notes

- Default **off** keeps CI and existing scripts unchanged until operators opt in.
- Folder trust is complementary to R1 project permissions; it does not replace tool allow/deny lists.
- Project hooks are gated today via **project plugins** (no separate direct project `hooks.yaml` loader in CLI). Direct hooks load remains a product follow-up if added later.
- API/toolbroker agentdef resolve paths are out of this CLI process gate unless they share the same process cache.
