// Phase 1（合并方案 §3.6）：会话行的统一接线件。
// 行视图模型（标题 / 选中态 / 状态图标 / 相对时间 / 谱系）在这里结算，行 UI 仍是
// session-item.tsx 的 SidebarSessionItem——合并后同一个会话只渲染一行，
// 因此行能力（菜单 / 重命名 / 拖拽 / 谱系）只需要在这一处接线。

import { type TFunction } from "i18next";

import { type RuntimeSessionRecord } from "@/lib/runtime-api";
import { type SidebarSessionActivity } from "./session-row-status";
import { resolveSidebarSessionRowViewModel } from "./session-row-view-model";
import { SidebarSessionItem } from "./session-item";
import {
  type SidebarSessionDragRowProps,
} from "./use-session-drag-reorder";
import { type SidebarThread } from "./types";

export type WorkspaceSidebarSessionRowProps = {
  /** 本会话的本地活动快照（等待类最醒目）；缺省回落到元数据状态。 */
  activity?: SidebarSessionActivity | undefined;
  /** 来自 useSidebarSessionDrag 的行级拖拽属性；manual 模式下才存在。 */
  dragProps?: SidebarSessionDragRowProps | undefined;
  onArchive?: ((sessionId: string) => void) | undefined;
  /** §4.8：活动投影判定「运行中 / 等待类」时为 true，菜单出现「停止运行」。 */
  canStop?: boolean | undefined;
  onCancelRename: () => void;
  onDelete?: ((sessionId: string) => void) | undefined;
  /** Fork：带上源标题，供接线层生成「（分支）」后缀。 */
  onFork?: ((sessionId: string, sourceTitle: string) => void) | undefined;
  onRenameSubmit: (sessionId: string, title: string) => void;
  onRestore?: ((sessionId: string) => void) | undefined;
  onStop?: ((sessionId: string) => void) | undefined;
  onSelectThread: (threadId: string) => void;
  onStartRename: (sessionId: string) => void;
  renaming: boolean;
  selectedThreadId: string;
  session: RuntimeSessionRecord;
  sessionThread?: SidebarThread | undefined;
  t: TFunction<"workspace">;
  /** 同批可见行：谱系缩进与「子行紧随父行」同口径（缺省表示无同批上下文）。 */
  visibleSessions?: readonly RuntimeSessionRecord[] | undefined;
};

export function WorkspaceSidebarSessionRow({
  activity,
  canStop,
  dragProps,
  onArchive,
  onCancelRename,
  onDelete,
  onFork,
  onRenameSubmit,
  onRestore,
  onStop,
  onSelectThread,
  onStartRename,
  renaming,
  selectedThreadId,
  session,
  sessionThread,
  t,
  visibleSessions,
}: WorkspaceSidebarSessionRowProps) {
  const row = resolveSidebarSessionRowViewModel({
    activity,
    lineageRows: visibleSessions,
    selectedThreadId,
    session,
    sessionThread,
    t,
  });

  return (
    <SidebarSessionItem
      actionLabels={{
        archivedBadge: t("sidebar.session.archivedBadge"),
        archive: t("sidebar.session.archive"),
        delete: t("sidebar.session.delete"),
        fork: t("sidebar.session.fork"),
        menu: t("sidebar.session.menu"),
        restore: t("sidebar.session.restore"),
        stop: t("sidebar.session.stop"),
      }}
      {...dragProps}
      canStop={canStop}
      isActive={row.isActive}
      lineage={row.lineage}
      onArchive={onArchive}
      onCancelRename={onCancelRename}
      onDelete={onDelete}
      onFork={
        onFork ? (sessionId) => onFork(sessionId, row.title) : undefined
      }
      onRenameSubmit={(sessionId, value) => onRenameSubmit(sessionId, value)}
      onRestore={onRestore}
      onStop={onStop}
      onSelect={() => onSelectThread(row.thread?.id ?? session.id)}
      onStartRename={onStartRename}
      renameLabels={{
        placeholder: t("sidebar.session.renamePlaceholder"),
        rename: t("sidebar.session.rename"),
      }}
      renaming={renaming}
      rowState={row.rowState}
      session={session}
      statusIcon={row.statusIcon}
      time={row.itemTime}
      title={row.title}
    />
  );
}
