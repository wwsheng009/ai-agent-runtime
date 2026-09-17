// Batch 3（§4.5）切线守卫：多会话注册表关闭时保留「切走会打断」的确认框；
// 打开后不再打断——切走的会话在后台继续跑，侧栏以「运行中 / 等待审批」提示。
//
// P0-2 拆分：自 `pages/workspace-page.tsx` 机械搬迁而来，仅搬迁不改语义。

import { useState } from "react";

import type { Thread } from "@/data/mock";
import { shouldConfirmThreadSwitch } from "@/hooks/workspace/use-workspace-thread-selection";
import { MULTI_SESSION_REGISTRY_ENABLED } from "@/lib/session-runtime/flags";

export type PendingSessionSwitch = {
  threadId: string;
  title: string;
};

export type UseWorkspaceSessionSwitchGuardOptions = {
  /** 选中会话是否在途（本地 + 续传）：只在「会打断在途回合」时弹确认框。 */
  currentSessionResponding: boolean;
  /** 真正执行切换（store 池按会话保留快照，不再 reset）。 */
  onSelectThread: (threadId: string) => void;
  selectedThread: Thread | null;
  threads: Thread[];
};

export type WorkspaceSessionSwitchGuard = {
  pendingSessionSwitch: PendingSessionSwitch | null;
  cancelSessionSwitch: () => void;
  confirmSessionSwitch: () => void;
  /** 侧栏 / 列表点击入口：按开关与在途状态决定直切还是弹确认框。 */
  selectThread: (threadId: string) => void;
};

export function useWorkspaceSessionSwitchGuard({
  currentSessionResponding,
  onSelectThread,
  selectedThread,
  threads,
}: UseWorkspaceSessionSwitchGuardOptions): WorkspaceSessionSwitchGuard {
  const [pendingSessionSwitch, setPendingSessionSwitch] =
    useState<PendingSessionSwitch | null>(null);

  // 左侧会话列表点击其它会话：当前会话仍在生成回复时先弹确认对话框，确认后
  // 才真正切换（轨迹重置同样延后到确认之后，避免取消时已经丢掉当前会话的
  // 轨迹游标）；当前会话空闲时直接切换，不打断用户。
  function selectThread(threadId: string) {
    // §4.5（Batch 3）：多会话注册表开启后不再弹打断式确认框——切走的会话在后台
    // 继续跑，侧栏以「运行中 / 等待审批」提示（回滚 UI 开关：确认框 ↔ 后台提示
    // 二选一，即本分支）。
    if (
      MULTI_SESSION_REGISTRY_ENABLED ||
      !shouldConfirmThreadSwitch(
        selectedThread?.id,
        threadId,
        currentSessionResponding,
      )
    ) {
      onSelectThread(threadId);
      return;
    }

    const targetThread = threads.find((thread) => thread.id === threadId);
    setPendingSessionSwitch({
      threadId,
      title: targetThread?.title ?? threadId,
    });
  }

  function confirmSessionSwitch() {
    const pending = pendingSessionSwitch;
    setPendingSessionSwitch(null);
    if (pending) {
      onSelectThread(pending.threadId);
    }
  }

  function cancelSessionSwitch() {
    setPendingSessionSwitch(null);
  }

  return {
    pendingSessionSwitch,
    cancelSessionSwitch,
    confirmSessionSwitch,
    selectThread,
  };
}
