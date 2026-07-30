import { GaugeIcon, ShieldEllipsisIcon, Trash2Icon } from "lucide-react";
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
  createDefaultRateLimitApiKeyLimit,
  createDefaultRateLimitPathLimit,
  type RuntimeRateLimitApiKeyLimitSummary,
  type RuntimeRateLimitConfigSummary,
  type RuntimeRateLimitPathLimitSummary,
} from "./runtime-rate-limit-domain-utils";
import {
  type RateLimitApiKeyDraftInput,
  type RateLimitPathDraftInput,
} from "./runtime-rate-limit-domain-form-utils";
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
import { SettingsPanelIcon } from "./settings-panel-icon";

const KNOWN_API_KEY_LIMIT_KEYS = new Set([
  "api_key_pattern",
  "qps",
  "qpd",
  "qpm",
  "block_duration",
]);

const KNOWN_PATH_LIMIT_KEYS = new Set(["requests_per_minute", "burst"]);

type RuntimeRateLimitDomainEditorProps = {
  apiKeyLimits: RuntimeRateLimitApiKeyLimitSummary[];
  onChangeConfig: (next: RuntimeRateLimitConfigSummary) => void;
  onDeleteApiKeyLimit: (index: number) => void;
  onDeletePathLimit: (path: string) => void;
  onSaveApiKeyLimit: (
    draft: RateLimitApiKeyDraftInput,
    editingIndex: number | null,
  ) => string | null;
  onSavePathLimit: (
    draft: RateLimitPathDraftInput,
    previousPath: string | null,
  ) => string | null;
  pathLimits: RuntimeRateLimitPathLimitSummary[];
  rateLimitConfig: RuntimeRateLimitConfigSummary;
};

