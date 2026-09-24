// Runtime profile API —— 归一化层（Batch 8 §10.5 契约）。
//
// 纪律：契约字段缺失/形状不对时抛错，不降级成「空列表」或「默认值」，
// 否则后端契约回归会被 UI 伪装成「尚未配置 profile」，问题无法被发现。
// 本模块不发请求，只把后端 payload 折叠成前端类型；入口见 queries.ts / mutations.ts。
//
// 导出范围：normalizeProfileView / readProfileDeleteBlockingReferences 是对外公共面；
// 其余 asRecord/read* 小工具仅供同目录 queries.ts / mutations.ts 复用，
// 由 index.ts 显式白名单再导出，避免内部工具泄漏给调用方。

import type {
  RuntimeProfileCreateRequest,
  RuntimeProfileCreateResponse,
  RuntimeProfileListEntry,
  RuntimeProfileListResponse,
  RuntimeProfileReferencesResponse,
  RuntimeProfileValidationIssue,
  RuntimeProfileView,
  RuntimeProfileWriteRequest,
} from "@/types/runtime";

import { RuntimeApiError } from "../shared";
export function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

export function readString(value: unknown) {
  return typeof value === "string" ? value.trim() : "";
}

/** 同时接受 snake_case / camelCase 两种后端字段名。 */
export function readAliasedString(source: Record<string, unknown>, camel: string) {
  return (
    readString(source[camel]) ||
    readString(source[camel.replace(/[A-Z]/g, (letter) => `_${letter.toLowerCase()}`)])
  );
}

