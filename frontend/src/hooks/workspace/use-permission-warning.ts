// P1-4：危险权限模式警示（会话级、可关闭）。
//
// 模式快照由 composer 权限控件上抛，避免主区再建一条独立的权限查询通道；
// 会话切换后的旧快照按 sessionId 自动失效，关闭状态按「会话 + 模式」记忆。

import { useCallback, useState } from "react";

export type PermissionModeSnapshot = {
  sessionId: string;
  mode: string;
  dangerous: boolean;
};

export type PermissionWarningController = {
  showWarning: boolean;
  dismissWarning: () => void;
  onModeChange: (snapshot: PermissionModeSnapshot) => void;
};

export function usePermissionWarning(
  sessionId: string | undefined,
): PermissionWarningController {
  const normalizedSessionId = sessionId?.trim() ?? "";
  const [snapshot, setSnapshot] = useState<PermissionModeSnapshot | null>(null);
  const [dismissedKey, setDismissedKey] = useState<string | null>(null);

  const dangerous =
    snapshot?.sessionId === normalizedSessionId && snapshot.dangerous;
  const warningKey = dangerous
    ? `${snapshot?.sessionId}:${snapshot?.mode}`
    : "";
  const showWarning =
    dangerous &&
    (warningKey.length === 0 || dismissedKey !== warningKey);

  const onModeChange = useCallback((next: PermissionModeSnapshot) => {
    setSnapshot(next);
    // 回到安全档后清掉「已关闭」记忆：再次切到危险档时警示应重新出现。
    if (!next.dangerous) {
      setDismissedKey(null);
    }
  }, []);

  const dismissWarning = useCallback(() => {
    if (warningKey.length > 0) {
      setDismissedKey(warningKey);
    }
  }, [warningKey]);

  return { showWarning, dismissWarning, onModeChange };
}