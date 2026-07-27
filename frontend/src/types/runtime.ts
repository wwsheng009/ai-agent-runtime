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

export type RuntimeSessionUserTurn = {
  index: number;
  message_index: number;
  preview: string;
  end_message_index: number;
  message_id?: string;
  turn_id?: string;
  has_later_mutation?: boolean;
  checkpoint_ids?: string[];
  base_checkpoint_id?: string;
};

export type RuntimeSessionTurnsResponse = {
  session_id: string;
  turns: RuntimeSessionUserTurn[];
  count: number;
};

export type RuntimeSessionBacktrackMode = "conversation" | "both" | "code";

export type RuntimeSessionBacktrackRequest = {
  user_turn_index?: number;
  message_index?: number;
  message_id?: string;
  mode?: RuntimeSessionBacktrackMode;
  edit_prompt?: string;
  auto_submit?: boolean;
  include_anchor?: boolean;
  preview_only?: boolean;
};

export type RuntimeSessionBacktrackCodeRestore = {
  checkpoint_id?: string;
  mode?: string;
  applied_paths?: string[];
  errors?: string[];
  preview?: string[];
};

export type RuntimeSessionBacktrackTombstone = {
  id: string;
  created_at: string;
  session_id?: string;
  mode?: string;
  reason?: string;
  user_turn_index: number;
  message_index: number;
  message_id?: string;
  anchor_preview?: string;
  truncated_to_message_count: number;
  removed_message_count: number;
  removed_user_turns: number;
  prior_message_count?: number;
  removed_message_ids?: string[];
  removed_turn_ids?: string[];
  edited?: boolean;
  include_anchor?: boolean;
  base_checkpoint_id?: string;
  later_checkpoint_ids?: string[];
};

export type RuntimeSessionBacktrackResult = {
  session_id: string;
  mode: string;
  user_turn_index: number;
  message_index: number;
  message_id?: string;
  truncated_to_message_count: number;
  removed_message_count: number;
  removed_user_turns: number;
  anchor_preview?: string;
  edited_prompt?: string;
  composer_prompt?: string;
  include_anchor?: boolean;
  auto_submitted?: boolean;
  preview_only?: boolean;
  base_checkpoint_id?: string;
  later_checkpoint_ids?: string[];
  tombstone?: RuntimeSessionBacktrackTombstone;
  code_restore?: RuntimeSessionBacktrackCodeRestore;
  warnings?: string[];
  events_emitted?: string[];
};

export type RuntimeSessionBacktrackResponse = {
  ok: boolean;
  result?: RuntimeSessionBacktrackResult;
  error?: string;
  submit_result?: Record<string, unknown>;
};

export type RuntimeSessionBacktrackAuditResponse = {
  session_id: string;
  entries: RuntimeSessionBacktrackTombstone[];
  count: number;
};

export type RuntimeSessionCheckpointRestoreResponse = {
  ok: boolean;
  result?: RuntimeSessionCheckpointPreviewResult;
  error?: string;
};

export type RuntimeSessionPlanModeStatus =
  | "inactive"
  | "active"
  | "exited"
  | string;

export type RuntimeSessionPlanModeExitDecision =
  | ""
  | "approve"
  | "request_changes"
  | "quit"
  | string;

export type RuntimeSessionPlanModeAction =
  | "enter"
  | "exit"
  | "approve"
  | "request_changes"
  | "quit"
  | "on"
  | "off"
  | "status";

export type RuntimeSessionPlanMode = {
  session_id: string;
  active: boolean;
  status: RuntimeSessionPlanModeStatus;
  plan_path?: string;
  write_allow_paths?: string[];
  previous_mode?: string;
  permission_mode: string;
  pending_exit_request?: boolean;
  exit_decision?: RuntimeSessionPlanModeExitDecision;
  notes?: string;
  entered_at?: string;
  exited_at?: string;
  workspace_path?: string;
  plan_content: string;
  plan_content_available: boolean;
  plan_content_truncated?: boolean;
  plan_content_error?: string;
  action?: string;
};

