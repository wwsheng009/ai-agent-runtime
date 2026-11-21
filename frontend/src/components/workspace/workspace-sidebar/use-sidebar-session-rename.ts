// 侧栏会话行内重命名的状态与动作。由 workspace-sidebar.tsx 机械拆分（P0-2），
// 仅搬迁不改语义：用渲染行 key 区分「最近会话」快捷行与目录树中的同一会话副本。

import { useState } from "react";

type UseSidebarSessionRenameParams = {
  onRenameRuntimeSession: (sessionId: string, title: string) => Promise<void>;
  setSidebarActionError: (message: string | null) => void;
};

export function useSidebarSessionRename({
  onRenameRuntimeSession,
  setSidebarActionError,
}: UseSidebarSessionRenameParams) {
  const [renamingSessionKey, setRenamingSessionKey] = useState<string | null>(
    null,
  );

  async function handleRenameSession(sessionId: string, title: string) {
    setRenamingSessionKey(null);
    try {
      await onRenameRuntimeSession(sessionId, title);
    } catch (renameError) {
      setSidebarActionError(
        renameError instanceof Error ? renameError.message : String(renameError),
      );
    }
  }

  function startSessionRename(renameKey: string) {
    setSidebarActionError(null);
    setRenamingSessionKey(renameKey);
  }

  function cancelSessionRename() {
    setRenamingSessionKey(null);
  }

  return {
    cancelSessionRename,
    handleRenameSession,
    renamingSessionKey,
    startSessionRename,
  };
}