// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { FileTextIcon, HardDriveDownloadIcon, LoaderCircleIcon, RefreshCcwIcon, RotateCcwIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { getModeLabel } from "../format";
import { type ConfigEditorCore } from "../use-config-core";


export function ConfigEditorControlBar({ core }: { core: ConfigEditorCore }) {
  const {
    enabledProviderCount,
    error,
    generatePreview,
    hasUnsavedChanges,
    isLoading,
    isModeSwitching,
    isPreviewFresh,
    isPreviewLoading,
    isRestarting,
    isSaving,
    mode,
    previewDiff,
    previewDocument,
    previewRequiresRestart,
    reloadDocument,
    restartService,
    saveDocument,
    savedRequiresRestart,
    statusMessage,
    t,
    tCommon,
    translatedModeMenuEntries,
  } = core;

  return (
    <div className="sticky top-2 z-20 mt-2.5 rounded-panel-lg border border-border bg-surface-softer p-3">
      <div className="flex flex-wrap items-center justify-between gap-2.5">
        <div className="flex flex-wrap items-center gap-2">
          <Badge>
            {hasUnsavedChanges
              ? tCommon("states.unsynced")
              : tCommon("states.synced")}
          </Badge>
          <Badge>
            {mode === "source" ? t("editor.sourceFocus") : t("editor.structuredFocus")}
          </Badge>
          {previewDocument ? (
            <Badge>
              {isPreviewFresh
                ? t("editor.preview.latestWithCount", {
                    count: previewDiff.length,
                  })
                : t("editor.preview.expired")}
            </Badge>
          ) : null}
          {previewRequiresRestart ? <Badge>{t("editor.preview.needsRestart")}</Badge> : null}
          {savedRequiresRestart && !hasUnsavedChanges ? (
            <Badge>{t("editor.preview.needsRestart")}</Badge>
          ) : null}
          {isModeSwitching ? <Badge>{t("editor.status.switchingMode")}</Badge> : null}
          {mode === "providers" ? (
            <Badge>
              {t("editor.counts.enabledProviders", {
                count: enabledProviderCount,
              })}
            </Badge>
          ) : null}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="ghost"
            size="sm"
            onClick={() => void reloadDocument()}
            disabled={isLoading || isModeSwitching}
          >
            {isLoading ? (
              <LoaderCircleIcon size={14} className="animate-spin" />
            ) : (
              <RefreshCcwIcon size={14} />
            )}
            {t("editor.controls.reload")}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void generatePreview()}
            disabled={isPreviewLoading || isModeSwitching}
          >
            {isPreviewLoading ? (
              <LoaderCircleIcon size={14} className="animate-spin" />
            ) : (
              <FileTextIcon size={14} />
            )}
            {t("editor.controls.preview")}
          </Button>
          <Button
            size="sm"
            onClick={() => void saveDocument()}
            disabled={isSaving || isModeSwitching}
          >
            {isSaving ? (
              <LoaderCircleIcon size={14} className="animate-spin" />
            ) : (
              <HardDriveDownloadIcon size={14} />
            )}
            {t("editor.controls.save")}
          </Button>
          <Button
            variant={
              savedRequiresRestart && !hasUnsavedChanges
                ? "primary"
                : "secondary"
            }
            size="sm"
            onClick={() => void restartService()}
            disabled={isRestarting || isModeSwitching}
          >
            {isRestarting ? (
              <LoaderCircleIcon size={14} className="animate-spin" />
            ) : (
              <RotateCcwIcon size={14} />
            )}
            {savedRequiresRestart && !hasUnsavedChanges
              ? t("editor.controls.restartWithEffect")
              : t("editor.controls.restart")}
          </Button>
        </div>
      </div>
      <div className="mt-2.5 text-xs text-muted-foreground">
        {t("editor.currentFocusPrefix")}
        {mode === "source"
          ? t("editor.sourceFocus")
          : getModeLabel(mode, translatedModeMenuEntries)}
      </div>
      {statusMessage ? (
        <div className="mt-2.5 rounded-[0.75rem] border border-accent-teal/24 bg-accent-teal/10 px-3 py-2.5 text-sm">
          {statusMessage}
        </div>
      ) : null}
      {error ? (
        <div className="mt-2.5 rounded-[0.75rem] border border-accent-orange/24 bg-accent-orange/10 px-3 py-2.5 text-sm">
          {error}
        </div>
      ) : null}
    </div>
  );
}
