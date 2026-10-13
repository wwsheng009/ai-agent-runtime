// IM 连接器管理面客户端（docs/bots/03-host-integration.md §6 路由表）。
//
// 端点由 backend/internal/connectors/restapi 提供，runtime-server 挂载为 /api/connectors*：
//   GET    /api/connectors             → {items: ConnectorView[]}
//   POST   /api/connectors             → 201 ConnectorView（校验失败 400 {error, fields[]}）
//   GET    /api/connectors/schema      → {version, providers[]}
//   GET    /api/connectors/{id}        → ConnectorView
//   PATCH  /api/connectors/{id}        → 200 ConnectorView
//   DELETE /api/connectors/{id}        → 204
//   POST   /api/connectors/{id}/reload → 202 {ok:true}
//   POST   /api/connectors/{id}/test   → 200 {ok:true}（body {chat,text}）
//
// 鉴权与 skills 管理面同口径（handler.go hasValidSearchAdminToken）：
// `X-Skills-Admin-Token`、或 `Authorization: Bearer <token>`、或环回直连。
//
// 归一化纪律（与 skills 客户端一致）：
//   * 核心结构缺失（`items` / `providers` 非数组）→ 抛错，不降级成「空列表」；
//   * 单条缺 id / provider → 只丢该条，其余照常呈现；
//   * `enabled` 缺失按 false（安全默认），`policy` 缺失按空对象；
//   * 密钥只透传 `secret_keys` 字段名，前端不臆造明文。

import type {
  ConnectorFieldError,
  ConnectorSchema,
  ConnectorSubmitPayload,
  ConnectorView,
} from "@/types/runtime";

import {
  RuntimeApiError,
  RUNTIME_FETCH_TIMEOUT_MS,
  buildRuntimeUrl,
  fetchRuntimeJson,
  parseErrorPayload,
} from "./shared";

export const CONNECTORS_PATH = "/api/connectors";
export const CONNECTORS_SCHEMA_PATH = `${CONNECTORS_PATH}/schema`;
/** 与 skills 管理面共用的写操作鉴权头。 */
export const CONNECTORS_ADMIN_TOKEN_HEADER = "X-Skills-Admin-Token";
/** 显式清除某个密钥的提交值（UI 的「清除」按钮产生）。 */
export const CONNECTOR_CLEAR_SECRET = "__clear__";

export interface ConnectorRequestOptions {
  adminToken?: string;
  signal?: AbortSignal;
}

export interface ConnectorTestMessageRequest {
  /** 目标语言会话（平台 chat id）。 */
  chat: string;
  text?: string;
}

function buildHeaders(
  options: ConnectorRequestOptions,
  json: boolean,
): Record<string, string> {
  const headers: Record<string, string> = {};
  const token = options.adminToken?.trim();
  if (token) {
    headers[CONNECTORS_ADMIN_TOKEN_HEADER] = token;
  }
  if (json) {
    headers["Content-Type"] = "application/json";
  }
  return headers;
}

function requestInit(
  options: ConnectorRequestOptions,
  method: string,
  body?: unknown,
): RequestInit {
  return {
    method,
    headers: buildHeaders(options, body !== undefined),
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
    ...(options.signal ? { signal: options.signal } : {}),
  };
}

function readOptionsInit(
  options: ConnectorRequestOptions,
): RequestInit {
  return {
    headers: buildHeaders(options, false),
    ...(options.signal ? { signal: options.signal } : {}),
  };
}

function connectorPath(id: string): string {
  return `${CONNECTORS_PATH}/${encodeURIComponent(id.trim())}`;
}

/** 无响应体请求（DELETE 204 / POST 202）：非 2xx 时保持与 fetchRuntimeJson 相同的错误语义。 */
async function performVoidRequest(url: string, init: RequestInit): Promise<void> {
  const response = await fetch(url, {
    ...init,
    signal: init.signal ?? AbortSignal.timeout(RUNTIME_FETCH_TIMEOUT_MS),
  });
  if (!response.ok) {
    const payload = await parseErrorPayload(response);
    throw new RuntimeApiError(response.status, payload);
  }
}

