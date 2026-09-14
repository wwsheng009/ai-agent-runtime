// P2-6 子片 1/2：侧栏会话行的拖拽编排（浏览器原生 HTML5 DnD，不引入依赖）。
// 边界（与批次范围一致）：
// - 组内落点：结算顺序数组，是否写账目 / 是否切模式由接线层决定；
// - 跨组落点（仅「按目录」视图、且目标目录可归属时）：结算目标组顺序 + 移动意图，
//   乐观归属与 Host 写回由接线层执行（本 hook 不碰网络、不写存储）；
// - 未登记归属路径（Unscoped）与宿主缺位的目录不作落点：不 preventDefault，浏览器如实
//   呈现「不可放置」，不伪造落点、不做假回滚；
// - 平铺视图不接收跨组落点（组边界不可见，跨组移动只在按目录视图可表达）；
// - 落点与现状等价时不发通知（不产生空写入，也不触发播报）。
//
// 结算基于调用方传入的**当前可见顺序**（已按模式结算过的分组），因此拖拽落点稳定可预期。

import { useState, type DragEvent } from "react";

import {
  insertSessionInOrder,
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

/** 跨组移动意图：目标分组 + 落点（行内前后侧）。 */
export type SidebarSessionMoveTarget = {
  groupKey: string;
  edge: SessionDropEdge;
  sessionId: string;
};

/** 直接展开到会话行上的拖拽属性（缺省形态与不接线时一致）。 */
export type SidebarSessionDragRowProps = {
  dragEnabled: boolean;
  dragging: boolean;
  dropEdge: SessionDropEdge | null;
  onDragEnd: () => void;
  // 展开目标不限定标签：同一组属性既落在 div 行上，也落在 button 组头上。
  onDragLeave: (event: DragEvent<HTMLElement>) => void;
  onDragOver: (event: DragEvent<HTMLElement>) => void;
  onDragStart: (event: DragEvent<HTMLElement>) => void;
  onDrop: (event: DragEvent<HTMLElement>) => void;
};

/** 直接展开到目录组头上的拖拽属性（跨组移动的「追加到该组末尾」落点）。 */
export type SidebarSessionDragGroupProps<
  T extends HTMLElement = HTMLElement,
> = {
  dropActive: boolean;
  onDragLeave: (event: DragEvent<T>) => void;
  onDragOver: (event: DragEvent<T>) => void;
  onDrop: (event: DragEvent<T>) => void;
};

type UseSidebarSessionDragArgs = {
  /** 手动模式才可拖：关闭时不渲染拖拽 affordance，也不接收落点。 */
  enabled: boolean;
  groups: readonly SidebarSessionDragGroup[];
  onReorder: (accountKey: string, order: readonly string[]) => void;
  /** 跨组移动是否可表达（按目录视图 + 接线层提供移动回调）。 */
  crossGroupEnabled?: boolean;
  /** 目标分组当前是否可接收跨组会话（未登记 / 宿主缺位的目录返回 false）。 */
  canMoveToGroup?: (groupKey: string) => boolean;
  /** 跨组移动意图（目标组顺序已写入账目后调用）。 */
  onMoveSession?: (sessionId: string, target: SidebarSessionMoveTarget) => void;
  /** 结算播报文案（已本地化），只在真正发生位移时取用。 */
  describeMoved: (accountKey: string, sessionId: string) => string;
  /** 跨组移动的播报文案（已本地化），只在真正发生跨组位移时取用。 */
  describeMovedToGroup?: (sessionId: string, groupKey: string) => string;
};

export type SidebarSessionDragController = {
  /** 无障碍播报内容（空串表示无新播报）。 */
  announcement: string;
  dragPropsFor: (accountKey: string, sessionId: string) => SidebarSessionDragRowProps;
  groupDropPropsFor: (groupKey: string) => SidebarSessionDragGroupProps;
};

export function useSidebarSessionDrag({
  canMoveToGroup,
  crossGroupEnabled = false,
  describeMoved,
  describeMovedToGroup,
  enabled,
  groups,
  onMoveSession,
  onReorder,
}: UseSidebarSessionDragArgs): SidebarSessionDragController {
  const [draggingSession, setDraggingSession] = useState<{
    accountKey: string;
    sessionId: string;
  } | null>(null);
  const [dropTarget, setDropTarget] = useState<SidebarSessionDragTarget | null>(
    null,
  );
  const [groupDropTarget, setGroupDropTarget] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState("");

  function clearDrag() {
    setDraggingSession(null);
    setDropTarget(null);
    setGroupDropTarget(null);
  }

  /** 该分组此刻能否接收跨组会话（跨组能力关闭时一律不可）。 */
  function acceptsCrossGroup(groupKey: string): boolean {
    if (!enabled || !crossGroupEnabled || !onMoveSession || !canMoveToGroup) {
      return false;
    }
    return canMoveToGroup(groupKey);
  }

  function handleDragStart(
    event: DragEvent<HTMLElement>,
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
    event: DragEvent<HTMLElement>,
    accountKey: string,
    sessionId: string,
  ) {
    if (
      !enabled ||
      !draggingSession ||
      draggingSession.sessionId === sessionId
    ) {
      return;
    }
    const sameGroup = draggingSession.accountKey === accountKey;
    if (!sameGroup && !acceptsCrossGroup(accountKey)) {
      return;
    }
    event.preventDefault();
    if (event.dataTransfer) {
      event.dataTransfer.dropEffect = "move";
    }
    setGroupDropTarget(null);
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
    event: DragEvent<HTMLElement>,
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
    event: DragEvent<HTMLElement>,
    accountKey: string,
    sessionId: string,
  ) {
    if (!enabled || !draggingSession) {
      return;
    }
    const sameGroup = draggingSession.accountKey === accountKey;
    if (!sameGroup && !acceptsCrossGroup(accountKey)) {
      return;
    }
    event.preventDefault();
    const edge =
      dropTarget?.accountKey === accountKey && dropTarget.sessionId === sessionId
        ? dropTarget.edge
        : "after";
    if (sameGroup) {
      commitDrop(accountKey, draggingSession.sessionId, sessionId, edge);
      return;
    }
    commitCrossGroupDrop(accountKey, draggingSession.sessionId, {
      groupKey: accountKey,
      edge,
      sessionId,
    });
  }

  function handleGroupDragOver(event: DragEvent<HTMLElement>, groupKey: string) {
    if (
      !draggingSession ||
      draggingSession.accountKey === groupKey ||
      !acceptsCrossGroup(groupKey)
    ) {
      return;
    }
    event.preventDefault();
    if (event.dataTransfer) {
      event.dataTransfer.dropEffect = "move";
    }
    setDropTarget(null);
    setGroupDropTarget((current) => (current === groupKey ? current : groupKey));
  }

  function handleGroupDragLeave(
    event: DragEvent<HTMLElement>,
    groupKey: string,
  ) {
    if (event.currentTarget.contains(event.relatedTarget as Node | null)) {
      return;
    }
    setGroupDropTarget((current) => (current === groupKey ? null : current));
  }

  /** 组头落点：追加到该组末尾（组内无会话时不结算，避免凭空造位置）。 */
  function handleGroupDrop(event: DragEvent<HTMLElement>, groupKey: string) {
    if (
      !draggingSession ||
      draggingSession.accountKey === groupKey ||
      !acceptsCrossGroup(groupKey)
    ) {
      return;
    }
    const sessionId = draggingSession.sessionId;
    const group = groups.find((item) => item.key === groupKey);
    const lastSessionId = group?.sessions.at(-1)?.id;
    if (!lastSessionId) {
      return;
    }
    event.preventDefault();
    commitCrossGroupDrop(groupKey, sessionId, {
      groupKey,
      edge: "after",
      sessionId: lastSessionId,
    });
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

  /** 跨组落点：目标组顺序落到本地账目，再交回接线层做乐观归属与 Host 写回。 */
  function commitCrossGroupDrop(
    groupKey: string,
    sourceId: string,
    target: SidebarSessionMoveTarget,
  ) {
    clearDrag();
    const group = groups.find((item) => item.key === groupKey);
    if (!group || !onMoveSession) {
      return;
    }

    const targetOrder = group.sessions.map((session) => session.id);
    const nextOrder = insertSessionInOrder(
      targetOrder,
      sourceId,
      target.sessionId,
      target.edge,
    );
    if (nextOrder !== targetOrder) {
      // 顺序账目先落本地（该会话此前的组内顺序由 Host 归属切换自然失效）。
      onReorder(groupKey, nextOrder);
    }
    onMoveSession(sourceId, target);
    setAnnouncement(
      describeMovedToGroup
        ? describeMovedToGroup(sourceId, groupKey)
        : describeMoved(groupKey, sourceId),
    );
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

  function groupDropPropsFor(groupKey: string): SidebarSessionDragGroupProps {
    return {
      dropActive: groupDropTarget === groupKey,
      onDragOver: (event) => handleGroupDragOver(event, groupKey),
      onDragLeave: (event) => handleGroupDragLeave(event, groupKey),
      onDrop: (event) => handleGroupDrop(event, groupKey),
    };
  }

  return { announcement, dragPropsFor, groupDropPropsFor };
}
