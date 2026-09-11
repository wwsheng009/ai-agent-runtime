import { type Dispatch, type SetStateAction } from "react";
import { type TFunction } from "i18next";

import { ConfigDomainDialog } from "../config-domain-dialog";
import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import {
  type ProviderGroupDraftInput,
  type ProviderGroupDraftValidationIssue,
} from "../runtime-provider-groups-domain-form-utils";
import { type RuntimeProviderSummary } from "../runtime-provider-config-utils";
import { SettingsDialogFooter } from "../settings-dialog-footer";
import { SettingsNoticeCard } from "../settings-notice-card";

import { ProviderGroupBasicFields } from "./provider-group-basic-fields";
import { ProviderGroupMembersSection } from "./provider-group-members-section";

type ProviderGroupDialogProps = {
  dialogError: string | null;
  draft: ProviderGroupDraftInput;
  draftValidationIssues: ProviderGroupDraftValidationIssue[];
  editingGroupName: string | null;
  onClose: () => void;
  onConfirm: () => void;
  open: boolean;
  providerLookup: Map<string, RuntimeProviderSummary>;
  providerNames: string[];
  providers: RuntimeProviderSummary[];
  setDraft: Dispatch<SetStateAction<ProviderGroupDraftInput>>;
  t: TFunction<"runtimeConfig">;
};

export function ProviderGroupDialog({
  dialogError,
  draft,
  draftValidationIssues,
  editingGroupName,
  onClose,
  onConfirm,
  open,
  providerLookup,
  providerNames,
  providers,
  setDraft,
  t,
}: ProviderGroupDialogProps) {
  return (
      <ConfigDomainDialog
        open={open}
        onClose={onClose}
        title={
          editingGroupName
            ? t("editor.providerGroups.dialog.editTitle", {
                name: editingGroupName,
              })
            : t("editor.providerGroups.dialog.createTitle")
        }
        description={t("editor.providerGroups.dialog.description")}
        footer={
          <SettingsDialogFooter
            buttonSize="sm"
            note={t("editor.providerGroups.dialog.saveNote")}
            confirmLabel={t("editor.providerGroups.actions.save")}
            onCancel={onClose}
            onConfirm={onConfirm}
          />
        }
        widthClassName="max-w-6xl"
      >
        <div className="space-y-3">
          {dialogError ? (
            <SettingsNoticeCard tone="warning-soft">
              {dialogError}
            </SettingsNoticeCard>
          ) : null}
          {draftValidationIssues.length > 0 ? (
            <SettingsNoticeCard tone="warning-soft">
              <div className="space-y-1">
                <div className="font-medium">
                  {t("editor.providerGroups.validation.pendingTitle")}
                </div>
                {draftValidationIssues.slice(0, 4).map((issue) => (
                  <div key={`${issue.field}-${issue.memberIndex ?? "root"}-${issue.message}`}>
                    {issue.message}
                  </div>
                ))}
                {draftValidationIssues.length > 4 ? (
                  <div>
                    {t("editor.providerGroups.validation.moreHidden", {
                      count: draftValidationIssues.length - 4,
                    })}
                  </div>
                ) : null}
              </div>
            </SettingsNoticeCard>
          ) : null}
          <ProviderGroupBasicFields
            draft={draft}
            draftValidationIssues={draftValidationIssues}
            setDraft={setDraft}
            t={t}
          />
          <ProviderGroupMembersSection
            draft={draft}
            draftValidationIssues={draftValidationIssues}
            providerLookup={providerLookup}
            providerNames={providerNames}
            providers={providers}
            setDraft={setDraft}
            t={t}
          />
          <ConfigFormField
            label={t("editor.providerGroups.fields.extraJson")}
            description={t("editor.providerGroups.fields.extraJsonHelp")}
          >
            <textarea
              className={`${editorControlClassName} min-h-40 resize-y font-mono`}
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
