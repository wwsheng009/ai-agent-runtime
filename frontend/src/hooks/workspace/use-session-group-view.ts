// P2-6 子片 2：侧栏会话**列表视图**的组装与跨组移动编排。
// 从 workspace-sidebar.tsx 拆出（P0-2 单文件非空行门禁）：分组视图模式、组内顺序账目、
// 乐观归属覆盖与 Host 写回都收在这一层，入口组件只做装配与装配参数透传。
//
// 事实源与边界：
// - 分组归属仍由会话元数据的工作目录决定（本层不改口径，只做投影）；
// - 乐观覆盖只作用于**可见会话**；Host 快照回收由 `pruneSessionMoveOverrides` 裁决；
// - 目标目录可归属性由 `resolveSessionMoveTarget` 判定，不伪造落点、不做假回滚。

import { useCallback, useMemo, useState } from "react";
import { type TFunction } from "i18next";

import {
  applySessionWorkspaceOverrides,
  mergeDirectoryGroups,
  pruneSessionMoveOverrides,
  resolveSessionMoveTarget,
  type MergedDirectoryGroup,
  type SessionMoveOverrides,
} from "@/components/workspace/workspace-sidebar-shared";
import { useSessionGrouping } from "@/hooks/workspace/use-session-grouping";
import { useSessionOrder } from "@/hooks/workspace/use-session-order";
import { type SessionGroupingMode } from "@/lib/workspace/session-grouping";
import {
  promoteBlankSessions,
  type SessionOrderAccount,
  type SessionOrderMode,
} from "@/lib/workspace/session-order";
import {
  type RuntimeSessionRecord,
  type RuntimeWorkspaceDirectory,
} from "@/types/runtime";

type UseSessionGroupViewArgs = {
  directories: readonly RuntimeWorkspaceDirectory[];
  /** 可见会话（已按归档开关过滤）：乐观覆盖只作用于此集合。 */
  visibleSessions: readonly RuntimeSessionRecord[];
  /** Host 快照原始会话：用于回收已被 Host 确认（或会话已消失）的乐观覆盖。 */
  runtimeSessions: readonly RuntimeSessionRecord[];
  /** 缺失时跨组移动整体关闭（只读浏览，不产生落点）。 */
  onMoveRuntimeSession?:
    | ((sessionId: string, workspacePath: string) => Promise<void> | void)
    | undefined;
  t: TFunction<"workspace">;
};

export type SessionGroupViewController = {
  /** 跨组移动：目标目录是否可归属（未登记 / 宿主缺位的目录不可作落点）。 */
  canMoveSessionToGroup: (groupKey: string) => boolean;
  /** 组内顺序账目写入（拖拽重排）。 */
  commitOrder: (accountKey: string, order: SessionOrderAccount) => void;
  groupingMode: SessionGroupingMode;
  setGroupingMode: (mode: SessionGroupingMode) => void;
  /** 目录分组（含空组，供目录段与展开态判定复用）。 */
  mergedDirectoryGroups: MergedDirectoryGroup[];
  /** 跨组移动意图：乐观归属 + Host 写回（失败回滚并就地报错）。 */
  moveSessionToGroup: (sessionId: string, groupKey: string) => Promise<void>;
  orderMode: SessionOrderMode;
  setOrderMode: (mode: SessionOrderMode) => void;
  /** 会话段使用的分组序列：只保留有会话的组，且已按排序模式结算。 */
  sessionGroups: MergedDirectoryGroup[];
  /** 跨组移动失败的就地提示（失败即回滚）。 */
  sessionMoveError: string | null;
};

