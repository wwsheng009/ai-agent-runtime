---
name: aicli
description: Use when Codex should delegate a bounded task to the local aicli command-line agent, call aicli exec/chat for model reasoning, runtime tools, project skills, team orchestration, code review, or compatibility testing, or package aicli as a tool bridge for another agent environment. Prefer this skill only when aicli provides useful extra context, configured providers, skills, runtime-server integration, or a separate agent run; do not use it for simple shell/file tasks Codex can perform directly.
---

# AICLI Bridge

Use `aicli` as a local agent bridge when it is the right execution surface, not as a default replacement for Codex tools.

## When To Use

Use `aicli` for:

- Running an independent model pass with the user's configured aicli providers or profiles.
- Exercising aicli-specific features: skills routing, runtime-server mode, local team orchestration, session resume, code review, image inputs, or JSON output.
- Checking whether an aicli CLI flow works from a real terminal.
- Delegating a bounded subtask where a separate agent transcript is useful and recursion risk is acceptable.

Do not use `aicli` for:

- Basic file reads, searches, edits, tests, or shell commands that Codex can run directly.
- Open-ended nested agent work without a timeout and a clear stop condition.
- Tasks that require interactive approval while running from a non-interactive Codex shell.
- Calling `aicli` to ask it to call Codex again.

## Quick Checks

Verify availability before relying on it:

```powershell
Get-Command aicli
aicli --help
aicli exec --help
```

Use explicit paths when needed, for example from this repository:

```powershell
E:\projects\ai\ai-agent-runtime\backend\aicli.exe exec --help
```

## Config Resolution

This skill file does not load aicli configuration by itself. It only instructs Codex to launch the `aicli` executable or, when available, to call the deterministic `aicli_exec` tool. The launched `aicli` process resolves configuration using the normal CLI rules:

1. The root `--config` / `-c` flag wins when provided.
2. If `--config` is omitted, aicli uses the first existing file in this order:
   - `$HOME/.aicli/config.yaml`
   - `./.aicli/config.yaml`
   - `./aicli.yaml`
   - `./configs/config.yaml`
3. If none exists for commands that need configuration, aicli creates a starter config at `$HOME/.aicli/config.yaml` when possible, otherwise `./.aicli/config.yaml`.
4. Relative `./...` entries are resolved from the current shell working directory used by Codex.

For stable automation from an agent/tool host, prefer the `aicli_exec` tool when available:

```json
{
  "prompt": "Summarize the configured provider behavior.",
  "disable_tools": true,
  "output": "text",
  "timeout": "2m"
}
```

Only pass `config` when the user explicitly needs a specific file. If `config` is omitted, `aicli_exec` does not inject the parent session config; the child `aicli` process uses the normal CLI lookup order above.

When this skill is invoked from inside `aicli chat` as a direct skill command, `aicli` treats it as a local bridge because `agents/openai.yaml` declares the local `aicli_exec` tool dependency. The behavior is based on that dependency declaration, not on the skill folder or skill name:

```text
/skill aicli 查看当前日期
/skill aicli {"prompt":"查看当前日期","options":{"timeout":"60s","request-timeout":"45s"}}
/skill aicli {"prompt":"查看当前日期","options":{"log_dir":"E:\\temp\\aicli-child-logs","debug_http":true,"fail_fast":true}}
```

That internal path calls `aicli_exec` with safe defaults (`disable_tools=true`, `output=text`, `timeout=2m`). It does not copy the parent chat session's provider, model, or config into the child process. Use the JSON `options` object only when an explicit override is required. Supported bridge options include `cwd`, `config`, `provider`, `model`, `profile`, `agent`, `log_dir`, `session_dir`, `user`, `title`, `output`, `json`, `envelope`, `disable_tools`, `permission_mode`, `yolo`, `skills_dir`, `skills_mode`, `timeout`, `timeout_ms`, `request_timeout`, `debug_http`, `fail_fast`, `executable_path`, `allow_nested`, `output_bytes_cap`, and `disable_output_cap`; hyphenated names such as `log-dir` and `debug-http` are normalized to underscore names. If this skill is renamed or installed under another directory, keep the `agents/openai.yaml` dependency on `aicli_exec` so the deterministic bridge remains active.

For direct shell automation, prefer an explicit config path:

