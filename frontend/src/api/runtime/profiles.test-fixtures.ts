// Runtime profile API 测试夹具：ref 句柄与后端 payload 样例（Batch 8 §10.5 契约）。
//
// 独立成文件的原因：profiles.test.ts 贴近 P0-2 行数门禁（≤500 非空行），
// 把大块 payload 常量抽到这里，测试文件只保留断言与 fetch mock 工具。
export const REF = "user:reviewer plan";
export const ENCODED_REF = encodeURIComponent(REF);

/** 后端 resolved view（GET /preview 同构）；字段名取自 profile resolver 的 snake_case。 */
export const VIEW_PAYLOAD = {
  ref: REF,
  name: "reviewer",
  description: "code review profile",
  source: "profile",
  layer: "user",
  path: "profiles/reviewer.yaml",
  profile_file: "profiles/reviewer.yaml",
  mtime: "2026-09-24T10:00:00Z",
  valid: true,
  error_count: 0,
  warning_count: 1,
  issues: [{ path: "skills.top_k", message: "large top_k", severity: "warning" }],
  // 写回基线：后端 GET 用 `spec` 携带原始 profile.yaml 文档。
  spec: { profile: { name: "reviewer" }, tools: { allowlist: ["read_file"] } },
  tools: {
    allowlist: ["read_file", "grep"],
    denylist: ["write_file"],
    effective: ["read_file", "grep"],
    excluded: ["write_file"],
    sources: ["profile", "global"],
  },
  skills: {
    allowlist: ["review"],
    denylist: [],
    dirs: ["skills"],
    exposure_mode: "top_k",
    top_k: 5,
  },
  mcp: {
    use_servers: ["chrome"],
    exclude_servers: ["legacy"],
    servers: [
      { name: "chrome", used: true, excluded: false },
      { name: "legacy", used: false, excluded: true },
    ],
    server_count: 2,
    used_count: 1,
  },
  prompts: {
    mode: "append",
    system: { path: "prompts/system.md", exists: true },
    role: { path: "", exists: false },
  },
  agents: {
    default_agent: "reviewer",
    available: ["reviewer", "coder"],
    entries: [
      {
        id: "reviewer",
        provider: "anthropic",
        model: "claude",
        tools: { allowlist: ["read_file"], denylist: [] },
      },
    ],
  },
  overrides: {
    keys: ["retry.max_attempts"],
    origins: { "retry.max_attempts": "profile" },
    count: 1,
    entries: [{ key: "retry.max_attempts", value: "3", origin: "profile", allowed: true }],
  },
  preferences: {
    permission_mode: "plan",
    permission_mode_source: "profile",
    provider: "anthropic",
    model: "claude",
  },
  estimate: {
    basis: "resolved",
    tool_count: 2,
    tool_tokens: 1200,
    prompt_tokens: 800,
    total_tokens: 2000,
    tool_allow_count: 2,
    tool_deny_count: 1,
    skill_allow_count: 1,
    skill_deny_count: 0,
    skill_dir_count: 1,
  },
  preview: true,
  default_agent: "reviewer",
};

/** 创建/复制的成功响应（POST /profiles 与 /duplicate 同构）。 */
export const CREATE_PAYLOAD = {
  created: true,
  name: "new",
  root: "/home/u/.aicli/profiles/new",
  layer: "user",
  files: ["profile.yaml", "prompts/system.md"],
  profile: { ref: "user:new", name: "new" },
  registered: true,
  default_profile_set: false,
  affects: "new_sessions_only",
  config_path: "/home/u/.aicli/config.yaml",
};