export type RuntimeSessionPlanModeUpdateRequest = {
  action?: RuntimeSessionPlanModeAction | string;
  decision?: RuntimeSessionPlanModeExitDecision | string;
  plan_path?: string;
  notes?: string;
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

export type AnalyticsGroupBy =
  | "day"
  | "provider"
  | "model"
  | "directory"
  | "project"
  | "status";

export type AnalyticsTokenTotals = {
  total_tokens: number;
  prompt_tokens: number;
  completion_tokens: number;
  cached_tokens: number;
  reasoning_tokens: number;
};

export type AnalyticsGlobalTotals = AnalyticsTokenTotals & {
  sessions: number;
  total_requests: number;
  total_responses: number;
  total_tool_calls: number;
  llm_requests: number;
  llm_successes: number;
  llm_errors: number;
  turns: number;
  failed_turns: number;
  recovered_turns: number;
  tool_results_observed: number;
  tool_errors: number;
  total_duration_ms: number;
  average_response_time_ms?: number;
};

export type AnalyticsSessionRollup = {
  session_id: string;
  runtime_session_id?: string;
  title?: string;
  title_source?: string;
  directory: string;
  project?: string;
  rel_path: string;
  start_time?: string;
  end_time?: string;
  last_observed_at?: string;
  status?: string;
  provider?: string;
  protocol?: string;
  model?: string;
  base_url?: string;
  stream?: boolean;
  total_requests: number;
  total_responses: number;
  total_tool_calls: number;
  total_tokens: number;
  prompt_tokens?: number;
  completion_tokens?: number;
  cached_tokens?: number;
  reasoning_tokens?: number;
  llm_requests?: number;
  llm_requests_with_usage?: number;
  llm_successes?: number;
  llm_errors?: number;
  turn_count: number;
  failed_turns: number;
  recovered_turns: number;
  tool_results_observed: number;
  tool_errors: number;
  average_response_time_ms?: number;
  total_duration_ms?: number;
  has_debug_usage?: boolean;
  source?: string;
  usage_quality: string;
  usage_complete: boolean;
  usage_coverage: number;
  partial: boolean;
  partial_reasons: string[];
  dropped_messages: number;
  reconciliation_status: string;
  reconciliation_delta: number;
};

export type AnalyticsGroupBucket = AnalyticsTokenTotals & {
  key: string;
  sessions: number;
  total_requests: number;
  total_responses: number;
  total_tool_calls: number;
  llm_requests: number;
  llm_successes: number;
  llm_errors: number;
  turns: number;
  failed_turns: number;
  recovered_turns: number;
  tool_results_observed: number;
  tool_errors: number;
  total_duration_ms: number;
  average_response_time_ms?: number;
};

export type AnalyticsSessionsQuery = {
  from?: string;
  to?: string;
  provider?: string;
  model?: string;
  directory?: string;
  project?: string;
  status?: string;
  q?: string;
  limit?: number;
  offset?: number;
  max_scan?: number;
};

export type AnalyticsSummaryQuery = AnalyticsSessionsQuery & {
  group_by?: AnalyticsGroupBy;
};

export type AnalyticsCoverage = {
  sessions: number;
  sessions_with_usage: number;
  usage_session_rate: number;
  llm_requests: number;
  llm_requests_with_usage: number;
  usage_request_rate: number;
  tool_results_observed: number;
  dropped_messages: number;
};

export type AnalyticsDataWindow = {
  from?: string;
  to?: string;
};

export type AnalyticsResponseMeta = {
  schema_version: string;
  generated_at: string;
  data_window: AnalyticsDataWindow;
  coverage: AnalyticsCoverage;
  partial: boolean;
  partial_reasons: string[];
};

export type AnalyticsSessionsResponse = AnalyticsResponseMeta & {
  sessions: AnalyticsSessionRollup[];
  count: number;
  total: number;
  limit: number;
  offset: number;
  scanned: number;
  totals: AnalyticsGlobalTotals;
};

export type AnalyticsSummaryResponse = AnalyticsResponseMeta & {
  group_by: AnalyticsGroupBy | string;
  totals: AnalyticsGlobalTotals;
  groups: AnalyticsGroupBucket[];
  scanned: number;
  matched: number;
};

export type AnalyticsDimensionsResponse = {
  schema_version: string;
  generated_at: string;
  providers: string[];
  models: string[];
  directories: string[];
  projects: string[];
  statuses: string[];
};

export type AnalyticsStepUsage = {
  started_at?: string;
  timestamp?: string;
  trace_id?: string;
  step?: number;
  success: boolean;
  prompt_tokens?: number;
  completion_tokens?: number;
  total_tokens?: number;
  cached_tokens?: number;
  cache_read_tokens?: number;
  cache_read_reported?: boolean;
  cache_hit_ratio?: number;
  cache_status?: string;
  reasoning_tokens?: number;
  usage_source?: string;
  usage_available: boolean;
  error_category?: string;
  duration_ms?: number;
  context_prompt_tokens?: number;
  context_window_tokens?: number;
  prompt_budget?: number;
  context_utilization?: number;
};

export type AnalyticsTurnUsage = {
  turn_id: string;
  trace_id: string;
  ordinal: number;
  started_at?: string;
  ended_at?: string;
  duration_ms: number;
  outcome: "success" | "recovered" | "failed" | "cancelled" | string;
  error_category?: string;
  llm_requests: number;
  llm_successes: number;
  llm_errors: number;
  tool_results_observed: number;
  tool_errors: number;
  usage: AnalyticsTokenTotals;
  usage_quality: string;
  usage_coverage: number;
  max_context_utilization: number;
};

export type AnalyticsDiagnostic = {
  code: string;
  severity: "info" | "warning" | "error" | string;
  count: number;
  rate?: number;
  turn_id?: string;
  error_category?: string;
};

export type AnalyticsSessionUsageDetail = Omit<AnalyticsResponseMeta, "data_window"> & {
  session: AnalyticsSessionRollup;
  steps: AnalyticsStepUsage[];
  step_count: number;
  turns: AnalyticsTurnUsage[];
  diagnostics: AnalyticsDiagnostic[];
  error_categories: Record<string, number>;
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

export type RuntimeAgentRoutePreviewParent = {
  provider?: string;
  model?: string;
  reasoning_effort?: string;
  max_tokens?: number;
  timeout?: string;
};

export type RuntimeAgentRoutePreviewTask = {
  role?: string;
  goal?: string;
  difficulty?: string;
  difficulty_rationale?: string;
  provider?: string;
  model?: string;
  reasoning_effort?: string;
  budget_tokens?: number;
  timeout?: string;
  read_only?: boolean;
};

export type RuntimeAgentRoutePreviewRequest = {
  document: RuntimeConfigDocumentSaveRequest;
  scope?: "auto" | "subagent" | "team";
  workflow?: string;
  parent?: RuntimeAgentRoutePreviewParent;
  task?: RuntimeAgentRoutePreviewTask;
};

export type RuntimeAgentRoutePreviewDecision = {
  difficulty?: string;
  difficulty_source?: string;
  difficulty_rationale?: string;
  provider?: string;
  model?: string;
  reasoning_effort?: string;
  max_tokens?: number;
  timeout?: string;
  source?: string;
  warnings?: string[];
  fallback_used?: boolean;
  fallback_reason?: string;
};

export type RuntimeAgentRoutePreviewResult = {
  scope: "subagent" | "team";
  routing_source: "subagent" | "team_independent" | "subagent_inherited";
  routing_enabled: boolean;
  parent: RuntimeAgentRoutePreviewParent;
  decision: RuntimeAgentRoutePreviewDecision;
};

export type RuntimeAgentRoutePreviewResponse = {
  route: RuntimeAgentRoutePreviewResult;
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
