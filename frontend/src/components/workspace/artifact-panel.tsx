import {
  FileCode2Icon,
  FileJsonIcon,
  GlobeIcon,
  HistoryIcon,
  ImageIcon,
  SparklesIcon,
} from "lucide-react";
import {
  lazy,
  Suspense,
  useId,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";

import { Badge } from "@/components/ui/badge";
import { type Artifact } from "@/data/mock";
import { useRuntimeCheckpoints } from "@/hooks/workspace/use-runtime-checkpoints";
import {
  classifyArtifactCategory,
  formatArtifactCategory,
} from "@/lib/workspace-artifacts";
import { cn } from "@/lib/utils";

const ArtifactPanelCheckpointSurface = lazy(() =>
  import("@/components/workspace/artifact-panel-checkpoint-surface").then(
    (module) => ({
      default: module.ArtifactPanelCheckpointSurface,
    }),
  ),
);

type ArtifactPanelProps = {
  artifacts: Artifact[];
  lastRuntimeEventType?: string;
  onOpenArtifact: (artifactId: string) => void;
  selectedArtifactId: string | null;
  sessionId?: string;
};

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

function surfaceButtonClass(
  active: boolean,
  tone: "artifact" | "checkpoint",
  disabled = false,
) {
  if (disabled) {
    return "inline-flex items-center gap-2 rounded-[0.65rem] border border-white/8 bg-white/4 px-2.5 py-1 text-base text-[var(--muted-foreground)] opacity-60";
  }

  if (tone === "artifact") {
    return cn(
      "inline-flex items-center gap-2 rounded-[0.65rem] border px-2.5 py-1 text-base transition focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--workspace-sidebar-bg)]",
      active
        ? "border-[#f0c77b]/30 bg-[#f0c77b]/8 text-[#f0c77b]"
        : "border-white/10 bg-white/4 text-[var(--muted-foreground)] hover:border-white/16 hover:bg-white/8",
    );
  }

  return cn(
    "inline-flex items-center gap-2 rounded-[0.65rem] border px-2.5 py-1 text-base transition focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--workspace-sidebar-bg)]",
    active
      ? "border-[#8fd0c6]/30 bg-[#8fd0c6]/10 text-[#8fd0c6]"
      : "border-white/10 bg-white/4 text-[var(--muted-foreground)] hover:border-white/16 hover:bg-white/8",
  );
}

function getEnabledTabIndices(disabledStates: boolean[]) {
  return disabledStates.flatMap((disabled, index) => (disabled ? [] : [index]));
}

function handleHorizontalTabKeyDown(
  event: ReactKeyboardEvent<HTMLButtonElement>,
  options: {
    currentIndex: number;
    disabledStates: boolean[];
    onSelectIndex: (index: number) => void;
    refs: Array<HTMLButtonElement | null>;
  },
) {
  const enabledIndices = getEnabledTabIndices(options.disabledStates);
  if (enabledIndices.length === 0) {
    return;
  }

  const currentEnabledIndex = enabledIndices.indexOf(options.currentIndex);
  let nextIndex = -1;

  if (event.key === "Home") {
    nextIndex = enabledIndices[0] ?? -1;
  } else if (event.key === "End") {
    nextIndex = enabledIndices[enabledIndices.length - 1] ?? -1;
  } else if (event.key === "ArrowRight" || event.key === "ArrowDown") {
    const targetEnabledIndex =
      currentEnabledIndex >= 0
        ? (currentEnabledIndex + 1) % enabledIndices.length
        : 0;
    nextIndex = enabledIndices[targetEnabledIndex] ?? -1;
  } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
    const targetEnabledIndex =
      currentEnabledIndex >= 0
        ? (currentEnabledIndex - 1 + enabledIndices.length) % enabledIndices.length
        : enabledIndices.length - 1;
    nextIndex = enabledIndices[targetEnabledIndex] ?? -1;
  }

  if (nextIndex < 0) {
    return;
  }

  event.preventDefault();
  options.onSelectIndex(nextIndex);
  options.refs[nextIndex]?.focus();
}

