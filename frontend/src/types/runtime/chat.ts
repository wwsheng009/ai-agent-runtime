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
  /**
   * P4-刷新续传：客户端断开（页面刷新 / 关标签）后不取消本回合，run 继续执行并把
   * 增量与历史照常落库；刷新后的新页面通过 `/runtime` 的 `active_turn` 重新挂载
   * 回合身份，在 `/runtime/stream` 上按游标续传。缺省 false 保持旧语义（断开即中止）。
   */
  resume_on_disconnect?: boolean;
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
  // 运行时工具生命周期帧（`tool.requested` / `tool.completed`，见
  // backend/internal/agent/tool_runtime_events.go）在结构化入参之外下发的定位字段：
  // 实时帧只有 `arg_preview` 键值文本（无 arguments），`display_file_path` 仅在路径
  // 需要独占一行时出现。
  arg_preview?: string;
  command_text?: string;
  display_file_path?: string;
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

/** 会话历史里的工具调用（后端 `types.ToolCall`，P4 轨迹兜底取 id/name/arguments）。 */
export type SessionHistoryToolCall = {
  id?: string;
  type?: string;
  name?: string;
  arguments?: Record<string, unknown>;
  /** 部分写入方只落原始入参 JSON 串（`types.ToolCall.RawInput`）。 */
  input?: string;
};

export type SessionHistoryMessage = {
  role: string;
  content: string;
  /** 多模态内容（文本/图片分片）；轨迹兜底只取文本分片以外的正文。 */
  content_parts?: Array<{ type?: string; text?: string }>;
  tool_calls?: SessionHistoryToolCall[];
  tool_call_id?: string;
  metadata?: Record<string, unknown>;
};

export type SessionHistoryResponse = {
  session_id: string;
  history: SessionHistoryMessage[];
  count: number;
  /** 分页元信息（后端 GetSessionHistory；旧响应可能缺省）。 */
  total?: number;
  has_more?: boolean;
  limit?: number;
  first_seq?: number;
  last_seq?: number;
  /** 下一页游标（`before_seq`，向前翻页，仅 has_more=true 时可用）。 */
  next_before_seq?: number;
};
