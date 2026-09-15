// 由 components/workspace/artifact-panel-checkpoint-surface.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { HistoryIcon, LoaderCircleIcon, ScrollTextIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

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
import type { RuntimeSessionCheckpointPreviewMode } from "@/lib/runtime-api";
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
  | "checkpointRestoreSummary"
  | "checkpointRestorePendingId"
  | "onRestoreCheckpoint"
  | "onSelectCheckpointFile"
  | "selectedCheckpoint"
  | "selectedCheckpointFilePath"
> & {
  checkpointDetailLoading: boolean;
  checkpointFilesForSelection: CheckpointFileSelection[];
};

// 「还原模式」选项：选中只记录意图，必须再点确认按钮才会执行还原。
const RESTORE_MODE_OPTIONS = [
  {
    value: "conversation",
    labelKey: "panels.artifacts.checkpointDetail.restoreConversation",
    hintKey: "panels.artifacts.checkpointDetail.restoreConversationHint",
  },
  {
    value: "code",
    labelKey: "panels.artifacts.checkpointDetail.restoreFiles",
    hintKey: "panels.artifacts.checkpointDetail.restoreFilesHint",
  },
  {
    value: "both",
    labelKey: "panels.artifacts.checkpointDetail.restoreBoth",
    hintKey: "panels.artifacts.checkpointDetail.restoreBothHint",
  },
] as const satisfies readonly {
  value: RuntimeSessionCheckpointPreviewMode;
  labelKey: string;
  hintKey: string;
}[];

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
  checkpointRestoreSummary = null,
  checkpointRestorePendingId = "",
  onRestoreCheckpoint,
  onSelectCheckpointFile,
  selectedCheckpoint,
  selectedCheckpointFilePath,
}: ArtifactPanelCheckpointDetailSectionProps) {
  const { t } = useTranslation("workspace");
  const [restoreMode, setRestoreMode] = useState<RuntimeSessionCheckpointPreviewMode>("both");
  const restorePending = Boolean(checkpointRestorePendingId);
  const restoreSummaryText = checkpointRestoreSummary
    ? t("panels.artifacts.checkpointDetail.restoreNotice", {
        checkpoint: checkpointRestoreSummary.checkpointId.slice(0, 12),
        conversation: t(
          checkpointRestoreSummary.conversationChanged
            ? "panels.artifacts.checkpointDetail.restoreConversationChanged"
            : "panels.artifacts.checkpointDetail.restoreConversationUnchanged",
        ),
        files: checkpointRestoreSummary.appliedPaths,
        mode: t(
          RESTORE_MODE_OPTIONS.find(
            (option) => option.value === checkpointRestoreSummary.mode,
          )?.labelKey ?? RESTORE_MODE_OPTIONS[2].labelKey,
        ),
      })
    : null;

  return (
    <section className="min-h-0 overflow-hidden rounded-panel-lg border border-white/8 bg-[linear-gradient(180deg,rgba(255,255,255,0.05),rgba(255,255,255,0.02))]">
      {selectedCheckpoint ? (
        <div className="flex h-full min-h-0 flex-col">
          <div className="border-b border-white/8 px-3.5 py-3.5">
            <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">
              {t("panels.artifacts.checkpointDetail.title")}
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
              <Badge>{t("panels.artifacts.checkpointDetail.badgeCheckpoint")}</Badge>
            </div>

            <div className="mt-3 flex flex-wrap gap-1.5">
              <Badge>
                {t("panels.artifacts.checkpointDetail.messageCount", {
                  count: selectedCheckpoint.message_count,
                })}
              </Badge>
              {selectedCheckpoint.conversation_exact ? (
                <Badge>
                  {t("panels.artifacts.checkpointDetail.exactConversation")}
                </Badge>
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
              <div className="mt-3 rounded-[0.75rem] border border-white/8 bg-black/20 px-3 py-3">
                <div className="app-text-11 uppercase tracking-[0.18em] text-muted-foreground">
                  {t("panels.artifacts.checkpointDetail.restoreModeLegend")}
                </div>
                <div className="mt-2 space-y-1.5">
                  {RESTORE_MODE_OPTIONS.map((option) => (
                    <label
                      key={option.value}
                      className={cn(
                        "flex cursor-pointer items-start gap-2.5 rounded-[0.75rem] border px-3 py-2 transition",
                        restoreMode === option.value
                          ? "border-accent-teal/30 bg-accent-teal/10 text-foreground"
                          : "border-white/8 bg-white/[0.02] text-muted-foreground hover:border-white/14",
                      )}
                    >
                      <input
                        checked={restoreMode === option.value}
                        className="mt-0.5 accent-accent-teal"
                        disabled={restorePending}
                        name="checkpoint-restore-mode"
                        onChange={() => setRestoreMode(option.value)}
                        type="radio"
                        value={option.value}
                      />
                      <span className="min-w-0">
                        <span className="block app-text-13">{t(option.labelKey)}</span>
                        <span className="mt-0.5 block text-xs leading-5 text-muted-foreground">
                          {t(option.hintKey)}
                        </span>
                      </span>
                    </label>
                  ))}
                </div>
                <div className="mt-2.5 flex flex-wrap items-center justify-between gap-2">
                  <span className="text-xs leading-5 text-muted-foreground">
                    {t("panels.artifacts.checkpointDetail.restoreHint")}
                  </span>
                  <Button
                    aria-label={t("panels.artifacts.checkpointDetail.confirmRestore")}
                    disabled={restorePending}
                    onClick={() => onRestoreCheckpoint(restoreMode)}
                    size="sm"
                    type="button"
                    variant="primary"
                  >
                    {restorePending ? (
                      <LoaderCircleIcon className="animate-spin" size={14} />
                    ) : null}
                    {restorePending
                      ? t("panels.artifacts.checkpointDetail.restorePending")
                      : t("panels.artifacts.checkpointDetail.confirmRestore")}
                  </Button>
                </div>
              </div>
            ) : null}

            {checkpointRestoreError ? (
              <div className="mt-3 rounded-[0.75rem] border border-accent-orange/18 bg-accent-orange/8 px-3 py-2 text-sm text-muted-foreground">
                {checkpointRestoreError}
              </div>
            ) : null}
            {restoreSummaryText ? (
              <div className="mt-3 rounded-[0.75rem] border border-accent-teal/18 bg-accent-teal/10 px-3 py-2 text-sm text-muted-foreground">
                {restoreSummaryText}
              </div>
            ) : null}
          </div>

          <div className="min-h-0 flex-1 overflow-auto p-3">
            <div className="grid min-h-full gap-3">
              <div className="space-y-3">
                {checkpointDetailsError ? (
                  <div className="rounded-card-lg border border-accent-orange/18 bg-accent-orange/8 px-3.5 py-3 text-sm leading-6 text-muted-foreground">
                    {checkpointDetailsError}
                  </div>
                ) : null}

                {checkpointConversationSummary.length > 0 ? (
                  <div className="rounded-panel border border-white/8 bg-white/4 p-3.5">
                    <div className="flex items-center gap-2 text-xs uppercase tracking-[0.18em] text-muted-foreground">
                      <ScrollTextIcon size={14} />
                      {t("panels.artifacts.checkpointDetail.conversationSnapshot")}
                    </div>
                    <div className="mt-2.5 space-y-2.5">
                      {checkpointConversationSummary.map((message, index) => (
                        <div
                          key={`${selectedCheckpoint.id}-conversation-${index}`}
                          className="rounded-[0.75rem] border border-white/8 bg-black/20 px-3.5 py-3"
                        >
                          <div className="app-text-11 uppercase tracking-[0.18em] text-accent-teal">
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
                  <div className="rounded-panel border border-white/8 bg-white/4 p-3.5">
                    <div className="flex items-center gap-2 text-xs uppercase tracking-[0.18em] text-muted-foreground">
                      <HistoryIcon size={14} />
                      {t("panels.artifacts.checkpointDetail.previewSummary")}
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

                <div className="overflow-hidden rounded-panel border border-white/8 bg-black/20">
                  <div className="flex items-center justify-between gap-3 border-b border-white/8 px-3 py-2.5">
                    <div>
                      <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">
                        {t("panels.artifacts.checkpointDetail.fileDiffReader")}
                      </div>
                      <div className="mt-1 text-sm text-muted-foreground">
                        {t("panels.artifacts.checkpointDetail.fileDiffReaderHint")}
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
                <div className="rounded-panel border border-white/8 bg-white/4 p-3.5">
                  <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">
                    {t("panels.artifacts.checkpointDetail.snapshotMetadata")}
                  </div>
                  <div className="mt-2.5 space-y-2.5">
                    <div className="rounded-[0.75rem] border border-white/8 bg-black/20 px-3 py-2.5">
                      <div className="app-text-11 uppercase tracking-[0.16em] text-muted-foreground">
                        {t("panels.artifacts.checkpointDetail.summary")}
                      </div>
                      <div className="mt-2 text-sm leading-6 text-foreground">
                        {formatCheckpointMeta(selectedCheckpoint)}
                      </div>
                    </div>
                    <div className="rounded-[0.75rem] border border-white/8 bg-black/20 px-3 py-2.5">
                      <div className="app-text-11 uppercase tracking-[0.16em] text-muted-foreground">
                        {t("panels.artifacts.checkpointDetail.readingState")}
                      </div>
                      <div className="mt-2 text-sm leading-6 text-foreground">
                        {checkpointFilesForSelection.length > 0
                          ? `${checkpointFilesForSelection.length} captured files available`
                          : "Waiting for file details from runtime"}
                      </div>
                    </div>
                  </div>
                </div>

                <div className="rounded-panel border border-white/8 bg-white/4 p-3.5">
                  <div className="flex items-center justify-between gap-3">
                    <div className="text-xs uppercase tracking-[0.18em] text-muted-foreground">
                      {t("panels.artifacts.checkpointDetail.changedFiles")}
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
                                ? "border-accent-teal/30 bg-accent-teal/10"
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
                                  "rounded-control border px-2 py-0.5 app-text-10 uppercase tracking-[0.14em]",
                                  isActive
                                    ? "border-accent-teal/25 bg-accent-teal/10 text-accent-teal"
                                    : "border-white/10 bg-black/20 text-muted-foreground",
                                )}
                              >
                                {t("panels.artifacts.checkpointDetail.fileBadge")}
                              </span>
                            </div>
                          </button>
                        );
                      })
                    ) : (
                      <div className="rounded-[0.75rem] border border-dashed border-white/10 px-3 py-3 text-sm leading-6 text-muted-foreground">
                        {t("panels.artifacts.checkpointDetail.noFileDiffs")}
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
            <div className="mx-auto inline-flex size-10 items-center justify-center rounded-card border border-white/8 bg-white/[0.04] text-muted-foreground">
              <HistoryIcon size={18} />
            </div>
            <div className="mt-3 text-sm font-semibold">
              {t("panels.artifacts.checkpointDetail.empty")}
            </div>
            <div className="mt-2 text-sm leading-6 text-muted-foreground">
              {t("panels.artifacts.checkpointDetail.emptyHint")}
            </div>
          </div>
        </div>
      )}
    </section>
  );
}
