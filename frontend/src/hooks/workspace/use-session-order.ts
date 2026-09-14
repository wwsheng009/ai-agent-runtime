// P2-6 子片 1：侧栏会话排序的接线层。
// 排序模式偏好走设置域（持久化在 AppSettings 里）；手动顺序账目是浏览器本地状态，
// 只有一个写入源——用户拖拽（`commitOrder`）。渲染期不写存储。

import { useCallback, useState } from "react";

import { useAppSettings } from "@/core/settings";
import {
  orderSessionsForMode,
  type SessionOrderAccount,
  type SessionOrderAccounts,
  type SessionOrderCandidate,
  type SessionOrderMode,
} from "@/lib/workspace/session-order";
import {
  defaultSessionOrderStorage,
  readSessionOrderAccounts,
  withSessionOrderAccount,
  writeSessionOrderAccounts,
} from "@/lib/workspace/session-order-store";

export type SessionOrderController = {
  /** 当前排序模式（设置域，跨会话持久化）。 */
  mode: SessionOrderMode;
  setMode: (mode: SessionOrderMode) => void;
  /** 分组键 → 手动顺序账目（浏览器本地）。 */
  accounts: SessionOrderAccounts;
  /** 写入某分组的手动顺序；顺序未变化时不写存储也不触发重渲染。 */
  commitOrder: (accountKey: string, order: SessionOrderAccount) => void;
  /** 按当前模式取分组内的会话顺序（纯派生，不改动入参）。 */
  orderFor: <T extends SessionOrderCandidate>(
    accountKey: string,
    sessions: readonly T[],
  ) => T[];
};

export function useSessionOrder(): SessionOrderController {
  const { settings, updateSection } = useAppSettings();
  const mode = settings.workspace.sessionOrder;
  const [accounts, setAccounts] = useState<SessionOrderAccounts>(() =>
    readSessionOrderAccounts(defaultSessionOrderStorage()),
  );

  const setMode = useCallback(
    (next: SessionOrderMode) => {
      updateSection("workspace", { sessionOrder: next });
    },
    [updateSection],
  );

  const commitOrder = useCallback(
    (accountKey: string, order: SessionOrderAccount) => {
      setAccounts((current) => {
        const next = withSessionOrderAccount(current, accountKey, order);
        if (next === current) {
          return current;
        }
        writeSessionOrderAccounts(defaultSessionOrderStorage(), next);
        return next;
      });
    },
    [],
  );

  const orderFor = useCallback(
    <T extends SessionOrderCandidate>(accountKey: string, sessions: readonly T[]) =>
      orderSessionsForMode(sessions, mode, accounts[accountKey]),
    [accounts, mode],
  );

  return { accounts, commitOrder, mode, orderFor, setMode };
}
