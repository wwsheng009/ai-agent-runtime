// Runtime profile API —— 只读查询（Batch 8 §10.5 契约）。
//
// 纪律：非 2xx 由 shared.fetchRuntimeJson 抛 RuntimeApiError 并保留后端 code；
// 列表/视图/引用面的契约回归一律抛错（归一化口径见 ./normalize 顶部注释）。

import type {
  RuntimeProfileListResponse,
  RuntimeProfileValidateResponse,
  RuntimeProfileView,
  RuntimeProfileWriteRequest,
} from "@/types/runtime";

import { fetchRuntimeJson } from "../shared";
import {
  asRecord,
  normalizeIssueList,
  normalizeProfileListResponse,
  normalizeProfileView,
  normalizeReferences,
  readAliasedString,
  readNumber,
} from "./normalize";
import {
  buildRuntimeProfilePreviewUrl,
  buildRuntimeProfileReferencesUrl,
  buildRuntimeProfileUrl,
  buildRuntimeProfileValidateUrl,
  jsonRequestInit,
  runtimeProfilesUrl,
} from "./url";
export async function listRuntimeProfiles() {
  const payload = await fetchRuntimeJson<RuntimeProfileListResponse>(runtimeProfilesUrl);
  return normalizeProfileListResponse(payload);
}

export async function getRuntimeProfile(ref: string) {
  const payload = await fetchRuntimeJson<RuntimeProfileView>(buildRuntimeProfileUrl(ref));
  return normalizeProfileView(payload);
}

/**
 * 校验草稿（POST /validate，请求体 `{agent?, yaml?, spec?}`）：
 * 响应为 `{ref, name, path, root, agent, valid, error_count, warning_count, issues}`。
 */
export async function validateRuntimeProfile(
  ref: string,
  document: RuntimeProfileWriteRequest["document"],
  agent?: string,
) {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeProfileValidateUrl(ref),
    jsonRequestInit("POST", { spec: document, agent: agent ?? "" }),
  );
  const record = asRecord(payload) ?? {};
  return {
    ref: readAliasedString(record, "ref") || ref,
    name: readAliasedString(record, "name"),
    path: readAliasedString(record, "path"),
    root: readAliasedString(record, "root"),
    agent: readAliasedString(record, "agent"),
    valid: record.valid !== false,
    errorCount: readNumber(record.error_count ?? record.errorCount, 0),
    warningCount: readNumber(record.warning_count ?? record.warningCount, 0),
    issues: normalizeIssueList(record.issues),
  } satisfies RuntimeProfileValidateResponse;
}

/**
 * 预览草稿（POST /preview）：后端与 GET 同构返回**解析后视图**（未写盘，
 * `preview=true`），不是 diff 文本；调用方按视图消费。
 */
export async function previewRuntimeProfile(
  ref: string,
  document: RuntimeProfileWriteRequest["document"],
  agent?: string,
) {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeProfilePreviewUrl(ref),
    jsonRequestInit("POST", { spec: document, agent: agent ?? "" }),
  );
  return normalizeProfileView(payload as RuntimeProfileView);
}

export async function listRuntimeProfileReferences(ref: string) {
  const payload = await fetchRuntimeJson<unknown>(buildRuntimeProfileReferencesUrl(ref));
  const normalized = normalizeReferences(payload);
  if (!normalized) {
    throw new Error("invalid runtime profile references payload: object expected");
  }
  return normalized;
}
