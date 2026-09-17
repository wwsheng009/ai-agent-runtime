// Batch 3（多会话并发运行时 §4.7.3）：注册表条目 → 页内通知的接线层。
//
// 只在开关打开时工作：关闭时既不清空也不订阅，页面行为与改造前一致。
// 通知只覆盖「非选中会话」——选中会话的完成 / 待交互由前台消息流与卡片呈现。

import { useCallback, useEffect, useRef, useSyncExternalStore } from "react";

import { MULTI_SESSION_REGISTRY_ENABLED } from "@/lib/session-runtime/flags";
import {
  diffSessionRuntimeNotices,
  dismissSessionRuntimeNotice,
  dismissSessionRuntimeNoticesForSession,
  getSessionRuntimeNoticesSnapshot,
  pushSessionRuntimeNotices,
  subscribeSessionRuntimeNotices,
  type SessionRuntimeNotice,
} from "@/lib/session-runtime/notices";
import type { SessionRuntimeEntrySnapshot } from "@/lib/session-runtime/types";

const EMPTY_NOTICES: readonly SessionRuntimeNotice[] = [];

export type SessionRuntimeNoticesOptions = {
  /** 注册表条目投影（`useSessionRuntimeEntries` 的返回值）。 */
  entries: readonly SessionRuntimeEntrySnapshot[];
  /** 选中会话：它的变化不再提示（前台已呈现）。 */
  selectedSessionId?: string | null;
  /** 缺省取回滚开关（false = 完全不产生通知）。 */
  enabled?: boolean;
};

export type SessionRuntimeNoticesController = {
  notices: readonly SessionRuntimeNotice[];
  dismiss: (id: string) => void;
};

export function useSessionRuntimeNotices(
  options: SessionRuntimeNoticesOptions,
): SessionRuntimeNoticesController {
  const { entries, selectedSessionId, enabled = MULTI_SESSION_REGISTRY_ENABLED } =
    options;

  const subscribe = useCallback(
    (onStoreChange: () => void) =>
      enabled ? subscribeSessionRuntimeNotices(onStoreChange) : () => {},
    [enabled],
  );
  const getSnapshot = useCallback(
    () => (enabled ? getSessionRuntimeNoticesSnapshot() : EMPTY_NOTICES),
    [enabled],
  );
  const notices = useSyncExternalStore(subscribe, getSnapshot, getSnapshot);

  // 上一次条目快照：diff 是内容比较，重复执行（相同内容）不会重复产通知。
  const previousRef = useRef<Map<string, SessionRuntimeEntrySnapshot>>(new Map());

  useEffect(() => {
    if (!enabled) {
      previousRef.current = new Map();
      return;
    }
    const previous = previousRef.current;
    const next = entries.map(
      (entry) => [entry.sessionId, entry] as const,
    );
    pushSessionRuntimeNotices(
      diffSessionRuntimeNotices(previous, entries, { selectedSessionId }),
    );
    previousRef.current = new Map(next);
  }, [enabled, entries, selectedSessionId]);

  // 切回某会话即视为「已读」：清掉它的通知，避免切回去还挂着提示。
  useEffect(() => {
    if (!enabled || !selectedSessionId) {
      return;
    }
    dismissSessionRuntimeNoticesForSession(selectedSessionId);
  }, [enabled, selectedSessionId]);

  const dismiss = useCallback((id: string) => {
    dismissSessionRuntimeNotice(id);
  }, []);

  return { notices, dismiss };
}
