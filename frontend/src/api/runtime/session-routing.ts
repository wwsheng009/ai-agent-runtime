// 会话路由 API 客户端：GET/PATCH `/api/runtime/sessions/{id}/routing`。
//
// 语义边界（I-6 / 方案 §10.2）：本模块**只做形状归一化**——缺字段时给空值，
// 不推导 provider/model/effort，不猜 source，也不改写 warnings 文案；
// 面板因此永远展示后端投影的原始口径（与 TUI `/routing show` 一致）。
//
// 写入路径：`target_layer` 决定落盘位置（session=会话记录覆盖 / workspace=
// 工作区 chat-prefs.yaml / config=全局配置），config 层需 `confirm=true` 二次确认。
// 后端鉴权：回环地址、admin token 或 admin 角色（authorizeSessionRoutingWrite）。

import type {
  RoutingLevelSummary,
  RoutingPanelMetadata,
  RoutingStatusProjection,
  SessionRoutingPatchRequest,
  SessionRoutingResponse,
} from "@/types/runtime";

import { buildRuntimeUrl, fetchRuntimeJson } from "./shared";

export type SessionRoutingRequestOptions = {
  /** 管理令牌；面板从 localStorage 读取后透传（与 usage 面板同一存储键）。 */
  adminToken?: string;
  signal?: AbortSignal;
};

function buildRequestHeaders(
  options: SessionRoutingRequestOptions,
  withBody: boolean,
): Record<string, string> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (withBody) {
    headers["Content-Type"] = "application/json";
  }
  const token = options.adminToken?.trim();
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  return headers;
}

function sessionRoutingPath(sessionId: string) {
  return `/api/runtime/sessions/${encodeURIComponent(sessionId)}/routing`;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function pickText(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function pickBool(value: unknown): boolean {
  return value === true;
}

function pickNumber(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function pickStringArray(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : [];
}

/** 归一化 §6.1 投影：字段名与后端 JSON 一一对应（snake_case → camelCase）。 */
export function normalizeRoutingStatusProjection(
  raw: unknown,
): RoutingStatusProjection {
  const source = isRecord(raw) ? raw : {};
  return {
    schemaVersion: pickNumber(source.schema_version),
    enabled: pickBool(source.enabled),
    level: pickText(source.level),
    provider: pickText(source.provider),
    model: pickText(source.model),
    reasoning: pickText(source.reasoning),
    source: pickText(source.source),
    disabled: pickBool(source.disabled),
    warnings: pickStringArray(source.warnings),
    revision: pickText(source.revision),
    effectiveFrom: pickText(source.effective_from),
  };
}

/** 归一化逐级表格行；`enabled` 缺失按 false 如实呈现（不臆造「已启用」）。 */
export function normalizeRoutingLevelSummary(raw: unknown): RoutingLevelSummary {
  const source = isRecord(raw) ? raw : {};
  return {
    level: pickText(source.level),
    enabled: pickBool(source.enabled),
    provider: pickText(source.provider),
    model: pickText(source.model),
    reasoning: pickText(source.reasoning),
    source: pickText(source.source),
    expensive: pickBool(source.expensive),
  };
}

/**
 * 归一化面板元数据。
 *
 * `writable_layers` 缺失/为空时**不做兜底推断**：调用方按「不可写」置灰该层，
 * 避免在后端尚未支持某层写入时给出可点击但必然失败的入口。
 */
export function normalizeRoutingPanelMetadata(raw: unknown): RoutingPanelMetadata {
  const source = isRecord(raw) ? raw : {};
  const levels = Array.isArray(source.levels) ? source.levels : [];
  return {
    scope: pickText(source.scope) === "sub" ? "sub" : "main",
    childSession: pickBool(source.child_session),
    sessionOverride: pickBool(source.session_override),
    workspaceOverride: pickBool(source.workspace_override),
    configOverride: pickBool(source.config_override),
    workspacePath: pickText(source.workspace_path),
    workspacePrefsPath: pickText(source.workspace_prefs_path),
    configPath: pickText(source.config_path),
    configLayer: pickText(source.config_layer),
    writableLayers: pickStringArray(source.writable_layers).map((layer) =>
      layer.trim().toLowerCase(),
    ),
    levels: levels.map(normalizeRoutingLevelSummary),
    subAgent: isRecord(source.sub_agent)
      ? normalizeRoutingStatusProjection(source.sub_agent)
      : null,
  };
}

/** 归一化 GET/PATCH 响应外壳。 */
export function normalizeSessionRoutingResponse(
  raw: unknown,
): SessionRoutingResponse {
  const source = isRecord(raw) ? raw : {};
  return {
    sessionId: pickText(source.session_id),
    scope: pickText(source.scope),
    targetLayer: pickText(source.target_layer),
    targetPath: pickText(source.target_path),
    actorInvalidated: pickBool(source.actor_invalidated),
    updated: pickBool(source.updated),
    routing: normalizeRoutingStatusProjection(source.routing),
    subAgent: normalizeRoutingStatusProjection(source.sub_agent),
    panel: normalizeRoutingPanelMetadata(source.panel),
    warnings: pickStringArray(source.warnings),
  };
}

/** 读取会话路由投影与面板元数据（只读，不落盘）。 */
export async function getSessionRouting(
  sessionId: string,
  options: SessionRoutingRequestOptions = {},
): Promise<SessionRoutingResponse> {
  const raw = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(sessionRoutingPath(sessionId)),
    {
      method: "GET",
      headers: buildRequestHeaders(options, false),
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  return normalizeSessionRoutingResponse(raw);
}

/**
 * 写入会话路由补丁。
 *
 * `target_layer` 必填（后端缺省按 session 处理，但面板始终显式传层，避免歧义）；
 * `clear=true` 表示清除该层覆盖（此时 `main_agent`/`clear_fields` 无意义）；
 * config 层必须带 `confirm=true`，否则后端 400（二次确认由 UI 负责）。
 */
export async function updateSessionRouting(
  sessionId: string,
  patch: SessionRoutingPatchRequest,
  options: SessionRoutingRequestOptions = {},
): Promise<SessionRoutingResponse> {
  const raw = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(sessionRoutingPath(sessionId)),
    {
      method: "PATCH",
      headers: buildRequestHeaders(options, true),
      body: JSON.stringify(patch),
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  return normalizeSessionRoutingResponse(raw);
}
