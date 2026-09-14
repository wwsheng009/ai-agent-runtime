// P2-6 子片 1：顺序账目的浏览器本地持久化。
// 单键 + 版本号 + 容错解析：非法 JSON / 未知版本 / 空账目一律按「无账目」处理，
// 不猜测也不半读；写入失败（配额、隐私模式）不阻塞内存态排序。

import {
  isSameSessionOrder,
  type SessionOrderAccount,
  type SessionOrderAccounts,
} from "./session-order";

export const SESSION_ORDER_STORAGE_KEY =
  "ai-agent-runtime.workspace.session-order";

const SESSION_ORDER_STORAGE_VERSION = 1;

type StoredSessionOrderDocument = {
  version: number;
  accounts: Record<string, string[]>;
};

/** 浏览器本地存储；无窗口 / 被禁用 / 抛错时返回 null（调用方退化为内存态）。 */
export function defaultSessionOrderStorage(): Storage | null {
  try {
    return typeof window === "undefined" ? null : window.localStorage;
  } catch {
    return null;
  }
}

export function parseSessionOrderAccounts(
  raw: string | null | undefined,
): SessionOrderAccounts {
  if (!raw) {
    return {};
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return {};
  }

  if (typeof parsed !== "object" || parsed === null) {
    return {};
  }

  const document = parsed as Partial<StoredSessionOrderDocument>;
  if (document.version !== SESSION_ORDER_STORAGE_VERSION) {
    return {};
  }
  if (typeof document.accounts !== "object" || document.accounts === null) {
    return {};
  }

  const accounts: Record<string, string[]> = {};
  for (const [key, value] of Object.entries(document.accounts)) {
    const account = normalizeSessionOrderAccount(value);
    if (key.trim() && account.length > 0) {
      accounts[key] = account;
    }
  }
  return accounts;
}

function normalizeSessionOrderAccount(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }

  const seen = new Set<string>();
  const ids: string[] = [];
  for (const item of value) {
    if (typeof item !== "string") {
      continue;
    }
    const id = item.trim();
    if (!id || seen.has(id)) {
      continue;
    }
    seen.add(id);
    ids.push(id);
  }
  return ids;
}

export function readSessionOrderAccounts(
  storage: Storage | null | undefined,
): SessionOrderAccounts {
  if (!storage) {
    return {};
  }

  try {
    return parseSessionOrderAccounts(storage.getItem(SESSION_ORDER_STORAGE_KEY));
  } catch {
    return {};
  }
}

export function writeSessionOrderAccounts(
  storage: Storage | null | undefined,
  accounts: SessionOrderAccounts,
): void {
  if (!storage) {
    return;
  }

  const serializable: Record<string, string[]> = {};
  for (const [key, account] of Object.entries(accounts)) {
    if (key.trim() && account.length > 0) {
      serializable[key] = [...account];
    }
  }

  try {
    const document: StoredSessionOrderDocument = {
      version: SESSION_ORDER_STORAGE_VERSION,
      accounts: serializable,
    };
    storage.setItem(SESSION_ORDER_STORAGE_KEY, JSON.stringify(document));
  } catch {
    // 存储不可写（配额 / 隐私模式）不影响本会话内的排序结果。
  }
}

/**
 * 写入一个分组的顺序账目：顺序未变化时返回**原对象引用**，
 * 让调用方（React state）跳过无谓的持久化与重渲染。
 */
export function withSessionOrderAccount(
  accounts: SessionOrderAccounts,
  key: string,
  order: SessionOrderAccount,
): SessionOrderAccounts {
  const trimmedKey = key.trim();
  if (!trimmedKey || order.length === 0) {
    return accounts;
  }
  const current = accounts[trimmedKey];
  if (current !== undefined && isSameSessionOrder(current, order)) {
    return accounts;
  }
  return { ...accounts, [trimmedKey]: [...order] };
}
