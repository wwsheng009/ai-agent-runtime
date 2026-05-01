import { CheckIcon, CopyIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import {
  isRuntimeLogIdentifierQueryActive,
} from "@/pages/logs-page-shared";
import type { RuntimeLogEntry } from "@/types/runtime";
import type { LogsPageDetailLabels } from "./logs-page-detail-panel.i18n";

type LogsPageDetailIdentifierRow = {
  key: string;
  label: string;
  value: string;
};

type LogsPageDetailMetadataRow = {
  label: string;
  value: string;
};

type RuntimeInsightRow = {
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

function readRuntimeLogNestedValue(
  source: unknown,
  path: string[],
) {
  let current: unknown = source;
  for (const segment of path) {
    const objectLike = getObjectLikeValue(current);
    if (!objectLike) {
      return undefined;
    }
    if (!(segment in objectLike)) {
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

type LogsPageDetailPanelProps = {
  copiedSection: string | null;
  extraFieldsText: string;
  identifierRows: LogsPageDetailIdentifierRow[];
  metadataRows: LogsPageDetailMetadataRow[];
  metadataText: string;
  onClearQuery: () => void;
  onCopy: (sectionKey: string, value: string) => void | Promise<void>;
  onToggleIdentifierQuery: (value: string) => void;
  query: string;
  rawJsonText: string;
  responsePreviewText: string;
  selectedEntry: RuntimeLogEntry;
  selectedEntrySubtitle: string;
  selectedLevelTone: string;
  labels: LogsPageDetailLabels;
};

type CopyActionButtonProps = {
  copied: boolean;
  copiedLabel: string;
  label: string;
  onClick: () => void;
};

export function LogsPageDetailPanel({
  copiedSection,
  extraFieldsText,
  identifierRows,
  metadataRows,
  metadataText,
  onClearQuery,
  onCopy,
  onToggleIdentifierQuery,
  query,
  rawJsonText,
  responsePreviewText,
  selectedEntry,
  selectedEntrySubtitle,
  selectedLevelTone,
  labels,
}: LogsPageDetailPanelProps) {
  const insightRows = buildRuntimeInsightRows(selectedEntry, labels);

  return (
    <div className="flex-1 space-y-2.5 overflow-y-auto px-3 py-3">
      <div className="rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="flex flex-wrap items-center gap-2">
            <span
              className={cn(
                "inline-flex items-center rounded-[0.65rem] border px-2 py-1 app-text-10 font-semibold uppercase tracking-[0.14em]",
                selectedLevelTone,
              )}
            >
              {selectedEntry.level || labels.levelFallback}
            </span>
            <span className="app-text-12 text-[var(--muted-foreground)]">
              {labels.cursorLabel} {selectedEntry.cursor}
            </span>
          </div>
          <CopyActionButton
            copied={copiedSection === "summary"}
            copiedLabel={labels.copied}
            label={labels.summary}
            onClick={() => onCopy("summary", rawJsonText)}
          />
        </div>
        <h2 className="mt-2.5 text-lg font-semibold tracking-[-0.02em]">
          {selectedEntry.message || selectedEntry.raw_text}
        </h2>
        <p className="mt-2 app-text-13 leading-5 text-[var(--muted-foreground)]">
          {selectedEntrySubtitle}
        </p>
      </div>

      {insightRows.length > 0 ? (
        <div className="rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                {labels.insights}
              </div>
              <div className="mt-1 app-text-11 text-[var(--muted-foreground)]">
                {labels.insightsHelp}
              </div>
            </div>
          </div>

          <dl className="mt-3 grid gap-x-4 gap-y-2.5 sm:grid-cols-2 2xl:grid-cols-3">
            {insightRows.map((row) => (
              <div
                key={row.key}
                className="min-w-0 border-t border-[var(--border)]/60 pt-2.5 first:border-t-0 first:pt-0"
              >
                <dt className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                  {row.label}
                </dt>
                <dd className="mt-1.5 break-all app-text-13 leading-5">
                  {row.valueClassName ? (
                    <span className={cn(row.valueClassName)}>{row.value}</span>
                  ) : (
                    row.value
                  )}
                </dd>
              </div>
            ))}
          </dl>
        </div>
      ) : null}

      {identifierRows.length > 0 ? (
        <div className="rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                {labels.identifiers}
              </div>
              <div className="mt-1 app-text-11 text-[var(--muted-foreground)]">
                {labels.identifiersHelp}
              </div>
            </div>
            {query.trim() ? (
              <Button
                variant="ghost"
                size="sm"
                className="h-7 rounded-[0.65rem] border border-[var(--border)] bg-black/10 px-2.5 text-[var(--muted-foreground)] hover:bg-black/20 hover:text-[var(--foreground)]"
                onClick={onClearQuery}
              >
                {labels.clearSearch}
              </Button>
            ) : null}
          </div>

          <div className="mt-3 space-y-2">
            {identifierRows.map((row) => {
              const active = isRuntimeLogIdentifierQueryActive(query, row.value);
              return (
                <div
                  key={row.key}
                  className={cn(
                    "flex flex-col gap-3 rounded-[0.75rem] border px-3 py-2.5 sm:flex-row sm:items-start sm:justify-between",
                    active
                      ? "border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)]"
                      : "border-[var(--border)] bg-black/10",
                  )}
                >
                  <div className="min-w-0">
                    <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                      {row.label}
                    </div>
                    <div className="mt-1.5 break-all font-mono app-text-12-5 leading-5 text-[var(--foreground)]">
                      {row.value}
                    </div>
                  </div>
                  <div className="flex flex-wrap items-center gap-2 sm:justify-end">
                    <CopyActionButton
                      copied={copiedSection === `identifier:${row.key}`}
                      copiedLabel={labels.copied}
                      label={labels.copyValue}
                      onClick={() => onCopy(`identifier:${row.key}`, row.value)}
                    />
                    <Button
                      variant={active ? "secondary" : "ghost"}
                      size="sm"
                      className={cn(
                        "h-7 rounded-[0.65rem] border px-2.5 app-text-11",
                        active
                          ? "border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)] text-[var(--accent-primary)]"
                          : "border-[var(--border)] bg-black/10 text-[var(--muted-foreground)] hover:bg-black/20 hover:text-[var(--foreground)]",
                      )}
                      onClick={() => onToggleIdentifierQuery(row.value)}
                    >
                      {active ? labels.cancelFilter : labels.filterSameValue}
                    </Button>
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      ) : null}

      <div className="rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
        <div className="flex items-center justify-between gap-3">
          <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            {labels.metadata}
          </div>
          <CopyActionButton
            copied={copiedSection === "metadata"}
            copiedLabel={labels.copied}
            label={labels.copyMetadata}
            onClick={() => onCopy("metadata", metadataText)}
          />
        </div>
        <dl className="mt-3 grid gap-x-4 gap-y-2.5 sm:grid-cols-2 2xl:grid-cols-3">
          {metadataRows.map((row) => (
            <div
              key={row.label}
              className="min-w-0 border-t border-[var(--border)]/60 pt-2.5 first:border-t-0 first:pt-0"
            >
              <dt className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
                {row.label}
              </dt>
              <dd className="mt-1.5 break-all app-text-13 leading-5">
                {row.value}
              </dd>
            </div>
          ))}
        </dl>
      </div>

      {selectedEntry.response_body_preview ? (
        <div className="rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
          <div className="flex items-center justify-between gap-3">
            <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              {labels.responsePreview}
            </div>
            <CopyActionButton
              copied={copiedSection === "response_preview"}
              copiedLabel={labels.copied}
              label={labels.copyPreview}
              onClick={() => onCopy("response_preview", responsePreviewText)}
            />
          </div>
          <pre className="app-code-surface mt-2 overflow-x-auto whitespace-pre-wrap break-words rounded-[0.75rem] border border-[var(--border)] bg-black/25 p-3 font-mono text-[var(--foreground)]">
            {selectedEntry.response_body_preview}
          </pre>
        </div>
      ) : null}

      {selectedEntry.fields && Object.keys(selectedEntry.fields).length > 0 ? (
        <div className="rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
          <div className="flex items-center justify-between gap-3">
            <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              {labels.extraFields}
            </div>
            <CopyActionButton
              copied={copiedSection === "extra_fields"}
              copiedLabel={labels.copied}
              label={labels.copyFields}
              onClick={() => onCopy("extra_fields", extraFieldsText)}
            />
          </div>
          <pre className="app-code-surface mt-2 overflow-x-auto whitespace-pre-wrap break-words rounded-[0.75rem] border border-[var(--border)] bg-black/25 p-3 font-mono text-[var(--foreground)]">
            {JSON.stringify(selectedEntry.fields, null, 2)}
          </pre>
        </div>
      ) : null}

      <div className="rounded-[0.85rem] border border-[var(--border)] bg-[var(--surface-softer)] p-3">
        <div className="flex items-center justify-between gap-3">
          <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
            {labels.rawJson}
          </div>
          <CopyActionButton
            copied={copiedSection === "raw_json"}
            copiedLabel={labels.copied}
            label={labels.copyJson}
            onClick={() => onCopy("raw_json", rawJsonText)}
          />
        </div>
        <pre className="app-code-surface mt-2 overflow-x-auto whitespace-pre-wrap break-words rounded-[0.75rem] border border-[var(--border)] bg-black/25 p-3 font-mono text-[var(--foreground)]">
          {selectedEntry.raw
            ? JSON.stringify(selectedEntry.raw, null, 2)
            : selectedEntry.raw_text}
        </pre>
      </div>
    </div>
  );
}

function CopyActionButton({
  copied,
  copiedLabel,
  label,
  onClick,
}: CopyActionButtonProps) {
  return (
    <Button
      variant="ghost"
      size="sm"
      className="h-7 rounded-[0.65rem] border border-[var(--border)] bg-black/10 px-2.5 text-[var(--muted-foreground)] hover:bg-black/20 hover:text-[var(--foreground)]"
      onClick={onClick}
    >
      {copied ? <CheckIcon size={13} /> : <CopyIcon size={13} />}
      {copied ? copiedLabel : label}
    </Button>
  );
}
