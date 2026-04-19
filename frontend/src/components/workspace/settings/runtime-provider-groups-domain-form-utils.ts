import { isConfigRecord } from "./runtime-provider-config-utils";

export type ProviderGroupMemberDraftInput = {
  enabled: boolean;
  name: string;
  role: string;
  weight: string;
};

export type ProviderGroupDraftInput = {
  extraJson: string;
  failoverEnabled: boolean;
  failoverMode: string;
  failoverScope: string;
  maxRetries: string;
  members: ProviderGroupMemberDraftInput[];
  name: string;
  retryDelay: string;
  strategy: string;
  truncationEnabled: boolean;
  truncationMaxRetries: string;
  truncationStep: string;
  truncationStrategy: string;
};

export type ProviderGroupDraftValidationIssue = {
  field:
    | "memberName"
    | "maxRetries"
    | "retryDelay"
    | "truncationMaxRetries"
    | "truncationStep"
    | "memberWeight";
  memberIndex?: number;
  message: string;
};

export function buildProviderGroupRecordFromDraft(
  draft: ProviderGroupDraftInput,
): { error: string | null; record: Record<string, unknown> | null } {
  const extraFields = parseJsonRecord(
    draft.extraJson,
    "`扩展字段 JSON` 必须是 JSON 对象。",
  );
  if (!extraFields.record) {
    return { error: extraFields.error, record: null };
  }

  const name = draft.name.trim();
  if (!name) {
    return { error: "Provider group 名称不能为空。", record: null };
  }

  const validationIssues = validateProviderGroupDraft(draft);
  if (validationIssues.length > 0) {
    return {
      error: validationIssues[0].message,
      record: null,
    };
  }

  const members = draft.members
    .map((member) => buildProviderGroupMemberRecord(member))
    .filter((member): member is Record<string, unknown> => member !== null);

  if (members.length === 0) {
    return { error: "Provider group 至少需要一个有效成员。", record: null };
  }

  const record: Record<string, unknown> = {
    ...extraFields.record,
    name,
    strategy: draft.strategy.trim() || "round_robin",
    failover: {
      enabled: draft.failoverEnabled,
      ...buildOptionalTextRecord({
        mode: draft.failoverMode,
        scope: draft.failoverScope,
      }),
    },
    truncation: {
      enabled: draft.truncationEnabled,
      ...buildOptionalScalarRecord({
        max_retries: draft.truncationMaxRetries,
        strategy: draft.truncationStrategy,
        step: draft.truncationStep,
      }),
    },
    providers: members,
  };

  Object.assign(
    record,
    buildOptionalScalarRecord({
      max_retries: draft.maxRetries,
      retry_delay: draft.retryDelay,
    }),
  );

  return {
    error: null,
    record,
  };
}

export function validateProviderGroupDraft(
  draft: ProviderGroupDraftInput,
): ProviderGroupDraftValidationIssue[] {
  const issues: ProviderGroupDraftValidationIssue[] = [];
  const memberNameCounts = new Map<string, number>();

  draft.members.forEach((member) => {
    const name = member.name.trim();
    if (!name) {
      return;
    }
    memberNameCounts.set(name, (memberNameCounts.get(name) ?? 0) + 1);
  });

  if (!isNonNegativeIntegerLike(draft.maxRetries)) {
    issues.push({
      field: "maxRetries",
      message: "`max_retries` 需要是非负整数，或使用环境变量模板。",
    });
  }

  if (!isRetryDelayLike(draft.retryDelay)) {
    issues.push({
      field: "retryDelay",
      message: "`retry_delay` 需要是 Go duration 风格值，例如 `1s`、`500ms`、`1m30s`。",
    });
  }

  if (!isNonNegativeIntegerLike(draft.truncationMaxRetries)) {
    issues.push({
      field: "truncationMaxRetries",
      message: "`truncation.max_retries` 需要是非负整数，或使用环境变量模板。",
    });
  }

  if (!isNonNegativeNumberLike(draft.truncationStep)) {
    issues.push({
      field: "truncationStep",
      message: "`truncation.step` 需要是非负数字，或使用环境变量模板。",
    });
  }

  draft.members.forEach((member, index) => {
    const memberName = member.name.trim();
    if (memberName && (memberNameCounts.get(memberName) ?? 0) > 1) {
      issues.push({
        field: "memberName",
        memberIndex: index,
        message: `成员 #${index + 1} 的 provider 与其它成员重复，同一个 group 中不能重复引用同一 provider。`,
      });
    }

    if (draft.strategy.trim() === "weighted" && !member.weight.trim()) {
      issues.push({
        field: "memberWeight",
        memberIndex: index,
        message: `成员 #${index + 1} 在 \`weighted\` 策略下必须填写 \`weight\`。`,
      });
      return;
    }

    if (member.weight.trim() && !isPositiveNumberLike(member.weight)) {
      issues.push({
        field: "memberWeight",
        memberIndex: index,
        message: `成员 #${index + 1} 的 \`weight\` 需要是大于 0 的数字，或使用环境变量模板。`,
      });
    }
  });

  return issues;
}

