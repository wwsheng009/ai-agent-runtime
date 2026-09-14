// P2-6：侧栏「会话用户」菜单项的纯口径（无 React、无 IO）。
// 从 workspace-sidebar.tsx 拆出（P0-2 单文件非空行门禁）：只做去重、兜底与默认用户标记，
// 语义与原实现一致——当前选中用户若不在用户目录里，按会话总数补一条占位项。

import { type RuntimeSessionUserSummary } from "@/types/runtime";

export type WorkspaceSidebarSessionUserMenuItem = {
  displayName: string;
  isDefaultUser: boolean;
  sessionCount: number;
  userId: string;
};

type BuildSessionUserMenuItemsArgs = {
  users: readonly RuntimeSessionUserSummary[];
  defaultUserId?: string | undefined;
  selectedUserId: string;
  /** 当前筛选下的会话总数（用于补齐未出现在用户目录里的选中用户）。 */
  totalCount: number;
};

export function buildSessionUserMenuItems({
  defaultUserId,
  selectedUserId,
  totalCount,
  users,
}: BuildSessionUserMenuItemsArgs): WorkspaceSidebarSessionUserMenuItem[] {
  const seen = new Set<string>();
  const items: WorkspaceSidebarSessionUserMenuItem[] = [];
  const trimmedDefaultUserId = defaultUserId?.trim() ?? "";

  for (const user of users) {
    const userId = user.user_id.trim();
    if (!userId || seen.has(userId)) {
      continue;
    }
    seen.add(userId);
    items.push({
      userId,
      displayName: user.display_name?.trim() || userId,
      isDefaultUser: trimmedDefaultUserId === userId,
      sessionCount: user.session_count ?? 0,
    });
  }

  const trimmedSelectedUserId = selectedUserId.trim();
  if (
    trimmedSelectedUserId &&
    !seen.has(trimmedSelectedUserId) &&
    totalCount > 0
  ) {
    items.unshift({
      userId: trimmedSelectedUserId,
      displayName: trimmedSelectedUserId,
      isDefaultUser: trimmedDefaultUserId === trimmedSelectedUserId,
      sessionCount: totalCount,
    });
  }

  return items;
}
