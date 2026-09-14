// 批次 E1（§8.4 规则 9 / §8.5 fallback）：未知事件类型兜底行。
// 契约：不得白屏、不得抛错、不得吞事件；保留原始载荷的只读 JSON 视图，标题走 i18n。

import { TriangleAlertIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

/** JSON 序列化对循环引用 / BigInt 不抛错：兜底行本身绝不能成为新的崩溃点。 */
function safeJson(raw: unknown): string {
  try {
    return JSON.stringify(raw, null, 2) ?? String(raw);
  } catch {
    return String(raw);
  }
}

export function FlowFallbackRow({
  anchorKey,
  flowKey,
  raw,
}: {
  anchorKey?: string;
  flowKey?: string;
  raw: unknown;
}) {
  const { t } = useTranslation("workspace");
  const json = safeJson(raw);

  return (
    <div
      className="min-w-0"
      data-chat-anchor-key={anchorKey}
      data-chat-flow-key={flowKey}
      data-chat-flow-kind="fallback"
    >
      <div className="flex items-center gap-1.5 app-chat-process-row text-muted-foreground">
        <TriangleAlertIcon aria-hidden="true" className="size-4 shrink-0 text-accent-gold" />
        <span className="shrink-0 font-semibold text-accent-gold">
          {t("panels.messages.flowFallback.title")}
        </span>
        <span aria-hidden="true" className="chat-row-sep" />
        <span className="min-w-0 truncate">
          {t("panels.messages.flowFallback.hint")}
        </span>
      </div>
      <pre className="mt-1 max-h-48 overflow-auto rounded-lg bg-surface-soft px-2.5 py-2 font-mono app-text-11 text-muted-foreground">
        {json}
      </pre>
    </div>
  );
}
