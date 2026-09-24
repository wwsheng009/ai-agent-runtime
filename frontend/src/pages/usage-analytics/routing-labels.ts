// P0-2 拆分：路由可观测面板的 i18n 归一表与标签函数（原 routing-observability-panel.tsx
// L30-L228）。原文件因 react-refresh「组件文件不导出 helper」约定而受限于单文件；
// 拆分后由本纯 TS 模块承载，组件文件只保留渲染逻辑。

import type { AnalyticsRouteBucket } from "@/types/runtime";
import type { TFunction } from "i18next";

// 归一表：后端取值 → i18n key。
const scopeKeys = {
  main_agent: "observability.routing.scopeMain",
  subagent: "observability.routing.scopeSubagent",
} as const;

const kindKeys = {
  applied: "observability.routing.kindApplied",
  cleared: "observability.routing.kindCleared",
  warning: "observability.routing.kindWarning",
} as const;

const sourceKeys = {
  explicit: "observability.routing.difficultySources.explicit",
  explicit_promoted: "observability.routing.difficultySources.explicitPromoted",
  heuristic: "observability.routing.difficultySources.heuristic",
  role: "observability.routing.difficultySources.role",
  // P4：profile source 新值 task_type_override；历史值 role_override 继续映射。
  role_override: "observability.routing.difficultySources.role",
  task_type_override: "observability.routing.difficultySources.taskTypeOverride",
  read_only: "observability.routing.difficultySources.readOnly",
  default: "observability.routing.difficultySources.default",
} as const;

// 难度来源（difficulty_source）归一表：后端 resolver.go 产出的取值 + P4 新增的
// task_type_floor / task_type_downgrade，与 route_source 的 sourceKeys 是两套
// 词表，勿混用（后者含 parent_inherit 等）。
const difficultySourceKeys = {
  explicit: "observability.routing.difficultySources.explicit",
  explicit_promoted: "observability.routing.difficultySources.explicitPromoted",
  inferred: "observability.routing.difficultySources.inferred",
  default: "observability.routing.difficultySources.default",
  task_type_floor: "observability.routing.difficultySources.taskTypeFloor",
  task_type_downgrade: "observability.routing.difficultySources.taskTypeDowngrade",
} as const;

// 任务类型（12 类封闭枚举）归一表：与配置编辑器 task_types 键集一致。
const taskTypeKeys = {
  config: "observability.routing.taskTypes.config",
  explore: "observability.routing.taskTypes.explore",
  generate: "observability.routing.taskTypes.generate",
  implement: "observability.routing.taskTypes.implement",
  integration: "observability.routing.taskTypes.integration",
  migrate: "observability.routing.taskTypes.migrate",
  modify: "observability.routing.taskTypes.modify",
  refactor: "observability.routing.taskTypes.refactor",
  security: "observability.routing.taskTypes.security",
  test: "observability.routing.taskTypes.test",
  understand: "observability.routing.taskTypes.understand",
  verify: "observability.routing.taskTypes.verify",
} as const;

// 护栏告警 token 归一表：精确 token + 「prefix:value」前缀 token（用 {{value}} 插值）。
// 未收录 token 原样回显，后端扩词表时页面不空白。
const warningKeys = {
  difficulty_missing_defaulted: "observability.routing.warningLabels.difficulty_missing_defaulted",
  difficulty_invalid_defaulted: "observability.routing.warningLabels.difficulty_invalid_defaulted",
  difficulty_promoted_by_heuristic: "observability.routing.warningLabels.difficulty_promoted_by_heuristic",
  difficulty_promoted_over_explicit: "observability.routing.warningLabels.difficulty_promoted_over_explicit",
  difficulty_promotion_warn_only: "observability.routing.warningLabels.difficulty_promotion_warn_only",
  max_expert_concurrency_zero_means_unlimited:
    "observability.routing.warningLabels.max_expert_concurrency_zero_means_unlimited",
} as const;

const warningPrefixKeys = {
  difficulty_promoted_by_keyword: "observability.routing.warningLabels.difficulty_promoted_by_keyword",
  difficulty_promoted_by_role: "observability.routing.warningLabels.difficulty_promoted_by_role",
  difficulty_floor_by_task_type: "observability.routing.warningLabels.difficulty_floor_by_task_type",
  difficulty_downgraded_by_task_type: "observability.routing.warningLabels.difficulty_downgraded_by_task_type",
  task_type_unknown: "observability.routing.warningLabels.task_type_unknown",
} as const;

const reasonKeys = {
  resolved: "observability.routing.reasons.resolved",
} as const;

export const routeFlagKeys = {
  routeChanged: "observability.routing.flags.routeChanged",
  routeUnchanged: "observability.routing.flags.routeUnchanged",
  fallbackUsed: "observability.routing.flags.fallbackUsed",
  fallbackUnused: "observability.routing.flags.fallbackUnused",
} as const;

type ScopeKey = (typeof scopeKeys)[keyof typeof scopeKeys];
type KindKey = (typeof kindKeys)[keyof typeof kindKeys];
type SourceKey = (typeof sourceKeys)[keyof typeof sourceKeys];
type DifficultySourceKey = (typeof difficultySourceKeys)[keyof typeof difficultySourceKeys];
type TaskTypeKey = (typeof taskTypeKeys)[keyof typeof taskTypeKeys];
type WarningKey = (typeof warningKeys)[keyof typeof warningKeys];
type WarningPrefixKey = (typeof warningPrefixKeys)[keyof typeof warningPrefixKeys];
type ReasonKey = (typeof reasonKeys)[keyof typeof reasonKeys];
export type RouteFlagKey = (typeof routeFlagKeys)[keyof typeof routeFlagKeys];

