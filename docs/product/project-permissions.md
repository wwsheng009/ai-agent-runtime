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

## Rule specifiers (rules[].tools)

Plain tool names keep their exact-match semantics. In addition, `rules[].tools`
accepts `Tool(specifier)` entries and tool-name globs
(see `docs/analysis/commandcode-permissions-design-borrowing-20260926.md` §4.1/§4.6/§4.10/§4.11):

```yaml
rules:
  - name: allow-git-read-only
    tools: ["Shell(git status)", "Shell(git diff:*)", "Shell(git log:*)"]  # per-segment match
    decision: allow
  - name: ask-before-push
    tools: ["Shell(git push:*)", "Shell(rm -rf .)"]
    decision: ask
  - name: protect-secret-material
    tools: ["Read(.env)", "Read(**/*.pem)", "Edit(.ssh/**)"]
    decision: ask
  - name: restrict-mcp
    tools: ["mcp__github__get_*", "mcp__*"]        # deny/ask may use broad globs
    decision: deny
  - name: no-background-shell
    tools: ["Shell(run_in_background:true)"]        # top-level arg match, deny/ask only
    decision: deny
```

- **Command patterns** (`Shell/Bash`): exact (`git status`), prefix (`git:*`),
  or `*`/`?` globs. Compound commands are split at `&& || ; | &` and newlines;
  **deny/ask match any segment, allow requires every segment** and never
  auto-allows an unparsable command or a wrapper/interpreter payload.
- **Path patterns** (`Read/Edit/Write/View`): `//abs` filesystem, `~/home`,
  `/workspace-root`, relative matches at any depth; `*` does not cross `/`,
  `**` does. deny/ask fold case; allow is exact (Windows is case-insensitive).
- **Domain patterns** (`WebFetch(domain:*.example.com)`, `WebFetch(example.com)`):
  matched against the URL host of network tools; `*.example.com` excludes the apex.
- **Param patterns** (`Tool(param:value)`, `Tool(param:value*)`): matched against
  the top-level arguments the model actually sent; **deny/ask only**.
- **Tool globs**: `mcp__github__get_*`, `edit_*`; allow rules must name a concrete
  server/prefix (`*` and `mcp__*` are rejected at load time).
- `deny_tools` / `allow_tools` are **hard exact names** and reject specifier
  syntax at load time; the CLI `--deny-tool` / `--allow-tool` flags are exact
  names too (specifier syntax there is not interpreted). Use `rules` for the
  syntax above.
- Specifier syntax in `allow` rules that would be unenforceable (bare `*`,
  `mcp__*`, param patterns) makes the file fail validation with an actionable error.

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

## 配置分层（§4.9）

权限文件按以下顺序**累积**（缺失层跳过；后者更具体，规则的 first-match 顺序即
此顺序，因此用户级 deny 规则先于项目级 allow 规则求值）：

```text
~/.aicli/permissions.yaml                  # 用户级（个人全局，建议放 disable_bypass）
<project>/.aicli/permissions.yaml          # 项目共享（提交进仓库）
<project>/.aicli/permissions.local.yaml    # 本地个人层（建议加入 .gitignore）
```

累积语义：

- `rules`：按 `user → project → local` 顺序拼接，规则名前缀自动标注层
  （如 `project/user-deny-network`）；first-match-wins 不变。
- `deny_tools` / `allow_tools`：跨层**并集**，deny 单调不可撤销。
- `disable_bypass`：任一层为 true 即生效（OR）。

`disable_bypass: true` 表示进程级禁用 `bypass_permissions`：

- engine 把以 bypass 进入的请求按 `default` 处理（该问的照问、headless 场景照拒），
  `dont_ask` 与 `plan` 不受影响；
- CLI `/permission-mode bypass_permissions`、shift+tab 循环、`/yolo` 等切换入口
  会被拒绝（`--yolo` 启动参数在引擎侧同样被降级）。

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
- **Read-only child sessions** use `ReadOnlyChildCapabilities`: control-plane tools (plan mode, ask user, collab) stay usable and `shell` stays visible (each command is validated against the read-only allow table), while write-like tools and `background_task` are removed from the model-visible surface and denied at execution. Max-depth / `BlockDelegation` still hides spawn tools.
- **Runtime-owned agent essentials** (`enter_plan_mode`, `exit_plan_mode`, `ask_user_question`, collab/team control tools, `todos`/`get_goal`/`update_goal`, `search_tool`, …) bypass the hard **allowlist** gate so a narrow `--allow-tool` / `allow_tools` list cannot brick the agent control plane. They still honor:
  - explicit `deny_tools` / `--deny-tool`
  - capability scope (e.g. a narrowed child without `agent_management` still cannot `spawn_agent`)
  - read-only / sandbox gates
