// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { getConfigValueAtPath, setConfigValueAtPath } from "../../runtime-config-editor-utils";
import { isConfigRecord } from "../../runtime-provider-config-utils";
import { type ProviderQueueProviderDraftInput, buildProviderQueueProviderRecordFromDraft } from "../../runtime-provider-queue-domain-form-utils";
import { type RuntimeProviderQueueConfigSummary } from "../../runtime-provider-queue-domain-utils";
import { parseLooseScalar } from "../format";
import { type ConfigEditorCore } from "../use-config-core";


export function createProviderQueueDomain(core: ConfigEditorCore) {
  const {
    providerQueueProviders,
    setDraftParsed,
    setError,
    setStatusMessage,
    t,
  } = core;

  function handleProviderQueueConfigChange(
    nextProviderQueueConfig: RuntimeProviderQueueConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentProviderQueueValue = getConfigValueAtPath(current, [
        "provider_queue",
      ]);
      const currentProviderQueue = isConfigRecord(currentProviderQueueValue)
        ? currentProviderQueueValue
        : {};
      const currentDefaultSlot = isConfigRecord(
        currentProviderQueue.default_slot,
      )
        ? currentProviderQueue.default_slot
        : {};
      const currentOverflow = isConfigRecord(currentProviderQueue.overflow)
        ? currentProviderQueue.overflow
        : {};
      const currentWaitHeartbeat = isConfigRecord(
        currentProviderQueue.wait_heartbeat,
      )
        ? currentProviderQueue.wait_heartbeat
        : {};

      return setConfigValueAtPath(current, ["provider_queue"], {
        ...currentProviderQueue,
        enabled: nextProviderQueueConfig.enabled,
        default_slot: {
          ...currentDefaultSlot,
          max_concurrency: parseLooseScalar(
            nextProviderQueueConfig.defaultMaxConcurrency,
          ),
          queue_size: parseLooseScalar(
            nextProviderQueueConfig.defaultQueueSize,
          ),
          queue_timeout: nextProviderQueueConfig.defaultQueueTimeout,
          overflow_strategy: nextProviderQueueConfig.defaultOverflowStrategy,
        },
        overflow: {
          ...currentOverflow,
          enabled: nextProviderQueueConfig.overflowEnabled,
          max_attempts: parseLooseScalar(
            nextProviderQueueConfig.overflowMaxAttempts,
          ),
          strategy: nextProviderQueueConfig.overflowStrategy,
        },
        wait_heartbeat: {
          ...currentWaitHeartbeat,
          enabled: nextProviderQueueConfig.waitHeartbeatEnabled,
          interval: nextProviderQueueConfig.waitHeartbeatInterval,
          comment: nextProviderQueueConfig.waitHeartbeatComment,
          max_wait_time: nextProviderQueueConfig.waitHeartbeatMaxWaitTime,
        },
      });
    });
  }

  function handleSaveProviderQueueProvider(
    draft: ProviderQueueProviderDraftInput,
    previousProvider: string | null,
  ) {
    const nextProvider = buildProviderQueueProviderRecordFromDraft(draft);
    const providerName = nextProvider.provider;
    const providerRecord = nextProvider.record;
    if (!providerName || !providerRecord) {
      return nextProvider.error ?? t("editor.validation.providerQueueInvalid");
    }
    if (
      providerQueueProviders.some(
        (item) =>
          item.provider === providerName && item.provider !== previousProvider,
      )
    ) {
      return `Provider "${providerName}" 已存在队列覆盖。`;
    }

    setDraftParsed((current: unknown) => {
      const currentProviderQueueValue = getConfigValueAtPath(current, [
        "provider_queue",
      ]);
      const currentProviderQueue = isConfigRecord(currentProviderQueueValue)
        ? currentProviderQueueValue
        : {};
      const currentProviders = isConfigRecord(currentProviderQueue.providers)
        ? { ...currentProviderQueue.providers }
        : {};

      if (previousProvider && previousProvider !== providerName) {
        delete currentProviders[previousProvider];
      }
      currentProviders[providerName] = providerRecord;

      return setConfigValueAtPath(current, ["provider_queue"], {
        ...currentProviderQueue,
        providers: currentProviders,
      });
    });
    setError(null);
    setStatusMessage(
      previousProvider
        ? t("editor.messages.providerQueueUpdated", { provider: providerName })
        : t("editor.messages.providerQueueCreated", {
            provider: providerName,
          }),
    );
    return null;
  }

  function handleDeleteProviderQueueProvider(provider: string) {
    if (
      !window.confirm(
        t("editor.messages.confirmDeleteProviderQueueProvider", {
          provider,
        }),
      )
    ) {
      return;
    }

    setDraftParsed((current: unknown) => {
      const currentProviderQueueValue = getConfigValueAtPath(current, [
        "provider_queue",
      ]);
      const currentProviderQueue = isConfigRecord(currentProviderQueueValue)
        ? currentProviderQueueValue
        : {};
      const currentProviders = isConfigRecord(currentProviderQueue.providers)
        ? { ...currentProviderQueue.providers }
        : {};
      delete currentProviders[provider];

      return setConfigValueAtPath(current, ["provider_queue"], {
        ...currentProviderQueue,
        providers: currentProviders,
      });
    });
    setStatusMessage(t("editor.messages.providerQueueDeleted", { provider }));
  }


  return {
    handleProviderQueueConfigChange,
    handleSaveProviderQueueProvider,
    handleDeleteProviderQueueProvider,
  };
}

export type ProviderQueueDomain = ReturnType<typeof createProviderQueueDomain>;
