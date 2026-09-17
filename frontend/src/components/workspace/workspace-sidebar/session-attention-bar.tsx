// Batch 3（多会话并发运行时 §4.7.2）：待办聚合入口——「哪个会话在等我」。
//
// 侧栏行图标仍是逐会话线索；本入口把「等待类」会话聚成一条可点击横幅，点击直达
// 最靠前的那条（顺序 = 侧栏行顺序）。无等待会话时不渲染，不占位。

import { BellRingIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

export type SessionAttentionBarProps = {
  /** 「在等我」的会话 id（按侧栏行顺序，见 `collectAttentionSessionIds`）。 */
  sessionIds: readonly string[];
  /**
   * 点击入口：跳到最靠前的等待会话。缺省时只展示计数（例如目标线程不在当前列表里），
   * 不渲染成空转按钮。
   */
  onOpenSession?: () => void;
};

export function SessionAttentionBar({
  sessionIds,
  onOpenSession,
}: SessionAttentionBarProps) {
  const { t } = useTranslation("workspace");

  const firstSessionId = sessionIds[0];
  if (!firstSessionId) {
    return null;
  }

  const label = t("sidebar.attention.summary", { count: sessionIds.length });
  const content = (
    <>
      <BellRingIcon size={13} />
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {onOpenSession ? (
        <span className="shrink-0 rounded-chip border border-accent-primary-border px-1.5 py-0.5 app-text-10 text-accent-primary">
          {t("sidebar.attention.jump")}
        </span>
      ) : null}
    </>
  );

  const className =
    "flex w-full items-center gap-2 rounded-card border border-accent-primary-border bg-accent-primary-soft px-2.5 py-1.5 text-xs font-medium text-foreground";

  if (!onOpenSession) {
    return (
      <div className={className} data-testid="session-attention-bar" title={t("sidebar.attention.hint")}>
        {content}
      </div>
    );
  }

  return (
    <button
      className={`${className} transition hover:border-accent-primary-border hover:bg-surface-soft`}
      data-testid="session-attention-bar"
      onClick={() => onOpenSession()}
      title={t("sidebar.attention.hint")}
      type="button"
    >
      {content}
    </button>
  );
}
