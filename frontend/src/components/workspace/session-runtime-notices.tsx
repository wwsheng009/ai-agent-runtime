// Batch 3（多会话并发运行时 §4.7.3）：后台会话「完成 / 待交互」的页内通知。
//
// 呈现约束：只在有通知时挂载；每条提供「前往会话」与「忽略」两个动作，点击卡片
// 主体同样跳到该会话（通知的终点是会话现场，而不是通知本身）。
// 桌面通知（浏览器不可见时才触发的 `maybeShowDesktopNotification`）与本组件互补：
// 一个负责离屏，一个负责在屏但不处于该会话。

import { BellIcon, XIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type {
  SessionRuntimeNotice,
  SessionRuntimeNoticeKind,
} from "@/lib/session-runtime/notices";

/** 标题 / 正文的文案键（联合字面量，保持 i18next 的键类型检查）。 */
const KIND_TITLE_KEYS = {
  turn_finished: "notices.kinds.turn_finished",
  approval: "notices.kinds.approval",
  question: "notices.kinds.question",
  plan_review: "notices.kinds.plan_review",
} as const satisfies Record<SessionRuntimeNoticeKind, string>;

const KIND_BODY_KEYS = {
  turn_finished: "notices.bodies.turn_finished",
  approval: "notices.bodies.approval",
  question: "notices.bodies.question",
  plan_review: "notices.bodies.plan_review",
} as const satisfies Record<SessionRuntimeNoticeKind, string>;

export type SessionRuntimeNoticesProps = {
  notices: readonly SessionRuntimeNotice[];
  onDismiss: (id: string) => void;
  onOpenSession: (sessionId: string) => void;
  /** 会话标题解析；缺省或解析不到时回落「会话 {{sessionId}}」，不伪造标题。 */
  resolveSessionTitle?: (sessionId: string) => string | undefined;
};

export function SessionRuntimeNotices({
  notices,
  onDismiss,
  onOpenSession,
  resolveSessionTitle,
}: SessionRuntimeNoticesProps) {
  const { t } = useTranslation("workspace");

  if (notices.length === 0) {
    return null;
  }

  return (
    <div
      aria-live="polite"
      className="pointer-events-none fixed bottom-4 right-4 z-50 flex w-80 max-w-[calc(100vw-2rem)] flex-col gap-2"
      data-testid="session-runtime-notices"
      role="status"
    >
      {notices.map((notice) => {
        const sessionTitle =
          resolveSessionTitle?.(notice.sessionId)?.trim() ||
          t("notices.sessionFallback", { sessionId: notice.sessionId });
        return (
          <div
            className="pointer-events-auto rounded-[0.6rem] border border-border bg-surface-popover p-3 shadow-lg"
            data-notice-kind={notice.kind}
            key={notice.id}
          >
            <div className="flex items-start gap-2">
              <BellIcon
                className="mt-0.5 shrink-0 text-accent-primary"
                size={14}
              />
              <div className="min-w-0 flex-1">
                <div className="truncate text-xs font-medium text-foreground">
                  {t(KIND_TITLE_KEYS[notice.kind], { session: sessionTitle })}
                </div>
                <div className="mt-0.5 app-text-10 text-muted-foreground">
                  {t(KIND_BODY_KEYS[notice.kind])}
                </div>
                <button
                  className="mt-1.5 rounded-chip border border-border px-1.5 py-0.5 app-text-10 text-foreground transition hover:bg-surface-soft"
                  onClick={() => onOpenSession(notice.sessionId)}
                  type="button"
                >
                  {t("notices.open")}
                </button>
              </div>
              <button
                aria-label={t("notices.dismiss")}
                className="shrink-0 rounded-chip p-0.5 text-muted-foreground transition hover:bg-surface-soft hover:text-foreground"
                onClick={() => onDismiss(notice.id)}
                title={t("notices.dismiss")}
                type="button"
              >
                <XIcon size={12} />
              </button>
            </div>
          </div>
        );
      })}
    </div>
  );
}
