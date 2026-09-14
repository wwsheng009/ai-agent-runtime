// P2-1A：运行时用量与配额类型（`GET /api/runtime/usage/{stats,ledger,policy}`）。
//
// 口径：后端账本（`entity.TokenUsageHistory`）只有 token/请求计数，没有货币价格字段；
// 前端因此只呈现 token 口径，不展示也不推算任何货币成本。
//
// 键名：这些端点由后端 `handler.go` 直接构造 `map[string]interface{}`，键名固定
// snake_case（非 Go struct 序列化），故归一化层只认 snake_case。

/** 单层配额上限；`null` 表示该维度未配置上限。 */
export type UsageQuotaLimit = {
  max_requests: number | null;
  max_tokens: number | null;
};

export type UsageQuotaLevel = "tenant" | "project" | "user";

/** 策略里的单条配额（层级 + 键 + 上限）。 */
export type UsageQuotaEntry = UsageQuotaLimit & {
  level: UsageQuotaLevel;
  key: string;
};

/** `GET /usage/stats` 返回的紧凑策略视图（只有计数，无明细）。 */
export type UsagePolicySummary = {
  tracking_enabled: boolean;
  ledger_enabled: boolean;
  quota_enabled: boolean;
  default_max_requests: number;
  default_max_tokens: number;
  tenant_quota_count: number;
  project_quota_count: number;
  user_quota_count: number;
};

/** `GET /usage/policy` 返回的完整策略视图（含各层配额明细）。 */
export type UsagePolicyDetails = {
  tracking_enabled: boolean;
  ledger_enabled: boolean;
  quota_enabled: boolean;
  default_max_requests: number;
  default_max_tokens: number;
  tenants: UsageQuotaEntry[];
  projects: UsageQuotaEntry[];
  users: UsageQuotaEntry[];
};

/** 用量作用域（tenant/project/user 三者归一化后的键）。 */
export type UsageScope = {
  tenant_id: string;
  project_id: string;
  user_id: string;
  scope_key: string;
};

/**
 * 计数主体：scoped 响应来自 `usageTracker.snapshot`，全局响应来自 `aggregate`。
 * 两者共有 request/execute/agent_chat/success/failure 与三类 token 计数；
 * `scope_count`/`user_count` 仅全局聚合提供，`last_*` 仅 scoped 快照提供。
 */
export type UsageMetrics = {
  scope_count: number;
  user_count: number;
  request_count: number;
  execute_count: number;
  agent_chat_count: number;
  success_count: number;
  failure_count: number;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  last_skill: string;
  last_entrypoint: string;
  last_request_at: string;
};

/** 指定 scope 时的配额余量（后端 `usageQuotaSnapshot`）；-1 表示不限额。 */
export type UsageQuotaState = {
  scope_key: string;
  enabled: boolean;
  max_requests: number;
  max_tokens: number;
  remaining_requests: number;
  remaining_tokens: number;
  resolved_from: string;
};

/** 归一化后的 `GET /usage/stats`。`scope === null` 即全局视图（带 `scopes` 列表）。 */
export type UsageStatsView = {
  tracking_enabled: boolean;
  policy: UsagePolicySummary;
  scope: UsageScope | null;
  quota: UsageQuotaState | null;
  usage: UsageMetrics;
  scopes: UsageScope[];
};

/** 账本单条记录（`entity.TokenUsageHistory`）。 */
export type UsageLedgerRecord = {
  id: string;
  request_id: string;
  model_id: string;
  provider_id: string;
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  message_count: number;
  max_tokens: number;
  success: boolean;
  status_code: number;
  /** 写入侧元数据：subsystem/tenant_id/project_id/user_id/scope_key/entrypoint/skill/resolved_from。 */
  metadata: Record<string, unknown>;
  created_at: string;
};

/** 归一化后的 `GET /usage/ledger`。 */
export type UsageLedgerView = {
  records: UsageLedgerRecord[];
  count: number;
  limit: number;
};
