// 2026-09-15 优化（方案 §15）：**派生目录**（只由会话的工作路径出现、尚未进注册表）的组头动作。
//
// 注册目录走 §3.6 的 ⋯ 菜单（新建会话 / 重命名 / 移除三件套）；派生目录没有注册表 id，
// 三件套都无从下手，因此这里只给一个动作：「注册并新建会话」——
// 一次点击完成 ① POST 注册表 ② 用注册响应里的 directory_id 在该目录下新建会话，
// 替代「先点注册 → 再从组头菜单新建会话」两跳。
//
// 样式沿用组头动作槽：hover 才显示（外层 opacity-0 group-hover/directory-row:opacity-100），
// focus-within 保持键盘可达；忙碌态换旋转图标并禁用重复点击。

import { LoaderCircleIcon, MessageSquarePlusIcon } from "lucide-react";
import { type TFunction } from "i18next";

type WorkspaceSidebarDirectoryDerivedActionsProps = {
  /** 该目录正在注册/建会话：换旋转态并禁用重复点击。 */
  busy: boolean;
  onRegisterAndCreate: () => void;
  t: TFunction<"workspace">;
};

export function WorkspaceSidebarDirectoryDerivedActions({
  busy,
  onRegisterAndCreate,
  t,
}: WorkspaceSidebarDirectoryDerivedActionsProps) {
  const label = t("sidebar.directories.registerAndNewChat");

  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      disabled={busy}
      data-testid="sidebar-directory-register-and-create"
      onClick={onRegisterAndCreate}
      className="rounded-chip p-1 text-muted-foreground transition hover:bg-surface-soft hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent-primary-border disabled:opacity-50"
    >
      {busy ? (
        <LoaderCircleIcon size={12} className="animate-spin" />
      ) : (
        <MessageSquarePlusIcon size={12} />
      )}
    </button>
  );
}
