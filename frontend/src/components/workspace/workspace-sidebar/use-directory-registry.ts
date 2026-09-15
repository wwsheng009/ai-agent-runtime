// Phase 2（合并方案 §3.5-D/E）：把「工作目录注册表」的接线状态与动作从 workspace-sidebar.tsx
// 下沉到本 hook（与 use-session-group-view / use-sidebar-effects 同一分层），
// 使入口文件在合并段之后重新落回「单文件非空行 ≤ 500」门禁以内。
//
// 覆盖范围：添加弹层 / 管理弹层 / 移除确认弹层的开关、目录行内重命名、目录内新建会话、
// 以及侧栏就地错误条（目录动作与会话重命名共用同一条提示）。

import { useMemo, useState } from "react";

import { type SidebarUnregisteredDirectory } from "@/components/workspace/workspace-directory-manage-dialog";
import { type MergedDirectoryGroup } from "@/components/workspace/workspace-sidebar-shared";
import { type RuntimeWorkspaceDirectory } from "@/lib/runtime-api";

import { type SidebarDirectoryDeleteTarget } from "./directories-section";
import { type WorkspaceDirectoryCreateRequest } from "./types";

type UseDirectoryRegistryParams = {
  mergedDirectoryGroups: MergedDirectoryGroup[];
  onRenameWorkspaceDirectory: (id: string, name: string) => Promise<void>;
  onCreateSessionInDirectory: (
    request: WorkspaceDirectoryCreateRequest,
  ) => Promise<void>;
  /** 方案 §12-A：把一个「仅由会话派生」的目录注册进注册表（POST + 刷新列表）。 */
  onRegisterWorkspaceDirectory: (path: string) => Promise<unknown>;
};

