// 由 components/workspace/artifact-panel.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import {
  FileCode2Icon,
  FileJsonIcon,
  GlobeIcon,
  ImageIcon,
  SparklesIcon,
} from "lucide-react";

import { type Artifact } from "@/data/mock";
import { cn } from "@/lib/utils";
import { classifyArtifactCategory } from "@/lib/workspace-artifacts";

function iconForArtifact(kind: Artifact["kind"]) {
  if (kind === "image") {
    return ImageIcon;
  }
  if (kind === "html") {
    return GlobeIcon;
  }
  if (kind === "json") {
    return FileJsonIcon;
  }
  return FileCode2Icon;
}

function ArtifactList({
  items,
  onOpenArtifact,
  selectedArtifactId,
}: {
  items: Artifact[];
  onOpenArtifact: (artifactId: string) => void;
  selectedArtifactId: string | null;
}) {
  if (items.length === 0) {
    return null;
  }

  return (
    <div className="space-y-0.5">
      {items.map((artifact) => {
        const Icon = iconForArtifact(artifact.kind);
        const category = classifyArtifactCategory(artifact);
        const isActive = artifact.id === selectedArtifactId;
        const showImageThumbnail =
          artifact.kind === "image" &&
          (!artifact.mimeType || artifact.mimeType.toLowerCase().startsWith("image/"));

        return (
          <button
            aria-pressed={isActive}
            key={artifact.id}
            type="button"
            onClick={() => onOpenArtifact(artifact.id)}
            title={`${artifact.path}\n${artifact.summary}`}
            className={cn(
              "w-full rounded-[0.65rem] border px-1.5 py-1 text-left transition",
              isActive
                ? "border-accent-gold/30 bg-accent-gold/8 shadow-[inset_0_1px_0_rgba(240,199,123,0.08)]"
                : "border-white/8 bg-white/4 hover:border-white/14 hover:bg-white/8",
            )}
          >
            <div className="flex items-center gap-2.5">
              {showImageThumbnail ? (
                <span
                  className={cn(
                    "inline-flex h-10 w-10 shrink-0 overflow-hidden rounded-[0.7rem] border",
                    category === "evidence"
                      ? "border-accent-teal/18 bg-accent-teal/10"
                      : isActive
                        ? "border-accent-gold/25 bg-accent-gold/12"
                        : "border-white/10 bg-black/20",
                  )}
                >
                  <img
                    alt={artifact.revisedPrompt ?? artifact.name}
                    className="h-full w-full object-cover"
                    loading="lazy"
                    src={artifact.content}
                  />
                </span>
              ) : (
                <span
                  className={cn(
                    "inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-[0.7rem] border",
                    category === "evidence"
                      ? "border-accent-teal/18 bg-accent-teal/10 text-accent-teal"
                      : isActive
                        ? "border-accent-gold/25 bg-accent-gold/12 text-accent-gold"
                        : "border-white/10 bg-black/20 text-muted-foreground",
                  )}
                >
                  <Icon size={16} />
                </span>
              )}
              <div className="min-w-0 flex-1">
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <div className="truncate app-text-12 font-semibold leading-5">
                      {artifact.name}
                    </div>
                    <div className="mt-0.5 truncate app-text-11 text-muted-foreground">
                      {artifact.kind === "image" && artifact.byteCount != null
                        ? formatBytes(artifact.byteCount)
                        : artifact.summary}
                    </div>
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    <span className="rounded-[0.5rem] border border-white/10 bg-black/20 px-1 py-0.5 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                      {category === "evidence" ? "ev" : "file"}
                    </span>
                    <span className="rounded-[0.5rem] border border-white/10 bg-black/20 px-1 py-0.5 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                      {artifact.kind}
                    </span>
                    {artifact.kind === "image" && artifact.byteCount != null ? (
                      <span className="rounded-[0.5rem] border border-white/10 bg-black/20 px-1 py-0.5 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                        {formatBytes(artifact.byteCount)}
                      </span>
                    ) : artifact.previewHtml ? (
                      <span className="inline-flex h-4 w-4 items-center justify-center rounded-[0.45rem] border border-white/10 bg-black/20 text-muted-foreground">
                        <SparklesIcon size={8} />
                      </span>
                    ) : null}
                  </div>
                </div>
              </div>
            </div>
          </button>
        );
      })}
    </div>
  );
}

export function ArtifactPanelArtifactSurface({
  artifacts,
  orderedArtifacts,
  onOpenArtifact,
  selectedArtifactId,
}: {
  artifacts: Artifact[];
  orderedArtifacts: Artifact[];
  onOpenArtifact: (artifactId: string) => void;
  selectedArtifactId: string | null;
}) {
  return (
    <div className="h-full min-h-0 p-2.5">
      <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-[0.95rem] border border-white/8 bg-white/[0.035]">
        <div className="app-scrollbar min-h-0 flex-1 overflow-y-auto px-2 py-2">
          {artifacts.length > 0 ? (
            <ArtifactList
              items={orderedArtifacts}
              onOpenArtifact={onOpenArtifact}
              selectedArtifactId={selectedArtifactId}
            />
          ) : (
            <div className="flex h-full items-center justify-center rounded-[0.8rem] border border-dashed border-white/10 px-3 py-5 text-center text-sm leading-6 text-muted-foreground">
              Artifacts appear here as the thread runs.
            </div>
          )}
        </div>
      </section>
    </div>
  );
}

function formatBytes(value: number) {
  if (!Number.isFinite(value) || value < 0) {
    return "—";
  }
  if (value < 1024) {
    return `${value} B`;
  }
  const units = ["KB", "MB", "GB", "TB"];
  let current = value / 1024;
  let unitIndex = 0;
  while (current >= 1024 && unitIndex < units.length - 1) {
    current /= 1024;
    unitIndex++;
  }
  return `${current.toFixed(current >= 10 ? 0 : 1)} ${units[unitIndex]}`;
}
