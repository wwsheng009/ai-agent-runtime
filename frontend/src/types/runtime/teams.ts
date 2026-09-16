export type RuntimeTeamRecord = {
  id: string;
  workspace_id?: string;
  lead_session_id?: string;
  status?: string;
  strategy?: string;
  max_teammates?: number;
  max_writers?: number;
  created_at?: string;
  updated_at?: string;
};

export type RuntimeTeamsResponse = {
  teams: RuntimeTeamRecord[];
  count: number;
  limit?: number;
  team_ids?: string[];
  workspace_id?: string;
  status?: string;
};

export type RuntimeTeamSummaryCounts = Record<string, number>;

export type RuntimeTeamSummaryEntry = {
  team_id: string;
  tasks: {
    total: number;
    counts: RuntimeTeamSummaryCounts;
  };
  teammates: {
    total: number;
    counts?: RuntimeTeamSummaryCounts;
  };
  mailbox?: {
    total?: number;
    unread?: number;
  };
  path_claims?: {
    total?: number;
    active?: number;
  };
};

export type RuntimeTeamSummariesResponse = {
  teams: RuntimeTeamSummaryEntry[];
  count: number;
  as_of?: string;
  team_ids?: string[];
  include_mailbox?: boolean;
  include_teammate_states?: boolean;
  include_path_claims?: boolean;
  light?: boolean;
};

export type RuntimeTeamFinalSummaryResponse = {
  team_id: string;
  summary: string;
};

export type RuntimeTeammateRecord = {
  id: string;
  team_id: string;
  name?: string;
  profile?: string;
  session_id?: string;
  state?: string;
  last_heartbeat?: string;
  capabilities?: string[];
  created_at?: string;
  updated_at?: string;
};

export type RuntimeCreateTeamRequest = {
  id?: string;
  lead_session_id?: string;
  max_teammates?: number;
  max_writers?: number;
  status?: string;
  strategy?: string;
  workspace_id?: string;
};

export type RuntimeCreateTeamResponse = {
  team: RuntimeTeamRecord;
};

export type RuntimeUpsertTeammateRequest = {
  capabilities?: string[];
  id?: string;
  last_heartbeat?: string;
  name?: string;
  profile?: string;
  session_id?: string;
  state?: string;
};

export type RuntimeUpsertTeammateResponse = {
  teammate: RuntimeTeammateRecord;
};

export type RuntimeTeamTask = {
  id: string;
  team_id?: string;
  parent_task_id?: string | null;
  title?: string;
  goal?: string;
  inputs?: string[];
  status?: string;
  priority?: number;
  assignee?: string | null;
  lease_until?: string;
  retry_count?: number;
  read_paths?: string[];
  write_paths?: string[];
  deliverables?: string[];
  summary?: string;
  result_ref?: string | null;
  version?: number;
  created_at?: string;
  updated_at?: string;
};

export type RuntimeCreateTeamTaskRequest = {
  assignee?: string;
  deliverables?: string[];
  goal?: string;
  id?: string;
  inputs?: string[];
  parent_task_id?: string;
  priority?: number;
  read_paths?: string[];
  result_ref?: string;
  status?: string;
  summary?: string;
  title?: string;
  write_paths?: string[];
};

export type RuntimeCreateTeamTaskResponse = {
  task: RuntimeTeamTask;
};

export type RuntimeTeamTaskDependency = {
  task_id: string;
  depends_on_id: string;
};

export type RuntimeTeammatesResponse = {
  teammates: RuntimeTeammateRecord[];
  count: number;
  limit?: number;
  state?: string | null;
};

export type RuntimeTeamTasksResponse = {
  tasks: RuntimeTeamTask[];
  count: number;
  limit?: number;
  status?: string[];
  assignee?: string | null;
  parent_task_id?: string | null;
  task_ids?: string[];
  dependencies?: Record<string, string[]>;
  dependents?: Record<string, string[]>;
};

export type RuntimeTaskGraphResponse = {
  tasks: RuntimeTeamTask[];
  count: number;
  edges: RuntimeTeamTaskDependency[];
  edge_count: number;
  missing_dependencies?: string[];
  task_ids?: string[];
  limit?: number;
  include_external?: boolean;
  status?: string[];
  assignee?: string | null;
  parent_task_id?: string | null;
};

export type RuntimeTeamEventRecord = {
  seq: number;
  type: string;
  team_id: string;
  payload?: Record<string, unknown>;
  timestamp: string;
};