export function useSessionGroupView({
  directories,
  onMoveRuntimeSession,
  runtimeSessions,
  t,
  visibleSessions,
}: UseSessionGroupViewArgs): SessionGroupViewController {
  const { mode: groupingMode, setMode: setGroupingMode } = useSessionGrouping();
  const {
    accounts: orderAccounts,
    commitOrder,
    mode: orderMode,
    orderFor,
    setMode: setOrderMode,
  } = useSessionOrder();
  /** 跨组移动的乐观覆盖（sessionId → 目标工作目录路径）。 */
  const [moveOverrides, setMoveOverrides] = useState<SessionMoveOverrides>({});
  const [sessionMoveError, setSessionMoveError] = useState<string | null>(null);

  // Host 快照已反映目标路径（或会话已消失）时回收乐观覆盖：在渲染期归约，
  // 而不是 effect 里 setState（避免级联渲染）；无变更时保持同一引用。
  const settledMoveOverrides = useMemo(
    () => pruneSessionMoveOverrides(moveOverrides, runtimeSessions),
    [moveOverrides, runtimeSessions],
  );
  const overriddenSessions = useMemo(
    () => applySessionWorkspaceOverrides(visibleSessions, settledMoveOverrides),
    [settledMoveOverrides, visibleSessions],
  );
  const mergedDirectoryGroups = useMemo(
    () => mergeDirectoryGroups(directories, overriddenSessions),
    [directories, overriddenSessions],
  );
  // Per-user session browser: skip registered directories without sessions
  // so the friendly empty state survives directory-only registrations.
  const sessionGroups = useMemo(
    () => mergedDirectoryGroups.filter((group) => group.sessions.length > 0),
    [mergedDirectoryGroups],
  );
  // 排序模式（设置域）+ 手动顺序账目（浏览器本地）：只重排分组内会话，不改分组口径。
  // 空白新会话（尚无任何消息）钉在组顶，获得首条消息后自然落入下面的排序结果；
  // 用户已在手动账目里显式摆放过的不提升（显式意图优先于呈现层）。
  const orderedSessionGroups = useMemo(
    () =>
      sessionGroups.map((group) => ({
        ...group,
        sessions: [
          ...promoteBlankSessions(orderFor(group.key, group.sessions), {
            anchoredIds: orderAccounts[group.key],
          }),
        ],
      })),
    [orderAccounts, orderFor, sessionGroups],
  );

  const resolveTarget = useCallback(
    (groupKey: string) => {
      const group = sessionGroups.find((item) => item.key === groupKey);
      return group
        ? resolveSessionMoveTarget(group, directories)
        : null;
    },
    [directories, sessionGroups],
  );

  const canMoveSessionToGroup = useCallback(
    (groupKey: string) => {
      if (!onMoveRuntimeSession) {
        return false;
      }
      return Boolean(resolveTarget(groupKey));
    },
    [onMoveRuntimeSession, resolveTarget],
  );

  const moveSessionToGroup = useCallback(
    async (sessionId: string, groupKey: string) => {
      const target = resolveTarget(groupKey);
      if (!target || !onMoveRuntimeSession) {
        return;
      }
      setSessionMoveError(null);
      // 乐观归属：分组视图立即落到目标组；失败回滚 = 撤销覆盖，由 Host 快照重新裁决。
      setMoveOverrides((current) => ({
        ...current,
        [sessionId]: target.path,
      }));
      try {
        await onMoveRuntimeSession(sessionId, target.path);
      } catch (moveError) {
        setMoveOverrides((current) => {
          if (!(sessionId in current)) {
            return current;
          }
          const next = { ...current };
          delete next[sessionId];
          return next;
        });
        setSessionMoveError(
          t("sidebar.sessionMove.failed", {
            message:
              moveError instanceof Error ? moveError.message : String(moveError),
          }),
        );
      }
    },
    [onMoveRuntimeSession, resolveTarget, t],
  );

  return {
    canMoveSessionToGroup,
    commitOrder,
    groupingMode,
    mergedDirectoryGroups,
    moveSessionToGroup,
    orderMode,
    sessionGroups: orderedSessionGroups,
    sessionMoveError,
    setGroupingMode,
    setOrderMode,
  };
}
