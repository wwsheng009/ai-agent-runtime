export type AgentChatMessage = {
  role: "user" | "assistant" | "system";
  content: string;
};

export type AgentChatRequest = {
  messages: AgentChatMessage[];
  session_id?: string;
  user_id?: string;
  workspace_path?: string;
  provider?: string;
  model?: string;
  reasoning_effort?: string;
  enable_react?: boolean;
  enable_routing?: boolean;
  max_steps?: number;
  stream?: boolean;
};

export type AgentChatResult = {
  kind?: string;
  source?: string;
  success?: boolean;
  output?: string;
  error?: string;
  model?: string;
  skill?: string;
  reasoning?: string;
  metadata?: Record<string, unknown>;
  orchestration?: Record<string, unknown>;
  planning?: Record<string, unknown>;
  subagent_summary?: Record<string, unknown>;
  subagent_results?: unknown[];
  tool_calls?: unknown[];
  usage?: Record<string, unknown> | null;
  duration?: Record<string, unknown> | null;
  trace_id?: string;
};

export type AgentChatResponse = {
  session_id?: string;
  agent_id?: string;
  source?: string;
  status?: string;
  result: AgentChatResult;
};

export type RuntimeModelProviderRecord = {
  name: string;
  default_model?: string;
  models: string[];
  model_count?: number;
  supports_tools?: boolean;
  supports_streaming?: boolean;
  max_context_tokens?: number;
  max_output_tokens?: number;
};

export type RuntimeModelsResponse = {
  default_provider?: string;
  default_model?: string;
  providers: RuntimeModelProviderRecord[];
  count: number;
};

export type SseEnvelopeMeta = {
  name?: string;
  schema_version?: string;
  sequence?: number;
  timestamp?: string;
};

export type AgentChatStreamMetaPayload = {
  _event?: SseEnvelopeMeta;
  session_id?: string;
  agent_id?: string;
  source?: string;
  kind?: string;
  status?: string;
  model?: string;
  orchestration?: Record<string, unknown>;
  planning?: Record<string, unknown>;
};

export type AgentChatStreamChunkPayload = {
  _event?: SseEnvelopeMeta;
  index?: number;
  type?: string;
  content?: string;
  total_chars?: number;
  text?: {
    content?: string;
    total_chars?: number;
  };
  reasoning?: Record<string, unknown>;
  tool?: Record<string, unknown>;
  tool_call?: Record<string, unknown> | null;
  delta?: Record<string, unknown> | null;
  metadata?: Record<string, unknown>;
};

export type AgentChatStreamDonePayload = {
  _event?: SseEnvelopeMeta;
  session_id?: string;
  agent_id?: string;
  source?: string;
  status?: string;
  content?: string;
  result?: AgentChatResult;
};

export type SessionHistoryMessage = {
  role: string;
  content: string;
  metadata?: Record<string, unknown>;
};

export type SessionHistoryResponse = {
  session_id: string;
  history: SessionHistoryMessage[];
  count: number;
};

export type RuntimeSessionRecord = {
  id: string;
  userId?: string;
  state?: string;
  metadata?: {
    title?: string;
    titleSource?: string;
    summary?: string;
    totalTurns?: number;
    lastAgent?: string;
    lastSkill?: string;
    lastModel?: string;
    createdBy?: string;
    context?: Record<string, unknown>;
  };
  createdAt?: string;
  updatedAt?: string;
  expiresAt?: string | null;
};

export type RuntimeCheckpointProvenanceSummary = {
  source_refs?: string[];
  profile_resource_refs?: string[];
  profile_resource_kinds?: Record<string, number>;
  profile_resource_count?: number;
  profile_memory_count?: number;
  profile_notes_count?: number;
  profile_resource_labels?: string[];
};

export type RuntimeSessionCheckpointSummary = {
  id: string;
  session_id: string;
  task_id?: string;
  reason?: string;
  history_hash?: string;
  message_count: number;
  conversation_exact?: boolean;
  conversation_message_count?: number;
  created_at: string;
  metadata?: Record<string, unknown>;
  provenance?: RuntimeCheckpointProvenanceSummary;
};

export type RuntimeSessionCheckpointsResponse = {
  checkpoints: RuntimeSessionCheckpointSummary[];
  count: number;
};

export type RuntimeSessionCheckpointPreviewMode = "both" | "code" | "conversation";

export type RuntimeSessionCheckpointPreviewFile = {
  path: string;
  change: string;
  diff_text?: string;
};

export type RuntimeSessionCheckpointConversationMessage = {
  role?: string;
  content?: string;
};

export type RuntimeSessionCheckpointPreviewResult = {
  checkpoint_id: string;
  mode: string;
  applied_paths?: string[];
  errors?: string[];
  preview?: string[];
  preview_files?: RuntimeSessionCheckpointPreviewFile[];
  conversation_changed?: boolean;
  conversation_head?: number;
  conversation_exact?: boolean;
  conversation_messages?: RuntimeSessionCheckpointConversationMessage[];
  provenance?: RuntimeCheckpointProvenanceSummary;
};

