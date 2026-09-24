// Profiles（profile.yaml）前端契约类型——Batch 8 §10.5。
//
// 权威来源（逐字段对齐，勿凭记忆改动）：
//   * 视图：backend/internal/api/skills/profiles_handlers.go（buildRuntimeProfileView）
//     与 profiles_view_groups.go（tools/skills/mcp/prompts/agents/preferences 分组）；
//   * 写请求：profiles_write_handlers.go（profileUpdateRequest / profileCreateRequest）；
//   * 生命周期：profiles_lifecycle_handlers.go（references/rename/move/default/delete）。
//
// 字段一律 camelCase（与 types/runtime/mcp.ts 同纪律）：后端 snake_case 由
// api/runtime/profiles.ts 的归一化层转换，UI 只消费这里的类型。

/** profile 所在层：user / project / builtin / config 等；后端可扩展，故保留 string。 */
export type RuntimeProfileLayer = string;

/** 列表条目（后端 runtimeProfileEntry，字段名见 profiles_store.go）。 */
export type RuntimeProfileListEntry = {
  /** 稳定引用（如 `user:reviewer`），用于所有后续端点。 */
  ref: string;
  name: string;
  description: string;
  layer: RuntimeProfileLayer;
  path: string;
  /** profile.yaml 可解析（false 时 error 给出原因）。 */
  valid: boolean;
  /** valid=false 时的原因（YAML 解析失败等）。 */
  error: string;
  /** 是否为全局默认 profile。 */
  isDefault: boolean;
  /** profile.yaml 声明的 default_agent（可空）。 */
  defaultAgent: string;
  /** API 能否写回（当前有 profile.yaml 即可写）。 */
  writable: boolean;
};

export type RuntimeProfileListResponse = {
  count: number;
  /** 全局默认 profile 名（config profiles.default_profile）。 */
  defaultProfile: string;
  /** 默认 profile 的根目录。 */
  defaultRoot: string;
  profiles: RuntimeProfileListEntry[];
};

/** 工具面：allowlist/denylist 为 profile 声明（可写），其余为后端推导（只读）。 */
export type RuntimeProfileToolGroup = {
  allowlist: string[];
  denylist: string[];
  /** 当前视图使用的 agent id（查询参数 agent 或 profile.default_agent）。 */
  agentId: string;
  /** 该 agent 在 agents/<id>/agent.yaml 或 agents.<id> 里的声明（只读）。 */
  agentAllowlist: string[];
  agentDenylist: string[];
  /** 合并全局策略后的生效清单（只读）。 */
  effective: string[];
  /** 被 deny/全局策略排除的清单（只读）。 */
  excluded: string[];
  /** 生效面来源（profile / agent / global …，只读）。 */
  sources: string[];
  /** 只读工具面（声明了 tools.read_only 时给出）。 */
  readOnly?: boolean;
};

/** Skills：allowlist/denylist 可写；dirs 与曝光参数为只读展示。 */
export type RuntimeProfileSkillGroup = {
  allowlist: string[];
  denylist: string[];
  /** 发现到的技能目录（只读）。 */
  dirs: string[];
  /** runtime.overrides.skills_runtime.aicli_skill_exposure_mode（只读）。 */
  exposureMode: string;
  /** runtime.overrides.skills_runtime.aicli_skill_exposure_top_k（只读，0=未声明）。 */
  topK: number;
};

export type RuntimeProfileMcpServer = {
  name: string;
  used: boolean;
  excluded: boolean;
};

/** MCP：useServers/excludeServers 可写；servers 为连接选择结果（只读）。 */
export type RuntimeProfileMcpGroup = {
  useServers: string[];
  excludeServers: string[];
  servers: RuntimeProfileMcpServer[];
  serverCount: number;
  usedCount: number;
};

/** prompt 层文件（只读）：路径不存在时 exists=false，UI 需标注。 */
export type RuntimeProfilePromptLayer = {
  path: string;
  exists: boolean;
};

