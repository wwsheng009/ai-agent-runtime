// 由 components/workspace/artifact-panel-checkpoint-surface.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { HistoryIcon, LoaderCircleIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import {
  formatCheckpointMeta,
  formatCheckpointProvenance,
  formatCheckpointProvenanceSummary,
  formatCheckpointReason,
  formatCheckpointTitle,
} from "@/components/workspace/artifact-panel-shared";
import { cn, formatRelativeTimestamp } from "@/lib/utils";

import { type ArtifactPanelCheckpointSurfaceProps } from "./types";

type ArtifactPanelCheckpointsSectionProps = Pick<
  ArtifactPanelCheckpointSurfaceProps,
  | "checkpoints"
  | "checkpointsError"
  | "checkpointsLoading"
  | "onSelectCheckpoint"
  | "selectedCheckpointId"
  | "sessionId"
>;

export function ArtifactPanelCheckpointsSection({
  checkpoints,
  checkpointsError,
  checkpointsLoading,
  onSelectCheckpoint,
  selectedCheckpointId,
  sessionId,
}: ArtifactPanelCheckpointsSectionProps) {
  const { t } = useTranslation("workspace");

  return (
    <section className="flex min-h-0 flex-col overflow-hidden rounded-panel-lg border border-white/8 bg-white/[0.035]">
      <div className="flex items-center justify-between gap-3 border-b border-white/8 px-3 py-2.5">
        <div className="inline-flex items-center gap-2 app-text-10 uppercase tracking-[0.16em] text-muted-foreground">
          <HistoryIcon size={14} />
          {t("panels.artifacts.checkpoints.title")}
        </div>
        <Badge>{checkpoints.length}</Badge>
      </div>
      <div className="min-h-0 flex-1 overflow-auto px-2.5 py-2.5">
        {checkpointsLoading ? (
          <div className="mb-2 inline-flex items-center gap-2 app-text-10 uppercase tracking-[0.16em] text-muted-foreground">
            <LoaderCircleIcon size={14} className="animate-spin" />
            {t("panels.artifacts.checkpoints.loading")}
          </div>
        ) : null}
        {!sessionId ? (
          <div className="flex h-full items-center justify-center rounded-card border border-dashed border-white/10 px-3 py-5 text-center text-sm leading-6 text-muted-foreground">
            {t("panels.artifacts.checkpoints.noSession")}
          </div>
        ) : checkpointsError ? (
          <div className="rounded-card-lg border border-accent-orange/18 bg-accent-orange/8 px-3.5 py-3 text-sm leading-6 text-muted-foreground">
            {checkpointsError}
          </div>
        ) : checkpoints.length > 0 ? (
          <div className="space-y-1.5">
            {checkpoints.map((checkpoint) => {
              const isActive = checkpoint.id === selectedCheckpointId;
              const summary = formatCheckpointProvenanceSummary(
                checkpoint.provenance,
              );
              const provenanceLabels =
                summary.length > 0
                  ? summary
                  : formatCheckpointProvenance(checkpoint.provenance);

              return (
                <button
                  key={checkpoint.id}
                  type="button"
                  onClick={() => onSelectCheckpoint(checkpoint.id)}
                  className={cn(
                    "w-full rounded-card border px-2.5 py-2 text-left transition",
                    isActive
                      ? "border-accent-teal/30 bg-accent-teal/12 shadow-[inset_0_1px_0_rgba(143,208,198,0.08)]"
                      : "border-white/8 bg-white/4 hover:border-white/14 hover:bg-white/8",
                  )}
                  >
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0 flex-1">
                        <div className="truncate app-text-13 font-semibold">
                          {formatCheckpointTitle(checkpoint)}
                        </div>
                        <div className="mt-0.5 app-text-11 text-muted-foreground">
                          {formatCheckpointMeta(checkpoint)}
                        </div>
                      </div>
                    <span className="shrink-0 rounded-control border border-white/10 bg-black/20 px-2 py-0.5 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                      {formatRelativeTimestamp(checkpoint.created_at)}
                    </span>
                  </div>

                  <div className="mt-1.5 line-clamp-1 text-sm leading-6 text-muted-foreground">
                    {formatCheckpointReason(checkpoint)}
                  </div>

                  <div className="mt-1.5 flex flex-wrap gap-1.5 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
                    {provenanceLabels.slice(0, 2).map((item) => (
                      <span
                        key={`${checkpoint.id}-${item}`}
                        className="rounded-control border border-white/10 bg-black/20 px-2 py-0.5"
                      >
                        {item}
                      </span>
                    ))}
                  </div>
                </button>
              );
            })}
          </div>
        ) : (
          <div className="flex h-full items-center justify-center rounded-card border border-dashed border-white/10 px-3 py-5 text-center text-sm leading-6 text-muted-foreground">
            {t("panels.artifacts.checkpoints.empty")}
          </div>
        )}
      </div>
    </section>
  );
}
