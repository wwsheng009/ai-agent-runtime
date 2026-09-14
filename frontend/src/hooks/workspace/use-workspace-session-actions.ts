// P1-9：会话行动作（重命名 / 归档 / 归档恢复 / 删除 / 目录内新建）自
// pages/workspace-page.tsx 机械拆分而来（P0-2 A2 复检处置），仅搬迁不改语义。

import type { Dispatch, SetStateAction } from "react";

import {
  activateRuntimeSession,
  archiveRuntimeSession,
  deleteRuntimeSession,
} from "@/api/runtime/sessions";
import {
  resolveSelectionAfterSessionDelete,
} from "@/components/workspace/workspace-sidebar/session-row-actions";
import type { Thread } from "@/data/mock";
import { createRuntimeSession, updateRuntimeSession } from "@/lib/runtime-api";
import { normalizeSessionId } from "@/lib/session-id";
import { useNavigate } from "react-router-dom";

type UseWorkspaceSessionActionsOptions = {
  /** 当前选中会话（线程 sessionId / id / 路由参数的归一值）：删除它时回到工作台首页。 */
  activeSessionId: string;
  /** 运行客户端身份里的用户 id（`selectedUserId` 为空时的回落）。 */
  clientUserId: string;
  /** 轨迹硬重置（会话切换后事件日志独立自增，必须显式清游标）。 */
  onResetTrajectory: () => void;
  /** 会话列表刷新（重命名 / 归档 / 删除后统一走快照刷新）。 */
  refreshSessions: () => void;
  /** 多用户视图下选中的用户 id（优先于 clientUserId）。 */
  selectedUserId: string;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
};

export function useWorkspaceSessionActions({
  activeSessionId,
  clientUserId,
  onResetTrajectory,
  refreshSessions,
  selectedUserId,
  setThreads,
}: UseWorkspaceSessionActionsOptions) {
  const navigate = useNavigate();

  async function handleRenameRuntimeSession(sessionId: string, title: string) {
    await updateRuntimeSession(sessionId, { title });
    refreshSessions();
    setThreads((current) =>
      current.map((thread) =>
        normalizeSessionId(thread.sessionId || thread.id) === sessionId
          ? { ...thread, title }
          : thread,
      ),
    );
  }

  // P2-6 子片 2：跨组移动只改写归属路径（后端按 context 键逐键合并，其它键不受影响），
  // 不触碰标题 / 状态；刷新快照让新归属进入同一排序/分组口径。
  async function handleMoveRuntimeSession(
    sessionId: string,
    workspacePath: string,
  ) {
    await updateRuntimeSession(sessionId, {
      context: { workspace_path: workspacePath },
    });
    refreshSessions();
  }

  // P1-9：归档为可恢复的非破坏操作，完成后刷新列表（新的 state 由快照统一呈现）。
  async function handleArchiveRuntimeSession(sessionId: string) {
    await archiveRuntimeSession(sessionId);
    refreshSessions();
  }

  async function handleRestoreRuntimeSession(sessionId: string) {
    await activateRuntimeSession(sessionId);
    refreshSessions();
  }

  // P1-9：非破坏删除——仅移除会话记录（不连带目录与磁盘数据）。
  // 删除的恰好是当前会话时清空本地线程并回到工作台首页，避免路由悬空。
  async function handleDeleteRuntimeSession(sessionId: string) {
    await deleteRuntimeSession(sessionId);
    setThreads((current) =>
      current.filter(
        (thread) =>
          normalizeSessionId(thread.sessionId || thread.id) !== sessionId,
      ),
    );
    refreshSessions();
    if (resolveSelectionAfterSessionDelete(activeSessionId, sessionId) === null) {
      onResetTrajectory();
      navigate("/workspace/chats/new");
    }
  }

  async function handleCreateSessionInDirectory(request: {
    path: string;
    directoryId?: string;
    label: string;
  }) {
    const response = await createRuntimeSession({
      title: request.label,
      user_id: selectedUserId || clientUserId,
      workspace_path: request.path || undefined,
      directory_id: request.directoryId,
    });
    refreshSessions();
    const createdSessionId = normalizeSessionId(response.session?.id ?? "");
    if (createdSessionId) {
      // 会话已绑定目录；直接跳转到 canonical 会话路由，等待 sessions
      // 刷新后由 mergeRuntimeSessionsIntoThreads 生成对应线程。
      onResetTrajectory();
      navigate(`/workspace/sessions/${encodeURIComponent(createdSessionId)}`);
    }
  }

  return {
    archiveSession: handleArchiveRuntimeSession,
    createSessionInDirectory: handleCreateSessionInDirectory,
    deleteSession: handleDeleteRuntimeSession,
    moveSession: handleMoveRuntimeSession,
    renameSession: handleRenameRuntimeSession,
    restoreSession: handleRestoreRuntimeSession,
  };
}