import { type TFunction } from "i18next";
import { type Dispatch, type SetStateAction } from "react";

import { ConfigDomainDialog } from "../config-domain-dialog";
import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import { type RateLimitApiKeyDraftInput } from "../runtime-rate-limit-domain-form-utils";
import { SettingsDialogFooter } from "../settings-dialog-footer";
import { SettingsNoticeCard } from "../settings-notice-card";

export function RuntimeRateLimitApiKeyLimitDialog({
  apiDialogError,
  apiDialogOpen,
  apiDraft,
  editingApiKeyIndex,
  handleSaveApiLimit,
  setApiDialogOpen,
  setApiDraft,
  t,
}: {
  apiDialogError: string | null;
  apiDialogOpen: boolean;
  apiDraft: RateLimitApiKeyDraftInput;
  editingApiKeyIndex: number | null;
  handleSaveApiLimit: () => void;
  setApiDialogOpen: (open: boolean) => void;
  setApiDraft: Dispatch<SetStateAction<RateLimitApiKeyDraftInput>>;
  t: TFunction<"runtimeConfig">;
}) {
  return (
    <ConfigDomainDialog
      open={apiDialogOpen}
      onClose={() => setApiDialogOpen(false)}
      title={
        editingApiKeyIndex == null
          ? t("editor.rateLimit.apiKey.dialog.createTitle")
          : t("editor.rateLimit.apiKey.dialog.editTitle", {
              index: String(editingApiKeyIndex + 1),
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
          <ConfigFormField label={t("editor.rateLimit.fields.apiKeyPattern")}>
            <input
              className={editorControlClassName}
              value={apiDraft.apiKeyPattern}
              onChange={(event) =>
                setApiDraft((current) => ({
                  ...current,
                  apiKeyPattern: event.target.value,
                }))
              }
              placeholder={t("editor.rateLimit.fields.apiKeyPatternPlaceholder")}
            />
          </ConfigFormField>
          <ConfigFormField label={t("editor.rateLimit.fields.blockDuration")}>
            <input
              className={editorControlClassName}
              value={apiDraft.blockDuration}
              onChange={(event) =>
                setApiDraft((current) => ({
                  ...current,
                  blockDuration: event.target.value,
                }))
              }
              placeholder={t("editor.rateLimit.fields.blockDurationPlaceholder")}
            />
          </ConfigFormField>
          <ConfigFormField label={t("editor.rateLimit.fields.qps")}>
            <input
              className={editorControlClassName}
              value={apiDraft.qps}
              onChange={(event) =>
                setApiDraft((current) => ({ ...current, qps: event.target.value }))
              }
            />
          </ConfigFormField>
          <ConfigFormField label={t("editor.rateLimit.fields.qpm")}>
            <input
              className={editorControlClassName}
              value={apiDraft.qpm}
              onChange={(event) =>
                setApiDraft((current) => ({ ...current, qpm: event.target.value }))
              }
            />
          </ConfigFormField>
          <ConfigFormField label={t("editor.rateLimit.fields.qpd")}>
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
  );
}

