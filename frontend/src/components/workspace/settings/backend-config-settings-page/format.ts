// 由 components/workspace/settings/backend-config-settings-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { type RuntimeConfigDocument } from "@/lib/runtime-api";
import { type RuntimeProxyConfigSummary, hasRuntimeProxyConfig } from "../runtime-proxy-domain-utils";
import { type EditorMode, type Translator } from "./types";


export function formatBytes(value: number) {
  if (value < 1024) {
    return `${value} B`;
  }
  if (value < 1024 * 1024) {
    return `${(value / 1024).toFixed(1)} KB`;
  }
  return `${(value / (1024 * 1024)).toFixed(1)} MB`;
}

export function formatRuntimeProxySummary(
  config: RuntimeProxyConfigSummary,
  t: Translator,
  tCommon: Translator,
) {
  if (!hasRuntimeProxyConfig(config)) {
    return t("editor.proxySummary.fallback");
  }

  const parts: string[] = [];
  if (config.http.trim()) {
    parts.push("HTTP");
  }
  if (config.https.trim()) {
    parts.push("HTTPS");
  }
  if (config.noProxy.trim()) {
    parts.push("NO_PROXY");
  }

  if (parts.length === 0) {
    return config.enabled
      ? tCommon("states.enabled")
      : tCommon("states.disabled");
  }

  return parts.join(" + ");
}

export function formatTimestamp(value: string | undefined, locale: string) {
  if (!value) {
    return "";
  }
  return new Intl.DateTimeFormat(locale, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}

export function getModeLabel(
  mode: EditorMode,
  entries: Array<{ label: string; mode: EditorMode }>,
) {
  return entries.find((entry) => entry.mode === mode)?.label ?? mode;
}

export function sleep(ms: number) {
  return new Promise((resolve) => {
    window.setTimeout(resolve, ms);
  });
}

export function parseLooseScalar(value: string) {
  const trimmed = value.trim();
  if (!trimmed) {
    return "";
  }
  if (/^-?\d+(?:\.\d+)?$/.test(trimmed)) {
    return Number(trimmed);
  }
  return value;
}
export function countImpactPaths(paths?: string[]) {
  return Array.isArray(paths) ? paths.length : 0;
}
export function buildSaveStatusMessage(
  t: Translator,
  document: RuntimeConfigDocument,
) {
  const targetPath = document.path?.trim() || t("editor.validation.defaultPath");
  const impact = document.runtime_impact;
  const appliedCount = countImpactPaths(impact?.applied_paths);
  const hotReloadCount = countImpactPaths(impact?.hot_reload_paths);
  const restartCount = countImpactPaths(impact?.restart_required_paths);
  const inactiveCount = countImpactPaths(impact?.inactive_paths);

  const parts: string[] = [];
  if (appliedCount > 0) {
    parts.push(t("editor.saveStatus.applied", { count: appliedCount }));
  } else if (hotReloadCount > 0) {
    parts.push(t("editor.saveStatus.hotReload", { count: hotReloadCount }));
  }
  if (restartCount > 0) {
    parts.push(t("editor.saveStatus.restart", { count: restartCount }));
  }
  if (inactiveCount > 0) {
    parts.push(t("editor.saveStatus.inactive", { count: inactiveCount }));
  }

  if (parts.length === 0) {
    return t("editor.saveStatus.basic", { targetPath });
  }
  return t("editor.saveStatus.detailed", {
    details: parts.join(t("editor.saveStatus.separator")),
    targetPath,
  });
}
