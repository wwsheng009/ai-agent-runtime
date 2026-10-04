// MCP 管理客户端（/api/runtime/mcps）。
//
// 错误统一由 shared.fetchRuntimeJson 抛 RuntimeApiError（含 HTTP status 与
// { error: { code, message } } 信封），调用方可用 isRuntimeApiErrorCode 按
// 400/404/403 分支降级；这里不吞错、不改写后端 code。

import type {
  RuntimeMcpDeleteResponse,
  RuntimeMcpEntry,
  RuntimeMcpListResponse,
  RuntimeMcpReloadResponse,
  RuntimeMcpTool,
  RuntimeMcpToolsBulkToggleResponse,
  RuntimeMcpToolsResponse,
  RuntimeMcpToolToggleResponse,
  RuntimeMcpUpsertRequest,
  RuntimeMcpUpsertResponse,
  RuntimeSessionMcpScopeResponse,
  RuntimeSessionMcpToolsResponse,
  RuntimeSessionMcpToggleResponse,
} from "@/types/runtime";

import { buildRuntimeUrl, fetchRuntimeJson } from "./shared";

const runtimeMcpsUrl = buildRuntimeUrl("/api/runtime/mcps");

/** 单个 MCP 的资源 URL；name 可能含空格/斜杠，必须整段编码。 */
export function buildRuntimeMcpUrl(name: string) {
  return `${runtimeMcpsUrl}/${encodeURIComponent(name)}`;
}

export function buildRuntimeMcpEnableUrl(name: string) {
  return `${buildRuntimeMcpUrl(name)}/enable`;
}

export function buildRuntimeMcpDisableUrl(name: string) {
  return `${buildRuntimeMcpUrl(name)}/disable`;
}

export function buildRuntimeMcpReloadUrl() {
  return `${runtimeMcpsUrl}/reload`;
}

export function buildRuntimeMcpToolsUrl(name: string) {
  return `${buildRuntimeMcpUrl(name)}/tools`;
}

/** 单个工具的 URL；tool 名可能含空格/斜杠，必须整段编码（与 MCP name 同纪律）。 */
export function buildRuntimeMcpToolUrl(name: string, tool: string) {
  return `${buildRuntimeMcpToolsUrl(name)}/${encodeURIComponent(tool)}`;
}

export function buildRuntimeMcpToolEnableUrl(name: string, tool: string) {
  return `${buildRuntimeMcpToolUrl(name, tool)}/enable`;
}

export function buildRuntimeMcpToolDisableUrl(name: string, tool: string) {
  return `${buildRuntimeMcpToolUrl(name, tool)}/disable`;
}

export function buildRuntimeMcpToolsEnableUrl(name: string) {
  return `${buildRuntimeMcpToolsUrl(name)}/enable`;
}

export function buildRuntimeMcpToolsDisableUrl(name: string) {
  return `${buildRuntimeMcpToolsUrl(name)}/disable`;
}

/**
 * 列表归一化：`mcps` 缺失或不是数组时抛错，而不是降级成空列表——
 * 否则后端契约回归会被 UI 伪装成「尚未配置 MCP」，问题无法被发现。
 */
function normalizeMcpListResponse(
  payload: RuntimeMcpListResponse | null | undefined,
): RuntimeMcpListResponse {
  if (!payload || !Array.isArray(payload.mcps)) {
    throw new Error("invalid runtime MCP list payload: mcps must be an array");
  }
  const mcps = payload.mcps.filter(
    (entry): entry is RuntimeMcpEntry =>
      Boolean(entry && typeof entry === "object" && entry.config),
  );
  return {
    count: typeof payload.count === "number" ? payload.count : mcps.length,
    mcps,
  };
}

export async function listRuntimeMcps() {
  const payload = await fetchRuntimeJson<RuntimeMcpListResponse>(runtimeMcpsUrl);
  return normalizeMcpListResponse(payload);
}

export async function createRuntimeMcp(request: RuntimeMcpUpsertRequest) {
  return fetchRuntimeJson<RuntimeMcpUpsertResponse>(runtimeMcpsUrl, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(request),
  });
}

export async function updateRuntimeMcp(
  name: string,
  request: RuntimeMcpUpsertRequest,
) {
  return fetchRuntimeJson<RuntimeMcpUpsertResponse>(buildRuntimeMcpUrl(name), {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(request),
  });
}

export async function deleteRuntimeMcp(name: string) {
  const payload = await fetchRuntimeJson<RuntimeMcpDeleteResponse>(
    buildRuntimeMcpUrl(name),
    { method: "DELETE" },
  );
  return {
    name: typeof payload?.name === "string" ? payload.name : name,
    removed: payload?.removed === true,
  } satisfies RuntimeMcpDeleteResponse;
}

