// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type RuntimeAgentRoutePreviewResult, type RuntimeAgentRoutePreviewTask, previewRuntimeAgentRoute } from "@/lib/runtime-api";
import { type AgentRoutingScope, type RuntimeAgentRoutingConfigSummary, updateRuntimeAgentRoutingConfig, updateRuntimeTeamRoutingInheritance } from "../../runtime-agent-routing-domain-utils";
import { type ConfigEditorCore } from "../use-config-core";


export function createAgentRoutingDomain(core: ConfigEditorCore) {
  const {
    draftParsed,
    setDraftParsed,
    setStatusMessage,
    t,
  } = core;

  function handleAgentRoutingChange(
    scope: AgentRoutingScope,
    nextConfig: RuntimeAgentRoutingConfigSummary,
  ) {
    setDraftParsed((current: unknown) =>
      updateRuntimeAgentRoutingConfig(current, scope, nextConfig),
    );
    setStatusMessage(
      t("editor.messages.agentRoutingUpdated", {
        scope: t(`editor.agentRouting.scopes.${scope}` as never),
      }),
    );
  }

  async function handleAgentRoutePreview(
    scope: AgentRoutingScope,
    task: RuntimeAgentRoutePreviewTask,
  ): Promise<RuntimeAgentRoutePreviewResult> {
    if (draftParsed == null) {
      throw new Error(t("editor.agentRouting.preview.documentUnavailable"));
    }
    return previewRuntimeAgentRoute({
      document: {
        mode: "structured",
        parsed: draftParsed,
        changed_by: "web-runtime-config",
      },
      scope: scope === "teams" ? "team" : "subagent",
      workflow: scope === "teams" ? "spawn_team" : "spawn_agent",
      task,
    });
  }

  function handleTeamRoutingInheritanceChange(inherit: boolean) {
    setDraftParsed((current: unknown) =>
      updateRuntimeTeamRoutingInheritance(current, inherit),
    );
    setStatusMessage(
      inherit
        ? t("editor.messages.teamRoutingInherited")
        : t("editor.messages.teamRoutingIndependent"),
    );
  }


  return {
    handleAgentRoutingChange,
    handleAgentRoutePreview,
    handleTeamRoutingInheritanceChange,
  };
}

export type AgentRoutingDomain = ReturnType<typeof createAgentRoutingDomain>;
