// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { RotateCcwIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { type RuntimeConfigDocument } from "@/lib/runtime-api";
import { cn } from "@/lib/utils";
import { SettingsSection } from "../settings-section";
import { countImpactPaths } from "./format";


export function ImpactStat({
  accentClassName,
  detail,
  label,
  value,
}: {
  accentClassName: string;
  detail: string;
  label: string;
  value: string;
}) {
  return (
    <div className="rounded-card border border-border bg-surface-solid px-3 py-2.5">
      <div className="flex items-center justify-between gap-3">
        <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
          {label}
        </div>
        <span className={cn("text-sm font-semibold", accentClassName)}>
          {value}
        </span>
      </div>
      <div className="mt-1.5 text-xs leading-5 text-muted-foreground">
        {detail}
      </div>
    </div>
  );
}

export function ImpactPathList({
  emptyText,
  paths,
  title,
  toneClassName,
}: {
  emptyText: string;
  paths?: string[];
  title: string;
  toneClassName: string;
}) {
  const { t } = useTranslation("runtimeConfig");
  const visiblePaths = Array.isArray(paths) ? paths.slice(0, 6) : [];
  const hiddenCount = Math.max(
    0,
    countImpactPaths(paths) - visiblePaths.length,
  );

  return (
    <div className="rounded-card border border-border bg-surface-solid p-3">
        <div className="flex items-center justify-between gap-3">
        <div className="text-sm font-semibold text-foreground">
          {title}
        </div>
        <Badge className={toneClassName}>
          {t("editor.counts.pathCount", { count: countImpactPaths(paths) })}
        </Badge>
      </div>
      {visiblePaths.length > 0 ? (
        <div className="mt-2 grid gap-1.5">
          {visiblePaths.map((path) => (
            <div
              key={path}
              className="rounded-field border border-border bg-surface-softer px-2.5 py-1.5 font-mono app-text-12 text-foreground"
            >
              {path}
            </div>
          ))}
          {hiddenCount > 0 ? (
            <div className="text-xs text-muted-foreground">
              {t("editor.counts.hiddenCount", { count: hiddenCount })}
            </div>
          ) : null}
        </div>
      ) : (
        <div className="mt-2 text-sm leading-6 text-muted-foreground">
          {emptyText}
        </div>
      )}
    </div>
  );
}
export function RuntimeImpactPanel({
  document,
  onRestart,
  serviceRunning,
}: {
  document: RuntimeConfigDocument;
  onRestart: () => void;
  serviceRunning: boolean;
}) {
  const { t } = useTranslation("runtimeConfig");
  const impact = document.runtime_impact;
  if (!impact || countImpactPaths(impact.changed_paths) === 0) {
    return null;
  }

  const changedCount = countImpactPaths(impact.changed_paths);
  const hotReloadCount = countImpactPaths(impact.hot_reload_paths);
  const restartCount = countImpactPaths(impact.restart_required_paths);
  const inactiveCount = countImpactPaths(impact.inactive_paths);
  const appliedCount = countImpactPaths(impact.applied_paths);
  const previewWarning = t("editor.validation.previewWarning");
  const warnings = (document.warnings ?? []).filter(
    (warning) => warning !== previewWarning,
  );
  const isPreview = document.updated_at == null;

  return (
    <SettingsSection
      title={
        isPreview
          ? t("editor.impact.previewTitle")
          : t("editor.impact.runtimeTitle")
      }
      description={
        isPreview
          ? t("editor.impact.previewDescription")
          : t("editor.impact.runtimeDescription")
      }
    >
      <div className="rounded-panel-lg border border-border bg-surface-softer p-3">
        <div className="flex flex-wrap items-center justify-between gap-2.5">
          <div className="flex flex-wrap items-center gap-2">
            <Badge>{isPreview ? t("editor.impact.previewBadge") : t("editor.impact.savedBadge")}</Badge>
            <Badge>{t("editor.impact.changedCount", { count: changedCount })}</Badge>
            <Badge>
              {document.restart_required
                ? t("editor.impact.needsRestart")
                : t("editor.impact.noRestart")}
            </Badge>
            {appliedCount > 0 ? (
              <Badge>{t("editor.impact.appliedCount", { count: appliedCount })}</Badge>
            ) : null}
          </div>
          {document.restart_required ? (
            <Button
              variant={serviceRunning ? "primary" : "secondary"}
              size="sm"
              onClick={onRestart}
            >
              <RotateCcwIcon size={14} />
              {t("editor.impact.restartToApply")}
            </Button>
          ) : null}
        </div>

        <div className="mt-3 grid gap-2.5 md:grid-cols-2 xl:grid-cols-4">
          <ImpactStat
            label={t("editor.impact.stats.changed")}
            value={`${changedCount}`}
            detail={t("editor.impact.details.changed")}
            accentClassName="text-foreground"
          />
          <ImpactStat
            label={t("editor.impact.stats.hotReload")}
            value={`${hotReloadCount}`}
            detail={
              appliedCount > 0
                ? t("editor.impact.details.hotReloadApplied")
                : t("editor.impact.details.hotReload")
            }
            accentClassName="text-accent-teal"
          />
          <ImpactStat
            label={t("editor.impact.stats.restart")}
            value={`${restartCount}`}
            detail={t("editor.impact.details.restart")}
            accentClassName="text-accent-orange"
          />
          <ImpactStat
            label={t("editor.impact.stats.inactive")}
            value={`${inactiveCount}`}
            detail={t("editor.impact.details.inactive")}
            accentClassName="text-[#e7d58c]"
          />
        </div>

        <div className="mt-3 grid gap-3 xl:grid-cols-2">
          <ImpactPathList
            title={t("editor.impact.paths.applied")}
            paths={impact.applied_paths}
            emptyText={t("editor.impact.paths.emptyApplied")}
            toneClassName="border-accent-teal/24 bg-accent-teal/10 text-[#d6fff6]"
          />
          <ImpactPathList
            title={t("editor.impact.paths.restart")}
            paths={impact.restart_required_paths}
            emptyText={t("editor.impact.paths.emptyRestart")}
            toneClassName="border-accent-orange/24 bg-accent-orange/10 text-[#ffd9ce]"
          />
          <ImpactPathList
            title={t("editor.impact.paths.hotReload")}
            paths={impact.hot_reload_paths}
            emptyText={t("editor.impact.paths.emptyHotReload")}
            toneClassName="border-accent-teal/24 bg-accent-teal/10 text-[#d6fff6]"
          />
          <ImpactPathList
            title={t("editor.impact.paths.inactive")}
            paths={impact.inactive_paths}
            emptyText={t("editor.impact.paths.emptyInactive")}
            toneClassName="border-[#e7d58c]/24 bg-[#e7d58c]/10 text-[#fff3c4]"
          />
        </div>

        {warnings.length > 0 ? (
          <div className="mt-3 rounded-card border border-border bg-surface-solid p-3">
            <div className="text-sm font-semibold text-foreground">
              {t("editor.impact.warningsTitle")}
            </div>
            <div className="mt-2 grid gap-2">
              {warnings.map((warning, index) => (
                <div
                  key={`${warning}-${index}`}
                  className="rounded-field border border-border bg-surface-softer px-2.5 py-2 text-sm leading-6 text-muted-foreground"
                >
                  {warning}
                </div>
              ))}
            </div>
          </div>
        ) : null}
      </div>
    </SettingsSection>
  );
}
