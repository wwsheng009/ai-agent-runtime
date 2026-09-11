import { Trash2Icon } from "lucide-react";
import { type TFunction } from "i18next";
import { type Dispatch, type SetStateAction, useMemo } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";

import { editorControlClassName } from "../editor-control-class";
import {
  type ProviderGroupDraftInput,
  type ProviderGroupDraftValidationIssue,
  type ProviderGroupMemberDraftInput,
} from "../runtime-provider-groups-domain-form-utils";
import { type RuntimeProviderSummary } from "../runtime-provider-config-utils";
import { SettingsAddButton } from "../settings-add-button";
import { SettingsEmptyState } from "../settings-empty-state";
import { SettingsIconActionButton } from "../settings-action-group";
import { SettingsNoticeCard } from "../settings-notice-card";

import {
  buildMemberProviderOptions,
  buildSelectOptionsWithCurrent,
  createEmptyMemberDraft,
  describeMemberProviderHint,
  findDraftIssue,
  getDraftFieldClassName,
  providerGroupMemberRoleOptions,
  summarizeMembers,
} from "./draft-utils";
import { FieldIssueText } from "./field-parts";

type ProviderGroupMembersSectionProps = {
  draft: ProviderGroupDraftInput;
  draftValidationIssues: ProviderGroupDraftValidationIssue[];
  providerLookup: Map<string, RuntimeProviderSummary>;
  providerNames: string[];
  providers: RuntimeProviderSummary[];
  setDraft: Dispatch<SetStateAction<ProviderGroupDraftInput>>;
  t: TFunction<"runtimeConfig">;
};

export function ProviderGroupMembersSection({
  draft,
  draftValidationIssues,
  providerLookup,
  providerNames,
  providers,
  setDraft,
  t,
}: ProviderGroupMembersSectionProps) {
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
    </>
  );
}