/** Prompts：只有 mode（replace/append）可写，其余只读。 */
export type RuntimeProfilePrompts = {
  mode: string;
  system: RuntimeProfilePromptLayer;
  role: RuntimeProfilePromptLayer;
  tools: RuntimeProfilePromptLayer;
};

/** profile.yaml agents 节条目（只读展示；default_agent 在 profile.default_agent）。 */
export type RuntimeProfileAgentEntry = {
  id: string;
  provider: string;
  model: string;
  tools: {
    allowlist: string[];
    denylist: string[];
  };
};

export type RuntimeProfileAgents = {
  defaultAgent: string;
  /** profile.yaml 声明的 agent id 清单（排序）。 */
  available: string[];
  entries: RuntimeProfileAgentEntry[];
};

export type RuntimeProfileOverrideEntry = {
  key: string;
  /** 值文本（标量直出、结构体 JSON）；写回时按标量类型化解析。 */
  value: string;
  /** 来源标记（profile）。 */
  origin: string;
  /** D14 白名单判定；false 时 UI 禁止写入。 */
  allowed: boolean;
};

export type RuntimeProfileOverrides = {
  keys: string[];
  origins: Record<string, string>;
  count: number;
  entries: RuntimeProfileOverrideEntry[];
};

/** 偏好与审批：全部只读（权限模式来自 agentdef 权威解析 + D16 约束）。 */
export type RuntimeProfilePreferences = {
  permissionMode: string;
  /** 权限模式来源（profile:/abs/path 或 builtin 等）。 */
  permissionModeSource: string;
  provider: string;
  model: string;
};

export type RuntimeProfileEstimate = {
  basis: string;
  toolCount: number;
  toolTokens: number;
  promptTokens: number;
  totalTokens: number;
  toolAllowCount: number;
  toolDenyCount: number;
  skillAllowCount: number;
  skillDenyCount: number;
  skillDirCount: number;
};

export type RuntimeProfileIssueSeverity = "error" | "warning";

export type RuntimeProfileValidationIssue = {
  path: string;
  message: string;
  severity: RuntimeProfileIssueSeverity;
};

export type RuntimeProfileView = {
  ref: string;
  name: string;
  description: string;
  /** registered / root / default 等来源标记。 */
  source: string;
  layer: RuntimeProfileLayer;
  path: string;
  profileFile: string;
  /** profile.yaml 的 mtime（写回冲突检测基线）。 */
  mtime: string;
  valid: boolean;
  errorCount: number;
  warningCount: number;
  issues: RuntimeProfileValidationIssue[];
  /** profile.yaml 原始文档（PUT 写回的 base；未知字段靠它保留）。 */
  document: RuntimeProfileDocument;
  tools: RuntimeProfileToolGroup;
  skills: RuntimeProfileSkillGroup;
  mcp: RuntimeProfileMcpGroup;
  prompts: RuntimeProfilePrompts;
  agents: RuntimeProfileAgents;
  overrides: RuntimeProfileOverrides;
  preferences: RuntimeProfilePreferences;
  estimate: RuntimeProfileEstimate;
  /** true 表示来自 preview 端点（未写盘）。 */
  preview: boolean;
  defaultAgent: string;
  promptMode: string;
  writeTarget: string;
};

/** 写回文档：后端按 profile schema 校验；前端只做结构保持。 */
export type RuntimeProfileDocument = Record<string, unknown>;

export type RuntimeProfileWriteRequest = {
  /** 完整 profile.yaml 文档（由 view.document 派生，保留未知字段）。 */
  document: RuntimeProfileDocument;
  /** 冲突检测基线（view.mtime）；缺失即不检查，因此调用方必须传。 */
  expectedMtime: string;
};

export type RuntimeProfileValidateResponse = {
  ref: string;
  name: string;
  path: string;
  root: string;
  agent: string;
  valid: boolean;
  errorCount: number;
  warningCount: number;
  issues: RuntimeProfileValidationIssue[];
};

/**
 * 校验报告别名：编辑器把 /validate 的响应当「报告」消费（旧称 validation report），
 * 形状与 RuntimeProfileValidateResponse 完全一致，不引入第二份结构。
 */
