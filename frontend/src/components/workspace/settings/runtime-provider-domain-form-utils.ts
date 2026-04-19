import {
  isConfigRecord,
  normalizeStringArrayInput,
} from "./runtime-provider-config-utils";
import {
  buildRuntimeProxyRecord,
  hasRuntimeProxyConfig,
  type RuntimeProxyConfigSummary,
} from "./runtime-proxy-domain-utils";

export type ProviderDraftInput = {
  apiKey: string;
  apiPath: string;
  baseUrl: string;
  defaultModel: string;
  enabled: boolean;
  extraJson: string;
  forwardUrl: string;
  headersJson: string;
  modelMappingsJson: string;
  name: string;
  protocol: string;
  proxyEnabled: boolean;
  proxyHttp: string;
  proxyHttps: string;
  proxyNoProxy: string;
  setAsDefault: boolean;
  supportedModelsText: string;
  supportTypesText: string;
  timeout: string;
  truncationAdapter: string;
};

export function buildProviderRecordFromDraft(
  draft: ProviderDraftInput,
): { error: string | null; record: Record<string, unknown> | null } {
  const headers = parseJsonRecord(draft.headersJson, "`headers` 必须是 JSON 对象。");
  if (!headers.record) {
    return { error: headers.error, record: null };
  }

  const modelMappings = parseJsonRecord(
    draft.modelMappingsJson,
    "`model_mappings` 必须是 JSON 对象。",
  );
  if (!modelMappings.record) {
    return { error: modelMappings.error, record: null };
  }

  const extraFields = parseJsonRecord(
    draft.extraJson,
    "`扩展字段 JSON` 必须是 JSON 对象。",
  );
  if (!extraFields.record) {
    return { error: extraFields.error, record: null };
  }

  const proxyConfig: RuntimeProxyConfigSummary = {
    enabled: draft.proxyEnabled,
    http: draft.proxyHttp,
    https: draft.proxyHttps,
    noProxy: draft.proxyNoProxy,
  };
  const proxyRecord = hasRuntimeProxyConfig(proxyConfig)
    ? buildRuntimeProxyRecord(proxyConfig)
    : null;

  return {
    error: null,
    record: {
      ...extraFields.record,
      enabled: draft.enabled,
      protocol: draft.protocol.trim(),
      base_url: draft.baseUrl.trim(),
      api_path: draft.apiPath.trim(),
      forward_url: draft.forwardUrl.trim(),
      api_key: draft.apiKey,
      default_model: draft.defaultModel.trim(),
      supported_models: normalizeStringArrayInput(draft.supportedModelsText),
      support_types: normalizeStringArrayInput(draft.supportTypesText),
      timeout: draft.timeout.trim(),
      truncation_adapter: draft.truncationAdapter.trim(),
      headers: headers.record,
      model_mappings: modelMappings.record,
      ...(proxyRecord ? { proxy: proxyRecord } : {}),
    },
  };
}

function parseJsonRecord(
  value: string,
  errorMessage: string,
): { error: string | null; record: Record<string, unknown> | null } {
  const trimmed = value.trim();
  if (!trimmed) {
    return { error: null, record: {} };
  }

  try {
    const parsed = JSON.parse(trimmed) as unknown;
    if (!isConfigRecord(parsed)) {
      return { error: errorMessage, record: null };
    }
    return { error: null, record: parsed };
  } catch {
    return { error: errorMessage, record: null };
  }
}
