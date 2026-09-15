// 单条任务行（方案 §3.3）：状态图标 + 单行文案（§6.4：长文本一律 `line-clamp-1` +
// `title` 悬浮全文，保持条状几何稳定，不因模型写了长句而把输入区顶开）。
//
// 行本身**不可交互**：折叠/展开只由 header 的整行按钮负责，避免行内出现过多可聚焦元素
// （列表可能一次渲染十几行，键盘 Tab 会被拖慢）。

import {
  CheckIcon,
  CircleIcon,
  LoaderCircleIcon,
  type LucideIcon,
} from "lucide-react";

import type { TodoItem, TodoStatus } from "@/lib/thread-state/todos";
import { cn } from "@/lib/utils";

type StatusPresentation = {
  Icon: LucideIcon;
  iconClassName: string;
  textClassName: string;
};

// 状态 → 视觉的唯一真源：图标、图标色、文案修饰一起定义，避免三处分支漂移。
const STATUS_PRESENTATION: Record<TodoStatus, StatusPresentation> = {
  pending: {
    Icon: CircleIcon,
    iconClassName: "text-muted-foreground",
    textClassName: "text-muted-foreground",
  },
  in_progress: {
    Icon: LoaderCircleIcon,
    // 进行中是唯一「活着」的状态：旋转动画 + 主色，扫一眼即可定位当前项。
    iconClassName: "animate-spin text-accent-teal",
    textClassName: "font-medium text-foreground",
  },
  completed: {
    Icon: CheckIcon,
    iconClassName: "text-accent-teal",
    textClassName: "text-muted-foreground line-through",
  },
};

type TaskPanelItemProps = {
  item: TodoItem;
  /** 已本地化的状态名（供屏幕阅读器朗读，视觉上由图标表达）。 */
  statusLabel: string;
};

export function TaskPanelItem({ item, statusLabel }: TaskPanelItemProps) {
  const { Icon, iconClassName, textClassName } = STATUS_PRESENTATION[item.status];

  return (
    <li
      className="flex items-start gap-2 px-3 py-1.5"
      data-status={item.status}
      data-testid="todo-panel-item"
    >
      <Icon
        aria-hidden="true"
        className={cn("mt-0.5 size-3.5 shrink-0", iconClassName)}
      />
      <span className="sr-only">{statusLabel}</span>
      <span
        className={cn("line-clamp-1 min-w-0 flex-1 text-xs leading-5", textClassName)}
        title={item.content}
      >
        {item.content}
      </span>
    </li>
  );
}