export type RuntimeTeamEventsResponse = {
  team_id: string;
  events: RuntimeTeamEventRecord[];
  after?: number;
  limit?: number;
  event_type?: string;
  since?: string | null;
  until?: string | null;
};

export type RuntimeTeamMailboxMessage = {
  id: string;
  team_id: string;
  from_agent: string;
  to_agent: string;
  task_id?: string | null;
  kind: string;
  body: string;
  metadata?: Record<string, unknown>;
  created_at?: string;
  acked_at?: string | null;
};

export type RuntimeTeamMailboxResponse = {
  messages: RuntimeTeamMailboxMessage[];
  count: number;
  parent_task_id?: string;
  limit?: number;
  marked_read?: boolean;
  agent_id?: string;
  filters?: Record<string, unknown>;
};

export type RuntimeSendTeamMailboxRequest = {
  body: string;
  from_agent?: string;
  kind?: string;
  metadata?: Record<string, unknown>;
  task_id?: string;
  to_agent?: string;
};

export type RuntimeSendTeamMailboxResponse = {
  message: RuntimeTeamMailboxMessage;
  dispatch_error?: string;
};

export type RuntimeAckTeamMailboxResponse = {
  message_id: string;
  team_id: string;
  agent_id?: string;
};

export type RuntimePathClaimRecord = {
  id: string;
  team_id: string;
  task_id: string;
  owner_agent_id: string;
  path: string;
  mode: string;
  lease_until?: string;
};

export type RuntimeTeamPathClaimsResponse = {
  claims: RuntimePathClaimRecord[];
  count: number;
  active_only?: boolean;
  as_of?: string;
  limit?: number;
  filters?: Record<string, unknown>;
};

export type RuntimePathClaimConflict = {
  path: string;
  existing_path: string;
  existing_owner: string;
  existing_task_id: string;
  existing_mode: string;
};

export type RuntimeCheckTeamPathClaimsResponse = {
  ok: boolean;
  conflicts: RuntimePathClaimConflict[];
};

/**
 * 嵌套错误体：`/api/runtime/fs/*`、`/api/runtime/git/*` 等新端点用
 * `{"error":{"code":…,"message":…}}`，旧端点仍是 `{"error":"…","code":…}`。
 * 读取一律走 `readErrorEnvelope()`（api/runtime/shared.ts），不要再假设 `error` 是字符串。
 */
export type RuntimeErrorEnvelope = {
  code?: string;
  message?: string;
  /** 少数后端（如 OpenAI 兼容层）把消息放在嵌套的 `error` 字段里。 */
  error?: string;
};

export type RuntimeErrorPayload = {
  error?: string | RuntimeErrorEnvelope;
  code?: string;
  context?: Record<string, unknown>;
  request_id?: string;
};

export type RuntimeTeamsQuery = {
  limit?: number;
  status?: string;
  workspaceId?: string;
};

export type RuntimeTeamSummariesQuery = {
  includeMailbox?: boolean;
  includePathClaims?: boolean;
  includeTeammateStates?: boolean;
  light?: boolean;
  limit?: number;
  teamIds?: string[];
};

export type RuntimeTeamTeammatesQuery = {
  limit?: number;
  state?: string;
};

export type RuntimeTeamTasksQuery = {
  assignee?: string;
  includeDependencies?: boolean;
  includeDependents?: boolean;
  limit?: number;
  parentTaskId?: string;
  status?: string[];
  taskIds?: string[];
};

export type RuntimeTaskGraphQuery = {
  assignee?: string;
  includeExternal?: boolean;
  limit?: number;
  parentTaskId?: string;
  status?: string[];
  taskIds?: string[];
};

export type RuntimeTeamEventsQuery = {
  after?: number;
  eventType?: string;
  limit?: number;
  since?: string;
  until?: string;
};

export type RuntimeTeamMailboxQuery = {
  agentId?: string;
  fromAgent?: string;
  includeBroadcast?: boolean;
  kind?: string;
  limit?: number;
  markRead?: boolean;
  parentTaskId?: string;
  since?: string;
  taskId?: string;
  toAgent?: string;
  unreadOnly?: boolean;
};

export type RuntimeTeamPathClaimsQuery = {
  activeOnly?: boolean;
  asOf?: string;
  limit?: number;
  mode?: string;
  ownerAgentId?: string;
  taskId?: string;
};

export type RuntimeCheckTeamPathClaimsRequest = {
  readPaths?: string[];
  writePaths?: string[];
};