export function readNumber(value: unknown, fallback = 0) {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

export function readBoolean(value: unknown) {
  return value === true;
}

export function readStringArray(value: unknown) {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((item) => readString(item))
    .filter((item) => item.length > 0);
}

export function readStringMap(value: unknown) {
  const record = asRecord(value);
  if (!record) {
    return {} as Record<string, string>;
  }
  const result: Record<string, string> = {};
  for (const [key, raw] of Object.entries(record)) {
    const text = readString(raw);
    if (key && text) {
      result[key] = text;
    }
  }
  return result;
}

export function normalizePromptLayer(value: unknown) {
  const record = asRecord(value);
  return {
    path: record ? readAliasedString(record, "path") : "",
    exists: record ? record.exists === true : false,
  };
}

/** 列表条目归一化：ref 是后续所有端点的句柄，缺失即契约违约，直接抛错。 */
export function normalizeProfileListEntry(value: unknown, index: number): RuntimeProfileListEntry {
  const record = asRecord(value);
  const ref = record ? readAliasedString(record, "ref") : "";
  if (!record || !ref) {
    throw new Error(
      `invalid runtime profiles payload: profiles[${index}].ref must be a non-empty string`,
    );
  }
  return {
    ref,
    name: readAliasedString(record, "name") || ref,
    description: readAliasedString(record, "description"),
    layer: readAliasedString(record, "layer"),
    path: readAliasedString(record, "path"),
    valid: record.valid !== false,
    error: readAliasedString(record, "error"),
    isDefault: readBoolean(record.is_default ?? record.isDefault),
    defaultAgent: readAliasedString(record, "defaultAgent"),
    writable: record.writable !== false,
  };
}

export function normalizeProfileListResponse(
  payload: RuntimeProfileListResponse | null | undefined,
): RuntimeProfileListResponse {
  const record = asRecord(payload);
  if (!record || !Array.isArray(record.profiles)) {
    throw new Error("invalid runtime profiles payload: profiles must be an array");
  }
  const profiles = record.profiles.map(normalizeProfileListEntry);
  return {
    count: readNumber(record.count, profiles.length),
    defaultProfile: readAliasedString(record, "defaultProfile"),
    defaultRoot: readAliasedString(record, "defaultRoot"),
    profiles,
  };
}

/** 单条校验问题归一化：message 缺失时保留 path 提示，避免出现空行。 */
export function normalizeIssue(value: unknown): RuntimeProfileValidationIssue {
  const record = asRecord(value);
  const path = record ? readAliasedString(record, "path") : "";
  const message = record ? readAliasedString(record, "message") : "";
  const severity = record ? readAliasedString(record, "severity") : "";
  return {
    path,
    message: message || path,
    severity: severity === "warning" ? "warning" : "error",
  };
}

export function normalizeIssueList(value: unknown) {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map(normalizeIssue)
    .filter((issue) => issue.message.length > 0 || issue.path.length > 0);
}

/**
 * resolved view 归一化：`ref` 是必填句柄，缺失即契约违约。
 * 其余分组字段缺失时给空结构——这些分组在「新建 profile」场景下本就为空，
 * 与列表条目不同，不算契约回归。
 */
export function normalizeProfileView(
  payload: RuntimeProfileView | null | undefined,
): RuntimeProfileView {
  const record = asRecord(payload);
  const ref = record ? readAliasedString(record, "ref") : "";
  if (!record || !ref) {
    throw new Error("invalid runtime profile payload: ref must be a non-empty string");
  }

  const tools = asRecord(record.tools) ?? {};
  const skills = asRecord(record.skills) ?? {};
  const mcp = asRecord(record.mcp) ?? {};
  const prompts = asRecord(record.prompts) ?? {};
  const agents = asRecord(record.agents) ?? {};
  const overrides = asRecord(record.overrides) ?? {};
  const preferences = asRecord(record.preferences) ?? {};
  const estimate = asRecord(record.estimate) ?? {};
  const overrideEntries = Array.isArray(overrides.entries) ? overrides.entries : [];

  return {
    ref,
    name: readAliasedString(record, "name") || ref,
    description: readAliasedString(record, "description"),
    source: readAliasedString(record, "source"),
    layer: readAliasedString(record, "layer"),
    path: readAliasedString(record, "path"),
    profileFile: readAliasedString(record, "profileFile"),
    mtime: readAliasedString(record, "mtime"),
    valid: record.valid !== false,
    errorCount: readNumber(record.error_count ?? record.errorCount, 0),
    warningCount: readNumber(record.warning_count ?? record.warningCount, 0),
    issues: normalizeIssueList(record.issues),
    // 写回 base：后端 GET 用 `spec` 携带原始 profile.yaml 文档。
    document: asRecord(record.spec) ?? asRecord(record.document) ?? {},
    tools: {
      allowlist: readStringArray(tools.allowlist),
      denylist: readStringArray(tools.denylist),
      agentId: readAliasedString(tools, "agentId"),
      agentAllowlist: readStringArray(tools.agent_allowlist ?? tools.agentAllowlist),
      agentDenylist: readStringArray(tools.agent_denylist ?? tools.agentDenylist),
      effective: readStringArray(tools.effective),
      excluded: readStringArray(tools.excluded),
      sources: readStringArray(tools.sources),
      ...(tools.read_only === true || tools.readOnly === true ? { readOnly: true } : {}),
    },
    skills: {
      allowlist: readStringArray(skills.allowlist),
      denylist: readStringArray(skills.denylist),
      dirs: readStringArray(skills.dirs),
      exposureMode: readAliasedString(skills, "exposureMode"),
      topK: readNumber(skills.top_k ?? skills.topK, 0),
    },
    mcp: {
      useServers: readStringArray(mcp.use_servers ?? mcp.useServers),
      excludeServers: readStringArray(mcp.exclude_servers ?? mcp.excludeServers),
      servers: (Array.isArray(mcp.servers) ? mcp.servers : [])
        .map((entry) => {
          const item = asRecord(entry) ?? {};
          return {
            name: readAliasedString(item, "name"),
            used: item.used === true,
            excluded: item.excluded === true,
          };
        })
        .filter((server) => server.name.length > 0),
      serverCount: readNumber(mcp.server_count ?? mcp.serverCount, 0),
      usedCount: readNumber(mcp.used_count ?? mcp.usedCount, 0),
    },
    prompts: {
      mode: readAliasedString(prompts, "mode"),
      system: normalizePromptLayer(prompts.system),
      role: normalizePromptLayer(prompts.role),
      tools: normalizePromptLayer(prompts.tools),
    },
    agents: {
      defaultAgent: readAliasedString(agents, "defaultAgent"),
      available: readStringArray(agents.available),
      entries: (Array.isArray(agents.entries) ? agents.entries : [])
        .map((entry) => {
          const item = asRecord(entry) ?? {};
          const entryTools = asRecord(item.tools) ?? {};
          return {
            id: readAliasedString(item, "id"),
            provider: readAliasedString(item, "provider"),
            model: readAliasedString(item, "model"),
            tools: {
              allowlist: readStringArray(entryTools.allowlist),
              denylist: readStringArray(entryTools.denylist),
            },
          };
        })
        .filter((entry) => entry.id.length > 0),
    },
    overrides: {
      keys: readStringArray(overrides.keys),
      origins: readStringMap(overrides.origins),
      count: readNumber(overrides.count, overrideEntries.length),
      entries: overrideEntries
        .map((entry) => {
          const item = asRecord(entry) ?? {};
          return {
            key: readAliasedString(item, "key"),
            value: readString(item.value),
            origin: readAliasedString(item, "origin"),
            allowed: item.allowed !== false,
          };
        })
        .filter((entry) => entry.key.length > 0),
    },
    preferences: {
      permissionMode: readAliasedString(preferences, "permissionMode"),
      permissionModeSource: readAliasedString(preferences, "permissionModeSource"),
      provider: readAliasedString(preferences, "provider"),
      model: readAliasedString(preferences, "model"),
    },
    estimate: {
      basis: readAliasedString(estimate, "basis"),
      toolCount: readNumber(estimate.tool_count ?? estimate.toolCount, 0),
      toolTokens: readNumber(estimate.tool_tokens ?? estimate.toolTokens, 0),
      promptTokens: readNumber(estimate.prompt_tokens ?? estimate.promptTokens, 0),
      totalTokens: readNumber(estimate.total_tokens ?? estimate.totalTokens, 0),
      toolAllowCount: readNumber(estimate.tool_allow_count ?? estimate.toolAllowCount, 0),
      toolDenyCount: readNumber(estimate.tool_deny_count ?? estimate.toolDenyCount, 0),
      skillAllowCount: readNumber(estimate.skill_allow_count ?? estimate.skillAllowCount, 0),
      skillDenyCount: readNumber(estimate.skill_deny_count ?? estimate.skillDenyCount, 0),
      skillDirCount: readNumber(estimate.skill_dir_count ?? estimate.skillDirCount, 0),
    },
    preview: record.preview === true,
    defaultAgent: readAliasedString(record, "defaultAgent"),
    promptMode: readAliasedString(record, "promptMode"),
    writeTarget: readAliasedString(record, "writeTarget"),
  };
}

/** 写回时同时带 document 与基线 mtime，便于后端做冲突检测（409）。 */
export function buildWriteBody(request: RuntimeProfileWriteRequest) {
  return {
    spec: request.document,
    expected_mtime: request.expectedMtime ?? "",
  };
}

/** 创建/复制的公共请求体（后端 profileCreateRequest）。 */
export function buildCreateBody(request: RuntimeProfileCreateRequest) {
  return {
    name: request.name,
    layer: request.layer ?? "",
    root: request.root ?? "",
    template: request.template ?? "",
    from_ref: request.fromRef ?? "",
    agent: request.agent ?? "",
    force: request.force === true,
    use: request.use === true,
    set_default: request.setDefault === true,
  };
}

/**
 * 创建/复制响应（POST /profiles 与 /duplicate 共用同一实现）：
 * `{created, name, root, layer, files, profile, config_path, registered,
 *   default_profile_set, affects}`；后续端点句柄取 `profile.ref`。
 */
export function normalizeCreateResponse(payload: unknown, fallbackName: string) {
  const record = asRecord(payload) ?? {};
  const profile = asRecord(record.profile) ?? {};
  const name =
    readAliasedString(record, "name") || readAliasedString(profile, "name") || fallbackName;
  const ref = readAliasedString(profile, "ref") || name;
  if (!ref) {
    throw new Error("invalid runtime profile create payload: ref must be a non-empty string");
  }
  return {
    name,
    root: readAliasedString(record, "root"),
    layer: readAliasedString(record, "layer"),
    files: readStringArray(record.files),
    ref,
    registered: record.registered === true,
    defaultProfileSet: readBoolean(record.default_profile_set ?? record.defaultProfileSet),
    affects: readAliasedString(record, "affects"),
    configPath: readAliasedString(record, "configPath"),
  } satisfies RuntimeProfileCreateResponse;
}

/** 引用面归一化（references / delete / rename / move 共用同一结构）。 */
export function normalizeReferences(value: unknown): RuntimeProfileReferencesResponse | null {
  const record = asRecord(value);
  if (!record) {
    return null;
  }
  return {
    ref: readAliasedString(record, "ref"),
    root: readAliasedString(record, "root"),
    defaultProfile: readAliasedString(record, "defaultProfile"),
    isDefault: record.is_default === true || record.isDefault === true,
    blocking: readStringArray(record.blocking),
    warnings: readStringArray(record.warnings),
    configItems: (Array.isArray(record.config_items) ? record.config_items : [])
      .map((entry) => {
        const item = asRecord(entry) ?? {};
        return {
          name: readAliasedString(item, "name"),
          root: readAliasedString(item, "root"),
          isDefault: item.is_default === true || item.isDefault === true,
        };
      })
      .filter((item) => item.name.length > 0),
    sessionScan: asRecord(record.session_scan ?? record.sessionScan) ?? {},
    agentReferences: (Array.isArray(record.agent_references) ? record.agent_references : [])
      .map((entry) => asRecord(entry) ?? {})
      .filter((entry) => Object.keys(entry).length > 0),
    agentReferenceNote: readAliasedString(record, "agentReferenceNote"),
    files: readStringArray(record.files),
    fileCount: readNumber(record.file_count ?? record.fileCount, 0),
  };
}

/**
 * 删除被引用阻断（HTTP 409）时，后端把 `references` 放进**错误体**
 * （profiles_lifecycle_handlers.go 的 DeleteRuntimeProfile）：`{deleted:false,
 * error, references}`。调用方据此回填「强制删除」对话框的阻断数，
 * 而不是把 409 当成普通红错丢掉引用信息。
 */
export function readProfileDeleteBlockingReferences(
  error: unknown,
): RuntimeProfileReferencesResponse | null {
  if (!(error instanceof RuntimeApiError) || error.status !== 409) {
    return null;
  }
  const record = asRecord(error.payload);
  return normalizeReferences(record?.references);
}
