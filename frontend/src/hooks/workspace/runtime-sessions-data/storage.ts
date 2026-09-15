// 由 hooks/workspace/use-runtime-sessions-data.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { type RuntimeSessionRecord } from "@/lib/runtime-api";

import { normalizeRuntimeSessions } from "./normalize";
import { sortRuntimeSessions } from "./sorting";
import { type StoredRuntimeSessionsPayload } from "./types";

const runtimeSessionsStorageKeyPrefix = "workspace.runtime.sessions";
const runtimeSessionsSelectedUserStorageKey =
  "workspace.runtime.sessions.selectedUser";

// 本地缓存是数百会话级（实测 186KB）的 JSON。`JSON.parse` + `normalizeRuntimeSessions`
// 是毫秒级同步开销，而读路径会被 effect / 状态初始化反复触发，因此：
//   * 读：按 (storage key, 原始字符串) 单槽记忆 —— 同内容直接复用上次结果数组，
//     既省解析，也让返回**引用稳定**（同内容的新数组会让下游 state/effect 空转）；
//   * 写：内容未变时跳过 `setItem`。`localStorage.setItem` 是同步写，186KB 的
//     序列化 + 落盘会直接占用主线程（流式回复期间触发刷新时尤其明显）。
// 记忆键包含 `Storage` 实例本身：不同实例（测试替身 / 被替换的 storage）即便内容相同
// 也各自解析，避免把上一个实例的结果数组交给新实例。
let readCacheStorage: Storage | null = null;
let readCacheStorageKey = "";
let readCacheRaw = "";
let readCacheResult: RuntimeSessionRecord[] = [];

const lastWrittenSessions = new Map<
  string,
  { storage: Storage; serialized: string }
>();

export function getBrowserStorage() {
  if (typeof window === "undefined") {
    return null;
  }

  return window.localStorage;
}

export function buildStoredRuntimeSessionsKey(userId: string) {
  const resolvedUserId = userId.trim();
  if (!resolvedUserId) {
    return runtimeSessionsStorageKeyPrefix;
  }

  return `${runtimeSessionsStorageKeyPrefix}:${encodeURIComponent(resolvedUserId)}`;
}

export function readStoredRuntimeSessionUserId(
  storage: Storage | null | undefined,
) {
  if (!storage) {
    return "";
  }

  try {
    return storage.getItem(runtimeSessionsSelectedUserStorageKey)?.trim() ?? "";
  } catch {
    return "";
  }
}

export function writeStoredRuntimeSessionUserId(
  storage: Storage | null | undefined,
  userId: string,
) {
  const resolvedUserId = userId.trim();
  if (!storage || !resolvedUserId) {
    return;
  }

  storage.setItem(runtimeSessionsSelectedUserStorageKey, resolvedUserId);
}

export function readStoredRuntimeSessions(
  storage: Storage | null | undefined,
  userId: string,
) {
  const resolvedUserId = userId.trim();
  if (!storage || !resolvedUserId) {
    return [] as RuntimeSessionRecord[];
  }

  try {
    const raw = storage.getItem(buildStoredRuntimeSessionsKey(resolvedUserId));
    if (!raw) {
      return [];
    }

    const storageKey = buildStoredRuntimeSessionsKey(resolvedUserId);
    if (
      storage === readCacheStorage &&
      raw === readCacheRaw &&
      storageKey === readCacheStorageKey
    ) {
      return readCacheResult;
    }

    const parsed = JSON.parse(raw) as Partial<StoredRuntimeSessionsPayload>;
    const sessions = normalizeRuntimeSessions(parsed.sessions);
    readCacheStorage = storage;
    readCacheStorageKey = storageKey;
    readCacheRaw = raw;
    readCacheResult = sessions;
    return sessions;
  } catch {
    return [];
  }
}

export function writeStoredRuntimeSessions(
  storage: Storage | null | undefined,
  userId: string,
  sessions: RuntimeSessionRecord[],
) {
  const resolvedUserId = userId.trim();
  if (!storage || !resolvedUserId) {
    return;
  }

  const storageKey = buildStoredRuntimeSessionsKey(resolvedUserId);
  const serializedSessions = JSON.stringify(
    sortRuntimeSessions(normalizeRuntimeSessions(sessions)),
  );
  const lastWritten = lastWrittenSessions.get(storageKey);
  if (
    lastWritten?.storage === storage &&
    lastWritten.serialized === serializedSessions
  ) {
    // 内容与上次写入完全一致：跳过 186KB 级 setItem。
    return;
  }
  lastWrittenSessions.set(storageKey, { storage, serialized: serializedSessions });

  // 字段顺序与 `StoredRuntimeSessionsPayload` 一致，产出与整体 `JSON.stringify` 等价。
  storage.setItem(
    storageKey,
    `{"sessions":${serializedSessions},"storedAt":${JSON.stringify(
      new Date().toISOString(),
    )},"userId":${JSON.stringify(resolvedUserId)}}`,
  );
}
