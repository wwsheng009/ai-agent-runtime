import { type Dispatch, type SetStateAction } from "react";
import { useTranslation } from "react-i18next";

import { ConfigDomainDialog } from "../config-domain-dialog";
import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import { type ProviderDraftInput } from "../runtime-provider-domain-form-utils";
import { SettingsDialogFooter } from "../settings-dialog-footer";
import { SettingsNoticeCard } from "../settings-notice-card";

import { type AccountAction } from "./draft-utils";
import { ProviderAccountSection } from "./provider-account-section";
import { ProviderBasicFields } from "./provider-basic-fields";
import { ProviderNetworkFields } from "./provider-network-fields";

type ProviderDialogProps = {
  accountBusy: AccountAction;
  accountError: string | null;
  accountNotice: string | null;
  accountSummaryLine: string;
  dialogError: string | null;
  draft: ProviderDraftInput;
  editingProviderName: string | null;
  onClose: () => void;
  onConfirm: () => void;
  onDetectSiteType: () => void;
  onFetchAccount: () => void;
  onRefreshProviderAccount: (
    providerName: string,
    options?: { fromDialog?: boolean },
  ) => void;
  open: boolean;
  setDraft: Dispatch<SetStateAction<ProviderDraftInput>>;
};

export function ProviderDialog({
  accountBusy,
  accountError,
  accountNotice,
  accountSummaryLine,
  dialogError,
  draft,
  editingProviderName,
  onClose,
  onConfirm,
  onDetectSiteType,
  onFetchAccount,
  onRefreshProviderAccount,
  open,
  setDraft,
}: ProviderDialogProps) {
  const { t } = useTranslation("runtimeConfig");
  return (
      <ConfigDomainDialog
        open={open}
        onClose={onClose}
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
            onCancel={onClose}
            onConfirm={onConfirm}
          />
        }
      >
        <div className="space-y-3">
          {dialogError ? (
            <SettingsNoticeCard tone="warning-soft">
              {dialogError}
            </SettingsNoticeCard>
          ) : null}

          <ProviderBasicFields draft={draft} setDraft={setDraft} />

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

          <ProviderNetworkFields draft={draft} setDraft={setDraft} />

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

          <div className="flex flex-wrap gap-3 rounded-[0.8rem] border border-border bg-surface-softer px-3 py-2.5">
            <label className="flex items-center gap-2 text-sm text-foreground">
              <input
                type="checkbox"
                className="h-4 w-4 accent-accent-primary"
                checked={draft.enabled}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, enabled: event.target.checked }))
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
      </ConfigDomainDialog>
  );
}
