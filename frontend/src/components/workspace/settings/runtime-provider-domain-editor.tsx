import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  appendProviderModels,
  autoImportRuntimeProvider,
  buildProviderAccountConfigPatch,
  detectRuntimeSiteAccount,
  fetchRuntimeProviderModels,
  fetchRuntimeSiteAccount,
  formatProviderAccountCacheLine,
  formatSiteAccountBalanceLine,
  probeRuntimeProviderModels,
  refreshRuntimeProviderAccount,
} from "@/api/runtime";
import type { ProviderProbeResult } from "@/types/runtime";

import {
  buildProviderCreateConfigSnippet,
  type RuntimeProviderSummary,
} from "./runtime-provider-config-utils";
import {
  createProviderDraftInput,
  describeAccountError,
  type AccountAction,
} from "./runtime-provider-domain-editor/draft-utils";
import { ProviderDialog } from "./runtime-provider-domain-editor/provider-dialog";
import { type ProviderModelsAction } from "./runtime-provider-domain-editor/provider-models-section";
import { ProviderTable } from "./runtime-provider-domain-editor/provider-table";
import { type ProviderDraftInput } from "./runtime-provider-domain-form-utils";
import {
  buildProviderOpsRequestFromDraft,
  canResolveProviderOpsTarget,
  joinProviderOpsWarnings,
  normalizeProviderModelIDs,
  parseSupportedModelsText,
  providerAutoImportPatch,
  providerModelsPatch,
  resolveProbeModels,
  summarizeProbeResults,
} from "./provider-ops-utils";
import { SettingsNoticeCard } from "./settings-notice-card";

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
  const [modelsBusy, setModelsBusy] = useState<ProviderModelsAction>(null);
  const [modelsNotice, setModelsNotice] = useState<string | null>(null);
  const [modelsError, setModelsError] = useState<string | null>(null);
  const [assumedModelIDs, setAssumedModelIDs] = useState<string[]>([]);
  const [probeResult, setProbeResult] = useState<ProviderProbeResult | null>(null);

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
    setModelsNotice(null);
    setModelsError(null);
    setModelsBusy(null);
    setAssumedModelIDs([]);
    setProbeResult(null);
    setEditingProviderName(null);
    setDraft(createProviderDraftInput(null, defaultProvider));
    setDialogOpen(true);
  }

  function openEditDialog(provider: RuntimeProviderSummary) {
    setDialogError(null);
    setAccountNotice(null);
    setAccountError(null);
    setAccountBusy(null);
    setModelsNotice(null);
    setModelsError(null);
    setModelsBusy(null);
    setAssumedModelIDs([]);
    setProbeResult(null);
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

  function describeModelsError(error: unknown, fallback: string) {
    return describeAccountError(error, fallback);
  }

  function modelsWarningsSuffix(warnings: string[] | undefined) {
    const joined = joinProviderOpsWarnings(warnings);
    return joined ? t("editor.providers.models.warningsSuffix", { warnings: joined }) : "";
  }

  async function handleFetchModels() {
    if (!canResolveProviderOpsTarget({ baseUrl: draft.baseUrl, providerName: editingProviderName })) {
      setModelsError(t("editor.providers.models.fetchRequiresBaseUrl"));
      return;
    }

    setModelsBusy("fetch");
    setModelsError(null);
    setModelsNotice(null);
    try {
      const result = await fetchRuntimeProviderModels(
        buildProviderOpsRequestFromDraft(draft, editingProviderName),
      );
      const assumed = normalizeProviderModelIDs(result.assumed_model_ids);
      const fetched = normalizeProviderModelIDs(result.model_ids);
      setAssumedModelIDs(assumed);
      setProbeResult(null);
      const patch = providerModelsPatch(result);
      if (patch) {
        setDraft((current) => ({ ...current, ...patch }));
      }
      const warnings = modelsWarningsSuffix(result.warnings);
      if (fetched.length > 0) {
        setModelsNotice(
          t("editor.providers.models.fetchSuccess", {
            count: fetched.length,
            endpoint: result.endpoint || "models",
            warnings,
          }),
        );
      } else {
        setModelsNotice(
          `${t("editor.providers.models.fetchEmpty")}${warnings}`.trim(),
        );
      }
    } catch (error) {
      setModelsError(
        describeModelsError(error, t("editor.providers.models.fetchFailed")),
      );
    } finally {
      setModelsBusy(null);
    }
  }

  async function handleAutoImport() {
    if (!canResolveProviderOpsTarget({ baseUrl: draft.baseUrl, providerName: editingProviderName })) {
      setModelsError(t("editor.providers.models.autoImportRequiresBaseUrl"));
      return;
    }

    setModelsBusy("auto-import");
    setModelsError(null);
    setModelsNotice(null);
    try {
      const result = await autoImportRuntimeProvider({
        ...buildProviderOpsRequestFromDraft(draft, editingProviderName),
        default_model: draft.defaultModel.trim() || undefined,
      });
      setDraft((current) => ({ ...current, ...providerAutoImportPatch(result) }));
      const imported = normalizeProviderModelIDs(result.supported_models);
      setAssumedModelIDs(imported);
      setProbeResult(null);
      setModelsNotice(
        t("editor.providers.models.autoImportSuccess", {
          warnings: modelsWarningsSuffix(result.warnings),
        }),
      );
    } catch (error) {
      setModelsError(
        describeModelsError(error, t("editor.providers.models.autoImportFailed")),
      );
    } finally {
      setModelsBusy(null);
    }
  }

  function handleMergeModels(modelIDs: string[]) {
    const current = parseSupportedModelsText(draft.supportedModelsText);
    const merged = appendProviderModels(current, modelIDs);
    if (merged.length === current.length) {
      return;
    }
    setDraft((currentDraft) => ({
      ...currentDraft,
      supportedModelsText: merged.join("\n"),
    }));
    setModelsError(null);
    setModelsNotice(
      t("editor.providers.models.mergeAssumedNotice", {
        count: merged.length - current.length,
      }),
    );
  }

  async function handleProbeModels() {
    const models = resolveProbeModels(assumedModelIDs, draft);
    if (models.length === 0) {
      setModelsError(t("editor.providers.models.probeRequiresModels"));
      return;
    }
    setModelsBusy("probe");
    setModelsError(null);
    setModelsNotice(null);
    try {
      const result = await probeRuntimeProviderModels({
        ...buildProviderOpsRequestFromDraft(draft, editingProviderName),
        models,
      });
      setProbeResult(result);
      const summary = summarizeProbeResults(result);
      setModelsNotice(
        t("editor.providers.models.probeSuccess", {
          total: String(summary.total),
          ok: String(summary.ok),
        }),
      );
    } catch (error) {
      setProbeResult(null);
      setModelsError(
        describeModelsError(error, t("editor.providers.models.probeFailed")),
      );
    } finally {
      setModelsBusy(null);
    }
  }

  return (
    <>
      {rowNotice ? (
        <div className="mb-3">
          <SettingsNoticeCard tone="warning-soft">{rowNotice}</SettingsNoticeCard>
        </div>
      ) : null}
      <ProviderTable
        copiedProviderName={copiedProviderName}
        defaultProvider={defaultProvider}
        enabledCount={enabledCount}
        onCopyProvider={(provider) => void handleCopyProvider(provider)}
        onCreateProvider={openCreateDialog}
        onDeleteProvider={onDeleteProvider}
        onEditProvider={openEditDialog}
        onRefreshProviderAccount={(name) => void handleRefreshProviderAccount(name)}
        onSetDefaultProvider={onSetDefaultProvider}
        providers={providers}
        rowBusyName={rowBusyName}
      />

      <ProviderDialog
        accountBusy={accountBusy}
        accountError={accountError}
        accountNotice={accountNotice}
        accountSummaryLine={accountSummaryLine}
        assumedModelIDs={assumedModelIDs}
        dialogError={dialogError}
        draft={draft}
        editingProviderName={editingProviderName}
        modelsBusy={modelsBusy}
        modelsError={modelsError}
        modelsNotice={modelsNotice}
        onClose={() => setDialogOpen(false)}
        onConfirm={handleSave}
        onDetectSiteType={() => void handleDetectSiteType()}
        onFetchAccount={() => void handleFetchAccount()}
        onAutoImport={() => void handleAutoImport()}
        onFetchModels={() => void handleFetchModels()}
        onMergeModels={handleMergeModels}
        onProbeModels={() => void handleProbeModels()}
        onRefreshProviderAccount={(name, options) =>
          void handleRefreshProviderAccount(name, options)
        }
        open={dialogOpen}
        probeResult={probeResult}
        setDraft={setDraft}
      />
    </>
  );
}