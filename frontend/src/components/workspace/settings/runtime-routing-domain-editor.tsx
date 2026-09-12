import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { type RouteDraftInput } from "./runtime-routing-domain-form-utils";
import { type RuntimeRouteSummary } from "./runtime-routing-domain-utils";
import { createRouteDraftInput } from "./runtime-routing-domain-editor/draft-utils";
import { RuntimeRoutingOverviewSection } from "./runtime-routing-domain-editor/overview-section";
import { RuntimeRoutingRouteDialog } from "./runtime-routing-domain-editor/route-dialog";
import { RuntimeRoutingRoutesTable } from "./runtime-routing-domain-editor/routes-table";
import { type RuntimeRoutingDomainEditorProps } from "./runtime-routing-domain-editor/types";

export function RuntimeRoutingDomainEditor({
  availableGroups,
  onChangeConfig,
  onDeleteRoute,
  onMoveRoute,
  onSaveRoute,
  routeConfig,
  routes,
}: RuntimeRoutingDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [editingIndex, setEditingIndex] = useState<number | null>(null);
  const [draft, setDraft] = useState<RouteDraftInput>(() =>
    createRouteDraftInput(null),
  );

  const protocolCount = useMemo(
    () =>
      new Set(routes.map((route) => route.protocol).filter(Boolean)).size,
    [routes],
  );

  function openCreateDialog() {
    setDialogError(null);
    setEditingIndex(null);
    setDraft(createRouteDraftInput(null));
    setDialogOpen(true);
  }

  function openEditDialog(route: RuntimeRouteSummary) {
    setDialogError(null);
    setEditingIndex(route.index);
    setDraft(createRouteDraftInput(route));
    setDialogOpen(true);
  }

  function handleSave() {
    const error = onSaveRoute(draft, editingIndex);
    if (error) {
      setDialogError(error);
      return;
    }
    setDialogOpen(false);
  }

  return (
    <div className="space-y-3">
      <RuntimeRoutingOverviewSection
        onChangeConfig={onChangeConfig}
        protocolCount={protocolCount}
        routeConfig={routeConfig}
        routes={routes}
        t={t}
      />

      <RuntimeRoutingRoutesTable
        availableGroups={availableGroups}
        onDeleteRoute={onDeleteRoute}
        onMoveRoute={onMoveRoute}
        openCreateDialog={openCreateDialog}
        openEditDialog={openEditDialog}
        routeConfig={routeConfig}
        routes={routes}
        t={t}
      />

      <RuntimeRoutingRouteDialog
        availableGroups={availableGroups}
        dialogError={dialogError}
        dialogOpen={dialogOpen}
        draft={draft}
        editingIndex={editingIndex}
        handleSave={handleSave}
        setDialogOpen={setDialogOpen}
        setDraft={setDraft}
        t={t}
      />
    </div>
  );
}