/** 列出全部连接器（含已停用项）。 */
export async function listConnectors(
  options: ConnectorRequestOptions = {},
): Promise<ConnectorView[]> {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(CONNECTORS_PATH),
    readOptionsInit(options),
  );
  return normalizeConnectorList(payload);
}

/** 读取三端共用的 Schema（渲染器的唯一输入）。 */
export async function getConnectorSchema(
  options: ConnectorRequestOptions = {},
): Promise<ConnectorSchema> {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(CONNECTORS_SCHEMA_PATH),
    readOptionsInit(options),
  );
  return normalizeConnectorSchema(payload);
}

/** 新建连接器；字段级校验失败时抛 400 RuntimeApiError（fields 见 readConnectorFieldErrors）。 */
export async function createConnector(
  payload: ConnectorSubmitPayload,
  options: ConnectorRequestOptions = {},
): Promise<ConnectorView> {
  const response = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(CONNECTORS_PATH),
    requestInit(options, "POST", payload),
  );
  const view = normalizeConnectorView(response);
  if (!view) {
    throw new Error("runtime connector payload is missing `id`");
  }
  return view;
}

/** 更新连接器（PATCH 部分更新：缺省字段保持不变，密钥留空=不改）。 */
export async function updateConnector(
  id: string,
  payload: ConnectorSubmitPayload,
  options: ConnectorRequestOptions = {},
): Promise<ConnectorView> {
  const response = await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(connectorPath(id)),
    requestInit(options, "PATCH", payload),
  );
  const view = normalizeConnectorView(response);
  if (!view) {
    throw new Error("runtime connector payload is missing `id`");
  }
  return view;
}

/** 删除连接器（宿主会先停轮询，再级联清理绑定与游标）。 */
export async function deleteConnector(
  id: string,
  options: ConnectorRequestOptions = {},
): Promise<void> {
  await performVoidRequest(
    buildRuntimeUrl(connectorPath(id)),
    requestInit(options, "DELETE"),
  );
}

/** 应用配置变更（重启轮询 / 重连 / 重注册 webhook）。 */
export async function reloadConnector(
  id: string,
  options: ConnectorRequestOptions = {},
): Promise<void> {
  await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(`${connectorPath(id)}/reload`),
    requestInit(options, "POST"),
  );
}

/** 向指定 IM 会话发送测试消息。 */
export async function sendConnectorTestMessage(
  id: string,
  request: ConnectorTestMessageRequest,
  options: ConnectorRequestOptions = {},
): Promise<void> {
  const chat = request.chat.trim();
  if (!chat) {
    throw new Error("test message requires a chat id");
  }
  const text = request.text?.trim();
  await fetchRuntimeJson<unknown>(
    buildRuntimeUrl(`${connectorPath(id)}/test`),
    requestInit(options, "POST", text ? { chat, text } : { chat }),
  );
}

/** 从 400 校验错误体读出字段级错误（非该形状时返回空数组）。 */
export function readConnectorFieldErrors(error: unknown): ConnectorFieldError[] {
  if (!(error instanceof RuntimeApiError)) {
    return [];
  }
  const record = asRecord(error.payload);
  const fields = record?.fields;
  if (!Array.isArray(fields)) {
    return [];
  }
  return fields.flatMap((item) => {
    const entry = asRecord(item);
    const key = readString(entry?.key);
    if (!key) {
      return [];
    }
    return [{ key, message: readString(entry?.message) }];
  });
}

// ── 归一化 ────────────────────────────────────────────────────────────────

export function normalizeConnectorList(payload: unknown): ConnectorView[] {
  const record = asRecord(payload);
  if (!record) {
    throw new Error("runtime connectors payload is not an object");
  }
  if (!Array.isArray(record.items)) {
    throw new Error("runtime connectors payload is missing `items`");
  }
  return record.items.flatMap((item) => {
    const view = normalizeConnectorView(item);
    return view ? [view] : [];
  });
}

