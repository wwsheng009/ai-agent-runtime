import type { RuntimeLogEntry } from "@/types/runtime";

export type RuntimeLogLevelKey =
  | "error"
  | "warn"
  | "info"
  | "debug"
  | "other";

export type RuntimeLogLevelFilter =
  | ""
  | "error"
  | "warn"
  | "info"
  | "debug";

export type RuntimeLogLevelStat = {
  key: RuntimeLogLevelKey;
  label: string;
  shortLabel: string;
  count: number;
};

export type RuntimeLogIdentifierRow = {
  key: "request_id" | "trace_id" | "session_id";
  label: string;
  value: string;
};

export type RuntimeLogsUrlState = {
  query: string;
  level: RuntimeLogLevelFilter;
  follow: boolean;
  cursor: number | null;
};

export type RuntimeLogsActiveChip = {
  key: "query" | "level" | "follow" | "cursor";
  label: string;
  value: string;
};

const runtimeLogsUrlKeys = {
  cursor: "cursor",
  follow: "follow",
  level: "level",
  query: "query",
} as const;

const runtimeLogLevelStatOrder: RuntimeLogLevelKey[] = [
  "error",
  "warn",
  "info",
  "debug",
  "other",
];

const runtimeLogLevelStatLabels: Record<
  RuntimeLogLevelKey,
  Pick<RuntimeLogLevelStat, "label" | "shortLabel">
> = {
  error: { label: "Error", shortLabel: "ERR" },
  warn: { label: "Warn", shortLabel: "WRN" },
  info: { label: "Info", shortLabel: "INF" },
  debug: { label: "Debug", shortLabel: "DBG" },
  other: { label: "Other", shortLabel: "LOG" },
};

export function normalizeRuntimeLogLevel(level?: string): RuntimeLogLevelKey {
  switch ((level ?? "").trim().toLowerCase()) {
    case "error":
      return "error";
    case "warn":
      return "warn";
    case "info":
      return "info";
    case "debug":
      return "debug";
    default:
      return "other";
  }
}

export function normalizeRuntimeLogLevelFilter(
  level?: string,
): RuntimeLogLevelFilter {
  switch ((level ?? "").trim().toLowerCase()) {
    case "error":
      return "error";
    case "warn":
      return "warn";
    case "info":
      return "info";
    case "debug":
      return "debug";
    default:
      return "";
  }
}

export function buildRuntimeLogLevelStats(
  entries: RuntimeLogEntry[],
): RuntimeLogLevelStat[] {
  const counts: Record<RuntimeLogLevelKey, number> = {
    error: 0,
    warn: 0,
    info: 0,
    debug: 0,
    other: 0,
  };

  for (const entry of entries) {
    counts[normalizeRuntimeLogLevel(entry.level)] += 1;
  }

  return runtimeLogLevelStatOrder.map((key) => ({
    key,
    label: runtimeLogLevelStatLabels[key].label,
    shortLabel: runtimeLogLevelStatLabels[key].shortLabel,
    count: counts[key],
  }));
}

export function buildRuntimeLogIdentifierRows(
  entry: RuntimeLogEntry | null,
): RuntimeLogIdentifierRow[] {
  if (!entry) {
    return [];
  }

  const identifiers: RuntimeLogIdentifierRow[] = [];
  if (entry.request_id?.trim()) {
    identifiers.push({
      key: "request_id",
      label: "Request ID",
      value: entry.request_id.trim(),
    });
  }
  if (entry.trace_id?.trim()) {
    identifiers.push({
      key: "trace_id",
      label: "Trace ID",
      value: entry.trace_id.trim(),
    });
  }
  if (entry.session_id?.trim()) {
    identifiers.push({
      key: "session_id",
      label: "Session ID",
      value: entry.session_id.trim(),
    });
  }
  return identifiers;
}

export function isRuntimeLogIdentifierQueryActive(
  query: string,
  value: string,
) {
  return query.trim() === value.trim();
}

function parseRuntimeLogsFollow(value: string | null) {
  switch ((value ?? "").trim().toLowerCase()) {
    case "0":
    case "false":
    case "no":
    case "off":
      return false;
    default:
      return true;
  }
}

function parseRuntimeLogsCursor(value: string | null) {
  if (!value?.trim()) {
    return null;
  }

  const parsed = Number.parseInt(value.trim(), 10);
  if (!Number.isFinite(parsed) || parsed < 0) {
    return null;
  }
  return parsed;
}

export function readRuntimeLogsUrlState(
  searchParams: URLSearchParams,
): RuntimeLogsUrlState {
  return {
    query: searchParams.get(runtimeLogsUrlKeys.query)?.trim() ?? "",
    level: normalizeRuntimeLogLevelFilter(
      searchParams.get(runtimeLogsUrlKeys.level) ?? "",
    ),
    follow: parseRuntimeLogsFollow(searchParams.get(runtimeLogsUrlKeys.follow)),
    cursor: parseRuntimeLogsCursor(searchParams.get(runtimeLogsUrlKeys.cursor)),
  };
}

export function writeRuntimeLogsUrlState(
  currentSearchParams: URLSearchParams,
  state: RuntimeLogsUrlState,
) {
  const nextSearchParams = new URLSearchParams(currentSearchParams);
  const normalizedQuery = state.query.trim();

  if (normalizedQuery) {
    nextSearchParams.set(runtimeLogsUrlKeys.query, normalizedQuery);
  } else {
    nextSearchParams.delete(runtimeLogsUrlKeys.query);
  }

  if (state.level) {
    nextSearchParams.set(runtimeLogsUrlKeys.level, state.level);
  } else {
    nextSearchParams.delete(runtimeLogsUrlKeys.level);
  }

  if (state.follow) {
    nextSearchParams.delete(runtimeLogsUrlKeys.follow);
  } else {
    nextSearchParams.set(runtimeLogsUrlKeys.follow, "false");
  }

  if (typeof state.cursor === "number" && state.cursor >= 0) {
    nextSearchParams.set(runtimeLogsUrlKeys.cursor, String(state.cursor));
  } else {
    nextSearchParams.delete(runtimeLogsUrlKeys.cursor);
  }

  return nextSearchParams;
}

export function buildRuntimeLogsActiveChips(
  state: RuntimeLogsUrlState,
  newestCursor: number | null,
): RuntimeLogsActiveChip[] {
  const chips: RuntimeLogsActiveChip[] = [];

  if (state.query.trim()) {
    chips.push({
      key: "query",
      label: "搜索",
      value: state.query.trim(),
    });
  }

  if (state.level) {
    chips.push({
      key: "level",
      label: "级别",
      value: state.level,
    });
  }

  if (!state.follow) {
    chips.push({
      key: "follow",
      label: "Follow",
      value: "off",
    });
  }

  if (
    typeof state.cursor === "number" &&
    (!state.follow || newestCursor === null || state.cursor !== newestCursor)
  ) {
    chips.push({
      key: "cursor",
      label: "Cursor",
      value: String(state.cursor),
    });
  }

  return chips;
}

export function buildRuntimeLogsShareState(
  state: RuntimeLogsUrlState,
  newestCursor: number | null,
): RuntimeLogsUrlState {
  const normalizedQuery = state.query.trim();
  const shouldKeepCursor =
    typeof state.cursor === "number" &&
    (!state.follow || newestCursor === null || state.cursor !== newestCursor);

  return {
    query: normalizedQuery,
    level: state.level,
    follow: state.follow,
    cursor: shouldKeepCursor ? state.cursor : null,
  };
}
