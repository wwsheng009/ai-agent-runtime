import { RuntimeApiError } from "@/api/runtime";

import {
  createDefaultProviderConfig,
  isConfigRecord,
  type RuntimeProviderSummary,
} from "../runtime-provider-config-utils";
import { type ProviderDraftInput } from "../runtime-provider-domain-form-utils";
import { readRuntimeProxyConfig } from "../runtime-proxy-domain-utils";

export function providerSearchText(provider: RuntimeProviderSummary): string {
  return [
    provider.name,
    provider.baseUrl,
    provider.protocol,
    provider.defaultModel,
    provider.siteType,
    provider.siteTypeConfidence,
    provider.accountSummary,
    provider.accountAuthRef,
    provider.apiPath,
    provider.forwardUrl,
    provider.proxySummary,
    ...provider.supportTypes,
    ...provider.supportedModels,
  ].join(" ");
}

export const KNOWN_PROVIDER_KEYS = new Set([
  "enabled",
  "protocol",
  "truncation_adapter",
  "base_url",
  "api_path",
  "forward_url",
  "api_key",
  "default_model",
  "supported_models",
  "support_types",
  "timeout",
  "headers",
  "model_mappings",
  "proxy",
  "site_type",
  "site_type_confidence",
  "site_type_detected_at",
  "site_type_scores",
  "account_auth_ref",
  "account",
]);

export type AccountAction = "detect" | "fetch" | "refresh" | null;


export const providerProtocolOptions = [
  { value: "openai", label: "openai" },
  { value: "openai_image", label: "openai_image" },
  { value: "anthropic", label: "anthropic" },
  { value: "gemini", label: "gemini" },
  { value: "codex", label: "codex" },
] as const;

export function createProviderDraftInput(
  provider: RuntimeProviderSummary | null,
  defaultProvider: string,
): ProviderDraftInput {
  if (!provider) {
    const defaults = createDefaultProviderConfig("new_provider", "openai");
    return {
      name: "",
      enabled: Boolean(defaults.enabled),
      protocol: typeof defaults.protocol === "string" ? defaults.protocol : "openai",
      baseUrl: typeof defaults.base_url === "string" ? defaults.base_url : "",
      apiPath: typeof defaults.api_path === "string" ? defaults.api_path : "",
      forwardUrl: typeof defaults.forward_url === "string" ? defaults.forward_url : "",
      apiKey: typeof defaults.api_key === "string" ? defaults.api_key : "",
      defaultModel:
        typeof defaults.default_model === "string" ? defaults.default_model : "",
      supportedModelsText: Array.isArray(defaults.supported_models)
        ? defaults.supported_models.join("\n")
        : "",
      supportTypesText: Array.isArray(defaults.support_types)
        ? defaults.support_types.join("\n")
        : "",
      timeout: typeof defaults.timeout === "string" ? defaults.timeout : "",
      truncationAdapter:
        typeof defaults.truncation_adapter === "string"
          ? defaults.truncation_adapter
          : "",
      proxyEnabled: false,
      proxyHttp: "",
      proxyHttps: "",
      proxyNoProxy: "",
      headersJson: JSON.stringify(defaults.headers ?? {}, null, 2),
      modelMappingsJson: JSON.stringify(defaults.model_mappings ?? {}, null, 2),
      extraJson: "{}",
      setAsDefault: defaultProvider === "",
      siteType: "",
      siteTypeConfidence: "",
      siteTypeDetectedAt: "",
      siteTypeScores: {},
      accountAuthRef: "",
      account: null,
      systemAccessToken: "",
      subjectUserId: "",
    };
  }

  const extraFields = Object.fromEntries(
    Object.entries(provider.raw).filter(([key]) => !KNOWN_PROVIDER_KEYS.has(key)),
  );
  const proxyConfig = readRuntimeProxyConfig(provider.raw.proxy);

  return {
    name: provider.name,
    enabled: provider.enabled,
    protocol: provider.protocol,
    baseUrl: provider.baseUrl,
    apiPath: provider.apiPath,
    forwardUrl: provider.forwardUrl,
    apiKey: provider.apiKey,
    defaultModel: provider.defaultModel,
    supportedModelsText: provider.supportedModels.join("\n"),
    supportTypesText: provider.supportTypes.join("\n"),
    timeout: provider.timeout,
    truncationAdapter: provider.truncationAdapter,
    proxyEnabled: proxyConfig.enabled,
    proxyHttp: proxyConfig.http,
    proxyHttps: proxyConfig.https,
    proxyNoProxy: proxyConfig.noProxy,
    headersJson: JSON.stringify(
      isConfigRecord(provider.raw.headers) ? provider.raw.headers : {},
      null,
      2,
    ),
    modelMappingsJson: JSON.stringify(
      isConfigRecord(provider.raw.model_mappings) ? provider.raw.model_mappings : {},
      null,
      2,
    ),
    extraJson: JSON.stringify(extraFields, null, 2),
    setAsDefault: provider.name === defaultProvider,
    siteType: provider.siteType,
    siteTypeConfidence: provider.siteTypeConfidence,
    siteTypeDetectedAt: provider.siteTypeDetectedAt,
    siteTypeScores: { ...provider.siteTypeScores },
    accountAuthRef: provider.accountAuthRef,
    account: provider.account,
    // Secrets stay out of provider config; only prefill non-secret subject id hint.
    systemAccessToken: "",
    subjectUserId: provider.account?.external_user_id?.trim() || "",
  };
}

export function describeAccountError(error: unknown, fallback: string) {
  if (error instanceof RuntimeApiError) {
    return error.message || fallback;
  }
  if (error instanceof Error && error.message.trim()) {
    return error.message.trim();
  }
  return fallback;
}