export function useDirectoryRegistry({
  mergedDirectoryGroups,
  onRenameWorkspaceDirectory,
  onCreateSessionInDirectory,
  onRegisterWorkspaceDirectory,
}: UseDirectoryRegistryParams) {
  const [directoryAddOpen, setDirectoryAddOpen] = useState(false);
  /** 平铺视图没有组头（方案 §3.5-D），目录管理由段内「管理目录」弹层承载。 */
  const [directoryManageOpen, setDirectoryManageOpen] = useState(false);
  const [directoryDeleteTarget, setDirectoryDeleteTarget] =
    useState<SidebarDirectoryDeleteTarget | null>(null);
  const [renamingDirectoryId, setRenamingDirectoryId] = useState<string | null>(
    null,
  );
  const [submittingDirectoryRename, setSubmittingDirectoryRename] =
    useState(false);
  const [creatingSessionKey, setCreatingSessionKey] = useState<string | null>(
    null,
  );
  /** 侧栏就地错误条：目录动作与会话重命名共用（合并为单段后只剩一条错误条）。 */
  const [sidebarActionError, setSidebarActionError] = useState<string | null>(
    null,
  );

  function startDirectoryRename(group: MergedDirectoryGroup) {
    if (!group.directoryId) {
      return;
    }
    setSidebarActionError(null);
    setRenamingDirectoryId(group.directoryId);
  }

  function cancelDirectoryRename() {
    setRenamingDirectoryId(null);
  }

  async function commitDirectoryRename(nextName: string) {
    const directoryId = renamingDirectoryId;
    const trimmedName = nextName.trim();
    if (!directoryId || submittingDirectoryRename) {
      return;
    }
    if (!trimmedName) {
      cancelDirectoryRename();
      return;
    }
    setSubmittingDirectoryRename(true);
    try {
      await onRenameWorkspaceDirectory(directoryId, trimmedName);
      cancelDirectoryRename();
    } catch (renameError) {
      setSidebarActionError(
        renameError instanceof Error ? renameError.message : String(renameError),
      );
    } finally {
      setSubmittingDirectoryRename(false);
    }
  }

  /** 管理目录弹层：按 registry id 统计当前可见会话数（与组头计数同口径）。 */
  const directorySessionCounts = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const group of mergedDirectoryGroups) {
      if (group.directoryId) {
        counts[group.directoryId] = group.sessions.length;
      }
    }
    return counts;
  }, [mergedDirectoryGroups]);

  /**
   * 方案 §12-A：管理弹层里「未注册（来自会话）」分区的数据源 —— 只取会话派生组
   * （`registered: false` 且带真实路径，排除无路径的 Unscoped 桶）。
   * 注册成功后注册表刷新 → 同路径的派生组被合并进注册组，本列表自动少一行。
   */
  const unregisteredDirectories = useMemo<SidebarUnregisteredDirectory[]>(() => {
    const entries: SidebarUnregisteredDirectory[] = [];
    for (const group of mergedDirectoryGroups) {
      if (group.registered || !group.fullPath) {
        continue;
      }
      entries.push({
        path: group.fullPath,
        label: group.label,
        sessionCount: group.sessions.length,
      });
    }
    return entries;
  }, [mergedDirectoryGroups]);

  /**
   * 目录内新建会话的统一链路（组头 hover 与「管理目录」弹层共用）。
   * 成功返回 true；失败把消息落到侧栏错误条并返回 false（调用方据此决定是否收起弹层）。
   */
  async function createSessionInDirectory(
    request: WorkspaceDirectoryCreateRequest,
  ): Promise<boolean> {
    if (!request.path || creatingSessionKey) {
      return false;
    }
    setSidebarActionError(null);
    setCreatingSessionKey(request.directoryId ?? request.path);
    try {
      await onCreateSessionInDirectory(request);
      return true;
    } catch (createError) {
      setSidebarActionError(
        createError instanceof Error ? createError.message : String(createError),
      );
      return false;
    } finally {
      setCreatingSessionKey(null);
    }
  }

  function handleCreateSessionInDirectory(
    group: MergedDirectoryGroup,
  ): Promise<void> {
    return createSessionInDirectory({
      path: group.fullPath,
      directoryId: group.directoryId,
      label: group.label,
    }).then(() => undefined);
  }

  /**
   * 弹层发起的新建会话：无论成功失败都收起弹层——成功时新会话会被选中，
   * 失败时错误条在侧栏上（弹层会把它遮住）。
   */
  async function handleCreateSessionFromManager(
    request: WorkspaceDirectoryCreateRequest,
  ) {
    try {
      await createSessionInDirectory(request);
    } finally {
      setDirectoryManageOpen(false);
    }
  }

  /** 管理目录弹层：按 id 直接重命名（不经过段内的行内编辑状态机）。 */
  async function renameDirectoryById(directoryId: string, nextName: string) {
    const trimmedName = nextName.trim();
    if (!directoryId || !trimmedName || submittingDirectoryRename) {
      return;
    }
    setSubmittingDirectoryRename(true);
    try {
      await onRenameWorkspaceDirectory(directoryId, trimmedName);
      setSidebarActionError(null);
    } catch (renameError) {
      setSidebarActionError(
        renameError instanceof Error ? renameError.message : String(renameError),
      );
      setDirectoryManageOpen(false);
    } finally {
      setSubmittingDirectoryRename(false);
    }
  }

  function handleRequestRemoveDirectory(directory: RuntimeWorkspaceDirectory) {
    setDirectoryManageOpen(false);
    setDirectoryDeleteTarget({
      id: directory.id,
      label: directory.name?.trim() || directory.path,
      fullPath: directory.path,
      sessionCount: directorySessionCounts[directory.id] ?? 0,
    });
  }

  /**
   * 方案 §12-A：注册一个会话派生目录。失败时把消息落到侧栏错误条**并继续抛出**，
   * 让弹层就地显示（弹层遮住侧栏，单靠错误条用户看不到）；成功后注册表刷新，
   * 该路径在合并分组里从「派生」变「已注册」，弹层对应行随之消失。
   */
  async function registerDirectoryFromManager(path: string): Promise<void> {
    const trimmedPath = path.trim();
    if (!trimmedPath) {
      return;
    }
    setSidebarActionError(null);
    try {
      await onRegisterWorkspaceDirectory(trimmedPath);
    } catch (registerError) {
      const message =
        registerError instanceof Error
          ? registerError.message
          : String(registerError);
      setSidebarActionError(message);
      throw registerError;
    }
  }

  return {
    cancelDirectoryRename,
    commitDirectoryRename,
    creatingSessionKey,
    directoryAddOpen,
    directoryDeleteTarget,
    directoryManageOpen,
    directorySessionCounts,
    handleCreateSessionFromManager,
    handleCreateSessionInDirectory,
    handleRequestRemoveDirectory,
    registerDirectoryFromManager,
    renameDirectoryById,
    renamingDirectoryId,
    setDirectoryAddOpen,
    setDirectoryDeleteTarget,
    setDirectoryManageOpen,
    setSidebarActionError,
    sidebarActionError,
    startDirectoryRename,
    unregisteredDirectories,
  };
}
