/**
 * Provider 模型发现（provider ops）的纯函数层：
 *   * 从编辑草稿构造请求载荷；
 *   * 把 fetch-models / auto-import 响应折算成草稿补丁；
 *   * probe-models 结论的聚合（按模型分组 + verdict 计数）。
 *
 * UI 组件只做状态编排，语义都收敛在这里，便于单测覆盖。
 */

import { buildProviderOpsRequest } from "@/api/runtime";
import type {
  ProviderAutoImportResult,
  ProviderModelsResult,
  ProviderOpsRequest,
  ProviderProbeResult,
  ProviderProbeResultItem,
} from "@/types/runtime";

import { isConfigRecord, normalizeStringArrayInput } from "./runtime-provider-config-utils";
import { providerProtocolOptions } from "./runtime-provider-domain-editor/draft-utils";
import { type ProviderDraftInput } from "./runtime-provider-domain-form-utils";

/**
 * 从编辑草稿构造 provider ops 请求：已保存 provider 用原始名称（改名时后端
 * 仍能解析旧配置），未保存的新 provider 用表单名称。留空字段不上行，让后端
 * 走配置快照补齐——api_key 留空表示「沿用已保存的 key」而不是清空。
 */
export function buildProviderOpsRequestFromDraft(
  draft: ProviderDraftInput,
  providerName?: string | null,
): ProviderOpsRequest {
  return buildProviderOpsRequest({
    name: providerName?.trim() || draft.name.trim(),
    baseUrl: draft.baseUrl,
    apiKey: draft.apiKey,
    protocol: draft.protocol,
    headers: parseDraftHeaders(draft.headersJson),
    timeoutSeconds: parseGoDurationSeconds(draft.timeout),
  });
}

/**
 * 是否具备可解析的操作目标（fetch / auto-import / probe 的前置条件）：
 * 已保存 provider 可只传名称由后端快照补齐；未保存草稿必须给出 base_url，
 * 否则后端既无快照可查、也无法探测协议，只会返回 "base_url is required"。
 */
export function canResolveProviderOpsTarget(input: {
  baseUrl?: string | null;
  providerName?: string | null;
}): boolean {
  if (String(input.providerName ?? "").trim()) {
    return true;
  }
  return Boolean(String(input.baseUrl ?? "").trim());
}

/** 解析草稿里的 `headers` JSON；非对象或非法 JSON 时返回 undefined（沿用快照）。 */
export function parseDraftHeaders(
  headersJson: string,
): Record<string, string> | undefined {
  const raw = headersJson.trim();
  if (!raw || raw === "{}") {
    return undefined;
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return undefined;
  }
  if (!isConfigRecord(parsed)) {
    return undefined;
  }
  const headers: Record<string, string> = {};
  for (const [key, value] of Object.entries(parsed)) {
    const name = key.trim();
    if (name && typeof value === "string" && value.trim()) {
      headers[name] = value;
    }
  }
  return Object.keys(headers).length > 0 ? headers : undefined;
}

/**
 * 解析 Go duration 字符串（"300s" / "5m" / "1h30m"）为秒；纯数字按秒处理。
 * 解析不出或非正数时返回 undefined（后端用默认超时）。
 */
export function parseGoDurationSeconds(value: string): number | undefined {
  const raw = value.trim();
  if (!raw) {
    return undefined;
  }
  if (/^\d+$/.test(raw)) {
    const seconds = Number.parseInt(raw, 10);
    return seconds > 0 ? seconds : undefined;
  }
  // 注意 `ms` 必须排在 `m` 之前，否则 "500ms" 会先匹配出 "500m" 再因剩余 "s" 失败。
  const pattern = /(\d+(?:\.\d+)?)(ms|h|m|s)/g;
  let total = 0;
  let matched = false;
  let lastIndex = 0;
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(raw)) !== null) {
    if (match.index !== lastIndex) {
      return undefined;
    }
    lastIndex = match.index + match[0].length;
    matched = true;
    const amount = Number.parseFloat(match[1]);
    switch (match[2]) {
      case "h":
        total += amount * 3600;
        break;
      case "m":
        total += amount * 60;
        break;
      case "s":
        total += amount;
        break;
      default:
        total += amount / 1000;
        break;
    }
  }
  if (!matched || lastIndex !== raw.length || total <= 0) {
    return undefined;
  }
  return Math.max(1, Math.round(total));
}

/** 去重（保序）后的模型 ID 列表。 */
export function normalizeProviderModelIDs(modelIDs: string[] | undefined): string[] {
  const models: string[] = [];
  for (const raw of modelIDs ?? []) {
    const id = String(raw ?? "").trim();
    if (id && !models.includes(id)) {
      models.push(id);
    }
  }
  return models;
}

