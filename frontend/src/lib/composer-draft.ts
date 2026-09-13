// P1-4 子片 1：Composer 草稿持久化（纯存储层，React/DOM 无关，便于单测）。
//
// 约定：
// - 键按「会话」隔离：优先 sessionId（已绑定运行时会话），否则回退线程 id
//   （新建未发送的 draft 线程只有 `new`）。两者都没有 → 不持久化。
// - 值为草稿原文；空白草稿视为「无草稿」并删除条目，避免残留空键。
// - 所有访问容错：存储不可用（SSR / 隐私模式 / 配额满 / 值异常）只降级为
//   内存态，绝不抛出——打字过程不能因存储异常中断。
// - key 带 schema 版本前缀，未来改结构时可整体弃用而不误读旧值。

export const COMPOSER_DRAFT_STORAGE_PREFIX =
  "aicli.workspace.composer-draft.v1:";

export type ComposerDraftStorage = Pick<
  Storage,
  "getItem" | "setItem" | "removeItem"
>;

export type ComposerDraftThreadSource = {
  id?: string;
  sessionId?: string;
};

/** 会话键：sessionId 优先，其次线程 id；空白视为无键（不持久化）。 */
export function resolveComposerDraftThreadKey(
  thread: ComposerDraftThreadSource | undefined | null,
): string | null {
  const sessionId = thread?.sessionId?.trim();
  if (sessionId) {
    return sessionId;
  }
  const threadId = thread?.id?.trim();
  return threadId ? threadId : null;
}

export function composerDraftStorageKey(threadKey: string): string {
  return `${COMPOSER_DRAFT_STORAGE_PREFIX}${threadKey}`;
}

export function readComposerDraft(
  storage: ComposerDraftStorage | null | undefined,
  threadKey: string | null,
): string {
  if (!storage || !threadKey) {
    return "";
  }
  try {
    return storage.getItem(composerDraftStorageKey(threadKey)) ?? "";
  } catch {
    return "";
  }
}

export function writeComposerDraft(
  storage: ComposerDraftStorage | null | undefined,
  threadKey: string | null,
  value: string,
): void {
  if (!storage || !threadKey) {
    return;
  }
  const key = composerDraftStorageKey(threadKey);
  try {
    if (value.trim() === "") {
      storage.removeItem(key);
      return;
    }
    storage.setItem(key, value);
  } catch {
    // 配额/权限异常：放弃持久化，保留内存态。
  }
}

export function clearComposerDraft(
  storage: ComposerDraftStorage | null | undefined,
  threadKey: string | null,
): void {
  writeComposerDraft(storage, threadKey, "");
}

/** 浏览器环境下的 localStorage；SSR 或不可用时返回 null。 */
export function resolveBrowserComposerDraftStorage(): ComposerDraftStorage | null {
  if (typeof window === "undefined") {
    return null;
  }
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}