export async function setRuntimeMcpEnabled(name: string, enabled: boolean) {
  return fetchRuntimeJson<RuntimeMcpUpsertResponse>(
    enabled ? buildRuntimeMcpEnableUrl(name) : buildRuntimeMcpDisableUrl(name),
    { method: "POST" },
  );
}

export async function reloadRuntimeMcps() {
  return fetchRuntimeJson<RuntimeMcpReloadResponse>(buildRuntimeMcpReloadUrl(), {
    method: "POST",
  });
}

/**
 * 工具清单归一化：`tools` 缺失或不是数组时抛错，而不是降级成空列表——
 * 与列表同纪律，缺数据不能被 UI 伪装成「该 MCP 没有工具」。
 * 空数组是合法结果（未启用 / 未连接 / 尚未完成 tools/list 握手）。
 */
function normalizeMcpToolsResponse(
  payload: RuntimeMcpToolsResponse | null | undefined,
  fallbackName: string,
): RuntimeMcpToolsResponse {
  if (!payload || !Array.isArray(payload.tools)) {
    throw new Error("invalid runtime MCP tools payload: tools must be an array");
  }
  const tools = payload.tools.filter(
    (tool): tool is RuntimeMcpTool =>
      Boolean(tool && typeof tool === "object" && typeof tool.name === "string"),
  );
  return {
    name: typeof payload.name === "string" ? payload.name : fallbackName,
    count: typeof payload.count === "number" ? payload.count : tools.length,
    tools,
  };
}

export async function listRuntimeMcpTools(name: string) {
  const payload = await fetchRuntimeJson<RuntimeMcpToolsResponse>(
    buildRuntimeMcpToolsUrl(name),
  );
  return normalizeMcpToolsResponse(payload, name);
}

/**
 * 会话工具清单：读会话实际生效的 manager（工作区锚定时即工作区配置链实例），
 * 工作区私有 server 也能取到；未启用 / 未连接时 tools 为空数组。
 */
export async function listRuntimeSessionMcpTools(
  sessionId: string,
  name: string,
) {
  const payload = await fetchRuntimeJson<RuntimeSessionMcpToolsResponse>(
    buildRuntimeSessionMcpToolsUrl(sessionId, name),
  );
  return {
    ...normalizeMcpToolsResponse(payload, name),
    session_id:
      typeof payload?.session_id === "string" ? payload.session_id : sessionId,
    scope: payload?.scope,
  } satisfies RuntimeSessionMcpToolsResponse;
}

/**
 * 单个工具启用/停用：POST 到 /tools/{tool}/enable|disable，无请求体。
 * 返回新的用户配置状态；调用方通常随后重新拉取工具清单以拿到最新 enabled/healthy。
 */
export async function setRuntimeMcpToolEnabled(
  name: string,
  tool: string,
  enabled: boolean,
) {
  return fetchRuntimeJson<RuntimeMcpToolToggleResponse>(
    enabled
      ? buildRuntimeMcpToolEnableUrl(name, tool)
      : buildRuntimeMcpToolDisableUrl(name, tool),
    { method: "POST" },
  );
}

/**
 * 批量启用/停用：POST 到 /tools/enable|disable，body { tools }。
 *
 * tools 缺省或空数组在后端表示「全部工具」；这里始终显式发送数组（默认空），
 * 避免调用方把「省略数组」误读成「不操作」。
 */
export async function setRuntimeMcpToolsEnabled(
  name: string,
  enabled: boolean,
  tools: string[] = [],
) {
  return fetchRuntimeJson<RuntimeMcpToolsBulkToggleResponse>(
    enabled
      ? buildRuntimeMcpToolsEnableUrl(name)
      : buildRuntimeMcpToolsDisableUrl(name),
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ tools }),
    },
  );
}

// ---- 会话级覆盖（不写配置文件；与 CLI `--session` / chat web `?scope=session` 同语义） ----

/** 会话级 MCP 覆盖 URL；sessionId 可能含空格/斜杠，必须整段编码。 */
export function buildRuntimeSessionMcpsUrl(sessionId: string) {
  return buildRuntimeUrl(
    `/api/runtime/sessions/${encodeURIComponent(sessionId)}/runtime/mcps`,
  );
}

/** 单个 server 的会话级资源 URL；name 与 sessionId 同纪律整段编码。 */
export function buildRuntimeSessionMcpUrl(sessionId: string, name: string) {
  return `${buildRuntimeSessionMcpsUrl(sessionId)}/${encodeURIComponent(name)}`;
}

export function buildRuntimeSessionMcpEnableUrl(
  sessionId: string,
  name: string,
) {
  return `${buildRuntimeSessionMcpUrl(sessionId, name)}/enable`;
}

