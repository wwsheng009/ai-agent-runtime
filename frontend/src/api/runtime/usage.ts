// P2-1A：运行时用量 / 配额 REST 客户端。
//
// 端点（backend/internal/api/skills/handler.go:677-682；实现 :11316-11720）：
//   GET /api/runtime/usage/stats?tenant_id=&project_id=&user_id=
//       → { tracking_enabled, policy, usage, [scope, quota] | [scopes] }
//   GET /api/runtime/usage/ledger?tenant_id=&project_id=&user_id=&entrypoint=&skill=&success=&since=&limit=
//       → { records: TokenUsageHistory[], count, filters }
//   GET /api/runtime/usage/ledger?...&group_by=profile
//       → 追加 { group_by, groups: [{profile, requests, failures, input_tokens, output_tokens, total_tokens}], grouped_total }
//   GET /api/runtime/usage/policy → { policy: UsagePolicyDetails }
//
// 三个端点均受 `authorizeUsageAdmin` 限制（Bearer admin token），前端沿用
// `lib/admin-token` 的存储键，仅负责透传。
//
// 归一化说明：键名固定 snake_case（后端直接构造 map，无 struct tag 歧义）；
// 结构不满足（缺 usage/policy/records 等核心字段）时返回 null，由调用方抛错，
// 绝不伪造「正常空态」。scoped 响应缺 quota 是显式降级（UI 另有不可用提示），
// 不计入结构失败——它不影响 token 用量的真实呈现。分组（`group_by=profile`）
// 是可选维度：响应缺 `groups` 数组时 `profileGroups` 置 null，不让可选维度的
// 缺失去否决 records 主契约；未请求分组时响应键与旧版逐字节一致。

import type {
  UsageLedgerGroup,
  UsageLedgerRecord,
  UsageLedgerView,
  UsageMetrics,
  UsagePolicyDetails,
  UsagePolicySummary,
  UsageQuotaEntry,
  UsageQuotaLevel,
  UsageQuotaState,
  UsageScope,
  UsageStatsView,
} from "@/types/runtime";

import { buildRuntimeUrlWithQuery, fetchRuntimeJson } from "./shared";

type RawRecord = Record<string, unknown>;

export const DEFAULT_USAGE_LEDGER_LIMIT = 50;

export type UsageScopeQuery = {
  tenantId?: string;
  projectId?: string;
  userId?: string;
};

export type UsageLedgerQuery = UsageScopeQuery & {
  entrypoint?: string;
  skill?: string;
  success?: boolean;
  /** RFC3339；后端按时间过滤（`time.Parse(time.RFC3339, ...)`）。 */
  since?: string;
  limit?: number;
  /**
   * 按维度聚合账本（后端白名单当前只有 `"profile"`，FR-13）。
   * 显式传参才发 `group_by`；未传时响应与旧版逐字节一致（records/count/filters）。
   */
  groupBy?: "profile";
};

type UsageRequestOptions = {
  adminToken?: string;
};

function asRecord(value: unknown): RawRecord | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as RawRecord)
    : null;
}

function pickString(record: RawRecord, key: string): string {
  const value = record[key];
  return typeof value === "string" ? value.trim() : "";
}

function pickNumber(record: RawRecord, key: string, fallback = 0): number {
  const value = record[key];
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

function pickBoolean(record: RawRecord, key: string, fallback = false): boolean {
  const value = record[key];
  return typeof value === "boolean" ? value : fallback;
}

/** 配额上限：非有限数 / 缺失 → null（未配置），保留 0 与负数语义给展示层判定。 */
function pickLimit(record: RawRecord, key: string): number | null {
  const value = record[key];
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return null;
  }
  return value;
}

function normalizeUsageScope(raw: unknown): UsageScope | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const scopeKey = pickString(record, "scope_key");
  if (!scopeKey) {
    return null;
  }
  return {
    tenant_id: pickString(record, "tenant_id"),
    project_id: pickString(record, "project_id"),
    user_id: pickString(record, "user_id"),
    scope_key: scopeKey,
  };
}

function normalizeUsageQuota(raw: unknown): UsageQuotaState | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const scopeKey = pickString(record, "scope_key");
  if (!scopeKey) {
    return null;
  }
  return {
    scope_key: scopeKey,
    enabled: pickBoolean(record, "enabled"),
    max_requests: pickNumber(record, "max_requests", -1),
    max_tokens: pickNumber(record, "max_tokens", -1),
    remaining_requests: pickNumber(record, "remaining_requests", -1),
    remaining_tokens: pickNumber(record, "remaining_tokens", -1),
    resolved_from: pickString(record, "resolved_from"),
  };
}

