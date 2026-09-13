/**
 * 轨迹详情面板（P2-1 验收②：与明细单行共用同一 Item 对象，无两套数据）。
 */
import { XIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { MessageMarkdown } from "@/components/workspace/message-markdown";
import { displayReasoningText } from "@/lib/trajectory/reasoning-window";
import type { TrajectoryItem } from "@/lib/trajectory/types";
import { cn } from "@/lib/utils";

import {
  subagentSessionTarget,
  type SubagentSessionTarget,
} from "./subagent-session-target";
import { trajectoryItemKindKey, trajectoryItemStatusKey } from "./trajectory-view-shared";

const STATUS_BADGE: Record<TrajectoryItem["status"], string> = {
  pending: "border-border bg-surface-soft text-muted-foreground",
  running: "border-accent-teal/20 bg-accent-teal/10 text-accent-teal",
  completed: "border-accent-teal/20 bg-accent-teal/10 text-accent-teal",
  failed: "border-accent-gold/24 bg-accent-gold/12 text-accent-gold",
  canceled: "border-border bg-surface-soft text-muted-foreground",
};

function StructuredPayload({ payload }: { payload: Record<string, unknown> }) {
  return (
    <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-md border border-border bg-surface-solid px-3 py-2.5 app-text-12 app-chat-copy text-muted-foreground">
      {JSON.stringify(payload, null, 2)}
    </pre>
  );
}

/** 推理内容：超长时只渲染末尾稳定窗口（P2-6，流式期间布局稳定）。 */
function ReasoningContent({ content }: { content: string }) {
  const { t } = useTranslation("workspace");
  const { visible, droppedChars, totalChars } = displayReasoningText(content);
  return (
    <div className="flex flex-col gap-1.5">
      {droppedChars > 0 ? (
        <div className="app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
          {t("panels.shell.trajectory.reasoningTrimmed", {
            dropped: droppedChars.toLocaleString(),
            total: totalChars.toLocaleString(),
          })}
        </div>
      ) : null}
      <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-md border border-border bg-surface-solid px-3 py-2.5 app-text-12 app-chat-copy text-muted-foreground">
        {visible}
      </pre>
    </div>
  );
}

export function TrajectoryDetailPanel({
  item,
  onClose,
  onOpenSubagentSession,
}: {
  item: TrajectoryItem | null;
  onClose: () => void;
  /** 子 agent 下钻入口（P1-5）：仅当 item 携带子会话 ID 时渲染按钮。 */
  onOpenSubagentSession?: (target: SubagentSessionTarget) => void;
}) {
  const { t } = useTranslation("workspace");

  if (!item) {
    return (
      <aside
        data-trajectory-detail
        className="hidden w-80 shrink-0 border-l border-border bg-surface-softer lg:block"
      />
    );
  }

  const subagentTarget = subagentSessionTarget(item);

  return (
    <aside
      data-trajectory-detail
      className="flex w-80 shrink-0 flex-col overflow-hidden border-l border-border bg-surface-softer"
    >
      <header className="flex items-center gap-2 border-b border-border px-3 py-2.5">
        <span className="min-w-0 flex-1 truncate app-text-13 font-semibold text-foreground">
          {t(trajectoryItemKindKey(item.kind))}
        </span>
        <span
          className={cn(
            "inline-flex shrink-0 items-center rounded-full border px-2 py-0.5 app-text-10 uppercase tracking-[0.12em]",
            STATUS_BADGE[item.status],
          )}
        >
          {t(trajectoryItemStatusKey(item.status))}
        </span>
        <button
          aria-label={t("panels.shell.trajectory.closeDetail")}
          className="shrink-0 rounded p-1 text-muted-foreground transition hover:bg-surface-soft hover:text-foreground"
          onClick={onClose}
          type="button"
        >
          <XIcon size={14} />
        </button>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto px-3 py-3">
        {subagentTarget && onOpenSubagentSession ? (
          <button
            aria-label={t("panels.shell.trajectory.openSubagentAria")}
            className="mb-3 flex w-full items-center justify-center gap-1.5 rounded-md border border-accent-teal/30 bg-accent-teal/10 px-2.5 py-1.5 app-text-12 text-accent-teal transition hover:bg-accent-teal/16"
            data-open-subagent-session={subagentTarget.sessionId}
            onClick={() => onOpenSubagentSession(subagentTarget)}
            type="button"
          >
            {t("panels.shell.trajectory.openSubagent")}
          </button>
        ) : null}
        <dl className="mb-3 grid grid-cols-2 gap-x-3 gap-y-1.5 app-text-11">
          <div>
            <dt className="text-muted-foreground">{t("panels.shell.trajectory.fields.seq")}</dt>
            <dd className="font-mono text-foreground">{item.seq}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">{t("panels.shell.trajectory.fields.id")}</dt>
            <dd className="truncate font-mono text-foreground" title={item.id}>
              {item.id}
            </dd>
          </div>
          {item.causeId ? (
            <div>
              <dt className="text-muted-foreground">{t("panels.shell.trajectory.fields.cause")}</dt>
              <dd className="truncate font-mono text-foreground" title={item.causeId}>
                {item.causeId}
              </dd>
            </div>
          ) : null}
          <div>
            <dt className="text-muted-foreground">{t("panels.shell.trajectory.fields.updated")}</dt>
            <dd className="font-mono text-foreground">#{item.updatedAt}</dd>
          </div>
        </dl>

        {item.head.kind === "text" ? (
          <MessageMarkdown
            className="app-text-13 leading-6 text-foreground"
            content={item.head.content}
            streaming={item.status === "running"}
          />
        ) : null}

        {item.head.kind === "reasoning" ? (
          <ReasoningContent content={item.head.content} />
        ) : null}

        {item.head.kind === "tool" ? (
          <div className="flex flex-col gap-2.5">
            <div className="flex items-center gap-2">
              <span className="app-text-13 font-semibold text-foreground">
                {item.head.name}
              </span>
              <span
                className={cn(
                  "inline-flex shrink-0 items-center rounded-full border px-2 py-0.5 app-text-10 uppercase tracking-[0.12em]",
                  STATUS_BADGE[item.status],
                )}
              >
                {item.head.phase}
              </span>
            </div>
            {item.head.argsSummary ? (
              <div>
                <div className="mb-1 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                  {t("panels.shell.trajectory.fields.arguments")}
                </div>
                <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded-md border border-border bg-surface-solid px-3 py-2 app-text-12 app-chat-copy text-muted-foreground">
                  {item.head.argsSummary}
                </pre>
              </div>
            ) : null}
            {item.head.resultSummary ? (
              <div>
                <div className="mb-1 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                  {t("panels.shell.trajectory.fields.result")}
                </div>
                <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words rounded-md border border-accent-teal/20 bg-accent-teal/8 px-3 py-2 app-text-12 app-chat-copy text-foreground">
                  {item.head.resultSummary}
                </pre>
              </div>
            ) : null}
            {item.head.errorMessage ? (
              <div>
                <div className="mb-1 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                  {t("panels.shell.trajectory.fields.error")}
                </div>
                <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded-md border border-accent-gold/24 bg-accent-gold/12 px-3 py-2 app-text-12 app-chat-copy text-accent-gold">
                  {item.head.errorMessage}
                </pre>
              </div>
            ) : null}
            {item.head.durationMs !== undefined ? (
              <div className="app-text-11 text-muted-foreground">
                {t("panels.shell.trajectory.fields.duration", {
                  ms: item.head.durationMs,
                })}
              </div>
            ) : null}
          </div>
        ) : null}

        {item.head.kind === "structured" ? (
          <StructuredPayload payload={item.head.payload} />
        ) : null}

        {item.head.kind === "system" ? (
          <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words rounded-md border border-border bg-surface-solid px-3 py-2.5 app-text-12 app-chat-copy text-muted-foreground">
            {item.head.note}
          </pre>
        ) : null}
      </div>
    </aside>
  );
}
