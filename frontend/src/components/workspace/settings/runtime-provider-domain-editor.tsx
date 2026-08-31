import {
  BotIcon,
  CheckIcon,
  CopyIcon,
  PencilIcon,
  RefreshCwIcon,
  StarIcon,
  Trash2Icon,
} from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  buildProviderAccountConfigPatch,
  detectRuntimeSiteAccount,
  fetchRuntimeSiteAccount,
  formatProviderAccountCacheLine,
  formatSiteAccountBalanceLine,
  refreshRuntimeProviderAccount,
  RuntimeApiError,
} from "@/api/runtime";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";

import { ConfigDomainDialog } from "./config-domain-dialog";
import {
  ConfigDomainSummaryBadge,
  ConfigDomainTable,
} from "./config-domain-table";
import { ConfigFormField } from "./config-form-field";
import { editorControlClassName } from "./editor-control-class";
import {
  buildProviderCreateConfigSnippet,
  createDefaultProviderConfig,
  isConfigRecord,
  type RuntimeProviderSummary,
} from "./runtime-provider-config-utils";
import { type ProviderDraftInput } from "./runtime-provider-domain-form-utils";
import { readRuntimeProxyConfig } from "./runtime-proxy-domain-utils";

function providerSearchText(provider: RuntimeProviderSummary): string {
  return [
    provider.name,
    provider.baseUrl,
    provider.protocol,
    provider.defaultModel,
    provider.siteType,
    provider.siteTypeConfidence,
    provider.accountSummary,
    provider.accountAuthRef,
    provider.apiPath,
    provider.forwardUrl,
    provider.proxySummary,
    ...provider.supportTypes,
    ...provider.supportedModels,
  ].join(" ");
}
import {
  SettingsActionGroup,
  SettingsIconActionButton,
} from "./settings-action-group";
import { SettingsAddButton } from "./settings-add-button";
import { SettingsDialogFooter } from "./settings-dialog-footer";
import { SettingsNoticeCard } from "./settings-notice-card";

const KNOWN_PROVIDER_KEYS = new Set([
  "enabled",
  "protocol",
  "truncation_adapter",
  "base_url",
  "api_path",
  "forward_url",
  "api_key",
  "default_model",
  "supported_models",
  "support_types",
  "timeout",
  "headers",
  "model_mappings",
  "proxy",
  "site_type",
  "site_type_confidence",
  "site_type_detected_at",
  "site_type_scores",
  "account_auth_ref",
  "account",
]);

type AccountAction = "detect" | "fetch" | "refresh" | null;

const providerProtocolOptions = [
  { value: "openai", label: "openai" },
  { value: "openai_image", label: "openai_image" },
  { value: "anthropic", label: "anthropic" },
  { value: "gemini", label: "gemini" },
  { value: "codex", label: "codex" },
] as const;

type RuntimeProviderDomainEditorProps = {
  defaultProvider: string;
  onApplyProviderAccountFields?: (
    name: string,
    fields: Record<string, unknown>,
  ) => void;
  onDeleteProvider: (name: string) => void;
  onSaveProvider: (
    draft: ProviderDraftInput,
    previousName: string | null,
  ) => string | null;
  onSetDefaultProvider: (name: string) => void;
  providers: RuntimeProviderSummary[];
};

