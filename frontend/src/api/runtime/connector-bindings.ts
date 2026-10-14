// 连接器绑定客户端（docs/bots/09-multi-instance-binding.md §5.2）：
// GET /connectors/bindings + PUT/DELETE /connectors/{id}/bindings/{chat}。
//
// 从 api/runtime/connectors.ts 拆出（P0-2 行数门禁）；鉴权/URL 归一化复用
// connectors.ts 的内部助手（经显式参数传入，避免形成循环依赖）。
// 语义：bind 是覆盖式指针切换（乐观锁 expected_updated_at）；unbind 写零值墓碑
//（等价 IM 内 /new）。

import type {
  ConnectorBindingView,
} from "@/types/runtime";

import {
  asRecord,
  buildRuntimeUrl,
  fetchRuntimeJson,
  readString,
  RuntimeApiError,
} from "./shared";

/** 绑定管理面的请求选项（与 connectors 客户端同一鉴权口径）。 */
export interface ConnectorBindingRequestOptions {
  adminToken?: string;
  signal?: AbortSignal;
}

/** 组装绑定端点的请求头（与 connectors.ts buildHeaders 同口径）。 */
function buildHeaders(
  options: ConnectorBindingRequestOptions,
  json: boolean,
): Record<string, string> {
  const headers: Record<string, string> = {};
  const token = options.adminToken?.trim();
  if (token) {
    headers["X-Skills-Admin-Token"] = token;
  }
  if (json) {
    headers["Content-Type"] = "application/json";
  }
  return headers;
}

function requestInit(
  options: ConnectorBindingRequestOptions,
  method: "PUT" | "DELETE",
  body?: Record<string, unknown>,
): RequestInit {
  return {
    method,
    headers: buildHeaders(options, body !== undefined),
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
    ...(options.signal ? { signal: options.signal } : {}),
  };
}

/**
 * 列出连接器 ↔ 会话绑定；connectorId/chat 可选过滤。
 * items 按归一化投影（缺 id/chat 的条目丢弃）。
 */
export async function listConnectorBindings(
  options: ConnectorBindingRequestOptions & { connectorId?: string; chat?: string } = {},
): Promise<ConnectorBindingView[]> {
  const query = new URLSearchParams();
  if (options.connectorId?.trim()) {
    query.set("connector_id", options.connectorId.trim());
  }
  if (options.chat?.trim()) {
    query.set("chat", options.chat.trim());
  }
  const suffix = query.toString() ? `?${query.toString()}` : "";
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(`/api/connectors/bindings${suffix}`),
    {
      headers: buildHeaders(options, false),
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  return normalizeConnectorBindings(payload);
}

/**
 * 绑定会话到连接器（覆盖式指针切换；expectedUpdatedAt 传当前 updated_at 可做
 * 乐观锁）。错误语义：404 未知连接器 / 400 参数 / 409 乐观锁未命中 / 403 白名单。
 */
export async function bindConnectorSession(
  id: string,
  chat: string,
  request: { sessionId: string; nodeId?: string; expectedUpdatedAt?: string },
  options: ConnectorBindingRequestOptions = {},
): Promise<ConnectorBindingView> {
  return mutateConnectorBinding(id, chat, options, {
    session_id: request.sessionId.trim(),
    ...(request.nodeId?.trim() ? { node_id: request.nodeId.trim() } : {}),
    ...(request.expectedUpdatedAt?.trim()
      ? { expected_updated_at: request.expectedUpdatedAt.trim() }
      : {}),
  }, "PUT");
}

/** 解绑（写零值墓碑，语义等价 IM 内 /new）。 */
export async function unbindConnectorSession(
  id: string,
  chat: string,
  options: ConnectorBindingRequestOptions = {},
): Promise<ConnectorBindingView> {
  return mutateConnectorBinding(id, chat, options, undefined, "DELETE");
}

async function mutateConnectorBinding(
  id: string,
  chat: string,
  options: ConnectorBindingRequestOptions,
  body: Record<string, unknown> | undefined,
  method: "PUT" | "DELETE",
): Promise<ConnectorBindingView> {
  const target = chat.trim();
  if (!target) {
    throw new Error("connector binding requires a chat");
  }
  const response = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(
      `/api/connectors/${encodeURIComponent(id.trim())}/bindings/${encodeURIComponent(target)}`,
    ),
    requestInit(options, method, body),
  );
  const view = normalizeConnectorBinding(response);
  if (!view) {
    throw new Error("runtime connector binding payload is missing `connector_id`");
  }
  return view;
}

export function normalizeConnectorBindings(payload: unknown): ConnectorBindingView[] {
  const record = asRecord(payload);
  if (!record || !Array.isArray(record.items)) {
    throw new Error("runtime connector bindings payload is missing `items`");
  }
  return record.items.flatMap((item) => {
    const view = normalizeConnectorBinding(item);
    return view ? [view] : [];
  });
}

function normalizeConnectorBinding(payload: unknown): ConnectorBindingView | null {
  const record = asRecord(payload);
  const connectorId = readString(record?.connector_id);
  const chat = readString(record?.chat);
  if (!record || !connectorId || !chat) {
    return null;
  }
  const connectorRecord = asRecord(record.connector);
  return {
    connector_id: connectorId,
    chat,
    session_id: readString(record.session_id) || "",
    node_id: readString(record.node_id) || undefined,
    updated_at: readString(record.updated_at) || undefined,
    connector: connectorRecord
      ? {
          id: readString(connectorRecord.id) || connectorId,
          name: readString(connectorRecord.name) || connectorId,
          provider: readString(connectorRecord.provider) || "",
          enabled: connectorRecord.enabled === true,
          active_owner: readString(connectorRecord.active_owner) || undefined,
        }
      : undefined,
  };
}

// RuntimeApiError 由 fetchRuntimeJson 抛出（409/403 语义见 §5.2）；这里显式引用
// 以保证错误类型在 d.ts 中可达。
export type { RuntimeApiError };
