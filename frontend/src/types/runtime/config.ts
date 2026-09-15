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

/** 工作区设置「最大步骤数」保存：写后端 runtime 内存快照 + 配置文件。 */
export type RuntimeAgentMaxStepsSaveRequest = {
  max_steps: number;
};

export type RuntimeAgentMaxStepsSaveResponse = {
  updated: boolean;
  max_steps: number;
  config_file: string;
};

/** 读取后端缺省的「最大步骤数」：runtime 内存快照里的 agent.maxSteps + 来源配置文件路径。 */
export type RuntimeAgentMaxStepsResponse = {
  /** 后端接受的取值上限（0 = 不限制）。 */
  limit: number;
  max_steps: number;
  config_file: string;
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
