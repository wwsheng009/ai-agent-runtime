// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { getConfigValueAtPath, setConfigValueAtPath } from "../../runtime-config-editor-utils";
import { isConfigRecord } from "../../runtime-provider-config-utils";
import { type RouteDraftInput, buildRouteRecordFromDraft } from "../../runtime-routing-domain-form-utils";
import { type RuntimeRoutingConfigSummary } from "../../runtime-routing-domain-utils";
import { type ConfigEditorCore } from "../use-config-core";


export function createRoutingDomain(core: ConfigEditorCore) {
  const {
    setDraftParsed,
    setError,
    setStatusMessage,
    t,
  } = core;

  function handleRoutingConfigChange(
    nextRoutingConfig: RuntimeRoutingConfigSummary,
  ) {
    setDraftParsed((current: unknown) => {
      const currentRoutingValue = getConfigValueAtPath(current, ["routing"]);
      const currentRouting = isConfigRecord(currentRoutingValue)
        ? currentRoutingValue
        : {};

      return setConfigValueAtPath(current, ["routing"], {
        ...currentRouting,
        strategy: nextRoutingConfig.strategy,
        failover: nextRoutingConfig.failover,
      });
    });
  }

  function handleSaveRoute(
    draft: RouteDraftInput,
    editingIndex: number | null,
  ) {
    const nextRoute = buildRouteRecordFromDraft(draft);
    const routeRecord = nextRoute.record;
    if (!routeRecord) {
      return nextRoute.error ?? t("editor.validation.routeInvalid");
    }

    setDraftParsed((current: unknown) => {
      const currentRoutingValue = getConfigValueAtPath(current, ["routing"]);
      const currentRouting = isConfigRecord(currentRoutingValue)
        ? currentRoutingValue
        : {};
      const currentRoutes = Array.isArray(currentRouting.routes)
        ? [...currentRouting.routes]
        : [];

      if (editingIndex == null) {
        currentRoutes.push(routeRecord);
      } else {
        currentRoutes[editingIndex] = routeRecord;
      }

      return setConfigValueAtPath(current, ["routing"], {
        ...currentRouting,
        routes: currentRoutes,
      });
    });
    setError(null);
    setStatusMessage(
      editingIndex == null
        ? t("editor.messages.routeCreated")
        : t("editor.messages.routeUpdated", { index: String(editingIndex + 1) }),
    );
    return null;
  }

  function handleDeleteRoute(index: number) {
    if (!window.confirm(t("editor.messages.confirmDeleteRoute", { index: String(index + 1) }))) {
      return;
    }

    setDraftParsed((current: unknown) => {
      const currentRoutingValue = getConfigValueAtPath(current, ["routing"]);
      const currentRouting = isConfigRecord(currentRoutingValue)
        ? currentRoutingValue
        : {};
      const currentRoutes = Array.isArray(currentRouting.routes)
        ? [...currentRouting.routes]
        : [];
      currentRoutes.splice(index, 1);

      return setConfigValueAtPath(current, ["routing"], {
        ...currentRouting,
        routes: currentRoutes,
      });
    });
    setStatusMessage(t("editor.messages.routeDeleted", { index: String(index + 1) }));
  }

  function handleMoveRoute(index: number, direction: "up" | "down") {
    setDraftParsed((current: unknown) => {
      const currentRoutingValue = getConfigValueAtPath(current, ["routing"]);
      const currentRouting = isConfigRecord(currentRoutingValue)
        ? currentRoutingValue
        : {};
      const currentRoutes = Array.isArray(currentRouting.routes)
        ? [...currentRouting.routes]
        : [];
      const targetIndex = direction === "up" ? index - 1 : index + 1;

      if (
        index < 0 ||
        index >= currentRoutes.length ||
        targetIndex < 0 ||
        targetIndex >= currentRoutes.length
      ) {
        return current;
      }

      const [route] = currentRoutes.splice(index, 1);
      currentRoutes.splice(targetIndex, 0, route);

      return setConfigValueAtPath(current, ["routing"], {
        ...currentRouting,
        routes: currentRoutes,
      });
    });
    setStatusMessage(
      direction === "up"
        ? t("editor.messages.routeMovedUp", { index: String(index + 1) })
        : t("editor.messages.routeMovedDown", { index: String(index + 1) }),
    );
  }


  return {
    handleRoutingConfigChange,
    handleSaveRoute,
    handleDeleteRoute,
    handleMoveRoute,
  };
}

export type RoutingDomain = ReturnType<typeof createRoutingDomain>;
