import {
  CheckIcon,
  CopyIcon,
  PencilIcon,
  RouteIcon,
  Trash2Icon,
} from "lucide-react";
import { type TFunction } from "i18next";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";

import { ConfigDomainDialog } from "./config-domain-dialog";
import {
  ConfigDomainSummaryBadge,
  ConfigDomainTable,
} from "./config-domain-table";
import { ConfigFormField } from "./config-form-field";
import {
  buildProviderGroupCreateConfigSnippet,
  createDefaultProviderGroup,
  type RuntimeProviderGroupSummary,
} from "./runtime-config-domain-utils";
import {
  SettingsActionGroup,
  SettingsIconActionButton,
} from "./settings-action-group";
import { SettingsAddButton } from "./settings-add-button";
import { SettingsDialogFooter } from "./settings-dialog-footer";
import { SettingsEmptyState } from "./settings-empty-state";
import { SettingsNoticeCard } from "./settings-notice-card";
import {
  type ProviderGroupDraftInput,
  type ProviderGroupDraftValidationIssue,
  type ProviderGroupMemberDraftInput,
  validateProviderGroupDraft,
} from "./runtime-provider-groups-domain-form-utils";
import {
  isConfigRecord,
  type RuntimeProviderSummary,
} from "./runtime-provider-config-utils";
import { editorControlClassName } from "./editor-control-class";

const KNOWN_PROVIDER_GROUP_KEYS = new Set([
  "name",
  "strategy",
  "max_retries",
  "retry_delay",
  "failover",
  "truncation",
  "providers",
]);

const providerGroupStrategyOptions = [
  { value: "round_robin", label: "round_robin" },
  { value: "health", label: "health" },
  { value: "random", label: "random" },
  { value: "weighted", label: "weighted" },
] as const;
const providerGroupFailoverModeOptions = [
  { value: "primary_standby", label: "primary_standby" },
] as const;
const providerGroupFailoverScopeOptions = [
  { value: "model_key", label: "model_key" },
] as const;
const providerGroupTruncationStrategyOptions = [
  { value: "percentage", label: "percentage" },
] as const;
const providerGroupMemberRoleOptions = [
  { value: "primary", label: "primary" },
  { value: "standby", label: "standby" },
] as const;

type RuntimeProviderGroupsDomainEditorProps = {
  groups: RuntimeProviderGroupSummary[];
  onDeleteGroup: (name: string) => void;
  onSaveGroup: (
    draft: ProviderGroupDraftInput,
    previousName: string | null,
  ) => string | null;
  providers: RuntimeProviderSummary[];
};

