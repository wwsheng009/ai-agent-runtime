// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { Badge } from "@/components/ui/badge";
import { SettingsSection } from "../../settings-section";
import { getModeLabel } from "../format";
import { ConfigEditorControlBar } from "./control-bar";
import { ConfigEditorSummaryGrid } from "./summary-grid";
import { type ConfigEditorCore } from "../use-config-core";


export function ConfigEditorHeaderSection({ core }: { core: ConfigEditorCore }) {
  const {
    hasUnsavedChanges,
    mode,
    t,
    translatedModeMenuEntries,
  } = core;

  return (
    <SettingsSection title={t("editor.title")}>
      <div className="flex flex-wrap items-center gap-2 rounded-panel border border-border bg-surface-softer px-3 py-2.5">
        <Badge>{t("editor.independentBadge")}</Badge>
        <Badge>{getModeLabel(mode, translatedModeMenuEntries)}</Badge>
        {hasUnsavedChanges ? <Badge>{t("editor.unsavedBadge")}</Badge> : null}
      </div>

      <ConfigEditorSummaryGrid core={core} />

      <ConfigEditorControlBar core={core} />
    </SettingsSection>
  );
}
