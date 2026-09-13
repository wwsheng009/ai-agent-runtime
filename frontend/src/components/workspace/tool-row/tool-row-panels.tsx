// P1-6：工具行面板。失败态用错误块替换输出块（不追加），输入始终在展开面板内。

import { useTranslation } from "react-i18next";

import { type ToolMessageSegment } from "@/lib/thread-state/messages";
import { type ToolCardKind } from "@/lib/tool-row";
import { cn } from "@/lib/utils";

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
  const outputText = segment.resultSummary?.trim() || "";

  return (
    <>
      {isFailure
        ? failureText
          ? (
              <div
                className="border-t border-accent-gold/16 bg-accent-gold/8 px-3 py-2.5 app-text-11 text-accent-gold"
                data-tool-row-output="error"
                role="note"
              >
                {failureText}
              </div>
            )
          : null
        : outputText
          ? (
              <div className="border-t border-border px-3 py-2.5" data-tool-row-output="result">
                <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
                  {t("panels.messages.toolRow.outputLabel")}
                </div>
                <pre className="mt-1.5 max-h-48 overflow-y-auto whitespace-pre-wrap break-words app-text-12 app-chat-copy text-foreground">
                  {kind === "json" ? prettyJson(outputText) : outputText}
                </pre>
              </div>
            )
          : null}

      {expandable ? (
        <div
          className={cn("border-t border-border", !open && "hidden")}
          data-tool-row-input-panel="true"
          hidden={!open}
          id={panelId}
        >
          <div className="px-3 py-2.5">
            <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
              {t("panels.messages.toolRow.inputLabel")}
            </div>
            <pre className="mt-1.5 max-h-48 overflow-y-auto whitespace-pre-wrap break-words app-text-12 app-chat-copy text-muted-foreground">
              {segment.argsSummary}
            </pre>
          </div>
        </div>
      ) : null}
    </>
  );
}