export function RuntimeProviderDomainEditor({
  defaultProvider,
  onApplyProviderAccountFields,
  onDeleteProvider,
  onSaveProvider,
  onSetDefaultProvider,
  providers,
}: RuntimeProviderDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [accountBusy, setAccountBusy] = useState<AccountAction>(null);
  const [accountNotice, setAccountNotice] = useState<string | null>(null);
  const [accountError, setAccountError] = useState<string | null>(null);
  const [rowBusyName, setRowBusyName] = useState<string | null>(null);
  const [rowNotice, setRowNotice] = useState<string | null>(null);
  const [editingProviderName, setEditingProviderName] = useState<string | null>(null);
  const [copiedProviderName, setCopiedProviderName] = useState<string | null>(null);
  const [draft, setDraft] = useState<ProviderDraftInput>(() =>
    createProviderDraftInput(null, ""),
  );

  const enabledCount = useMemo(
    () => providers.filter((provider) => provider.enabled).length,
    [providers],
  );
  const accountSummaryLine = useMemo(
    () =>
      formatProviderAccountCacheLine(draft.account) ||
      (draft.siteType
        ? `site_type=${draft.siteType}${
            draft.siteTypeConfidence ? ` (${draft.siteTypeConfidence})` : ""
          }`
        : ""),
    [draft.account, draft.siteType, draft.siteTypeConfidence],
  );

  function openCreateDialog() {
    setDialogError(null);
    setAccountNotice(null);
    setAccountError(null);
    setAccountBusy(null);
    setEditingProviderName(null);
    setDraft(createProviderDraftInput(null, defaultProvider));
    setDialogOpen(true);
  }

  function openEditDialog(provider: RuntimeProviderSummary) {
    setDialogError(null);
    setAccountNotice(null);
    setAccountError(null);
    setAccountBusy(null);
    setEditingProviderName(provider.name);
    setDraft(createProviderDraftInput(provider, defaultProvider));
    setDialogOpen(true);
  }

  function handleSave() {
    const error = onSaveProvider(draft, editingProviderName);
    if (error) {
      setDialogError(error);
      return;
    }
    setDialogOpen(false);
  }

  async function handleCopyProvider(provider: RuntimeProviderSummary) {
    try {
      await navigator.clipboard.writeText(buildProviderCreateConfigSnippet(provider));
      setCopiedProviderName(provider.name);
      window.setTimeout(() => {
        setCopiedProviderName((currentName) =>
          currentName === provider.name ? null : currentName,
        );
      }, 1500);
    } catch {
      setCopiedProviderName(null);
    }
  }

  async function handleDetectSiteType() {
    const baseUrl = draft.baseUrl.trim();
    if (!baseUrl) {
      setAccountError(t("editor.providers.account.detectRequiresBaseUrl"));
      return;
    }

    setAccountBusy("detect");
    setAccountError(null);
    setAccountNotice(null);
    try {
      const result = await detectRuntimeSiteAccount({ base_url: baseUrl });
      const detect = result.detect;
      setDraft((current) => ({
        ...current,
        siteType: detect?.site_type?.trim() || current.siteType,
        siteTypeConfidence: detect?.confidence?.trim() || current.siteTypeConfidence,
        siteTypeDetectedAt: detect?.detected_at?.trim() || current.siteTypeDetectedAt,
        siteTypeScores: detect?.score ?? current.siteTypeScores,
      }));
      const warnings =
        detect?.warnings && detect.warnings.length > 0
          ? t("editor.providers.account.warningsSuffix", {
              warnings: detect.warnings.join("; "),
            })
          : "";
      setAccountNotice(
        t("editor.providers.account.detectSuccess", {
          siteType: detect?.site_type || "unknown",
          confidence: detect?.confidence || "n/a",
          warnings,
        }),
      );
    } catch (error) {
      setAccountError(
        describeAccountError(error, t("editor.providers.account.detectFailed")),
      );
    } finally {
      setAccountBusy(null);
    }
  }

  async function handleFetchAccount() {
    const baseUrl = draft.baseUrl.trim();
    if (!baseUrl) {
      setAccountError(t("editor.providers.account.fetchRequiresBaseUrl"));
      return;
    }

    setAccountBusy("fetch");
    setAccountError(null);
    setAccountNotice(null);
    try {
      const result = await fetchRuntimeSiteAccount({
        base_url: baseUrl,
        site_type: draft.siteType.trim() || undefined,
        api_key: draft.apiKey.trim() || undefined,
        system_access_token: draft.systemAccessToken.trim() || undefined,
        subject_user_id: draft.subjectUserId.trim() || undefined,
      });
      if (result.detect?.site_type) {
        setDraft((current) => ({
          ...current,
          siteType: result.detect?.site_type?.trim() || current.siteType,
          siteTypeConfidence:
            result.detect?.confidence?.trim() || current.siteTypeConfidence,
          siteTypeDetectedAt:
            result.detect?.detected_at?.trim() || current.siteTypeDetectedAt,
          siteTypeScores: result.detect?.score ?? current.siteTypeScores,
        }));
      }
      const balanceLine =
        result.balance_line?.trim() ||
        formatSiteAccountBalanceLine(result.account_view) ||
        t("editor.providers.account.snapshotWithoutBalance");
      const warnings =
        result.warnings && result.warnings.length > 0
          ? t("editor.providers.account.warningsSuffix", {
              warnings: result.warnings.join("; "),
            })
          : "";
      setAccountNotice(`${balanceLine}${warnings}`);
    } catch (error) {
      setAccountError(
        describeAccountError(error, t("editor.providers.account.fetchFailed")),
      );
    } finally {
      setAccountBusy(null);
    }
  }

  async function handleRefreshProviderAccount(
    providerName: string,
    options?: { fromDialog?: boolean },
  ) {
    const name = providerName.trim();
    if (!name) {
      const message = t("editor.providers.account.refreshRequiresName");
      if (options?.fromDialog) {
        setAccountError(message);
      } else {
        setRowNotice(message);
      }
      return;
    }

    if (options?.fromDialog) {
      setAccountBusy("refresh");
      setAccountError(null);
      setAccountNotice(null);
    } else {
      setRowBusyName(name);
      setRowNotice(null);
    }

    try {
      const result = await refreshRuntimeProviderAccount(name, {
        site_type: options?.fromDialog ? draft.siteType.trim() || undefined : undefined,
        api_key: options?.fromDialog ? draft.apiKey.trim() || undefined : undefined,
        system_access_token: options?.fromDialog
          ? draft.systemAccessToken.trim() || undefined
          : undefined,
        subject_user_id: options?.fromDialog
          ? draft.subjectUserId.trim() || undefined
          : undefined,
        persist: true,
        save_account_auth: true,
      });
      const patch = buildProviderAccountConfigPatch(result);
      onApplyProviderAccountFields?.(name, patch);
      if (options?.fromDialog) {
        setDraft((current) => ({
          ...current,
          siteType: result.site_type || current.siteType,
          siteTypeConfidence:
            result.site_type_confidence || current.siteTypeConfidence,
          siteTypeDetectedAt:
            result.site_type_detected_at || current.siteTypeDetectedAt,
          siteTypeScores: result.site_type_scores ?? current.siteTypeScores,
          accountAuthRef: result.account_auth_ref || current.accountAuthRef,
          account: result.account_cache ?? current.account,
        }));
      }
      const balanceLine =
        result.balance_line?.trim() ||
        formatSiteAccountBalanceLine(result.account_view) ||
        formatProviderAccountCacheLine(result.account_cache) ||
        t("editor.providers.account.synced");
      const persisted = result.persisted
        ? t("editor.providers.account.persisted")
        : t("editor.providers.account.notPersisted");
      const warnings =
        result.warnings && result.warnings.length > 0
          ? t("editor.providers.account.warningsSuffix", {
              warnings: result.warnings.join("; "),
            })
          : "";
      const message = t("editor.providers.account.refreshMessage", {
        name,
        balanceLine,
        persisted,
        warnings,
      });
      if (options?.fromDialog) {
        setAccountNotice(message);
      } else {
        setRowNotice(message);
      }
    } catch (error) {
      const message = describeAccountError(
        error,
        t("editor.providers.account.refreshFailed", { name }),
      );
      if (options?.fromDialog) {
        setAccountError(message);
      } else {
        setRowNotice(message);
      }
    } finally {
      if (options?.fromDialog) {
        setAccountBusy(null);
      } else {
        setRowBusyName(null);
      }
    }
  }

  return (
    <>
      {rowNotice ? (
        <div className="mb-3">
          <SettingsNoticeCard tone="warning-soft">{rowNotice}</SettingsNoticeCard>
        </div>
      ) : null}
      <ConfigDomainTable
        title={t("editor.providers.title")}
        titleIcon={BotIcon}
        description={t("editor.providers.description")}
        items={providers}
        getRowKey={(provider) => provider.name}
        searchable
        getSearchText={providerSearchText}
        pageSize={10}
        emptyState={t("editor.providers.emptyState")}
        summary={
          <>
            <ConfigDomainSummaryBadge>
              {t("editor.providers.summary.total", { count: providers.length })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {t("editor.providers.summary.enabled", { count: enabledCount })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {defaultProvider
                ? t("editor.providers.summary.defaultNamed", { name: defaultProvider })
                : t("editor.providers.summary.noDefault")}
            </ConfigDomainSummaryBadge>
          </>
        }
        actions={
          <SettingsAddButton
            size="sm"
            label={t("editor.providers.create")}
            onClick={openCreateDialog}
          />
        }
        columns={[
          {
            header: t("editor.providers.columns.name"),
            cell: (provider) => (
              <div className="min-w-[11rem]">
                <div className="flex flex-wrap items-center gap-2">
                  <div className="font-semibold text-[var(--foreground)]">
                    {provider.name}
                  </div>
                  {provider.name === defaultProvider ? (
                    <Badge>{t("editor.providers.row.defaultBadge")}</Badge>
                  ) : null}
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {provider.baseUrl || t("editor.providers.row.noBaseUrl")}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.providers.columns.protocol"),
            cell: (provider) => (
              <div className="min-w-[7rem]">
                <div>{provider.protocol || "--"}</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {provider.supportTypes.join(", ") || t("editor.providers.row.noSupportTypes")}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.providers.columns.defaultModel"),
            cell: (provider) => (
              <div className="min-w-[10rem]">
                <div>{provider.defaultModel || "--"}</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {t("editor.providers.row.modelCount", {
                    count: provider.supportedModels.length,
                  })}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.providers.columns.siteBalance"),
            cell: (provider) => (
              <div className="min-w-[12rem]">
                <div className="flex flex-wrap gap-2">
                  {provider.siteType ? (
                    <Badge>
                      {provider.siteType}
                      {provider.siteTypeConfidence
                        ? ` · ${provider.siteTypeConfidence}`
                        : ""}
                    </Badge>
                  ) : (
                    <Badge>{t("editor.providers.row.siteUndetected")}</Badge>
                  )}
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {provider.accountSummary ||
                    (provider.accountAuthRef
                      ? t("editor.providers.row.authRef", {
                          ref: provider.accountAuthRef,
                        })
                      : t("editor.providers.row.noAccountCache"))}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.providers.columns.status"),
            cell: (provider) => (
              <div className="flex flex-wrap gap-2">
                <Badge>
                  {provider.enabled
                    ? t("editor.providers.row.enabled")
                    : t("editor.providers.row.disabled")}
                </Badge>
                {provider.hasProxyOverride ? (
                  <Badge>
                    {provider.proxyEnabled
                      ? t("editor.providers.row.proxyOverride")
                      : t("editor.providers.row.proxyConfigured")}
                  </Badge>
                ) : null}
                {provider.extraFieldCount > 0 ? (
                  <Badge>
                    {t("editor.providers.row.extraFields", {
                      count: provider.extraFieldCount,
                    })}
                  </Badge>
                ) : null}
              </div>
            ),
          },
          {
            header: t("editor.providers.columns.actions"),
            cell: (provider) => (
              <SettingsActionGroup compact>
                {provider.name !== defaultProvider ? (
                  <SettingsIconActionButton
                    label={t("editor.providers.actions.setDefault", {
                      name: provider.name,
                    })}
                    onClick={() => onSetDefaultProvider(provider.name)}
                  >
                    <StarIcon size={13} />
                  </SettingsIconActionButton>
                ) : null}
                <SettingsIconActionButton
                  label={t("editor.providers.actions.refreshBalance", {
                    name: provider.name,
                  })}
                  onClick={() => void handleRefreshProviderAccount(provider.name)}
                  disabled={rowBusyName === provider.name}
                >
                  <RefreshCwIcon
                    size={13}
                    className={
                      rowBusyName === provider.name ? "animate-spin" : undefined
                    }
                  />
                </SettingsIconActionButton>
                <SettingsIconActionButton
                  label={t("editor.providers.actions.copyConfig", {
                    name: provider.name,
                  })}
                  onClick={() => void handleCopyProvider(provider)}
                >
                  {copiedProviderName === provider.name ? (
                    <CheckIcon size={13} />
                  ) : (
                    <CopyIcon size={13} />
                  )}
                </SettingsIconActionButton>
                <SettingsIconActionButton
                  label={t("editor.providers.actions.edit", { name: provider.name })}
                  onClick={() => openEditDialog(provider)}
                >
                  <PencilIcon size={13} />
                </SettingsIconActionButton>
                <SettingsIconActionButton
                  label={t("editor.providers.actions.delete", { name: provider.name })}
                  onClick={() => onDeleteProvider(provider.name)}
                >
                  <Trash2Icon size={13} />
                </SettingsIconActionButton>
              </SettingsActionGroup>
            ),
            align: "right",
            className: "w-[11.5rem] min-w-[11.5rem]",
          },
        ]}
      />

      <ConfigDomainDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        title={
          editingProviderName
            ? t("editor.providers.editTitle", { name: editingProviderName })
            : t("editor.providers.createTitle")
        }
        description={t("editor.providers.dialogDescription")}
        footer={
          <SettingsDialogFooter
            buttonSize="sm"
            note={t("editor.providers.saveNote")}
            confirmLabel={t("editor.providers.saveButton")}
            onCancel={() => setDialogOpen(false)}
            onConfirm={handleSave}
          />
        }
      >
        <div className="space-y-3">
          {dialogError ? (
            <SettingsNoticeCard tone="warning-soft">
              {dialogError}
            </SettingsNoticeCard>
          ) : null}

          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField
              label={t("editor.providers.fields.name")}
              description={t("editor.providers.fields.nameDescription")}
            >
              <input
                className={editorControlClassName}
                value={draft.name}
                onChange={(event) => setDraft((current) => ({ ...current, name: event.target.value }))}
                placeholder={t("editor.providers.fields.namePlaceholder")}
              />
            </ConfigFormField>
            <ConfigFormField
              label={t("editor.providers.fields.protocol")}
              description={t("editor.providers.fields.protocolDescription")}
            >
              <Select
                ariaLabel={t("editor.providers.fields.protocolAria")}
                value={draft.protocol}
                onChange={(value) =>
                  setDraft((current) => ({ ...current, protocol: value }))
                }
                options={providerProtocolOptions}
                className="w-full"
                triggerClassName={editorControlClassName}
                optionClassName="text-sm"
              />
            </ConfigFormField>
            <ConfigFormField label="base_url">
              <input
                className={editorControlClassName}
                value={draft.baseUrl}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, baseUrl: event.target.value }))
                }
                placeholder="https://api.example.com"
              />
            </ConfigFormField>
            <ConfigFormField label="default_model">
              <input
                className={editorControlClassName}
                value={draft.defaultModel}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, defaultModel: event.target.value }))
                }
                placeholder="gpt-5.4"
              />
            </ConfigFormField>
            <ConfigFormField label="api_path">
              <input
                className={editorControlClassName}
                value={draft.apiPath}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, apiPath: event.target.value }))
                }
                placeholder="/v1/chat/completions"
              />
            </ConfigFormField>
            <ConfigFormField label="forward_url">
              <input
                className={editorControlClassName}
                value={draft.forwardUrl}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, forwardUrl: event.target.value }))
                }
                placeholder="/v1/chat/completions"
              />
            </ConfigFormField>
            <ConfigFormField label="timeout">
              <input
                className={editorControlClassName}
                value={draft.timeout}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, timeout: event.target.value }))
                }
                placeholder="300s"
              />
            </ConfigFormField>
            <ConfigFormField label="truncation_adapter">
              <input
                className={editorControlClassName}
                value={draft.truncationAdapter}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    truncationAdapter: event.target.value,
                  }))
                }
                placeholder="openai_local"
              />
            </ConfigFormField>
          </div>

          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField
              label="supported_models"
              description={t("editor.providers.fields.supportedModelsDescription")}
            >
              <textarea
                className={`${editorControlClassName} min-h-36 resize-y font-mono`}
                value={draft.supportedModelsText}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    supportedModelsText: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
            <ConfigFormField
              label="support_types"
              description={t("editor.providers.fields.supportTypesDescription")}
            >
              <textarea
                className={`${editorControlClassName} min-h-36 resize-y font-mono`}
                value={draft.supportTypesText}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    supportTypesText: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
          </div>

          <ConfigFormField
            label="api_key"
            description={t("editor.providers.fields.apiKeyDescription")}
          >
            <textarea
              className={`${editorControlClassName} min-h-28 resize-y font-mono`}
              value={draft.apiKey}
              onChange={(event) =>
                setDraft((current) => ({ ...current, apiKey: event.target.value }))
              }
            />
          </ConfigFormField>

          <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
            <div className="mb-3 flex flex-wrap items-start justify-between gap-3">
              <div className="min-w-0 flex-1">
                <div className="text-[13px] font-semibold text-[var(--foreground)]">
                  {t("editor.providers.account.title")}
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {t("editor.providers.account.description")}
                </div>
              </div>
              <div className="flex flex-wrap gap-2">
                {draft.siteType ? (
                  <Badge>
                    {draft.siteType}
                    {draft.siteTypeConfidence
                      ? ` · ${draft.siteTypeConfidence}`
                      : ""}
                  </Badge>
                ) : (
                  <Badge>{t("editor.providers.account.undetected")}</Badge>
                )}
                {draft.accountAuthRef ? (
                  <Badge>{t("editor.providers.account.authRefBadge")}</Badge>
                ) : null}
              </div>
            </div>

            <div className="grid gap-3 xl:grid-cols-2">
              <ConfigFormField
                label="site_type"
                description={t("editor.providers.account.siteTypeDescription")}
              >
                <input
                  className={editorControlClassName}
                  value={draft.siteType}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      siteType: event.target.value,
                    }))
                  }
                  placeholder="sub2api / newapi / unknown"
                />
              </ConfigFormField>
              <ConfigFormField
                label="account_auth_ref"
                description={t("editor.providers.account.accountAuthRefDescription")}
              >
                <input
                  className={editorControlClassName}
                  value={draft.accountAuthRef}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      accountAuthRef: event.target.value,
                    }))
                  }
                  placeholder="providers/<name>/account"
                />
              </ConfigFormField>
            </div>

            <div className="mt-3 grid gap-3 xl:grid-cols-2">
              <ConfigFormField label="site_type_confidence">
                <input
                  className={editorControlClassName}
                  value={draft.siteTypeConfidence}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      siteTypeConfidence: event.target.value,
                    }))
                  }
                  placeholder="high / medium / low"
                />
              </ConfigFormField>
              <ConfigFormField label="site_type_detected_at">
                <input
                  className={editorControlClassName}
                  value={draft.siteTypeDetectedAt}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      siteTypeDetectedAt: event.target.value,
                    }))
                  }
                  placeholder="ISO timestamp"
                />
              </ConfigFormField>
            </div>

            <div className="mt-3 grid gap-3 xl:grid-cols-2">
              <ConfigFormField
                label="system_access_token"
                description={t("editor.providers.account.systemAccessTokenDescription")}
              >
                <input
                  className={editorControlClassName}
                  type="password"
                  autoComplete="off"
                  value={draft.systemAccessToken}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      systemAccessToken: event.target.value,
                    }))
                  }
                  placeholder="NewAPI system access token"
                />
              </ConfigFormField>
              <ConfigFormField
                label="subject_user_id"
                description={t("editor.providers.account.subjectUserIdDescription")}
              >
                <input
                  className={editorControlClassName}
                  value={draft.subjectUserId}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      subjectUserId: event.target.value,
                    }))
                  }
                  placeholder={t("editor.providers.account.subjectUserIdPlaceholder")}
                />
              </ConfigFormField>
            </div>

            <div className="mt-3 flex flex-wrap gap-2">
              <Button
                size="sm"
                variant="secondary"
                disabled={accountBusy !== null}
                onClick={() => void handleDetectSiteType()}
              >
                {accountBusy === "detect"
                  ? t("editor.providers.account.detecting")
                  : t("editor.providers.account.detect")}
              </Button>
              <Button
                size="sm"
                variant="secondary"
                disabled={accountBusy !== null}
                onClick={() => void handleFetchAccount()}
              >
                {accountBusy === "fetch"
                  ? t("editor.providers.account.fetching")
                  : t("editor.providers.account.fetch")}
              </Button>
              <Button
                size="sm"
                variant="secondary"
                disabled={accountBusy !== null || !editingProviderName}
                onClick={() =>
                  void handleRefreshProviderAccount(
                    editingProviderName || draft.name,
                    { fromDialog: true },
                  )
                }
              >
                {accountBusy === "refresh"
                  ? t("editor.providers.account.refreshing")
                  : t("editor.providers.account.refresh")}
              </Button>
            </div>

            {accountSummaryLine ? (
              <SettingsNoticeCard tone="muted" className="mt-3">
                {accountSummaryLine}
              </SettingsNoticeCard>
            ) : null}
            {accountNotice ? (
              <SettingsNoticeCard tone="neutral" className="mt-3">
                {accountNotice}
              </SettingsNoticeCard>
            ) : null}
            {accountError ? (
              <SettingsNoticeCard tone="warning-soft" className="mt-3">
                {accountError}
              </SettingsNoticeCard>
            ) : null}
            {!editingProviderName ? (
              <SettingsNoticeCard tone="muted" className="mt-3">
                {t("editor.providers.account.syncRequiresName")}
              </SettingsNoticeCard>
            ) : null}
          </div>

          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField
              label="headers JSON"
              description={t("editor.providers.fields.headersDescription")}
            >
              <textarea
                className={`${editorControlClassName} min-h-40 resize-y font-mono`}
                value={draft.headersJson}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, headersJson: event.target.value }))
                }
              />
            </ConfigFormField>
            <ConfigFormField
              label="model_mappings JSON"
              description={t("editor.providers.fields.modelMappingsDescription")}
            >
              <textarea
                className={`${editorControlClassName} min-h-40 resize-y font-mono`}
                value={draft.modelMappingsJson}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    modelMappingsJson: event.target.value,
                  }))
                }
              />
            </ConfigFormField>
          </div>

          <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
            <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
              <div>
                <div className="text-[13px] font-semibold text-[var(--foreground)]">
                  {t("editor.providers.proxy.title")}
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {t("editor.providers.proxy.description")}
                </div>
              </div>
              <label className="flex items-center gap-2 text-sm text-[var(--foreground)]">
                <input
                  type="checkbox"
                  className="h-4 w-4 accent-[var(--accent-primary)]"
                  checked={draft.proxyEnabled}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      proxyEnabled: event.target.checked,
                    }))
                  }
                />
                {t("editor.providers.proxy.enable")}
              </label>
            </div>

            <div className="grid gap-3 xl:grid-cols-2">
              <ConfigFormField
                label="proxy.http"
                description={t("editor.providers.proxy.httpDescription")}
              >
                <input
                  className={editorControlClassName}
                  value={draft.proxyHttp}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      proxyHttp: event.target.value,
                    }))
                  }
                  placeholder={t("editor.providers.proxy.proxyPlaceholder")}
                />
              </ConfigFormField>
              <ConfigFormField
                label="proxy.https"
                description={t("editor.providers.proxy.httpsDescription")}
              >
                <input
                  className={editorControlClassName}
                  value={draft.proxyHttps}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      proxyHttps: event.target.value,
                    }))
                  }
                  placeholder={t("editor.providers.proxy.proxyPlaceholder")}
                />
              </ConfigFormField>
            </div>

            <div className="mt-3">
              <ConfigFormField
                label="proxy.no_proxy"
                description={t("editor.providers.proxy.noProxyDescription")}
              >
                <textarea
                  className={`${editorControlClassName} min-h-24 resize-y font-mono`}
                  value={draft.proxyNoProxy}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      proxyNoProxy: event.target.value,
                    }))
                  }
                  placeholder="localhost,127.0.0.1,.internal.example.com"
                />
              </ConfigFormField>
            </div>
          </div>

          <ConfigFormField
            label={t("editor.providers.fields.extraJson")}
            description={t("editor.providers.fields.extraJsonDescription")}
          >
            <textarea
              className={`${editorControlClassName} min-h-44 resize-y font-mono`}
              value={draft.extraJson}
              onChange={(event) =>
                setDraft((current) => ({ ...current, extraJson: event.target.value }))
              }
            />
          </ConfigFormField>

          <div className="flex flex-wrap gap-3 rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] px-3 py-2.5">
            <label className="flex items-center gap-2 text-sm text-[var(--foreground)]">
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={draft.enabled}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, enabled: event.target.checked }))
                }
              />
              {t("editor.providers.fields.enableProvider")}
            </label>
            <label className="flex items-center gap-2 text-sm text-[var(--foreground)]">
              <input
                type="checkbox"
                className="h-4 w-4 accent-[var(--accent-primary)]"
                checked={draft.setAsDefault}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    setAsDefault: event.target.checked,
                  }))
                }
              />
              {t("editor.providers.fields.setAsDefault")}
            </label>
          </div>
        </div>
      </ConfigDomainDialog>
    </>
  );
}

