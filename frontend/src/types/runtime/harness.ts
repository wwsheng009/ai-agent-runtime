export type RuntimeHarnessPermissionRule = {
  name?: string;
  tools?: string[];
  capabilities?: string[];
  decision: string;
  reason?: string;
};

export type RuntimeHarnessPermissionsResponse = {
  workspace_path: string;
  source_path?: string;
  exists: boolean;
  version?: number;
  deny_tools?: string[];
  allow_tools?: string[];
  rules?: RuntimeHarnessPermissionRule[];
};

export type RuntimeHarnessGrant = {
  tool: string;
  pattern?: string;
  scope?: string;
};

/**
 * D29 工作区信任结论（Batch 14，Q22 UI 闭环）。
 *
 * `feature_enabled=false` 时门控关闭、`trusted` 恒为 true；只有
 * `feature_enabled=true && trusted=false` 才意味着项目级 profile 的 prompts
 * 正在被扣留（清单里对应条目 prompt_suppressed=true）。
 */
export type RuntimeHarnessTrustResponse = {
  workspace_path: string;
  feature_enabled: boolean;
  trusted: boolean;
  source?: string;
  workspace_key?: string;
  project_root?: string;
  store_path?: string;
  project_configs?: string[];
  action?: string;
};

/** 只支持 grant：撤销信任是破坏性操作，不在 UI 闭环内（走 CLI `/trust`）。 */
export type RuntimeHarnessTrustAction = "grant";

export type RuntimeHarnessTrustRequest = {
  workspace_path?: string;
  action?: RuntimeHarnessTrustAction;
};

export type RuntimeHarnessGrantsResponse = {
  workspace_path: string;
  store_path?: string;
  grants: RuntimeHarnessGrant[];
  count: number;
  action?: string;
  removed?: number;
};

export type RuntimeHarnessGrantsAction = "remember" | "revoke";

export type RuntimeHarnessGrantsUpdateRequest = {
  workspace_path?: string;
  action: RuntimeHarnessGrantsAction | "add" | "grant" | "remove" | "delete";
  tool: string;
  pattern?: string;
  scope?: string;
  match_empty_pattern?: boolean;
};

export type RuntimeHarnessMemoryNote = {
  id: string;
  text: string;
  tags?: string[];
  source?: string;
  session_id?: string;
  created_at?: string;
  score?: number;
};

export type RuntimeHarnessMemoryResponse = {
  workspace_path: string;
  root?: string;
  path?: string;
  query?: string;
  notes?: RuntimeHarnessMemoryNote[];
  hits?: RuntimeHarnessMemoryNote[];
  count: number;
  note?: RuntimeHarnessMemoryNote;
  action?: string;
};

export type RuntimeHarnessMemoryAppendRequest = {
  workspace_path?: string;
  text: string;
  tags?: string[];
  source?: string;
  session_id?: string;
};

export type RuntimeMemoryStatus = {
  workspace_path: string;
  mode: string;
  scope: string;
  db_path?: string;
  schema_version: number;
  total: number;
  by_status?: Record<string, number>;
  by_freshness?: Record<string, number>;
  summary_revision?: number;
  summary_status?: string;
  summary_freshness?: string;
  pinned_count?: number;
  pinned_budget_ratio?: number;
  injection_mode?: string;
  jobs?: Record<string, number>;
  fallback: boolean;
  fallback_reason?: string;
};

export type RuntimeMemoryRecord = {
  id: string;
  scope: string;
  kind: string;
  status: string;
  text: string;
  tags?: string[];
  confidence?: number;
  sensitivity?: string;
  pinned: boolean;
  retention_class?: string;
  source_kind: string;
  source_session_id?: string;
  source_turn_id?: string;
  source_refs?: string[];
  revision: number;
  created_at?: string;
  updated_at?: string;
  last_used_at?: string;
  use_count?: number;
  valid_until?: string;
  freshness_status: string;
  last_verified_at?: string;
  revalidate_after?: string;
  stale_reason?: string;
  usage?: RuntimeMemoryUsage[];
};

