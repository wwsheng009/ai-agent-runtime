// 会话路由（session-scoped agent routing）前端契约：GET/PATCH
// `/api/runtime/sessions/{id}/routing` 的响应与请求体。
//
// 契约来源（只读参考，禁止前端改写语义）：
//   * backend/internal/agentconfig/routing_status.go —— RoutingStatusProjection /
//     RoutingLevelSummary / RoutingPanelMetadata 的 snake_case JSON；
//   * backend/internal/agentconfig/routing_resolution.go —— 覆盖结构的字段级补丁；
//   * backend/internal/api/skills/session_routing_handlers.go —— 请求/响应外壳。
//
// I-6（方案 §10.2）：这些字段值一律**直接取自后端投影**，前端不重算 provider/
// model/effort/source，也不推断「某层是否被覆盖」以外的语义。

/** 投影来源标记（§6.1）：session|workspace|config|default|derived。 */
export type RoutingSourceKind =
  | "session"
  | "workspace"
  | "config"
  | "default"
  | "derived";

/** 写入层（§5.4 写入层选择器）：三层均可在面板内写入。 */
export type SessionRoutingTargetLayer = "session" | "workspace" | "config";

/** 面板作用域（§4.4）：main=主 Agent 路由，sub=子 Agent 路由。 */
export type RoutingPanelScope = "main" | "sub";

/** §6.1 唯一投影结构；TUI / web / API 三处同源。 */
export type RoutingStatusProjection = {
  schemaVersion: number;
  enabled: boolean;
  /** 当前 turn 生效档位；未判定时为空串。 */
  level: string;
  provider: string;
  model: string;
  reasoning: string;
  source: string;
  /** true=该作用域路由未启用（与 enabled 互斥，二者都由后端给出）。 */
  disabled: boolean;
  warnings: string[];
  /** §3.4 覆盖的 UpdatedAt/UpdatedBy 投影；无覆盖时为空串。 */
  revision: string;
  /** 写入生效时机（§4.5）：固定 next_turn。 */
  effectiveFrom: string;
};

/** 面板逐级表格的一行（§5.3/§5.7：level × provider/model/effort/source）。 */
export type RoutingLevelSummary = {
  level: string;
  enabled: boolean;
  provider: string;
  model: string;
  reasoning: string;
  source: string;
  /** 高价档位标记（§5.7）；false/缺失即非高价。 */
  expensive: boolean;
};

/** GET /routing 附加的面板元数据（§4.4：各层徽标 / 可写性 / 目标文件路径）。 */
export type RoutingPanelMetadata = {
  scope: RoutingPanelScope;
  childSession: boolean;
  sessionOverride: boolean;
  workspaceOverride: boolean;
  configOverride: boolean;
  workspacePath: string;
  workspacePrefsPath: string;
  configPath: string;
  configLayer: string;
  /** 后端给出的可写层白名单；前端只据此启用/置灰，不自行推断。 */
  writableLayers: string[];
  levels: RoutingLevelSummary[];
  subAgent: RoutingStatusProjection | null;
};

/** GET/PATCH /routing 的响应外壳（§6.5）。 */
export type SessionRoutingResponse = {
  sessionId: string;
  scope: string;
  targetLayer: string;
  /** PATCH 实际落盘的文件路径；GET 或未写入时为空。 */
  targetPath: string;
  /** 写入是否使运行中的 actor 失效（下一 turn 重新解析）。 */
  actorInvalidated: boolean;
  /** 本次 PATCH 是否真的产生了改动。 */
  updated: boolean;
  routing: RoutingStatusProjection;
  subAgent: RoutingStatusProjection;
  panel: RoutingPanelMetadata;
  warnings: string[];
};

/** profile 字段级覆盖补丁：nil=继承，非 nil=显式覆盖（含显式空串）。 */
export type SessionRoutingLevelProfilePatch = {
  provider?: string;
  model?: string;
  reasoning_effort?: string;
};

/** 主 Agent 覆盖补丁：字段级合并，缺省字段不动。 */
export type SessionRoutingMainAgentPatch = {
  enabled?: boolean;
  profiles?: Record<string, SessionRoutingLevelProfilePatch>;
};

/** PATCH /routing 请求体（§3.2 补丁 + 层路由字段）。 */
export type SessionRoutingPatchRequest = {
  target_layer?: SessionRoutingTargetLayer;
  /** true=清除该层覆盖（reset 语义，§3.5.x）。 */
  clear?: boolean;
  /** config 层写入的二次确认（§5.4）。 */
  confirm?: boolean;
  main_agent?: SessionRoutingMainAgentPatch;
  /** §3.5.1 键路径清除，如 `main_agent.profiles.hard.model`。 */
  clear_fields?: string[];
  updated_by?: string;
};

/** 写入层选择器的候选（顺序即 UI 顺序）。 */
export const SESSION_ROUTING_TARGET_LAYERS: readonly SessionRoutingTargetLayer[] =
  ["session", "workspace", "config"];

/** 面板内的三列可编辑 profile 字段（其余 profile 字段不在 P3 面板暴露）。 */
export const SESSION_ROUTING_EDITABLE_FIELDS = [
  "provider",
  "model",
  "reasoning_effort",
] as const;

export type SessionRoutingEditableField =
  (typeof SESSION_ROUTING_EDITABLE_FIELDS)[number];
