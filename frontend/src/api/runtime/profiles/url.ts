// Runtime profile API —— URL 构造与 JSON 请求体（Batch 8 §10.5 契约）。
//
// 纪律：ref 允许 user:reviewer、project/foo 这类含分隔符的取值，
// 所有单资源端点必须整段 encodeURIComponent，不得做路径拼接假设。
//
// 本文件只构造 URL 与请求体，不发请求；端点实现见 queries.ts / mutations.ts。

import { buildRuntimeUrl } from "../shared";
export const runtimeProfilesUrl = buildRuntimeUrl("/api/runtime/profiles");

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

export const jsonRequestInit = (method: string, body: unknown): RequestInit => ({
  method,
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify(body),
});
