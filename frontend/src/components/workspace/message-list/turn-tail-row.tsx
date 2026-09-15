// 批次 C2（§8.5 turn-tail）：28px 动作组 + 统计文本（默认隐藏，hover / 键盘焦点显现）。
// 统计口径说明：本地消息模型无时间戳（无「用时 / 首 token」数据源），故统计轴取既有
// `lib/turn-usage` 的 token 用量；无完整用量时整段隐藏（不显示 0）。
//
// 批次 2（§5.4）：新增「在新对话中分支」入口。可用性由宿主在**整段历史**上求解后下发
// （`lib/chat-view/branch-availability` 的 `resolveBranchAnchors`）：**只有锚点消息会拿到
// `onBranch`**，非锚点（含只有推理的消息）连按钮都不渲染 —— 不再出现「可见但不可用」的
// 常驻禁用图标，也不再有 `aria-disabled` 不可用态。

import {
  CopyIcon,
  GitBranchIcon,
  LoaderCircleIcon,
  RotateCcwIcon,
} from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { type ChatMessage } from "@/data/mock";
import { answerTextOf, hasVisibleText } from "@/lib/chat-view/visible-text";
import { type TurnUsage } from "@/lib/turn-usage";
import { cn } from "@/lib/utils";

type TurnTailRowProps = {
  anchorKey?: string;
  /** 本行锚点的分支请求在途：显示 spinner 并拦截点击。 */
  branchPending?: boolean;
  flowKey?: string;
  message: ChatMessage;
  /** 分支入口：宿主只对可分支锚点下发；非锚点不传 ⇒ 按钮不渲染。 */
  onBranch?: () => void;
  /** 重试入口（宿主提供才渲染；本地当前无回合级重试事件）。 */
  onRetry?: () => void;
  usage: TurnUsage | null;
};

/** 动作按钮统一形制：28×28 圆图标，默认三级文本，hover 升二级（与用户气泡同口径）。 */
const ACTION_BUTTON_CLASS =
  "app-hover-reveal inline-flex size-7 shrink-0 items-center justify-center rounded-full text-muted-foreground transition hover:bg-surface-soft hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none";

export function TurnTailRow({
  anchorKey,
  branchPending = false,
  flowKey,
  message,
  onBranch,
  onRetry,
  usage,
}: TurnTailRowProps) {
  const { t } = useTranslation("workspace");
  const [copied, setCopied] = useState(false);
  const text = answerTextOf(message);
  const branchLabel = branchPending
    ? t("panels.messages.branch.pending")
    : t("panels.messages.branch.label");
  // 双保险：宿主只对锚点下发 `onBranch`（`resolveBranchAnchors` 已排除无正文的消息），
  // 行内再按可见正文兜一层 —— 只有推理 / 只有工具的消息永不渲染分支入口。
  const canBranch = Boolean(onBranch) && hasVisibleText(text);
  // §12.1.4：没有任何可呈现内容时不渲染整行——28px 动作行会给每个空回合留一条空行。
  const hasActions =
    hasVisibleText(text) || canBranch || Boolean(onRetry) || Boolean(usage);

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
      {/* 批次 2（§5.4）：分支入口挂在轮末尾行（而不是用户气泡）。宿主只对
          `resolveBranchAnchors` 命中的锚点下发 `onBranch`，因此这里渲染即代表可用；
          在途时只做 spinner + 点击拦截，不引入禁用态外观。 */}
      {canBranch ? (
        <button
          aria-busy={branchPending ? "true" : undefined}
          aria-label={branchLabel}
          className={cn(ACTION_BUTTON_CLASS, branchPending ? "cursor-wait" : null)}
          data-branch-state={branchPending ? "pending" : "available"}
          onClick={(event) => {
            event.stopPropagation();
            if (branchPending) return;
            onBranch?.();
          }}
          title={branchLabel}
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
