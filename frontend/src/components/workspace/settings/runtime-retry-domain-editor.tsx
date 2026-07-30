import {
  ArrowDownIcon,
  ArrowUpIcon,
  RefreshCcwIcon,
  Trash2Icon,
} from "lucide-react";
import { type TFunction } from "i18next";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";

import { ConfigDomainDialog } from "./config-domain-dialog";
import {
  ConfigDomainSummaryBadge,
  ConfigDomainTable,
} from "./config-domain-table";
import { ConfigFormField } from "./config-form-field";
import {
  editorControlClassName,
  editorToggleRowClassName,
} from "./editor-control-class";
import {
  SettingsActionButton,
  SettingsActionGroup,
} from "./settings-action-group";
import { SettingsAddButton } from "./settings-add-button";
import { SettingsBadgeList } from "./settings-badge-list";
import { SettingsDialogFooter } from "./settings-dialog-footer";
import { SettingsNoticeCard } from "./settings-notice-card";
import { SettingsPanelCard } from "./settings-panel-card";
import { SettingsPanelIcon } from "./settings-panel-icon";
import { SettingsSubsectionCard } from "./settings-subsection-card";
import {
  type RuntimeRetryConfigSummary,
  type RuntimeRetryRuleSummary,
} from "./runtime-retry-domain-utils";

export type RetryRuleDraftInput = {
  backoffMultiplier: string;
  description: string;
  enabled: boolean;
  errorCodeCodesText: string;
  errorCodePattern: string;
  keywordCaseSensitive: boolean;
  keywordPatternsText: string;
  keywordValuesText: string;
  maxRetries: string;
  name: string;
  retryDelayMs: string;
  statusCodeRange: string;
};

type RuntimeRetryDomainEditorProps = {
  config: RuntimeRetryConfigSummary;
  onChangeConfig: (next: RuntimeRetryConfigSummary) => void;
  onDeleteRule: (index: number) => void;
  onMoveRule: (index: number, direction: "up" | "down") => void;
  onSaveRule: (
    draft: RetryRuleDraftInput,
    editingIndex: number | null,
  ) => string | null;
  rules: RuntimeRetryRuleSummary[];
};