export function ArtifactPanel({
  artifacts,
  lastRuntimeEventType,
  onOpenArtifact,
  selectedArtifactId,
  sessionId,
}: ArtifactPanelProps) {
  const asideTitleId = useId();
  const asideDescriptionId = useId();
  const artifactSurfaceTabId = useId();
  const checkpointSurfaceTabId = useId();
  const artifactSurfacePanelId = useId();
  const checkpointSurfacePanelId = useId();
  const surfaceTabRefs = useState<Array<HTMLButtonElement | null>>([])[0];
  const [activeSurface, setActiveSurface] = useState<"artifacts" | "checkpoints">(
    artifacts.length > 0 ? "artifacts" : "checkpoints",
  );

  const {
    checkpointConversationSummary,
    checkpointDetailsError,
    checkpointDetailsLoadingId,
    checkpointFileCode,
    checkpointFiles,
    checkpointPreview,
    checkpointPreviewFiles,
    checkpointProvenance,
    checkpointProvenanceSummary,
    checkpoints,
    checkpointsError,
    checkpointsLoading,
    onSelectCheckpoint,
    onSelectCheckpointFile,
    selectedCheckpoint,
    selectedCheckpointFilePath,
    selectedCheckpointId,
  } = useRuntimeCheckpoints({
    lastRuntimeEventType,
    sessionId,
  });

  const resolvedActiveSurface =
    artifacts.length === 0 && checkpoints.length > 0
      ? "checkpoints"
      : checkpoints.length === 0 && artifacts.length > 0
        ? "artifacts"
        : activeSurface;
  const evidenceArtifacts = artifacts.filter(
    (artifact) => classifyArtifactCategory(artifact) === "evidence",
  );
  const outputArtifacts = artifacts.filter(
    (artifact) => classifyArtifactCategory(artifact) === "file",
  );
  const orderedArtifacts = [...evidenceArtifacts, ...outputArtifacts];
  const selectedArtifact =
    artifacts.find((artifact) => artifact.id === selectedArtifactId) ?? null;
  const selectedArtifactCategory = selectedArtifact
    ? classifyArtifactCategory(selectedArtifact)
    : null;
  const surfaceTabDisabledStates = [false, !sessionId];
  const artifactSelectionAnnouncement = selectedArtifact
    ? `${formatArtifactCategory(selectedArtifactCategory ?? "file")} selected: ${
        selectedArtifact.name
      }. Opens in dialog.`
    : "Artifact rail ready. Select an item to open it in a dialog.";

  function renderArtifactList(items: Artifact[]) {
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
                  ? "border-[#f0c77b]/30 bg-[#f0c77b]/8 shadow-[inset_0_1px_0_rgba(240,199,123,0.08)]"
                  : "border-white/8 bg-white/4 hover:border-white/14 hover:bg-white/8",
              )}
            >
              <div className="flex items-center gap-2.5">
                {showImageThumbnail ? (
                  <span
                    className={cn(
                      "inline-flex h-10 w-10 shrink-0 overflow-hidden rounded-[0.7rem] border",
                      category === "evidence"
                        ? "border-[#8fd0c6]/18 bg-[#8fd0c6]/10"
                        : isActive
                          ? "border-[#f0c77b]/25 bg-[#f0c77b]/12"
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
                        ? "border-[#8fd0c6]/18 bg-[#8fd0c6]/10 text-[#8fd0c6]"
                        : isActive
                          ? "border-[#f0c77b]/25 bg-[#f0c77b]/12 text-[#f0c77b]"
                          : "border-white/10 bg-black/20 text-[var(--muted-foreground)]",
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
                      <div className="mt-0.5 truncate app-text-11 text-[var(--muted-foreground)]">
                        {artifact.kind === "image" && artifact.byteCount != null
                          ? formatBytes(artifact.byteCount)
                          : artifact.summary}
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-1">
                      <span className="rounded-[0.5rem] border border-white/10 bg-black/20 px-1 py-0.5 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                        {category === "evidence" ? "ev" : "file"}
                      </span>
                      <span className="rounded-[0.5rem] border border-white/10 bg-black/20 px-1 py-0.5 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                        {artifact.kind}
                      </span>
                      {artifact.kind === "image" && artifact.byteCount != null ? (
                        <span className="rounded-[0.5rem] border border-white/10 bg-black/20 px-1 py-0.5 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                          {formatBytes(artifact.byteCount)}
                        </span>
                      ) : artifact.previewHtml ? (
                        <span className="inline-flex h-4 w-4 items-center justify-center rounded-[0.45rem] border border-white/10 bg-black/20 text-[var(--muted-foreground)]">
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

  const artifactSurface = (
    <div className="h-full min-h-0 p-2.5">
      <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-[0.95rem] border border-white/8 bg-white/[0.035]">
        <div className="app-scrollbar min-h-0 flex-1 overflow-y-auto px-2 py-2">
          {artifacts.length > 0 ? (
            renderArtifactList(orderedArtifacts)
          ) : (
            <div className="flex h-full items-center justify-center rounded-[0.8rem] border border-dashed border-white/10 px-3 py-5 text-center text-sm leading-6 text-[var(--muted-foreground)]">
              Artifacts appear here as the thread runs.
            </div>
          )}
        </div>
      </section>
    </div>
  );

  return (
    <aside
      aria-describedby={asideDescriptionId}
      aria-labelledby={asideTitleId}
      className="hidden h-full min-h-0 flex-col overflow-hidden border-l border-white/8 [background:var(--workspace-sidebar-bg)] xl:flex"
    >
      <div
        key={selectedArtifactId ?? "none"}
        aria-atomic="true"
        aria-live="polite"
        className="sr-only"
        role="status"
      >
        {artifactSelectionAnnouncement}
      </div>
      <div className="sr-only" id={asideDescriptionId}>
        Workspace artifacts and restore points for the current thread.
      </div>
      <div className="border-b border-white/8 px-3 py-2.5">
        <div className="flex items-center justify-between gap-3">
          <div className="sr-only" id={asideTitleId}>
            Artifacts
          </div>
          <div
            aria-label="Artifact panel surfaces"
            aria-orientation="horizontal"
            className="flex flex-wrap gap-1.5"
            role="tablist"
          >
            <button
              aria-controls={artifactSurfacePanelId}
              aria-selected={resolvedActiveSurface === "artifacts"}
              id={artifactSurfaceTabId}
              ref={(node) => {
                surfaceTabRefs[0] = node;
              }}
              role="tab"
              tabIndex={resolvedActiveSurface === "artifacts" ? 0 : -1}
              type="button"
              onClick={() => setActiveSurface("artifacts")}
              onKeyDown={(event) =>
                handleHorizontalTabKeyDown(event, {
                  currentIndex: 0,
                  disabledStates: surfaceTabDisabledStates,
                  onSelectIndex: (index) =>
                    setActiveSurface(index === 0 ? "artifacts" : "checkpoints"),
                  refs: surfaceTabRefs,
                })
              }
              className={surfaceButtonClass(
                resolvedActiveSurface === "artifacts",
                "artifact",
              )}
            >
              <FileCode2Icon size={14} />
              Items
            </button>
            <button
              aria-controls={checkpointSurfacePanelId}
              aria-selected={resolvedActiveSurface === "checkpoints"}
              id={checkpointSurfaceTabId}
              ref={(node) => {
                surfaceTabRefs[1] = node;
              }}
              role="tab"
              tabIndex={resolvedActiveSurface === "checkpoints" ? 0 : -1}
              type="button"
              onClick={() => setActiveSurface("checkpoints")}
              onKeyDown={(event) =>
                handleHorizontalTabKeyDown(event, {
                  currentIndex: 1,
                  disabledStates: surfaceTabDisabledStates,
                  onSelectIndex: (index) =>
                    setActiveSurface(index === 0 ? "artifacts" : "checkpoints"),
                  refs: surfaceTabRefs,
                })
              }
              className={surfaceButtonClass(
                resolvedActiveSurface === "checkpoints",
                "checkpoint",
                !sessionId,
              )}
              disabled={!sessionId}
            >
              <HistoryIcon size={14} />
              Restore
            </button>
          </div>
          <div className="flex items-center gap-2">
            <Badge>{artifacts.length}</Badge>
          </div>
        </div>
      </div>

      <div
        aria-labelledby={artifactSurfaceTabId}
        className="min-h-0 flex-1"
        hidden={resolvedActiveSurface !== "artifacts"}
        id={artifactSurfacePanelId}
        role="tabpanel"
      >
        {artifactSurface}
      </div>
      <div
        aria-labelledby={checkpointSurfaceTabId}
        className="min-h-0 flex-1"
        hidden={resolvedActiveSurface !== "checkpoints"}
        id={checkpointSurfacePanelId}
        role="tabpanel"
      >
        {resolvedActiveSurface === "checkpoints" ? (
          <Suspense fallback={<ArtifactPanelCheckpointFallback />}>
            <ArtifactPanelCheckpointSurface
              checkpointConversationSummary={checkpointConversationSummary}
              checkpointDetailsError={checkpointDetailsError}
              checkpointDetailsLoadingId={checkpointDetailsLoadingId}
              checkpointFileCode={checkpointFileCode}
              checkpointFiles={checkpointFiles}
              checkpointPreview={checkpointPreview}
              checkpointPreviewFiles={checkpointPreviewFiles}
              checkpointProvenance={checkpointProvenance}
              checkpointProvenanceSummary={checkpointProvenanceSummary}
              checkpoints={checkpoints}
              checkpointsError={checkpointsError}
              checkpointsLoading={checkpointsLoading}
              onSelectCheckpoint={onSelectCheckpoint}
              onSelectCheckpointFile={onSelectCheckpointFile}
              selectedCheckpoint={selectedCheckpoint}
              selectedCheckpointFilePath={selectedCheckpointFilePath}
              selectedCheckpointId={selectedCheckpointId}
              sessionId={sessionId}
            />
          </Suspense>
        ) : null}
      </div>
    </aside>
  );
}

function ArtifactPanelCheckpointFallback() {
  return (
    <div className="grid min-h-0 flex-1 gap-3 overflow-auto p-3">
      <div className="flex min-h-[14rem] items-center justify-center rounded-[0.9rem] border border-white/8 bg-white/[0.035] px-3.5 py-2.5 text-sm text-[var(--muted-foreground)]">
        正在加载 restore points…
      </div>
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
