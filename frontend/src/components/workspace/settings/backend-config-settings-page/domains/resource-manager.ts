// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { getConfigValueAtPath, setConfigValueAtPath } from "../../runtime-config-editor-utils";
import { isConfigRecord } from "../../runtime-provider-config-utils";
import { type RuntimeResourceManagerConfigSummary } from "../../runtime-resource-manager-domain-utils";
import { parseLooseScalar } from "../format";
import { type ConfigEditorCore } from "../use-config-core";


export function createResourceManagerDomain(core: ConfigEditorCore) {
  const {
    setDraftParsed,
  } = core;

  function handleResourceManagerConfigChange(
    nextResourceManagerConfig: RuntimeResourceManagerConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentResourceManagerValue = getConfigValueAtPath(current, [
        "resource_manager",
      ]);
      const currentResourceManager = isConfigRecord(currentResourceManagerValue)
        ? currentResourceManagerValue
        : {};
      const currentHealthCheck = isConfigRecord(
        currentResourceManager.health_check,
      )
        ? currentResourceManager.health_check
        : {};

      return setConfigValueAtPath(current, ["resource_manager"], {
        ...currentResourceManager,
        enabled: nextResourceManagerConfig.enabled,
        default_group_algorithm:
          nextResourceManagerConfig.defaultGroupAlgorithm,
        default_provider_algorithm:
          nextResourceManagerConfig.defaultProviderAlgorithm,
        default_key_algorithm: nextResourceManagerConfig.defaultKeyAlgorithm,
        cross_provider_key_selection:
          nextResourceManagerConfig.crossProviderKeySelection,
        health_check: {
          ...currentHealthCheck,
          enabled: nextResourceManagerConfig.healthCheckEnabled,
          interval: nextResourceManagerConfig.healthCheckInterval,
          auto_recovery: nextResourceManagerConfig.healthCheckAutoRecovery,
          recovery_threshold: parseLooseScalar(
            nextResourceManagerConfig.healthCheckRecoveryThreshold,
          ),
        },
        enable_stats: nextResourceManagerConfig.enableStats,
        stats_retention: nextResourceManagerConfig.statsRetention,
      });
    });
  }


  return {
    handleResourceManagerConfigChange,
  };
}

export type ResourceManagerDomain = ReturnType<typeof createResourceManagerDomain>;
