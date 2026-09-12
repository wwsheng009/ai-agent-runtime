// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { getConfigValueAtPath, setConfigValueAtPath } from "../../runtime-config-editor-utils";
import { type ConcurrencyProviderLimitDraftInput } from "../../runtime-concurrency-domain-editor";
import { type RuntimeConcurrencyConfigSummary } from "../../runtime-concurrency-domain-utils";
import { isConfigRecord } from "../../runtime-provider-config-utils";
import { parseLooseScalar } from "../format";
import { type ConfigEditorCore } from "../use-config-core";


export function createConcurrencyDomain(core: ConfigEditorCore) {
  const {
    concurrencyProviderLimits,
    setDraftParsed,
    setError,
    setStatusMessage,
    t,
  } = core;

  function handleConcurrencyConfigChange(
    nextConcurrencyConfig: RuntimeConcurrencyConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentConcurrencyValue = getConfigValueAtPath(current, [
        "concurrency",
      ]);
      const currentConcurrency = isConfigRecord(currentConcurrencyValue)
        ? currentConcurrencyValue
        : {};

      return setConfigValueAtPath(current, ["concurrency"], {
        ...currentConcurrency,
        enabled: nextConcurrencyConfig.enabled,
        max_concurrent_requests: parseLooseScalar(
          nextConcurrencyConfig.maxConcurrentRequests,
        ),
        queue_size: parseLooseScalar(nextConcurrencyConfig.queueSize),
        queue_timeout: nextConcurrencyConfig.queueTimeout,
      });
    });
  }

  function handleSaveConcurrencyProviderLimit(
    draft: ConcurrencyProviderLimitDraftInput,
    previousProvider: string | null,
  ) {
    const provider = draft.provider.trim();
    if (!provider) {
      return t("editor.validation.providerNameRequired");
    }
    if (
      concurrencyProviderLimits.some(
        (item) =>
          item.provider === provider && item.provider !== previousProvider,
      )
    ) {
      return t("editor.validation.concurrencyProviderExists", {
        provider,
      });
    }

    const limit = draft.limit.trim();
    if (!limit) {
      return t("editor.validation.concurrencyLimitRequired");
    }

    setDraftParsed((current: unknown) => {
      const currentConcurrencyValue = getConfigValueAtPath(current, [
        "concurrency",
      ]);
      const currentConcurrency = isConfigRecord(currentConcurrencyValue)
        ? currentConcurrencyValue
        : {};
      const currentLimits = isConfigRecord(
        currentConcurrency.per_provider_limits,
      )
        ? { ...currentConcurrency.per_provider_limits }
        : {};

      if (previousProvider && previousProvider !== provider) {
        delete currentLimits[previousProvider];
      }
      currentLimits[provider] = parseLooseScalar(limit);

      return setConfigValueAtPath(current, ["concurrency"], {
        ...currentConcurrency,
        per_provider_limits: currentLimits,
      });
    });
    setError(null);
    setStatusMessage(
      previousProvider
        ? t("editor.messages.concurrencyLimitUpdated", {
            provider,
          })
        : t("editor.messages.concurrencyLimitCreated", {
            provider,
          }),
    );
    return null;
  }

  function handleDeleteConcurrencyProviderLimit(provider: string) {
    if (
      !window.confirm(
        t("editor.messages.confirmDeleteConcurrencyLimit", { provider }),
      )
    ) {
      return;
    }

    setDraftParsed((current: unknown) => {
      const currentConcurrencyValue = getConfigValueAtPath(current, [
        "concurrency",
      ]);
      const currentConcurrency = isConfigRecord(currentConcurrencyValue)
        ? currentConcurrencyValue
        : {};
      const currentLimits = isConfigRecord(
        currentConcurrency.per_provider_limits,
      )
        ? { ...currentConcurrency.per_provider_limits }
        : {};
      delete currentLimits[provider];

      return setConfigValueAtPath(current, ["concurrency"], {
        ...currentConcurrency,
        per_provider_limits: currentLimits,
      });
    });
    setStatusMessage(t("editor.messages.concurrencyLimitDeleted", { provider }));
  }


  return {
    handleConcurrencyConfigChange,
    handleSaveConcurrencyProviderLimit,
    handleDeleteConcurrencyProviderLimit,
  };
}

export type ConcurrencyDomain = ReturnType<typeof createConcurrencyDomain>;