export type RuntimeMemoryRecordsResponse = {
  workspace_path: string;
  items: RuntimeMemoryRecord[];
  count: number;
  next_cursor?: string;
};

export type RuntimeMemorySummary = {
  id: string;
  scope: string;
  project_key: string;
  summary_kind: string;
  revision: number;
  status: string;
  freshness_status: string;
  text: string;
  token_estimate: number;
  source_count: number;
  input_watermark?: string;
  generated_at: string;
  model?: string;
  content_hash?: string;
  record_ids?: string[];
};

export type RuntimeMemorySummariesResponse = {
  workspace_path: string;
  items: RuntimeMemorySummary[];
  count: number;
};

export type RuntimeMemoryUsage = {
  id: string;
  memory_id: string;
  session_id: string;
  turn_id?: string;
  message_id?: string;
  used_at: string;
  use_kind: "cited" | "read" | "injected_only";
};

export type RuntimeMemoryUsageResponse = {
  workspace_path: string;
  items: RuntimeMemoryUsage[];
  count: number;
};

export type RuntimeMemoryJob = {
  id: string;
  kind: string;
  job_key: string;
  scope: string;
  project_key: string;
  status: string;
  priority: number;
  worker_id?: string;
  lease_until?: string;
  attempt: number;
  max_attempts: number;
  retry_at?: string;
  started_at?: string;
  finished_at?: string;
  last_error?: string;
  input_watermark?: string;
  last_success_watermark?: string;
  created_at: string;
  updated_at: string;
};

export type RuntimeMemoryJobsResponse = {
  items: RuntimeMemoryJob[];
  count: number;
};

export type RuntimeMemoryRecallTrace = {
  id: string;
  session_id: string;
  turn_id?: string;
  project_key: string;
  query_hash: string;
  query_preview?: string;
  summary_revision?: number;
  candidate_count: number;
  filtered_count: number;
  selected_ids?: string[];
  dropped?: { id: string; reason: string }[];
  token_estimate: number;
  token_budget: number;
  budget_source?: string;
  injected: boolean;
  created_at?: string;
};

export type RuntimeMemoryRecallTracesResponse = {
  items: RuntimeMemoryRecallTrace[];
  count: number;
};

export type RuntimeHarnessPlugin = {
  id: string;
  name: string;
  version?: string;
  description?: string;
  author?: string;
  root?: string;
  trust: string;
  enabled: boolean;
  active: boolean;
  warnings?: string[];
};

export type RuntimeHarnessPluginsResponse = {
  workspace_path: string;
  state_path?: string;
  plugins: RuntimeHarnessPlugin[];
  count: number;
  plugin?: RuntimeHarnessPlugin;
  action?: string;
};

export type RuntimeHarnessPluginAction =
  | "trust"
  | "untrust"
  | "enable"
  | "disable";

export type RuntimeHarnessPluginUpdateRequest = {
  workspace_path?: string;
  action?: RuntimeHarnessPluginAction;
  trust?: string;
  enabled?: boolean;
};

export type RuntimeHarnessHook = {
  id: string;
  event: string;
  handler_type: string;
  execution_mode: string;
  matcher?: string;
  command?: string;
  url?: string;
  timeout_seconds?: number;
  status_message?: string;
  source?: string;
  source_path?: string;
  enabled: boolean;
};

export type RuntimeHarnessHooksResponse = {
  workspace_path: string;
  hooks: RuntimeHarnessHook[];
  sources?: string[];
  warnings?: string[];
  recent_runs?: RuntimeHarnessHookRun[];
  count: number;
};

export type RuntimeHarnessHookRun = {
  hook_id: string;
  event: string;
  phase: string;
  status_message?: string;
  source?: string;
  at: string;
};

export type RuntimeHarnessHookUpdateRequest = {
  workspace_path?: string;
  id: string;
  source_path: string;
  enabled: boolean;
};
