// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { FileTextIcon, HardDriveDownloadIcon, LoaderCircleIcon, RotateCcwIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { type ConfigEditorCore } from "../use-config-core";


export function ConfigUnsavedBar({ core }: { core: ConfigEditorCore }) {
  const {
    canSaveAndRestart,
    generatePreview,
    hasUnsavedChanges,
    isModeSwitching,
    isPreviewLoading,
    isRestarting,
    isSaving,
    saveAndRestartDocument,
    saveDocument,
    t,
  } = core;

  return (
    hasUnsavedChanges ? (
      <div className="mt-3">
        <div className="rounded-panel border border-accent-primary-border bg-accent-primary-soft px-3 py-2.5">
          <div className="flex flex-col gap-2.5 lg:flex-row lg:items-center lg:justify-between">
            <div className="flex flex-wrap items-center gap-2">
              <Badge>{t("editor.sticky.unsaved")}</Badge>
              <div className="text-sm text-muted-foreground">{t("editor.sticky.hint")}</div>
            </div>
            <div className="flex flex-wrap items-center gap-2">
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
                {t("editor.sticky.previewButton")}
              </Button>
              <Button
                variant={canSaveAndRestart ? "secondary" : "primary"}
                size="sm"
                onClick={() => void saveDocument()}
                disabled={isSaving || isModeSwitching}
              >
                {isSaving ? (
                  <LoaderCircleIcon size={14} className="animate-spin" />
                ) : (
                  <HardDriveDownloadIcon size={14} />
                )}
                {t("editor.sticky.saveButton")}
              </Button>
              {canSaveAndRestart ? (
                <Button
                  size="sm"
                  onClick={() => void saveAndRestartDocument()}
                  disabled={isSaving || isRestarting || isModeSwitching}
                >
                  {isSaving || isRestarting ? (
                    <LoaderCircleIcon size={14} className="animate-spin" />
                  ) : (
                    <RotateCcwIcon size={14} />
                  )}
                  {t("editor.sticky.saveAndRestartButton")}
                </Button>
              ) : null}
            </div>
          </div>
        </div>
      </div>
    ) : null
  );
}
