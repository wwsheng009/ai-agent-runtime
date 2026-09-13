import {
  CheckIcon,
  CopyIcon,
  PencilIcon,
  RouteIcon,
  Trash2Icon,
} from "lucide-react";
import { type TFunction } from "i18next";

import { Badge } from "@/components/ui/badge";

import {
  ConfigDomainSummaryBadge,
  ConfigDomainTable,
} from "../config-domain-table";
import { type RuntimeProviderGroupSummary } from "../runtime-config-domain-utils";
import { type RuntimeProviderSummary } from "../runtime-provider-config-utils";
import {
  SettingsActionGroup,
  SettingsIconActionButton,
} from "../settings-action-group";
import { SettingsAddButton } from "../settings-add-button";

import { summarizeMembers } from "./draft-utils";
import { ProviderReferenceBadge } from "./field-parts";

type ProviderGroupsTableProps = {
  copiedGroupName: string | null;
  groups: RuntimeProviderGroupSummary[];
  missingReferencedProviderCount: number;
  onCopyGroup: (group: RuntimeProviderGroupSummary) => void;
  onCreateGroup: () => void;
  onDeleteGroup: (name: string) => void;
  onEditGroup: (group: RuntimeProviderGroupSummary) => void;
  providerLookup: Map<string, RuntimeProviderSummary>;
  providers: RuntimeProviderSummary[];
  referencedProviderCount: number;
  t: TFunction<"runtimeConfig">;
};

export function ProviderGroupsTable({
  copiedGroupName,
  groups,
  missingReferencedProviderCount,
  onCopyGroup,
  onCreateGroup,
  onDeleteGroup,
  onEditGroup,
  providerLookup,
  providers,
  referencedProviderCount,
  t,
}: ProviderGroupsTableProps) {
  return (
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
            onClick={onCreateGroup}
          />
        }
        columns={[
          {
            header: t("editor.providerGroups.columns.name"),
            cell: (group) => (
              <div className="min-w-[11rem]">
                <div className="font-semibold text-foreground">{group.name}</div>
                <div className="mt-1 text-xs text-muted-foreground">
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
                <div className="mt-1 text-xs text-muted-foreground">
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
                  <div className="mt-1 text-xs text-muted-foreground">
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
                  onClick={() => void onCopyGroup(group)}
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
                  onClick={() => onEditGroup(group)}
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
  );
}