function buildProviderGroupMemberRecord(
  member: ProviderGroupMemberDraftInput,
): Record<string, unknown> | null {
  const name = member.name.trim();
  if (!name) {
    return null;
  }

  return {
    name,
    enabled: member.enabled,
    ...buildOptionalTextRecord({ role: member.role }),
    ...buildOptionalScalarRecord({ weight: member.weight }),
  };
}

function buildOptionalTextRecord(
  entries: Record<string, string>,
): Record<string, string> {
  return Object.fromEntries(
    Object.entries(entries).flatMap(([key, value]) => {
      const trimmed = value.trim();
      return trimmed ? [[key, trimmed]] : [];
    }),
  );
}

function buildOptionalScalarRecord(
  entries: Record<string, string>,
): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries(entries).flatMap(([key, value]) => {
      const parsed = parseEditableScalar(value);
      return parsed === undefined ? [] : [[key, parsed]];
    }),
  );
}

function parseEditableScalar(value: string) {
  const trimmed = value.trim();
  if (!trimmed) {
    return undefined;
  }
  if (/^-?\d+(?:\.\d+)?$/.test(trimmed)) {
    return Number(trimmed);
  }
  return trimmed;
}

function isNonNegativeIntegerLike(value: string) {
  const trimmed = value.trim();
  if (!trimmed) {
    return true;
  }
  return isEnvTemplate(trimmed) || /^\d+$/.test(trimmed);
}

function isNonNegativeNumberLike(value: string) {
  const trimmed = value.trim();
  if (!trimmed) {
    return true;
  }
  return (
    isEnvTemplate(trimmed) ||
    (/^(?:\d+|\d+\.\d+|\d*\.\d+)$/.test(trimmed) && Number(trimmed) >= 0)
  );
}

function isPositiveNumberLike(value: string) {
  const trimmed = value.trim();
  if (!trimmed) {
    return true;
  }
  return (
    isEnvTemplate(trimmed) ||
    (/^(?:\d+|\d+\.\d+|\d*\.\d+)$/.test(trimmed) && Number(trimmed) > 0)
  );
}

function isRetryDelayLike(value: string) {
  const trimmed = value.trim();
  if (!trimmed) {
    return true;
  }
  return (
    isEnvTemplate(trimmed) ||
    /^(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))+$/.test(trimmed)
  );
}

function isEnvTemplate(value: string) {
  return /^\$\{.+\}$/.test(value);
}

function parseJsonRecord(
  value: string,
  errorMessage: string,
): { error: string | null; record: Record<string, unknown> | null } {
  const trimmed = value.trim();
  if (!trimmed) {
    return { error: null, record: {} };
  }

  try {
    const parsed = JSON.parse(trimmed) as unknown;
    if (!isConfigRecord(parsed)) {
      return { error: errorMessage, record: null };
    }
    return { error: null, record: parsed };
  } catch {
    return { error: errorMessage, record: null };
  }
}
