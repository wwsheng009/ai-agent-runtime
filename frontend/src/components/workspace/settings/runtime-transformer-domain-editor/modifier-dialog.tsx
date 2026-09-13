// 由 components/workspace/settings/runtime-transformer-domain-editor.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type TFunction } from "i18next";
import { type Dispatch, type SetStateAction } from "react";

import { ConfigDomainDialog } from "../config-domain-dialog";
import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import { type TransformerModifierDraftInput } from "../runtime-transformer-domain-form-utils";
import { type TransformerModifierScope } from "../runtime-transformer-domain-utils";
import { SettingsDialogFooter } from "../settings-dialog-footer";
import { SettingsNoticeCard } from "../settings-notice-card";

import { TransformerToggleCard } from "./toggle-card";

export function RuntimeTransformerModifierDialog({
  dialogError,
  dialogOpen,
  dialogScope,
  draft,
  editingIndex,
  handleSave,
  setDialogOpen,
  setDraft,
  t,
}: {
  dialogError: string | null;
  dialogOpen: boolean;
  dialogScope: TransformerModifierScope;
  draft: TransformerModifierDraftInput;
  editingIndex: number | null;
  handleSave: () => void;
  setDialogOpen: (open: boolean) => void;
  setDraft: Dispatch<SetStateAction<TransformerModifierDraftInput>>;
  t: TFunction<"runtimeConfig">;
}) {
  return (
      <ConfigDomainDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        title={
          editingIndex == null
            ? dialogScope === "request"
              ? t("editor.transformer.dialog.createRequestTitle")
              : t("editor.transformer.dialog.createResponseTitle")
            : dialogScope === "request"
              ? t("editor.transformer.dialog.editRequestTitle", {
                  index: String(editingIndex + 1),
                })
              : t("editor.transformer.dialog.editResponseTitle", {
                  index: String(editingIndex + 1),
                })
        }
        description={t("editor.transformer.dialog.description")}
        footer={
          <SettingsDialogFooter
            confirmLabel={t("editor.transformer.actions.saveDraft")}
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
            <ConfigFormField
              label={t("editor.transformer.fields.typeLabel")}
              description={t("editor.transformer.fields.typeHelp")}
            >
              <input
                className={editorControlClassName}
                value={draft.type}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, type: event.target.value }))
                }
                placeholder={t("editor.transformer.fields.typePlaceholder")}
              />
            </ConfigFormField>
            <TransformerToggleCard
              label={t("editor.transformer.fields.enabledLabel")}
              description={t("editor.transformer.fields.enabledHelp")}
              checked={draft.enabled}
              onCheckedChange={(checked) =>
                setDraft((current) => ({ ...current, enabled: checked }))
              }
            />
          </div>

          <ConfigFormField
            label={t("editor.transformer.fields.modelsLabel")}
            description={t("editor.transformer.fields.modelsHelp")}
          >
            <textarea
              className={`${editorControlClassName} min-h-24 resize-y`}
              value={draft.modelsText}
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  modelsText: event.target.value,
                }))
              }
              placeholder={t("editor.transformer.fields.modelsPlaceholder")}
            />
          </ConfigFormField>

          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField
              label={t("editor.transformer.fields.paramsJsonLabel")}
              description={t("editor.transformer.fields.paramsJsonHelp")}
            >
              <textarea
                className={`${editorControlClassName} min-h-40 resize-y font-mono`}
                spellCheck={false}
                value={draft.paramsJson}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    paramsJson: event.target.value,
                  }))
                }
                placeholder={t("editor.transformer.fields.paramsJsonPlaceholder")}
              />
            </ConfigFormField>
            <ConfigFormField
              label={t("editor.transformer.fields.extraJson")}
              description={t("editor.transformer.fields.extraJsonHelp")}
            >
              <textarea
                className={`${editorControlClassName} min-h-40 resize-y font-mono`}
                spellCheck={false}
                value={draft.extraJson}
                onChange={(event) =>
                  setDraft((current) => ({
                    ...current,
                    extraJson: event.target.value,
                  }))
                }
                placeholder={t("editor.transformer.fields.extraJsonPlaceholder")}
              />
            </ConfigFormField>
          </div>
        </div>
      </ConfigDomainDialog>
  );
}
