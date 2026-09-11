import { type TFunction } from "i18next";

import {
  createDefaultProviderGroup,
  type RuntimeProviderGroupSummary,
} from "../runtime-config-domain-utils";
import {
  type ProviderGroupDraftInput,
  type ProviderGroupDraftValidationIssue,
  type ProviderGroupMemberDraftInput,
} from "../runtime-provider-groups-domain-form-utils";
import {
  isConfigRecord,
  type RuntimeProviderSummary,
} from "../runtime-provider-config-utils";
import { editorControlClassName } from "../editor-control-class";

const KNOWN_PROVIDER_GROUP_KEYS = new Set([
  "name",
  "strategy",
  "max_retries",
  "retry_delay",
  "failover",
  "truncation",
  "providers",
]);

export const providerGroupStrategyOptions = [
  { value: "round_robin", label: "round_robin" },
  { value: "health", label: "health" },
  { value: "random", label: "random" },
  { value: "weighted", label: "weighted" },
] as const;
export const providerGroupFailoverModeOptions = [
  { value: "primary_standby", label: "primary_standby" },
] as const;
export const providerGroupFailoverScopeOptions = [
  { value: "model_key", label: "model_key" },
] as const;
export const providerGroupTruncationStrategyOptions = [
  { value: "percentage", label: "percentage" },
] as const;
export const providerGroupMemberRoleOptions = [
  { value: "primary", label: "primary" },
  { value: "standby", label: "standby" },
] as const;

export function findDraftIssue(
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

export function getDraftFieldClassName(invalid: boolean) {
  return invalid
    ? `${editorControlClassName} border-[#f59e7d]/45 bg-[#f59e7d]/8`
    : editorControlClassName;
}

export function buildMemberProviderOptions(
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

export function buildSelectOptionsWithCurrent(
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

export function summarizeMembers(
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

export function describeMemberProviderHint(
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

export function createProviderGroupDraftInput(
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

export function createEmptyMemberDraft(
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
