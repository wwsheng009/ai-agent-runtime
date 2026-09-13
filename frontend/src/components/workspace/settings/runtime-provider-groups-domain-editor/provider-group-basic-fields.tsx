import { type Dispatch, type SetStateAction } from "react";
import { type TFunction } from "i18next";

import { Select } from "@/components/ui/select";

import { ConfigFormField } from "../config-form-field";
import { editorControlClassName } from "../editor-control-class";
import {
  type ProviderGroupDraftInput,
  type ProviderGroupDraftValidationIssue,
} from "../runtime-provider-groups-domain-form-utils";

import {
  buildSelectOptionsWithCurrent,
  findDraftIssue,
  getDraftFieldClassName,
  providerGroupFailoverModeOptions,
  providerGroupFailoverScopeOptions,
  providerGroupStrategyOptions,
  providerGroupTruncationStrategyOptions,
} from "./draft-utils";
import { FieldIssueText } from "./field-parts";

type ProviderGroupBasicFieldsProps = {
  draft: ProviderGroupDraftInput;
  draftValidationIssues: ProviderGroupDraftValidationIssue[];
  setDraft: Dispatch<SetStateAction<ProviderGroupDraftInput>>;
  t: TFunction<"runtimeConfig">;
};

export function ProviderGroupBasicFields({
  draft,
  draftValidationIssues,
  setDraft,
  t,
}: ProviderGroupBasicFieldsProps) {
  return (
    <>
          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField
              label={t("editor.providerGroups.fields.name")}
              description={t("editor.providerGroups.fields.nameHelp")}
            >
              <input
                className={editorControlClassName}
                value={draft.name}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, name: event.target.value }))
                }
                placeholder="openai_group"
              />
            </ConfigFormField>
            <ConfigFormField
              label="strategy"
              description={t("editor.providerGroups.fields.strategyHelp")}
            >
              <Select
                ariaLabel={t("editor.providerGroups.fields.strategyAria")}
                value={draft.strategy}
                onChange={(value) =>
                  setDraft((current) => ({ ...current, strategy: value }))
                }
                options={buildSelectOptionsWithCurrent(
                  providerGroupStrategyOptions,
                  draft.strategy,
                  t,
                )}
                placeholder={t("editor.providerGroups.fields.strategyPlaceholder")}
                className="w-full"
                triggerClassName={editorControlClassName}
                optionClassName="text-sm"
              />
            </ConfigFormField>
            <ConfigFormField label="max_retries">
              <input
                className={getDraftFieldClassName(
                  findDraftIssue(draftValidationIssues, "maxRetries") != null,
                )}
                value={draft.maxRetries}
                inputMode="numeric"
                onChange={(event) =>
                  setDraft((current) => ({ ...current, maxRetries: event.target.value }))
                }
                placeholder="3"
              />
              <FieldIssueText issue={findDraftIssue(draftValidationIssues, "maxRetries")} />
            </ConfigFormField>
            <ConfigFormField label="retry_delay">
              <input
                className={getDraftFieldClassName(
                  findDraftIssue(draftValidationIssues, "retryDelay") != null,
                )}
                value={draft.retryDelay}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, retryDelay: event.target.value }))
                }
                placeholder="1s"
              />
              <FieldIssueText issue={findDraftIssue(draftValidationIssues, "retryDelay")} />
            </ConfigFormField>
          </div>

          <div className="grid gap-3 xl:grid-cols-2">
            <div className="space-y-3 rounded-[0.8rem] border border-border bg-surface-softer p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div>
                  <div className="text-[13px] font-semibold text-foreground">
                    {t("editor.providerGroups.sections.failover")}
                  </div>
                  <div className="mt-1 text-xs text-muted-foreground">
                    {t("editor.providerGroups.sections.failoverHelp")}
                  </div>
                </div>
                <label className="flex items-center gap-2 text-sm text-foreground">
                  <input
                    type="checkbox"
                    className="h-4 w-4 accent-accent-primary"
                    checked={draft.failoverEnabled}
                    onChange={(event) =>
                      setDraft((current) => ({
                        ...current,
                        failoverEnabled: event.target.checked,
                      }))
                    }
                  />
                  {t("editor.providerGroups.toggle.enabled")}
                </label>
              </div>

              <div className="grid gap-3">
                <ConfigFormField label="failover.mode">
                  <Select
                    ariaLabel={t("editor.providerGroups.fields.failoverModeAria")}
                    value={draft.failoverMode}
                    onChange={(value) =>
                      setDraft((current) => ({
                        ...current,
                        failoverMode: value,
                      }))
                    }
                    options={buildSelectOptionsWithCurrent(
                      providerGroupFailoverModeOptions,
                      draft.failoverMode,
                      t,
                    )}
                    placeholder={t("editor.providerGroups.fields.failoverModePlaceholder")}
                    className="w-full"
                    triggerClassName={editorControlClassName}
                    optionClassName="text-sm"
                  />
                </ConfigFormField>
                <ConfigFormField label="failover.scope">
                  <Select
                    ariaLabel={t("editor.providerGroups.fields.failoverScopeAria")}
                    value={draft.failoverScope}
                    onChange={(value) =>
                      setDraft((current) => ({
                        ...current,
                        failoverScope: value,
                      }))
                    }
                    options={buildSelectOptionsWithCurrent(
                      providerGroupFailoverScopeOptions,
                      draft.failoverScope,
                      t,
                    )}
                    placeholder={t("editor.providerGroups.fields.failoverScopePlaceholder")}
                    className="w-full"
                    triggerClassName={editorControlClassName}
                    optionClassName="text-sm"
                  />
                </ConfigFormField>
              </div>
            </div>

            <div className="space-y-3 rounded-[0.8rem] border border-border bg-surface-softer p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div>
                  <div className="text-[13px] font-semibold text-foreground">
                    {t("editor.providerGroups.sections.truncation")}
                  </div>
                  <div className="mt-1 text-xs text-muted-foreground">
                    {t("editor.providerGroups.sections.truncationHelp")}
                  </div>
                </div>
                <label className="flex items-center gap-2 text-sm text-foreground">
                  <input
                    type="checkbox"
                    className="h-4 w-4 accent-accent-primary"
                    checked={draft.truncationEnabled}
                    onChange={(event) =>
                      setDraft((current) => ({
                        ...current,
                        truncationEnabled: event.target.checked,
                      }))
                    }
                  />
                  {t("editor.providerGroups.toggle.enabled")}
                </label>
              </div>

              <div className="grid gap-3">
                <ConfigFormField label="truncation.max_retries">
                  <input
                    className={getDraftFieldClassName(
                      findDraftIssue(draftValidationIssues, "truncationMaxRetries") != null,
                    )}
                    value={draft.truncationMaxRetries}
                    inputMode="numeric"
                    onChange={(event) =>
                      setDraft((current) => ({
                        ...current,
                        truncationMaxRetries: event.target.value,
                      }))
                    }
                    placeholder="3"
                  />
                  <FieldIssueText
                    issue={findDraftIssue(draftValidationIssues, "truncationMaxRetries")}
                  />
                </ConfigFormField>
                <div className="grid gap-3 xl:grid-cols-2">
                  <ConfigFormField label="truncation.strategy">
                    <Select
                      ariaLabel={t("editor.providerGroups.fields.truncationStrategyAria")}
                      value={draft.truncationStrategy}
                      onChange={(value) =>
                        setDraft((current) => ({
                          ...current,
                          truncationStrategy: value,
                        }))
                      }
                      options={buildSelectOptionsWithCurrent(
                        providerGroupTruncationStrategyOptions,
                        draft.truncationStrategy,
                        t,
                      )}
                      placeholder={t(
                        "editor.providerGroups.fields.truncationStrategyPlaceholder",
                      )}
                      className="w-full"
                      triggerClassName={editorControlClassName}
                      optionClassName="text-sm"
                    />
                  </ConfigFormField>
                  <ConfigFormField label="truncation.step">
                    <input
                      className={getDraftFieldClassName(
                        findDraftIssue(draftValidationIssues, "truncationStep") != null,
                      )}
                      value={draft.truncationStep}
                      inputMode="decimal"
                      onChange={(event) =>
                        setDraft((current) => ({
                          ...current,
                          truncationStep: event.target.value,
                        }))
                      }
                      placeholder="10"
                    />
                    <FieldIssueText
                      issue={findDraftIssue(draftValidationIssues, "truncationStep")}
                    />
                  </ConfigFormField>
                </div>
              </div>
            </div>
          </div>
    </>
  );
}
