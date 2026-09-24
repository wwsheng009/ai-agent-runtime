// Runtime profile API —— URL 构造与 JSON 请求体（Batch 8 §10.5 契约）。
//
// 纪律：ref 允许 user:reviewer、project/foo 这类含分隔符的取值，
// 所有单资源端点必须整段 encodeURIComponent，不得做路径拼接假设。
//
// 本文件只构造 URL 与请求体，不发请求；端点实现见 queries.ts / mutations.ts。

import { buildRuntimeUrl, buildRuntimeUrlWithQuery } from "../shared";
export const runtimeProfilesUrl = buildRuntimeUrl("/api/runtime/profiles");

/**
 * 列表端点带 workspace 时（Batch 14），后端附带 D29 工作区信任上下文与逐条
 * prompts 扣留标记；不带 workspace 时响应与既有完全一致（旧行为零变化）。
 */
export function buildRuntimeProfilesListUrl(query: { workspace?: string } = {}) {
  return buildRuntimeUrlWithQuery("/api/runtime/profiles", {
    workspace: query.workspace?.trim() || undefined,
  });
}

/** 单个 profile 的资源 URL；ref 可能含 `:` `/`，必须整段编码。 */
export function buildRuntimeProfileUrl(ref: string) {
  return `${runtimeProfilesUrl}/${encodeURIComponent(ref)}`;
}

export function buildRuntimeProfileValidateUrl(ref: string) {
  return `${buildRuntimeProfileUrl(ref)}/validate`;
}

export function buildRuntimeProfilePreviewUrl(ref: string) {
  return `${buildRuntimeProfileUrl(ref)}/preview`;
}

export function buildRuntimeProfileDuplicateUrl(ref: string) {
  return `${buildRuntimeProfileUrl(ref)}/duplicate`;
}

export function buildRuntimeProfileRenameUrl(ref: string) {
  return `${buildRuntimeProfileUrl(ref)}/rename`;
}

export function buildRuntimeProfileMoveUrl(ref: string) {
  return `${buildRuntimeProfileUrl(ref)}/move`;
}

export function buildRuntimeProfileDefaultUrl(ref: string) {
  return `${buildRuntimeProfileUrl(ref)}/default`;
}

export function buildRuntimeProfileApplyUrl(ref: string) {
  return `${buildRuntimeProfileUrl(ref)}/apply`;
}

export function buildRuntimeProfileReferencesUrl(ref: string) {
  return `${buildRuntimeProfileUrl(ref)}/references`;
}

/** 导出端点：POST + 二进制响应（zip），不是 JSON。 */
export function buildRuntimeProfileExportUrl(ref: string) {
  return `${buildRuntimeProfileUrl(ref)}/export`;
}

/** 导入端点：请求体是 zip 本身，name/layer/dry_run 一律走查询参数。 */
export function buildRuntimeProfileImportUrl(query: {
  name?: string;
  layer?: string;
  dryRun?: boolean;
}) {
  return buildRuntimeUrlWithQuery("/api/runtime/profiles/import", {
    name: query.name,
    layer: query.layer,
    dry_run: query.dryRun === true ? "true" : undefined,
  });
}

export const jsonRequestInit = (method: string, body: unknown): RequestInit => ({
  method,
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify(body),
});
