# aicli Docs

This directory contains `aicli`-specific runtime documentation.

Recommended entry points:

- [quickstart.md](./quickstart.md)
  - Shortest path and first-install checklist with success signals: install → `aicli init --global` → `aicli login` → `aicli` chat, plus doctor checks and the first few commands after setup.
- [install.md](./install.md)
  - Full install/config guide: configuration loading order, starter bootstrap, `aicli init`, `aicli login`, default `aicli` / `aicli chat` startup, session/resume flags, MCP / skill / plugin / agent CLI overview, shell/background notes, current chat slash commands, subagent difficulty routing dry-run/debug notes, and uninstall.
- [faq.md](./faq.md)
  - Common troubleshooting: empty providers, login models validation, `/model` switch failures, HTTP 401, Windows PATH, config overrides, logs, and doctor usage.
- [exec.md](./exec.md)
  - Headless `aicli exec` usage, JSON/JSONL output contracts, session resume, code review, schema validation, config overrides, exit codes, and CI examples.
- [agents.md](./agents.md)
  - Portable AgentDefinition, `aicli chat --agent` (with or without profile), `spawn_agent.agent_type` defaults, `aicli agent stdio` ACP host, agents three-layer meanings, and difference from skill `openai.yaml`.
- [tool_image_generate.md](./tool_image_generate.md)
  - `openai_image_generate` / `aicli image` / chat `/image` paths: OpenAI-compatible images API vs Codex native image generation, auto path selection, and output directories.
- [windows7.md](./windows7.md)
  - Windows 7 end-user installation, isolated configuration/session paths, console/IME modes, and troubleshooting.
- [windows7-build.md](./windows7-build.md)
  - Developer build and verification recipe for the Go 1.20.14 `win7compat` binaries and release bundle.

Related runtime docs:

- [../skill_runtime/aicli_skills_usage.md](../skill_runtime/aicli_skills_usage.md)
  - How `aicli` / `aicli chat` exposes and routes skills (default-enabled, top-k, exec disable-tools false negatives).
- [../plan/grok-harness-productization-implementation-plan.md](../plan/grok-harness-productization-implementation-plan.md)
  - Harness productization plan / Iteration A checklist.
- [../codex/image-generation-capability-flow.md](../codex/image-generation-capability-flow.md)
  - Codex provider image-generation capability and diagnostic notes.