export function scopeLabel(t: TFunction<"usageAnalytics">, raw?: string): string {
  const normalized = (raw ?? "").trim().toLowerCase();
  const key = scopeKeys[normalized as keyof typeof scopeKeys] as ScopeKey | undefined;
  if (key) return t(key);
  return (raw ?? "").trim() || t("observability.routing.scopeUnknown");
}

export function kindLabel(t: TFunction<"usageAnalytics">, raw?: string): string {
  const normalized = (raw ?? "").trim().toLowerCase();
  const key = kindKeys[normalized as keyof typeof kindKeys] as KindKey | undefined;
  if (key) return t(key);
  return (raw ?? "").trim() || t("observability.routing.kindUnknown");
}

export function sourceLabel(t: TFunction<"usageAnalytics">, raw?: string): string {
  const normalized = (raw ?? "").trim().toLowerCase();
  const key = sourceKeys[normalized as keyof typeof sourceKeys] as SourceKey | undefined;
  if (key) return t(key);
  return (raw ?? "").trim() || t("observability.routing.difficultySources.unknown");
}

// 难度来源标签（词表见 difficultySourceKeys）：未记录的取值回显原值。
export function difficultySourceLabel(t: TFunction<"usageAnalytics">, raw?: string): string {
  const normalized = (raw ?? "").trim().toLowerCase();
  const key = difficultySourceKeys[normalized as keyof typeof difficultySourceKeys] as DifficultySourceKey | undefined;
  if (key) return t(key);
  return (raw ?? "").trim() || t("observability.routing.difficultySources.unknown");
}

// 任务类型标签（词表见 taskTypeKeys）：12 类封闭枚举走 i18n，未知取值原样回显。
export function taskTypeLabel(t: TFunction<"usageAnalytics">, raw?: string): string {
  const normalized = (raw ?? "").trim().toLowerCase();
  const key = taskTypeKeys[normalized as keyof typeof taskTypeKeys] as TaskTypeKey | undefined;
  if (key) return t(key);
  return (raw ?? "").trim() || t("observability.routing.flags.notRecorded");
}

// 护栏告警 token → 标签：精确 token 优先；否则按 ":" 拆前缀做 {{value}} 插值；
// 两者都不命中时回显原始 token（保留排障可读性）。
export function warningLabel(t: TFunction<"usageAnalytics">, raw?: string): string {
  const token = (raw ?? "").trim();
  if (!token) return t("observability.routing.flags.notRecorded");
  const exact = warningKeys[token as keyof typeof warningKeys] as WarningKey | undefined;
  if (exact) return t(exact);
  const separator = token.indexOf(":");
  if (separator > 0) {
    const prefix = token.slice(0, separator).trim().toLowerCase();
    const key = warningPrefixKeys[prefix as keyof typeof warningPrefixKeys] as WarningPrefixKey | undefined;
    if (key) return t(key, { value: token.slice(separator + 1).trim() });
  }
  return token;
}

export function reasonLabel(t: TFunction<"usageAnalytics">, raw?: string): string {
  const normalized = (raw ?? "").trim().toLowerCase();
  const key = reasonKeys[normalized as keyof typeof reasonKeys] as ReasonKey | undefined;
  if (key) return t(key);
  return (raw ?? "").trim() || t("observability.routing.kindUnknown");
}

// 维度桶里没有归一表可用的取值（provider / model / difficulty / role / 告警词）直接回显；
// 空值统一显示「未记录」，避免图上出现无名桶。
export function rawOrNotRecorded(t: TFunction<"usageAnalytics">, raw?: string): string {
  return (raw ?? "").trim() || t("observability.routing.flags.notRecorded");
}

export function kindTone(kind: string): string {
  switch ((kind ?? "").trim().toLowerCase()) {
    case "applied":
      return "border-analytics-success-border bg-analytics-success-soft text-analytics-success";
    case "warning":
      return "border-analytics-warning-border bg-analytics-warning-soft text-analytics-warning";
    default:
      return "";
  }
}

// 三态布尔 → 文本：undefined 表示事件未携带该字段，显示「未记录」。
export function triStateLabel(
  t: TFunction<"usageAnalytics">,
  value: boolean | undefined,
  trueKey: RouteFlagKey,
  falseKey: RouteFlagKey,
): string {
  if (value === true) return t(trueKey);
  if (value === false) return t(falseKey);
  return t("observability.routing.flags.notRecorded");
}

export function bucketsToEntries(
  t: TFunction<"usageAnalytics">,
  buckets: AnalyticsRouteBucket[] | undefined,
  label: (t: TFunction<"usageAnalytics">, raw?: string) => string,
) {
  return (buckets ?? []).map((bucket) => ({
    key: bucket.key,
    count: bucket.count,
    label: label(t, bucket.key),
    detail: bucket.route_changed > 0 || bucket.fallback_used > 0
      ? t("observability.routing.metricDetail.appliedCleared", {
          applied: String(bucket.route_changed),
          cleared: String(bucket.fallback_used),
        })
      : undefined,
  }));
}
