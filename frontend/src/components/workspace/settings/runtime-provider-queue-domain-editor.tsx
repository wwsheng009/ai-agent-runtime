import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  type ProviderQueueProviderDraftInput,
} from "./runtime-provider-queue-domain-form-utils";
import {
  type RuntimeProviderQueueProviderSummary,
} from "./runtime-provider-queue-domain-utils";

import { stringifyExtraFields } from "./runtime-provider-queue-domain-editor/draft-utils";
import { RuntimeProviderQueueDefaultsSection } from "./runtime-provider-queue-domain-editor/defaults-section";
import { RuntimeProviderQueueOverflowHeartbeatSection } from "./runtime-provider-queue-domain-editor/overflow-heartbeat-section";
import { RuntimeProviderQueueProviderDialog } from "./runtime-provider-queue-domain-editor/provider-dialog";
import { RuntimeProviderQueueProviderTable } from "./runtime-provider-queue-domain-editor/provider-table";
import { type RuntimeProviderQueueDomainEditorProps } from "./runtime-provider-queue-domain-editor/types";

export function RuntimeProviderQueueDomainEditor({
  config,
  onChangeConfig,
  onDeleteProvider,
  onSaveProvider,
  providers,
}: RuntimeProviderQueueDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [editingProvider, setEditingProvider] = useState<string | null>(null);
  const [draft, setDraft] = useState<ProviderQueueProviderDraftInput>({
    provider: "",
    maxConcurrency: "",
    queueSize: "",
    queueTimeout: "",
    extraJson: "",
  });

  const totalMaxConcurrency = useMemo(
    () => providers.reduce((sum, item) => sum + (Number(item.maxConcurrency) || 0), 0),
    [providers],
  );

  function openCreateDialog() {
    setDialogError(null);
    setEditingProvider(null);
    setDraft({
      provider: "",
      maxConcurrency: "",
      queueSize: "",
      queueTimeout: "",
      extraJson: "",
    });
    setDialogOpen(true);
  }

  function openEditDialog(item: RuntimeProviderQueueProviderSummary) {
    setDialogError(null);
    setEditingProvider(item.provider);
    setDraft({
      provider: item.provider,
      maxConcurrency: item.maxConcurrency,
      queueSize: item.queueSize,
      queueTimeout: item.queueTimeout,
      extraJson: stringifyExtraFields(item.raw),
    });
    setDialogOpen(true);
  }

  function handleSave() {
    const error = onSaveProvider(draft, editingProvider);
    if (error) {
      setDialogError(error);
      return;
    }
    setDialogOpen(false);
  }

  return (
    <div className="space-y-3">
      <RuntimeProviderQueueDefaultsSection
        config={config}
        onChangeConfig={onChangeConfig}
        providers={providers}
        t={t}
        totalMaxConcurrency={totalMaxConcurrency}
      />

      <RuntimeProviderQueueOverflowHeartbeatSection
        config={config}
        onChangeConfig={onChangeConfig}
        t={t}
      />

      <RuntimeProviderQueueProviderTable
        config={config}
        onDeleteProvider={onDeleteProvider}
        openCreateDialog={openCreateDialog}
        openEditDialog={openEditDialog}
        providers={providers}
        t={t}
      />

      <RuntimeProviderQueueProviderDialog
        dialogError={dialogError}
        dialogOpen={dialogOpen}
        draft={draft}
        editingProvider={editingProvider}
        handleSave={handleSave}
        setDialogOpen={setDialogOpen}
        setDraft={setDraft}
        t={t}
      />
    </div>
  );
}
