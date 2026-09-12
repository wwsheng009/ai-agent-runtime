// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { type WebsocketDomain } from "../../domains/websocket";
import { RuntimeWebsocketDomainEditor } from "../../lazy-editors";
import { ConfigEditorLoadingCard } from "../../primitives";
import { type ConfigEditorCore } from "../../use-config-core";


export function WebsocketModeSection({ core, domain }: { core: ConfigEditorCore; domain: WebsocketDomain }) {
  const {
    t,
    websocketConfig,
  } = core;

  const {
    handleWebsocketConfigChange,
  } = domain;

  return (
    <Suspense
      fallback={
        <ConfigEditorLoadingCard
          label={t("editor.modes.websocket.label")}
        />
      }
    >
      <RuntimeWebsocketDomainEditor
        config={websocketConfig}
        onChange={handleWebsocketConfigChange}
      />
    </Suspense>
  );
}
