// 批次 C2（§8.5 turn-tail）：28px 动作组 + 统计文本（默认隐藏，hover / 键盘焦点显现）。
// 统计口径说明：本地消息模型无时间戳（无「用时 / 首 token」数据源），故统计轴取既有
// `lib/turn-usage` 的 token 用量；无完整用量时整段隐藏（不显示 0）。
//
// 批次 2（§5.4）：新增「在新对话中分支」入口。可用性由宿主在**整条 flow** 上求解后下发
// （`lib/chat-view/branch-availability`）；不可用态**不用**原生 `disabled`——按钮保持可聚焦，
// 用 `aria-disabled` + `title` + `sr-only` 原因 + 点击拦截表达「可见但不可用」，
// 与 `user-message-bubble.tsx` 的动作区无障碍口径一致（全仓无 Tooltip 原语）。

import {
  CopyIcon,
  GitBranchIcon,
  LoaderCircleIcon,
  RotateCcwIcon,
} from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { type ChatMessage } from "@/data/mock";
import {
  BRANCH_UNAVAILABLE_REASON_KEY,
  type BranchUnavailableReasonKey,
} from "@/lib/chat-view/branch-availability";
import { hasVisibleText } from "@/lib/chat-view/visible-text";
import { type TurnUsage } from "@/lib/turn-usage";
import { cn } from "@/lib/utils";

type TurnTailRowProps = {
  anchorKey?: string;
  /** 本行是否是唯一可分支锚点（宿主在整条 flow 上求解一次后下发）。 */
  canBranch?: boolean;
  /** 不可用原因 i18n 键（`workspace` 命名空间）；缺省按「仅可从已完成轮次最后一条消息分支」。 */
  branchDisabledReason?: BranchUnavailableReasonKey;
  /** 本行锚点的分支请求在途：显示 spinner 并拦截点击。 */
  branchPending?: boolean;
  flowKey?: string;
  message: ChatMessage;
  /** 分支入口（宿主提供才渲染；不可用 / 在途时渲染但拦截点击）。 */
  onBranch?: () => void;
  /** 重试入口（宿主提供才渲染；本地当前无回合级重试事件）。 */
  onRetry?: () => void;
  usage: TurnUsage | null;
};

/** 动作按钮统一形制：28×28 圆图标，默认三级文本，hover 升二级（与用户气泡同口径）。 */
const ACTION_BUTTON_CLASS =
  "app-hover-reveal inline-flex size-7 shrink-0 items-center justify-center rounded-full text-muted-foreground transition hover:bg-surface-soft hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none";

/** 回合可见文本（复制目标）：只取正文段，工具/推理不参与。 */
function answerText(message: ChatMessage): string {
  return message.segments
    .filter(
      (segment): segment is Extract<ChatMessage["segments"][number], { type: "text" }> =>
        segment.type === "text",
    )
    .map((segment) => segment.content)
    .join("\n\n")
    .trim();
}

export function TurnTailRow({
  anchorKey,
  canBranch = false,
  branchDisabledReason,
  branchPending = false,
  flowKey,
  message,
  onBranch,
  onRetry,
  usage,
}: TurnTailRowProps) {
  const { t } = useTranslation("workspace");
  const [copied, setCopied] = useState(false);
  const text = answerText(message);
  const branchUnavailable = !canBranch;
  const branchBlocked = branchUnavailable || branchPending;
  const branchReasonKey = branchDisabledReason ?? BRANCH_UNAVAILABLE_REASON_KEY;
  const branchReasonId = `${message.id}-branch-reason`;
  const branchLabel = branchPending
    ? t("panels.messages.branch.pending")
    : t("panels.messages.branch.label");
  // §12.1.4：没有任何可呈现内容时不渲染整行——28px 动作行会给每个空回合留一条空行。
  const hasActions =
    hasVisibleText(text) || Boolean(onBranch) || Boolean(onRetry) || Boolean(usage);

  const copy = async () => {
    if (!hasVisibleText(text)) return;
    try {
      await navigator.clipboard?.writeText(text);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1200);
    } catch {
      // 剪贴板不可用（权限 / 非安全上下文）时静默：不阻塞会话阅读。
    }
  };

  if (!hasActions) return null;

  return (
    <div
      className="group -ml-1.5 flex h-7 items-center gap-2.5"
      data-chat-anchor-key={anchorKey}
      data-chat-flow-key={flowKey}
      data-chat-flow-kind="turn-tail"
    >
      {/* 复制图标按内容显示（§12.1.4）：该回合没有可见回答文本（工具 / 仅推理）时
          整颗图标不渲染，不留「禁用但常驻」的空动作位。 */}
      {hasVisibleText(text) ? (
        <button
          aria-label={t("panels.messages.turnTail.copyAriaLabel")}
          className={ACTION_BUTTON_CLASS}
          onClick={() => {
            void copy();
          }}
          type="button"
        >
          <CopyIcon aria-hidden="true" className="size-4" />
        </button>
      ) : null}
      {/* 批次 2（§5.4）：分支入口挂在轮末尾行（而不是用户气泡）。可用性由宿主在整条
          flow 上求解后下发：非锚点行「可见但不可用」，用 aria-disabled + title + sr-only
          原因 + 点击拦截表达，不占用原生 disabled（保留聚焦与读屏可达）。 */}
      {onBranch ? (
        <button
          aria-busy={branchPending ? "true" : undefined}
          aria-describedby={
            branchUnavailable && !branchPending ? branchReasonId : undefined
          }
          aria-disabled={branchBlocked ? "true" : undefined}
          aria-label={branchLabel}
          className={cn(
            ACTION_BUTTON_CLASS,
            branchBlocked
              ? "cursor-not-allowed opacity-40 hover:bg-transparent hover:text-muted-foreground"
              : null,
          )}
          data-branch-state={
            branchUnavailable
              ? "unavailable"
              : branchPending
                ? "pending"
                : "available"
          }
          onClick={(event) => {
            event.stopPropagation();
            if (branchBlocked) return;
            onBranch();
          }}
          title={branchUnavailable ? t(branchReasonKey) : branchLabel}
          type="button"
        >
          {branchPending ? (
            <LoaderCircleIcon
              aria-hidden="true"
              className="size-4 animate-spin"
            />
          ) : (
            <GitBranchIcon aria-hidden="true" className="size-4" />
          )}
          {branchUnavailable && !branchPending ? (
            <span className="sr-only" id={branchReasonId}>
              {t(branchReasonKey)}
            </span>
          ) : null}
        </button>
      ) : null}
      {onRetry ? (
        <button
          aria-label={t("panels.messages.turnTail.retryAriaLabel")}
          className={ACTION_BUTTON_CLASS}
          onClick={onRetry}
          type="button"
        >
          <RotateCcwIcon aria-hidden="true" className="size-4" />
        </button>
      ) : null}
      {usage ? (
        <span
          className="app-hover-reveal min-w-0 truncate app-text-11 text-muted-foreground"
          data-chat-turn-stats="true"
          title={t("panels.messages.turnTail.statsLabel")}
        >
          {copied
            ? t("panels.messages.turnTail.copied")
            : t("panels.messages.turnUsage.summary", {
                prompt: usage.promptTokens.toLocaleString(),
                completion: usage.completionTokens.toLocaleString(),
                total: usage.totalTokens.toLocaleString(),
              })}
        </span>
      ) : null}
    </div>
  );
}
