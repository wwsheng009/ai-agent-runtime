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