/**
 * fetch-models 的草稿补丁：支持模型列表整体替换为本次拉取结果（覆盖语义，
 * 与 micro web client 一致）；服务端未返回可用模型时返回 null 表示不改动
 * 表单，避免把手填配置误清空。
 */
export function providerModelsPatch(
  result: ProviderModelsResult,
): Partial<ProviderDraftInput> | null {
  const models = normalizeProviderModelIDs(result.model_ids);
  if (models.length === 0) {
    return null;
  }
  return { supportedModelsText: models.join("\n") };
}

/**
 * auto-import 的草稿补丁：后端返回一份完整的 provider 草稿（协议、地址、
 * 模型、站点信息），这里只映射可编辑表单字段；api_key 与 NewAPI 密钥
 * 属于请求侧秘密，不回填。
 */
export function providerAutoImportPatch(
  result: ProviderAutoImportResult,
): Partial<ProviderDraftInput> {
  const patch: Partial<ProviderDraftInput> = {};
  const protocol = result.protocol?.trim();
  if (protocol && isKnownProviderProtocol(protocol)) {
    patch.protocol = protocol;
  }
  if (result.base_url?.trim()) {
    patch.baseUrl = result.base_url.trim();
  }
  if (result.api_path?.trim()) {
    patch.apiPath = result.api_path.trim();
  }
  if (result.forward_url?.trim()) {
    patch.forwardUrl = result.forward_url.trim();
  }
  if (result.default_model?.trim()) {
    patch.defaultModel = result.default_model.trim();
  }
  const models = normalizeProviderModelIDs(result.supported_models);
  if (models.length > 0) {
    patch.supportedModelsText = models.join("\n");
  }
  const supportTypes = normalizeProviderModelIDs(result.support_types);
  if (supportTypes.length > 0) {
    patch.supportTypesText = supportTypes.join("\n");
  }
  if (result.site_type?.trim()) {
    patch.siteType = result.site_type.trim();
  }
  if (result.site_type_confidence?.trim()) {
    patch.siteTypeConfidence = result.site_type_confidence.trim();
  }
  if (result.site_type_scores && Object.keys(result.site_type_scores).length > 0) {
    patch.siteTypeScores = { ...result.site_type_scores };
  }
  if (result.account) {
    patch.account = result.account;
  }
  return patch;
}

export function isKnownProviderProtocol(protocol: string): boolean {
  return providerProtocolOptions.some((option) => option.value === protocol);
}

/** 解析「支持模型」文本域为模型 ID 列表（复用逗号 / 换行归一化）。 */
export function parseSupportedModelsText(text: string): string[] {
  return normalizeStringArrayInput(text);
}

export type ProviderProbeSummary = {
  total: number;
  ok: number;
  unsupported: number;
  error: number;
};

/** 统计探测结论：ok=实测支持；unsupported=明确拒绝；其余（error/invalid）归为无法定性。 */
export function summarizeProbeResults(
  result: ProviderProbeResult | null | undefined,
): ProviderProbeSummary {
  const summary: ProviderProbeSummary = { total: 0, ok: 0, unsupported: 0, error: 0 };
  for (const item of result?.results ?? []) {
    summary.total += 1;
    if (item?.verdict === "ok") {
      summary.ok += 1;
    } else if (item?.verdict === "unsupported") {
      summary.unsupported += 1;
    } else {
      summary.error += 1;
    }
  }
  return summary;
}

export type ProviderProbeModelRow = {
  modelId: string;
  probes: ProviderProbeResultItem[];
};

/** 把扁平结论列表按模型聚合，保持后端返回顺序。 */
export function groupProbeResultsByModel(
  result: ProviderProbeResult | null | undefined,
): ProviderProbeModelRow[] {
  const rows: ProviderProbeModelRow[] = [];
  const index = new Map<string, ProviderProbeModelRow>();
  for (const item of result?.results ?? []) {
    const modelId = String(item?.model_id ?? "").trim();
    if (!modelId) {
      continue;
    }
    let row = index.get(modelId);
    if (!row) {
      row = { modelId, probes: [] };
      index.set(modelId, row);
      rows.push(row);
    }
    row.probes.push(item);
  }
  return rows;
}

/** 探测目标：优先用最近一次 fetch 的假定模型，其次退回表单里的支持模型。 */
export function resolveProbeModels(
  assumedModelIDs: string[],
  draft: ProviderDraftInput,
): string[] {
  const assumed = normalizeProviderModelIDs(assumedModelIDs);
  if (assumed.length > 0) {
    return assumed;
  }
  return normalizeProviderModelIDs(parseSupportedModelsText(draft.supportedModelsText));
}

/** 合并多条警告文案；空列表返回空串。 */
export function joinProviderOpsWarnings(warnings: string[] | undefined): string {
  if (!Array.isArray(warnings)) {
    return "";
  }
  return warnings
    .map((warning) => String(warning ?? "").trim())
    .filter(Boolean)
    .join("; ");
}
