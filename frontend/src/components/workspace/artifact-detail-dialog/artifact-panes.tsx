// 由 components/workspace/artifact-detail-dialog.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { CodeBlock } from "@/components/ui/code-block";
import { type Artifact } from "@/data/mock";

import type { ArtifactDetailView, ArtifactMetaItem } from "./types";

type ImageArtifactPaneProps = {
  artifact: Artifact;
  imageDetails: ArtifactMetaItem[];
};

export function ImageArtifactPane({
  artifact,
  imageDetails,
}: ImageArtifactPaneProps) {
  const { t } = useTranslation("workspace");

  return (
    <div className="overflow-hidden rounded-panel-lg border border-border bg-black/20">
      <div className="border-b border-border px-3.5 py-3">
        <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">
          {t("panels.artifacts.detail.imageTitle")}
        </div>
        <div className="mt-1 text-sm text-muted-foreground">
          {t("panels.artifacts.detail.imageHint")}
        </div>
      </div>
      <div className="p-4">
        <div className="flex min-h-[18rem] items-center justify-center rounded-panel border border-white/8 bg-black/40 p-4">
          <img
            alt={artifact.revisedPrompt?.trim() || artifact.name}
            className="max-h-[70vh] max-w-full rounded-[0.75rem] border border-white/10 object-contain"
            src={artifact.content}
          />
        </div>
        <div className="mt-4 grid gap-2">
          {imageDetails.map((item) => (
            <div
              key={`${artifact.id}-${item.label}`}
              className="rounded-card border border-border bg-surface-softer px-3 py-2.5"
            >
              <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                {item.label}
              </div>
              <div className="mt-1.5 break-all text-sm text-foreground">
                {item.value}
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

type ImageUnavailablePaneProps = {
  artifact: Artifact;
};

export function ImageUnavailablePane({ artifact }: ImageUnavailablePaneProps) {
  const { t } = useTranslation("workspace");

  return (
    <div className="overflow-hidden rounded-panel-lg border border-border bg-black/20">
      <div className="border-b border-border px-3.5 py-3">
        <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">
          {t("panels.artifacts.detail.imageUnavailableTitle")}
        </div>
        <div className="mt-1 text-sm text-muted-foreground">
          {t("panels.artifacts.detail.imageUnavailableHint")}
        </div>
      </div>
      <div className="p-4">
        <div className="rounded-card border border-border bg-surface-softer px-3.5 py-3">
          <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
            {t("panels.artifacts.detail.fileName")}
          </div>
          <div className="mt-1.5 break-all text-sm text-foreground">
            {artifact.name}
          </div>
          <div className="mt-3 app-text-11 text-muted-foreground">
            {t("panels.artifacts.detail.mimeRenderHint", {
              mime: artifact.mimeType?.trim() || "unknown",
            })}
          </div>
          <div className="mt-4">
            <Button
              onClick={() => {
                window.open(artifact.content, "_blank", "noopener,noreferrer");
              }}
              variant="secondary"
            >
              {t("panels.artifacts.detail.openRawFile")}
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}

type ArtifactPreviewPaneProps = {
  artifact: Artifact;
  previewDocument: string | null;
  previewPanelId: string;
  previewTabId: string;
  view: ArtifactDetailView;
};

export function ArtifactPreviewPane({
  artifact,
  previewDocument,
  previewPanelId,
  previewTabId,
  view,
}: ArtifactPreviewPaneProps) {
  const { t } = useTranslation("workspace");

  return (
    <div
      aria-labelledby={previewTabId}
      className="min-h-full overflow-hidden rounded-panel-lg border border-border bg-black/20"
      hidden={view !== "preview"}
      id={previewPanelId}
      role="tabpanel"
      tabIndex={0}
    >
      <div className="border-b border-border px-3.5 py-3">
        <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">
          {t("panels.artifacts.detail.previewTitle")}
        </div>
        <div className="mt-1 text-sm text-muted-foreground">
          {t("panels.artifacts.detail.previewHint")}
        </div>
      </div>
      <div className="p-4">
        <div className="h-[min(70vh,56rem)] min-h-[28rem] rounded-card-lg border border-white/8 bg-white p-3">
          <iframe
            title={artifact.name}
            srcDoc={previewDocument ?? artifact.previewHtml}
            className="h-full w-full rounded-[0.75rem] border border-slate-200 bg-white"
            sandbox="allow-scripts allow-same-origin"
          />
        </div>
      </div>
    </div>
  );
}

type ArtifactSourcePaneProps = {
  artifact: Artifact;
  sourcePanelId: string;
  sourceTabId: string;
  view: ArtifactDetailView;
};

export function ArtifactSourcePane({
  artifact,
  sourcePanelId,
  sourceTabId,
  view,
}: ArtifactSourcePaneProps) {
  const { t } = useTranslation("workspace");

  return (
    <div
      aria-labelledby={sourceTabId}
      className="overflow-hidden rounded-panel-lg border border-border bg-black/20"
      hidden={view !== "source"}
      id={sourcePanelId}
      role="tabpanel"
      tabIndex={0}
    >
      <div className="border-b border-border px-3.5 py-3">
        <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">
          {t("panels.artifacts.detail.sourceTitle")}
        </div>
        <div className="mt-1 text-sm text-muted-foreground">
          {t("panels.artifacts.detail.sourceHint")}
        </div>
      </div>
      <div className="p-4">
        <CodeBlock
          code={artifact.content}
          language={artifact.language ?? "json"}
          title={artifact.path}
        />
      </div>
    </div>
  );
}

type ArtifactSourceReaderCardProps = {
  artifact: Artifact;
};

export function ArtifactSourceReaderCard({
  artifact,
}: ArtifactSourceReaderCardProps) {
  const { t } = useTranslation("workspace");

  return (
    <div className="overflow-hidden rounded-panel-lg border border-border bg-black/20">
      <div className="border-b border-border px-3.5 py-3">
        <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">
          {t("panels.artifacts.detail.sourceTitle")}
        </div>
        <div className="mt-1 text-sm text-muted-foreground">
          {t("panels.artifacts.detail.sourceFullHint")}
        </div>
      </div>
      <div className="p-4">
        <CodeBlock
          code={artifact.content}
          language={artifact.language ?? "json"}
          title={artifact.path}
        />
      </div>
    </div>
  );
}
