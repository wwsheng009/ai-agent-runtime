// 由 components/workspace/artifact-panel-checkpoint-surface.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { LoaderCircleIcon, Undo2Icon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import {
  formatBacktrackAuditIdentity,
  formatBacktrackAuditMeta,
  formatBacktrackAuditSummary,
  formatBacktrackAuditTitle,
} from "@/components/workspace/artifact-panel-shared";
import { cn, formatRelativeTimestamp } from "@/lib/utils";

import { type ArtifactPanelCheckpointSurfaceProps } from "./types";

type ArtifactPanelBacktrackAuditSectionProps = Pick<
  ArtifactPanelCheckpointSurfaceProps,
  | "backtrackAuditEntries"
  | "backtrackAuditError"
  | "backtrackAuditLoading"
  | "sessionId"
> & {
  onSelectAuditId: (auditId: string) => void;
  selectedAuditId: string | null;
};

export function ArtifactPanelBacktrackAuditSection({
  backtrackAuditEntries = [],
  backtrackAuditError = null,
  backtrackAuditLoading = false,
  onSelectAuditId,
  selectedAuditId,
  sessionId,
}: ArtifactPanelBacktrackAuditSectionProps) {
  const selectedAudit =
    backtrackAuditEntries.find((entry) => entry.id === selectedAuditId) ?? null;

  return (
    <section className="flex min-h-0 flex-col overflow-hidden rounded-[0.95rem] border border-white/8 bg-white/[0.035]">
      <div className="flex items-center justify-between gap-3 border-b border-white/8 px-3 py-2.5">
        <div className="inline-flex items-center gap-2 text-[10px] uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
          <Undo2Icon size={14} />
          Backtrack audit
        </div>
        <Badge>{backtrackAuditEntries.length}</Badge>
      </div>
      <div className="min-h-0 flex-1 overflow-auto px-2.5 py-2.5">
        {backtrackAuditLoading ? (
          <div className="mb-2 inline-flex items-center gap-2 text-[10px] uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
            <LoaderCircleIcon size={14} className="animate-spin" />
            Loading
          </div>
        ) : null}
        {!sessionId ? (
          <div className="flex h-full items-center justify-center rounded-[0.8rem] border border-dashed border-white/10 px-3 py-5 text-center text-sm leading-6 text-[var(--muted-foreground)]">
            Backtrack tombstones appear after the thread attaches to a live
            session and a user-turn rewind is applied.
          </div>
        ) : backtrackAuditError ? (
          <div className="rounded-[0.85rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-3.5 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
            {backtrackAuditError}
          </div>
        ) : backtrackAuditEntries.length > 0 ? (
          <div className="space-y-1.5">
            {backtrackAuditEntries.map((entry) => {
              const isActive = entry.id === selectedAuditId;
              return (
                <button
                  key={entry.id}
                  type="button"
                  onClick={() => onSelectAuditId(entry.id)}
                  className={cn(
                    "w-full rounded-[0.8rem] border px-2.5 py-2 text-left transition",
                    isActive
                      ? "border-[#f0c77b]/30 bg-[#f0c77b]/10 shadow-[inset_0_1px_0_rgba(240,199,123,0.08)]"
                      : "border-white/8 bg-white/4 hover:border-white/14 hover:bg-white/8",
                  )}
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-[13px] font-semibold">
                        {formatBacktrackAuditTitle(entry)}
                      </div>
                      <div className="mt-0.5 text-[11px] text-[var(--muted-foreground)]">
                        {formatBacktrackAuditMeta(entry)}
                      </div>
                    </div>
                    <span className="shrink-0 rounded-[0.65rem] border border-white/10 bg-black/20 px-2 py-0.5 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                      {formatRelativeTimestamp(entry.created_at)}
                    </span>
                  </div>
                  <div className="mt-1.5 line-clamp-2 text-sm leading-6 text-[var(--muted-foreground)]">
                    {formatBacktrackAuditSummary(entry)}
                  </div>
                </button>
              );
            })}
          </div>
        ) : (
          <div className="flex h-full items-center justify-center rounded-[0.8rem] border border-dashed border-white/10 px-3 py-5 text-center text-sm leading-6 text-[var(--muted-foreground)]">
            No backtrack tombstones yet. Apply a user-turn rewind to record an
            audit summary here.
          </div>
        )}
      </div>
      {selectedAudit ? (
        <div className="border-t border-white/8 px-3 py-3">
          <div className="text-xs uppercase tracking-[0.18em] text-[var(--muted-foreground)]">
            Tombstone detail
          </div>
          <div className="mt-2 flex flex-wrap gap-1.5">
            <Badge>audit</Badge>
            {selectedAudit.mode ? <Badge>{selectedAudit.mode}</Badge> : null}
            {selectedAudit.edited ? <Badge>edited</Badge> : null}
            {selectedAudit.include_anchor ? <Badge>include anchor</Badge> : null}
            <Badge>−{selectedAudit.removed_message_count} msgs</Badge>
            <Badge>−{selectedAudit.removed_user_turns} turns</Badge>
            <Badge>kept {selectedAudit.truncated_to_message_count}</Badge>
          </div>
          <div className="mt-2.5 space-y-1.5 text-sm leading-6 text-[var(--muted-foreground)]">
            <div>
              <span className="text-[var(--foreground)]">Identity:</span>{" "}
              {formatBacktrackAuditIdentity(selectedAudit)}
            </div>
            {selectedAudit.message_id ? (
              <div>
                <span className="text-[var(--foreground)]">Message:</span>{" "}
                {selectedAudit.message_id}
              </div>
            ) : null}
            {selectedAudit.base_checkpoint_id ? (
              <div>
                <span className="text-[var(--foreground)]">Base checkpoint:</span>{" "}
                {selectedAudit.base_checkpoint_id}
              </div>
            ) : null}
            {(selectedAudit.later_checkpoint_ids?.length ?? 0) > 0 ? (
              <div>
                <span className="text-[var(--foreground)]">Later checkpoints:</span>{" "}
                {selectedAudit.later_checkpoint_ids?.join(", ")}
              </div>
            ) : null}
            {(selectedAudit.removed_message_ids?.length ?? 0) > 0 ? (
              <div>
                <span className="text-[var(--foreground)]">Removed message ids:</span>{" "}
                {selectedAudit.removed_message_ids?.slice(0, 6).join(", ")}
                {(selectedAudit.removed_message_ids?.length ?? 0) > 6
                  ? ` (+${(selectedAudit.removed_message_ids?.length ?? 0) - 6})`
                  : ""}
              </div>
            ) : null}
            {(selectedAudit.removed_turn_ids?.length ?? 0) > 0 ? (
              <div>
                <span className="text-[var(--foreground)]">Removed turn ids:</span>{" "}
                {selectedAudit.removed_turn_ids?.slice(0, 6).join(", ")}
                {(selectedAudit.removed_turn_ids?.length ?? 0) > 6
                  ? ` (+${(selectedAudit.removed_turn_ids?.length ?? 0) - 6})`
                  : ""}
              </div>
            ) : null}
          </div>
          <p className="mt-2 text-[11px] leading-5 text-[var(--muted-foreground)]">
            Tombstones are durable audit summaries only. Truncated transcript
            text is not recoverable from this panel.
          </p>
        </div>
      ) : null}
    </section>
  );
}
