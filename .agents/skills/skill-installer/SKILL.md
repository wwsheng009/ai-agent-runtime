---
name: skill-installer
description: Install Codex skills into this workspace's .agents/skills or the user's ~/.aicli/skills and ~/.aicli/agents/skills directories from a curated list or a GitHub repo path. Use when a user asks to list installable skills, install a curated skill, or install a skill from another repo (including private repos).
metadata:
  short-description: Install curated skills from openai/skills or other repos
---

# Skill Installer

Helps install skills. By default these are from https://github.com/openai/skills/tree/main/skills/.curated, but users can also provide other locations. Experimental skills live in https://github.com/openai/skills/tree/main/skills/.experimental and can be installed the same way.

If the skill should be auto-loaded by this repository, the repo-local copy of this installer will place it into `./.agents/skills` by default; use `--dest` only when you need to override the destination. If it is meant for a personal installation, use `~/.aicli/skills` by default and `~/.aicli/agents/skills` when you need the alternate user skills root.

Use the helper scripts based on the task:
- List skills when the user asks what is available, or if the user uses this skill without specifying what to do. Default listing is `.curated`, but you can pass `--path skills/.experimental` when they ask about experimental skills.
- Install from the curated list when the user provides a skill name.
- Install from another repo when the user provides a GitHub repo/path (including private repos).

Install skills with the helper scripts.

## Communication

When listing skills, output approximately as follows, depending on the context of the user's request. If they ask about experimental skills, list from `.experimental` instead of `.curated` and label the source accordingly:
"""
Skills from {repo}:
1. skill-1
2. skill-2 (already installed)
3. ...
Which ones would you like installed?
"""

After installing a skill, tell the user: "Restart Codex to pick up new skills."

## Scripts

All of these scripts use network, so when running in the sandbox, request escalation when running them.

- `scripts/list-skills.py` (prints skills list with installed annotations)
- `scripts/list-skills.py --format json`
- Example (experimental list): `scripts/list-skills.py --path skills/.experimental`
- `scripts/install-skill-from-github.py --repo <owner>/<repo> --path <path/to/skill> [<path/to/skill> ...]`
- `scripts/install-skill-from-github.py --url https://github.com/<owner>/<repo>/tree/<ref>/<path>`
- Example (experimental skill): `scripts/install-skill-from-github.py --repo openai/skills --path skills/.experimental/<skill-name>`
- Example (repo-local install, default in this workspace): `scripts/install-skill-from-github.py --repo <owner>/<repo> --path <path/to/skill>`
- Example (explicit user install): `scripts/install-skill-from-github.py --repo <owner>/<repo> --path <path/to/skill> --dest ~/.aicli/agents/skills`

## Behavior and Options

- Defaults to direct download for public GitHub repos.
- If download fails with auth/permission errors, falls back to git sparse checkout.
- Aborts if the destination skill directory already exists.
- Installs into `./.agents/skills/<skill-name>` by default when this repository copy is used.
- Falls back to `~/.aicli/skills/<skill-name>` when run outside a repo-local workspace.
- Use `--dest ~/.aicli/agents/skills` when the alternate user skills root is required.
- Use `--dest` when you need a different target directory.
- Multiple `--path` values install multiple skills in one run, each named from the path basename unless `--name` is supplied.
- Options: `--ref <ref>` (default `main`), `--dest <path>`, `--method auto|download|git`.

## Notes

- Curated listing is fetched from `https://github.com/openai/skills/tree/main/skills/.curated` via the GitHub API. If it is unavailable, explain the error and exit.
- Private GitHub repos can be accessed via existing git credentials or optional `GITHUB_TOKEN`/`GH_TOKEN` for download.
- Git fallback tries HTTPS first, then SSH.
- The skills at https://github.com/openai/skills/tree/main/skills/.system are preinstalled, so no need to help users install those. If they ask, just explain this. If they insist, you can download and overwrite.
- Installed annotations come from the repository-local `./.agents/skills` directory when this workspace copy is used, and from the user's `~/.aicli/skills` or `~/.aicli/agents/skills` directories otherwise.
