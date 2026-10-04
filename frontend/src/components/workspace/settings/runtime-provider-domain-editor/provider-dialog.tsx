import { type Dispatch, type SetStateAction, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import type { ProviderProbeResult } from "@/types/runtime";

import { ConfigDomainDialog } from "../config-domain-dialog";
import { type ProviderDraftInput } from "../runtime-provider-domain-form-utils";
import { SettingsDialogFooter } from "../settings-dialog-footer";
import { SettingsNoticeCard } from "../settings-notice-card";
import { SettingsTabBar, type SettingsTabItem } from "../settings-tab-bar";

import { type AccountAction } from "./draft-utils";
import {
  normalizeProviderModelIDs,
  parseProviderModelIDsText,
} from "./model-capability-draft";
import { ProviderAccountSection } from "./provider-account-section";
import { ProviderAdvancedFields } from "./provider-advanced-fields";
import { ProviderBasicFields } from "./provider-basic-fields";
import { ProviderCredentialFields } from "./provider-credential-fields";
import {
  ProviderModelsSection,
  type ProviderModelsAction,
} from "./provider-models-section";
import { ProviderNetworkFields } from "./provider-network-fields";

type ProviderDialogTab = "basic" | "connection" | "models" | "advanced";

type ProviderDialogProps = {
  accountBusy: AccountAction;
  accountError: string | null;
  accountNotice: string | null;
  accountSummaryLine: string;
  assumedModelIDs: string[];
  dialogError: string | null;
  draft: ProviderDraftInput;
  editingProviderName: string | null;
  modelsBusy: ProviderModelsAction;
  modelsError: string | null;
  modelsNotice: string | null;
  onClose: () => void;
  onConfirm: () => void;
  onDetectSiteType: () => void;
  onFetchAccount: () => void;
  onFetchModels: () => void;
  onAutoImport: () => void;
  onMergeModels: (modelIDs: string[]) => void;
  onProbeModels: () => void;
  onRefreshProviderAccount: (
    providerName: string,
    options?: { fromDialog?: boolean },
  ) => void;
  open: boolean;
  probeResult: ProviderProbeResult | null;
  setDraft: Dispatch<SetStateAction<ProviderDraftInput>>;
};

export function ProviderDialog({
  accountBusy,
  accountError,
  accountNotice,
  accountSummaryLine,
  assumedModelIDs,
  dialogError,
  draft,
  editingProviderName,
  modelsBusy,
  modelsError,
  modelsNotice,
  onClose,
  onConfirm,
  onDetectSiteType,
  onFetchAccount,
  onAutoImport,
  onFetchModels,
  onMergeModels,
  onProbeModels,
  onRefreshProviderAccount,
  open,
  probeResult,
  setDraft,
}: ProviderDialogProps) {
  const { t } = useTranslation("runtimeConfig");
  const [activeTab, setActiveTab] = useState<ProviderDialogTab>("basic");

  useEffect(() => {
    if (!open) {
      return;
    }
    // 与 settings-dialog 相同的 reset 纪律：迁移到微任务，避免在 effect 体内
    // 同步 setState 触发级联渲染（react-hooks/set-state-in-effect）。
    let cancelled = false;
    queueMicrotask(() => {
      if (!cancelled) {
        setActiveTab("basic");
      }
    });
    return () => {
      cancelled = true;
    };
  }, [open, editingProviderName]);

  const modelCount = useMemo(
    () =>
      normalizeProviderModelIDs([
        ...parseProviderModelIDsText(draft.supportedModelsText),
        draft.defaultModel,
      ]).length,
    [draft.supportedModelsText, draft.defaultModel],
  );

  const tabs: ReadonlyArray<SettingsTabItem<ProviderDialogTab>> = [
    { id: "basic", label: t("editor.providers.dialogTabs.basic") },
    { id: "connection", label: t("editor.providers.dialogTabs.connection") },
    {
      id: "models",
      label: t("editor.providers.dialogTabs.models"),
      badge: modelCount > 0 ? String(modelCount) : undefined,
    },
    { id: "advanced", label: t("editor.providers.dialogTabs.advanced") },
  ];

  return (
      <ConfigDomainDialog
        open={open}
        onClose={onClose}
        title={
          editingProviderName
            ? t("editor.providers.editTitle", { name: editingProviderName })
            : t("editor.providers.createTitle")
        }
        bodyClassName="p-0 sm:p-0"
        widthClassName="max-w-6xl"
        footer={
          <SettingsDialogFooter
            buttonSize="sm"
            note={t("editor.providers.saveNote")}
            confirmLabel={t("editor.providers.saveButton")}
            onCancel={onClose}
            onConfirm={onConfirm}
          />
        }
      >
        <div className="sticky top-0 z-20 border-b border-border/60 px-3 pb-2 pt-3 [background:var(--dialog-bg)] sm:px-4">
          <SettingsTabBar
            ariaLabel={t("editor.providers.dialogTabs.aria")}
            items={tabs}
            value={activeTab}
            onChange={setActiveTab}
          />
        </div>

        <div className="space-y-3 px-3 py-3 sm:px-4">
          {dialogError ? (
            <SettingsNoticeCard tone="warning-soft">
              {dialogError}
            </SettingsNoticeCard>
          ) : null}

          {activeTab === "basic" ? (
            <div role="tabpanel" className="space-y-3">
              <ProviderBasicFields draft={draft} setDraft={setDraft} />

              <div className="flex flex-wrap gap-3 rounded-card border border-border bg-surface-softer px-3 py-2.5">
                <label className="flex items-center gap-2 text-sm text-foreground">
                  <input
                    type="checkbox"
                    className="h-4 w-4 accent-accent-primary"
                    checked={draft.enabled}
                    onChange={(event) =>
                      setDraft((current) => ({
                        ...current,
                        enabled: event.target.checked,
                      }))
                    }
                  />
                  {t("editor.providers.fields.enableProvider")}
                </label>
                <label className="flex items-center gap-2 text-sm text-foreground">
                  <input
                    type="checkbox"
                    className="h-4 w-4 accent-accent-primary"
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
          ) : null}

          {activeTab === "connection" ? (
            <div role="tabpanel" className="space-y-3">
              <ProviderCredentialFields draft={draft} setDraft={setDraft} />
              <ProviderNetworkFields draft={draft} setDraft={setDraft} />
              <ProviderAccountSection
                accountBusy={accountBusy}
                accountError={accountError}
                accountNotice={accountNotice}
                accountSummaryLine={accountSummaryLine}
                draft={draft}
                editingProviderName={editingProviderName}
                onDetectSiteType={onDetectSiteType}
                onFetchAccount={onFetchAccount}
                onRefreshProviderAccount={onRefreshProviderAccount}
                setDraft={setDraft}
              />
            </div>
          ) : null}

          {activeTab === "models" ? (
            <div role="tabpanel" className="space-y-3">
              <ProviderModelsSection
                assumedModelIDs={assumedModelIDs}
                busy={modelsBusy}
                draft={draft}
                editingProviderName={editingProviderName}
                error={modelsError}
                modelsNotice={modelsNotice}
                onAutoImport={onAutoImport}
                onFetchModels={onFetchModels}
                onMergeModels={onMergeModels}
                onProbeModels={onProbeModels}
                probeResult={probeResult}
                setDraft={setDraft}
              />
            </div>
          ) : null}

          {activeTab === "advanced" ? (
            <div role="tabpanel" className="space-y-3">
              <ProviderAdvancedFields draft={draft} setDraft={setDraft} />
            </div>
          ) : null}
        </div>
      </ConfigDomainDialog>
  );
}
