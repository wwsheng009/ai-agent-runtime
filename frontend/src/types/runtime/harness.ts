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
