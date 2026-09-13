// 由 components/workspace/artifact-detail-dialog.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

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
  return (
    <div className="overflow-hidden rounded-panel-lg border border-border bg-black/20">
      <div className="border-b border-border px-3.5 py-3">
        <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">
          Rendered image
        </div>
        <div className="mt-1 text-sm text-muted-foreground">
          Inspect the generated image at full width. Use the metadata below for prompt and integrity details.
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
  return (
    <div className="overflow-hidden rounded-panel-lg border border-border bg-black/20">
      <div className="border-b border-border px-3.5 py-3">
        <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">
          Image unavailable
        </div>
        <div className="mt-1 text-sm text-muted-foreground">
          This artifact was recorded as an image, but the MIME type is not renderable inline.
        </div>
      </div>
      <div className="p-4">
        <div className="rounded-card border border-border bg-surface-softer px-3.5 py-3">
          <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
            File name
          </div>
          <div className="mt-1.5 break-all text-sm text-foreground">
            {artifact.name}
          </div>
          <div className="mt-3 app-text-11 text-muted-foreground">
            MIME type {artifact.mimeType?.trim() || "unknown"} cannot be rendered inline.
          </div>
          <div className="mt-4">
            <Button
              onClick={() => {
                window.open(artifact.content, "_blank", "noopener,noreferrer");
              }}
              variant="secondary"
            >
              Open raw file
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
          Rendered preview
        </div>
        <div className="mt-1 text-sm text-muted-foreground">
          Use the full dialog width to inspect the rendered output.
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
          Source reader
        </div>
        <div className="mt-1 text-sm text-muted-foreground">
          Inspect the exact file contents without squeezing them into the rail.
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
  return (
    <div className="overflow-hidden rounded-panel-lg border border-border bg-black/20">
      <div className="border-b border-border px-3.5 py-3">
        <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">
          Source reader
        </div>
        <div className="mt-1 text-sm text-muted-foreground">
          Inspect the exact structured payload or file contents in a full-width dialog.
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
