/**
 * 轨迹详情面板（P2-1 验收②：与明细单行共用同一 Item 对象，无两套数据）。
 */
import { XIcon } from "lucide-react";

import { MessageMarkdown } from "@/components/workspace/message-markdown";
import { displayReasoningText } from "@/lib/trajectory/reasoning-window";
import type { TrajectoryItem } from "@/lib/trajectory/types";
import { cn } from "@/lib/utils";

import { trajectoryItemKindLabel } from "./trajectory-view-shared";

const STATUS_BADGE: Record<TrajectoryItem["status"], string> = {
  pending: "border-[var(--border)] bg-[var(--surface-soft)] text-[var(--muted-foreground)]",
  running: "border-[#8fd0c6]/20 bg-[#8fd0c6]/10 text-[#8fd0c6]",
  completed: "border-[#8fd0c6]/20 bg-[#8fd0c6]/10 text-[#8fd0c6]",
  failed: "border-[#f0c77b]/24 bg-[#f0c77b]/12 text-[#f0c77b]",
  canceled: "border-[var(--border)] bg-[var(--surface-soft)] text-[var(--muted-foreground)]",
};

function StructuredPayload({ payload }: { payload: Record<string, unknown> }) {
  return (
    <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-md border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2.5 app-text-12 app-chat-copy text-[var(--muted-foreground)]">
      {JSON.stringify(payload, null, 2)}
    </pre>
  );
}

/** 推理内容：超长时只渲染末尾稳定窗口（P2-6，流式期间布局稳定）。 */
function ReasoningContent({ content }: { content: string }) {
  const { visible, droppedChars, totalChars } = displayReasoningText(content);
  return (
    <div className="flex flex-col gap-1.5">
      {droppedChars > 0 ? (
        <div className="app-text-10 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
          {droppedChars.toLocaleString()} leading chars trimmed ·{" "}
          {totalChars.toLocaleString()} total
        </div>
      ) : null}
      <pre className="max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-md border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2.5 app-text-12 app-chat-copy text-[var(--muted-foreground)]">
        {visible}
      </pre>
    </div>
  );
}

export function TrajectoryDetailPanel({
  item,
  onClose,
}: {
  item: TrajectoryItem | null;
  onClose: () => void;
}) {
  if (!item) {
    return (
      <aside
        data-trajectory-detail
        className="hidden w-80 shrink-0 border-l border-[var(--border)] bg-[var(--surface-softer)] lg:block"
      />
    );
  }

  return (
    <aside
      data-trajectory-detail
      className="flex w-80 shrink-0 flex-col overflow-hidden border-l border-[var(--border)] bg-[var(--surface-softer)]"
    >
      <header className="flex items-center gap-2 border-b border-[var(--border)] px-3 py-2.5">
        <span className="min-w-0 flex-1 truncate app-text-13 font-semibold text-[var(--foreground)]">
          {trajectoryItemKindLabel(item.kind)}
        </span>
        <span
          className={cn(
            "inline-flex shrink-0 items-center rounded-full border px-2 py-0.5 app-text-10 uppercase tracking-[0.12em]",
            STATUS_BADGE[item.status],
          )}
        >
          {item.status}
        </span>
        <button
          aria-label="Close trajectory detail"
          className="shrink-0 rounded p-1 text-[var(--muted-foreground)] transition hover:bg-[var(--surface-soft)] hover:text-[var(--foreground)]"
          onClick={onClose}
          type="button"
        >
          <XIcon size={14} />
        </button>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto px-3 py-3">
        <dl className="mb-3 grid grid-cols-2 gap-x-3 gap-y-1.5 app-text-11">
          <div>
            <dt className="text-[var(--muted-foreground)]">Seq</dt>
            <dd className="font-mono text-[var(--foreground)]">{item.seq}</dd>
          </div>
          <div>
            <dt className="text-[var(--muted-foreground)]">ID</dt>
            <dd className="truncate font-mono text-[var(--foreground)]" title={item.id}>
              {item.id}
            </dd>
          </div>
          {item.causeId ? (
            <div>
              <dt className="text-[var(--muted-foreground)]">Cause</dt>
              <dd className="truncate font-mono text-[var(--foreground)]" title={item.causeId}>
                {item.causeId}
              </dd>
            </div>
          ) : null}
          <div>
            <dt className="text-[var(--muted-foreground)]">Updated</dt>
            <dd className="font-mono text-[var(--foreground)]">#{item.updatedAt}</dd>
          </div>
        </dl>

        {item.head.kind === "text" ? (
          <MessageMarkdown
            className="app-text-13 leading-6 text-[var(--foreground)]"
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
              <span className="app-text-13 font-semibold text-[var(--foreground)]">
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
                <div className="mb-1 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                  Arguments
                </div>
                <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded-md border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2 app-text-12 app-chat-copy text-[var(--muted-foreground)]">
                  {item.head.argsSummary}
                </pre>
              </div>
            ) : null}
            {item.head.resultSummary ? (
              <div>
                <div className="mb-1 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                  Result
                </div>
                <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words rounded-md border border-[#8fd0c6]/20 bg-[#8fd0c6]/8 px-3 py-2 app-text-12 app-chat-copy text-[var(--foreground)]">
                  {item.head.resultSummary}
                </pre>
              </div>
            ) : null}
            {item.head.errorMessage ? (
              <div>
                <div className="mb-1 app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                  Error
                </div>
                <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded-md border border-[#f0c77b]/24 bg-[#f0c77b]/12 px-3 py-2 app-text-12 app-chat-copy text-[#f0c77b]">
                  {item.head.errorMessage}
                </pre>
              </div>
            ) : null}
            {item.head.durationMs !== undefined ? (
              <div className="app-text-11 text-[var(--muted-foreground)]">
                Duration: {item.head.durationMs}ms
              </div>
            ) : null}
          </div>
        ) : null}

        {item.head.kind === "structured" ? (
          <StructuredPayload payload={item.head.payload} />
        ) : null}

        {item.head.kind === "system" ? (
          <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words rounded-md border border-[var(--border)] bg-[var(--surface-solid)] px-3 py-2.5 app-text-12 app-chat-copy text-[var(--muted-foreground)]">
            {item.head.note}
          </pre>
        ) : null}
      </div>
    </aside>
  );
}
