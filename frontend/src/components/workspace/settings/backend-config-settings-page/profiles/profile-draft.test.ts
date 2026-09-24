// Profiles 草稿模型单测：view → draft → document → diff 的纯函数行为。
//
// 覆盖纪律（Batch 8 契约对账版）：
//   * 草稿字段名严格等于 profile schema（tools.allowlist / skills.denylist /
//     mcp.use_servers / prompts.mode / runtime.overrides），只读面不进草稿；
//   * 校验返回 issue 码（不是文案），UI 侧才能映射 i18n；
//   * 写回文档必须保留原始 document 的未知字段，避免前端静默丢字段；
//   * 变更检测基于 resolved view，格式差异（前后空白）不算改动。

import { describe, expect, it } from "vitest";

import type { RuntimeProfileView } from "@/types/runtime";

import {
  buildProfileDocument,
  createProfileDraft,
  createEmptyProfileDraft,
  diffProfileDraft,
  formatListText,
  hasProfileDraftChanges,
  parseListText,
  parseOverrideValue,
  validateProfileDraft,
} from "./profile-draft";

function makeView(patch: Partial<RuntimeProfileView> = {}): RuntimeProfileView {
  const view: RuntimeProfileView = {
    ref: "user:reviewer",
    name: "reviewer",
    description: "code review",
    source: "registered",
    layer: "user",
    path: "/root/profiles/reviewer",
    profileFile: "/root/profiles/reviewer/profile.yaml",
    mtime: "2026-09-24T10:00:00Z",
    valid: true,
    errorCount: 0,
    warningCount: 0,
    issues: [],
    document: {
      profile: { name: "reviewer", description: "code review" },
      tools: { allowlist: ["read_file", "grep"], denylist: ["write_file"] },
      unknown_future_field: { keep: true },
    },
    tools: {
      allowlist: ["read_file", "grep"],
      denylist: ["write_file"],
      agentId: "reviewer",
      agentAllowlist: [],
      agentDenylist: [],
      effective: ["read_file", "grep"],
      excluded: ["write_file"],
      sources: ["profile"],
    },
    skills: {
      allowlist: ["review"],
      denylist: [],
      dirs: ["skills"],
      exposureMode: "top_k",
      topK: 5,
    },
    mcp: {
      useServers: ["chrome"],
      excludeServers: [],
      servers: [{ name: "chrome", used: true, excluded: false }],
      serverCount: 3,
      usedCount: 1,
    },
    prompts: {
      mode: "append",
      system: { path: "prompts/system.md", exists: true },
      role: { path: "", exists: false },
      tools: { path: "", exists: false },
    },
    agents: {
      defaultAgent: "reviewer",
      available: ["reviewer"],
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
      permissionMode: "plan",
      permissionModeSource: "builtin",
      provider: "anthropic",
      model: "claude",
    },
    estimate: {
      basis: "resolved",
      toolCount: 2,
      toolTokens: 1200,
      promptTokens: 800,
      totalTokens: 2000,
      toolAllowCount: 2,
      toolDenyCount: 1,
      skillAllowCount: 1,
      skillDenyCount: 0,
      skillDirCount: 1,
    },
    preview: false,
    defaultAgent: "reviewer",
    promptMode: "append",
    writeTarget: "/root/profiles/reviewer/profile.yaml",
  };
  return { ...view, ...patch };
}

