// 批次 B2（§8.5 assistant-step / Think）：24px 单行（折叠）→ Markdown（展开）。
// 行本身复用 B1 共享控件；展开区与正文同排版（app-chat-copy），不再单独标定字号。

import { BrainCircuitIcon } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { ChatProcessRow } from "@/components/workspace/chat-process-row";
import { MessageMarkdown } from "@/components/workspace/message-markdown";
import { displayReasoningText } from "@/lib/trajectory/reasoning-window";
import { type ReasoningMessageSegment } from "@/lib/workspace-thread-state";

type MessageReasoningRowProps = {
  anchorKey?: string;
  flowKey?: string;
  segment: ReasoningMessageSegment;
  streaming?: boolean;
};

function summarizeReasoning(content: string): string {
  const compact = content.replace(/\s+/g, " ").trim();
  return compact.length > 96 ? `${compact.slice(0, 96).trimEnd()}…` : compact;
}

export function MessageReasoningRow({
  anchorKey,
  flowKey,
  segment,
  streaming = false,
}: MessageReasoningRowProps) {
  const { t } = useTranslation("workspace");
  const [open, setOpen] = useState(false);
  const baseId = useId();
  const panelId = `${baseId}-panel`;
  const running = streaming && segment.running !== false;
  const summary = summarizeReasoning(segment.content);
  const hasContent = segment.content.trim().length > 0;
  const reasoningDisplay = displayReasoningText(segment.content);
  const trimmed = reasoningDisplay.droppedChars > 0;
  const title = t("panels.messages.reasoningRow.title");

  return (
    <ChatProcessRow
      anchorKey={anchorKey}
      expandable
      expanded={open}
      flowKey={flowKey}
      icon={<BrainCircuitIcon className="size-4 text-accent-teal" />}
      onToggle={() => setOpen((current) => !current)}
      panelId={panelId}
      rowKind="reasoning"
      summary={hasContent ? summary : t("panels.messages.reasoningRow.empty")}
      title={running ? `${title}…` : title}
      titleClassName="text-muted-foreground"
      toggleLabel={t(
        open
          ? "panels.messages.reasoningRow.collapseLabel"
          : "panels.messages.reasoningRow.expandLabel",
      )}
      trailing={
        running ? (
          <span
            aria-hidden="true"
            className="size-1.5 shrink-0 animate-pulse rounded-full bg-accent-teal"
          />
        ) : null
      }
    >
      {trimmed ? (
        <div className="pb-1 app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
          {t("panels.messages.reasoningRow.trimmed", {
            chars: reasoningDisplay.droppedChars.toLocaleString(),
          })}
        </div>
      ) : null}
      <div className="app-chat-copy pb-1 text-muted-foreground">
        <MessageMarkdown content={reasoningDisplay.visible} />
      </div>
    </ChatProcessRow>
  );
}
