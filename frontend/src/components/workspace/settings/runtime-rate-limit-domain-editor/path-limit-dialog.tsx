import { type TFunction } from "i18next";
import { type Dispatch, type SetStateAction } from "react";

import { ConfigDomainDialog } from "../config-domain-dialog";
import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import { type RateLimitPathDraftInput } from "../runtime-rate-limit-domain-form-utils";
import { SettingsDialogFooter } from "../settings-dialog-footer";
import { SettingsNoticeCard } from "../settings-notice-card";

export function RuntimeRateLimitPathLimitDialog({
  editingPath,
  handleSavePathLimit,
  pathDialogError,
  pathDialogOpen,
  pathDraft,
  setPathDialogOpen,
  setPathDraft,
  t,
}: {
  editingPath: string | null;
  handleSavePathLimit: () => void;
  pathDialogError: string | null;
  pathDialogOpen: boolean;
  pathDraft: RateLimitPathDraftInput;
  setPathDialogOpen: (open: boolean) => void;
  setPathDraft: Dispatch<SetStateAction<RateLimitPathDraftInput>>;
  t: TFunction<"runtimeConfig">;
}) {
  return (
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
          <ConfigFormField label={t("editor.rateLimit.fields.path")}>
            <input
              className={editorControlClassName}
              value={pathDraft.path}
              onChange={(event) =>
                setPathDraft((current) => ({ ...current, path: event.target.value }))
              }
              placeholder={t("editor.rateLimit.fields.pathPlaceholder")}
            />
          </ConfigFormField>
          <ConfigFormField label={t("editor.rateLimit.fields.requestsPerMinute")}>
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
          <ConfigFormField label={t("editor.rateLimit.fields.burst")}>
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
  );
}