export function buildRuntimeSessionMcpDisableUrl(
  sessionId: string,
  name: string,
) {
  return `${buildRuntimeSessionMcpUrl(sessionId, name)}/disable`;
}

/** 会话工具清单 URL（会话生效 manager，工作区锚定）。 */
export function buildRuntimeSessionMcpToolsUrl(sessionId: string, name: string) {
  return `${buildRuntimeSessionMcpUrl(sessionId, name)}/tools`;
}

/**
 * 会话级覆盖清单归一化：`disabled` 缺失或不是数组时抛错——
 * 与全局列表同纪律，契约回归不能被 UI 伪装成「无覆盖」。
 */
export async function listRuntimeSessionMcps(sessionId: string) {
  const payload = await fetchRuntimeJson<RuntimeSessionMcpScopeResponse>(
    buildRuntimeSessionMcpsUrl(sessionId),
  );
  if (!payload || !Array.isArray(payload.disabled)) {
    throw new Error(
      "invalid runtime session MCP payload: disabled must be an array",
    );
  }
  const disabled = payload.disabled.filter(
    (name): name is string => typeof name === "string" && name.trim() !== "",
  );
  const enabled = Array.isArray(payload.enabled)
    ? payload.enabled.filter(
        (name): name is string => typeof name === "string" && name.trim() !== "",
      )
    : undefined;
  const mcps = Array.isArray(payload.mcps)
    ? payload.mcps.filter(
        (entry): entry is NonNullable<typeof entry> =>
          Boolean(entry && typeof entry === "object" && entry.config),
      )
    : undefined;
  return {
    session_id:
      typeof payload.session_id === "string" ? payload.session_id : sessionId,
    disabled,
    count: typeof payload.count === "number" ? payload.count : disabled.length,
    ...(enabled ? { enabled } : {}),
    ...(payload.scope ? { scope: payload.scope } : {}),
    ...(mcps ? { mcps } : {}),
    ...(payload.summary ? { summary: payload.summary } : {}),
  } satisfies RuntimeSessionMcpScopeResponse;
}

/**
 * 会话级启停：POST 到 /sessions/{id}/runtime/mcps/{name}/enable|disable，无请求体。
 *
 * scope="session"（默认）只影响当前会话；enable 仅清除覆盖（停用的 server 由
 * 后端返回 409）。scope="workspace" 表示持久化写入会话生效的配置文件并热重载
 * （工作区锚定时写工作区 mcp.yaml，否则写进程级文件）。
 */
export async function setRuntimeSessionMcpEnabled(
  sessionId: string,
  name: string,
  enabled: boolean,
  scope: "session" | "workspace" = "session",
) {
  const base = enabled
    ? buildRuntimeSessionMcpEnableUrl(sessionId, name)
    : buildRuntimeSessionMcpDisableUrl(sessionId, name);
  const url = scope === "workspace" ? `${base}?scope=workspace` : base;
  return fetchRuntimeJson<RuntimeSessionMcpToggleResponse>(
    url,
    { method: "POST" },
  );
}

// ---- 会话配置文件管理（写工作区/进程级 mcp.yaml；后端 Add/Update/Remove/Reload） ----

/** 在会话生效的配置文件中新增 MCP。 */
export async function createRuntimeSessionMcp(
  sessionId: string,
  request: RuntimeMcpUpsertRequest,
) {
  return fetchRuntimeJson<RuntimeMcpUpsertResponse>(
    buildRuntimeSessionMcpsUrl(sessionId),
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(request),
    },
  );
}

/** 更新会话配置文件中的 MCP。 */
export async function updateRuntimeSessionMcp(
  sessionId: string,
  name: string,
  request: RuntimeMcpUpsertRequest,
) {
  return fetchRuntimeJson<RuntimeMcpUpsertResponse>(
    buildRuntimeSessionMcpUrl(sessionId, name),
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(request),
    },
  );
}

/** 删除会话配置文件中的 MCP。 */
export async function deleteRuntimeSessionMcp(
  sessionId: string,
  name: string,
) {
  return fetchRuntimeJson<{ name: string; removed: boolean }>(
    buildRuntimeSessionMcpUrl(sessionId, name),
    { method: "DELETE" },
  );
}

/** 热重载会话生效的配置文件（工作区 manager 原地重连）。 */
export async function reloadRuntimeSessionMcps(sessionId: string) {
  return fetchRuntimeJson<{
    session_id: string;
    reloaded: boolean;
    scope: string;
    path: string;
  }>(`${buildRuntimeSessionMcpsUrl(sessionId)}/reload`, { method: "POST" });
}
