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
