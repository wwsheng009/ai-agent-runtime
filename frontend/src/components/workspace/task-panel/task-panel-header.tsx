// 面板头部（方案 §3.3）：整行即一个按钮，点击切换折叠/展开。
//
// 折叠态是默认态，因此这一行必须自带全部关键信息：标题、三态计数、当前进行项。

import { ChevronDownIcon, ListTodoIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { TodoCounts } from "@/lib/thread-state/todos";
import { cn } from "@/lib/utils";

/** 分段分隔（方案 §3.3）：U+2002 宽空格夹间隔点，中英混排时不至于挤成一片。 */
const COUNT_SEPARATOR = "\u2002·\u2002";

type TaskPanelHeaderProps = {
  counts: TodoCounts;
  /** 当前进行项文案（无进行项时为空串）。 */
  currentLabel: string;
  expanded: boolean;
  /** 展开区（列表）的 id，用于 aria-controls 关联。 */
  listId: string;
  onToggle: () => void;
};

export function TaskPanelHeader({
  counts,
  currentLabel,
  expanded,
  listId,
  onToggle,
}: TaskPanelHeaderProps) {
  const { t } = useTranslation("workspace");

  // 零值分段省略：宽度优先让给有内容的计数，避免折叠条被「已完成 0」这类噪声占满。
  // 显式标注 `(string | null)[]`：类型化词典让 `t()` 返回字面量类型，不标注会被收窄成联合。
  const countSegments: (string | null)[] = [
    counts.completed > 0
      ? t("panels.todos.counts.completed", { count: counts.completed })
      : null,
    counts.inProgress > 0
      ? t("panels.todos.counts.in_progress", { count: counts.inProgress })
      : null,
    counts.pending > 0
      ? t("panels.todos.counts.pending", { count: counts.pending })
      : null,
  ];
  const countsLabel = countSegments
    .filter((segment): segment is string => Boolean(segment))
    .join(COUNT_SEPARATOR);

  const currentSummary = currentLabel
    ? t("panels.todos.current", { text: currentLabel })
    : "";
  // §8.1（D6）的播报位文本：计数与当前项一变即变，其余渲染（如流式 token）不改其内容，
  // 因此屏幕阅读器只会在任务状态真正推进时收到一次礼貌播报。
  const statusText = [countsLabel, currentSummary].filter(Boolean).join(" ");

  return (
    <>
      <button
        aria-controls={listId}
        aria-expanded={expanded}
        className={cn(
          "flex w-full items-center gap-2 px-3 py-2 text-left text-xs transition-colors hover:bg-surface-soft-hover",
          // 展开时与列表之间用分隔线代替内边距，保持两种状态下的行高一致。
          expanded && "border-b border-border",
        )}
        onClick={onToggle}
        type="button"
      >
        <ListTodoIcon aria-hidden="true" className="size-3.5 shrink-0 text-accent-teal" />
        <span className="shrink-0 font-medium text-foreground">
          {t("panels.todos.title")}
        </span>
        <span className="min-w-0 truncate text-muted-foreground" title={countsLabel}>
          {countsLabel}
        </span>
        {currentSummary ? (
          <span
            className="hidden min-w-0 flex-1 truncate text-muted-foreground sm:block"
            title={currentLabel}
          >
            {currentSummary}
          </span>
        ) : (
          <span className="hidden min-w-0 flex-1 sm:block" />
        )}
        {/* 折叠/展开动作的文案对视觉是冗余的（箭头已表达），只对屏幕阅读器可见。 */}
        <span className="sr-only">
          {expanded ? t("panels.todos.collapse") : t("panels.todos.expand")}
        </span>
        <ChevronDownIcon
          aria-hidden="true"
          className={cn(
            "size-3.5 shrink-0 text-muted-foreground transition-transform",
            expanded && "rotate-180",
          )}
        />
      </button>
      {/* 进度播报位放在按钮之外：视觉行已由一个按钮表达全部信息，放进按钮会让同一段
          文案进入可访问名两次。sr-only 不占布局。 */}
      <span
        aria-live="polite"
        className="sr-only"
        data-testid="todo-panel-status"
        role="status"
      >
        {statusText}
      </span>
    </>
  );
}
