// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { type NetworkProxyDomain } from "../../domains/network-proxy";
import { RuntimeProxyDomainEditor } from "../../lazy-editors";
import { ConfigEditorLoadingCard } from "../../primitives";
import { type ConfigEditorCore } from "../../use-config-core";


export function NetworkProxyModeSection({ core, domain }: { core: ConfigEditorCore; domain: NetworkProxyDomain }) {
  const {
    proxyConfig,
    t,
  } = core;

  const {
    handleProxyConfigChange,
  } = domain;

  return (
    <Suspense
      fallback={
        <ConfigEditorLoadingCard
          label={t("editor.modes.networkProxy.label")}
        />
      }
    >
      <RuntimeProxyDomainEditor
        config={proxyConfig}
        onChange={handleProxyConfigChange}
      />
    </Suspense>
  );
}
