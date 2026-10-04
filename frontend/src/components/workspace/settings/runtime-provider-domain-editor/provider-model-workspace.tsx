/**
 * Provider 页面「模型配置」Tab 的工作区（参照 aicli micro web client 的模型编辑器）：
 * 选择一个已保存 provider，逐模型编辑能力，再「应用到配置草稿」。
 *
 * 工作副本语义：面板内的编辑先落在本地工作副本，点「应用」后才写入页面草稿
 * （draftParsed）；页面级保存仍由既有保存链路统一处理。这样与同一页面上的
 * 「同步账户」动作保持一致，也避免每次按键都重建整棵配置树。
 *
 * 切换 provider 或点「重新载入」通过 key 重挂载内部编辑器，工作副本总是从
 * 当前 provider 快照重新初始化——不存在跨 provider 的陈旧编辑串写。
 */

import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { fetchRuntimeProviderModels } from "@/api/runtime";

import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";

import { editorControlClassName } from "../editor-control-class";
import { joinProviderOpsWarnings } from "../provider-ops-utils";
import { type RuntimeProviderSummary } from "../runtime-provider-config-utils";
import { SettingsNoticeCard } from "../settings-notice-card";

import {
  buildProviderModelCapabilitiesRecord,
  formatProviderModelIDsText,
  mergeProviderModelDraftsWithMetadata,
  normalizeProviderModelIDs,
  type ProviderModelDraft,
  providerModelDraftsFromRecord,
} from "./model-capability-draft";
import { ProviderModelEditor } from "./provider-model-editor";

type ProviderModelWorkspaceProps = {
  defaultProvider: string;
  onApplyProviderModels: (
    name: string,
    fields: {
      model_capabilities: Record<string, unknown>;
      supported_models: string[];
    },
  ) => void;
  providers: RuntimeProviderSummary[];
};

export function ProviderModelWorkspace({
  defaultProvider,
  onApplyProviderModels,
  providers,
}: ProviderModelWorkspaceProps) {
  const { t } = useTranslation("runtimeConfig");
  const names = useMemo(
    () => providers.map((provider) => provider.name),
    [providers],
  );
  const [selectedName, setSelectedName] = useState(() => {
    if (defaultProvider && names.includes(defaultProvider)) {
      return defaultProvider;
    }
    return names[0] ?? "";
  });
  const [revision, setRevision] = useState(0);
  const provider = providers.find((entry) => entry.name === selectedName) ?? null;

  if (names.length === 0) {
    return (
      <div className="rounded-card border border-border bg-surface-softer p-4 text-sm text-muted-foreground">
        {t("editor.providers.models.workspace.noProviders")}
      </div>
    );
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-end gap-2">
        <label className="min-w-56 flex-1">
          <span className="mb-1 block text-xs text-muted-foreground">
            {t("editor.providers.models.workspace.selectLabel")}
          </span>
          <Select
            ariaLabel={t("editor.providers.models.workspace.selectAria")}
            value={selectedName}
            onChange={setSelectedName}
            options={providers.map((entry) => ({
              value: entry.name,
              label: entry.name,
            }))}
            className="w-full"
            triggerClassName={editorControlClassName}
            optionClassName="text-sm"
          />
        </label>
        <Button
          size="sm"
          variant="secondary"
          disabled={!provider}
          onClick={() => setRevision((current) => current + 1)}
        >
          {t("editor.providers.models.workspace.reload")}
        </Button>
      </div>

      <p className="text-xs text-muted-foreground">
        {t("editor.providers.models.workspace.applyHint")}
      </p>

      {provider ? (
        <ProviderModelWorkspaceEditor
          key={`${provider.name}:${revision}`}
          provider={provider}
          onApplyProviderModels={onApplyProviderModels}
        />
      ) : (
        <div className="rounded-card border border-border bg-surface-softer p-4 text-sm text-muted-foreground">
          {t("editor.providers.models.workspace.noProviderSelected")}
        </div>
      )}
    </div>
  );
}

type ProviderModelWorkspaceEditorProps = {
  onApplyProviderModels: ProviderModelWorkspaceProps["onApplyProviderModels"];
  provider: RuntimeProviderSummary;
};

function ProviderModelWorkspaceEditor({
  onApplyProviderModels,
  provider,
}: ProviderModelWorkspaceEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [models, setModels] = useState(() =>
    normalizeProviderModelIDs([...provider.supportedModels, provider.defaultModel]),
  );
  const [drafts, setDrafts] = useState<Record<string, ProviderModelDraft>>(() =>
    providerModelDraftsFromRecord(provider.raw.model_capabilities),
  );
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function handleFetchModels() {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const result = await fetchRuntimeProviderModels({ name: provider.name });
      const fetched = normalizeProviderModelIDs(result.model_ids);
      if (fetched.length === 0) {
        setError(t("editor.providers.models.workspace.fetchEmpty"));
        return;
      }
      const mergedDrafts =
        mergeProviderModelDraftsWithMetadata(drafts, result.metadata) ?? drafts;
      const nextModels = normalizeProviderModelIDs([
        ...fetched,
        provider.defaultModel,
      ]);
      setModels(nextModels);
      setDrafts(mergedDrafts);
      const warnings = joinProviderOpsWarnings(result.warnings);
      setNotice(
        t("editor.providers.models.workspace.fetchSuccess", {
          count: nextModels.length,
          warnings: warnings
            ? t("editor.providers.models.warningsSuffix", { warnings })
            : "",
        }),
      );
    } catch (fetchError) {
      setError(
        fetchError instanceof Error && fetchError.message.trim()
          ? fetchError.message.trim()
          : t("editor.providers.models.workspace.fetchFailed"),
      );
    } finally {
      setBusy(false);
    }
  }

  function handleApply() {
    const nextModels = normalizeProviderModelIDs([
      ...models,
      provider.defaultModel,
    ]);
    onApplyProviderModels(provider.name, {
      supported_models: nextModels,
      model_capabilities: buildProviderModelCapabilitiesRecord(drafts),
    });
    setError(null);
    setNotice(
      t("editor.providers.models.workspace.applied", { name: provider.name }),
    );
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <Button
          size="sm"
          variant="secondary"
          disabled={busy}
          onClick={() => void handleFetchModels()}
        >
          {busy
            ? t("editor.providers.models.workspace.fetching")
            : t("editor.providers.models.workspace.fetch")}
        </Button>
        <Button size="sm" disabled={busy} onClick={handleApply}>
          {t("editor.providers.models.workspace.apply")}
        </Button>
      </div>

      {notice ? (
        <SettingsNoticeCard tone="neutral">{notice}</SettingsNoticeCard>
      ) : null}
      {error ? (
        <SettingsNoticeCard tone="warning-soft">{error}</SettingsNoticeCard>
      ) : null}

      <ProviderModelEditor
        defaultModel={provider.defaultModel}
        disabled={busy}
        drafts={drafts}
        models={models}
        onChange={(next) => {
          setModels(next.models);
          setDrafts(next.drafts);
        }}
      />

      <p className="text-xs text-muted-foreground">
        {t("editor.providers.models.workspace.supportedModelsPreview", {
          models: formatProviderModelIDsText(models).replace(/\n/g, ", "),
        })}
      </p>
    </div>
  );
}
