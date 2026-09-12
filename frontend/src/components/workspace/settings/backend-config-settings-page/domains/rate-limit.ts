// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { getConfigValueAtPath, setConfigValueAtPath } from "../../runtime-config-editor-utils";
import { isConfigRecord } from "../../runtime-provider-config-utils";
import { type RateLimitApiKeyDraftInput, type RateLimitPathDraftInput, buildRateLimitApiKeyRecordFromDraft, buildRateLimitPathRecordFromDraft } from "../../runtime-rate-limit-domain-form-utils";
import { type RuntimeRateLimitConfigSummary } from "../../runtime-rate-limit-domain-utils";
import { parseLooseScalar } from "../format";
import { type ConfigEditorCore } from "../use-config-core";


export function createRateLimitDomain(core: ConfigEditorCore) {
  const {
    pathLimits,
    setDraftParsed,
    setError,
    setStatusMessage,
    t,
  } = core;

  function handleRateLimitConfigChange(
    nextRateLimitConfig: RuntimeRateLimitConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentRateLimitValue = getConfigValueAtPath(current, [
        "rate_limit",
      ]);
      const currentRateLimit = isConfigRecord(currentRateLimitValue)
        ? currentRateLimitValue
        : {};
      const currentDefaultLimits = isConfigRecord(
        currentRateLimit.default_limits,
      )
        ? currentRateLimit.default_limits
        : {};
      const currentGlobalLimits = isConfigRecord(currentRateLimit.global_limits)
        ? currentRateLimit.global_limits
        : {};

      return setConfigValueAtPath(current, ["rate_limit"], {
        ...currentRateLimit,
        enabled: nextRateLimitConfig.enabled,
        storage: nextRateLimitConfig.storage,
        algorithm: nextRateLimitConfig.algorithm,
        default_limits: {
          ...currentDefaultLimits,
          qps: parseLooseScalar(nextRateLimitConfig.defaultQps),
          tpm: parseLooseScalar(nextRateLimitConfig.defaultTpm),
          daily_tokens: parseLooseScalar(
            nextRateLimitConfig.defaultDailyTokens,
          ),
          monthly_tokens: parseLooseScalar(
            nextRateLimitConfig.defaultMonthlyTokens,
          ),
        },
        global_limits: {
          ...currentGlobalLimits,
          global_qps: parseLooseScalar(nextRateLimitConfig.globalQps),
          global_tpm: parseLooseScalar(nextRateLimitConfig.globalTpm),
          global_daily_tokens: parseLooseScalar(
            nextRateLimitConfig.globalDailyTokens,
          ),
          global_monthly_tokens: parseLooseScalar(
            nextRateLimitConfig.globalMonthlyTokens,
          ),
        },
      });
    });
  }

  function handleSaveApiKeyLimit(
    draft: RateLimitApiKeyDraftInput,
    editingIndex: number | null,
  ) {
    const nextLimit = buildRateLimitApiKeyRecordFromDraft(draft);
    const limitRecord = nextLimit.record;
    if (!limitRecord) {
      return nextLimit.error ?? t("editor.validation.apiKeyLimitInvalid");
    }

    setDraftParsed((current: unknown) => {
      const currentRateLimitValue = getConfigValueAtPath(current, [
        "rate_limit",
      ]);
      const currentRateLimit = isConfigRecord(currentRateLimitValue)
        ? currentRateLimitValue
        : {};
      const currentLimits = Array.isArray(currentRateLimit.api_key_limits)
        ? [...currentRateLimit.api_key_limits]
        : [];

      if (editingIndex == null) {
        currentLimits.push(limitRecord);
      } else {
        currentLimits[editingIndex] = limitRecord;
      }

      return setConfigValueAtPath(current, ["rate_limit"], {
        ...currentRateLimit,
        api_key_limits: currentLimits,
      });
    });
    setError(null);
    setStatusMessage(
      editingIndex == null
        ? t("editor.messages.apiKeyLimitCreated")
        : t("editor.messages.apiKeyLimitUpdated", { index: String(editingIndex + 1) }),
    );
    return null;
  }

  function handleDeleteApiKeyLimit(index: number) {
    if (
      !window.confirm(
        t("editor.messages.confirmDeleteApiKeyLimit", { index: String(index + 1) }),
      )
    ) {
      return;
    }

    setDraftParsed((current: unknown) => {
      const currentRateLimitValue = getConfigValueAtPath(current, [
        "rate_limit",
      ]);
      const currentRateLimit = isConfigRecord(currentRateLimitValue)
        ? currentRateLimitValue
        : {};
      const currentLimits = Array.isArray(currentRateLimit.api_key_limits)
        ? [...currentRateLimit.api_key_limits]
        : [];
      currentLimits.splice(index, 1);

      return setConfigValueAtPath(current, ["rate_limit"], {
        ...currentRateLimit,
        api_key_limits: currentLimits,
      });
    });
    setStatusMessage(
      t("editor.messages.apiKeyLimitDeleted", { index: String(index + 1) }),
    );
  }

  function handleSavePathLimit(
    draft: RateLimitPathDraftInput,
    previousPath: string | null,
  ) {
    const nextPathLimit = buildRateLimitPathRecordFromDraft(draft);
    const limitPath = nextPathLimit.path;
    const limitRecord = nextPathLimit.record;
    if (!limitPath || !limitRecord) {
      return nextPathLimit.error ?? t("editor.validation.pathLimitInvalid");
    }
    if (
      pathLimits.some(
        (item) => item.path === limitPath && item.path !== previousPath,
      )
    ) {
      return t("editor.validation.pathLimitExists", { path: limitPath });
    }

    setDraftParsed((current: unknown) => {
      const currentRateLimitValue = getConfigValueAtPath(current, [
        "rate_limit",
      ]);
      const currentRateLimit = isConfigRecord(currentRateLimitValue)
        ? currentRateLimitValue
        : {};
      const currentPathLimits = isConfigRecord(currentRateLimit.path_limits)
        ? { ...currentRateLimit.path_limits }
        : {};

      if (previousPath && previousPath !== limitPath) {
        delete currentPathLimits[previousPath];
      }
      currentPathLimits[limitPath] = limitRecord;

      return setConfigValueAtPath(current, ["rate_limit"], {
        ...currentRateLimit,
        path_limits: currentPathLimits,
      });
    });
    setError(null);
    setStatusMessage(
      previousPath
        ? t("editor.messages.pathLimitUpdated", { path: limitPath })
        : t("editor.messages.pathLimitCreated", { path: limitPath }),
    );
    return null;
  }

  function handleDeletePathLimit(path: string) {
    if (!window.confirm(t("editor.messages.confirmDeletePathLimit", { path }))) {
      return;
    }

    setDraftParsed((current: unknown) => {
      const currentRateLimitValue = getConfigValueAtPath(current, [
        "rate_limit",
      ]);
      const currentRateLimit = isConfigRecord(currentRateLimitValue)
        ? currentRateLimitValue
        : {};
      const currentPathLimits = isConfigRecord(currentRateLimit.path_limits)
        ? { ...currentRateLimit.path_limits }
        : {};
      delete currentPathLimits[path];

      return setConfigValueAtPath(current, ["rate_limit"], {
        ...currentRateLimit,
        path_limits: currentPathLimits,
      });
    });
    setStatusMessage(t("editor.messages.pathLimitDeleted", { path }));
  }


  return {
    handleRateLimitConfigChange,
    handleSaveApiKeyLimit,
    handleDeleteApiKeyLimit,
    handleSavePathLimit,
    handleDeletePathLimit,
  };
}

export type RateLimitDomain = ReturnType<typeof createRateLimitDomain>;
