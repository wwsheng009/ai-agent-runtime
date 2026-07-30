import type { ProviderAccountCache } from "@/types/runtime";

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
  account: ProviderAccountCache | null;
  accountAuthRef: string;
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
  siteType: string;
  siteTypeConfidence: string;
  siteTypeDetectedAt: string;
  siteTypeScores: Record<string, number>;
  /** Ephemeral NewAPI subject user id; request-only / auth-store via refresh. */
  subjectUserId: string;
  supportedModelsText: string;
  supportTypesText: string;
  /** Ephemeral NewAPI system token; never persisted into provider config. */
  systemAccessToken: string;
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

  const siteType = draft.siteType.trim();
  const siteTypeConfidence = draft.siteTypeConfidence.trim();
  const siteTypeDetectedAt = draft.siteTypeDetectedAt.trim();
  const accountAuthRef = draft.accountAuthRef.trim();

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
      ...(siteType ? { site_type: siteType } : {}),
      ...(siteTypeConfidence ? { site_type_confidence: siteTypeConfidence } : {}),
      ...(siteTypeDetectedAt ? { site_type_detected_at: siteTypeDetectedAt } : {}),
      ...(Object.keys(draft.siteTypeScores).length > 0
        ? { site_type_scores: draft.siteTypeScores }
        : {}),
      ...(accountAuthRef ? { account_auth_ref: accountAuthRef } : {}),
      ...(draft.account ? { account: draft.account } : {}),
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