export function normalizeUsageMetrics(raw: unknown): UsageMetrics | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  return {
    scope_count: pickNumber(record, "scope_count"),
    user_count: pickNumber(record, "user_count"),
    request_count: pickNumber(record, "request_count"),
    execute_count: pickNumber(record, "execute_count"),
    agent_chat_count: pickNumber(record, "agent_chat_count"),
    success_count: pickNumber(record, "success_count"),
    failure_count: pickNumber(record, "failure_count"),
    prompt_tokens: pickNumber(record, "prompt_tokens"),
    completion_tokens: pickNumber(record, "completion_tokens"),
    total_tokens: pickNumber(record, "total_tokens"),
    last_skill: pickString(record, "last_skill"),
    last_entrypoint: pickString(record, "last_entrypoint"),
    last_request_at: pickString(record, "last_request_at"),
  };
}

function normalizeUsagePolicySummary(raw: unknown): UsagePolicySummary | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  return {
    tracking_enabled: pickBoolean(record, "tracking_enabled"),
    ledger_enabled: pickBoolean(record, "ledger_enabled"),
    quota_enabled: pickBoolean(record, "quota_enabled"),
    default_max_requests: pickNumber(record, "default_max_requests"),
    default_max_tokens: pickNumber(record, "default_max_tokens"),
    tenant_quota_count: pickNumber(record, "tenant_quota_count"),
    project_quota_count: pickNumber(record, "project_quota_count"),
    user_quota_count: pickNumber(record, "user_quota_count"),
  };
}

const usageQuotaLevels: readonly UsageQuotaLevel[] = ["tenant", "project", "user"];

function normalizeUsageQuotaEntries(raw: unknown, level: UsageQuotaLevel): UsageQuotaEntry[] {
  const record = asRecord(raw);
  if (!record) {
    return [];
  }
  return Object.entries(record)
    .map(([key, value]) => {
      const limit = asRecord(value);
      if (!key.trim() || !limit) {
        return null;
      }
      return {
        level,
        key: key.trim(),
        max_requests: pickLimit(limit, "max_requests"),
        max_tokens: pickLimit(limit, "max_tokens"),
      } satisfies UsageQuotaEntry;
    })
    .filter((entry): entry is UsageQuotaEntry => entry !== null)
    .sort((left, right) => left.key.localeCompare(right.key));
}

/** `GET /usage/policy` → 完整策略视图；缺 `policy` 视为契约不满足（返回 null）。 */
export function normalizeUsagePolicy(raw: unknown): UsagePolicyDetails | null {
  const record = asRecord(raw);
  const policy = asRecord(record?.policy);
  if (!policy) {
    return null;
  }
  return {
    tracking_enabled: pickBoolean(policy, "tracking_enabled"),
    ledger_enabled: pickBoolean(policy, "ledger_enabled"),
    quota_enabled: pickBoolean(policy, "quota_enabled"),
    default_max_requests: pickNumber(policy, "default_max_requests"),
    default_max_tokens: pickNumber(policy, "default_max_tokens"),
    tenants: normalizeUsageQuotaEntries(policy.tenants, usageQuotaLevels[0]),
    projects: normalizeUsageQuotaEntries(policy.projects, usageQuotaLevels[1]),
    users: normalizeUsageQuotaEntries(policy.users, usageQuotaLevels[2]),
  };
}

/** `GET /usage/stats` → 归一化视图；缺 `usage`/`policy` 或 scope/quota 形状非法时返回 null。 */
export function normalizeUsageStats(raw: unknown): UsageStatsView | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const usage = normalizeUsageMetrics(record.usage);
  const policy = normalizeUsagePolicySummary(record.policy);
  if (!usage || !policy) {
    return null;
  }

  let scope: UsageScope | null = null;
  if (record.scope !== undefined && record.scope !== null) {
    scope = normalizeUsageScope(record.scope);
    if (!scope) {
      return null;
    }
  }

  let quota: UsageQuotaState | null = null;
  if (record.quota !== undefined && record.quota !== null) {
    quota = normalizeUsageQuota(record.quota);
    if (!quota) {
      return null;
    }
  }

  const scopesRaw = record.scopes;
  if (scopesRaw !== undefined && scopesRaw !== null && !Array.isArray(scopesRaw)) {
    return null;
  }
  const scopes = Array.isArray(scopesRaw)
    ? scopesRaw
        .map((item) => normalizeUsageScope(item))
        .filter((item): item is UsageScope => item !== null)
    : [];

  return {
    tracking_enabled: pickBoolean(record, "tracking_enabled"),
    policy,
    scope,
    quota,
    usage,
    scopes,
  };
}

function normalizeUsageLedgerRecord(raw: unknown): UsageLedgerRecord | null {
  const record = asRecord(raw);
  if (!record) {
    return null;
  }
  const id = pickString(record, "id");
  if (!id) {
    return null;
  }
  return {
    id,
    request_id: pickString(record, "request_id"),
    model_id: pickString(record, "model_id"),
    provider_id: pickString(record, "provider_id"),
    input_tokens: pickNumber(record, "input_tokens"),
    output_tokens: pickNumber(record, "output_tokens"),
    total_tokens: pickNumber(record, "total_tokens"),
    message_count: pickNumber(record, "message_count"),
    max_tokens: pickNumber(record, "max_tokens"),
    success: pickBoolean(record, "success"),
    status_code: pickNumber(record, "status_code"),
    metadata: asRecord(record.metadata) ?? {},
    created_at: pickString(record, "created_at"),
  };
}

