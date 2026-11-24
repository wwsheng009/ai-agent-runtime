export type RuntimeTeamSummaryState =
  | "pending"
  | "ready"
  | "failed"
  | "not_applicable"
  | "unknown";

export type RuntimeTeamCapabilityState = "supported" | "unsupported" | "unknown";

export type RuntimeTeamDefinition = {
  id: string;
  owner_id: string;
  workspace_id: string;
  name: string;
  description?: string;
  version: number;
  config: Record<string, unknown>;
  archived_at?: string | null;
  created_at: string;
  updated_at: string;
};

export type RuntimeTeamRun = {
  id: string;
  owner_id: string;
  workspace_id: string;
  definition_id?: string;
  definition_version?: number;
  legacy_team_id?: string;
  goal: string;
  status: string;
  phase: string;
  compatibility_mode: string;
  snapshot_schema_version: number;
  config_snapshot: Record<string, unknown>;
  snapshot_checksum: string;
  version: number;
  event_seq: number;
  summary_state: RuntimeTeamSummaryState | string;
  request_id?: string;
  created_at: string;
  started_at?: string | null;
  terminal_at?: string | null;
};

export type RuntimeTeamProductCapabilities = Record<
  string,
  RuntimeTeamCapabilityState | string
>;

export type RuntimeTeamDefinitionResponse = {
  definition: RuntimeTeamDefinition;
  capabilities?: RuntimeTeamProductCapabilities;
  request_id?: string;
};

export type RuntimeTeamDefinitionsResponse = {
  definitions: RuntimeTeamDefinition[];
  count: number;
  limit?: number;
  capabilities?: RuntimeTeamProductCapabilities;
  request_id?: string;
};

export type RuntimeTeamRunResponse = {
  run: RuntimeTeamRun;
  capabilities?: RuntimeTeamProductCapabilities;
  request_id?: string;
};

export type RuntimeTeamRunsResponse = {
  runs: RuntimeTeamRun[];
  count: number;
  limit?: number;
  capabilities?: RuntimeTeamProductCapabilities;
  request_id?: string;
};

export type RuntimeTeamRunPreflightFinding = {
  severity: "blocker" | "warning" | "info" | string;
  code: string;
  message: string;
  remediation?: string;
  team_id?: string;
  teammate_id?: string;
  related_teammate_id?: string;
  task_id?: string;
  related_task_id?: string;
  depends_on_id?: string;
  path?: string;
  related_path?: string;
  existing_owner?: string;
  existing_mode?: string;
};

export type RuntimeTeamRunPreflightReport = {
  run_id: string;
  legacy_team_id?: string;
  version: number;
  status: string;
  phase: string;
  snapshot_schema_version: number;
  ready: boolean;
  blocker_count: number;
  warning_count: number;
  info_count: number;
  findings?: RuntimeTeamRunPreflightFinding[];
  blockers?: RuntimeTeamRunPreflightFinding[];
  warnings?: RuntimeTeamRunPreflightFinding[];
  infos?: RuntimeTeamRunPreflightFinding[];
  route_preview_capability: string;
};

export type RuntimeTeamRunPreflightResponse = {
  run: RuntimeTeamRun;
  run_id: string;
  preflight: RuntimeTeamRunPreflightReport;
  readiness: boolean;
  capabilities?: RuntimeTeamProductCapabilities;
  request_id?: string;
};

export type RuntimeCreateTeamDefinitionRequest = {
  name: string;
  description?: string;
  config: Record<string, unknown>;
};

export type RuntimeUpdateTeamDefinitionRequest = RuntimeCreateTeamDefinitionRequest & {
  expected_version: number;
};

export type RuntimeCreateTeamRunDraftRequest = {
  definition_id?: string;
  expected_definition_version?: number;
  goal: string;
  config?: Record<string, unknown>;
};

export type RuntimeTeamProductListQuery = {
  limit?: number;
  signal?: AbortSignal;
};

export type RuntimeCreateTeamRunDraftOptions = {
  idempotencyKey: string;
  signal?: AbortSignal;
};

export type RuntimeStartTeamRunRequest = {
  expected_version: number;
};

export type RuntimeStartTeamRunOptions = {
  idempotencyKey: string;
  signal?: AbortSignal;
};
