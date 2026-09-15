// 批次 18（P2-2 子片 1）：草稿写入前置校验。
// 口径：只做「结构判定」——不猜业务语义、不改写草稿、不触文档；error 级问题阻断写入
//（保存 / 预览），warning 级只提示。未知 / 未设置（undefined 与 null）一律不判违规。

import { isConfigRecord } from "./runtime-provider-config-utils";

export type ConfigValuePath = Array<string | number>;

export type ConfigValidationSeverity = "error" | "warning";

export type ConfigValidationIssue = {
  /** runtimeConfig 词典 editor.draftValidation.* 下的键名（不含前缀）。 */
  messageKey: string;
  params?: Record<string, string | number>;
  path: ConfigValuePath;
  severity: ConfigValidationSeverity;
};

const PROVIDER_STRING_FIELDS = [
  "protocol",
  "base_url",
  "api_path",
  "forward_url",
  "api_key",
  "default_model",
  "timeout",
  "truncation_adapter",
] as const;

const PROVIDER_STRING_LIST_FIELDS = [
  "supported_models",
  "support_types",
] as const;

const PROVIDER_MAPPING_FIELDS = [
  "headers",
  "model_mappings",
  "proxy",
] as const;

export function formatConfigIssuePath(path: ConfigValuePath): string {
  if (path.length === 0) {
    return "(root)";
  }
  return path.map((segment) => String(segment)).join(".");
}

export function countConfigIssues(
  issues: ConfigValidationIssue[],
  severity: ConfigValidationSeverity,
): number {
  return issues.filter((issue) => issue.severity === severity).length;
}

export function validateRuntimeConfigRawDraft(
  raw: string,
): ConfigValidationIssue[] {
  return raw.trim() === ""
    ? [{ messageKey: "rawEmpty", path: [], severity: "error" }]
    : [];
}

export function validateRuntimeConfigStructuredDraft(
  parsed: unknown,
): ConfigValidationIssue[] {
  if (!isConfigRecord(parsed)) {
    return [{ messageKey: "rootNotMapping", path: [], severity: "error" }];
  }

  const issues: ConfigValidationIssue[] = [];
  if ("providers" in parsed && !isConfigRecord(parsed.providers)) {
    issues.push({
      messageKey: "providersNotMapping",
      path: ["providers"],
      severity: "error",
    });
    return issues;
  }

  const providers = isConfigRecord(parsed.providers) ? parsed.providers : null;
  if (!providers) {
    return issues;
  }

  const items = providers.items;
  if (items !== undefined && items !== null && !isConfigRecord(items)) {
    issues.push({
      messageKey: "providersItemsNotMapping",
      path: ["providers", "items"],
      severity: "error",
    });
  }

  const defaultProvider = providers.default_provider;
  if (
    defaultProvider !== undefined &&
    defaultProvider !== null &&
    typeof defaultProvider !== "string"
  ) {
    issues.push({
      messageKey: "defaultProviderNotString",
      path: ["providers", "default_provider"],
      severity: "error",
    });
  } else if (
    typeof defaultProvider === "string" &&
    defaultProvider.trim() !== "" &&
    isConfigRecord(items)
  ) {
    if (items[defaultProvider] === undefined) {
      issues.push({
        messageKey: "defaultProviderUnknown",
        params: { name: defaultProvider },
        path: ["providers", "default_provider"],
        severity: "warning",
      });
    }
  }

  if (isConfigRecord(items)) {
    for (const [name, provider] of Object.entries(items)) {
      if (!isConfigRecord(provider)) {
        if (provider === undefined || provider === null) {
          continue;
        }
        issues.push({
          messageKey: "providerNotMapping",
          params: { name },
          path: ["providers", "items", name],
          severity: "error",
        });
        continue;
      }
      issues.push(...validateProviderEntry(name, provider));
    }
  }

  return issues;
}

function validateProviderEntry(
  name: string,
  provider: Record<string, unknown>,
): ConfigValidationIssue[] {
  const issues: ConfigValidationIssue[] = [];
  const base: ConfigValuePath = ["providers", "items", name];

  for (const field of PROVIDER_STRING_FIELDS) {
    const value = provider[field];
    if (value === undefined || value === null) {
      continue;
    }
    if (typeof value !== "string") {
      issues.push({
        messageKey: "providerFieldNotString",
        params: { field },
        path: [...base, field],
        severity: "error",
      });
    }
  }

  const enabled = provider.enabled;
  if (enabled !== undefined && enabled !== null && typeof enabled !== "boolean") {
    issues.push({
      messageKey: "providerFieldNotBoolean",
      params: { field: "enabled" },
      path: [...base, "enabled"],
      severity: "error",
    });
  }

  for (const field of PROVIDER_STRING_LIST_FIELDS) {
    const value = provider[field];
    if (value === undefined || value === null) {
      continue;
    }
    if (!isStringArray(value)) {
      issues.push({
        messageKey: "providerFieldNotStringList",
        params: { field },
        path: [...base, field],
        severity: "error",
      });
    }
  }

  for (const field of PROVIDER_MAPPING_FIELDS) {
    const value = provider[field];
    if (value === undefined || value === null) {
      continue;
    }
    if (!isConfigRecord(value)) {
      issues.push({
        messageKey: "providerFieldNotMapping",
        params: { field },
        path: [...base, field],
        severity: "error",
      });
    }
  }

  return issues;
}

function isStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === "string");
}
