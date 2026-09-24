// Profiles 草稿模型（纯函数，无 React 依赖）：view → draft → document → diff。
//
// 拆分理由：编辑器各卡片只负责「渲染 draft / 上报 draft 变更」，所有校验、
// 序列化、变更检测都在这里，便于 vitest 直接覆盖（不需要 DOM）。
//
// 纪律（Batch 8 契约对账后收紧）：
//   * 草稿只承载**可写字段**，且键名严格等于 profile schema
//     （backend/internal/profile/spec.go）：
//       profile.name / profile.description / profile.default_agent
//       tools.allowlist / tools.denylist
//       skills.allowlist / skills.denylist
//       mcp.use_servers / mcp.exclude_servers
//       prompts.mode
//       runtime.overrides（点分键 → 嵌套映射，值按标量类型化）
//   * 只读面（skills.dirs/exposure_mode/top_k、prompts.*.path、agents.entries、
//     preferences.*）不在草稿里，UI 直接读 view 分组，避免前端复刻解析语义；
//   * 写回文档从 view.document（原始 profile.yaml）出发，只覆盖被编辑的组，
//     未知字段与未编辑组原样保留（避免前端丢后端字段）；
//   * 校验失败返回 issue 码（i18n key 由 UI 侧映射），不返回中文文案；
//   * 变更检测基于 resolved view，而不是原始 YAML，避免「格式差异」被当成改动。

import type { RuntimeProfileDocument, RuntimeProfileView } from "@/types/runtime";

export type ProfileDraftOverride = {
  key: string;
  /** 值文本；写回时按标量类型化（true/false、数字、null、其余字符串）。 */
  value: string;
  /** 后端 D14 白名单判定；false 时禁止写入（仅既有键会给出）。 */
  allowed: boolean;
};

export type ProfileDraft = {
  name: string;
  description: string;
  defaultAgent: string;
  toolsAllowlist: string;
  toolsDenylist: string;
  skillsAllowlist: string;
  skillsDenylist: string;
  mcpUseServers: string;
  mcpExcludeServers: string;
  promptMode: ProfilePromptMode;
  overrides: ProfileDraftOverride[];
};

export type ProfilePromptMode = "replace" | "append";

export const profilePromptModes: ProfilePromptMode[] = ["replace", "append"];

/** profile 名 → ref 的合法字符集：小写字母数字开头，允许 . _ -。 */
export const profileNamePattern = /^[a-z0-9][a-z0-9._-]*$/;

/** reasoning effort 档位（只读展示用：profile 层不写 reasoning_effort）。 */
export const profileReasoningEfforts = ["low", "medium", "high", "max"] as const;

/** 权限模式 canonical 值（只读展示用）；bypass 不允许写进 profile（D16）。 */
export const profilePermissionModes = ["default", "accept_edits", "plan"] as const;
export const forbiddenProfilePermissionMode = "bypass_permissions";

export type ProfileDraftIssueCode =
  | "nameRequired"
  | "nameInvalid"
  | "toolConflict"
  | "promptModeInvalid"
  | "overrideKeyRequired"
  | "overrideValueRequired"
  | "overrideDuplicate"
  | "overrideNotAllowed";

export type ProfileDraftIssue = {
  code: ProfileDraftIssueCode;
  /** 变更路径（与后端 issues.path 同名，便于联动定位）。 */
  path: string;
  /** 冲突/非法值等补充信息（如冲突的工具名、重复的键名）。 */
  detail?: string;
};

export type ProfileDraftValidationOptions = {
  /**
   * 后端下发的 override 白名单（命中即禁止写入）。为空表示后端未提供，
   * 此时只依赖逐条 allowed 标记与后端 validate 兜底。
   */
  allowedOverrideKeys?: string[];
};

export function parseListText(value: string): string[] {
  const seen = new Set<string>();
  const result: string[] = [];
  for (const item of value.split(/[\n,]/)) {
    const text = item.trim();
    if (!text || seen.has(text)) {
      continue;
    }
    seen.add(text);
    result.push(text);
  }
  return result;
}

export function formatListText(values: string[]): string {
  return values.join("\n");
}

export function createProfileDraft(view: RuntimeProfileView): ProfileDraft {
  return {
    name: view.name,
    description: view.description,
    defaultAgent: view.agents.defaultAgent,
    toolsAllowlist: formatListText(view.tools.allowlist),
    toolsDenylist: formatListText(view.tools.denylist),
    skillsAllowlist: formatListText(view.skills.allowlist),
    skillsDenylist: formatListText(view.skills.denylist),
    mcpUseServers: formatListText(view.mcp.useServers),
    mcpExcludeServers: formatListText(view.mcp.excludeServers),
    promptMode: view.prompts.mode === "append" ? "append" : "replace",
    overrides: view.overrides.entries.map((entry) => ({
      key: entry.key,
      value: entry.value,
      allowed: entry.allowed,
    })),
  };
}

