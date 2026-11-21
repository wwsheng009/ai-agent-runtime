// composer 工具条尾部动作（从 message-composer.tsx 抽出，P0-2 行数约束）。
// 只搬 JSX：发送/停止回调、禁用原因与宿主注入控件仍由 MessageComposer 结算。
import type { ReactNode } from "react";
import { ArrowUpIcon, SquareIcon } from "lucide-react";

import { Button } from "@/components/ui/button";

export type ComposerTrailingActionsProps = {
  contextUsageControl?: ReactNode;
  isResponding: boolean;
  onSubmit: () => void;
  onStop: () => void;
  profileControl?: ReactNode;
  submitButtonLabel: string;
  submitButtonTitle: string;
  submitDisabled: boolean;
  submitDisabledHint: string;
  submitDisabledHintId: string;
};

export function ComposerTrailingActions({
  contextUsageControl,
  isResponding,
  onSubmit,
  onStop,
  profileControl,
  submitButtonLabel,
  submitButtonTitle,
  submitDisabled,
  submitDisabledHint,
  submitDisabledHintId,
}: ComposerTrailingActionsProps) {
  return (
    <div
      className="flex min-w-0 shrink-0 flex-nowrap items-center justify-end gap-2"
      data-composer-trailing-actions
    >
      {profileControl ? <div className="shrink-0">{profileControl}</div> : null}
      {contextUsageControl ? (
        <div className="min-w-0 flex-[0_1_auto]">{contextUsageControl}</div>
      ) : null}
      <Button
        variant="secondary"
        size="icon"
        aria-label={submitButtonLabel}
        aria-describedby={submitDisabled ? submitDisabledHintId : undefined}
        title={submitButtonTitle}
        className={
          isResponding
            ? "size-8 shrink-0 border-accent-secondary-border bg-accent-secondary-soft p-0 text-foreground shadow-none hover:border-accent-secondary-border hover:bg-accent-secondary-soft"
            : "size-8 shrink-0 border-border bg-surface-soft p-0 text-foreground shadow-none hover:border-border-strong hover:bg-surface-soft-hover disabled:border-2 disabled:border-dashed disabled:border-border-strong disabled:bg-surface-soft-hover disabled:text-foreground disabled:opacity-100"
        }
        onClick={isResponding ? onStop : onSubmit}
        disabled={submitDisabled}
      >
        {isResponding ? (
          <SquareIcon size={14} />
        ) : (
          <ArrowUpIcon size={16} strokeWidth={2.5} />
        )}
        <span className="sr-only">{submitButtonLabel}</span>
      </Button>
      {submitDisabled ? (
        <span id={submitDisabledHintId} className="sr-only">
          {submitDisabledHint}
        </span>
      ) : null}
    </div>
  );
}