export function RuntimeRetryDomainEditor({
  config,
  onChangeConfig,
  onDeleteRule,
  onMoveRule,
  onSaveRule,
  rules,
}: RuntimeRetryDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [editingIndex, setEditingIndex] = useState<number | null>(null);
  const [draft, setDraft] = useState<RetryRuleDraftInput>(() =>
    createRetryRuleDraft(null),
  );

  const enabledRuleCount = useMemo(
    () => rules.filter((rule) => rule.enabled).length,
    [rules],
  );

  function openCreateDialog() {
    setDialogError(null);
    setEditingIndex(null);
    setDraft(createRetryRuleDraft(null));
    setDialogOpen(true);
  }

  function openEditDialog(rule: RuntimeRetryRuleSummary) {
    setDialogError(null);
    setEditingIndex(rule.index);
    setDraft(createRetryRuleDraft(rule));
    setDialogOpen(true);
  }

  function handleSave() {
    const error = onSaveRule(draft, editingIndex);
    if (error) {
      setDialogError(error);
      return;
    }
    setDialogOpen(false);
  }

  return (
    <div className="space-y-3">
      <SettingsPanelCard
        title={<span className="text-base">{t("editor.retry.title")}</span>}
        icon={
          <SettingsPanelIcon>
            <RefreshCcwIcon size={16} />
          </SettingsPanelIcon>
        }
        description={t("editor.retry.description")}
        descriptionClassName="mt-1"
        headerAside={
          <SettingsBadgeList>
            <Badge>
              {config.enabled
                ? t("editor.retry.status.retryOn")
                : t("editor.retry.status.retryOff")}
            </Badge>
            <Badge>
              {t("editor.retry.summary.enabledRules", {
                enabled: String(enabledRuleCount),
                total: String(rules.length),
              })}
            </Badge>
            <Badge>
              {config.enhancedStrategyEnabled
                ? t("editor.retry.status.enhancedOn")
                : t("editor.retry.status.enhancedOff")}
            </Badge>
          </SettingsBadgeList>
        }
      >
        <div className="grid gap-3 xl:grid-cols-[11rem_minmax(0,1fr)_minmax(0,1fr)]">
          <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
            <div className="text-sm font-semibold text-[var(--foreground)]">
              retry.enabled
            </div>
            <div className="mt-1 text-xs leading-5 text-[var(--muted-foreground)]">
              {t("editor.retry.enabledHelp")}
            </div>
            <label className={`mt-3 ${editorToggleRowClassName}`}>
              <span>
                {config.enabled
                  ? t("editor.retry.toggle.enabled")
                  : t("editor.retry.toggle.disabled")}
              </span>
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={config.enabled}
                onChange={(event) =>
                  onChangeConfig({ ...config, enabled: event.target.checked })
                }
              />
            </label>
          </div>

          <SettingsSubsectionCard title={t("editor.retry.defaultStrategy")}>
            <div className="grid gap-3 xl:grid-cols-3">
              <ConfigFormField label="default_max_retries">
                <input
                  className={editorControlClassName}
                  value={config.defaultMaxRetries}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      defaultMaxRetries: event.target.value,
                    })
                  }
                  placeholder="3"
                />
              </ConfigFormField>
              <ConfigFormField label="default_retry_delay_ms">
                <input
                  className={editorControlClassName}
                  value={config.defaultRetryDelayMs}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      defaultRetryDelayMs: event.target.value,
                    })
                  }
                  placeholder="1000"
                />
              </ConfigFormField>
              <ConfigFormField label="default_backoff_multiplier">
                <input
                  className={editorControlClassName}
                  value={config.defaultBackoffMultiplier}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      defaultBackoffMultiplier: event.target.value,
                    })
                  }
                  placeholder="2.0"
                />
              </ConfigFormField>
            </div>
          </SettingsSubsectionCard>

          <SettingsSubsectionCard title={t("editor.retry.summaryTitle")}>
            <SettingsBadgeList>
              <Badge>{`default ${config.defaultMaxRetries || "--"}`}</Badge>
              <Badge>{`delay ${config.defaultRetryDelayMs || "--"}ms`}</Badge>
              <Badge>{`backoff ${config.defaultBackoffMultiplier || "--"}`}</Badge>
            </SettingsBadgeList>
          </SettingsSubsectionCard>
        </div>

        <div className="mt-3 grid gap-3 xl:grid-cols-2">
          <SettingsSubsectionCard
            title="invalid_encrypted_content_recovery"
            description={t("editor.retry.invalidEncrypted.description")}
            headerAside={
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={config.invalidEncryptedContentStripClientStateOnce}
                onChange={(event) =>
                  onChangeConfig({
                    ...config,
                    invalidEncryptedContentStripClientStateOnce:
                      event.target.checked,
                  })
                }
              />
            }
          >
            <SettingsNoticeCard>
              {config.invalidEncryptedContentStripClientStateOnce
                ? t("editor.retry.invalidEncrypted.enabled")
                : t("editor.retry.invalidEncrypted.disabled")}
            </SettingsNoticeCard>
          </SettingsSubsectionCard>

          <SettingsSubsectionCard
            title="enhanced_strategy"
            description={t("editor.retry.enhancedStrategy.description")}
            headerAside={
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={config.enhancedStrategyEnabled}
                onChange={(event) =>
                  onChangeConfig({
                    ...config,
                    enhancedStrategyEnabled: event.target.checked,
                  })
                }
              />
            }
          >
            <div className="grid gap-3 xl:grid-cols-2">
              <ConfigFormField label="secondary_threshold">
                <input
                  className={editorControlClassName}
                  value={config.enhancedStrategySecondaryThreshold}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      enhancedStrategySecondaryThreshold: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="fallback_threshold">
                <input
                  className={editorControlClassName}
                  value={config.enhancedStrategyFallbackThreshold}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      enhancedStrategyFallbackThreshold: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="primary_min_score">
                <input
                  className={editorControlClassName}
                  value={config.enhancedStrategyPrimaryMinScore}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      enhancedStrategyPrimaryMinScore: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="secondary_excluded_score">
                <input
                  className={editorControlClassName}
                  value={config.enhancedStrategySecondaryExcludedScore}
                  onChange={(event) =>
                    onChangeConfig({
                      ...config,
                      enhancedStrategySecondaryExcludedScore: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
            </div>
          </SettingsSubsectionCard>
        </div>
      </SettingsPanelCard>

      <ConfigDomainTable
        title="Retry Rules"
        titleIcon={RefreshCcwIcon}
        description={t("editor.retry.rules.tableDescription")}
        items={rules}
        getRowKey={(rule) => rule.id}
        emptyState={t("editor.retry.rules.empty")}
        summary={
          <>
            <ConfigDomainSummaryBadge>
              {t("editor.retry.summary.rules", { count: rules.length })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {t("editor.retry.summary.defaultRetries", {
                value: config.defaultMaxRetries || "--",
              })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {config.enhancedStrategyEnabled ? "enhanced on" : "enhanced off"}
            </ConfigDomainSummaryBadge>
          </>
        }
        actions={
          <SettingsAddButton
            size="sm"
            label={t("editor.retry.rules.create")}
            onClick={openCreateDialog}
          />
        }
        columns={[
          {
            header: t("editor.retry.rules.columns.orderName"),
            cell: (rule) => (
              <div className="min-w-[14rem]">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge>{`#${rule.index + 1}`}</Badge>
                  <div className="font-semibold text-[var(--foreground)]">
                    {rule.name || "--"}
                  </div>
                  <Badge>
                    {rule.enabled
                      ? t("editor.retry.rules.enabled")
                      : t("editor.retry.rules.disabled")}
                  </Badge>
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {rule.description || t("editor.retry.rules.noDescription")}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.retry.rules.columns.matchers"),
            cell: (rule) => (
              <div className="min-w-[15rem] text-xs leading-5 text-[var(--muted-foreground)]">
                {summarizeMatcher(rule, t).map((line) => (
                  <div key={line}>{line}</div>
                ))}
              </div>
            ),
          },
          {
            header: t("editor.retry.rules.columns.strategy"),
            cell: (rule) => (
              <div className="min-w-[12rem]">
                <div>{`max ${rule.maxRetries || "--"} · delay ${rule.retryDelayMs || "--"}ms`}</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {`backoff ${rule.backoffMultiplier || "--"}`}
                  {rule.extraFieldCount > 0
                    ? ` · ${t("editor.retry.extraFields.count", {
                        count: rule.extraFieldCount,
                      })}`
                    : ""}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.retry.columns.actions"),
            cell: (rule) => (
              <SettingsActionGroup>
                <SettingsActionButton
                  variant="ghost"
                  icon={<ArrowUpIcon size={14} />}
                  label={t("editor.retry.actions.moveUp")}
                  onClick={() => onMoveRule(rule.index, "up")}
                  disabled={rule.index === 0}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<ArrowDownIcon size={14} />}
                  label={t("editor.retry.actions.moveDown")}
                  onClick={() => onMoveRule(rule.index, "down")}
                  disabled={rule.index === rules.length - 1}
                />
                <SettingsActionButton
                  variant="secondary"
                  label={t("editor.retry.actions.edit")}
                  onClick={() => openEditDialog(rule)}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<Trash2Icon size={14} />}
                  label={t("editor.retry.actions.delete")}
                  onClick={() => onDeleteRule(rule.index)}
                />
              </SettingsActionGroup>
            ),
            align: "right",
            className: "w-[22rem]",
          },
        ]}
      />

      <ConfigDomainDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        title={
          editingIndex == null
            ? t("editor.retry.dialog.createTitle")
            : t("editor.retry.dialog.editTitle", {
                index: String(editingIndex + 1),
              })
        }
        description={t("editor.retry.dialog.description")}
        footer={
          <SettingsDialogFooter
            confirmLabel={t("editor.retry.actions.saveDraft")}
            onCancel={() => setDialogOpen(false)}
            onConfirm={handleSave}
          />
        }
      >
        <div className="space-y-3">
          {dialogError ? (
            <SettingsNoticeCard tone="warning">
              {dialogError}
            </SettingsNoticeCard>
          ) : null}
          <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_11rem]">
            <ConfigFormField label="name">
              <input
                className={editorControlClassName}
                value={draft.name}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, name: event.target.value }))
                }
                placeholder="rate_limit_retry"
              />
            </ConfigFormField>
            <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
              <div className="text-sm font-semibold text-[var(--foreground)]">enabled</div>
              <label className={`mt-3 ${editorToggleRowClassName}`}>
                <span>
                  {draft.enabled
                    ? t("editor.retry.toggle.enabled")
                    : t("editor.retry.toggle.disabled")}
                </span>
                <input
                  type="checkbox"
                  className="h-4 w-4 accent-[var(--accent-primary)]"
                  checked={draft.enabled}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      enabled: event.target.checked,
                    }))
                  }
                />
              </label>
            </div>
          </div>

          <ConfigFormField label="description">
            <textarea
              className={`${editorControlClassName} min-h-24 resize-y`}
              value={draft.description}
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  description: event.target.value,
                }))
              }
            />
          </ConfigFormField>

          <div className="grid gap-3 md:grid-cols-3">
            <ConfigFormField label="max_retries">
              <input
                className={editorControlClassName}
                value={draft.maxRetries}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    maxRetries: event.target.value,
                  }))
                }
                placeholder="3"
              />
            </ConfigFormField>
            <ConfigFormField label="retry_delay_ms">
              <input
                className={editorControlClassName}
                value={draft.retryDelayMs}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    retryDelayMs: event.target.value,
                  }))
                }
                placeholder="1000"
              />
            </ConfigFormField>
            <ConfigFormField label="backoff_multiplier">
              <input
                className={editorControlClassName}
                value={draft.backoffMultiplier}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    backoffMultiplier: event.target.value,
                  }))
                }
                placeholder="2.0"
              />
            </ConfigFormField>
          </div>

          <div className="grid gap-3 md:grid-cols-2">
            <ConfigFormField label="status_code.range">
              <input
                className={editorControlClassName}
                value={draft.statusCodeRange}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    statusCodeRange: event.target.value,
                  }))
                }
                placeholder="500-504"
              />
            </ConfigFormField>
            <ConfigFormField label="error_code.pattern">
              <input
                className={editorControlClassName}
                value={draft.errorCodePattern}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    errorCodePattern: event.target.value,
                  }))
                }
                placeholder="^13.*"
              />
            </ConfigFormField>
          </div>

          <div className="grid gap-3 md:grid-cols-2">
            <ConfigFormField
              label="error_code.codes"
              description={t("editor.retry.fields.listHint")}
            >
              <textarea
                className={`${editorControlClassName} min-h-28 resize-y`}
                value={draft.errorCodeCodesText}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    errorCodeCodesText: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
            <ConfigFormField
              label="keyword.values"
              description={t("editor.retry.fields.listHint")}
            >
              <textarea
                className={`${editorControlClassName} min-h-28 resize-y`}
                value={draft.keywordValuesText}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    keywordValuesText: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
          </div>

          <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_11rem]">
            <ConfigFormField
              label="keyword.patterns"
              description={t("editor.retry.fields.listHint")}
            >
              <textarea
                className={`${editorControlClassName} min-h-28 resize-y font-mono`}
                value={draft.keywordPatternsText}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    keywordPatternsText: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
            <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
              <div className="text-sm font-semibold text-[var(--foreground)]">
                keyword.case_sensitive
              </div>
              <label className={`mt-3 ${editorToggleRowClassName}`}>
                <span>
                  {draft.keywordCaseSensitive
                    ? t("editor.retry.caseSensitive.on")
                    : t("editor.retry.caseSensitive.off")}
                </span>
                <input
                  type="checkbox"
                  className="h-4 w-4 accent-[var(--accent-primary)]"
                  checked={draft.keywordCaseSensitive}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      keywordCaseSensitive: event.target.checked,
                    }))
                  }
                />
              </label>
            </div>
          </div>
        </div>
      </ConfigDomainDialog>
    </div>
  );
}

