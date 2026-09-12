// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Suspense } from "react";
import { type AgentRoutingDomain } from "../../domains/agent-routing";
import { RuntimeAgentRoutingDomainEditor } from "../../lazy-editors";
import { ConfigEditorLoadingCard } from "../../primitives";
import { type ConfigEditorCore } from "../../use-config-core";


export function AgentRoutingModeSection({ core, domain }: { core: ConfigEditorCore; domain: AgentRoutingDomain }) {
  const {
    agentRoutingSettings,
    providers,
    t,
  } = core;

  const {
    handleAgentRoutePreview,
    handleAgentRoutingChange,
    handleTeamRoutingInheritanceChange,
  } = domain;

  return (
    <Suspense
      fallback={
        <ConfigEditorLoadingCard
          label={t("editor.modes.agentRouting.label")}
        />
      }
    >
      <RuntimeAgentRoutingDomainEditor
        onChange={handleAgentRoutingChange}
        onPreviewRoute={handleAgentRoutePreview}
        onTeamInheritanceChange={handleTeamRoutingInheritanceChange}
        providers={providers}
        settings={agentRoutingSettings}
      />
    </Suspense>
  );
}
