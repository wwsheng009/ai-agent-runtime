// 通用页签切换器（受控）：全前端 tab 的统一样式与交互基元。
//
// 形制（variant）：
//   * segmented（默认）：胶囊容器 + 选中项实心高亮，用于页面/弹窗内的分区切换；
//   * plain：不带容器样式，调用方自备外框（如已有卡片/分隔线的场景）；
//   * underline：下划线式，用于贴近内容区的视图切换。
//
// 逻辑（所有形制共用）：
//   * roving tabindex：只有选中项可 Tab 聚焦；
//   * 方向键（←→↑↓）循环切换、Home/End 跳首尾，选择跟随焦点并跳过 disabled 项；
//   * aria-selected / aria-controls / id / disabled 透传，测试可继续按 role=tab 契约断言。
//
// 例外：文件管理器的可关闭文档页签（tab-strip）与右侧栏 tone 主题面（surface-tabs）
// 有各自的关闭/tone 语义，保留专用实现；本组件只收敛「切换」型页签。

import { type KeyboardEvent, type ReactNode, useRef } from "react";

import { cn } from "@/lib/utils";

export type TabSwitcherItem<T extends string> = {
  id: T;
  label: string;
  icon?: ReactNode;
  badge?: ReactNode;
  disabled?: boolean;
  /** 页签按钮 id（ARIA tabs 模式）；与 panelId 成对提供。 */
  tabId?: string;
  /** 受控面板 id。 */
  panelId?: string;
  testId?: string;
  /** 悬浮说明（如禁用原因）。 */
  title?: string;
};

export type TabSwitcherVariant = "segmented" | "plain" | "underline";
export type TabSwitcherSize = "sm" | "md";

type TabSwitcherProps<T extends string> = {
  ariaLabel: string;
  className?: string;
  items: ReadonlyArray<TabSwitcherItem<T>>;
  onChange: (id: T) => void;
  value: T;
  variant?: TabSwitcherVariant;
  size?: TabSwitcherSize;
};

const CONTAINER_CLASS: Record<TabSwitcherVariant, string> = {
  segmented:
    "flex flex-wrap items-center gap-1 rounded-card border border-border bg-surface-softer p-1",
  plain: "flex flex-wrap items-center gap-1",
  underline: "flex items-center gap-1 border-b border-border",
};

const SIZE_CLASS: Record<TabSwitcherSize, string> = {
  sm: "px-2.5 py-1 text-sm",
  md: "px-3 py-1.5 text-sm",
};

const TAB_BASE =
  "inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap font-medium transition-colors outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-45";

const ACTIVE_FILL =
  "border-transparent bg-[linear-gradient(135deg,var(--accent-primary)_0%,var(--accent-primary-strong)_100%)] text-black shadow-[0_4px_14px_var(--accent-primary-shadow)]";

function tabClass(
  variant: TabSwitcherVariant,
  active: boolean,
): string {
  if (variant === "underline") {
    return cn(
      "-mb-px border-b-2",
      active
        ? "border-accent-primary text-foreground"
        : "border-transparent text-muted-foreground hover:text-foreground",
    );
  }
  return cn(
    "rounded-control border",
    active
      ? ACTIVE_FILL
      : "border-transparent text-muted-foreground hover:bg-surface-soft hover:text-foreground",
  );
}

function badgeClass(variant: TabSwitcherVariant, active: boolean): string {
  if (active && variant !== "underline") {
    return "rounded-chip bg-black/15 px-1.5 py-0.5 app-text-10 text-black";
  }
  return "rounded-chip border border-border bg-surface-solid px-1.5 py-0.5 app-text-10 text-muted-foreground";
}

/** 方向键/Home/End 解析下一个可聚焦页签下标；返回 -1 表示不处理。 */
function resolveNextTabIndex(
  key: string,
  enabledIndices: number[],
  currentIndex: number,
): number {
  if (enabledIndices.length === 0) {
    return -1;
  }
  const position = enabledIndices.indexOf(currentIndex);
  switch (key) {
    case "ArrowRight":
    case "ArrowDown":
      return (
        enabledIndices[position >= 0 ? (position + 1) % enabledIndices.length : 0] ?? -1
      );
    case "ArrowLeft":
    case "ArrowUp":
      return (
        enabledIndices[
          position >= 0
            ? (position - 1 + enabledIndices.length) % enabledIndices.length
            : enabledIndices.length - 1
        ] ?? -1
      );
    case "Home":
      return enabledIndices[0] ?? -1;
    case "End":
      return enabledIndices[enabledIndices.length - 1] ?? -1;
    default:
      return -1;
  }
}

export function TabSwitcher<T extends string>({
  ariaLabel,
  className,
  items,
  onChange,
  size = "sm",
  value,
  variant = "segmented",
}: TabSwitcherProps<T>) {
  const refs = useRef<Array<HTMLButtonElement | null>>([]);
  const enabledIndices = items.flatMap((item, index) =>
    item.disabled ? [] : [index],
  );
  const activeIndex = items.findIndex(
    (item) => item.id === value && !item.disabled,
  );
  const fallbackFocusIndex = enabledIndices[0] ?? -1;

  const handleKeyDown = (
    event: KeyboardEvent<HTMLButtonElement>,
    index: number,
  ) => {
    const nextIndex = resolveNextTabIndex(event.key, enabledIndices, index);
    if (nextIndex < 0) {
      return;
    }
    const next = items[nextIndex];
    if (!next) {
      return;
    }
    event.preventDefault();
    onChange(next.id);
    const node = refs.current[nextIndex];
    node?.focus();
    node?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  };

  return (
    <div
      aria-label={ariaLabel}
      aria-orientation="horizontal"
      className={cn(CONTAINER_CLASS[variant], className)}
      role="tablist"
    >
      {items.map((item, index) => {
        const active = item.id === value && !item.disabled;
        const focusable =
          active || (activeIndex < 0 && index === fallbackFocusIndex);
        return (
          <button
            aria-controls={item.panelId}
            aria-selected={active}
            className={cn(TAB_BASE, SIZE_CLASS[size], tabClass(variant, active))}
            data-testid={item.testId}
            disabled={item.disabled}
            id={item.tabId}
            key={item.id}
            onClick={() => {
              if (!item.disabled) {
                onChange(item.id);
              }
            }}
            onKeyDown={(event) => handleKeyDown(event, index)}
            ref={(node) => {
              refs.current[index] = node;
            }}
            role="tab"
            tabIndex={focusable ? 0 : -1}
            title={item.title}
            type="button"
          >
            {item.icon}
            {item.label}
            {item.badge !== undefined && item.badge !== null ? (
              <span className={badgeClass(variant, active)}>{item.badge}</span>
            ) : null}
          </button>
        );
      })}
    </div>
  );
}
