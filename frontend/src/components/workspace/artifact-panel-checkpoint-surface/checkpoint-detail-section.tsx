// 由 components/workspace/artifact-panel-checkpoint-surface.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { HistoryIcon, LoaderCircleIcon, ScrollTextIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { CodeBlock } from "@/components/ui/code-block";
import {
  formatCheckpointFileChangeLabel,
  formatCheckpointMeta,
  formatCheckpointReason,
  formatCheckpointTitle,
} from "@/components/workspace/artifact-panel-shared";
import { MessageMarkdown } from "@/components/workspace/message-markdown";
import { cn, formatRelativeTimestamp } from "@/lib/utils";

import {
  type ArtifactPanelCheckpointSurfaceProps,
  type CheckpointFileSelection,
} from "./types";

type ArtifactPanelCheckpointDetailSectionProps = Pick<
  ArtifactPanelCheckpointSurfaceProps,
  | "checkpointConversationSummary"
  | "checkpointDetailsError"
  | "checkpointFileCode"
  | "checkpointPreview"
  | "checkpointProvenance"
  | "checkpointProvenanceSummary"
  | "checkpointRestoreError"
  | "checkpointRestoreNotice"
  | "checkpointRestorePendingId"
  | "onRestoreCheckpoint"
  | "onSelectCheckpointFile"
  | "selectedCheckpoint"
  | "selectedCheckpointFilePath"
> & {
  checkpointDetailLoading: boolean;
  checkpointFilesForSelection: CheckpointFileSelection[];
};

