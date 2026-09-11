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

export type RuntimeCreateSessionRequest = {
  title?: string;
  user_id?: string;
  /** Bind the new session to a registered workspace directory path. */
  workspace_path?: string;
  /** Alternative to workspace_path: resolved server-side via the registry. */
  directory_id?: string;
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

export type RuntimeWorkspaceDirectory = {
  id: string;
  path: string;
  name?: string;
  /** Unix seconds. */
  created_at?: number;
  /** Unix seconds. */
  last_used_at?: number;
  /** Computed live by the server; false when the path is missing on disk. */
  exists?: boolean;
  /** Best-effort server-side count; the workspace UI aggregates locally. */
  session_count?: number;
};

export type RuntimeWorkspaceDirectoriesResponse = {
  directories: RuntimeWorkspaceDirectory[];
  count: number;
};

export type RuntimeCreateWorkspaceDirectoryRequest = {
  path: string;
  name?: string;
};

export type RuntimeUpdateWorkspaceDirectoryRequest = {
  name?: string;
};

export type RuntimeDeleteWorkspaceDirectoryResponse = {
  deleted: boolean;
  id: string;
  sessions_affected?: number;
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