describe("profiles 草稿模型", () => {
  it("列表文本解析：去空行、去重、逗号与换行都算分隔符", () => {
    expect(parseListText(" a , b\n\na\nc \n")).toEqual(["a", "b", "c"]);
    expect(parseListText("")).toEqual([]);
    expect(formatListText(["a", "b"])).toBe("a\nb");
  });

  it("view → draft：字段名对齐 profile schema，只读面不进草稿", () => {
    const draft = createProfileDraft(makeView());

    expect(draft.name).toBe("reviewer");
    expect(draft.description).toBe("code review");
    expect(draft.defaultAgent).toBe("reviewer");
    expect(draft.toolsAllowlist).toBe("read_file\ngrep");
    expect(draft.toolsDenylist).toBe("write_file");
    expect(draft.skillsAllowlist).toBe("review");
    expect(draft.skillsDenylist).toBe("");
    expect(draft.mcpUseServers).toBe("chrome");
    expect(draft.mcpExcludeServers).toBe("");
    expect(draft.promptMode).toBe("append");
    expect(draft.overrides).toEqual([
      { key: "retry.max_attempts", value: "3", allowed: true },
    ]);
    // 只读面没有对应字段：dirs / exposureMode / preferences 等不参与草稿。
    expect(Object.keys(draft)).not.toContain("skillsDirs");
    expect(Object.keys(draft)).not.toContain("preferences");
  });

  it("空草稿：全部字段为空、promptMode 默认 replace（模板由后端决定）", () => {
    const draft = createEmptyProfileDraft();

    expect(draft.name).toBe("");
    expect(draft.toolsAllowlist).toBe("");
    expect(draft.promptMode).toBe("replace");
    expect(draft.overrides).toEqual([]);
  });

  it("未改动时 diff 为空，空白差异不算改动", () => {
    const view = makeView();
    const draft = createProfileDraft(view);

    expect(diffProfileDraft(draft, view)).toEqual([]);
    expect(hasProfileDraftChanges(draft, view)).toBe(false);

    const whitespace = { ...draft, description: "  code review  " };
    expect(diffProfileDraft(whitespace, view)).toEqual([]);
  });

  it("变更检测：列表内容、文本字段、override 增删各产生一条路径", () => {
    const view = makeView();
    const draft = {
      ...createProfileDraft(view),
      name: "reviewer2",
      toolsAllowlist: "read_file",
      overrides: [{ key: "retry.max_attempts", value: "5", allowed: true }],
    };

    expect(diffProfileDraft(draft, view)).toEqual([
      "profile.name",
      "tools.allowlist",
      "runtime.overrides.retry.max_attempts",
    ]);

    const removed = { ...createProfileDraft(view), overrides: [] };
    expect(diffProfileDraft(removed, view)).toEqual(["runtime.overrides.retry.max_attempts"]);

    // 列表顺序变化也算改动（写回后 YAML 顺序会变）。
    const reordered = { ...createProfileDraft(view), toolsAllowlist: "grep\nread_file" };
    expect(diffProfileDraft(reordered, view)).toEqual(["tools.allowlist"]);
  });

  it("校验：名称必填 / 名称非法（路径与后端 issues.path 同名）", () => {
    const base = createProfileDraft(makeView());

    expect(validateProfileDraft({ ...base, name: " " })).toEqual([
      { code: "nameRequired", path: "profile.name" },
    ]);
    expect(validateProfileDraft({ ...base, name: "Bad Name" })).toEqual([
      { code: "nameInvalid", path: "profile.name", detail: "Bad Name" },
    ]);
  });

  it("校验：allow 与 deny 同时命中即冲突，detail 给出冲突项", () => {
    const draft = {
      ...createProfileDraft(makeView()),
      toolsAllowlist: "read_file",
      toolsDenylist: "read_file,write_file",
    };

    expect(validateProfileDraft(draft)).toContainEqual({
      code: "toolConflict",
      path: "tools.denylist",
      detail: "read_file",
    });
  });

  it("校验：提示词模式只能是 replace / append", () => {
    const draft = {
      ...createProfileDraft(makeView()),
      promptMode: "merge" as never,
    };

    expect(validateProfileDraft(draft)).toEqual([
      { code: "promptModeInvalid", path: "prompts.mode", detail: "merge" },
    ]);
  });

  it("校验：override 键必填、值必填、重复键、非放行键", () => {
    const base = createProfileDraft(makeView());

    const issues = validateProfileDraft({
      ...base,
      overrides: [
        { key: "", value: "1", allowed: true },
        { key: "retry.max_attempts", value: "", allowed: true },
        { key: "retry.timeout", value: "5s", allowed: true },
        { key: "retry.timeout", value: "6s", allowed: true },
        { key: "unknown.key", value: "x", allowed: false },
      ],
    });

    expect(issues.map((issue) => issue.code)).toEqual([
      "overrideKeyRequired",
      "overrideValueRequired",
      "overrideDuplicate",
      "overrideNotAllowed",
    ]);
    expect(issues[2]).toMatchObject({
      path: "runtime.overrides.retry.timeout",
      detail: "retry.timeout",
    });
    expect(issues[3]).toMatchObject({
      path: "runtime.overrides.unknown.key",
      detail: "unknown.key",
    });
  });

  it("校验：仅当调用方显式传入白名单时，白名单外的键才被拒", () => {
    const base = createProfileDraft(makeView());
    const draft = {
      ...base,
      overrides: [...base.overrides, { key: "skills_runtime.enabled", value: "true", allowed: true }],
    };

    // 默认（后端未下发白名单）：只认逐条 allowed，新键不拦。
    expect(validateProfileDraft(draft)).toEqual([]);

    // 显式白名单：白名单外的键一律阻断（错误码 overrideNotAllowed）。
    expect(
      validateProfileDraft(draft, { allowedOverrideKeys: ["retry.max_attempts"] }),
    ).toEqual([
      {
        code: "overrideNotAllowed",
        path: "runtime.overrides.skills_runtime.enabled",
        detail: "skills_runtime.enabled",
      },
    ]);
  });

  it("override 值类型化：布尔 / 数字 / null / 引号字符串", () => {
    expect(parseOverrideValue("true")).toBe(true);
    expect(parseOverrideValue("FALSE")).toBe(false);
    expect(parseOverrideValue(" 12 ")).toBe(12);
    expect(parseOverrideValue("-1.5")).toBe(-1.5);
    expect(parseOverrideValue("null")).toBeNull();
    expect(parseOverrideValue("~")).toBeNull();
    expect(parseOverrideValue('"5"')).toBe("5");
    expect(parseOverrideValue("5s")).toBe("5s");
    expect(parseOverrideValue("")).toBe("");
  });

  it("写回文档：只覆盖已编辑组，未知字段原样保留；点分键写成嵌套映射", () => {
    const view = makeView();
    const draft = {
      ...createProfileDraft(view),
      description: "",
      toolsAllowlist: "read_file\ngrep\nlist_dir",
      mcpUseServers: "chrome\nfiles",
      promptMode: "replace" as const,
      overrides: [{ key: "skills_runtime.enabled", value: "true", allowed: true }],
    };

    const result = buildProfileDocument(draft, view.document);
    expect(result.ok).toBe(true);
    if (!result.ok) {
      return;
    }

    expect(result.document).toEqual({
      profile: { name: "reviewer", default_agent: "reviewer" },
      tools: { allowlist: ["read_file", "grep", "list_dir"], denylist: ["write_file"] },
      skills: { allowlist: ["review"] },
      mcp: { use_servers: ["chrome", "files"] },
      prompts: { mode: "replace" },
      runtime: { overrides: { skills_runtime: { enabled: true } } },
      unknown_future_field: { keep: true },
    });
    // 原始 document 不被就地修改（写回基于克隆）。
    expect(view.document.profile).toEqual({ name: "reviewer", description: "code review" });
  });

  it("写回文档：清空列表会删掉对应键，空分组整段裁剪", () => {
    const view = makeView();
    const draft = {
      ...createProfileDraft(view),
      toolsAllowlist: "",
      toolsDenylist: "",
      skillsAllowlist: "",
      skillsDenylist: "",
      mcpUseServers: "",
      mcpExcludeServers: "",
      overrides: [],
    };

    const result = buildProfileDocument(draft, view.document);
    expect(result.ok).toBe(true);
    if (!result.ok) {
      return;
    }

    expect(result.document).toEqual({
      profile: { name: "reviewer", description: "code review", default_agent: "reviewer" },
      prompts: { mode: "append" },
      unknown_future_field: { keep: true },
    });
  });

  it("写回文档：校验失败时返回 issue 列表且不产出 document", () => {
    const view = makeView();
    const result = buildProfileDocument({ ...createProfileDraft(view), name: "" }, view.document);

    expect(result.ok).toBe(false);
    if (result.ok) {
      return;
    }
    expect(result.issues.map((issue) => issue.code)).toEqual(["nameRequired"]);
  });
});
