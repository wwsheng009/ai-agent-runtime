// P2-6 子片 3：组内展开 / 折叠的呈现状态（会话期本地状态，不落存储）。
// 渲染期不写任何地方；展开键按目录路径记忆，目录消失只影响下一次呈现。

import { useCallback, useState } from "react";

import {
  resolveSessionGroupVisibility,
  type SessionGroupVisibility,
} from "@/lib/workspace/session-grouping";

export type SessionGroupVisibilityController = {
  /** 展开 / 收起某个目录组（同一组内两次调用回到原态）。 */
  toggle: (groupKey: string) => void;
  /** 取该组当下的可见性判定（含「选中会话落在折叠区则自动展开」）。 */
  visibilityFor: <T extends { id: string }>(
    groupKey: string,
    sessions: readonly T[],
    pinnedId?: string | null,
  ) => SessionGroupVisibility<T>;
};

export function useSessionGroupVisibility(): SessionGroupVisibilityController {
  const [expandedGroupKeys, setExpandedGroupKeys] = useState<
    Record<string, boolean>
  >({});

  const toggle = useCallback((groupKey: string) => {
    setExpandedGroupKeys((current) => ({
      ...current,
      [groupKey]: current[groupKey] !== true,
    }));
  }, []);

  const visibilityFor = useCallback(
    <T extends { id: string }>(
      groupKey: string,
      sessions: readonly T[],
      pinnedId?: string | null,
    ) =>
      resolveSessionGroupVisibility(sessions, {
        expanded: expandedGroupKeys[groupKey] === true,
        pinnedId,
      }),
    [expandedGroupKeys],
  );

  return { toggle, visibilityFor };
}
