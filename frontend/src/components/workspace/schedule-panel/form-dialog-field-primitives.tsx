// 自动化任务表单的最小布局原子：分区壳与「标签 + 控件 + 错误/说明」字段行。
// 仅承载展示结构，表单状态写操作仍由调用方的 patchForm 完成。

import type { LucideIcon } from "lucide-react";
import { useId, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { cn } from "@/lib/utils";

import { HelpTip } from "./help-tip";

type FormSectionProps = {
  children: ReactNode;
  description: string;
  icon: LucideIcon;
  title: string;
};

export function FormSection({
  children,
  description,
  icon: Icon,
  title,
}: FormSectionProps) {
  const { t } = useTranslation("workspace");
  const titleId = useId();
  return (
    <section
      aria-labelledby={titleId}
      className="rounded-card border border-border bg-surface-softer p-3.5 sm:p-4"
    >
      {/* 标题行只留一行：图标 + 标题 + 说明入口。分区说明从常驻段落收进浮层，
          避免每个区块先来一句解释，把主字段挤到下面。 */}
      <div className="mb-3 flex items-center gap-2">
        <span className="grid size-6 shrink-0 place-items-center rounded-field border border-border bg-surface-solid text-accent-secondary">
          <Icon aria-hidden="true" size={13} />
        </span>
        <h3
          className="min-w-0 flex-1 text-sm font-semibold tracking-[-0.01em] text-foreground"
          id={titleId}
        >
          {title}
        </h3>
        <HelpTip
          label={t("panels.schedule.helpLabel", { field: title })}
          text={description}
        />
      </div>
      {children}
    </section>
  );
}

function FieldLabel({
  children,
  required = false,
}: {
  children: ReactNode;
  required?: boolean;
}) {
  return (
    <span className="flex items-center gap-1 text-xs font-medium text-foreground">
      {children}
      {required ? (
        <span aria-hidden="true" className="text-accent-orange">
          *
        </span>
      ) : null}
    </span>
  );
}

export function Field({
  action,
  children,
  className,
  error,
  errorId,
  help,
  helpLabel,
  label,
  required = false,
}: {
  /** 字段标题行右侧的动作入口，例如 Cron 可视化助手。 */
  action?: ReactNode;
  children: ReactNode;
  className?: string;
  error?: string;
  errorId?: string;
  /** 字段说明，展示在问号提示气泡中。 */
  help?: string;
  /** 提示按钮的无障碍名称。 */
  helpLabel?: string;
  /** 渲染在输入控件之外（仍在 label 外）的补充内容，例如 Cron 可视化区块。 */
  label: ReactNode;
  required?: boolean;
}) {
  const hasHelp = Boolean(help && helpLabel);

  return (
    <div className={cn("grid gap-1.5", className)}>
      <div className="flex items-start justify-between gap-2">
        <label className="grid min-w-0 flex-1 gap-1.5">
          <FieldLabel required={required}>{label}</FieldLabel>
          {children}
        </label>
        {action || hasHelp ? (
          <div className="mt-0.5 flex shrink-0 items-center gap-1">
            {action}
            {hasHelp ? (
              <HelpTip label={helpLabel as string} text={help as string} />
            ) : null}
          </div>
        ) : null}
      </div>
      {error ? (
        <span
          className="text-xs text-destructive"
          id={errorId}
          data-testid="schedule-field-error"
        >
          {error}
        </span>
      ) : null}
    </div>
  );
}