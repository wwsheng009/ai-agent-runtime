// 通道动作载荷的请求/归一化（docs/bots/03-host-integration.md §6 actions）：
// 从 api/runtime/connectors.ts 拆出（P0-2 行数预算）；asRecord/readString 复用
// shared.ts 的导出助手。
//
//   * normalizeActionRequestBody：提交体归一化（丢 undefined，空=不带 body）；
//   * normalizeConnectorActionPayload：返回载荷归一化（已知字段按类型读出，
//     未识别字段原样进 `raw`；非对象视为结构错误——Provider 契约是 JSON object）。

import type { ConnectorActionPayload } from "@/types/runtime";

import { asRecord, readString } from "./shared";

export function normalizeActionRequestBody(
  payload?: Record<string, unknown> | null,
): Record<string, unknown> | undefined {
  if (!payload) {
    return undefined;
  }
  const entries = Object.entries(payload).filter(
    ([, value]) => value !== undefined,
  );
  return entries.length > 0 ? Object.fromEntries(entries) : undefined;
}

export function normalizeConnectorActionPayload(
  payload: unknown,
): ConnectorActionPayload {
  const record = asRecord(payload);
  if (!record) {
    throw new Error("runtime connector action payload is not an object");
  }
  return {
    login_id: readString(record.login_id) || undefined,
    status: readString(record.status) || undefined,
    base_url: readString(record.base_url) || undefined,
    qr_code: readString(record.qr_code) || undefined,
    qr_code_content: readString(record.qr_code_content) || undefined,
    expires_at: readString(record.expires_at) || undefined,
    refresh_count:
      typeof record.refresh_count === "number" &&
      Number.isFinite(record.refresh_count)
        ? record.refresh_count
        : undefined,
    account_id: readString(record.account_id) || undefined,
    user_id: readString(record.user_id) || undefined,
    credential_ready: record.credential_ready === true,
    message: readString(record.message) || undefined,
    settings: asRecord(record.settings)
      ? Object.fromEntries(
          Object.entries(record.settings as Record<string, unknown>).filter(
            (entry): entry is [string, string] => typeof entry[1] === "string",
          ),
        )
      : undefined,
    secrets: asRecord(record.secrets)
      ? Object.fromEntries(
          Object.entries(record.secrets as Record<string, unknown>).filter(
            (entry): entry is [string, string] => typeof entry[1] === "string",
          ),
        )
      : undefined,
    raw: record,
  };
}
