// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Badge } from "@/components/ui/badge";
import { ConfigEditorControlBar } from "./control-bar";
import { ConfigEditorSummaryGrid } from "./summary-grid";
import { type ConfigEditorCore } from "../use-config-core";


export function ConfigEditorHeaderSection({ core }: { core: ConfigEditorCore }) {
  const {
    document,
    hasUnsavedChanges,
    t,
  } = core;
  const providerLayers = (document?.layers ?? []).filter(
    (layer) => layer.present && layer.fragment,
  );
  const providerLayer = providerLayers[providerLayers.length - 1];

  return (
    <section className="space-y-3 scroll-mt-28">
      {providerLayer || hasUnsavedChanges ? (
        <div className="flex flex-wrap items-center gap-2 rounded-panel border border-border bg-surface-softer px-3 py-2.5">
          {providerLayer ? (
            <span title={t("editor.providerFileBadgeTitle", { path: providerLayer.path })}>
              <Badge>{t("editor.providerFileBadge")}</Badge>
            </span>
          ) : null}
          {hasUnsavedChanges ? <Badge>{t("editor.unsavedBadge")}</Badge> : null}
        </div>
      ) : null}

      <ConfigEditorSummaryGrid core={core} />

      <ConfigEditorControlBar core={core} />
    </section>
  );
}
