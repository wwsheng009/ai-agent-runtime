import type { AnalyticsGroupBy } from "@/types/runtime";

export function buildAnalyticsGroupSelectionSearchParams(
  current: URLSearchParams,
  groupBy: AnalyticsGroupBy,
  key: string,
) {
  const next = new URLSearchParams(current);
  const setOrDelete = (name: string, value: string) => {
    const normalized = value.trim();
    if (normalized) next.set(name, normalized);
    else next.delete(name);
  };

  if (groupBy === "day") {
    setOrDelete("from", key);
    setOrDelete("to", key);
  } else if (groupBy === "provider") {
    setOrDelete("provider", key === "(unknown)" ? "" : key);
  } else if (groupBy === "model") {
    setOrDelete("model", key === "(unknown)" ? "" : key);
  } else if (groupBy === "directory") {
    setOrDelete("directory", key === "." ? "" : key);
  } else if (groupBy === "project") {
    setOrDelete("project", key === "(unknown)" ? "" : key);
  } else {
    setOrDelete("status", key === "(unknown)" ? "" : key);
  }

  next.delete("offset");
  return next;
}
