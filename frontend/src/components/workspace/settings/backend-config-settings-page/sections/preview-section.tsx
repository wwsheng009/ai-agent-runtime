// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { InfoIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import { SettingsSection } from "../../settings-section";
import { SummaryPill } from "../primitives";
import { type ConfigEditorCore } from "../use-config-core";


export function ConfigPreviewSection({ core }: { core: ConfigEditorCore }) {
  const {
    isPreviewFresh,
    previewAdditions,
    previewDiff,
    previewDocument,
    previewRemovals,
    t,
  } = core;

  return (
    previewDocument ? (
      <SettingsSection
        title={t("editor.preview.title")}
        description={t("editor.preview.description")}
      >
        <div className="rounded-panel border border-border bg-surface-softer p-3">
          <div className="mb-3 flex flex-wrap items-center justify-between gap-2.5">
            <div className="flex flex-wrap items-center gap-2">
              <SummaryPill
                label={t("editor.preview.added")}
                value={`${previewAdditions}`}
              />
              <SummaryPill
                label={t("editor.preview.removed")}
                value={`${previewRemovals}`}
              />
              <Badge>
                {isPreviewFresh
                  ? t("editor.preview.latest")
                  : t("editor.preview.expired")}
              </Badge>
              {previewDocument.restart_required ? (
                <Badge>{t("editor.preview.needsRestart")}</Badge>
              ) : null}
            </div>
            <details className="rounded-[0.75rem] border border-border bg-surface-solid px-2.5 py-1.5">
              <summary className="flex cursor-pointer list-none items-center gap-2 text-xs text-muted-foreground">
                <InfoIcon
                  size={14}
                  className="text-accent-primary"
                />
                {t("editor.preview.helpTitle")}
              </summary>
              <div className="mt-2.5 max-w-[30rem] text-sm leading-6 text-muted-foreground">
                {isPreviewFresh
                  ? t("editor.preview.helpFresh")
                  : t("editor.preview.helpStale")}
              </div>
            </details>
          </div>
          <div className="max-h-[27rem] overflow-auto rounded-[0.75rem] border border-border bg-black/15">
            {previewDiff.map((line, index) => (
              <div
                key={`${line.type}-${index}`}
                className={cn(
                  "grid grid-cols-[3.5rem_3.5rem_1.5rem_minmax(0,1fr)] px-3 py-1.5 font-mono app-text-12",
                  line.type === "add"
                    ? "bg-accent-teal/10 text-[#d6fff6]"
                    : line.type === "remove"
                      ? "bg-accent-orange/10 text-[#ffd9ce]"
                      : "text-foreground",
                )}
              >
                <div className="text-muted-foreground">
                  {line.beforeLine ?? ""}
                </div>
                <div className="text-muted-foreground">
                  {line.afterLine ?? ""}
                </div>
                <div>
                  {line.type === "add"
                    ? "+"
                    : line.type === "remove"
                      ? "-"
                      : " "}
                </div>
                <div className="whitespace-pre-wrap break-words">
                  {line.value || " "}
                </div>
              </div>
            ))}
          </div>
        </div>
      </SettingsSection>
    ) : null
  );
}