export type RuntimeSessionCheckpointPreviewResponse = {
  result: RuntimeSessionCheckpointPreviewResult;
};

export type RuntimeSessionCheckpointFile = {
  id: string;
  checkpoint_id: string;
  path: string;
  op: string;
  before_blob_id?: string;
  after_blob_id?: string;
  before_hash?: string;
  after_hash?: string;
  diff_text?: string;
};

export type RuntimeSessionCheckpointFilesResponse = {
  files: RuntimeSessionCheckpointFile[];
  count: number;
};

export type RuntimeCreateSessionRequest = {
  title?: string;
  user_id?: string;
};

export type RuntimeCreateSessionResponse = {
  session: RuntimeSessionRecord;
};

export type RuntimeSessionsResponse = {
  sessions: RuntimeSessionRecord[];
  count: number;
  user_id?: string;
};

export type RuntimeSessionsQuery = {
  userId?: string;
};

export type RuntimeSessionUserSummary = {
  user_id: string;
  display_name?: string;
  source?: string;
  session_count: number;
  active_count?: number;
  idle_count?: number;
  closed_count?: number;
  archived_count?: number;
  recoverable_count?: number;
  latest_updated_at?: string;
};

export type RuntimeSessionUsersResponse = {
  users: RuntimeSessionUserSummary[];
  count: number;
  total_count?: number;
  default_user_id?: string;
  limit?: number;
};

export type RuntimeSessionCheckpointsQuery = {
  limit?: number;
  offset?: number;
};

export type SessionRuntimeEvent = {
  type: string;
  trace_id?: string;
  agent_name?: string;
  session_id?: string;
  tool_name?: string;
  payload?: Record<string, unknown>;
  timestamp: string;
  provenance?: Record<string, unknown>;
};

export type RuntimeLogEntry = {
  cursor: number;
  raw?: Record<string, unknown>;
  raw_text: string;
  timestamp?: string;
  level?: string;
  module?: string;
  caller?: string;
  message?: string;
  request_id?: string;
  trace_id?: string;
  session_id?: string;
  provider?: string;
  model?: string;
  method?: string;
  url?: string;
  response_status_code?: number;
  response_body_preview?: string;
  upstream_error?: string;
  fields?: Record<string, unknown>;
};

export type RuntimeLogsQuery = {
  limit?: number;
  level?: string;
  query?: string;
};

export type RuntimeLogsResponse = {
  entries: RuntimeLogEntry[];
  count: number;
  exists?: boolean;
  file_path?: string;
  next_cursor: number;
  filters?: {
    limit?: number;
    level?: string;
    query?: string;
  };
};

export type RuntimeLogStreamReadyPayload = {
  cursor?: number;
  exists?: boolean;
  file_path?: string;
};

export type RuntimeLogStreamResetPayload = {
  cursor?: number;
  exists?: boolean;
  file_path?: string;
  reason?: string;
};

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

export type RuntimeErrorPayload = {
  error?: string;
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

export type RuntimeConfigDocumentSection = {
  key: string;
  kind: string;
  item_count?: number;
};

export type RuntimeConfigDocumentRuntimeImpact = {
  changed_paths?: string[];
  hot_reload_paths?: string[];
  restart_required_paths?: string[];
  inactive_paths?: string[];
  applied_paths?: string[];
};

export type RuntimeConfigDocument = {
  path: string;
  format: string;
  raw: string;
  parsed: unknown;
  sections?: RuntimeConfigDocumentSection[];
  size_bytes: number;
  updated_at?: string;
  warnings?: string[];
  restart_required?: boolean;
  supports_structured_save?: boolean;
  runtime_impact?: RuntimeConfigDocumentRuntimeImpact;
};

export type RuntimeConfigDocumentResponse = {
  document: RuntimeConfigDocument;
};

export type RuntimeConfigDocumentSaveRequest = {
  raw?: string;
  parsed?: unknown;
  mode?: "raw" | "structured";
  changed_by?: string;
};

export type RuntimeConfigDocumentSaveResponse = {
  saved: boolean;
  document: RuntimeConfigDocument;
};

export type RuntimeServiceStatus = {
  running: boolean;
  pid: number;
  pid_file?: string;
  listen_addr?: string;
  config_path?: string;
  cwd?: string;
  executable?: string;
  started_at?: string;
  restart_supported?: boolean;
  note?: string;
};

export type RuntimeServiceStatusResponse = {
  service: RuntimeServiceStatus;
};

export type RuntimeServiceRestartResult = {
  accepted: boolean;
  message?: string;
  requested_at?: string;
};

export type RuntimeServiceRestartResponse = {
  restart: RuntimeServiceRestartResult;
};
