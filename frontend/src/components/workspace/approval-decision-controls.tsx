/**
 * §4.8：审批决定控件（记住作用域 + 说明 + 批准/拒绝）。
 *
 * 主会话待办条（`PendingInteractionBar`）与子代理下钻弹层
 * （`subagent-session-dialog`）共用同一份交互语义：
 *
 * - `rememberPattern` 由后端派生下发，缺省 = 该审批不可记忆（危险工具 / 硬问询 /
 *   敏感写 / 外部目录准入），此时不渲染「记住」入口，不给用户假选项；
 * - 勾选前就展示将记住的模式——让「记住」是可核对的决定而不是盲选；
 * - 「记住」默认关闭，且仅在批准时提交；拒绝永远不产生授权；
 * - 说明文本批准与拒绝都会随决策送达（拒绝时并入模型可见的决策原因）。
 *
 * 文案由调用方以 `labels` 传入（各入口分属不同 i18n 命名空间），本组件不做翻译。
 */
import { CheckIcon, XIcon } from "lucide-react";
import { useState } from "react";

import type { SessionApprovalRememberScope } from "@/api/runtime/sessions";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export type ApprovalDecisionOptions = {
  rememberScope?: SessionApprovalRememberScope;
  feedback?: string;
};

export type ApprovalDecisionLabels = {
  remember: string;
  rememberScopeLabel: string;
  rememberScopeSession: string;
  rememberScopeProject: string;
  /** 已插值好的「将记住：…」提示文案（由调用方带入 pattern）。 */
  rememberPattern: string;
  feedbackPlaceholder: string;
  approve: string;
  deny: string;
  /** 提交中时的批准按钮文案（缺省回退 `approve`）。 */
  busyApprove?: string;
};

type ApprovalDecisionControlsProps = {
  /** 后端建议的记忆模式；空 = 不可记忆，不渲染「记住」。 */
  rememberPattern?: string;
  disabled?: boolean;
  labels: ApprovalDecisionLabels;
  onDecide: (allow: boolean, options?: ApprovalDecisionOptions) => void;
  className?: string;
  /** 透传到批准/拒绝按钮的 `data-*`（既有测试与埋点的稳定锚点）。 */
  approveAttrs?: Record<string, string>;
  rejectAttrs?: Record<string, string>;
};

export function ApprovalDecisionControls({
  rememberPattern,
  disabled = false,
  labels,
  onDecide,
  className,
  approveAttrs,
  rejectAttrs,
}: ApprovalDecisionControlsProps) {
  // 调用方以 `key={交互条目 id}` 挂载：身份切换即重建（草稿不串条目）。
  const [remember, setRemember] = useState(false);
  const [scope, setScope] =
    useState<Exclude<SessionApprovalRememberScope, "once">>("session");
  const [feedback, setFeedback] = useState("");
  const pattern = rememberPattern?.trim() ?? "";
  const options = {
    ...(feedback.trim() ? { feedback } : {}),
    ...(remember && pattern ? { rememberScope: scope } : {}),
  };
  // 无任何选项时不传第三个参数（请求体保持旧形状，既有契约零扰动）。
  const decide = (allow: boolean) =>
    Object.keys(options).length > 0
      ? onDecide(allow, options)
      : onDecide(allow);

  return (
    <div
      className={cn("space-y-2", className)}
      data-approval-decision-controls
    >
      {pattern ? (
        <div className="space-y-1.5 rounded-field border border-border/70 bg-surface-solid/60 px-2.5 py-2">
          <label className="flex items-center gap-2 text-xs text-foreground">
            <input
              checked={remember}
              className="size-3.5 accent-[var(--accent-primary)]"
              disabled={disabled}
              type="checkbox"
              onChange={(event) => setRemember(event.target.checked)}
            />
            {labels.remember}
          </label>
          <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            {remember ? (
              <select
                aria-label={labels.rememberScopeLabel}
                className="h-7 rounded-field border border-border bg-surface-solid px-1.5 text-xs text-foreground"
                disabled={disabled}
                value={scope}
                onChange={(event) =>
                  setScope(
                    event.target.value === "project" ? "project" : "session",
                  )
                }
              >
                <option value="session">{labels.rememberScopeSession}</option>
                <option value="project">{labels.rememberScopeProject}</option>
              </select>
            ) : null}
            {/* 勾选前就展示将记住的模式：让「记住」是可核对的决定而不是盲选。 */}
            <span className="font-mono text-[11px] break-all">
              {labels.rememberPattern}
            </span>
          </div>
        </div>
      ) : null}
      <input
        aria-label={labels.feedbackPlaceholder}
        className="h-8 w-full rounded-field border border-border bg-surface-solid px-2.5 text-xs text-foreground outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring"
        disabled={disabled}
        placeholder={labels.feedbackPlaceholder}
        value={feedback}
        onChange={(event) => setFeedback(event.target.value)}
      />
      <div className="flex flex-wrap items-center gap-2">
        <Button
          disabled={disabled}
          size="sm"
          variant="primary"
          onClick={() => decide(true)}
          {...approveAttrs}
        >
          <CheckIcon className="size-3.5" />
          {disabled && labels.busyApprove ? labels.busyApprove : labels.approve}
        </Button>
        <Button
          disabled={disabled}
          size="sm"
          variant="destructive"
          onClick={() => decide(false)}
          {...rejectAttrs}
        >
          <XIcon className="size-3.5" />
          {labels.deny}
        </Button>
      </div>
    </div>
  );
}
