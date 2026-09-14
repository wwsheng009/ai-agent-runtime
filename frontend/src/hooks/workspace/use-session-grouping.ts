// P2-6 子片 2：侧栏会话分组视图的接线层。
// 与排序模式一样走设置域（持久化在 AppSettings 里）：渲染期只读，切换即写入。

import { useCallback } from "react";

import { useAppSettings } from "@/core/settings";
import {
  isSessionGroupingMode,
  type SessionGroupingMode,
} from "@/lib/workspace/session-grouping";

export type SessionGroupingController = {
  /** 当前分组视图（设置域，跨会话持久化）。 */
  mode: SessionGroupingMode;
  setMode: (mode: SessionGroupingMode) => void;
};

export function useSessionGrouping(): SessionGroupingController {
  const { settings, updateSection } = useAppSettings();
  const stored = settings.workspace.sessionGrouping;
  // 设置规范器已把未知取值收敛到合法集合；这里再兜一层，避免脏存储透传到渲染层。
  const mode: SessionGroupingMode = isSessionGroupingMode(stored)
    ? stored
    : "directory";

  const setMode = useCallback(
    (next: SessionGroupingMode) => {
      updateSection("workspace", { sessionGrouping: next });
    },
    [updateSection],
  );

  return { mode, setMode };
}