/** 空草稿（新建 profile 时使用，模板由后端决定）。 */
export function createEmptyProfileDraft(): ProfileDraft {
  return {
    name: "",
    description: "",
    defaultAgent: "",
    toolsAllowlist: "",
    toolsDenylist: "",
    skillsAllowlist: "",
    skillsDenylist: "",
    mcpUseServers: "",
    mcpExcludeServers: "",
    promptMode: "replace",
    overrides: [],
  };
}

function intersect(left: string[], right: string[]) {
  const rightSet = new Set(right);
  return left.filter((item) => rightSet.has(item));
}

/**
 * 校验草稿：返回全部 issue（不短路），UI 一次展示完。
 * 白名单校验：逐条 `allowed === false` 一律阻断；`allowedOverrideKeys`
 * 仅在调用方显式提供时参与判定（前端不自己维护会漂移的白名单）。
 */
export function validateProfileDraft(
  draft: ProfileDraft,
  options: ProfileDraftValidationOptions = {},
): ProfileDraftIssue[] {
  const issues: ProfileDraftIssue[] = [];
  const allowedKeys = options.allowedOverrideKeys ?? [];
  const name = draft.name.trim();
  if (!name) {
    issues.push({ code: "nameRequired", path: "profile.name" });
  } else if (!profileNamePattern.test(name)) {
    issues.push({ code: "nameInvalid", path: "profile.name", detail: name });
  }

  const conflicts = intersect(parseListText(draft.toolsAllowlist), parseListText(draft.toolsDenylist));
  if (conflicts.length > 0) {
    issues.push({ code: "toolConflict", path: "tools.denylist", detail: conflicts.join(", ") });
  }

  if (!profilePromptModes.includes(draft.promptMode)) {
    issues.push({ code: "promptModeInvalid", path: "prompts.mode", detail: draft.promptMode });
  }

  const seenKeys = new Set<string>();
  for (const override of draft.overrides) {
    const key = override.key.trim();
    const value = override.value.trim();
    if (!key && !value) {
      continue;
    }
    if (!key) {
      issues.push({ code: "overrideKeyRequired", path: "runtime.overrides" });
      continue;
    }
    if (seenKeys.has(key)) {
      issues.push({ code: "overrideDuplicate", path: `runtime.overrides.${key}`, detail: key });
      continue;
    }
    seenKeys.add(key);
    if (override.allowed === false || (allowedKeys.length > 0 && !allowedKeys.includes(key))) {
      issues.push({ code: "overrideNotAllowed", path: `runtime.overrides.${key}`, detail: key });
      continue;
    }
    if (!value) {
      issues.push({ code: "overrideValueRequired", path: `runtime.overrides.${key}`, detail: key });
    }
  }

  return issues;
}

/**
 * 值文本 → 标量：`true/false` → 布尔，整数/小数 → 数字，`null`/`~` → null，
 * 引号包裹 → 强制字符串，其余原样字符串（避免把 "5" 写成字符串导致类型不匹配）。
 */
export function parseOverrideValue(text: string): unknown {
  const trimmed = text.trim();
  if (trimmed === "") {
    return "";
  }
  const lower = trimmed.toLowerCase();
  if (lower === "true") {
    return true;
  }
  if (lower === "false") {
    return false;
  }
  if (lower === "null" || lower === "~") {
    return null;
  }
  if (/^-?\d+$/.test(trimmed)) {
    return Number(trimmed);
  }
  if (/^-?(?:\d+\.\d*|\.\d+)(?:[eE][+-]?\d+)?$/.test(trimmed)) {
    return Number(trimmed);
  }
  if (/^".*"$/.test(trimmed) || /^'.*'$/.test(trimmed)) {
    return trimmed.slice(1, -1);
  }
  return trimmed;
}

/** 点分键写入嵌套映射（`a.b.c` → `{a:{b:{c:value}}}`）。 */
export function setNestedOverride(
  target: Record<string, unknown>,
  key: string,
  value: unknown,
): void {
  const parts = key.split(".").filter((part) => part.length > 0);
  let cursor = target;
  for (let index = 0; index < parts.length; index += 1) {
    const part = parts[index];
    if (index === parts.length - 1) {
      cursor[part] = value;
      return;
    }
    const next = cursor[part];
    if (next && typeof next === "object" && !Array.isArray(next)) {
      cursor = next as Record<string, unknown>;
      continue;
    }
    const created: Record<string, unknown> = {};
    cursor[part] = created;
    cursor = created;
  }
}

function cloneDocument(base: RuntimeProfileDocument): RuntimeProfileDocument {
  try {
    return JSON.parse(JSON.stringify(base ?? {})) as RuntimeProfileDocument;
  } catch {
    return { ...(base ?? {}) };
  }
}

function sectionOf(document: RuntimeProfileDocument, key: string): Record<string, unknown> {
  const value = document[key];
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return { ...(value as Record<string, unknown>) };
  }
  return {};
}

function assignList(section: Record<string, unknown>, key: string, text: string) {
  const list = parseListText(text);
  if (list.length > 0) {
    section[key] = list;
    return;
  }
  delete section[key];
}

