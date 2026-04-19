import {
  ArrowDownIcon,
  ArrowUpIcon,
  RefreshCcwIcon,
  Trash2Icon,
} from "lucide-react";
import { useMemo, useState } from "react";

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
        title={<span className="text-base">Retry 配置</span>}
        icon={
          <SettingsPanelIcon>
            <RefreshCcwIcon size={16} />
          </SettingsPanelIcon>
        }
        description="维护全局重试默认值、增强策略，以及按顺序匹配的 retry rules。"
        descriptionClassName="mt-1"
        headerAside={
          <SettingsBadgeList>
            <Badge>{config.enabled ? "重试开" : "重试关"}</Badge>
            <Badge>{`${enabledRuleCount}/${rules.length} 条规则启用`}</Badge>
            <Badge>
              {config.enhancedStrategyEnabled ? "增强策略开" : "增强策略关"}
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
              关闭后规则仍保留，但运行时不再执行自动重试。
            </div>
            <label className={`mt-3 ${editorToggleRowClassName}`}>
              <span>{config.enabled ? "已启用" : "已关闭"}</span>
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

          <SettingsSubsectionCard title="默认策略">
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

          <SettingsSubsectionCard title="摘要">
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
            description="控制原生 Responses 请求在状态失效时是否剥离客户端状态后重试一次。"
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
                ? "strip_client_state_once 已启用"
                : "strip_client_state_once 已关闭"}
            </SettingsNoticeCard>
          </SettingsSubsectionCard>

          <SettingsSubsectionCard
            title="enhanced_strategy"
            description="第几次重试开始做降级和候选剔除，由阈值与评分参数共同决定。"
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
        description="规则按顺序匹配，第一个命中的 rule 会生效，因此支持直接上移和下移。"
        items={rules}
        getRowKey={(rule) => rule.id}
        emptyState="当前没有 retry rule，可直接新建第一条。"
        summary={
          <>
            <ConfigDomainSummaryBadge>{`${rules.length} 条规则`}</ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>{`默认 ${config.defaultMaxRetries || "--"} 次`}</ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {config.enhancedStrategyEnabled ? "enhanced on" : "enhanced off"}
            </ConfigDomainSummaryBadge>
          </>
        }
        actions={
          <SettingsAddButton size="sm" label="新建规则" onClick={openCreateDialog} />
        }
        columns={[
          {
            header: "顺序 / 名称",
            cell: (rule) => (
              <div className="min-w-[14rem]">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge>{`#${rule.index + 1}`}</Badge>
                  <div className="font-semibold text-[var(--foreground)]">
                    {rule.name || "--"}
                  </div>
                  <Badge>{rule.enabled ? "启用" : "关闭"}</Badge>
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {rule.description || "未填写 description"}
                </div>
              </div>
            ),
          },
          {
            header: "匹配条件",
            cell: (rule) => (
              <div className="min-w-[15rem] text-xs leading-5 text-[var(--muted-foreground)]">
                {summarizeMatcher(rule).map((line) => (
                  <div key={line}>{line}</div>
                ))}
              </div>
            ),
          },
          {
            header: "重试策略",
            cell: (rule) => (
              <div className="min-w-[12rem]">
                <div>{`max ${rule.maxRetries || "--"} · delay ${rule.retryDelayMs || "--"}ms`}</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {`backoff ${rule.backoffMultiplier || "--"}`}
                  {rule.extraFieldCount > 0
                    ? ` · ${rule.extraFieldCount} 个扩展字段`
                    : ""}
                </div>
              </div>
            ),
          },
          {
            header: "操作",
            cell: (rule) => (
              <SettingsActionGroup>
                <SettingsActionButton
                  variant="ghost"
                  icon={<ArrowUpIcon size={14} />}
                  label="上移"
                  onClick={() => onMoveRule(rule.index, "up")}
                  disabled={rule.index === 0}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<ArrowDownIcon size={14} />}
                  label="下移"
                  onClick={() => onMoveRule(rule.index, "down")}
                  disabled={rule.index === rules.length - 1}
                />
                <SettingsActionButton
                  variant="secondary"
                  label="编辑"
                  onClick={() => openEditDialog(rule)}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<Trash2Icon size={14} />}
                  label="删除"
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
        title={editingIndex == null ? "新建 retry 规则" : `编辑规则 #${editingIndex + 1}`}
        description="维护规则名称、匹配条件和重试参数。未知字段会在保存时继续保留。"
        footer={
          <SettingsDialogFooter
            confirmLabel="保存草稿"
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
                <span>{draft.enabled ? "已启用" : "已关闭"}</span>
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
            <ConfigFormField label="error_code.codes" description="支持换行或逗号分隔。">
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
            <ConfigFormField label="keyword.values" description="支持换行或逗号分隔。">
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
            <ConfigFormField label="keyword.patterns" description="支持换行或逗号分隔。">
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
                <span>{draft.keywordCaseSensitive ? "区分大小写" : "忽略大小写"}</span>
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

function summarizeMatcher(rule: RuntimeRetryRuleSummary) {
  const lines = [
    rule.statusCodeRange ? `status ${rule.statusCodeRange}` : null,
    summarizeTextGroup("codes", rule.errorCodeCodesText),
    rule.errorCodePattern ? `pattern ${rule.errorCodePattern}` : null,
    summarizeTextGroup("keyword", rule.keywordValuesText),
    summarizeTextGroup("regex", rule.keywordPatternsText),
  ].filter((value): value is string => Boolean(value));

  return lines.length > 0 ? lines : ["未配置显式匹配条件"];
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
