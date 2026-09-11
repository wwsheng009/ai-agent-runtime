export type AgentChatMessage = {
  role: "user" | "assistant" | "system";
  content: string;
};

export type AgentChatRequest = {
  messages: AgentChatMessage[];
  session_id?: string;
  turn_id?: string;
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
  turn_id?: string;
  assistant_stream_id?: string;
  assistant_stream_sequence?: number;
};

export type AgentChatResponse = {
  session_id?: string;
  agent_id?: string;
  source?: string;
  status?: string;
  result: AgentChatResult;
};

export type RuntimeModelCapabilitySpec = {
  reasoning_model?: boolean;
  reasoning_efforts?: string[];
  reasoning_effort_budgets?: Record<string, number>;
  default_reasoning_effort?: string;
  [key: string]: unknown;
};

export type RuntimeModelProviderRecord = {
  name: string;
  default_model?: string;
  models: string[];
  model_count?: number;
  model_capabilities?: Record<string, RuntimeModelCapabilitySpec>;
  supports_tools?: boolean;
  supports_streaming?: boolean;
  max_context_tokens?: number;
  max_output_tokens?: number;
};

export type RuntimeModelsResponse = {
  default_provider?: string;
  default_model?: string;
  default_reasoning_effort?: string;
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
  turn_id?: string;
};

export type AgentChatStreamChunkPayload = {
  _event?: SseEnvelopeMeta;
  index?: number;
  type?: string;
  content?: string;
  stream_id?: string;
  sequence?: number;
  turn_id?: string;
  step?: number;
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

export type ChatStreamPhase =
  | "connecting"
  | "first-token"
  | "streaming"
  | "tool"
  | "finalizing";

export type AgentChatStreamDonePayload = {
  _event?: SseEnvelopeMeta;
  session_id?: string;
  agent_id?: string;
  source?: string;
  status?: string;
  content?: string;
  result?: AgentChatResult;
  turn_id?: string;
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
