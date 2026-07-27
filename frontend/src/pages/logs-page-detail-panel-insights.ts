import type { RuntimeLogEntry } from "@/types/runtime";
import type { LogsPageDetailLabels } from "./logs-page-detail-panel.i18n";

export type RuntimeInsightRow = {
  key: string;
  label: string;
  value: string;
  valueClassName?: string;
};

function getObjectLikeValue(value: unknown) {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return undefined;
  }
  return value as Record<string, unknown>;
}

function readRuntimeLogNestedValue(source: unknown, path: string[]) {
  let current: unknown = source;
  for (const segment of path) {
    const objectLike = getObjectLikeValue(current);
    if (!objectLike || !(segment in objectLike)) {
      return undefined;
    }
    current = objectLike[segment];
  }
  return current;
}

function readRuntimeInsightValue(
  sources: Array<Record<string, unknown> | undefined>,
  path: string[],
) {
  for (const source of sources) {
    const value = readRuntimeLogNestedValue(source, path);
    if (value !== undefined) {
      return value;
    }
  }
  return undefined;
}

function hasRuntimeInsightValue(value: unknown) {
  return !(
    value === undefined ||
    value === null ||
    (typeof value === "string" && value.trim() === "")
  );
}

function formatRuntimeInsightValue(value: unknown) {
  if (value === undefined || value === null) {
    return "";
  }
  if (typeof value === "string") {
    return value.trim();
  }
  if (typeof value === "number" || typeof value === "bigint") {
    return String(value);
  }
  if (typeof value === "boolean") {
    return value ? "true" : "false";
  }
  return JSON.stringify(value);
}

function formatRuntimeCacheHitValue(
  value: unknown,
  labels: LogsPageDetailLabels,
) {
  if (typeof value === "boolean") {
    return value ? labels.cacheHitValueHit : labels.cacheHitValueMiss;
  }
  if (typeof value === "number") {
    return value === 0 ? labels.cacheHitValueMiss : labels.cacheHitValueHit;
  }
  if (typeof value === "string") {
    switch (value.trim().toLowerCase()) {
      case "true":
      case "hit":
      case "1":
      case "yes":
        return labels.cacheHitValueHit;
      case "false":
      case "miss":
      case "0":
      case "no":
        return labels.cacheHitValueMiss;
      default:
        return value.trim();
    }
  }
  return formatRuntimeInsightValue(value);
}

function formatRuntimeCacheHitTone(value: unknown) {
  if (typeof value === "boolean") {
    return value
      ? "border-emerald-500/25 bg-emerald-500/10 text-emerald-100"
      : "border-amber-500/25 bg-amber-500/10 text-amber-100";
  }
  if (typeof value === "number") {
    return value === 0
      ? "border-amber-500/25 bg-amber-500/10 text-amber-100"
      : "border-emerald-500/25 bg-emerald-500/10 text-emerald-100";
  }
  if (typeof value === "string") {
    switch (value.trim().toLowerCase()) {
      case "true":
      case "hit":
      case "1":
      case "yes":
        return "border-emerald-500/25 bg-emerald-500/10 text-emerald-100";
      case "false":
      case "miss":
      case "0":
      case "no":
        return "border-amber-500/25 bg-amber-500/10 text-amber-100";
    }
  }
  return "border-[var(--border)] bg-black/10 text-[var(--foreground)]";
}

export function buildRuntimeInsightRows(
  entry: RuntimeLogEntry | null,
  labels: LogsPageDetailLabels,
): RuntimeInsightRow[] {
  if (!entry) {
    return [];
  }

  const sources: Array<Record<string, unknown> | undefined> = [
    entry.fields,
    entry.raw,
  ];
  const rows: RuntimeInsightRow[] = [];

  const cacheHitValue = readRuntimeInsightValue(sources, ["cache_hit"]);
  if (hasRuntimeInsightValue(cacheHitValue)) {
    rows.push({
      key: "cache_hit",
      label: labels.cacheHit,
      value: formatRuntimeCacheHitValue(cacheHitValue, labels),
      valueClassName: `inline-flex items-center rounded-full border px-2 py-0.5 font-mono app-text-10 uppercase tracking-[0.14em] ${formatRuntimeCacheHitTone(cacheHitValue)}`,
    });
  }

  const insightFields: Array<{
    key: string;
    label: string;
    path: string[];
  }> = [
    {
      key: "skill_exposure_mode",
      label: labels.skillExposureMode,
      path: ["skill_exposure", "mode"],
    },
    {
      key: "skill_exposure_final_function_count",
      label: labels.finalFunctionCount,
      path: ["skill_exposure", "final_function_count"],
    },
    {
      key: "skill_exposure_routed_skill_count",
      label: labels.routedSkillCount,
      path: ["skill_exposure", "routed_skill_count"],
    },
    {
      key: "skill_exposure_candidate_count",
      label: labels.candidateCount,
      path: ["skill_exposure", "candidate_count"],
    },
    {
      key: "exposed_function_count",
      label: labels.exposedFunctionCount,
      path: ["exposed_function_count"],
    },
  ];

  for (const field of insightFields) {
    const value = readRuntimeInsightValue(sources, field.path);
    if (!hasRuntimeInsightValue(value)) {
      continue;
    }
    rows.push({
      key: field.key,
      label: field.label,
      value: formatRuntimeInsightValue(value),
    });
  }

  return rows;
}
