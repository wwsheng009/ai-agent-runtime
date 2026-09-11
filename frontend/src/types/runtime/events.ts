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