```powershell
aicli --config E:\projects\ai\ai-agent-runtime\backend\configs\config.yaml exec --disable-tools --output text --timeout 2m "Summarize the configured provider behavior."
aicli --log-dir E:\temp\aicli-child-logs exec --debug-http --fail-fast --disable-tools --output text --timeout 2m "查看当前日期"
```

If the task must use the current workspace's local config, first run from that workspace root or pass `--config .\.aicli\config.yaml` explicitly.

## Default Invocation Patterns

For pure text/model reasoning through the deterministic tool, use:

```json
{
  "prompt": "Summarize the design tradeoffs in this file.",
  "disable_tools": true,
  "output": "text",
  "timeout": "2m"
}
```

For pure text/model reasoning through a shell fallback, disable tools so aicli will not request approval:

```powershell
aicli exec --disable-tools --output text --timeout 2m "Summarize the design tradeoffs in this file."
```

For structured output:

```json
{
  "prompt": "Return JSON with keys: summary, risks, next_steps.",
  "disable_tools": true,
  "output": "json",
  "envelope": true,
  "timeout": "2m"
}
```

Shell fallback:

```powershell
aicli exec --disable-tools --output json --envelope --timeout 2m "Return JSON with keys: summary, risks, next_steps."
```

For a trusted local tool run where aicli tools are required:

```powershell
aicli exec --yolo --output text --timeout 5m "Inspect the current workspace and report likely causes of the failing test."
```

For a conservative tool run that may need approvals, use interactive `aicli chat` outside headless automation:

```powershell
aicli chat --permission-mode default
```

For project skills:

```json
{
  "prompt": "Use the relevant skill to ...",
  "skills_dir": ["E:\\projects\\ai\\ai-agent-runtime\\.agents\\skills"],
  "skills_mode": "prefer",
  "disable_tools": false,
  "timeout": "5m"
}
```

Shell fallback:

```powershell
aicli exec --skills-dir E:\projects\ai\ai-agent-runtime\.agents\skills --skills-mode prefer --output text --timeout 5m "Use the relevant skill to ..."
```

For code review:

```powershell
aicli exec review --uncommitted --output text --timeout 5m
```

## Safety Rules

- Always set `--timeout` for Codex-launched `aicli exec` calls.
- Prefer `aicli_exec` over shell when it is available. It uses argv process execution and passes the prompt on stdin, avoiding shell quoting and Windows command-line length problems.
- Prefer `--disable-tools` unless the task explicitly needs aicli tools, project skills, or team/runtime features.
- Prefer `--output text` for human-readable answers and `--output json --envelope` when parsing.
- Use `--yolo` only for trusted local workspaces where tool execution is intended.
- For questions about dates, environment, or simple facts, avoid aicli entirely or use `--disable-tools`.
- Do not pass secrets in prompts; prefer configured profiles/providers.
- Keep prompts bounded: state the objective, allowed paths, expected output, and stop condition.

## Windows Notes

This repository is commonly developed on Windows. Keep inline `aicli` prompts short enough for PowerShell/cmd command-line limits. For long prompts or large context, write input to a temporary file or pipe stdin:

```powershell
Get-Content .\task.md -Raw | aicli exec --disable-tools --output text --timeout 5m -p "Follow these instructions."
```

Avoid huge inline JSON, heredocs, or nested shell quoting through `cmd.exe`.

## Interpreting Results

If a non-interactive run fails with an approval error:

- Re-run with `--disable-tools` if the task is pure text/model reasoning.
- Re-run with `--yolo` only if tool execution is expected and trusted.
- Switch to `aicli chat` if human approval or follow-up questions are required.

If output is too verbose, rerun with a stricter prompt and ask for concise sections or JSON.

If output needs to become repo changes, Codex should inspect the result and apply edits directly; do not ask aicli to mutate files unless the user explicitly wants aicli to own that execution.

## Skill Packaging Guidance

This skill lives in the project skill source directory:

```text
E:\projects\ai\ai-agent-runtime\.agents\skills\aicli\SKILL.md
```

It is intentionally a standard Codex-style skill directory. A future `aicli` installer can copy the whole `aicli` directory into target tool skill roots such as `~/.codex/skills`, `~/.aicli/skills`, or workspace `.agents/skills` without transforming the content.
