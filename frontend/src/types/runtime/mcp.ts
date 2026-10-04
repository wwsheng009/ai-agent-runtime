// MCP 管理 API 的前端类型（对齐 backend/internal/mcp/config 与 mcp/admin 的 JSON 形状）。
//
// 后端契约（runtime-server，经 vite 代理到 /api）：
//   GET    /api/runtime/mcps                  → { count, mcps: [{ config, status }] }
//   POST   /api/runtime/mcps                  → 201 { config, status }
//   PUT    /api/runtime/mcps/{name}           → 200 { config, status }
//   DELETE /api/runtime/mcps/{name}           → 200 { name, removed }
//   POST   /api/runtime/mcps/{name}/enable    → 200 { config, status }
//   POST   /api/runtime/mcps/{name}/disable   → 200 { config, status }
//   POST   /api/runtime/mcps/reload           → 200 { reloaded, ... }
//   GET    /api/runtime/mcps/{name}/tools     → 200 { name, count, tools: [{ name, description, enabled, configured_enabled, healthy, inputSchema }] }
//   POST   /api/runtime/mcps/{name}/tools/{tool}/enable  → 200 { name, tool, enabled }
//   POST   /api/runtime/mcps/{name}/tools/{tool}/disable → 200 { name, tool, enabled }
//   POST   /api/runtime/mcps/{name}/tools/enable  body { tools: [] } → 200 { name, enabled, tools }
//   POST   /api/runtime/mcps/{name}/tools/disable body { tools: [] } → 200 { name, enabled, tools }
// 错误：非 2xx + { error: { code, message } }（400 校验失败 / 404 不存在 / 403 权限策略）。
// 工具清单：未启用 / 未连接时 tools 为空数组（不是错误）；404 表示该 MCP 不存在。

/** MCP 传输类型；后端另接受 streamableHttp 等别名，但配置回显以这四个为准。 */
export type RuntimeMcpTransportType =
  | "stdio"
  | "sse"
  | "websocket"
  | "streamable";

/** MCP 信任级别；缺省时后端按传输类型推导（stdio → local，其余 → untrusted_remote）。 */
export type RuntimeMcpTrustLevel =
  | "local"
  | "trusted_remote"
  | "untrusted_remote";

/** MCP 健康检查配置（MCPConfig.healthCheck 的结构化部分）。 */
export type RuntimeMcpHealthCheckConfig = {
  tools?: string[];
  toolArgs?: Record<string, Record<string, unknown>>;
  resources?: string[];
};

/** 单个 MCP Server 的配置（MCPConfig JSON）。 */
export type RuntimeMcpConfig = {
  name: string;
  description?: string;
  type: RuntimeMcpTransportType | string;
  trustLevel?: RuntimeMcpTrustLevel | string;
  maxParallelCalls?: number;
  /** stdio 传输的启动命令。 */
  command?: string;
  args?: string[];
  /** sse / websocket / streamable 传输的连接 URL。 */
  url?: string;
  env?: Record<string, string>;
  enabled?: boolean;
  /** 与 enabled 相反，兼容 MCP 官方格式；两者同时出现时 disabled 优先。 */
  disabled?: boolean;
  /** Go duration 字符串，如 "30s"。 */
  timeout?: string;
  maxRetry?: number;
  healthCheck?: RuntimeMcpHealthCheckConfig;
};

/** 单个 MCP Server 的运行时状态（MCPStatus JSON）。 */
export type RuntimeMcpStatus = {
  name: string;
  type: RuntimeMcpTransportType | string;
  trustLevel?: RuntimeMcpTrustLevel | string;
  executionMode?: string;
  maxParallelCalls?: number;
  enabled: boolean;
  connected: boolean;
  toolCount: number;
  lastError?: string;
  /** RFC3339 时间；零值在后端被 omitempty 省略。 */
  lastConnect?: string;
  healthCheck?: string;
};

/** 列表条目：配置 + 状态。 */
export type RuntimeMcpEntry = {
  config: RuntimeMcpConfig;
  status: RuntimeMcpStatus;
};

/** 单个候选配置文件（后端按优先级从高到低给出）。 */
export type RuntimeMcpConfigCandidate = {
  path: string;
  source: string;
  exists: boolean;
};

/** GET /api/runtime/mcps 附带的解析诊断（等价后端 mcpadmin.ConfigDiagnostics）。 */
export type RuntimeMcpConfigDiagnostics = {
  /** 生效的配置文件路径（管理面读写与重载都锚定它）。 */
  path: string;
  /** 命中的优先级层（explicit/local/project/user/upward/executable/default/user-fallback）。 */
  source?: string;
  exists: boolean;
  size_bytes?: number;
  mod_time?: string;
  /** 运行中的 manager 是否已加载该文件。 */
  manager_loaded: boolean;
  candidates?: RuntimeMcpConfigCandidate[];
};

/** 列表汇总（等价后端 mcpadmin.Summarize）。 */
export type RuntimeMcpListSummary = {
  total: number;
  enabled: number;
  disabled: number;
  connected: number;
  tools: number;
};

export type RuntimeMcpListResponse = {
  count: number;
  mcps: RuntimeMcpEntry[];
  /** 配置解析诊断；旧后端省略。 */
  config?: RuntimeMcpConfigDiagnostics;
  /** 汇总统计；旧后端省略。 */
  summary?: RuntimeMcpListSummary;
};

/**
 * 新增/更新请求（UpsertRequest）。
 *
 * 除 name/type 外全部可选：nil / 缺省字段表示「保持原值」，新增时由后端填默认值。
 * stdio 用 command/args/env；sse/websocket/streamable 用 url/headers。
 */