function createProviderDraftInput(
  provider: RuntimeProviderSummary | null,
  defaultProvider: string,
): ProviderDraftInput {
  if (!provider) {
    const defaults = createDefaultProviderConfig("new_provider", "openai");
    return {
      name: "",
      enabled: Boolean(defaults.enabled),
      protocol: typeof defaults.protocol === "string" ? defaults.protocol : "openai",
      baseUrl: typeof defaults.base_url === "string" ? defaults.base_url : "",
      apiPath: typeof defaults.api_path === "string" ? defaults.api_path : "",
      forwardUrl: typeof defaults.forward_url === "string" ? defaults.forward_url : "",
      apiKey: typeof defaults.api_key === "string" ? defaults.api_key : "",
      defaultModel:
        typeof defaults.default_model === "string" ? defaults.default_model : "",
      supportedModelsText: Array.isArray(defaults.supported_models)
        ? defaults.supported_models.join("\n")
        : "",
      supportTypesText: Array.isArray(defaults.support_types)
        ? defaults.support_types.join("\n")
        : "",
      timeout: typeof defaults.timeout === "string" ? defaults.timeout : "",
      truncationAdapter:
        typeof defaults.truncation_adapter === "string"
          ? defaults.truncation_adapter
          : "",
      proxyEnabled: false,
      proxyHttp: "",
      proxyHttps: "",
      proxyNoProxy: "",
      headersJson: JSON.stringify(defaults.headers ?? {}, null, 2),
      modelMappingsJson: JSON.stringify(defaults.model_mappings ?? {}, null, 2),
      extraJson: "{}",
      setAsDefault: defaultProvider === "",
      siteType: "",
      siteTypeConfidence: "",
      siteTypeDetectedAt: "",
      siteTypeScores: {},
      accountAuthRef: "",
      account: null,
      systemAccessToken: "",
      subjectUserId: "",
    };
  }

  const extraFields = Object.fromEntries(
    Object.entries(provider.raw).filter(([key]) => !KNOWN_PROVIDER_KEYS.has(key)),
  );
  const proxyConfig = readRuntimeProxyConfig(provider.raw.proxy);

  return {
    name: provider.name,
    enabled: provider.enabled,
    protocol: provider.protocol,
    baseUrl: provider.baseUrl,
    apiPath: provider.apiPath,
    forwardUrl: provider.forwardUrl,
    apiKey: provider.apiKey,
    defaultModel: provider.defaultModel,
    supportedModelsText: provider.supportedModels.join("\n"),
    supportTypesText: provider.supportTypes.join("\n"),
    timeout: provider.timeout,
    truncationAdapter: provider.truncationAdapter,
    proxyEnabled: proxyConfig.enabled,
    proxyHttp: proxyConfig.http,
    proxyHttps: proxyConfig.https,
    proxyNoProxy: proxyConfig.noProxy,
    headersJson: JSON.stringify(
      isConfigRecord(provider.raw.headers) ? provider.raw.headers : {},
      null,
      2,
    ),
    modelMappingsJson: JSON.stringify(
      isConfigRecord(provider.raw.model_mappings) ? provider.raw.model_mappings : {},
      null,
      2,
    ),
    extraJson: JSON.stringify(extraFields, null, 2),
    setAsDefault: provider.name === defaultProvider,
    siteType: provider.siteType,
    siteTypeConfidence: provider.siteTypeConfidence,
    siteTypeDetectedAt: provider.siteTypeDetectedAt,
    siteTypeScores: { ...provider.siteTypeScores },
    accountAuthRef: provider.accountAuthRef,
    account: provider.account,
    // Secrets stay out of provider config; only prefill non-secret subject id hint.
    systemAccessToken: "",
    subjectUserId: provider.account?.external_user_id?.trim() || "",
  };
}

function describeAccountError(error: unknown, fallback: string) {
  if (error instanceof RuntimeApiError) {
    return error.message || fallback;
  }
  if (error instanceof Error && error.message.trim()) {
    return error.message.trim();
  }
  return fallback;
}