/**
 * 分组单条：`profile` 键必须存在且为字符串（空串是合法的「未归属」组身份，
 * 不能像 record 缺 id 那样丢弃），其余计数键缺失按 0 处理（后端恒回传）。
 */
function normalizeUsageLedgerGroup(raw: unknown): UsageLedgerGroup | null {
  const record = asRecord(raw);
  if (!record || typeof record.profile !== "string") {
    return null;
  }
  return {
    profile: record.profile.trim(),
    requests: pickNumber(record, "requests"),
    failures: pickNumber(record, "failures"),
    input_tokens: pickNumber(record, "input_tokens"),
    output_tokens: pickNumber(record, "output_tokens"),
    total_tokens: pickNumber(record, "total_tokens"),
  };
}

/**
 * `GET /usage/ledger` → 归一化视图；缺 `records` 数组视为契约不满足（返回 null）。
 * 单条记录缺 `id` 时丢弃该行（无法稳定作 key），不臆造 id。
 */
export function normalizeUsageLedger(
  raw: unknown,
  fallbackLimit: number = DEFAULT_USAGE_LEDGER_LIMIT,
): UsageLedgerView | null {
  const record = asRecord(raw);
  if (!record || !Array.isArray(record.records)) {
    return null;
  }
  const records = record.records
    .map((item) => normalizeUsageLedgerRecord(item))
    .filter((item): item is UsageLedgerRecord => item !== null);
  const filters = asRecord(record.filters);
  const echoedLimit = filters ? pickNumber(filters, "limit", fallbackLimit) : fallbackLimit;
  // 分组是可选维度：响应缺 `groups` 数组（未请求分组 / 旧后端忽略 group_by）时
  // 如实置 null，由 UI 区分「未返回分组」与「真实空分组」。
  const profileGroups = Array.isArray(record.groups)
    ? record.groups
        .map((item) => normalizeUsageLedgerGroup(item))
        .filter((item): item is UsageLedgerGroup => item !== null)
    : null;
  const rawGroupedTotal = record.grouped_total;
  const groupedTotal =
    profileGroups !== null &&
    typeof rawGroupedTotal === "number" &&
    Number.isFinite(rawGroupedTotal) &&
    rawGroupedTotal >= 0
      ? rawGroupedTotal
      : null;
  return {
    records,
    count: records.length,
    limit: echoedLimit > 0 ? echoedLimit : fallbackLimit,
    profileGroups,
    groupedTotal,
  };
}

function buildUsageHeaders(adminToken?: string) {
  const token = adminToken?.trim();
  if (!token) {
    return {} as Record<string, string>;
  }
  return {
    Authorization: `Bearer ${token}`,
  } satisfies Record<string, string>;
}

/** 读取用量统计（不传 scope 参数即全局聚合 + scope 列表）。 */
export async function getUsageStats(
  options: UsageScopeQuery & UsageRequestOptions = {},
): Promise<UsageStatsView> {
  const raw = await fetchRuntimeJson<unknown>(
    buildRuntimeUrlWithQuery("/api/runtime/usage/stats", {
      tenant_id: options.tenantId,
      project_id: options.projectId,
      user_id: options.userId,
    }),
    {
      headers: buildUsageHeaders(options.adminToken),
    },
  );
  const view = normalizeUsageStats(raw);
  if (!view) {
    throw new Error("invalid usage stats payload");
  }
  return view;
}

/** 读取持久化 token 账本（后端未配置账本时返回 503，由调用方展示错误）。 */
export async function getUsageLedger(
  options: UsageLedgerQuery & UsageRequestOptions = {},
): Promise<UsageLedgerView> {
  const limit = options.limit ?? DEFAULT_USAGE_LEDGER_LIMIT;
  const raw = await fetchRuntimeJson<unknown>(
    buildRuntimeUrlWithQuery("/api/runtime/usage/ledger", {
      tenant_id: options.tenantId,
      project_id: options.projectId,
      user_id: options.userId,
      entrypoint: options.entrypoint,
      skill: options.skill,
      success: options.success === undefined ? undefined : String(options.success),
      since: options.since,
      limit,
      group_by: options.groupBy,
    }),
    {
      headers: buildUsageHeaders(options.adminToken),
    },
  );
  const view = normalizeUsageLedger(raw, limit);
  if (!view) {
    throw new Error("invalid usage ledger payload");
  }
  return view;
}

/** 读取用量 / 配额策略明细。 */
export async function getUsagePolicy(
  options: UsageRequestOptions = {},
): Promise<UsagePolicyDetails> {
  const raw = await fetchRuntimeJson<unknown>(
    buildRuntimeUrlWithQuery("/api/runtime/usage/policy", {}),
    {
      headers: buildUsageHeaders(options.adminToken),
    },
  );
  const policy = normalizeUsagePolicy(raw);
  if (!policy) {
    throw new Error("invalid usage policy payload");
  }
  return policy;
}
