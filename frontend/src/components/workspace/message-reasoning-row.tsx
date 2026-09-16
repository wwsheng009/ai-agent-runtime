// 批次 B2（§8.5 assistant-step / Think）：24px 单行（折叠）→ Markdown（展开）。
// 行本身复用 B1 共享控件；展开区与正文同排版（app-chat-copy），不再单独标定字号。

import { BrainCircuitIcon } from "lucide-react";
import { useId, useState } from "react";
import { useTranslation } from "react-i18next";

import { ChatProcessRow } from "@/components/workspace/chat-process-row";
import { MessageMarkdown } from "@/components/workspace/message-markdown";
import { hasVisibleText } from "@/lib/chat-view/visible-text";
import { useLiveStreamEntry } from "@/lib/live-stream-text";
import { displayReasoningText } from "@/lib/trajectory/reasoning-window";
import { type ReasoningMessageSegment } from "@/lib/workspace-thread-state";

type MessageReasoningRowProps = {
  anchorKey?: string;
  flowKey?: string;
  /** live 通道键（消息 id）：只由「正在增长的推理段」这一行携带。 */
  liveStreamId?: string | null;
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
  liveStreamId,
  segment,
  streaming = false,
}: MessageReasoningRowProps) {
  const { t } = useTranslation("workspace");
  const [open, setOpen] = useState(false);
  const baseId = useId();
  const panelId = `${baseId}-panel`;
  // live 通道：推理增量同样不再写页面级 thread state（见 lib/live-stream-text.ts），
  // 只有本行随增量重渲染。
  const live = useLiveStreamEntry(streaming ? liveStreamId : null);
  // live 记录按**当前推理块**寻址（见 lib/live-stream-text.ts）：只有它是这一行
  // store 副本的延长（前缀一致）时才用它。其余情况回落 store 副本——
  // - reload / 重连后 live 从半截重新起算 → 不是前缀，回落，已显示的推理不被截断；
  // - live 属于**另一块**推理（工具行之后新起的一块还没落到 store）→ 回落。
  // 旧实现只比长度（`live.length >= segment.content.length`），于是「尾行 + 整轮
  // 累加的 live 文本」会把前几块推理也拼进这一行——推理渲染全并成一段。
  const content =
    live && live.reasoningText.startsWith(segment.content)
      ? live.reasoningText
      : segment.content;
  const running = streaming && segment.running !== false;
  const summary = summarizeReasoning(content);
  const reasoningDisplay = displayReasoningText(content);
  const trimmed = reasoningDisplay.droppedChars > 0;
  const title = t("panels.messages.reasoningRow.title");

  // §12.1.4：无推理正文时不渲染行（既不用占位文案顶上屏，也不留 24px 空行）。
  // 流式窗口内首块到达前的空壳同样不占位；有内容后行自然出现。
  if (!hasVisibleText(content)) return null;

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
      summary={summary}
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