export function RuntimeProviderGroupsDomainEditor({
  groups,
  onDeleteGroup,
  onSaveGroup,
  providers,
}: RuntimeProviderGroupsDomainEditorProps) {
  const { t } = useTranslation("runtimeConfig");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogError, setDialogError] = useState<string | null>(null);
  const [editingGroupName, setEditingGroupName] = useState<string | null>(null);
  const [copiedGroupName, setCopiedGroupName] = useState<string | null>(null);
  const [draft, setDraft] = useState<ProviderGroupDraftInput>(() =>
    createProviderGroupDraftInput(null),
  );

  const providerLookup = useMemo(
    () => new Map(providers.map((provider) => [provider.name, provider])),
    [providers],
  );
  const providerNames = useMemo(
    () => providers.map((provider) => provider.name),
    [providers],
  );
  const draftValidationIssues = useMemo(
    () => validateProviderGroupDraft(draft),
    [draft],
  );
  const isPrimaryStandbyMode =
    draft.failoverEnabled && draft.failoverMode.trim() === "primary_standby";
  const missingRoleCount = useMemo(
    () =>
      draft.members.filter((member) => member.name.trim() && !member.role.trim()).length,
    [draft.members],
  );
  const weightedMissingWeightCount = useMemo(
    () =>
      draft.strategy.trim() === "weighted"
        ? draft.members.filter((member) => member.name.trim() && !member.weight.trim()).length
        : 0,
    [draft.members, draft.strategy],
  );
  const memberSummary = useMemo(
    () => summarizeMembers(draft.members),
    [draft.members],
  );
  const referencedProviderCount = useMemo(
    () =>
      new Set(
        groups.flatMap((group) =>
          group.providers.map((provider) => provider.name).filter(Boolean),
        ),
      ).size,
    [groups],
  );
  const missingReferencedProviderCount = useMemo(
    () =>
      new Set(
        groups.flatMap((group) =>
          group.providers
            .map((provider) => provider.name)
            .filter((name) => name && !providerLookup.has(name)),
        ),
      ).size,
    [groups, providerLookup],
  );

  function openCreateDialog() {
    setDialogError(null);
    setEditingGroupName(null);
    setDraft(createProviderGroupDraftInput(null));
    setDialogOpen(true);
  }

  function openEditDialog(group: RuntimeProviderGroupSummary) {
    setDialogError(null);
    setEditingGroupName(group.name);
    setDraft(createProviderGroupDraftInput(group));
    setDialogOpen(true);
  }

  function handleSave() {
    if (draftValidationIssues.length > 0) {
      setDialogError(draftValidationIssues[0].message);
      return;
    }
    const error = onSaveGroup(draft, editingGroupName);
    if (error) {
      setDialogError(error);
      return;
    }
    setDialogOpen(false);
  }

  async function handleCopyGroup(group: RuntimeProviderGroupSummary) {
    try {
      await navigator.clipboard.writeText(buildProviderGroupCreateConfigSnippet(group));
      setCopiedGroupName(group.name);
      window.setTimeout(() => {
        setCopiedGroupName((currentName) =>
          currentName === group.name ? null : currentName,
        );
      }, 1500);
    } catch {
      setCopiedGroupName(null);
    }
  }

  function updateDraftMember(
    index: number,
    patch: Partial<ProviderGroupMemberDraftInput>,
  ) {
    setDraft((current) => ({
      ...current,
      members: current.members.map((item, itemIndex) =>
        itemIndex === index ? { ...item, ...patch } : item,
      ),
    }));
  }

  function autofillPrimaryStandbyRoles() {
    setDraft((current) => {
      let primaryAssigned = current.members.some(
        (member) => member.role.trim() === "primary",
      );
      const fallbackPrimaryIndex = current.members.findIndex(
        (member) => member.enabled && member.name.trim(),
      );
      const resolvedPrimaryIndex =
        fallbackPrimaryIndex >= 0
          ? fallbackPrimaryIndex
          : current.members.findIndex((member) => member.name.trim());

      return {
        ...current,
        members: current.members.map((member, index) => {
          if (!member.name.trim() || member.role.trim()) {
            return member;
          }
          if (!primaryAssigned && index === resolvedPrimaryIndex) {
            primaryAssigned = true;
            return { ...member, role: "primary" };
          }
          return { ...member, role: "standby" };
        }),
      };
    });
  }

  function fillMissingMemberWeights() {
    setDraft((current) => ({
      ...current,
      members: current.members.map((member) =>
        member.name.trim() && !member.weight.trim()
          ? { ...member, weight: "100" }
          : member,
      ),
    }));
  }

  return (
    <>
      <ConfigDomainTable
        title={t("editor.providerGroups.title")}
        titleIcon={RouteIcon}
        description={t("editor.providerGroups.description")}
        items={groups}
        getRowKey={(group) => group.name}
        emptyState={t("editor.providerGroups.emptyState")}
        summary={
          <>
            <ConfigDomainSummaryBadge>
              {t("editor.providerGroups.summary.groups", { count: groups.length })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {t("editor.providerGroups.summary.referencedProviders", {
                count: referencedProviderCount,
              })}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {t("editor.providerGroups.summary.availableProviders", {
                count: providers.length,
              })}
            </ConfigDomainSummaryBadge>
            {missingReferencedProviderCount > 0 ? (
              <ConfigDomainSummaryBadge>
                {t("editor.providerGroups.summary.missingReferences", {
                  count: missingReferencedProviderCount,
                })}
              </ConfigDomainSummaryBadge>
            ) : null}
          </>
        }
        actions={
          <SettingsAddButton
            size="sm"
            label={t("editor.providerGroups.actions.create")}
            onClick={openCreateDialog}
          />
        }
        columns={[
          {
            header: t("editor.providerGroups.columns.name"),
            cell: (group) => (
              <div className="min-w-[11rem]">
                <div className="font-semibold text-[var(--foreground)]">{group.name}</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {group.strategy || t("editor.providerGroups.row.noStrategy")}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.providerGroups.columns.retry"),
            cell: (group) => (
              <div className="min-w-[10rem]">
                <div>
                  {group.maxRetries
                    ? t("editor.providerGroups.row.retryCount", {
                        value: group.maxRetries,
                      })
                    : "--"}
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {group.retryDelay
                    ? t("editor.providerGroups.row.retryDelay", {
                        delay: group.retryDelay,
                      })
                    : t("editor.providerGroups.row.noRetryDelay")}
                </div>
              </div>
            ),
          },
          {
            header: t("editor.providerGroups.columns.failoverTruncation"),
            cell: (group) => (
              <div className="flex flex-wrap gap-2">
                <Badge>
                  {group.failoverEnabled
                    ? t("editor.providerGroups.badges.failoverOn")
                    : t("editor.providerGroups.badges.failoverOff")}
                </Badge>
                <Badge>
                  {group.truncationEnabled
                    ? t("editor.providerGroups.badges.truncationOn")
                    : t("editor.providerGroups.badges.truncationOff")}
                </Badge>
              </div>
            ),
          },
          {
            header: t("editor.providerGroups.columns.members"),
            cell: (group) => {
              const groupMemberSummary = summarizeMembers(group.providers);
              const missingCount = group.providers.filter(
                (provider) => provider.name && !providerLookup.has(provider.name),
              ).length;
              const isGroupPrimaryStandby =
                group.failoverEnabled && group.failoverMode === "primary_standby";

              return (
                <div className="min-w-[16rem]">
                  <div className="flex flex-wrap gap-1.5">
                    {group.providers.length > 0 ? (
                      group.providers.slice(0, 4).map((provider) => (
                        <ProviderReferenceBadge
                          key={`${group.name}-${provider.name}-${provider.role}`}
                          member={provider}
                          provider={providerLookup.get(provider.name)}
                          t={t}
                        />
                      ))
                    ) : (
                      <Badge>{t("editor.providerGroups.row.noMembers")}</Badge>
                    )}
                    {group.providers.length > 4 ? (
                      <Badge>{`+${group.providers.length - 4}`}</Badge>
                    ) : null}
                  </div>
                  <div className="mt-1.5 flex flex-wrap gap-1.5">
                    <Badge>
                      {t("editor.providerGroups.row.enabledRatio", {
                        enabled: String(groupMemberSummary.enabledCount),
                        total: String(groupMemberSummary.namedCount),
                      })}
                    </Badge>
                    {isGroupPrimaryStandby ? (
                      <>
                        <Badge>
                          {t("editor.providerGroups.row.primaryCount", {
                            count: groupMemberSummary.primaryCount,
                          })}
                        </Badge>
                        <Badge>
                          {t("editor.providerGroups.row.standbyCount", {
                            count: groupMemberSummary.standbyCount,
                          })}
                        </Badge>
                        {groupMemberSummary.unsetRoleCount > 0 ? (
                          <Badge>
                            {t("editor.providerGroups.row.unsetRoleCount", {
                              count: groupMemberSummary.unsetRoleCount,
                            })}
                          </Badge>
                        ) : null}
                      </>
                    ) : null}
                    {group.strategy === "weighted" ? (
                      <Badge>
                        {t("editor.providerGroups.row.totalWeight", {
                          weight: groupMemberSummary.totalWeightText,
                        })}
                      </Badge>
                    ) : null}
                  </div>
                  <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                    {missingCount > 0
                      ? t("editor.providerGroups.row.membersMissing", {
                          count: missingCount,
                        })
                      : isGroupPrimaryStandby && groupMemberSummary.primaryCount === 0
                        ? t("editor.providerGroups.row.noPrimary")
                        : isGroupPrimaryStandby && groupMemberSummary.primaryCount > 1
                          ? t("editor.providerGroups.row.multiplePrimary", {
                              count: groupMemberSummary.primaryCount,
                            })
                          : t("editor.providerGroups.row.membersLinked", {
                              count: group.providerCount,
                            })}
                  </div>
                </div>
              );
            },
          },
          {
            header: t("editor.providerGroups.columns.actions"),
            cell: (group) => (
              <SettingsActionGroup compact>
                <SettingsIconActionButton
                  label={t("editor.providerGroups.actions.copyConfig", {
                    name: group.name,
                  })}
                  onClick={() => void handleCopyGroup(group)}
                >
                  {copiedGroupName === group.name ? (
                    <CheckIcon size={13} />
                  ) : (
                    <CopyIcon size={13} />
                  )}
                </SettingsIconActionButton>
                <SettingsIconActionButton
                  label={t("editor.providerGroups.actions.edit", {
                    name: group.name,
                  })}
                  onClick={() => openEditDialog(group)}
                >
                  <PencilIcon size={13} />
                </SettingsIconActionButton>
                <SettingsIconActionButton
                  label={t("editor.providerGroups.actions.delete", {
                    name: group.name,
                  })}
                  onClick={() => onDeleteGroup(group.name)}
                >
                  <Trash2Icon size={13} />
                </SettingsIconActionButton>
              </SettingsActionGroup>
            ),
            align: "right",
            className: "w-[7rem] min-w-[7rem]",
          },
        ]}
      />

      <ConfigDomainDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
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
            onCancel={() => setDialogOpen(false)}
            onConfirm={handleSave}
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
            <div className="space-y-3 rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div>
                  <div className="text-[13px] font-semibold text-[var(--foreground)]">
                    {t("editor.providerGroups.sections.failover")}
                  </div>
                  <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                    {t("editor.providerGroups.sections.failoverHelp")}
                  </div>
                </div>
                <label className="flex items-center gap-2 text-sm text-[var(--foreground)]">
                  <input
                    type="checkbox"
                    className="h-4 w-4 accent-[var(--accent-primary)]"
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

            <div className="space-y-3 rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div>
                  <div className="text-[13px] font-semibold text-[var(--foreground)]">
                    {t("editor.providerGroups.sections.truncation")}
                  </div>
                  <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                    {t("editor.providerGroups.sections.truncationHelp")}
                  </div>
                </div>
                <label className="flex items-center gap-2 text-sm text-[var(--foreground)]">
                  <input
                    type="checkbox"
                    className="h-4 w-4 accent-[var(--accent-primary)]"
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

          <div className="rounded-[0.8rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div>
                <div className="text-[13px] font-semibold text-[var(--foreground)]">
                  {t("editor.providerGroups.sections.members")}
                </div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {t("editor.providerGroups.sections.membersHelp")}
                </div>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                {isPrimaryStandbyMode ? (
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={autofillPrimaryStandbyRoles}
                    disabled={missingRoleCount === 0}
                  >
                    {t("editor.providerGroups.actions.fillRoles")}
                  </Button>
                ) : null}
                {draft.strategy.trim() === "weighted" ? (
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={fillMissingMemberWeights}
                    disabled={weightedMissingWeightCount === 0}
                  >
                    {t("editor.providerGroups.actions.fillWeights")}
                  </Button>
                ) : null}
                <SettingsAddButton
                  variant="secondary"
                  size="sm"
                  label={t("editor.providerGroups.actions.addMember")}
                  onClick={() =>
                    setDraft((current) => ({
                      ...current,
                      members: [
                        ...current.members,
                        createEmptyMemberDraft(providerNames, current.members),
                      ],
                    }))
                  }
                />
              </div>
            </div>

            <SettingsNoticeCard tone="muted" className="mt-3">
              {t("editor.providerGroups.members.poolNotice", {
                count: providers.length,
              })}
            </SettingsNoticeCard>
            <div className="mt-3 flex flex-wrap gap-1.5">
              <Badge>
                {t("editor.providerGroups.members.selectedCount", {
                  count: memberSummary.namedCount,
                })}
              </Badge>
              <Badge>
                {t("editor.providerGroups.members.enabledCount", {
                  count: memberSummary.enabledCount,
                })}
              </Badge>
              {isPrimaryStandbyMode ? (
                <>
                  <Badge>
                    {t("editor.providerGroups.members.primaryCount", {
                      count: memberSummary.primaryCount,
                    })}
                  </Badge>
                  <Badge>
                    {t("editor.providerGroups.members.standbyCount", {
                      count: memberSummary.standbyCount,
                    })}
                  </Badge>
                  {memberSummary.unsetRoleCount > 0 ? (
                    <Badge>
                      {t("editor.providerGroups.members.unsetRoleCount", {
                        count: memberSummary.unsetRoleCount,
                      })}
                    </Badge>
                  ) : null}
                </>
              ) : null}
              {draft.strategy.trim() === "weighted" ? (
                <>
                  <Badge>
                    {t("editor.providerGroups.row.totalWeight", {
                      weight: memberSummary.totalWeightText,
                    })}
                  </Badge>
                  {memberSummary.numericWeightCount > 0 ? (
                    <Badge>
                      {t("editor.providerGroups.members.numericWeightCount", {
                        count: memberSummary.numericWeightCount,
                      })}
                    </Badge>
                  ) : null}
                </>
              ) : null}
            </div>
            {isPrimaryStandbyMode ? (
              <SettingsNoticeCard
                tone={
                  memberSummary.namedCount > 0 &&
                  (memberSummary.primaryCount === 0 ||
                    memberSummary.primaryCount > 1 ||
                    missingRoleCount > 0)
                    ? "warning-soft"
                    : "muted"
                }
                className="mt-3"
              >
                {memberSummary.namedCount === 0
                  ? t("editor.providerGroups.members.primaryStandby.needMembers")
                  : memberSummary.primaryCount === 0
                    ? t("editor.providerGroups.members.primaryStandby.needPrimary")
                    : memberSummary.primaryCount > 1
                      ? t("editor.providerGroups.members.primaryStandby.tooManyPrimary", {
                          count: memberSummary.primaryCount,
                        })
                      : missingRoleCount > 0
                        ? t("editor.providerGroups.members.primaryStandby.missingRoles", {
                            count: missingRoleCount,
                          })
                        : t("editor.providerGroups.members.primaryStandby.ready")}
              </SettingsNoticeCard>
            ) : null}
            {draft.strategy.trim() === "weighted" ? (
              <SettingsNoticeCard tone="warning-soft" className="mt-3">
                {weightedMissingWeightCount > 0
                  ? t("editor.providerGroups.members.weighted.missingWeights", {
                      count: weightedMissingWeightCount,
                    })
                  : t("editor.providerGroups.members.weighted.ready")}
              </SettingsNoticeCard>
            ) : null}

            <div className="mt-3 overflow-auto">
              <table className="min-w-full border-collapse">
                <thead>
                  <tr className="border-b border-[var(--border)] bg-[var(--surface-solid)] text-left">
                    <th className="px-3 py-2 app-text-11 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                      Provider
                    </th>
                    <th className="px-3 py-2 app-text-11 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                      Role
                    </th>
                    <th className="px-3 py-2 app-text-11 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                      Weight
                    </th>
                    <th className="px-3 py-2 app-text-11 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                      Enabled
                    </th>
                    <th className="px-3 py-2 text-right app-text-11 uppercase tracking-[0.12em] text-[var(--muted-foreground)]">
                      {t("editor.providerGroups.columns.actions")}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {draft.members.map((member, index) => (
                    <tr
                      key={`${member.name || "member"}-${index}`}
                      className="border-b border-[var(--border)]/70 align-top last:border-b-0"
                    >
                      <td className="px-3 py-2.5">
                        <Select
                          ariaLabel={t("editor.providerGroups.members.providerAria", {
                            index: String(index + 1),
                          })}
                          value={member.name}
                          onChange={(value) => updateDraftMember(index, { name: value })}
                          options={buildMemberProviderOptions(
                            providers,
                            draft.members,
                            index,
                            t,
                          )}
                          placeholder={
                            providers.length > 0
                              ? t("editor.providerGroups.members.selectProvider")
                              : t("editor.providerGroups.members.noProvider")
                          }
                          className="w-full"
                          triggerClassName={`${getDraftFieldClassName(
                            findDraftIssue(
                              draftValidationIssues,
                              "memberName",
                              index,
                            ) != null,
                          )} min-w-[16rem]`}
                          optionClassName="text-sm"
                        />
                        <FieldIssueText
                          issue={findDraftIssue(draftValidationIssues, "memberName", index)}
                        />
                        <div
                          className={`mt-1 text-xs ${
                            member.name.trim() && !providerLookup.get(member.name)
                              ? "text-[#f5c7b8]"
                              : "text-[var(--muted-foreground)]"
                          }`}
                        >
                          {describeMemberProviderHint(
                            member.name,
                            providerLookup.get(member.name),
                            t,
                          )}
                        </div>
                      </td>
                      <td className="px-3 py-2.5">
                        <Select
                          ariaLabel={t("editor.providerGroups.members.roleAria", {
                            index: String(index + 1),
                          })}
                          value={member.role}
                          onChange={(value) => updateDraftMember(index, { role: value })}
                          options={buildSelectOptionsWithCurrent(
                            providerGroupMemberRoleOptions,
                            member.role,
                            t,
                            { includeEmpty: true },
                          )}
                          placeholder={t("editor.providerGroups.members.selectRole")}
                          className="w-full"
                          triggerClassName={`${editorControlClassName} min-w-[10rem]`}
                          optionClassName="text-sm"
                        />
                      </td>
                      <td className="px-3 py-2.5">
                        <input
                          className={getDraftFieldClassName(
                            findDraftIssue(
                              draftValidationIssues,
                              "memberWeight",
                              index,
                            ) != null,
                          )}
                          value={member.weight}
                          inputMode="decimal"
                          onChange={(event) =>
                            updateDraftMember(index, { weight: event.target.value })
                          }
                          placeholder="100"
                        />
                        <FieldIssueText
                          issue={findDraftIssue(
                            draftValidationIssues,
                            "memberWeight",
                            index,
                          )}
                        />
                      </td>
                      <td className="px-3 py-2.5">
                        <label className="inline-flex items-center gap-2 text-sm text-[var(--foreground)]">
                          <input
                            type="checkbox"
                            className="h-4 w-4 accent-[var(--accent-primary)]"
                            checked={member.enabled}
                            onChange={(event) =>
                              updateDraftMember(index, { enabled: event.target.checked })
                            }
                          />
                          {t("editor.providerGroups.toggle.enabled")}
                        </label>
                      </td>
                      <td className="px-3 py-2.5 text-right">
                        <SettingsIconActionButton
                          label={t("editor.providerGroups.actions.deleteMember", {
                            name: member.name || String(index + 1),
                          })}
                          onClick={() =>
                            setDraft((current) => ({
                              ...current,
                              members: current.members.filter(
                                (_, itemIndex) => itemIndex !== index,
                              ),
                            }))
                          }
                        >
                          <Trash2Icon size={13} />
                        </SettingsIconActionButton>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            {draft.members.length === 0 ? (
              <SettingsEmptyState variant="dashed" className="mt-3 py-4">
                {t("editor.providerGroups.members.empty")}
              </SettingsEmptyState>
            ) : null}
          </div>

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
    </>
  );
}

function ProviderReferenceBadge({
  member,
  provider,
  t,
}: {
  member: RuntimeProviderGroupSummary["providers"][number];
  provider?: RuntimeProviderSummary;
  t: TFunction<"runtimeConfig">;
}) {
  const label = [member.name, provider?.protocol || "", member.role || ""]
    .filter(Boolean)
    .join(" · ");

  return (
    <span
      title={describeMemberProviderHint(member.name, provider, t)}
      className={`inline-flex max-w-full items-center rounded-[0.6rem] border px-2 py-0.5 text-[11px] ${
        provider
          ? "border-[var(--border)] bg-[var(--surface-solid)] text-[var(--muted-foreground)]"
          : "border-[#f59e7d]/30 bg-[#f59e7d]/10 text-[#f5c7b8]"
      }`}
    >
      <span className="truncate">
        {label || member.name || t("editor.providerGroups.row.unnamedMember")}
      </span>
    </span>
  );
}

function FieldIssueText({
  issue,
}: {
  issue: ProviderGroupDraftValidationIssue | null;
}) {
  if (!issue) {
    return null;
  }

  return <div className="mt-1 text-xs text-[#f5c7b8]">{issue.message}</div>;
}

function findDraftIssue(
  issues: ProviderGroupDraftValidationIssue[],
  field: ProviderGroupDraftValidationIssue["field"],
  memberIndex?: number,
) {
  return (
    issues.find(
      (issue) =>
        issue.field === field &&
        (memberIndex === undefined || issue.memberIndex === memberIndex),
    ) ?? null
  );
}

function getDraftFieldClassName(invalid: boolean) {
  return invalid
    ? `${editorControlClassName} border-[#f59e7d]/45 bg-[#f59e7d]/8`
    : editorControlClassName;
}

function buildMemberProviderOptions(
  providers: RuntimeProviderSummary[],
  members: ProviderGroupMemberDraftInput[],
  currentIndex: number,
  t: TFunction<"runtimeConfig">,
) {
  const usedNamesByOthers = new Set(
    members
      .map((member, index) =>
        index === currentIndex ? "" : member.name.trim(),
      )
      .filter(Boolean),
  );
  const options = providers.map((provider) => ({
    value: provider.name,
    label: buildProviderOptionLabel(provider),
    disabled: usedNamesByOthers.has(provider.name),
  }));
  const knownNames = new Set(providers.map((provider) => provider.name));
  const missingNames = Array.from(
    new Set(
      members
        .map((member) => member.name.trim())
        .filter((name) => name && !knownNames.has(name)),
    ),
  );

  return [
    ...options,
    ...missingNames.map((name) => ({
      value: name,
      label: t("editor.providerGroups.members.missingProviderOption", { name }),
      disabled: usedNamesByOthers.has(name),
    })),
  ];
}

function buildSelectOptionsWithCurrent(
  options: ReadonlyArray<{ label: string; value: string }>,
  currentValue: string,
  t: TFunction<"runtimeConfig">,
  config?: {
    includeEmpty?: boolean;
  },
) {
  const normalizedCurrentValue = currentValue.trim();
  const baseOptions = [...options];

  if (
    config?.includeEmpty &&
    !baseOptions.some((option) => option.value === "")
  ) {
    baseOptions.unshift({
      value: "",
      label: t("editor.providerGroups.select.unset"),
    });
  }

  if (
    normalizedCurrentValue &&
    !baseOptions.some((option) => option.value === normalizedCurrentValue)
  ) {
    return [
      {
        value: normalizedCurrentValue,
        label: t("editor.providerGroups.select.currentValue", {
          value: normalizedCurrentValue,
        }),
      },
      ...baseOptions,
    ];
  }

  return baseOptions;
}

function buildProviderOptionLabel(provider: RuntimeProviderSummary) {
  return [
    provider.name,
    provider.protocol || "",
    provider.defaultModel || "",
  ]
    .filter(Boolean)
    .join(" · ");
}

function summarizeMembers(
  members: Array<{
    enabled: boolean;
    name: string;
    role: string;
    weight: string;
  }>,
) {
  let namedCount = 0;
  let enabledCount = 0;
  let primaryCount = 0;
  let standbyCount = 0;
  let unsetRoleCount = 0;
  let numericWeightCount = 0;
  let numericWeightTotal = 0;

  members.forEach((member) => {
    if (!member.name.trim()) {
      return;
    }

    namedCount += 1;
    if (member.enabled) {
      enabledCount += 1;
    }

    const role = member.role.trim();
    if (role === "primary") {
      primaryCount += 1;
    } else if (role === "standby") {
      standbyCount += 1;
    } else {
      unsetRoleCount += 1;
    }

    const numericWeight = Number(member.weight.trim());
    if (Number.isFinite(numericWeight) && member.weight.trim() !== "") {
      numericWeightCount += 1;
      numericWeightTotal += numericWeight;
    }
  });

  return {
    namedCount,
    enabledCount,
    primaryCount,
    standbyCount,
    unsetRoleCount,
    numericWeightCount,
    numericWeightTotal,
    totalWeightText:
      numericWeightCount > 0 ? String(numericWeightTotal) : "--",
  };
}

function describeMemberProviderHint(
  memberName: string,
  provider: RuntimeProviderSummary | undefined,
  t: TFunction<"runtimeConfig">,
) {
  if (!memberName.trim()) {
    return t("editor.providerGroups.hint.selectMember");
  }
  if (!provider) {
    return t("editor.providerGroups.hint.notFound");
  }

  const parts = [
    provider.enabled
      ? t("editor.providerGroups.hint.providerEnabled")
      : t("editor.providerGroups.hint.providerDisabled"),
    provider.protocol,
    provider.defaultModel,
    provider.baseUrl,
  ].filter(Boolean);

  return parts.join(" / ") || t("editor.providerGroups.hint.linked");
}

function createProviderGroupDraftInput(
  group: RuntimeProviderGroupSummary | null,
): ProviderGroupDraftInput {
  if (!group) {
    const defaults = createDefaultProviderGroup("new_group");
    const failover: Record<string, unknown> =
      isConfigRecord(defaults.failover) ? defaults.failover : {};
    const truncation: Record<string, unknown> =
      isConfigRecord(defaults.truncation) ? defaults.truncation : {};

    return {
      name: "",
      strategy: typeof defaults.strategy === "string" ? defaults.strategy : "round_robin",
      maxRetries: stringifyEditableValue(defaults.max_retries),
      retryDelay: stringifyEditableValue(defaults.retry_delay),
      failoverEnabled: Boolean(failover.enabled),
      failoverMode: stringifyEditableValue(failover.mode),
      failoverScope: stringifyEditableValue(failover.scope),
      truncationEnabled: Boolean(truncation.enabled),
      truncationMaxRetries: stringifyEditableValue(truncation.max_retries),
      truncationStrategy: stringifyEditableValue(truncation.strategy),
      truncationStep: stringifyEditableValue(truncation.step),
      members: [],
      extraJson: "{}",
    };
  }

  const extraFields = Object.fromEntries(
    Object.entries(group.raw).filter(([key]) => !KNOWN_PROVIDER_GROUP_KEYS.has(key)),
  );

  return {
    name: group.name,
    strategy: group.strategy,
    maxRetries: group.maxRetries,
    retryDelay: group.retryDelay,
    failoverEnabled: group.failoverEnabled,
    failoverMode: group.failoverMode,
    failoverScope: group.failoverScope,
    truncationEnabled: group.truncationEnabled,
    truncationMaxRetries: group.truncationMaxRetries,
    truncationStrategy: group.truncationStrategy,
    truncationStep: group.truncationStep,
    members: group.providers.map((provider) => ({
      name: provider.name,
      role: provider.role,
      weight: provider.weight,
      enabled: provider.enabled,
    })),
    extraJson: JSON.stringify(extraFields, null, 2),
  };
}

function createEmptyMemberDraft(
  availableProviders: string[],
  members: ProviderGroupMemberDraftInput[],
): ProviderGroupMemberDraftInput {
  const usedNames = new Set(members.map((member) => member.name));
  const nextName =
    availableProviders.find((providerName) => !usedNames.has(providerName)) ??
    availableProviders[0] ??
    "";

  return {
    name: nextName,
    role: "",
    weight: "100",
    enabled: true,
  };
}

function stringifyEditableValue(value: unknown) {
  if (typeof value === "number") {
    return String(value);
  }
  return typeof value === "string" ? value : "";
}
