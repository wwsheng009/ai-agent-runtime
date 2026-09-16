// P1-6 / 批次 B4（§5.5 / §8.5）：工具行面板。
//
// 折叠契约：折叠态工具行只有 24px 单行（结果/错误/输入均在展开面板内），
// 否则不满足「折叠态 24px / 失败态行高不变」。失败态用错误块替换输出块（不追加）。
// 面板：无 1px 边框，用底色 + 间距分隔（B4）。

import { useTranslation } from "react-i18next";

import { type ToolMessageSegment } from "@/lib/thread-state/messages";
import {
  formatToolInputPreview,
  resolveToolSegmentDetails,
  type ToolCardKind,
} from "@/lib/tool-row";
import { stripRenderedPatch } from "@/lib/tool-row/diff-text";
import { cn } from "@/lib/utils";

import { ToolRowDiffPanel } from "./tool-row-diff-panel";

type ToolRowPanelsProps = {
  expandable: boolean;
  isFailure: boolean;
  kind: ToolCardKind;
  open: boolean;
  panelId: string;
  segment: ToolMessageSegment;
};

function prettyJson(text: string) {
  const trimmed = text.trim();
  if (!trimmed || (!trimmed.startsWith("{") && !trimmed.startsWith("["))) {
    return text;
  }
  try {
    return JSON.stringify(JSON.parse(trimmed), null, 2);
  } catch {
    return text;
  }
}

export function ToolRowPanels({
  expandable,
  isFailure,
  kind,
  open,
  panelId,
  segment,
}: ToolRowPanelsProps) {
  const { t } = useTranslation("workspace");
  const failureText = segment.errorMessage?.trim() || segment.resultSummary?.trim() || "";
  const details = kind === "diff" && !isFailure ? resolveToolSegmentDetails(segment) : undefined;
  const diffText = details?.diffText;
  const rawOutputText = segment.resultSummary?.trim() || "";
  // 行级视图已经承载补丁正文：输出面板不再重复原始 diff 文本（原文仍可复制 / 下载）。
  const outputText = diffText ? stripRenderedPatch(rawOutputText) : rawOutputText;
  // 回放链路的入参是原始 JSON、实时链路是后端预览文本：面板统一成同一套键值展示，
  // 否则同一次调用在两条链路上「展开后长得不一样」（折叠行已统一，见 orderSummaryParts）。
  const inputText = formatToolInputPreview(segment.argsSummary ?? "");
  const detail = isFailure
    ? failureText
      ? (
          <div
            className="mt-1 rounded-lg bg-accent-gold/8 px-2.5 py-2 app-text-11 text-accent-orange"
            data-tool-row-output="error"
            role="note"
          >
            {failureText}
          </div>
        )
      : null
    : outputText
      ? (
          <div
            className="mt-1 rounded-lg bg-surface-soft px-2.5 py-2"
            data-tool-row-output="result"
          >
            <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.messages.toolRow.outputLabel")}
            </div>
            <pre className="mt-1.5 max-h-48 overflow-y-auto whitespace-pre-wrap break-words app-text-12 app-chat-copy text-foreground">
              {kind === "json" ? prettyJson(outputText) : outputText}
            </pre>
          </div>
        )
      : null;

  if (!expandable) {
    return null;
  }

  return (
    <div
      className={cn(!open && "hidden")}
      data-tool-row-detail-panel="true"
      hidden={!open}
      id={panelId}
    >
      {detail}
      {diffText ? (
        <ToolRowDiffPanel
          filePathHint={details?.filePath}
          patchText={diffText}
          stats={details?.diff}
          truncated={details?.diffTextTruncated === true}
        />
      ) : inputText ? (
        <div className="mt-1 rounded-lg bg-surface-soft px-2.5 py-2" data-tool-row-input-panel="true">
          <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
            {t("panels.messages.toolRow.inputLabel")}
          </div>
          <pre className="mt-1.5 max-h-48 overflow-y-auto whitespace-pre-wrap break-words app-text-12 app-chat-copy text-muted-foreground">
            {inputText}
          </pre>
        </div>
      ) : null}
    </div>
  );
}
