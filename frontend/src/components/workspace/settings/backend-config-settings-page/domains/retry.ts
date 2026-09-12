// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { getConfigValueAtPath, setConfigValueAtPath } from "../../runtime-config-editor-utils";
import { isConfigRecord } from "../../runtime-provider-config-utils";
import { type RetryRuleDraftInput } from "../../runtime-retry-domain-editor";
import { type RuntimeRetryConfigSummary, normalizeRetryMatchList } from "../../runtime-retry-domain-utils";
import { parseLooseScalar } from "../format";
import { type ConfigEditorCore } from "../use-config-core";


export function createRetryDomain(core: ConfigEditorCore) {
  const {
    retryRules,
    setDraftParsed,
    setError,
    setStatusMessage,
    t,
  } = core;

  function handleRetryConfigChange(nextRetryConfig: RuntimeRetryConfigSummary) {
    setDraftParsed((current: unknown) => {
      const currentRetryValue = getConfigValueAtPath(current, ["retry"]);
      const currentRetry = isConfigRecord(currentRetryValue)
        ? currentRetryValue
        : {};
      const currentRecovery = isConfigRecord(
        currentRetry.invalid_encrypted_content_recovery,
      )
        ? currentRetry.invalid_encrypted_content_recovery
        : {};
      const currentEnhancedStrategy = isConfigRecord(
        currentRetry.enhanced_strategy,
      )
        ? currentRetry.enhanced_strategy
        : {};

      return setConfigValueAtPath(current, ["retry"], {
        ...currentRetry,
        enabled: nextRetryConfig.enabled,
        default_max_retries: parseLooseScalar(
          nextRetryConfig.defaultMaxRetries,
        ),
        default_retry_delay_ms: parseLooseScalar(
          nextRetryConfig.defaultRetryDelayMs,
        ),
        default_backoff_multiplier: parseLooseScalar(
          nextRetryConfig.defaultBackoffMultiplier,
        ),
        invalid_encrypted_content_recovery: {
          ...currentRecovery,
          strip_client_state_once:
            nextRetryConfig.invalidEncryptedContentStripClientStateOnce,
        },
        enhanced_strategy: {
          ...currentEnhancedStrategy,
          enabled: nextRetryConfig.enhancedStrategyEnabled,
          secondary_threshold: parseLooseScalar(
            nextRetryConfig.enhancedStrategySecondaryThreshold,
          ),
          fallback_threshold: parseLooseScalar(
            nextRetryConfig.enhancedStrategyFallbackThreshold,
          ),
          primary_min_score: parseLooseScalar(
            nextRetryConfig.enhancedStrategyPrimaryMinScore,
          ),
          secondary_excluded_score: parseLooseScalar(
            nextRetryConfig.enhancedStrategySecondaryExcludedScore,
          ),
        },
      });
    });
  }

  function handleSaveRetryRule(
    draft: RetryRuleDraftInput,
    editingIndex: number | null,
  ) {
    const name = draft.name.trim();
    if (!name) {
      return t("editor.validation.retryRuleNameRequired");
    }
    if (
      retryRules.some(
        (rule) => rule.name === name && rule.index !== editingIndex,
      )
    ) {
      return t("editor.validation.retryRuleExists", { name });
    }

    setDraftParsed((current: unknown) => {
      const currentRetryValue = getConfigValueAtPath(current, ["retry"]);
      const currentRetry = isConfigRecord(currentRetryValue)
        ? currentRetryValue
        : {};
      const currentRules = Array.isArray(currentRetry.rules)
        ? [...currentRetry.rules]
        : [];
      const currentRuleValue =
        editingIndex != null &&
        editingIndex >= 0 &&
        editingIndex < currentRules.length &&
        isConfigRecord(currentRules[editingIndex])
          ? currentRules[editingIndex]
          : {};
      const currentRule = isConfigRecord(currentRuleValue)
        ? currentRuleValue
        : {};
      const currentErrorCode = isConfigRecord(currentRule.error_code)
        ? { ...currentRule.error_code }
        : {};
      const currentKeyword = isConfigRecord(currentRule.keyword)
        ? { ...currentRule.keyword }
        : {};
      const currentStatusCode = isConfigRecord(currentRule.status_code)
        ? { ...currentRule.status_code }
        : {};
      const nextRule: Record<string, unknown> = {
        ...currentRule,
        name,
        description: draft.description.trim(),
        enabled: draft.enabled,
        max_retries: parseLooseScalar(draft.maxRetries),
        retry_delay_ms: parseLooseScalar(draft.retryDelayMs),
        backoff_multiplier: parseLooseScalar(draft.backoffMultiplier),
      };

      const errorCodeCodes = normalizeRetryMatchList(draft.errorCodeCodesText);
      if (errorCodeCodes.length > 0) {
        currentErrorCode.codes = errorCodeCodes;
      } else {
        delete currentErrorCode.codes;
      }
      if (draft.errorCodePattern.trim()) {
        currentErrorCode.pattern = draft.errorCodePattern.trim();
      } else {
        delete currentErrorCode.pattern;
      }
      if (Object.keys(currentErrorCode).length > 0) {
        nextRule.error_code = currentErrorCode;
      } else {
        delete nextRule.error_code;
      }

      const keywordValues = normalizeRetryMatchList(draft.keywordValuesText);
      const keywordPatterns = normalizeRetryMatchList(
        draft.keywordPatternsText,
      );
      if (keywordValues.length > 0) {
        currentKeyword.values = keywordValues;
      } else {
        delete currentKeyword.values;
      }
      if (keywordPatterns.length > 0) {
        currentKeyword.patterns = keywordPatterns;
      } else {
        delete currentKeyword.patterns;
      }
      if (
        keywordValues.length > 0 ||
        keywordPatterns.length > 0 ||
        draft.keywordCaseSensitive
      ) {
        currentKeyword.case_sensitive = draft.keywordCaseSensitive;
      } else {
        delete currentKeyword.case_sensitive;
      }
      if (Object.keys(currentKeyword).length > 0) {
        nextRule.keyword = currentKeyword;
      } else {
        delete nextRule.keyword;
      }

      if (draft.statusCodeRange.trim()) {
        currentStatusCode.range = draft.statusCodeRange.trim();
      } else {
        delete currentStatusCode.range;
      }
      if (Object.keys(currentStatusCode).length > 0) {
        nextRule.status_code = currentStatusCode;
      } else {
        delete nextRule.status_code;
      }

      if (editingIndex == null) {
        currentRules.push(nextRule);
      } else {
        currentRules[editingIndex] = nextRule;
      }

      return setConfigValueAtPath(current, ["retry"], {
        ...currentRetry,
        rules: currentRules,
      });
    });
    setError(null);
    setStatusMessage(
      editingIndex == null
        ? t("editor.messages.retryRuleCreated", { name })
        : t("editor.messages.retryRuleUpdated", { name }),
    );
    return null;
  }

  function handleDeleteRetryRule(index: number) {
    if (!window.confirm(t("editor.messages.confirmDeleteRetryRule", { index: String(index + 1) }))) {
      return;
    }

    setDraftParsed((current: unknown) => {
      const currentRetryValue = getConfigValueAtPath(current, ["retry"]);
      const currentRetry = isConfigRecord(currentRetryValue)
        ? currentRetryValue
        : {};
      const currentRules = Array.isArray(currentRetry.rules)
        ? [...currentRetry.rules]
        : [];
      currentRules.splice(index, 1);

      return setConfigValueAtPath(current, ["retry"], {
        ...currentRetry,
        rules: currentRules,
      });
    });
    setStatusMessage(t("editor.messages.retryRuleDeleted", { index: String(index + 1) }));
  }

  function handleMoveRetryRule(index: number, direction: "up" | "down") {
    setDraftParsed((current: unknown) => {
      const currentRetryValue = getConfigValueAtPath(current, ["retry"]);
      const currentRetry = isConfigRecord(currentRetryValue)
        ? currentRetryValue
        : {};
      const currentRules = Array.isArray(currentRetry.rules)
        ? [...currentRetry.rules]
        : [];
      const targetIndex = direction === "up" ? index - 1 : index + 1;

      if (
        index < 0 ||
        index >= currentRules.length ||
        targetIndex < 0 ||
        targetIndex >= currentRules.length
      ) {
        return current;
      }

      const [rule] = currentRules.splice(index, 1);
      currentRules.splice(targetIndex, 0, rule);

      return setConfigValueAtPath(current, ["retry"], {
        ...currentRetry,
        rules: currentRules,
      });
    });
    setStatusMessage(
      direction === "up"
        ? t("editor.messages.retryRuleMovedUp", { index: String(index + 1) })
        : t("editor.messages.retryRuleMovedDown", { index: String(index + 1) }),
    );
  }


  return {
    handleRetryConfigChange,
    handleSaveRetryRule,
    handleDeleteRetryRule,
    handleMoveRetryRule,
  };
}

export type RetryDomain = ReturnType<typeof createRetryDomain>;
