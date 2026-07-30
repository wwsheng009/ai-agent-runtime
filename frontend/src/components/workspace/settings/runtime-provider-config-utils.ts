import type { ProviderAccountCache } from "@/types/runtime";

import { formatProviderAccountCacheLine } from "@/api/runtime/siteaccount";

import { inferConfigValueKind } from "./runtime-config-editor-utils";
import { buildNamedMapConfigSnippet } from "./runtime-config-yaml-utils";
import {
  hasRuntimeProxyConfig,
  readRuntimeProxyConfig,
  summarizeRuntimeProxyConfig,
} from "./runtime-proxy-domain-utils";

const COMMON_PROVIDER_KEYS = new Set([
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

export type RuntimeProviderSummary = {
  account: ProviderAccountCache | null;
  accountAuthRef: string;
  accountSummary: string;
  apiKey: string;
  apiPath: string;
  baseUrl: string;
  defaultModel: string;
  enabled: boolean;
  extraFieldCount: number;
  forwardUrl: string;
  hasProxyOverride: boolean;
  name: string;
  protocol: string;
  proxyEnabled: boolean;
  proxySummary: string;
  raw: Record<string, unknown>;
  siteType: string;
  siteTypeConfidence: string;
  siteTypeDetectedAt: string;
  siteTypeScores: Record<string, number>;
  supportedModels: string[];
  supportTypes: string[];
  timeout: string;
  truncationAdapter: string;
};

export function isConfigRecord(
  value: unknown,
): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function normalizeStringArrayInput(value: string) {
  return value
    .split(/[\n,]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

export function getRuntimeDefaultProvider(value: unknown) {
  const providers = getProvidersRoot(value);
  const defaultProvider = providers?.default_provider;
  return typeof defaultProvider === "string" ? defaultProvider : "";
}

export function getRuntimeProviderRecord(
  value: unknown,
  providerName: string,
): Record<string, unknown> | null {
  const providers = getProvidersRoot(value);
  const items = providers?.items;
  if (!isConfigRecord(items)) {
    return null;
  }

  const provider = items[providerName];
  return isConfigRecord(provider) ? provider : null;
}

export function listRuntimeProviderSummaries(
  value: unknown,
): RuntimeProviderSummary[] {
  const providers = getProvidersRoot(value);
  const items = providers?.items;
  if (!isConfigRecord(items)) {
    return [];
  }

  return Object.entries(items)
    .sort(([leftName], [rightName]) => leftName.localeCompare(rightName))
    .flatMap(([name, provider]) =>
      isConfigRecord(provider) ? [buildProviderSummary(name, provider)] : [],
    );
}

export function createDefaultProviderConfig(
  providerName: string,
  protocol = "openai",
) {
  const sanitizedProtocol = protocol.trim() || "openai";
  return {
    enabled: true,
    protocol: sanitizedProtocol,
    base_url: "",
    api_path: "",
    forward_url:
      sanitizedProtocol === "anthropic"
        ? "/v1/messages"
        : sanitizedProtocol === "openai_image"
          ? "/v1/images/generations"
          : sanitizedProtocol === "gemini"
            ? "/v1beta/models/{model}:streamGenerateContent?alt=sse"
            : sanitizedProtocol === "codex"
              ? "/v1/responses"
              : "/v1/chat/completions",
    api_key: "",
    default_model: "",
    supported_models: [],
    support_types: [sanitizedProtocol],
    timeout: "300s",
    headers: {},
    model_mappings: {
      "*": "",
    },
    truncation_adapter: deriveDefaultTruncationAdapter(
      providerName,
      sanitizedProtocol,
    ),
  };
}

export function buildProviderCreateConfigSnippet(
  provider: RuntimeProviderSummary,
) {
  return buildNamedMapConfigSnippet(provider.name, provider.raw);
}

export function summarizeDraftSections(value: unknown) {
  if (!isConfigRecord(value)) {
    return [];
  }

  return Object.keys(value)
    .sort((left, right) => left.localeCompare(right))
    .map((key) => {
      const sectionValue = value[key];
      let itemCount = 0;
      if (Array.isArray(sectionValue)) {
        itemCount = sectionValue.length;
      } else if (isConfigRecord(sectionValue)) {
        itemCount = Object.keys(sectionValue).length;
      }

      return {
        id: key,
        kind: inferConfigValueKind(sectionValue),
        itemCount,
        label: key,
      };
    });
}

function getProvidersRoot(value: unknown) {
  if (!isConfigRecord(value)) {
    return null;
  }
  const providers = value.providers;
  return isConfigRecord(providers) ? providers : null;
}

function buildProviderSummary(
  name: string,
  raw: Record<string, unknown>,
): RuntimeProviderSummary {
  const extraFieldCount = Object.keys(raw).filter(
    (key) => !COMMON_PROVIDER_KEYS.has(key),
  ).length;
  const proxyConfig = readRuntimeProxyConfig(raw.proxy);
  const account = readProviderAccountCache(raw.account);

  return {
    name,
    raw,
    enabled: Boolean(raw.enabled),
    protocol: readStringField(raw.protocol),
    truncationAdapter: readStringField(raw.truncation_adapter),
    baseUrl: readStringField(raw.base_url),
    apiPath: readStringField(raw.api_path),
    forwardUrl: readStringField(raw.forward_url),
    apiKey: readStringField(raw.api_key),
    defaultModel: readStringField(raw.default_model),
    supportedModels: readStringArrayField(raw.supported_models),
    supportTypes: readStringArrayField(raw.support_types),
    timeout: readStringField(raw.timeout),
    hasProxyOverride: hasRuntimeProxyConfig(proxyConfig),
    proxyEnabled: proxyConfig.enabled,
    proxySummary: summarizeRuntimeProxyConfig(proxyConfig),
    extraFieldCount,
    siteType: readStringField(raw.site_type),
    siteTypeConfidence: readStringField(raw.site_type_confidence),
    siteTypeDetectedAt: readStringField(raw.site_type_detected_at),
    siteTypeScores: readNumberRecordField(raw.site_type_scores),
    accountAuthRef: readStringField(raw.account_auth_ref),
    account,
    accountSummary: formatProviderAccountCacheLine(account),
  };
}

function readProviderAccountCache(value: unknown): ProviderAccountCache | null {
  if (!isConfigRecord(value)) {
    return null;
  }
  return value as ProviderAccountCache;
}

function readNumberRecordField(value: unknown): Record<string, number> {
  if (!isConfigRecord(value)) {
    return {};
  }
  const result: Record<string, number> = {};
  for (const [key, entry] of Object.entries(value)) {
    if (typeof entry === "number" && Number.isFinite(entry)) {
      result[key] = entry;
    }
  }
  return result;
}

function readStringField(value: unknown) {
  return typeof value === "string" ? value : "";
}

function readStringArrayField(value: unknown) {
  if (!Array.isArray(value)) {
    return [];
  }

  return value
    .map((item) => (typeof item === "string" ? item.trim() : ""))
    .filter(Boolean);
}

function deriveDefaultTruncationAdapter(
  providerName: string,
  protocol: string,
) {
  if (protocol === "anthropic") {
    return "anthropic_local";
  }
  if (protocol === "gemini") {
    return "gemini_local";
  }
  if (protocol === "codex") {
    return providerName.toLowerCase().includes("cli") ? "openai_local" : "";
  }
  return "openai_local";
}
