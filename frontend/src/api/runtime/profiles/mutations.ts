// Runtime profile API —— 写操作（Batch 8 §10.5 契约）。
//
// 纪律：写回体与后端字段名对齐（spec + expected_mtime，便于 409 冲突检测）；
// 响应按 deleted/renamed/moved/registered 等布尔位归一化，不猜默认成功。

import type {
  RuntimeProfileApplyResponse,
  RuntimeProfileCreateRequest,
  RuntimeProfileDefaultResponse,
  RuntimeProfileDeleteResponse,
  RuntimeProfileDuplicateRequest,
  RuntimeProfileMoveRequest,
  RuntimeProfileMoveResponse,
  RuntimeProfileRenameRequest,
  RuntimeProfileRenameResponse,
  RuntimeProfileView,
  RuntimeProfileWriteRequest,
} from "@/types/runtime";

import { buildRuntimeUrlWithQuery, fetchRuntimeJson } from "../shared";
import {
  asRecord,
  buildCreateBody,
  buildWriteBody,
  normalizeCreateResponse,
  normalizeIssueList,
  normalizeProfileView,
  normalizeReferences,
  readAliasedString,
  readNumber,
  readStringArray,
  readStringMap,
} from "./normalize";
import {
  buildRuntimeProfileApplyUrl,
  buildRuntimeProfileDefaultUrl,
  buildRuntimeProfileDuplicateUrl,
  buildRuntimeProfileMoveUrl,
  buildRuntimeProfileRenameUrl,
  buildRuntimeProfileUrl,
  jsonRequestInit,
  runtimeProfilesUrl,
} from "./url";
export async function createRuntimeProfile(request: RuntimeProfileCreateRequest) {
  const payload = await fetchRuntimeJson<unknown>(
    runtimeProfilesUrl,
    jsonRequestInit("POST", buildCreateBody(request)),
  );
  return normalizeCreateResponse(payload, request.name);
}

export async function updateRuntimeProfile(
  ref: string,
  request: RuntimeProfileWriteRequest,
) {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeProfileUrl(ref),
    jsonRequestInit("PUT", buildWriteBody(request)),
  );
  const record = asRecord(payload) ?? {};
  const profile = asRecord(record.profile);
  if (!profile) {
    throw new Error("invalid runtime profile update payload: profile view missing");
  }
  return {
    view: normalizeProfileView(profile as RuntimeProfileView),
    mtime: readAliasedString(record, "mtime"),
    warningCount: readNumber(record.warning_count ?? record.warningCount, 0),
    issues: normalizeIssueList(record.issues),
    nameMismatch: record.name_mismatch === true || record.nameMismatch === true,
    hint: readAliasedString(record, "hint"),
  };
}

export async function deleteRuntimeProfile(ref: string, options?: { force?: boolean }) {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeUrlWithQuery(`/api/runtime/profiles/${encodeURIComponent(ref)}`, {
      force: options?.force ? "true" : undefined,
    }),
    { method: "DELETE" },
  );
  const record = asRecord(payload) ?? {};
  return {
    deleted: record.deleted === true,
    ref: readAliasedString(record, "ref") || ref,
    root: readAliasedString(record, "root"),
    removedFiles: readStringArray(record.removed_files ?? record.removedFiles),
    removedFileCount: readNumber(record.removed_file_count ?? record.removedFileCount, 0),
    configItemsRemoved: readStringArray(record.config_items_removed ?? record.configItemsRemoved),
    defaultCleared: record.default_cleared === true || record.defaultCleared === true,
    references: normalizeReferences(record.references),
    recoverable: record.recoverable === true,
    error: readAliasedString(record, "error"),
  } satisfies RuntimeProfileDeleteResponse;
}

export async function duplicateRuntimeProfile(
  ref: string,
  request: RuntimeProfileDuplicateRequest,
) {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeProfileDuplicateUrl(ref),
    jsonRequestInit("POST", buildCreateBody({ ...request, fromRef: ref })),
  );
  return normalizeCreateResponse(payload, request.name);
}

export async function renameRuntimeProfile(ref: string, request: RuntimeProfileRenameRequest) {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeProfileRenameUrl(ref),
    jsonRequestInit("POST", { name: request.name }),
  );
  const record = asRecord(payload) ?? {};
  return {
    renamed: record.renamed === true,
    oldRef: readAliasedString(record, "oldRef") || ref,
    newRef: readAliasedString(record, "newRef") || request.name,
    root: readAliasedString(record, "root"),
    layer: readAliasedString(record, "layer"),
    configUpdated: record.config_updated === true || record.configUpdated === true,
    error: readAliasedString(record, "error"),
    sessionNote: readAliasedString(record, "sessionNote"),
    references: normalizeReferences(record.references),
  } satisfies RuntimeProfileRenameResponse;
}

export async function moveRuntimeProfile(ref: string, request: RuntimeProfileMoveRequest) {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeProfileMoveUrl(ref),
    jsonRequestInit("POST", { layer: request.layer ?? "", root: request.root ?? "" }),
  );
  const record = asRecord(payload) ?? {};
  return {
    moved: record.moved === true,
    ref: readAliasedString(record, "ref") || ref,
    from: readAliasedString(record, "from"),
    to: readAliasedString(record, "to"),
    configUpdated: record.config_updated === true || record.configUpdated === true,
    error: readAliasedString(record, "error"),
    references: normalizeReferences(record.references),
  } satisfies RuntimeProfileMoveResponse;
}

/**
 * 设为默认（POST /default）：只影响新会话（D26），响应给出
 * `{default_profile, previous_default, config_path, registered, affects, current_session_note}`。
 */
export async function setDefaultRuntimeProfile(ref: string, options?: { register?: boolean }) {
  const payload = await fetchRuntimeJson<unknown>(
    buildRuntimeProfileDefaultUrl(ref),
    jsonRequestInit("POST", { register: options?.register !== false }),
  );
  const record = asRecord(payload) ?? {};
  return {
    defaultProfile: readAliasedString(record, "defaultProfile") || ref,
    previousDefault: readAliasedString(record, "previousDefault"),
    configPath: readAliasedString(record, "configPath"),
    registered: record.registered === true,
    affects: readAliasedString(record, "affects"),
    currentSessionNote: readAliasedString(record, "currentSessionNote"),
  } satisfies RuntimeProfileDefaultResponse;
}

/**
 * 应用到会话（Batch 12 的 set_profile 与这里共用同一端点）。
 * Batch 8 后端尚未落地时返回 501 not_implemented，调用方需按码降级为提示。
 */
export async function applyRuntimeProfile(ref: string, sessionId?: string) {
  const payload = await fetchRuntimeJson<RuntimeProfileApplyResponse>(
    buildRuntimeProfileApplyUrl(ref),
    jsonRequestInit("POST", { session_id: sessionId ?? "" }),
  );
  const record = asRecord(payload) ?? {};
  return {
    ok: record.ok !== false,
    changed: readStringMap(record.changed),
    warnings: readStringArray(record.warnings),
  } satisfies RuntimeProfileApplyResponse;
}