export function ArtifactPanelCheckpointDetailSection({
  checkpointConversationSummary,
  checkpointDetailLoading,
  checkpointDetailsError,
  checkpointFileCode,
  checkpointFilesForSelection,
  checkpointPreview,
  checkpointProvenance,
  checkpointProvenanceSummary,
  checkpointRestoreError = null,
  checkpointRestoreNotice = null,
  checkpointRestorePendingId = "",
  onRestoreCheckpoint,
  onSelectCheckpointFile,
  selectedCheckpoint,
  selectedCheckpointFilePath,
}: ArtifactPanelCheckpointDetailSectionProps) {
  return (
    <section className="min-h-0 overflow-hidden rounded-[0.95rem] border border-white/8 bg-[linear-gradient(180deg,rgba(255,255,255,0.05),rgba(255,255,255,0.02))]">
      {selectedCheckpoint ? (
        <div className="flex h-full min-h-0 flex-col">
          <div className="border-b border-white/8 px-3.5 py-3.5">
            <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">
              Checkpoint detail
            </div>
            <div className="mt-2.5 flex items-start justify-between gap-4">
              <div className="min-w-0">
                <div className="text-base font-semibold tracking-[-0.02em]">
                  {formatCheckpointTitle(selectedCheckpoint)}
                </div>
                <div className="mt-1.5 text-sm text-muted-foreground">
                  {formatRelativeTimestamp(selectedCheckpoint.created_at)}
                </div>
                <p className="mt-3 max-w-3xl text-sm leading-6 text-muted-foreground">
                  {formatCheckpointReason(selectedCheckpoint)}
                </p>
              </div>
              <Badge>checkpoint</Badge>
            </div>

            <div className="mt-3 flex flex-wrap gap-1.5">
              <Badge>{selectedCheckpoint.message_count} messages</Badge>
              {selectedCheckpoint.conversation_exact ? (
                <Badge>exact conversation</Badge>
              ) : null}
              {selectedCheckpoint.task_id ? (
                <Badge>{selectedCheckpoint.task_id.slice(0, 12)}</Badge>
              ) : null}
              {checkpointProvenance.map((item) => (
                <Badge key={`${selectedCheckpoint.id}-${item}`}>{item}</Badge>
              ))}
              {checkpointProvenanceSummary.map((item) => (
                <Badge key={`${selectedCheckpoint.id}-${item}-summary`}>{item}</Badge>
              ))}
            </div>

            {typeof onRestoreCheckpoint === "function" ? (
              <div className="mt-3 flex flex-wrap gap-2">
                <Button
                  disabled={Boolean(checkpointRestorePendingId)}
                  onClick={() => onRestoreCheckpoint("conversation")}
                  size="sm"
                  type="button"
                  variant="secondary"
                >
                  {checkpointRestorePendingId === selectedCheckpoint.id ? (
                    <LoaderCircleIcon size={14} className="animate-spin" />
                  ) : null}
                  Restore conversation
                </Button>
                <Button
                  disabled={Boolean(checkpointRestorePendingId)}
                  onClick={() => onRestoreCheckpoint("code")}
                  size="sm"
                  type="button"
                  variant="secondary"
                >
                  Restore files
                </Button>
                <Button
                  disabled={Boolean(checkpointRestorePendingId)}
                  onClick={() => onRestoreCheckpoint("both")}
                  size="sm"
                  type="button"
                  variant="primary"
                >
                  Restore both
                </Button>
              </div>
            ) : null}

            {checkpointRestoreError ? (
              <div className="mt-3 rounded-[0.75rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-3 py-2 text-sm text-muted-foreground">
                {checkpointRestoreError}
              </div>
            ) : null}
            {checkpointRestoreNotice ? (
              <div className="mt-3 rounded-[0.75rem] border border-[#8fd0c6]/18 bg-[#8fd0c6]/10 px-3 py-2 text-sm text-muted-foreground">
                {checkpointRestoreNotice}
              </div>
            ) : null}
          </div>

          <div className="min-h-0 flex-1 overflow-auto p-3">
            <div className="grid min-h-full gap-3">
              <div className="space-y-3">
                {checkpointDetailsError ? (
                  <div className="rounded-[0.85rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-3.5 py-3 text-sm leading-6 text-muted-foreground">
                    {checkpointDetailsError}
                  </div>
                ) : null}

                {checkpointConversationSummary.length > 0 ? (
                  <div className="rounded-[0.9rem] border border-white/8 bg-white/4 p-3.5">
                    <div className="flex items-center gap-2 text-xs uppercase tracking-[0.18em] text-muted-foreground">
                      <ScrollTextIcon size={14} />
                      Conversation snapshot
                    </div>
                    <div className="mt-2.5 space-y-2.5">
                      {checkpointConversationSummary.map((message, index) => (
                        <div
                          key={`${selectedCheckpoint.id}-conversation-${index}`}
                          className="rounded-[0.75rem] border border-white/8 bg-black/20 px-3.5 py-3"
                        >
                          <div className="app-text-11 uppercase tracking-[0.18em] text-[#8fd0c6]">
                            {message.role}
                          </div>
                          <MessageMarkdown
                            className="mt-1.5 app-text-13"
                            content={message.content}
                          />
                        </div>
                      ))}
                    </div>
                  </div>
                ) : null}

                {checkpointPreview?.preview && checkpointPreview.preview.length > 0 ? (
                  <div className="rounded-[0.9rem] border border-white/8 bg-white/4 p-3.5">
                    <div className="flex items-center gap-2 text-xs uppercase tracking-[0.18em] text-muted-foreground">
                      <HistoryIcon size={14} />
                      Preview summary
                    </div>
                    <div className="mt-2.5 space-y-1.5">
                      {checkpointPreview.preview.map((line, index) => (
                        <div
                          key={`${selectedCheckpoint.id}-preview-${index}`}
                          className="rounded-[0.75rem] border border-white/8 bg-black/20 px-3.5 py-2.5"
                        >
                          <MessageMarkdown
                            className="app-text-13"
                            content={line}
                          />
                        </div>
                      ))}
                    </div>
                  </div>
                ) : null}

                <div className="overflow-hidden rounded-[0.9rem] border border-white/8 bg-black/20">
                  <div className="flex items-center justify-between gap-3 border-b border-white/8 px-3 py-2.5">
                    <div>
                      <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">
                        File diff reader
                      </div>
                      <div className="mt-1 text-sm text-muted-foreground">
                        Review captured file changes from the selected checkpoint.
                      </div>
                    </div>
                    {checkpointDetailLoading ? (
                      <LoaderCircleIcon
                        size={14}
                        className="animate-spin text-muted-foreground"
                      />
                    ) : null}
                  </div>
                  <div className="p-3">
                    <CodeBlock
                      code={checkpointFileCode.code}
                      language={checkpointFileCode.language}
                      title={checkpointFileCode.title}
                    />
                  </div>
                </div>
              </div>

              <div className="space-y-3">
                <div className="rounded-[0.9rem] border border-white/8 bg-white/4 p-3.5">
                  <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">
                    Snapshot metadata
                  </div>
                  <div className="mt-2.5 space-y-2.5">
                    <div className="rounded-[0.75rem] border border-white/8 bg-black/20 px-3 py-2.5">
                      <div className="app-text-11 uppercase tracking-[0.16em] text-muted-foreground">
                        Summary
                      </div>
                      <div className="mt-2 text-sm leading-6 text-foreground">
                        {formatCheckpointMeta(selectedCheckpoint)}
                      </div>
                    </div>
                    <div className="rounded-[0.75rem] border border-white/8 bg-black/20 px-3 py-2.5">
                      <div className="app-text-11 uppercase tracking-[0.16em] text-muted-foreground">
                        Reading state
                      </div>
                      <div className="mt-2 text-sm leading-6 text-foreground">
                        {checkpointFilesForSelection.length > 0
                          ? `${checkpointFilesForSelection.length} captured files available`
                          : "Waiting for file details from runtime"}
                      </div>
                    </div>
                  </div>
                </div>

                <div className="rounded-[0.9rem] border border-white/8 bg-white/4 p-3.5">
                  <div className="flex items-center justify-between gap-3">
                    <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">
                      Changed files
                    </div>
                    {checkpointDetailLoading ? (
                      <LoaderCircleIcon
                        size={14}
                        className="animate-spin text-muted-foreground"
                      />
                    ) : null}
                  </div>

                  <div className="mt-2.5 space-y-1.5">
                    {checkpointFilesForSelection.length > 0 ? (
                      checkpointFilesForSelection.map((file) => {
                        const isActive = file.path === selectedCheckpointFilePath;
                        return (
                          <button
                            key={`${selectedCheckpoint.id}-${file.path}`}
                            type="button"
                            onClick={() => onSelectCheckpointFile(file.path)}
                            className={cn(
                              "w-full rounded-[0.75rem] border px-3 py-2.5 text-left transition",
                              isActive
                                ? "border-[#8fd0c6]/30 bg-[#8fd0c6]/10"
                                : "border-white/8 bg-black/20 hover:border-white/14 hover:bg-white/8",
                            )}
                          >
                            <div className="flex items-start justify-between gap-3">
                              <div className="min-w-0 flex-1">
                                <div className="truncate text-[13px] font-medium text-foreground">
                                  {file.path}
                                </div>
                                <div className="mt-0.5 app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
                                  {formatCheckpointFileChangeLabel(file)}
                                </div>
                              </div>
                              <span
                                className={cn(
                                  "rounded-[0.65rem] border px-2 py-0.5 app-text-10 uppercase tracking-[0.14em]",
                                  isActive
                                    ? "border-[#8fd0c6]/25 bg-[#8fd0c6]/10 text-[#8fd0c6]"
                                    : "border-white/10 bg-black/20 text-muted-foreground",
                                )}
                              >
                                file
                              </span>
                            </div>
                          </button>
                        );
                      })
                    ) : (
                      <div className="rounded-[0.75rem] border border-dashed border-white/10 px-3 py-3 text-sm leading-6 text-muted-foreground">
                        No checkpoint file diffs available yet.
                      </div>
                    )}
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      ) : (
        <div className="flex h-full items-center justify-center px-5 py-8">
          <div className="max-w-sm text-center">
            <div className="mx-auto inline-flex size-10 items-center justify-center rounded-[0.8rem] border border-white/8 bg-white/[0.04] text-muted-foreground">
              <HistoryIcon size={18} />
            </div>
            <div className="mt-3 text-sm font-semibold">No checkpoint selected</div>
            <div className="mt-2 text-sm leading-6 text-muted-foreground">
              Select a runtime checkpoint from the timeline to inspect the
              conversation snapshot, preview summary, and captured file diffs.
            </div>
          </div>
        </div>
      )}
    </section>
  );
}
