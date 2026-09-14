// P2-6 子片 1：侧栏会话行的**组内**拖拽重排（浏览器原生 HTML5 DnD，不引入依赖）。
// 边界（与批次范围一致）：
// - 只接收同组落点；跨组拖拽不 preventDefault，浏览器呈现「不可放置」，不伪造落点、不做假回滚；
// - 结算只回传顺序数组，是否写账目 / 是否切模式由接线层决定；
// - 落点与现状等价时不发通知（不产生空写入，也不触发播报）。
//
// 结算基于调用方传入的**当前可见顺序**（已按模式结算过的分组），因此拖拽落点稳定可预期。

import { useState, type DragEvent } from "react";

import {
  moveSessionInOrder,
  type SessionDropEdge,
} from "@/lib/workspace/session-order";

export type SidebarSessionDragGroup = {
  key: string;
  sessions: readonly { id: string }[];
};

export type SidebarSessionDragTarget = {
  accountKey: string;
  edge: SessionDropEdge;
  sessionId: string;
};

/** 直接展开到会话行上的拖拽属性（缺省形态与不接线时一致）。 */
export type SidebarSessionDragRowProps = {
  dragEnabled: boolean;
  dragging: boolean;
  dropEdge: SessionDropEdge | null;
  onDragEnd: () => void;
  onDragLeave: (event: DragEvent<HTMLDivElement>) => void;
  onDragOver: (event: DragEvent<HTMLDivElement>) => void;
  onDragStart: (event: DragEvent<HTMLDivElement>) => void;
  onDrop: (event: DragEvent<HTMLDivElement>) => void;
};

type UseSidebarSessionDragArgs = {
  /** 手动模式才可拖：关闭时不渲染拖拽 affordance，也不接收落点。 */
  enabled: boolean;
  groups: readonly SidebarSessionDragGroup[];
  onReorder: (accountKey: string, order: readonly string[]) => void;
  /** 结算播报文案（已本地化），只在真正发生位移时取用。 */
  describeMoved: (accountKey: string, sessionId: string) => string;
};

export type SidebarSessionDragController = {
  /** 无障碍播报内容（空串表示无新播报）。 */
  announcement: string;
  dragPropsFor: (accountKey: string, sessionId: string) => SidebarSessionDragRowProps;
};

export function useSidebarSessionDrag({
  describeMoved,
  enabled,
  groups,
  onReorder,
}: UseSidebarSessionDragArgs): SidebarSessionDragController {
  const [draggingSession, setDraggingSession] = useState<{
    accountKey: string;
    sessionId: string;
  } | null>(null);
  const [dropTarget, setDropTarget] = useState<SidebarSessionDragTarget | null>(
    null,
  );
  const [announcement, setAnnouncement] = useState("");

  function clearDrag() {
    setDraggingSession(null);
    setDropTarget(null);
  }

  function handleDragStart(
    event: DragEvent<HTMLDivElement>,
    accountKey: string,
    sessionId: string,
  ) {
    setDraggingSession({ accountKey, sessionId });
    if (event.dataTransfer) {
      event.dataTransfer.effectAllowed = "move";
      event.dataTransfer.setData("text/plain", sessionId);
    }
  }

  function handleDragOver(
    event: DragEvent<HTMLDivElement>,
    accountKey: string,
    sessionId: string,
  ) {
    if (
      !enabled ||
      !draggingSession ||
      draggingSession.accountKey !== accountKey ||
      draggingSession.sessionId === sessionId
    ) {
      return;
    }
    event.preventDefault();
    if (event.dataTransfer) {
      event.dataTransfer.dropEffect = "move";
    }
    const rect = event.currentTarget.getBoundingClientRect();
    const edge: SessionDropEdge =
      event.clientY < rect.top + rect.height / 2 ? "before" : "after";
    setDropTarget((current) =>
      current?.accountKey === accountKey &&
      current.sessionId === sessionId &&
      current.edge === edge
        ? current
        : { accountKey, edge, sessionId },
    );
  }

  function handleDragLeave(
    event: DragEvent<HTMLDivElement>,
    accountKey: string,
    sessionId: string,
  ) {
    if (event.currentTarget.contains(event.relatedTarget as Node | null)) {
      return;
    }
    setDropTarget((current) =>
      current?.accountKey === accountKey && current.sessionId === sessionId
        ? null
        : current,
    );
  }

  function handleDrop(
    event: DragEvent<HTMLDivElement>,
    accountKey: string,
    sessionId: string,
  ) {
    if (!enabled || !draggingSession || draggingSession.accountKey !== accountKey) {
      return;
    }
    event.preventDefault();
    const edge =
      dropTarget?.accountKey === accountKey && dropTarget.sessionId === sessionId
        ? dropTarget.edge
        : "after";
    commitDrop(accountKey, draggingSession.sessionId, sessionId, edge);
  }

  function commitDrop(
    accountKey: string,
    sourceId: string,
    targetId: string,
    edge: SessionDropEdge,
  ) {
    clearDrag();
    const group = groups.find((item) => item.key === accountKey);
    if (!group) {
      return;
    }

    const currentOrder = group.sessions.map((session) => session.id);
    const nextOrder = moveSessionInOrder(currentOrder, sourceId, targetId, edge);
    if (nextOrder === currentOrder) {
      // 落点与现状等价：不写账目，也不把排序模式改成手动。
      return;
    }

    onReorder(accountKey, nextOrder);
    setAnnouncement(describeMoved(accountKey, sourceId));
  }

  function dragPropsFor(
    accountKey: string,
    sessionId: string,
  ): SidebarSessionDragRowProps {
    return {
      dragEnabled: enabled,
      dragging:
        draggingSession?.accountKey === accountKey &&
        draggingSession.sessionId === sessionId,
      dropEdge:
        dropTarget?.accountKey === accountKey &&
        dropTarget.sessionId === sessionId
          ? dropTarget.edge
          : null,
      onDragStart: (event) => handleDragStart(event, accountKey, sessionId),
      onDragOver: (event) => handleDragOver(event, accountKey, sessionId),
      onDragLeave: (event) => handleDragLeave(event, accountKey, sessionId),
      onDrop: (event) => handleDrop(event, accountKey, sessionId),
      onDragEnd: clearDrag,
    };
  }

  return { announcement, dragPropsFor };
}