export function normalizeConnectorView(payload: unknown): ConnectorView | null {
  const record = asRecord(payload);
  if (!record) {
    return null;
  }
  const id = readString(record.id);
  const provider = readString(record.provider);
  if (!id || !provider) {
    return null;
  }
  return {
    id,
    provider,
    name: readString(record.name) || id,
    enabled: record.enabled === true,
    mode: readString(record.mode),
    settings: readStringMap(record.settings),
    secret_keys: readStringArray(record.secret_keys),
    policy: readPolicy(record.policy),
  };
}

export function normalizeConnectorSchema(payload: unknown): ConnectorSchema {
  const record = asRecord(payload);
  if (!record) {
    throw new Error("runtime connector schema payload is not an object");
  }
  if (!Array.isArray(record.providers)) {
    throw new Error("runtime connector schema payload is missing `providers`");
  }
  return {
    version: readString(record.version),
    providers: record.providers.flatMap((item) => {
      const provider = normalizeProviderSchema(item);
      return provider ? [provider] : [];
    }),
  };
}

function normalizeProviderSchema(payload: unknown) {
  const record = asRecord(payload);
  if (!record) {
    return null;
  }
  const provider = readString(record.provider);
  if (!provider) {
    return null;
  }
  return {
    provider,
    label: readString(record.label) || provider,
    modes: readStringArray(record.modes),
    fields: Array.isArray(record.fields)
      ? record.fields.flatMap((item) => {
          const field = normalizeFieldSchema(item);
          return field ? [field] : [];
        })
      : [],
    actions: Array.isArray(record.actions)
      ? record.actions.flatMap((item) => {
          const action = asRecord(item);
          const key = readString(action?.key);
          if (!key) {
            return [];
          }
          return [
            {
              key,
              label: readString(action?.label) || key,
              kind: readString(action?.kind),
            },
          ];
        })
      : undefined,
  };
}

function normalizeFieldSchema(payload: unknown) {
  const record = asRecord(payload);
  if (!record) {
    return null;
  }
  const key = readString(record.key);
  if (!key) {
    return null;
  }
  const options = Array.isArray(record.options)
    ? record.options.flatMap((item) => {
        const option = asRecord(item);
        const value = readString(option?.value);
        if (!value) {
          return [];
        }
        return [{ value, label: readString(option?.label) || value }];
      })
    : undefined;
  return {
    key,
    label: readString(record.label) || key,
    // 未知类型保留原样：渲染器按已知枚举映射，未知降级为文本框并展示原始 key。
    type: readString(record.type) || "text",
    required: record.required === true,
    placeholder: readString(record.placeholder),
    help: readString(record.help),
    default: readString(record.default),
    options: options && options.length > 0 ? options : undefined,
  };
}

function readPolicy(value: unknown): ConnectorView["policy"] {
  const record = asRecord(value);
  if (!record) {
    return {};
  }
  const allowedUsers = readStringArray(record.allowed_users);
  return {
    command_prefix: readString(record.command_prefix) || undefined,
    allowed_users:
      allowedUsers && allowedUsers.length > 0 ? allowedUsers : undefined,
    session_mode: readString(record.session_mode) || undefined,
    reply_mode: readString(record.reply_mode) || undefined,
    runtime_owner: readString(record.runtime_owner) || undefined,
  };
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

function readString(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function readStringArray(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) {
    return undefined;
  }
  const items = value.filter(
    (item): item is string => typeof item === "string" && item.trim().length > 0,
  );
  return items.length > 0 ? items.map((item) => item.trim()) : undefined;
}

function readStringMap(value: unknown): Record<string, string> | undefined {
  const record = asRecord(value);
  if (!record) {
    return undefined;
  }
  const entries = Object.entries(record).filter(
    (entry): entry is [string, string] => typeof entry[1] === "string",
  );
  return entries.length > 0 ? Object.fromEntries(entries) : undefined;
}
