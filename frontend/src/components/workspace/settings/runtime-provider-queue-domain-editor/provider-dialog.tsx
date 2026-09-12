// 由 components/workspace/settings/runtime-provider-queue-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { type Dispatch, type SetStateAction } from "react";

import {
  type ProviderQueueProviderDraftInput,
} from "../runtime-provider-queue-domain-form-utils";
import { ConfigDomainDialog } from "../config-domain-dialog";
import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import { SettingsDialogFooter } from "../settings-dialog-footer";
import { SettingsNoticeCard } from "../settings-notice-card";

export function RuntimeProviderQueueProviderDialog({
  dialogError,
  dialogOpen,
  draft,
  editingProvider,
  handleSave,
  setDialogOpen,
  setDraft,
  t,
}: {
  dialogError: string | null;
  dialogOpen: boolean;
  draft: ProviderQueueProviderDraftInput;
  editingProvider: string | null;
  handleSave: () => void;
  setDialogOpen: Dispatch<SetStateAction<boolean>>;
  setDraft: Dispatch<SetStateAction<ProviderQueueProviderDraftInput>>;
  t: TFunction<"runtimeConfig">;
}) {
  return (
      <ConfigDomainDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        title={
          editingProvider
            ? t("editor.providerQueue.dialog.editTitle", { name: editingProvider })
            : t("editor.providerQueue.dialog.createTitle")
        }
        description={t("editor.providerQueue.dialog.description")}
        footer={
          <SettingsDialogFooter
            confirmLabel={t("editor.providerQueue.dialog.save")}
            onCancel={() => setDialogOpen(false)}
            onConfirm={handleSave}
          />
        }
        widthClassName="max-w-3xl"
      >
        <div className="space-y-3">
          {dialogError ? (
            <SettingsNoticeCard tone="warning">{dialogError}</SettingsNoticeCard>
          ) : null}
          <div className="grid gap-3 md:grid-cols-2">
            <ConfigFormField label="provider">
              <input
                className={editorControlClassName}
                value={draft.provider}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, provider: event.target.value }))
                }
                placeholder="nvidia"
              />
            </ConfigFormField>
            <ConfigFormField label="max_concurrency">
              <input
                className={editorControlClassName}
                value={draft.maxConcurrency}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    maxConcurrency: event.target.value,
                  }))
                }
                placeholder="5"
              />
            </ConfigFormField>
            <ConfigFormField label="queue_size">
              <input
                className={editorControlClassName}
                value={draft.queueSize}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    queueSize: event.target.value,
                  }))
                }
                placeholder="20"
              />
            </ConfigFormField>
            <ConfigFormField label="queue_timeout">
              <input
                className={editorControlClassName}
                value={draft.queueTimeout}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    queueTimeout: event.target.value,
                  }))
                }
                placeholder="60s"
              />
            </ConfigFormField>
          </div>

          <ConfigFormField
            label={t("editor.providerQueue.fields.extraJson")}
            description={t("editor.providerQueue.fields.extraJsonDescription")}
          >
            <textarea
              className={`${editorControlClassName} min-h-28 resize-y font-mono`}
              spellCheck={false}
              value={draft.extraJson}
              onChange={(event) =>
                setDraft((current) => ({ ...current, extraJson: event.target.value }))
              }
            />
          </ConfigFormField>
        </div>
      </ConfigDomainDialog>
  );
}
