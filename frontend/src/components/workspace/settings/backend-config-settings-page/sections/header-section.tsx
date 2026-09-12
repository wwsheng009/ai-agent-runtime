// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { InfoIcon } from "lucide-react";
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
    <SettingsSection
      title={t("editor.title")}
      description={t("editor.description")}
    >
      <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3 py-2.5">
        <div className="flex flex-wrap items-center justify-between gap-2.5">
          <div className="flex flex-wrap items-center gap-2">
            <Badge>{t("editor.independentBadge")}</Badge>
            <Badge>{getModeLabel(mode, translatedModeMenuEntries)}</Badge>
            {hasUnsavedChanges ? <Badge>{t("editor.unsavedBadge")}</Badge> : null}
          </div>
          <details className="rounded-[0.75rem] border border-[var(--border)] bg-[var(--surface-solid)] px-2.5 py-1.5">
            <summary className="flex cursor-pointer list-none items-center gap-2 text-xs text-[var(--muted-foreground)]">
              <InfoIcon size={14} className="text-[var(--accent-primary)]" />
              {t("editor.usage.title")}
            </summary>
            <div className="mt-2.5 max-w-[32rem] text-sm leading-6 text-[var(--muted-foreground)]">
              {t("editor.usage.body")}
            </div>
          </details>
        </div>
      </div>

      <ConfigEditorSummaryGrid core={core} />

      <ConfigEditorControlBar core={core} />
    </SettingsSection>
  );
}
