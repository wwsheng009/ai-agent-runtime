// 批次 18（P2-2 子片 1）：草稿校验面板——把写入前的字段级问题摆到编辑区上方。

import { AlertTriangleIcon, CircleAlertIcon } from "lucide-react";

import { cn } from "@/lib/utils";

import {
  countConfigIssues,
  formatConfigIssuePath,
} from "../../runtime-config-validation";
import { type Translator } from "../types";
import { type ConfigEditorCore } from "../use-config-core";

export function ConfigDraftValidationPanel({ core }: { core: ConfigEditorCore }) {
  const { draftIssues, t } = core;
  // 问题文案的 key 由校验模块按 messageKey 动态拼出，这里沿用编辑器内部松类型约定。
  const translate = t as unknown as Translator;

  if (draftIssues.length === 0) {
    return null;
  }

  const errorCount = countConfigIssues(draftIssues, "error");
  const warningCount = countConfigIssues(draftIssues, "warning");
  const hasErrors = errorCount > 0;

  return (
    <div
      className={cn(
        "rounded-panel border px-3 py-2.5",
        hasErrors
          ? "border-analytics-danger-border bg-analytics-danger-soft"
          : "border-analytics-warning-border bg-analytics-warning-soft",
      )}
      data-testid="config-draft-validation"
    >
      <div
        className="flex flex-wrap items-center gap-2"
        role={hasErrors ? "alert" : "status"}
      >
        {hasErrors ? (
          <CircleAlertIcon size={14} className="text-analytics-danger" />
        ) : (
          <AlertTriangleIcon size={14} className="text-analytics-warning" />
        )}
        <span
          className={cn(
            "text-sm font-medium",
            hasErrors ? "text-analytics-danger" : "text-analytics-warning",
          )}
        >
          {t("editor.draftValidation.title")}
        </span>
        {errorCount > 0 ? (
          <span className="text-xs text-analytics-danger">
            {t("editor.draftValidation.errorBadge", { count: errorCount })}
          </span>
        ) : null}
        {warningCount > 0 ? (
          <span className="text-xs text-analytics-warning">
            {t("editor.draftValidation.warningBadge", { count: warningCount })}
          </span>
        ) : null}
      </div>

      <div className="mt-1 text-xs text-muted-foreground">
        {t("editor.draftValidation.description")}
      </div>

      <ul className="mt-2 space-y-1">
        {draftIssues.map((issue) => (
          <li
            className="flex flex-wrap items-baseline gap-2 text-xs"
            data-testid="config-draft-validation-row"
            key={`${issue.severity}:${issue.messageKey}:${formatConfigIssuePath(issue.path)}`}
          >
            <span
              className={cn(
                "rounded px-1.5 py-0.5 font-mono",
                issue.severity === "error"
                  ? "bg-analytics-danger-soft text-analytics-danger"
                  : "bg-analytics-warning-soft text-analytics-warning",
              )}
            >
              {formatConfigIssuePath(issue.path)}
            </span>
            <span className="text-foreground">
              {translate(`editor.draftValidation.${issue.messageKey}`, issue.params)}
            </span>
            <span className="text-muted-foreground">
              {t(
                issue.severity === "error"
                  ? "editor.draftValidation.severityError"
                  : "editor.draftValidation.severityWarning",
              )}
            </span>
          </li>
        ))}
      </ul>

      {hasErrors ? (
        <div className="mt-2 text-xs text-analytics-danger">
          {t("editor.draftValidation.blockedHint")}
        </div>
      ) : null}
    </div>
  );
}
