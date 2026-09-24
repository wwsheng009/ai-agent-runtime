// 第 7、9 张卡片：配置覆盖 / 校验与影响。
//
// 覆盖卡：后端不提供「白名单数组」，只有逐键 allowed（overrideKeyAllowed 用 D14 的
// 同一校验器判定，profiles_view_groups.go:242）——因此这里只展示既有键的判定结果，
// 新增键交给 /validate 兜底，前端不维护会漂移的本地白名单。
//
// 校验卡：本地草稿问题（实时）、后端 /validate 报告、/preview 生效视图三者并列；
// 「变更路径」复用编辑器算出的 diff（与后端 issues.path 同名），不假装 /preview 会返回 diff。

import { PlusIcon, ShieldCheckIcon, Trash2Icon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

import { editorControlClassName } from "../../editor-control-class";
import { SettingsEmptyState } from "../../settings-empty-state";
import { SettingsIconActionButton } from "../../settings-action-group";
import { SummaryPill } from "../primitives";
import type { ProfileCardContext, ProfileValidationState } from "./profile-editor-context";
import { ProfileCardHeader, ProfileField } from "./profile-form-fields";
import { formatProfileIssue } from "./profile-i18n";

export function ProfileOverridesCard({ ctx }: { ctx: ProfileCardContext }) {
  const { t } = useTranslation("runtimeConfig");
  const { addOverride, disabled, draft, removeOverride, updateOverride, view } = ctx;
  const allowedByKey = new Map(
    view.overrides.entries.map((entry) => [entry.key, entry.allowed]),
  );
  const allowedKeys = view.overrides.entries
    .filter((entry) => entry.allowed)
    .map((entry) => entry.key);

  return (
    <div className="grid gap-3">
      <ProfileCardHeader
        description={t("profiles.overrides.description")}
        icon={<ShieldCheckIcon size={13} />}
        title={t("profiles.overrides.title")}
      />
      <div className="text-xs leading-5 text-muted-foreground">
        {allowedKeys.length > 0
          ? t("profiles.overrides.allowedKeysHint", { keys: allowedKeys.join(", ") })
          : t("profiles.overrides.allowedKeysUnknown")}
      </div>
      {draft.overrides.length === 0 ? (
        <SettingsEmptyState variant="dashed">{t("profiles.overrides.empty")}</SettingsEmptyState>
      ) : (
        <div className="grid gap-2">
          {draft.overrides.map((override, index) => {
            const trimmedKey = override.key.trim();
            const allowed = allowedByKey.get(trimmedKey);
            return (
              <div
                key={`${trimmedKey}-${index}`}
                className="grid gap-2 rounded-card border border-border bg-surface-softer p-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]"
                data-profile-override={trimmedKey || index}
              >
                <input
                  aria-label={t("profiles.overrides.key")}
                  className={editorControlClassName}
                  disabled={disabled}
                  placeholder={t("profiles.overrides.keyHint")}
                  type="text"
                  value={override.key}
                  onChange={(event) => {
                    updateOverride(index, { key: event.target.value });
                  }}
                />
                <input
                  aria-label={t("profiles.overrides.value")}
                  className={editorControlClassName}
                  disabled={disabled}
                  placeholder={t("profiles.overrides.valueHint")}
                  type="text"
                  value={override.value}
                  onChange={(event) => {
                    updateOverride(index, { value: event.target.value });
                  }}
                />
                <div className="flex items-center justify-between gap-2">
                  <Badge
                    className={cn(
                      "normal-case",
                      allowed === false ? "text-accent-orange" : undefined,
                    )}
                  >
                    {allowed === false
                      ? t("profiles.overrides.notAllowed")
                      : t("profiles.overrides.allowed")}
                  </Badge>
                  <SettingsIconActionButton
                    disabled={disabled}
                    label={t("profiles.overrides.remove")}
                    type="button"
                    onClick={() => {
                      removeOverride(index);
                    }}
                  >
                    <Trash2Icon size={13} />
                  </SettingsIconActionButton>
                </div>
              </div>
            );
          })}
        </div>
      )}
      <div>
        <Button
          disabled={disabled}
          size="sm"
          type="button"
          variant="secondary"
          onClick={addOverride}
        >
          <PlusIcon size={14} />
          {t("profiles.overrides.add")}
        </Button>
      </div>
    </div>
  );
}

export function ProfileValidationCard({
  ctx,
  onPreview,
  onValidate,
  state,
}: {
  ctx: ProfileCardContext;
  onPreview: () => void;
  onValidate: () => void;
  state: ProfileValidationState;
}) {
  const { t } = useTranslation("runtimeConfig");
  const { changes, disabled, issues, view } = ctx;
  const report = state.report;
  const preview = state.preview;
  const reportErrors = report?.issues.filter((issue) => issue.severity === "error") ?? [];
  const reportWarnings = report?.issues.filter((issue) => issue.severity === "warning") ?? [];
  // 估算优先用 preview（反映当前草稿），未预览时回落到已落盘的 view。
  const estimate = preview?.estimate ?? view.estimate;

  return (
    <div className="grid gap-3">
      <ProfileCardHeader
        description={t("profiles.validation.description")}
        icon={<ShieldCheckIcon size={13} />}
        title={t("profiles.validation.title")}
      />
      <div className="flex flex-wrap items-center gap-2">
        <Button
          disabled={disabled || state.isValidating}
          size="sm"
          type="button"
          variant="secondary"
          onClick={onValidate}
        >
          {state.isValidating ? t("profiles.validation.validating") : t("profiles.validation.validate")}
        </Button>
        <Button
          disabled={disabled || state.isPreviewing}
          size="sm"
          type="button"
          variant="secondary"
          onClick={onPreview}
        >
          {state.isPreviewing ? t("profiles.validation.previewing") : t("profiles.validation.preview")}
        </Button>
        <div className="text-xs leading-5 text-muted-foreground">
          {state.lastValidatedAt
            ? t("profiles.validation.lastValidated", { time: state.lastValidatedAt })
            : t("profiles.validation.noValidationYet")}
        </div>
      </div>
      <div className="text-xs leading-5 text-muted-foreground">
        {t("profiles.validation.validateHint")} {t("profiles.validation.previewHint")}
      </div>

      <ProfileField label={t("profiles.editor.issuesTitle")}>
        <div className="grid gap-1.5" data-profile-issues={issues.length}>
          {issues.length === 0 ? (
            <div className="text-xs leading-5 text-muted-foreground">
              {t("profiles.validation.ok")}
            </div>
          ) : (
            issues.map((issue) => (
              <div
                key={`${issue.code}-${issue.path}`}
                className="flex flex-wrap items-center gap-2 text-xs leading-5"
                role="alert"
              >
                <Badge className="normal-case text-accent-orange">
                  {t("profiles.validation.errors")}
                </Badge>
                <span className="font-mono text-muted-foreground">{issue.path}</span>
                <span className="text-foreground">{formatProfileIssue(t, issue)}</span>
              </div>
            ))
          )}
        </div>
        <div className="mt-2 text-xs leading-5 text-muted-foreground">
          {t("profiles.editor.issueHint")}
        </div>
      </ProfileField>

      {report ? (
        <ProfileField
          label={t("profiles.editor.issueCount", {
            errorCount: String(reportErrors.length),
            warningCount: String(reportWarnings.length),
          })}
        >
          <div className="grid gap-1.5" data-profile-report={report.valid ? "valid" : "invalid"}>
            {reportErrors.length === 0 && reportWarnings.length === 0 ? (
              <div className="text-xs leading-5 text-muted-foreground">
                {t("profiles.validation.ok")}
              </div>
            ) : null}
            {reportErrors.map((issue) => (
              <div
                key={`error-${issue.path}`}
                className="flex flex-wrap items-center gap-2 text-xs leading-5"
              >
                <Badge className="normal-case text-accent-orange">
                  {t("profiles.validation.errors")}
                </Badge>
                <span className="font-mono text-muted-foreground">{issue.path}</span>
                <span className="text-foreground">{issue.message}</span>
              </div>
            ))}
            {reportWarnings.map((issue) => (
              <div
                key={`warning-${issue.path}`}
                className="flex flex-wrap items-center gap-2 text-xs leading-5"
              >
                <Badge className="normal-case">{t("profiles.validation.warnings")}</Badge>
                <span className="font-mono text-muted-foreground">{issue.path}</span>
                <span className="text-foreground">{issue.message}</span>
              </div>
            ))}
          </div>
        </ProfileField>
      ) : null}

      <ProfileField
        description={t("profiles.editor.changeCount", { count: changes.length })}
        label={t("profiles.validation.changedPaths")}
      >
        {changes.length > 0 ? (
          <div className="flex flex-wrap gap-1.5" data-profile-changes={changes.length}>
            {changes.map((path) => (
              <Badge key={path} className="normal-case">
                {path}
              </Badge>
            ))}
          </div>
        ) : (
          <div className="text-xs leading-5 text-muted-foreground">
            {t("profiles.validation.changedPathsEmpty")}
          </div>
        )}
      </ProfileField>

      <ProfileField
        description={t("profiles.validation.previewHint")}
        label={t("profiles.validation.preview")}
      >
        {preview ? (
          <div className="flex flex-wrap items-center gap-2">
            <Badge
              className={cn(
                "normal-case",
                preview.valid ? "text-accent-primary" : "text-accent-orange",
              )}
            >
              {preview.valid ? t("profiles.validation.ok") : t("profiles.validation.errors")}
            </Badge>
            <span className="text-xs leading-5 text-muted-foreground">
              {t("profiles.editor.issueCount", {
                errorCount: String(preview.errorCount),
                warningCount: String(preview.warningCount),
              })}
            </span>
          </div>
        ) : (
          <div className="text-xs leading-5 text-muted-foreground">
            {t("profiles.validation.empty")}
          </div>
        )}
      </ProfileField>

      <ProfileField
        description={t("profiles.validation.estimateHint")}
        label={t("profiles.validation.estimate")}
      >
        <div className="flex flex-wrap gap-2">
          <SummaryPill
            label={t("profiles.validation.estimateToolCount")}
            value={t("profiles.list.toolCount", { count: estimate.toolCount })}
          />
          <SummaryPill
            label={t("profiles.validation.estimateToolTokens")}
            value={t("profiles.list.totalTokens", { count: estimate.toolTokens })}
          />
          <SummaryPill
            label={t("profiles.validation.estimatePromptTokens")}
            value={t("profiles.list.totalTokens", { count: estimate.promptTokens })}
          />
          <SummaryPill
            label={t("profiles.validation.estimateTotalTokens")}
            value={t("profiles.list.totalTokens", { count: estimate.totalTokens })}
          />
        </div>
      </ProfileField>
    </div>
  );
}