function createRetryRuleDraft(
  rule: RuntimeRetryRuleSummary | null,
): RetryRuleDraftInput {
  if (!rule) {
    return {
      name: "",
      description: "",
      enabled: true,
      maxRetries: "3",
      retryDelayMs: "1000",
      backoffMultiplier: "2.0",
      errorCodeCodesText: "",
      errorCodePattern: "",
      keywordValuesText: "",
      keywordPatternsText: "",
      keywordCaseSensitive: false,
      statusCodeRange: "",
    };
  }

  return {
    name: rule.name,
    description: rule.description,
    enabled: rule.enabled,
    maxRetries: rule.maxRetries,
    retryDelayMs: rule.retryDelayMs,
    backoffMultiplier: rule.backoffMultiplier,
    errorCodeCodesText: rule.errorCodeCodesText,
    errorCodePattern: rule.errorCodePattern,
    keywordValuesText: rule.keywordValuesText,
    keywordPatternsText: rule.keywordPatternsText,
    keywordCaseSensitive: rule.keywordCaseSensitive,
    statusCodeRange: rule.statusCodeRange,
  };
}

function summarizeMatcher(
  rule: RuntimeRetryRuleSummary,
  t: TFunction<"runtimeConfig">,
) {
  const lines = [
    rule.statusCodeRange ? `status ${rule.statusCodeRange}` : null,
    summarizeTextGroup("codes", rule.errorCodeCodesText),
    rule.errorCodePattern ? `pattern ${rule.errorCodePattern}` : null,
    summarizeTextGroup("keyword", rule.keywordValuesText),
    summarizeTextGroup("regex", rule.keywordPatternsText),
  ].filter((value): value is string => Boolean(value));

  return lines.length > 0
    ? lines
    : [t("editor.retry.rules.noExplicitMatchers")];
}

function summarizeTextGroup(prefix: string, value: string) {
  const items = value
    .split(/\r?\n/)
    .map((item) => item.trim())
    .filter(Boolean);
  if (items.length === 0) {
    return null;
  }
  return items.length === 1 ? `${prefix} ${items[0]}` : `${prefix} ${items[0]} +${items.length - 1}`;
}
