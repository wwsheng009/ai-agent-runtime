export type RuntimeSessionRecord = {
  id: string;
  userId?: string;
  state?: string;
  metadata?: {
    title?: string;
    titleSource?: string;
    summary?: string;
    /** 服务端会话标签（P2-1A：检索按标签 AND 过滤）。 */
    tags?: string[];
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

/**
 * 元数据检索筛选（POST /api/runtime/sessions/search）。
 * 全部可选；tags 为 AND 语义，state 为全等匹配（active/idle/closed/archived）。
 */
export type RuntimeSessionSearchFilters = {
  userId?: string;
  tags?: string[];
  state?: string;
  limit?: number;
  offset?: number;
};

/**
 * 后端回显的筛选条件（chat.SessionSearchOptions）：与请求体不同，
 * 回显的 json tag 是 camelCase（userId/tags/state/limit/offset）。
 */
export type RuntimeSessionSearchEcho = {
  userId?: string;
  tags?: string[];
  state?: string;
  limit?: number;
  offset?: number;
};

/** 检索响应：`{sessions, count, filters}`（sessions 为 chat.Session 记录）。 */
export type RuntimeSessionSearchResponse = {
  sessions: RuntimeSessionRecord[];
  count: number;
  filters?: RuntimeSessionSearchEcho;
};

/** 会话统计（对应后端 chat.SessionStatistics）。 */
export type RuntimeSessionStats = {
  total: number;
  active: number;
  idle: number;
  closed: number;
  archived: number;
  /** camelCase per backend json tag。 */
  totalMessages: number;
  tags: Record<string, number>;
};

export type RuntimeSessionStatsResponse = {
  user_id: string;
  stats: RuntimeSessionStats;
};

/** 归档/激活/关闭返回体：`{"session": ..., "state": "archived"|"active"|"closed"}`。 */
export type RuntimeSessionStateChangeResponse = {
  session: RuntimeSessionRecord;
  state: string;
};

/** 批量归档/删除返回体（后端只保证 action 与计数类字段，其余按需读取）。 */
export type RuntimeSessionBatchActionResponse = {
  action: string;
  [key: string]: unknown;
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
