// 展开态列表（方案 §3.3）：可见区最多约 8 行，超出的项靠滚动继续阅读，
// 并额外用「还有 N 项」提示列表还没到头（滚动条在小尺寸下不够显眼）。

import { useTranslation } from "react-i18next";

import type { TodoItem, TodoStatus } from "@/lib/thread-state/todos";
import { TaskPanelItem } from "./task-panel-item";

/** 单屏可见项数的软上限，仅用于「还有 N 项」提示的计数口径。 */
const MAX_VISIBLE_ITEMS = 8;

// 用字面量键而非模板拼接：i18next 的类型化键在编译期就能校验出拼写错误。
const STATUS_LABEL_KEYS = {
  pending: "panels.todos.status.pending",
  in_progress: "panels.todos.status.in_progress",
  completed: "panels.todos.status.completed",
} as const satisfies Record<TodoStatus, string>;

type TaskPanelListProps = {
  items: readonly TodoItem[];
  /** 与 header 的 aria-controls 保持一致。 */
  listId: string;
};

export function TaskPanelList({ items, listId }: TaskPanelListProps) {
  const { t } = useTranslation("workspace");
  const hiddenCount = Math.max(0, items.length - MAX_VISIBLE_ITEMS);

  return (
    <div>
      <ul
        className="max-h-40 overflow-y-auto py-1"
        data-testid="todo-panel-list"
        id={listId}
      >
        {items.map((item, index) => (
          // content 可能重复（模型偶尔复用同一句描述），故用下标兜底保证 key 唯一。
          <TaskPanelItem
            item={item}
            key={`${index}:${item.content}`}
            statusLabel={t(STATUS_LABEL_KEYS[item.status])}
          />
        ))}
      </ul>
      {hiddenCount > 0 ? (
        <p
          className="border-t border-border px-3 py-1.5 text-[11px] text-muted-foreground"
          data-testid="todo-panel-more"
        >
          {t("panels.todos.more", { count: hiddenCount })}
        </p>
      ) : null}
    </div>
  );
}
