import { isConfigRecord } from "./runtime-provider-config-utils";
import type { RuntimeShellSettingsSaveRequest } from "@/types/runtime";

/**
 * `shell:` 段在编辑器草稿里的形状。
 *
 * 键名与后端 yaml tag 一致（camelCase），因此草稿可以直接保存回 runtime.yaml。
 * 设计见 docs/plan/windows-posix-shell-detection-and-config-plan-20261104.md §4。
 */
export type RuntimeShellConfigSummary = {
  autoDetect: boolean;
  prefer: string[];
  path: string;
  probeTimeout: string;
  strictProbe: boolean;
  scriptDelivery: string;
  env: Record<string, string>;
};

/** 后端支持的候选 ID（顺序即内置缺省链）；用于下拉框与校验提示。 */
export const runtimeShellCandidateIds = [
  "git-bash",
  "msys-bash",
  "busybox",
  "wsl",
  "pwsh",
  "powershell",
  "cmd",
  "bash",
  "zsh",
  "sh",
] as const;

export function getRuntimeShellConfig(value: unknown): RuntimeShellConfigSummary {
  const shellRoot = getShellRoot(value) ?? {};

  return {
    // 缺省 true：不写 autoDetect 时按内置链探测（与后端零值语义一致）。
    autoDetect: shellRoot.autoDetect === undefined ? true : Boolean(shellRoot.autoDetect),
    prefer: readTextList(shellRoot.prefer),
    path: readText(shellRoot.path),
    probeTimeout: readText(shellRoot.probeTimeout),
    strictProbe: shellRoot.strictProbe === undefined ? true : Boolean(shellRoot.strictProbe),
    // 空白串必须当作「未设置」回退到 stdin：后端按枚举校验，`""` / `"  "` 会被判非法。
    scriptDelivery: readText(shellRoot.scriptDelivery).trim() || "stdin",
    env: readTextRecord(shellRoot.env),
  };
}

/** 逗号/换行分隔的候选 ID 文本 -> 去重、小写、保序的数组。 */
export function parseShellPreferInput(text: string): string[] {
  const seen = new Set<string>();
  const result: string[] = [];
  for (const part of text.split(/[\s,，]+/)) {
    const id = part.trim().toLowerCase();
    if (!id || seen.has(id)) {
      continue;
    }
    seen.add(id);
    result.push(id);
  }
  return result;
}

export function formatShellPrefer(prefer: string[]): string {
  return prefer.join(", ");
}

/** 从候选链里上移/下移一项；越界时原样返回。 */
export function moveShellPreferItem(
  prefer: string[],
  index: number,
  offset: -1 | 1,
): string[] {
  const target = index + offset;
  if (index < 0 || index >= prefer.length || target < 0 || target >= prefer.length) {
    return prefer;
  }
  const next = [...prefer];
  const [item] = next.splice(index, 1);
  next.splice(target, 0, item);
  return next;
}

export function addShellPreferItem(prefer: string[], id: string): string[] {
  const normalized = id.trim().toLowerCase();
  if (!normalized || prefer.includes(normalized)) {
    return prefer;
  }
  return [...prefer, normalized];
}

export function removeShellPreferItem(prefer: string[], id: string): string[] {
  return prefer.filter((item) => item !== id);
}

/** Map the camelCase editor draft to the dedicated shell API request shape. */
export function toRuntimeShellSettingsSaveRequest(
  config: RuntimeShellConfigSummary,
): RuntimeShellSettingsSaveRequest {
  return {
    auto_detect: config.autoDetect,
    prefer: config.prefer.map((item) => item.trim().toLowerCase()).filter(Boolean),
    path: config.path.trim(),
    probe_timeout: config.probeTimeout.trim(),
    strict_probe: config.strictProbe,
    script_delivery: config.scriptDelivery.trim() || "stdin",
    env: { ...config.env },
  };
}

/** Compare only the fields owned by the shell domain. */
export function runtimeShellConfigsEqual(
  left: RuntimeShellConfigSummary,
  right: RuntimeShellConfigSummary,
): boolean {
  return (
    left.autoDetect === right.autoDetect &&
    left.strictProbe === right.strictProbe &&
    left.path.trim() === right.path.trim() &&
    left.probeTimeout.trim() === right.probeTimeout.trim() &&
    (left.scriptDelivery.trim() || "stdin") ===
      (right.scriptDelivery.trim() || "stdin") &&
    left.prefer.join("\u0000") === right.prefer.join("\u0000") &&
    envRecordsEqual(left.env, right.env)
  );
}

/**
 * Compare the actual `shell` nodes for write routing. This intentionally does
 * not use the normalized summary: `{}` and `{autoDetect: true}` are equivalent
 * at runtime, but only the former should stay out of the dedicated writer.
 */
export function runtimeShellConfigNodesEqual(
  leftDocument: unknown,
  rightDocument: unknown,
): boolean {
  return (
    stableShellNodeJSON(getShellRoot(leftDocument)) ===
    stableShellNodeJSON(getShellRoot(rightDocument))
  );
}

/**
 * Replace the shell node in a parsed document with another snapshot. The
 * generic config-document writer uses this to keep shell edits out of its
 * payload; shell.* is persisted by the dedicated layered API instead.
 */
export function withShellConfigFrom(
  target: unknown,
  source: unknown,
): unknown {
  if (!isConfigRecord(target)) {
    return target;
  }
  const next = JSON.parse(JSON.stringify(target)) as Record<string, unknown>;
  const sourceShell = getShellRoot(source);
  if (sourceShell) {
    next.shell = JSON.parse(JSON.stringify(sourceShell)) as unknown;
  } else {
    delete next.shell;
  }
  return next;
}

function getShellRoot(value: unknown) {
  if (!isConfigRecord(value)) {
    return null;
  }
  return isConfigRecord(value.shell) ? value.shell : null;
}

function readText(value: unknown) {
  if (typeof value === "string") {
    return value;
  }
  if (typeof value === "number") {
    return String(value);
  }
  return "";
}

function readTextList(value: unknown): string[] {
  if (Array.isArray(value)) {
    return value
      .map((item) => readText(item).trim())
      .filter((item) => item.length > 0);
  }
  if (typeof value === "string") {
    return parseShellPreferInput(value);
  }
  return [];
}

function readTextRecord(value: unknown): Record<string, string> {
  if (!isConfigRecord(value)) {
    return {};
  }
  const result: Record<string, string> = {};
  for (const [key, item] of Object.entries(value)) {
    const text = readText(item);
    // Empty values are meaningful environment overrides: they clear a value
    // inherited from the parent process. Dropping them here would silently
    // remove the override on the next save.
    if (key) {
      result[key] = text;
    }
  }
  return result;
}

function envRecordsEqual(
  left: Record<string, string>,
  right: Record<string, string>,
): boolean {
  const leftKeys = Object.keys(left).sort();
  const rightKeys = Object.keys(right).sort();
  if (leftKeys.join("\u0000") !== rightKeys.join("\u0000")) {
    return false;
  }
  return leftKeys.every((key) => left[key] === right[key]);
}

function stableShellNodeJSON(value: unknown): string {
  if (Array.isArray(value)) {
    return `[${value.map(stableShellNodeJSON).join(",")}]`;
  }
  if (isConfigRecord(value)) {
    return `{${Object.keys(value)
      .sort()
      .map((key) => `${JSON.stringify(key)}:${stableShellNodeJSON(value[key])}`)
      .join(",")}}`;
  }
  return JSON.stringify(value) ?? "null";
}
