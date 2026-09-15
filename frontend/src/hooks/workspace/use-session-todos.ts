// composer 上沿「当前任务」面板的会话级视图模型（方案 §5.3 / §6.2）。
//
// 数据来自会话投影：`applyRuntimeEventToThread`（通道 A：tool.completed 的
// protocol_result.metadata.todo_snapshot）与 `applySessionHistoryToThread`
// （通道 B1：会话历史原文的 metadata.todos）已经各自折叠出 `thread.todoSnapshot`。
// 因此这里不再订阅第二套事件流、不重复拉会话历史——只把快照转成面板可直接渲染的
// 视图模型，并持有「折叠态」这一纯 UI 状态（按会话维度记在 sessionStorage）。

import { useCallback, useMemo, useState } from "react";

import {
  countTodos,
  currentTodoItem,
  isTodoPanelVisible,
  todoItemLabel,
  type TodoCounts,
  type TodoItem,
  type TodoSnapshot,
} from "@/lib/thread-state/todos";

const COLLAPSED_STORAGE_PREFIX = "workspace.todoPanel.collapsed";

/** 默认折叠为单行条（方案 §6.3）。 */
const DEFAULT_COLLAPSED = true;

export type SessionTodosViewModel = {
  /** 是否渲染面板：无快照 / 空列表 / 全部完成 → 不渲染（不占位）。 */
  visible: boolean;
  snapshot: TodoSnapshot | null;
  items: readonly TodoItem[];
  counts: TodoCounts;
  /** 当前进行项（最多一条；契约见 backend/internal/toolkit/tools/todos.go）。 */
  current: TodoItem | null;
  currentLabel: string;
  collapsed: boolean;
  toggleCollapsed: () => void;
};

export function readTodoPanelCollapsed(sessionId: string): boolean {
  if (!sessionId) {
    return DEFAULT_COLLAPSED;
  }
  try {
    const stored = sessionStorage.getItem(
      `${COLLAPSED_STORAGE_PREFIX}.${sessionId}`,
    );
    if (stored === "0") {
      return false;
    }
    if (stored === "1") {
      return true;
    }
  } catch {
    // 私密模式 / 存储被禁用：回落到默认折叠，不影响面板本身。
  }
  return DEFAULT_COLLAPSED;
}

export function writeTodoPanelCollapsed(sessionId: string, collapsed: boolean) {
  if (!sessionId) {
    return;
  }
  try {
    sessionStorage.setItem(
      `${COLLAPSED_STORAGE_PREFIX}.${sessionId}`,
      collapsed ? "1" : "0",
    );
  } catch {
    // 同上：持久化失败只影响下次进入会话的默认态。
  }
}

export function useSessionTodos({
  sessionId,
  snapshot,
}: {
  sessionId?: string;
  snapshot?: TodoSnapshot | null;
}): SessionTodosViewModel {
  const resolvedSessionId = sessionId ?? "";
  // 折叠态按会话隔离：会话切换时用「当前会话 id」判定，派生值不会串上一个会话；
  // 只有用户点击才写状态（不在渲染期 setState）。
  const [preference, setPreference] = useState(() => ({
    sessionId: resolvedSessionId,
    collapsed: readTodoPanelCollapsed(resolvedSessionId),
  }));
  const collapsed =
    preference.sessionId === resolvedSessionId
      ? preference.collapsed
      : readTodoPanelCollapsed(resolvedSessionId);

  const toggleCollapsed = useCallback(() => {
    setPreference((current) => {
      const base =
        current.sessionId === resolvedSessionId
          ? current.collapsed
          : readTodoPanelCollapsed(resolvedSessionId);
      const next = !base;
      writeTodoPanelCollapsed(resolvedSessionId, next);
      return { sessionId: resolvedSessionId, collapsed: next };
    });
  }, [resolvedSessionId]);

  const resolved = snapshot ?? null;
  const items = useMemo(() => resolved?.items ?? [], [resolved]);
  const counts = useMemo(() => countTodos(items), [items]);
  const current = useMemo(() => currentTodoItem(items), [items]);
  const currentLabel = current ? todoItemLabel(current) : "";
  const visible = Boolean(resolvedSessionId) && isTodoPanelVisible(resolved);

  return {
    visible,
    snapshot: resolved,
    items,
    counts,
    current,
    currentLabel,
    collapsed,
    toggleCollapsed,
  };
}
