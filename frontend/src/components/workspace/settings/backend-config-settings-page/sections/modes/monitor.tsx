// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { type MonitorDomain } from "../../domains/monitor";
import { RuntimeMonitorDomainEditor } from "../../lazy-editors";
import { ConfigEditorLoadingCard } from "../../primitives";
import { type ConfigEditorCore } from "../../use-config-core";


export function MonitorModeSection({ core, domain }: { core: ConfigEditorCore; domain: MonitorDomain }) {
  const {
    monitorConfig,
    t,
  } = core;

  const {
    handleMonitorConfigChange,
  } = domain;

  return (
    <Suspense
      fallback={
        <ConfigEditorLoadingCard
          label={t("editor.modes.monitor.label")}
        />
      }
    >
      <RuntimeMonitorDomainEditor
        config={monitorConfig}
        onChange={handleMonitorConfigChange}
      />
    </Suspense>
  );
}
