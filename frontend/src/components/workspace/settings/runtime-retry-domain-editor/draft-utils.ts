// 由 components/workspace/settings/runtime-retry-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";

import { type RuntimeRetryRuleSummary } from "../runtime-retry-domain-utils";

import { type RetryRuleDraftInput } from "./types";

export function createRetryRuleDraft(
  rule: RuntimeRetryRuleSummary | null,
): RetryRuleDraftInput {
  if (!rule) {
    return {
      name: "",
      description: "",
      enabled: true,
      maxRetries: "3",
      retryDelayMs: "1000",
      backoffMultiplier: "2.0",
      errorCodeCodesText: "",
      errorCodePattern: "",
      keywordValuesText: "",
      keywordPatternsText: "",
      keywordCaseSensitive: false,
      statusCodeRange: "",
    };
  }

  return {
    name: rule.name,
    description: rule.description,
    enabled: rule.enabled,
    maxRetries: rule.maxRetries,
    retryDelayMs: rule.retryDelayMs,
    backoffMultiplier: rule.backoffMultiplier,
    errorCodeCodesText: rule.errorCodeCodesText,
    errorCodePattern: rule.errorCodePattern,
    keywordValuesText: rule.keywordValuesText,
    keywordPatternsText: rule.keywordPatternsText,
    keywordCaseSensitive: rule.keywordCaseSensitive,
    statusCodeRange: rule.statusCodeRange,
  };
}

export function summarizeMatcher(
  rule: RuntimeRetryRuleSummary,
  t: TFunction<"runtimeConfig">,
) {
  const lines = [
    rule.statusCodeRange ? `status ${rule.statusCodeRange}` : null,
    summarizeTextGroup("codes", rule.errorCodeCodesText),
    rule.errorCodePattern ? `pattern ${rule.errorCodePattern}` : null,
    summarizeTextGroup("keyword", rule.keywordValuesText),
    summarizeTextGroup("regex", rule.keywordPatternsText),
  ].filter((value): value is string => Boolean(value));

  return lines.length > 0
    ? lines
    : [t("editor.retry.rules.noExplicitMatchers")];
}

function summarizeTextGroup(prefix: string, value: string) {
  const items = value
    .split(/\r?\n/)
    .map((item) => item.trim())
    .filter(Boolean);
  if (items.length === 0) {
    return null;
  }
  return items.length === 1 ? `${prefix} ${items[0]}` : `${prefix} ${items[0]} +${items.length - 1}`;
}
