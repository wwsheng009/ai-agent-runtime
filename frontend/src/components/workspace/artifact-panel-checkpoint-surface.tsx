import { HistoryIcon, LoaderCircleIcon, ScrollTextIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { CodeBlock } from "@/components/ui/code-block";
import { MessageMarkdown } from "@/components/workspace/message-markdown";
import {
  formatCheckpointFileChangeLabel,
  formatCheckpointMeta,
  formatCheckpointProvenance,
  formatCheckpointProvenanceSummary,
  formatCheckpointReason,
  formatCheckpointTitle,
  isCheckpointDetailLoading,
  resolveCheckpointFileEntries,
} from "@/components/workspace/artifact-panel-shared";
import type {
  RuntimeSessionCheckpointFile,
  RuntimeSessionCheckpointPreviewFile,
  RuntimeSessionCheckpointPreviewResult,
  RuntimeSessionCheckpointSummary,
} from "@/lib/runtime-api";
import { cn, formatRelativeTimestamp } from "@/lib/utils";

type CheckpointFileCode = {
  code: string;
  language: string;
  title: string;
};

type ConversationSummaryMessage = {
  content: string;
  role: string;
};

type ArtifactPanelCheckpointSurfaceProps = {
  checkpointConversationSummary: ConversationSummaryMessage[];
  checkpointDetailsError: string | null;
  checkpointDetailsLoadingId: string;
  checkpointFileCode: CheckpointFileCode;
  checkpointFiles: RuntimeSessionCheckpointFile[];
  checkpointPreview?: RuntimeSessionCheckpointPreviewResult;
  checkpointPreviewFiles: RuntimeSessionCheckpointPreviewFile[];
  checkpointProvenance: string[];
  checkpointProvenanceSummary: string[];
  checkpoints: RuntimeSessionCheckpointSummary[];
  checkpointsError: string | null;
  checkpointsLoading: boolean;
  onSelectCheckpoint: (checkpointId: string) => void;
  onSelectCheckpointFile: (filePath: string) => void;
  selectedCheckpoint: RuntimeSessionCheckpointSummary | null;
  selectedCheckpointFilePath: string | null;
  selectedCheckpointId: string | null;
  sessionId?: string;
};

export function ArtifactPanelCheckpointSurface({
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
  sessionId,
}: ArtifactPanelCheckpointSurfaceProps) {
  const checkpointFilesForSelection = resolveCheckpointFileEntries(
    checkpointPreviewFiles,
    checkpointFiles,
  );
  const checkpointDetailLoading = isCheckpointDetailLoading(
    selectedCheckpoint,
    checkpointDetailsLoadingId,
  );

  return (
    <div className="grid min-h-0 flex-1 gap-2.5 overflow-auto p-2.5">
      <section className="flex min-h-0 flex-col overflow-hidden rounded-[0.95rem] border border-white/8 bg-white/[0.035]">
        <div className="min-h-0 flex-1 overflow-auto px-2.5 py-2.5">
          {checkpointsLoading ? (
            <div className="mb-2 inline-flex items-center gap-2 text-[10px] uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
              <LoaderCircleIcon size={14} className="animate-spin" />
              Loading
            </div>
          ) : null}
          {!sessionId ? (
            <div className="flex h-full items-center justify-center rounded-[0.8rem] border border-dashed border-white/10 px-3 py-5 text-center text-sm leading-6 text-[var(--muted-foreground)]">
              Restore points become available after the thread attaches to a live
              session.
            </div>
          ) : checkpointsError ? (
            <div className="rounded-[0.85rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-3.5 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
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
                      "w-full rounded-[0.8rem] border px-2.5 py-2 text-left transition",
                      isActive
                        ? "border-[#8fd0c6]/30 bg-[#8fd0c6]/12 shadow-[inset_0_1px_0_rgba(143,208,198,0.08)]"
                        : "border-white/8 bg-white/4 hover:border-white/14 hover:bg-white/8",
                    )}
                    >
                      <div className="flex items-start justify-between gap-3">
                        <div className="min-w-0 flex-1">
                          <div className="truncate text-[13px] font-semibold">
                            {formatCheckpointTitle(checkpoint)}
                          </div>
                          <div className="mt-0.5 text-[11px] text-[var(--muted-foreground)]">
                            {formatCheckpointMeta(checkpoint)}
                          </div>
                        </div>
                      <span className="shrink-0 rounded-[0.65rem] border border-white/10 bg-black/20 px-2 py-0.5 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                        {formatRelativeTimestamp(checkpoint.created_at)}
                      </span>
                    </div>

                    <div className="mt-1.5 line-clamp-1 text-sm leading-6 text-[var(--muted-foreground)]">
                      {formatCheckpointReason(checkpoint)}
                    </div>

                    <div className="mt-1.5 flex flex-wrap gap-1.5 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                      {provenanceLabels.slice(0, 2).map((item) => (
                        <span
                          key={`${checkpoint.id}-${item}`}
                          className="rounded-[0.65rem] border border-white/10 bg-black/20 px-2 py-0.5"
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
            <div className="flex h-full items-center justify-center rounded-[0.8rem] border border-dashed border-white/10 px-3 py-5 text-center text-sm leading-6 text-[var(--muted-foreground)]">
              No restore points available for this session yet.
            </div>
          )}
        </div>
      </section>

      <section className="min-h-0 overflow-hidden rounded-[0.95rem] border border-white/8 bg-[linear-gradient(180deg,rgba(255,255,255,0.05),rgba(255,255,255,0.02))]">
        {selectedCheckpoint ? (
          <div className="flex h-full min-h-0 flex-col">
            <div className="border-b border-white/8 px-3.5 py-3.5">
              <div className="text-xs uppercase tracking-[0.18em] text-[var(--muted-foreground)]">
                Checkpoint detail
              </div>
              <div className="mt-2.5 flex items-start justify-between gap-4">
                <div className="min-w-0">
                  <div className="text-base font-semibold tracking-[-0.02em]">
                    {formatCheckpointTitle(selectedCheckpoint)}
                  </div>
                  <div className="mt-1.5 text-sm text-[var(--muted-foreground)]">
                    {formatRelativeTimestamp(selectedCheckpoint.created_at)}
                  </div>
                  <p className="mt-3 max-w-3xl text-sm leading-6 text-[var(--muted-foreground)]">
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
            </div>

            <div className="min-h-0 flex-1 overflow-auto p-3">
              <div className="grid min-h-full gap-3">
                <div className="space-y-3">
                  {checkpointDetailsError ? (
                    <div className="rounded-[0.85rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-3.5 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
                      {checkpointDetailsError}
                    </div>
                  ) : null}

                  {checkpointConversationSummary.length > 0 ? (
                    <div className="rounded-[0.9rem] border border-white/8 bg-white/4 p-3.5">
                      <div className="flex items-center gap-2 text-xs uppercase tracking-[0.18em] text-[var(--muted-foreground)]">
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
                      <div className="flex items-center gap-2 text-xs uppercase tracking-[0.18em] text-[var(--muted-foreground)]">
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
                        <div className="text-xs uppercase tracking-[0.18em] text-[var(--muted-foreground)]">
                          File diff reader
                        </div>
                        <div className="mt-1 text-sm text-[var(--muted-foreground)]">
                          Review captured file changes from the selected checkpoint.
                        </div>
                      </div>
                      {checkpointDetailLoading ? (
                        <LoaderCircleIcon
                          size={14}
                          className="animate-spin text-[var(--muted-foreground)]"
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
                    <div className="text-xs uppercase tracking-[0.18em] text-[var(--muted-foreground)]">
                      Snapshot metadata
                    </div>
                    <div className="mt-2.5 space-y-2.5">
                      <div className="rounded-[0.75rem] border border-white/8 bg-black/20 px-3 py-2.5">
                        <div className="app-text-11 uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
                          Summary
                        </div>
                        <div className="mt-2 text-sm leading-6 text-[var(--foreground)]">
                          {formatCheckpointMeta(selectedCheckpoint)}
                        </div>
                      </div>
                      <div className="rounded-[0.75rem] border border-white/8 bg-black/20 px-3 py-2.5">
                        <div className="app-text-11 uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
                          Reading state
                        </div>
                        <div className="mt-2 text-sm leading-6 text-[var(--foreground)]">
                          {checkpointFilesForSelection.length > 0
                            ? `${checkpointFilesForSelection.length} captured files available`
                            : "Waiting for file details from runtime"}
                        </div>
                      </div>
                    </div>
                  </div>

                  <div className="rounded-[0.9rem] border border-white/8 bg-white/4 p-3.5">
                    <div className="flex items-center justify-between gap-3">
                      <div className="text-xs uppercase tracking-[0.18em] text-[var(--muted-foreground)]">
                        Changed files
                      </div>
                      {checkpointDetailLoading ? (
                        <LoaderCircleIcon
                          size={14}
                          className="animate-spin text-[var(--muted-foreground)]"
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
                                  <div className="truncate text-[13px] font-medium text-[var(--foreground)]">
                                    {file.path}
                                  </div>
                                  <div className="mt-0.5 app-text-11 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                                    {formatCheckpointFileChangeLabel(file)}
                                  </div>
                                </div>
                                <span
                                  className={cn(
                                    "rounded-[0.65rem] border px-2 py-0.5 app-text-10 uppercase tracking-[0.14em]",
                                    isActive
                                      ? "border-[#8fd0c6]/25 bg-[#8fd0c6]/10 text-[#8fd0c6]"
                                      : "border-white/10 bg-black/20 text-[var(--muted-foreground)]",
                                  )}
                                >
                                  file
                                </span>
                              </div>
                            </button>
                          );
                        })
                      ) : (
                        <div className="rounded-[0.75rem] border border-dashed border-white/10 px-3 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
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
              <div className="mx-auto inline-flex size-10 items-center justify-center rounded-[0.8rem] border border-white/8 bg-white/[0.04] text-[var(--muted-foreground)]">
                <HistoryIcon size={18} />
              </div>
              <div className="mt-3 text-sm font-semibold">No checkpoint selected</div>
              <div className="mt-2 text-sm leading-6 text-[var(--muted-foreground)]">
                Select a runtime checkpoint from the timeline to inspect the
                conversation snapshot, preview summary, and captured file diffs.
              </div>
            </div>
          </div>
        )}
      </section>
    </div>
  );
}