export function RuntimeRateLimitDomainEditor({
  apiKeyLimits,
  onChangeConfig,
  onDeleteApiKeyLimit,
  onDeletePathLimit,
  onSaveApiKeyLimit,
  onSavePathLimit,
  pathLimits,
  rateLimitConfig,
}: RuntimeRateLimitDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [apiDialogOpen, setApiDialogOpen] = useState(false);
  const [apiDialogError, setApiDialogError] = useState<string | null>(null);
  const [editingApiKeyIndex, setEditingApiKeyIndex] = useState<number | null>(null);
  const [apiDraft, setApiDraft] = useState<RateLimitApiKeyDraftInput>(() =>
    createApiKeyDraftInput(null),
  );

  const [pathDialogOpen, setPathDialogOpen] = useState(false);
  const [pathDialogError, setPathDialogError] = useState<string | null>(null);
  const [editingPath, setEditingPath] = useState<string | null>(null);
  const [pathDraft, setPathDraft] = useState<RateLimitPathDraftInput>(() =>
    createPathDraftInput(null),
  );

  const totalPathBurst = useMemo(
    () =>
      pathLimits.reduce((sum, item) => sum + (Number(item.burst) || 0), 0),
    [pathLimits],
  );

  function openCreateApiDialog() {
    setApiDialogError(null);
    setEditingApiKeyIndex(null);
    setApiDraft(createApiKeyDraftInput(null));
    setApiDialogOpen(true);
  }

  function openEditApiDialog(limit: RuntimeRateLimitApiKeyLimitSummary) {
    setApiDialogError(null);
    setEditingApiKeyIndex(limit.index);
    setApiDraft(createApiKeyDraftInput(limit));
    setApiDialogOpen(true);
  }

  function openCreatePathDialog() {
    setPathDialogError(null);
    setEditingPath(null);
    setPathDraft(createPathDraftInput(null));
    setPathDialogOpen(true);
  }

  function openEditPathDialog(limit: RuntimeRateLimitPathLimitSummary) {
    setPathDialogError(null);
    setEditingPath(limit.path);
    setPathDraft(createPathDraftInput(limit));
    setPathDialogOpen(true);
  }

  function handleSaveApiLimit() {
    const error = onSaveApiKeyLimit(apiDraft, editingApiKeyIndex);
    if (error) {
      setApiDialogError(error);
      return;
    }
    setApiDialogOpen(false);
  }

  function handleSavePathLimit() {
    const error = onSavePathLimit(pathDraft, editingPath);
    if (error) {
      setPathDialogError(error);
      return;
    }
    setPathDialogOpen(false);
  }

  return (
    <div className="space-y-3">
      <div className="rounded-[0.9rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-3">
            <SettingsPanelIcon>
              <GaugeIcon size={15} />
            </SettingsPanelIcon>
            <div>
              <div className="text-base font-semibold text-[var(--foreground)]">
                {t("editor.rateLimit.title")}
              </div>
              <div className="mt-1 text-sm text-[var(--muted-foreground)]">
                {t("editor.rateLimit.description")}
              </div>
            </div>
          </div>
          <SettingsBadgeList>
            <ConfigDomainSummaryBadge>
              {rateLimitConfig.enabled
                ? t("editor.rateLimit.status.enabled")
                : t("editor.rateLimit.status.disabled")}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {t("editor.rateLimit.summary.apiKeyRules", {
                count: apiKeyLimits.length,
              })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {t("editor.rateLimit.summary.pathRules", {
                count: pathLimits.length,
              })}
            </ConfigDomainSummaryBadge>
          </SettingsBadgeList>
        </div>

        <div className="mt-3 grid gap-3 xl:grid-cols-[12rem_minmax(0,1fr)_minmax(0,1fr)]">
          <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
            <div className="text-[13px] font-semibold text-[var(--foreground)]">rate_limit.enabled</div>
            <div className="mt-1 text-xs leading-5 text-[var(--muted-foreground)]">
              {t("editor.rateLimit.enabledHelp")}
            </div>
            <label className={`mt-3 ${editorToggleRowClassName}`}>
              <span>
                {rateLimitConfig.enabled
                  ? t("editor.rateLimit.toggle.enabled")
                  : t("editor.rateLimit.toggle.disabled")}
              </span>
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={rateLimitConfig.enabled}
                onChange={(event) =>
                  onChangeConfig({
                    ...rateLimitConfig,
                    enabled: event.target.checked,
                  })
                }
              />
            </label>
          </div>

          <div className="space-y-3 rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
            <div className="text-[13px] font-semibold text-[var(--foreground)]">
              {t("editor.rateLimit.basicConfig")}
            </div>
            <div className="grid gap-3 xl:grid-cols-2">
              <ConfigFormField label="storage">
                <input
                  className={editorControlClassName}
                  value={rateLimitConfig.storage}
                  onChange={(event) =>
                    onChangeConfig({
                      ...rateLimitConfig,
                      storage: event.target.value,
                    })
                  }
                  placeholder="memory"
                />
              </ConfigFormField>
              <ConfigFormField label="algorithm">
                <input
                  className={editorControlClassName}
                  value={rateLimitConfig.algorithm}
                  onChange={(event) =>
                    onChangeConfig({
                      ...rateLimitConfig,
                      algorithm: event.target.value,
                    })
                  }
                  placeholder="token_bucket"
                />
              </ConfigFormField>
            </div>
          </div>

          <div className="space-y-3 rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
            <div className="text-[13px] font-semibold text-[var(--foreground)]">
              {t("editor.rateLimit.summaryTitle")}
            </div>
            <SettingsBadgeList>
              <Badge>{`storage ${rateLimitConfig.storage || "--"}`}</Badge>
              <Badge>{`algorithm ${rateLimitConfig.algorithm || "--"}`}</Badge>
              <Badge>
                {t("editor.rateLimit.summary.burstTotal", {
                  count: totalPathBurst,
                })}
              </Badge>
            </SettingsBadgeList>
          </div>
        </div>

        <div className="mt-3 grid gap-3 xl:grid-cols-2">
          <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
            <div className="mb-3 text-[13px] font-semibold text-[var(--foreground)]">
              default_limits
            </div>
            <div className="grid gap-3 xl:grid-cols-2">
              <ConfigFormField label="qps">
                <input
                  className={editorControlClassName}
                  value={rateLimitConfig.defaultQps}
                  onChange={(event) =>
                    onChangeConfig({
                      ...rateLimitConfig,
                      defaultQps: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="tpm">
                <input
                  className={editorControlClassName}
                  value={rateLimitConfig.defaultTpm}
                  onChange={(event) =>
                    onChangeConfig({
                      ...rateLimitConfig,
                      defaultTpm: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="daily_tokens">
                <input
                  className={editorControlClassName}
                  value={rateLimitConfig.defaultDailyTokens}
                  onChange={(event) =>
                    onChangeConfig({
                      ...rateLimitConfig,
                      defaultDailyTokens: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="monthly_tokens">
                <input
                  className={editorControlClassName}
                  value={rateLimitConfig.defaultMonthlyTokens}
                  onChange={(event) =>
                    onChangeConfig({
                      ...rateLimitConfig,
                      defaultMonthlyTokens: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
            </div>
          </div>

          <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
            <div className="mb-3 text-[13px] font-semibold text-[var(--foreground)]">
              global_limits
            </div>
            <div className="grid gap-3 xl:grid-cols-2">
              <ConfigFormField label="global_qps">
                <input
                  className={editorControlClassName}
                  value={rateLimitConfig.globalQps}
                  onChange={(event) =>
                    onChangeConfig({
                      ...rateLimitConfig,
                      globalQps: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="global_tpm">
                <input
                  className={editorControlClassName}
                  value={rateLimitConfig.globalTpm}
                  onChange={(event) =>
                    onChangeConfig({
                      ...rateLimitConfig,
                      globalTpm: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="global_daily_tokens">
                <input
                  className={editorControlClassName}
                  value={rateLimitConfig.globalDailyTokens}
                  onChange={(event) =>
                    onChangeConfig({
                      ...rateLimitConfig,
                      globalDailyTokens: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
              <ConfigFormField label="global_monthly_tokens">
                <input
                  className={editorControlClassName}
                  value={rateLimitConfig.globalMonthlyTokens}
                  onChange={(event) =>
                    onChangeConfig({
                      ...rateLimitConfig,
                      globalMonthlyTokens: event.target.value,
                    })
                  }
                />
              </ConfigFormField>
            </div>
          </div>
        </div>
      </div>

      <ConfigDomainTable
        title="API Key Limits"
        titleIcon={ShieldEllipsisIcon}
        description={t("editor.rateLimit.apiKey.tableDescription")}
        items={apiKeyLimits}
        getRowKey={(item) => item.id}
        emptyState={t("editor.rateLimit.apiKey.empty")}
        summary={
          <>
            <ConfigDomainSummaryBadge>
              {t("editor.rateLimit.summary.rules", {
                count: apiKeyLimits.length,
              })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {apiKeyLimits.length > 0
                ? t("editor.rateLimit.apiKey.firstPattern", {
                    pattern: apiKeyLimits[0]?.apiKeyPattern,
                  })
                : t("editor.rateLimit.apiKey.notConfigured")}
            </ConfigDomainSummaryBadge>
          </>
        }
        actions={
          <SettingsAddButton
            size="sm"
            label={t("editor.rateLimit.apiKey.create")}
            onClick={openCreateApiDialog}
          />
        }
        columns={[
          {
            header: "Pattern",
            cell: (item) => (
              <div className="min-w-[12rem] font-semibold text-[var(--foreground)]">
                {item.apiKeyPattern}
              </div>
            ),
          },
          {
            header: t("editor.rateLimit.apiKey.columns.quota"),
            cell: (item) => (
              <div className="min-w-[12rem] text-sm">
                <div>{`qps ${item.qps || "--"} / qpm ${item.qpm || "--"}`}</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {`qpd ${item.qpd || "--"}`}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.rateLimit.apiKey.columns.block"),
            cell: (item) => (
              <div className="min-w-[10rem]">
                <div>{item.blockDuration || "--"}</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {item.extraFieldCount > 0
                    ? t("editor.rateLimit.extraFields.count", {
                        count: item.extraFieldCount,
                      })
                    : t("editor.rateLimit.extraFields.none")}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.rateLimit.columns.actions"),
            cell: (item) => (
              <SettingsActionGroup>
                <SettingsActionButton
                  variant="secondary"
                  label={t("editor.rateLimit.actions.edit")}
                  onClick={() => openEditApiDialog(item)}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<Trash2Icon size={14} />}
                  label={t("editor.rateLimit.actions.delete")}
                  onClick={() => onDeleteApiKeyLimit(item.index)}
                />
              </SettingsActionGroup>
            ),
            align: "right",
            className: "w-[12rem]",
          },
        ]}
      />

      <ConfigDomainTable
        title="Path Limits"
        titleIcon={GaugeIcon}
        description={t("editor.rateLimit.path.tableDescription")}
        items={pathLimits}
        getRowKey={(item) => item.path}
        emptyState={t("editor.rateLimit.path.empty")}
        summary={
          <>
            <ConfigDomainSummaryBadge>
              {t("editor.rateLimit.summary.pathRules", {
                count: pathLimits.length,
              })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {t("editor.rateLimit.summary.burstTotal", {
                count: totalPathBurst,
              })}
            </ConfigDomainSummaryBadge>
          </>
        }
        actions={
          <SettingsAddButton
            size="sm"
            label={t("editor.rateLimit.path.create")}
            onClick={openCreatePathDialog}
          />
        }
        columns={[
          {
            header: t("editor.rateLimit.path.columns.path"),
            cell: (item) => (
              <div className="min-w-[14rem] font-semibold text-[var(--foreground)]">
                {item.path}
              </div>
            ),
          },
          {
            header: "requests_per_minute",
            cell: (item) => <div>{item.requestsPerMinute || "--"}</div>,
          },
          {
            header: "burst",
            cell: (item) => (
              <div>
                <div>{item.burst || "--"}</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {item.extraFieldCount > 0
                    ? t("editor.rateLimit.extraFields.count", {
                        count: item.extraFieldCount,
                      })
                    : t("editor.rateLimit.extraFields.none")}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.rateLimit.columns.actions"),
            cell: (item) => (
              <SettingsActionGroup>
                <SettingsActionButton
                  variant="secondary"
                  label={t("editor.rateLimit.actions.edit")}
                  onClick={() => openEditPathDialog(item)}
                />
                <SettingsActionButton
                  variant="ghost"
                  icon={<Trash2Icon size={14} />}
                  label={t("editor.rateLimit.actions.delete")}
                  onClick={() => onDeletePathLimit(item.path)}
                />
              </SettingsActionGroup>
            ),
            align: "right",
            className: "w-[12rem]",
          },
        ]}
      />

      <ConfigDomainDialog
        open={apiDialogOpen}
        onClose={() => setApiDialogOpen(false)}
        title={
          editingApiKeyIndex == null
            ? t("editor.rateLimit.apiKey.dialog.createTitle")
            : t("editor.rateLimit.apiKey.dialog.editTitle", {
                index: editingApiKeyIndex + 1,
              })
        }
        description={t("editor.rateLimit.apiKey.dialog.description")}
        footer={
          <SettingsDialogFooter
            buttonSize="sm"
            confirmLabel={t("editor.rateLimit.actions.saveRule")}
            onCancel={() => setApiDialogOpen(false)}
            onConfirm={handleSaveApiLimit}
          />
        }
      >
        <div className="space-y-3">
          {apiDialogError ? (
            <SettingsNoticeCard tone="warning-soft">
              {apiDialogError}
            </SettingsNoticeCard>
          ) : null}

          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField label="api_key_pattern">
              <input
                className={editorControlClassName}
                value={apiDraft.apiKeyPattern}
                onChange={(event) =>
                  setApiDraft((current) => ({
                    ...current,
                    apiKeyPattern: event.target.value,
                  }))
                }
                placeholder="sk-"
              />
            </ConfigFormField>
            <ConfigFormField label="block_duration">
              <input
                className={editorControlClassName}
                value={apiDraft.blockDuration}
                onChange={(event) =>
                  setApiDraft((current) => ({
                    ...current,
                    blockDuration: event.target.value,
                  }))
                }
                placeholder="60s"
              />
            </ConfigFormField>
            <ConfigFormField label="qps">
              <input
                className={editorControlClassName}
                value={apiDraft.qps}
                onChange={(event) =>
                  setApiDraft((current) => ({ ...current, qps: event.target.value }))
                }
              />
            </ConfigFormField>
            <ConfigFormField label="qpm">
              <input
                className={editorControlClassName}
                value={apiDraft.qpm}
                onChange={(event) =>
                  setApiDraft((current) => ({ ...current, qpm: event.target.value }))
                }
              />
            </ConfigFormField>
            <ConfigFormField label="qpd">
              <input
                className={editorControlClassName}
                value={apiDraft.qpd}
                onChange={(event) =>
                  setApiDraft((current) => ({ ...current, qpd: event.target.value }))
                }
              />
            </ConfigFormField>
          </div>

          <ConfigFormField label={t("editor.rateLimit.fields.extraJson")}>
            <textarea
              className={`${editorControlClassName} min-h-40 resize-y font-mono`}
              value={apiDraft.extraJson}
              onChange={(event) =>
                setApiDraft((current) => ({ ...current, extraJson: event.target.value }))
              }
            />
          </ConfigFormField>
        </div>
      </ConfigDomainDialog>

      <ConfigDomainDialog
        open={pathDialogOpen}
        onClose={() => setPathDialogOpen(false)}
        title={
          editingPath == null
            ? t("editor.rateLimit.path.dialog.createTitle")
            : t("editor.rateLimit.path.dialog.editTitle", {
                path: editingPath,
              })
        }
        description={t("editor.rateLimit.path.dialog.description")}
        footer={
          <SettingsDialogFooter
            buttonSize="sm"
            confirmLabel={t("editor.rateLimit.actions.saveRule")}
            onCancel={() => setPathDialogOpen(false)}
            onConfirm={handleSavePathLimit}
          />
        }
      >
        <div className="space-y-3">
          {pathDialogError ? (
            <SettingsNoticeCard tone="warning-soft">
              {pathDialogError}
            </SettingsNoticeCard>
          ) : null}

          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField label="path">
              <input
                className={editorControlClassName}
                value={pathDraft.path}
                onChange={(event) =>
                  setPathDraft((current) => ({ ...current, path: event.target.value }))
                }
                placeholder="/v1/responses"
              />
            </ConfigFormField>
            <ConfigFormField label="requests_per_minute">
              <input
                className={editorControlClassName}
                value={pathDraft.requestsPerMinute}
                onChange={(event) =>
                  setPathDraft((current) => ({
                    ...current,
                    requestsPerMinute: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
            <ConfigFormField label="burst">
              <input
                className={editorControlClassName}
                value={pathDraft.burst}
                onChange={(event) =>
                  setPathDraft((current) => ({ ...current, burst: event.target.value }))
                }
              />
            </ConfigFormField>
          </div>

          <ConfigFormField label={t("editor.rateLimit.fields.extraJson")}>
            <textarea
              className={`${editorControlClassName} min-h-40 resize-y font-mono`}
              value={pathDraft.extraJson}
              onChange={(event) =>
                setPathDraft((current) => ({ ...current, extraJson: event.target.value }))
              }
            />
          </ConfigFormField>
        </div>
      </ConfigDomainDialog>
    </div>
  );
}

function createApiKeyDraftInput(
  limit: RuntimeRateLimitApiKeyLimitSummary | null,
): RateLimitApiKeyDraftInput {
  if (!limit) {
    const defaults = createDefaultRateLimitApiKeyLimit();
    return {
      apiKeyPattern:
        typeof defaults.api_key_pattern === "string"
          ? defaults.api_key_pattern
          : "",
      qps: stringifyEditableValue(defaults.qps),
      qpd: stringifyEditableValue(defaults.qpd),
      qpm: stringifyEditableValue(defaults.qpm),
      blockDuration:
        typeof defaults.block_duration === "string" ? defaults.block_duration : "",
      extraJson: "{}",
    };
  }

  const extraFields = Object.fromEntries(
    Object.entries(limit.raw).filter(([key]) => !KNOWN_API_KEY_LIMIT_KEYS.has(key)),
  );

  return {
    apiKeyPattern: limit.apiKeyPattern,
    qps: limit.qps,
    qpd: limit.qpd,
    qpm: limit.qpm,
    blockDuration: limit.blockDuration,
    extraJson: JSON.stringify(extraFields, null, 2),
  };
}

function createPathDraftInput(
  limit: RuntimeRateLimitPathLimitSummary | null,
): RateLimitPathDraftInput {
  if (!limit) {
    const defaults = createDefaultRateLimitPathLimit();
    return {
      path: "",
      requestsPerMinute: stringifyEditableValue(defaults.requests_per_minute),
      burst: stringifyEditableValue(defaults.burst),
      extraJson: "{}",
    };
  }

  const extraFields = Object.fromEntries(
    Object.entries(limit.raw).filter(([key]) => !KNOWN_PATH_LIMIT_KEYS.has(key)),
  );

  return {
    path: limit.path,
    requestsPerMinute: limit.requestsPerMinute,
    burst: limit.burst,
    extraJson: JSON.stringify(extraFields, null, 2),
  };
}

function stringifyEditableValue(value: unknown) {
  if (typeof value === "number") {
    return String(value);
  }
  return typeof value === "string" ? value : "";
}