export type RuntimeProfileValidationReport = RuntimeProfileValidateResponse;

/** preview 端点与 GET 同构（多用于带 draft 的预览），故复用视图类型。 */
export type RuntimeProfilePreviewReport = RuntimeProfileView;

export type RuntimeProfileConfigItem = {
  name: string;
  root: string;
  isDefault: boolean;
};

/** 引用面（删除/移动前展示）：profiles_lifecycle_handlers.go 的运行时结构。 */
export type RuntimeProfileReferencesResponse = {
  ref: string;
  root: string;
  defaultProfile: string;
  isDefault: boolean;
  blocking: string[];
  warnings: string[];
  configItems: RuntimeProfileConfigItem[];
  sessionScan: Record<string, unknown>;
  agentReferences: Record<string, unknown>[];
  agentReferenceNote: string;
  files: string[];
  fileCount: number;
};

export type RuntimeProfileApplyResponse = {
  /** Batch 13 才落地；当前后端返回 501，调用方据此降级展示。 */
  ok: boolean;
  changed: Record<string, string>;
  warnings: string[];
};

export type RuntimeProfileCreateRequest = {
  name: string;
  /** user / project；缺省 user。 */
  layer?: RuntimeProfileLayer;
  /** 显式覆盖落盘目录（与 layer 同时给出时以 root 为准）。 */
  root?: string;
  /** 模板名（与 fromRef 互斥）。 */
  template?: string;
  /** 以既有 profile 为模板复制。 */
  fromRef?: string;
  /** 模板 profile 的 default_agent。 */
  agent?: string;
  /** 目标目录非空时覆盖。 */
  force?: boolean;
  /** 注册到 config profiles.items。 */
  use?: boolean;
  /** 同时写入 config profiles.default_profile（只影响新会话）。 */
  setDefault?: boolean;
};

export type RuntimeProfileCreateResponse = {
  name: string;
  root: string;
  layer: RuntimeProfileLayer;
  /** 落盘文件清单（相对路径）。 */
  files: string[];
  /** 指向新建 profile（按 name 解析），可直接作为后续端点 ref。 */
  ref: string;
  registered: boolean;
  defaultProfileSet: boolean;
  /** new_sessions_only 等影响面说明。 */
  affects: string;
  configPath: string;
};

export type RuntimeProfileDuplicateRequest = RuntimeProfileCreateRequest;

export type RuntimeProfileRenameRequest = {
  name: string;
};

export type RuntimeProfileRenameResponse = {
  renamed: boolean;
  oldRef: string;
  newRef: string;
  root: string;
  layer: RuntimeProfileLayer;
  configUpdated: boolean;
  /** 配置更新失败时后端给出原因（HTTP 500 但目录已改名）。 */
  error: string;
  sessionNote: string;
  references: RuntimeProfileReferencesResponse | null;
};

export type RuntimeProfileMoveRequest = {
  /** 目标层（user / project）；与 root 同时给出时以 root 为准。 */
  layer?: RuntimeProfileLayer;
  root?: string;
};

export type RuntimeProfileMoveResponse = {
  moved: boolean;
  ref: string;
  from: string;
  to: string;
  configUpdated: boolean;
  error: string;
  references: RuntimeProfileReferencesResponse | null;
};

export type RuntimeProfileDeleteResponse = {
  deleted: boolean;
  ref: string;
  root: string;
  removedFiles: string[];
  removedFileCount: number;
  configItemsRemoved: string[];
  defaultCleared: boolean;
  references: RuntimeProfileReferencesResponse | null;
  /** 硬删语义：不可恢复（无回收站）。 */
  recoverable: boolean;
  /** 被引用且未 force 时后端返回的阻断信息。 */
  error: string;
};

export type RuntimeProfileDefaultResponse = {
  defaultProfile: string;
  previousDefault: string;
  configPath: string;
  registered: boolean;
  /** 只影响新会话（D26）。 */
  affects: string;
  currentSessionNote: string;
};