function pruneEmptySections(document: RuntimeProfileDocument) {
  for (const key of ["profile", "tools", "skills", "mcp", "prompts", "runtime"]) {
    const value = document[key];
    if (
      value &&
      typeof value === "object" &&
      !Array.isArray(value) &&
      Object.keys(value as Record<string, unknown>).length === 0
    ) {
      delete document[key];
    }
  }
}

export type ProfileDocumentResult =
  | { ok: true; document: RuntimeProfileDocument }
  | { ok: false; issues: ProfileDraftIssue[] };

/**
 * 生成写回文档：从 `base`（view.document，原始 profile.yaml）出发，只覆盖被
 * 编辑的组；未知字段与未编辑组（providers/agents 等）原样保留。
 */
export function buildProfileDocument(
  draft: ProfileDraft,
  base: RuntimeProfileDocument = {},
  options: ProfileDraftValidationOptions = {},
): ProfileDocumentResult {
  const issues = validateProfileDraft(draft, options);
  if (issues.length > 0) {
    return { ok: false, issues };
  }

  const document = cloneDocument(base);

  const profile = sectionOf(document, "profile");
  profile.name = draft.name.trim();
  const description = draft.description.trim();
  if (description) {
    profile.description = description;
  } else {
    delete profile.description;
  }
  const defaultAgent = draft.defaultAgent.trim();
  if (defaultAgent) {
    profile.default_agent = defaultAgent;
  } else {
    delete profile.default_agent;
  }
  document.profile = profile;

  const tools = sectionOf(document, "tools");
  assignList(tools, "allowlist", draft.toolsAllowlist);
  assignList(tools, "denylist", draft.toolsDenylist);
  document.tools = tools;

  const skills = sectionOf(document, "skills");
  assignList(skills, "allowlist", draft.skillsAllowlist);
  assignList(skills, "denylist", draft.skillsDenylist);
  document.skills = skills;

  const mcp = sectionOf(document, "mcp");
  assignList(mcp, "use_servers", draft.mcpUseServers);
  assignList(mcp, "exclude_servers", draft.mcpExcludeServers);
  document.mcp = mcp;

  const prompts = sectionOf(document, "prompts");
  prompts.mode = draft.promptMode;
  document.prompts = prompts;

  const runtime = sectionOf(document, "runtime");
  const overrides: Record<string, unknown> = {};
  for (const override of draft.overrides) {
    const key = override.key.trim();
    if (key) {
      setNestedOverride(overrides, key, parseOverrideValue(override.value));
    }
  }
  if (Object.keys(overrides).length > 0) {
    runtime.overrides = overrides;
  } else {
    delete runtime.overrides;
  }
  document.runtime = runtime;

  pruneEmptySections(document);
  return { ok: true, document };
}

function listDiff(path: string, next: string, current: string[], changes: string[]) {
  const nextList = parseListText(next);
  if (nextList.join("\n") !== current.join("\n")) {
    changes.push(path);
  }
}

function textDiff(path: string, next: string, current: string, changes: string[]) {
  if (next.trim() !== current.trim()) {
    changes.push(path);
  }
}

/** 变更路径列表（与后端 issues.path 同名），用于保存前的影响面提示。 */
export function diffProfileDraft(draft: ProfileDraft, view: RuntimeProfileView): string[] {
  const changes: string[] = [];
  textDiff("profile.name", draft.name, view.name, changes);
  textDiff("profile.description", draft.description, view.description, changes);
  textDiff("profile.default_agent", draft.defaultAgent, view.agents.defaultAgent, changes);
  listDiff("tools.allowlist", draft.toolsAllowlist, view.tools.allowlist, changes);
  listDiff("tools.denylist", draft.toolsDenylist, view.tools.denylist, changes);
  listDiff("skills.allowlist", draft.skillsAllowlist, view.skills.allowlist, changes);
  listDiff("skills.denylist", draft.skillsDenylist, view.skills.denylist, changes);
  listDiff("mcp.use_servers", draft.mcpUseServers, view.mcp.useServers, changes);
  listDiff("mcp.exclude_servers", draft.mcpExcludeServers, view.mcp.excludeServers, changes);
  textDiff("prompts.mode", draft.promptMode, view.prompts.mode, changes);

  const currentOverrides = new Map(
    view.overrides.entries.map((entry) => [entry.key, entry.value]),
  );
  for (const override of draft.overrides) {
    const key = override.key.trim();
    if (!key) {
      continue;
    }
    const value = override.value.trim();
    if (currentOverrides.get(key) !== value) {
      changes.push(`runtime.overrides.${key}`);
    }
    currentOverrides.delete(key);
  }
  for (const removed of currentOverrides.keys()) {
    changes.push(`runtime.overrides.${removed}`);
  }

  return changes;
}

export function hasProfileDraftChanges(draft: ProfileDraft, view: RuntimeProfileView) {
  return diffProfileDraft(draft, view).length > 0;
}
