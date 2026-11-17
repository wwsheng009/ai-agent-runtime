// 由 components/workspace/artifact-detail-dialog.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";

import { injectPreviewDocumentSettings } from "@/components/workspace/artifact-preview-document";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import { useFocusTrap } from "@/components/ui/use-focus-trap";
import { useAppSettings } from "@/core/settings";
import { useFocusRestore } from "@/hooks/workspace/use-focus-restore";
import { classifyArtifactCategory } from "@/lib/workspace-artifacts";

import {
  ArtifactPreviewPane,
  ArtifactSourcePane,
  ArtifactSourceReaderCard,
  ImageArtifactPane,
  ImageUnavailablePane,
} from "./artifact-detail-dialog/artifact-panes";
import { ArtifactDetailHeader } from "./artifact-detail-dialog/dialog-header";
import {
  buildMetaItems,
  formatBytes,
} from "./artifact-detail-dialog/dialog-helpers";
import { ArtifactMetadataAside } from "./artifact-detail-dialog/metadata-aside";
import { ArtifactReadingTabs } from "./artifact-detail-dialog/reading-tabs";
import type { ArtifactDetailDialogProps } from "./artifact-detail-dialog/types";

export function ArtifactDetailDialog({
  artifact,
  onClose,
  open,
}: ArtifactDetailDialogProps) {
  const { settings } = useAppSettings();
  const titleId = useId();
  const descriptionId = useId();
  const previewTabId = useId();
  const sourceTabId = useId();
  const previewPanelId = useId();
  const sourcePanelId = useId();
  const panelRef = useRef<HTMLDivElement | null>(null);
  const [preferredView, setPreferredView] = useState<"preview" | "source">(
    "preview",
  );

  useEffect(() => {
    if (!artifact) {
      return;
    }

    // P0-2 机械搬迁：保留原「artifact 切换时同步 reading mode」语义。
     
    setPreferredView(artifact.previewHtml ? "preview" : "source");
  }, [artifact, artifact?.id, artifact?.previewHtml]);

  useDialogLifecycle(open, onClose);
  useFocusTrap(panelRef, open);
  useFocusRestore(open);

  useEffect(() => {
    if (open) {
      panelRef.current?.focus();
    }
  }, [open]);

  if (!open || !artifact) {
    return null;
  }

  if (typeof document === "undefined") {
    return null;
  }

  const category = classifyArtifactCategory(artifact);
  const view = artifact.previewHtml ? preferredView : "source";
  const previewDocument = artifact.previewHtml
    ? injectPreviewDocumentSettings(artifact.previewHtml, {
        codeTextSize: settings.appearance.codeTextSize,
        textSize: settings.appearance.textSize,
      })
    : null;
  const metaItems = buildMetaItems(artifact, view);
  const isImageArtifact = artifact.kind === "image";
  const isRenderableImage =
    isImageArtifact &&
    (!artifact.mimeType || artifact.mimeType.toLowerCase().startsWith("image/"));
  const imageDetails = isImageArtifact
    ? [
        {
          label: "Revised prompt",
          value: artifact.revisedPrompt?.trim() || "—",
        },
        {
          label: "MIME type",
          value: artifact.mimeType?.trim() || "image/png",
        },
        {
          label: "SHA-256",
          value: artifact.sha256?.trim() || "—",
        },
        {
          label: "Byte count",
          value:
            artifact.byteCount != null ? formatBytes(artifact.byteCount) : "—",
        },
      ]
    : [];

  return createPortal(
    <div
      className="fixed inset-0 z-[130] flex items-center justify-center bg-dialog-backdrop px-3 py-4 backdrop-blur-sm"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          onClose();
        }
      }}
    >
      <div
        ref={panelRef}
        aria-describedby={descriptionId}
        aria-labelledby={titleId}
        aria-modal="true"
        className="flex max-h-[calc(100vh-1.5rem)] w-full max-w-[min(90rem,calc(100vw-1.5rem))] flex-col overflow-hidden rounded-panel-lg border border-border [background:var(--dialog-bg)] shadow-[0_18px_48px_rgba(0,0,0,0.28)]"
        data-artifact-detail-dialog="true"
        role="dialog"
        tabIndex={-1}
      >

        <ArtifactDetailHeader
          artifact={artifact}
          category={category}
          descriptionId={descriptionId}
          onClose={onClose}
          titleId={titleId}
        />

        <div className="grid min-h-0 flex-1 gap-0 xl:grid-cols-[18rem_minmax(0,1fr)]">

          <ArtifactMetadataAside artifact={artifact} metaItems={metaItems} />

          <section className="flex min-h-0 flex-col overflow-hidden">
            {artifact.previewHtml ? (
              <ArtifactReadingTabs
                onSelectView={setPreferredView}
                previewPanelId={previewPanelId}
                previewTabId={previewTabId}
                sourcePanelId={sourcePanelId}
                sourceTabId={sourceTabId}
                view={view}
              />
            ) : null}
            <div className="app-scrollbar min-h-0 flex-1 overflow-y-auto p-4">
            {isImageArtifact ? (
              isRenderableImage ? (
                <ImageArtifactPane
                  artifact={artifact}
                  imageDetails={imageDetails}
                />
              ) : (
                <ImageUnavailablePane artifact={artifact} />
              )
            ) : artifact.previewHtml ? (
              <>
                <ArtifactPreviewPane
                  artifact={artifact}
                  previewDocument={previewDocument}
                  previewPanelId={previewPanelId}
                  previewTabId={previewTabId}
                  view={view}
                />
                <ArtifactSourcePane
                  artifact={artifact}
                  sourcePanelId={sourcePanelId}
                  sourceTabId={sourceTabId}
                  view={view}
                />
              </>
            ) : (
              <ArtifactSourceReaderCard artifact={artifact} />
            )}
            </div>
          </section>
        </div>
      </div>
    </div>,
    document.body,
  );
}
