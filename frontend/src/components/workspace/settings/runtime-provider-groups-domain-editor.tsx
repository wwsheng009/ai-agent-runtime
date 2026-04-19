import {
  CheckIcon,
  CopyIcon,
  PencilIcon,
  RouteIcon,
  Trash2Icon,
} from "lucide-react";
import { useMemo, useState } from "react";

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
        title="Provider Groups"
        titleIcon={RouteIcon}
        description="用列表管理 provider group，用弹出表单维护重试、故障切换、截断策略和成员列表。"
        items={groups}
        getRowKey={(group) => group.name}
        emptyState="当前还没有 provider group，可直接新建一组路由池配置。"
        summary={
          <>
            <ConfigDomainSummaryBadge>{`${groups.length} 个分组`}</ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {`${referencedProviderCount} 个 provider 已被引用`}
            </ConfigDomainSummaryBadge>
            <ConfigDomainSummaryBadge>
              {`${providers.length} 个 provider 可选`}
            </ConfigDomainSummaryBadge>
            {missingReferencedProviderCount > 0 ? (
              <ConfigDomainSummaryBadge>
                {`${missingReferencedProviderCount} 个引用待修复`}
              </ConfigDomainSummaryBadge>
            ) : null}
          </>
        }
        actions={
          <SettingsAddButton size="sm" label="新建分组" onClick={openCreateDialog} />
        }
        columns={[
          {
            header: "名称",
            cell: (group) => (
              <div className="min-w-[11rem]">
                <div className="font-semibold text-[var(--foreground)]">{group.name}</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {group.strategy || "未设 strategy"}
                </div>
              </div>
            ),
          },
          {
            header: "重试策略",
            cell: (group) => (
              <div className="min-w-[10rem]">
                <div>{group.maxRetries ? `${group.maxRetries} 次` : "--"}</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  {group.retryDelay ? `间隔 ${group.retryDelay}` : "未设 retry_delay"}
                </div>
              </div>
            ),
          },
          {
            header: "故障切换 / 截断",
            cell: (group) => (
              <div className="flex flex-wrap gap-2">
                <Badge>{group.failoverEnabled ? "故障切换开" : "故障切换关"}</Badge>
                <Badge>{group.truncationEnabled ? "截断开" : "截断关"}</Badge>
              </div>
            ),
          },
          {
            header: "成员",
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
                        />
                      ))
                    ) : (
                      <Badge>无成员</Badge>
                    )}
                    {group.providers.length > 4 ? (
                      <Badge>{`+${group.providers.length - 4}`}</Badge>
                    ) : null}
                  </div>
                  <div className="mt-1.5 flex flex-wrap gap-1.5">
                    <Badge>{`${groupMemberSummary.enabledCount}/${groupMemberSummary.namedCount} 启用`}</Badge>
                    {isGroupPrimaryStandby ? (
                      <>
                        <Badge>{`${groupMemberSummary.primaryCount} primary`}</Badge>
                        <Badge>{`${groupMemberSummary.standbyCount} standby`}</Badge>
                        {groupMemberSummary.unsetRoleCount > 0 ? (
                          <Badge>{`${groupMemberSummary.unsetRoleCount} 未设 role`}</Badge>
                        ) : null}
                      </>
                    ) : null}
                    {group.strategy === "weighted" ? (
                      <Badge>{`总权重 ${groupMemberSummary.totalWeightText}`}</Badge>
                    ) : null}
                  </div>
                  <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                    {missingCount > 0
                      ? `${missingCount} 个成员未在当前 provider 列表中找到`
                      : isGroupPrimaryStandby && groupMemberSummary.primaryCount === 0
                        ? "当前分组没有 primary 成员"
                        : isGroupPrimaryStandby && groupMemberSummary.primaryCount > 1
                          ? `当前分组有 ${groupMemberSummary.primaryCount} 个 primary`
                      : `${group.providerCount} 个成员，均已关联到当前 provider 列表`}
                  </div>
                </div>
              );
            },
          },
          {
            header: "操作",
            cell: (group) => (
              <SettingsActionGroup compact>
                <SettingsIconActionButton
                  label={`复制 ${group.name} 配置`}
                  onClick={() => void handleCopyGroup(group)}
                >
                  {copiedGroupName === group.name ? (
                    <CheckIcon size={13} />
                  ) : (
                    <CopyIcon size={13} />
                  )}
                </SettingsIconActionButton>
                <SettingsIconActionButton
                  label={`编辑 ${group.name}`}
                  onClick={() => openEditDialog(group)}
                >
                  <PencilIcon size={13} />
                </SettingsIconActionButton>
                <SettingsIconActionButton
                  label={`删除 ${group.name}`}
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
        title={editingGroupName ? `编辑 Provider Group: ${editingGroupName}` : "新建 Provider Group"}
        description="基础路由策略、故障切换、截断参数和组成员都在这里维护，未纳入专用字段的扩展配置可通过 JSON 保留。"
        footer={
          <SettingsDialogFooter
            buttonSize="sm"
            note="保存后建议回到预览区检查 `provider_groups` diff。"
            confirmLabel="保存分组"
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
                <div className="font-medium">当前草稿还有待修正的字段：</div>
                {draftValidationIssues.slice(0, 4).map((issue) => (
                  <div key={`${issue.field}-${issue.memberIndex ?? "root"}-${issue.message}`}>
                    {issue.message}
                  </div>
                ))}
                {draftValidationIssues.length > 4 ? (
                  <div>{`还有 ${draftValidationIssues.length - 4} 条未展示。`}</div>
                ) : null}
              </div>
            </SettingsNoticeCard>
          ) : null}

          <div className="grid gap-3 xl:grid-cols-2">
            <ConfigFormField label="名称" description="provider_groups 中的唯一标识，用于路由 group 引用。">
              <input
                className={editorControlClassName}
                value={draft.name}
                onChange={(event) =>
                  setDraft((current) => ({ ...current, name: event.target.value }))
                }
                placeholder="openai_group"
              />
            </ConfigFormField>
            <ConfigFormField label="strategy" description="常见值包括 round_robin、health、random 等。">
              <Select
                ariaLabel="选择 Provider Group strategy"
                value={draft.strategy}
                onChange={(value) =>
                  setDraft((current) => ({ ...current, strategy: value }))
                }
                options={buildSelectOptionsWithCurrent(
                  providerGroupStrategyOptions,
                  draft.strategy,
                )}
                placeholder="选择 strategy"
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
                  <div className="text-[13px] font-semibold text-[var(--foreground)]">故障切换</div>
                  <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                    配置 primary/standby 之类的切换策略。
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
                  启用
                </label>
              </div>

              <div className="grid gap-3">
                <ConfigFormField label="failover.mode">
                  <Select
                    ariaLabel="选择 failover.mode"
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
                    )}
                    placeholder="选择 failover.mode"
                    className="w-full"
                    triggerClassName={editorControlClassName}
                    optionClassName="text-sm"
                  />
                </ConfigFormField>
                <ConfigFormField label="failover.scope">
                  <Select
                    ariaLabel="选择 failover.scope"
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
                    )}
                    placeholder="选择 failover.scope"
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
                  <div className="text-[13px] font-semibold text-[var(--foreground)]">截断重试</div>
                  <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                    处理超长上下文时的回退策略。
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
                  启用
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
                      ariaLabel="选择 truncation.strategy"
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
                      )}
                      placeholder="选择 truncation.strategy"
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
                <div className="text-[13px] font-semibold text-[var(--foreground)]">成员列表</div>
                <div className="mt-1 text-xs text-[var(--muted-foreground)]">
                  直接引用当前 provider 列表；选择后会显示协议、默认模型和启用状态。
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
                    补齐角色
                  </Button>
                ) : null}
                {draft.strategy.trim() === "weighted" ? (
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={fillMissingMemberWeights}
                    disabled={weightedMissingWeightCount === 0}
                  >
                    补齐权重
                  </Button>
                ) : null}
                <SettingsAddButton
                  variant="secondary"
                  size="sm"
                  label="添加成员"
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
              当前 provider 池共 {providers.length} 个可选项。
              如果 group 中引用了已经删除的 provider，这里会继续显示，但标记为“未在当前列表中找到”。
            </SettingsNoticeCard>
            <div className="mt-3 flex flex-wrap gap-1.5">
              <Badge>{`${memberSummary.namedCount} 个已选成员`}</Badge>
              <Badge>{`${memberSummary.enabledCount} 个启用`}</Badge>
              {isPrimaryStandbyMode ? (
                <>
                  <Badge>{`${memberSummary.primaryCount} 个 primary`}</Badge>
                  <Badge>{`${memberSummary.standbyCount} 个 standby`}</Badge>
                  {memberSummary.unsetRoleCount > 0 ? (
                    <Badge>{`${memberSummary.unsetRoleCount} 个未设 role`}</Badge>
                  ) : null}
                </>
              ) : null}
              {draft.strategy.trim() === "weighted" ? (
                <>
                  <Badge>{`总权重 ${memberSummary.totalWeightText}`}</Badge>
                  {memberSummary.numericWeightCount > 0 ? (
                    <Badge>{`${memberSummary.numericWeightCount} 个数值权重`}</Badge>
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
                  ? "当前 failover.mode 为 `primary_standby`，请先添加成员。"
                  : memberSummary.primaryCount === 0
                    ? "当前 failover.mode 为 `primary_standby`，但还没有 primary 成员。"
                    : memberSummary.primaryCount > 1
                      ? `当前 failover.mode 为 \`primary_standby\`，已设置 ${memberSummary.primaryCount} 个 primary，建议收敛为 1 个。`
                      : missingRoleCount > 0
                        ? `当前 failover.mode 为 \`primary_standby\`，还有 ${missingRoleCount} 个成员未设置 role，可用“补齐角色”快速补全。`
                        : "当前 failover.mode 为 `primary_standby`，成员角色已经补齐。"}
              </SettingsNoticeCard>
            ) : null}
            {draft.strategy.trim() === "weighted" ? (
              <SettingsNoticeCard tone="warning-soft" className="mt-3">
                {weightedMissingWeightCount > 0
                  ? `当前 strategy 为 \`weighted\`，还有 ${weightedMissingWeightCount} 个成员缺少 weight，可用“补齐权重”快速填成 100。`
                  : "当前 strategy 为 `weighted`，所有成员都已经填写了 weight。"}
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
                      操作
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
                          ariaLabel={`选择 Provider Group 成员 ${index + 1} 的 provider`}
                          value={member.name}
                          onChange={(value) => updateDraftMember(index, { name: value })}
                          options={buildMemberProviderOptions(providers, draft.members, index)}
                          placeholder={providers.length > 0 ? "选择 provider" : "暂无 provider"}
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
                          {describeMemberProviderHint(member.name, providerLookup.get(member.name))}
                        </div>
                      </td>
                      <td className="px-3 py-2.5">
                        <Select
                          ariaLabel={`选择成员 ${index + 1} 的 role`}
                          value={member.role}
                          onChange={(value) => updateDraftMember(index, { role: value })}
                          options={buildSelectOptionsWithCurrent(
                            providerGroupMemberRoleOptions,
                            member.role,
                            { includeEmpty: true },
                          )}
                          placeholder="选择 role"
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
                          启用
                        </label>
                      </td>
                      <td className="px-3 py-2.5 text-right">
                        <SettingsIconActionButton
                          label={`删除成员 ${member.name || index + 1}`}
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
                这个分组还没有成员，保存前至少需要添加一个 provider。
              </SettingsEmptyState>
            ) : null}
          </div>

          <ConfigFormField
            label="扩展字段 JSON"
            description="保留 health_check 等未纳入专用表单的 root 级字段。"
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
}: {
  member: RuntimeProviderGroupSummary["providers"][number];
  provider?: RuntimeProviderSummary;
}) {
  const label = [member.name, provider?.protocol || "", member.role || ""]
    .filter(Boolean)
    .join(" · ");

  return (
    <span
      title={describeMemberProviderHint(member.name, provider)}
      className={`inline-flex max-w-full items-center rounded-[0.6rem] border px-2 py-0.5 text-[11px] ${
        provider
          ? "border-[var(--border)] bg-[var(--surface-solid)] text-[var(--muted-foreground)]"
          : "border-[#f59e7d]/30 bg-[#f59e7d]/10 text-[#f5c7b8]"
      }`}
    >
      <span className="truncate">{label || member.name || "未命名成员"}</span>
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
      label: `${name} · 当前 group 已引用，但 provider 列表中不存在`,
      disabled: usedNamesByOthers.has(name),
    })),
  ];
}

function buildSelectOptionsWithCurrent(
  options: ReadonlyArray<{ label: string; value: string }>,
  currentValue: string,
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
      label: "未设置",
    });
  }

  if (
    normalizedCurrentValue &&
    !baseOptions.some((option) => option.value === normalizedCurrentValue)
  ) {
    return [
      {
        value: normalizedCurrentValue,
        label: `${normalizedCurrentValue} · 当前值`,
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
  provider?: RuntimeProviderSummary,
) {
  if (!memberName.trim()) {
    return "从当前 provider 列表选择一个成员。";
  }
  if (!provider) {
    return "该名称未在当前 provider 列表中找到，请检查 provider 配置或重新选择。";
  }

  const parts = [
    provider.enabled ? "provider 已启用" : "provider 已禁用",
    provider.protocol,
    provider.defaultModel,
    provider.baseUrl,
  ].filter(Boolean);

  return parts.join(" / ") || "已关联到当前 provider。";
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
