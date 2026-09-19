/**
 * Provider 模型发现 API（runtime server）。
 *
 * 端点与 aicli micro web client 的 `/web/api/config/providers/*` 同源，
 * 两者都复用后端 `internal/providerops`，语义一致：
 *   * fetch-models   —— 拉取 /models，按协议分类并返回可合并的 model_ids；
 *   * auto-import    —— 探测协议 + 拉取模型 + 生成配置补丁（不落盘）；
 *   * probe-models   —— 对指定模型 × 协议做最小补全实测。
 *
 * 请求字段可只给 name（已保存 provider 由后端从配置快照补齐），
 * 编辑器里新输入的 base_url / api_key 优先于快照。
 */

import type {
  ProviderAutoImportRequest,
  ProviderAutoImportResult,
  ProviderModelsResult,
  ProviderOpsRequest,
  ProviderProbeRequest,
  ProviderProbeResult,
} from "@/types/runtime";

import { buildRuntimeUrl, fetchRuntimeJson } from "./shared";

const providerFetchModelsUrl = buildRuntimeUrl("/api/runtime/providers/fetch-models");
const providerAutoImportUrl = buildRuntimeUrl("/api/runtime/providers/auto-import");
const providerProbeModelsUrl = buildRuntimeUrl("/api/runtime/providers/probe-models");

export async function fetchRuntimeProviderModels(
  request: ProviderOpsRequest,
): Promise<ProviderModelsResult> {
  return fetchRuntimeJson<ProviderModelsResult>(providerFetchModelsUrl, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify(request),
  });
}

export async function autoImportRuntimeProvider(
  request: ProviderAutoImportRequest,
): Promise<ProviderAutoImportResult> {
  return fetchRuntimeJson<ProviderAutoImportResult>(providerAutoImportUrl, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify(request),
  });
}

export async function probeRuntimeProviderModels(
  request: ProviderProbeRequest,
): Promise<ProviderProbeResult> {
  return fetchRuntimeJson<ProviderProbeResult>(providerProbeModelsUrl, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify(request),
  });
}

/**
 * 生成请求载荷：只有非空字段才上行，让后端对缺省字段走配置快照补齐
 * （尤其是 api_key：留空表示「沿用已保存的 key」，而不是把它清空）。
 */
export function buildProviderOpsRequest(input: {
  apiKey?: string;
  baseUrl?: string;
  headers?: Record<string, string>;
  modelsPath?: string;
  name?: string;
  protocol?: string;
  timeoutSeconds?: number;
}): ProviderOpsRequest {
  const request: ProviderOpsRequest = {};
  assignIfPresent(request, "name", input.name);
  assignIfPresent(request, "base_url", input.baseUrl);
  assignIfPresent(request, "api_key", input.apiKey);
  assignIfPresent(request, "protocol", input.protocol);
  assignIfPresent(request, "models_path", input.modelsPath);
  if (input.headers && Object.keys(input.headers).length > 0) {
    request.headers = input.headers;
  }
  if (
    typeof input.timeoutSeconds === "number" &&
    Number.isFinite(input.timeoutSeconds) &&
    input.timeoutSeconds > 0
  ) {
    request.timeout_seconds = Math.floor(input.timeoutSeconds);
  }
  return request;
}

/**
 * 追加合并（仅用于「探测后手动合并」这类显式动作）：保留手填行，
 * 新增模型去重追加到末尾。
 */
export function appendProviderModels(
  currentModelIDs: string[],
  fetchedModelIDs: string[],
): string[] {
  const merged: string[] = [];
  for (const raw of currentModelIDs) {
    const id = String(raw ?? "").trim();
    if (id && !merged.includes(id)) {
      merged.push(id);
    }
  }
  for (const raw of fetchedModelIDs) {
    const id = String(raw ?? "").trim();
    if (id && !merged.includes(id)) {
      merged.push(id);
    }
  }
  return merged;
}

/** 从探测结果里挑出实测支持（ok）的模型 ID（去重保序）。 */
export function collectProbeSupportedModels(
  results: ProviderProbeResult["results"] | undefined,
): string[] {
  if (!Array.isArray(results)) {
    return [];
  }
  const models: string[] = [];
  for (const item of results) {
    const id = String(item?.model_id ?? "").trim();
    if (item?.verdict === "ok" && id && !models.includes(id)) {
      models.push(id);
    }
  }
  return models;
}

function assignIfPresent<T extends object, K extends keyof T>(
  target: T,
  key: K,
  value: T[K] | undefined,
) {
  if (typeof value !== "string") {
    return;
  }
  const trimmed = value.trim();
  if (trimmed) {
    target[key] = trimmed as T[K];
  }
}
