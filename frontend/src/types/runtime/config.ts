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

/** runtime 配置文件（runtime.yaml）的一层：候选文件、是否存在、是否只读。 */
export type RuntimeConfigLayer = {
  kind: string;
  path: string;
  present: boolean;
  /** 只读层只提供默认值（发行包/仓库自带），写入会落到可写层。 */
  read_only?: boolean;
};

/** 读取后端缺省的「最大步骤数」：runtime 内存快照里的 agent.maxSteps + 来源配置文件路径。 */
export type RuntimeAgentMaxStepsResponse = {
  /** 后端接受的取值上限（0 = 不限制）。 */
  limit: number;
  max_steps: number;
  config_file: string;
  /** runtime.yaml 的层栈（分层）：候选文件、只读层与写入目标。 */
  layers?: RuntimeConfigLayer[];
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
  /** 任务类型（12 类封闭枚举；与 role 并存，task_type 优先命中 task_types 配置）。 */
  task_type?: string;
  /** 任务主体（用于展示与启发式判断的可读描述）。 */
  task_subject?: string;
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
  /** 命中的任务类型（回显；未声明时为空）。 */
  task_type?: string;
  task_subject?: string;
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