export type RuntimeMcpUpsertRequest = {
  name: string;
  type: RuntimeMcpTransportType;
  description?: string;
  command?: string;
  args?: string[];
  url?: string;
  env?: Record<string, string>;
  headers?: Record<string, string>;
  enabled?: boolean;
  trustLevel?: RuntimeMcpTrustLevel;
  timeoutSeconds?: number;
  maxParallelCalls?: number;
};

export type RuntimeMcpUpsertResponse = {
  config: RuntimeMcpConfig;
  status: RuntimeMcpStatus;
};

export type RuntimeMcpDeleteResponse = {
  name: string;
  removed: boolean;
};

export type RuntimeMcpReloadResponse = {
  reloaded: boolean;
  count?: number;
  mcps?: RuntimeMcpEntry[];
  errors?: string[];
};

/** 单个工具条目（GET /api/runtime/mcps/{name}/tools）。 */
export type RuntimeMcpTool = {
  name: string;
  description?: string;
  /** 有效暴露状态：configured_enabled 为 true 且 MCP 已启用/连接（后端计算）。 */
  enabled: boolean;
  /** 用户配置的启用状态；旧后端可能省略（此时以 enabled 兜底）。 */
  configured_enabled?: boolean;
  /** 运行时健康：false 表示工具当前不可调用（未连接 / 健康检查失败）。 */
  healthy?: boolean;
  /** 工具入参 JSON Schema；未声明时后端省略该字段。 */
  inputSchema?: unknown;
};

export type RuntimeMcpToolsResponse = {
  name: string;
  count: number;
  tools: RuntimeMcpTool[];
};

/**
 * 会话工具清单（GET /api/runtime/sessions/{id}/runtime/mcps/{name}/tools）。
 *
 * 与全局工具清单同形；scope=workspace 表示清单来自会话工作区配置链的 manager
 * （工作区私有 server 也能取到）。
 */
export type RuntimeSessionMcpToolsResponse = RuntimeMcpToolsResponse & {
  session_id: string;
  scope?: "workspace" | "global";
};

/** 单个工具启用/停用响应（POST /api/runtime/mcps/{name}/tools/{tool}/enable|disable）。 */
export type RuntimeMcpToolToggleResponse = {
  name: string;
  tool: string;
  /** 操作后的用户配置启用状态。 */
  enabled: boolean;
};

/**
 * 批量启用/停用响应（POST /api/runtime/mcps/{name}/tools/enable|disable）。
 *
 * 请求体 `{ tools: [...] }`；`tools` 缺省或空数组表示作用于全部工具，
 * 响应里的 `tools` 回显本次实际作用的工具名单。
 */
export type RuntimeMcpToolsBulkToggleResponse = {
  name: string;
  enabled: boolean;
  tools: string[];
};

/** 会话 MCP 配置面：读/写目标与工作区锚定信息（GET 会话 MCP 列表附带）。 */
export type RuntimeSessionMcpScope = {
  /** 会话绑定的工作区目录；空串 = 未绑定（读写都走进程级）。 */
  workspace: string;
  /** 写目标是会话工作区锚定的配置文件。 */
  workspace_scoped: boolean;
  /** 当前生效面回退到进程级（工作区还没有配置文件）。 */
  workspace_fallback: boolean;
  read: { path: string; source: string; exists: boolean };
  write: { path: string; source: string; exists: boolean };
  candidates?: RuntimeMcpConfigCandidate[];
};

/** 会话 MCP 列表条目：配置/状态 + 来源 + 本会话覆盖标记。 */
export type RuntimeSessionMcpEntry = RuntimeMcpEntry & {
  source?: "workspace" | "global";
  session_disabled?: boolean;
  /** 配置里停用、但被本会话临时启用（内存连接，不写配置文件）。 */
  session_enabled?: boolean;
};

/**
 * 会话 MCP 列表（GET /api/runtime/sessions/{id}/runtime/mcps）。
 *
 * `mcps`/`scope`/`summary` 是 2026-10 增强字段（会话实际生效的工作区配置面）；
 * 旧后端只返回 disabled/count，前端按「全局面回退」渲染。
 */
export type RuntimeSessionMcpScopeResponse = {
  session_id: string;
  /** 本会话停用的 MCP server 名；空数组 = 全部全局面。 */
  disabled: string[];
  /** 本会话临时启用的 MCP server 名（配置停用 + 会话私有连接）。 */
  enabled?: string[];
  count: number;
  scope?: RuntimeSessionMcpScope;
  mcps?: RuntimeSessionMcpEntry[];
  summary?: RuntimeMcpListSummary;
};

/**
 * 会话级启停响应
 * （POST /api/runtime/sessions/{id}/runtime/mcps/{name}/enable|disable）。
 *
 * 不写配置文件；enable 对全局停用的 server 返回 409（暂不支持会话级临时连接）。
 */
export type RuntimeSessionMcpToggleResponse = {
  session_id: string;
  name: string;
  enabled: boolean;
  /** session = 仅本会话覆盖；workspace/global = 持久化写入对应配置文件。 */
  scope: "session" | "workspace" | "global";
  /** 仅 scope=session 时返回："disabled" / "enabled"（临时连接）/ ""（恢复全局面）。 */
  session_state?: string;
  changed: boolean;
  message: string;
  /** 持久化启停写入的文件（scope=workspace/global 时返回）。 */
  config_path?: string;
};
