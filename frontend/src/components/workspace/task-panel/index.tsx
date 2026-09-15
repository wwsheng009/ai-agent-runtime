// composer 上沿浮动「当前任务」面板（方案 §3.3 视觉规格 / §6.1 挂载位置）。
//
// 契约：
// - 数据只来自会话投影的 `thread.todoSnapshot`（通道 A 实时 / 通道 B1 历史兜底），
//   本组件不订阅事件流、不拉会话历史；
// - 不可见（无快照、空列表、全部完成）时返回 null，不占位、不影响 composer 布局；
// - 宽度与审批条同轴（--app-chat-content-width-dock）；父容器是 pointer-events-none 的浮层，
//   故指针事件只在卡片本体上开启。

import { useId } from "react";
import { useTranslation } from "react-i18next";

import { useSessionTodos } from "@/hooks/workspace/use-session-todos";
import type { TodoSnapshot } from "@/lib/thread-state/todos";
import { cn } from "@/lib/utils";
import { TaskPanelHeader } from "./task-panel-header";
import { TaskPanelList } from "./task-panel-list";

type TodoPanelProps = {
  sessionId?: string;
  snapshot?: TodoSnapshot | null;
};

export function TodoPanel({ sessionId, snapshot }: TodoPanelProps) {
  const { t } = useTranslation("workspace");
  const listId = useId();
  const { visible, items, counts, currentLabel, collapsed, toggleCollapsed } =
    useSessionTodos({ sessionId, snapshot });

  if (!visible) {
    return null;
  }

  const expanded = !collapsed;

  return (
    <section
      aria-label={t("panels.todos.ariaLabel")}
      className="pointer-events-auto mx-auto mb-1.5 w-full max-w-[var(--app-chat-content-width-dock)] overflow-hidden rounded-panel border border-border bg-surface-soft/95 shadow-[0_10px_30px_var(--surface-shadow)] backdrop-blur"
      data-testid="todo-panel"
    >
      <TaskPanelHeader
        counts={counts}
        currentLabel={currentLabel}
        expanded={expanded}
        listId={listId}
        onToggle={toggleCollapsed}
      />
      {/* 展开/收起动效（方案 §6.3）：用 grid-rows 0fr↔1fr 做高度过渡，
          `motion-safe:` 前缀保证系统开启「减少动效」时直接跳变。
          折叠态**卸载**列表子树——不可见的东西不进无障碍树、不占布局（§3.3 不渲染空壳）。 */}
      <div
        className={cn(
          "grid min-h-0 motion-safe:transition-[grid-template-rows] motion-safe:duration-200 motion-safe:ease-out",
          expanded ? "grid-rows-[1fr]" : "grid-rows-[0fr]",
        )}
      >
        <div className="min-h-0 overflow-hidden">
          {expanded ? <TaskPanelList items={items} listId={listId} /> : null}
        </div>
      </div>
    </section>
  );
}
