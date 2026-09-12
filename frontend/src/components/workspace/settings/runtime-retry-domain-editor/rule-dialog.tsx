// 由 components/workspace/settings/runtime-retry-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { type Dispatch, type SetStateAction } from "react";

import { ConfigDomainDialog } from "../config-domain-dialog";
import { ConfigFormField } from "../config-form-field";
import { editorControlClassName, editorToggleRowClassName } from "../editor-control-class";
import { SettingsDialogFooter } from "../settings-dialog-footer";
import { SettingsNoticeCard } from "../settings-notice-card";

import { type RetryRuleDraftInput } from "./types";

export function RuntimeRetryRuleDialog({
  dialogError,
  dialogOpen,
  draft,
  editingIndex,
  handleSave,
  setDialogOpen,
  setDraft,
  t,
}: {
  dialogError: string | null;
  dialogOpen: boolean;
  draft: RetryRuleDraftInput;
  editingIndex: number | null;
  handleSave: () => void;
  setDialogOpen: (open: boolean) => void;
  setDraft: Dispatch<SetStateAction<RetryRuleDraftInput>>;
  t: TFunction<"runtimeConfig">;
}) {
  return (
      <ConfigDomainDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        title={
          editingIndex == null
            ? t("editor.retry.dialog.createTitle")
            : t("editor.retry.dialog.editTitle", {
                index: String(editingIndex + 1),
              })
        }
        description={t("editor.retry.dialog.description")}
        footer={
          <SettingsDialogFooter
            confirmLabel={t("editor.retry.actions.saveDraft")}
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
                <span>
                  {draft.enabled
                    ? t("editor.retry.toggle.enabled")
                    : t("editor.retry.toggle.disabled")}
                </span>
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
            <ConfigFormField
              label="error_code.codes"
              description={t("editor.retry.fields.listHint")}
            >
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
            <ConfigFormField
              label="keyword.values"
              description={t("editor.retry.fields.listHint")}
            >
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
            <ConfigFormField
              label="keyword.patterns"
              description={t("editor.retry.fields.listHint")}
            >
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
                <span>
                  {draft.keywordCaseSensitive
                    ? t("editor.retry.caseSensitive.on")
                    : t("editor.retry.caseSensitive.off")}
                </span>
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
  );
}
