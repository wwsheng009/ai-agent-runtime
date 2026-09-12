// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { removeConfigValueAtPath, setConfigValueAtPath } from "../../runtime-config-editor-utils";
import { type RuntimeProxyConfigSummary, buildRuntimeProxyRecord, hasRuntimeProxyConfig } from "../../runtime-proxy-domain-utils";
import { formatRuntimeProxySummary } from "../format";
import { type ConfigEditorCore } from "../use-config-core";


export function createNetworkProxyDomain(core: ConfigEditorCore) {
  const {
    setDraftParsed,
    setStatusMessage,
    t,
    tCommon,
  } = core;

  function handleProxyConfigChange(nextProxyConfig: RuntimeProxyConfigSummary) {
    setDraftParsed((current: unknown) => {
      if (!hasRuntimeProxyConfig(nextProxyConfig)) {
        return removeConfigValueAtPath(current, ["providers", "proxy"]);
      }

      return setConfigValueAtPath(
        current,
        ["providers", "proxy"],
        buildRuntimeProxyRecord(nextProxyConfig),
      );
    });
    setStatusMessage(
      hasRuntimeProxyConfig(nextProxyConfig)
        ? t("editor.messages.globalProxyUpdated", {
            summary: formatRuntimeProxySummary(nextProxyConfig, t, tCommon),
          })
        : t("editor.messages.globalProxyCleared"),
    );
  }


  return {
    handleProxyConfigChange,
  };
}

export type NetworkProxyDomain = ReturnType<typeof createNetworkProxyDomain>;
