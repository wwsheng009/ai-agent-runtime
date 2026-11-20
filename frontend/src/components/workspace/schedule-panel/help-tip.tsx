// 字段/分区旁的「说明」提示：问号按钮 + 浮层气泡。
//
// 设计取舍：
// - 说明只在需要时看，常驻在表单里会把「新建任务」撑成一大片文字墙，
//   主字段（名称、时区、Cron）反而被挤下去。改为点/悬停才弹出的浮层。
// - 气泡用 portal 挂到 body：弹窗本身 overflow 裁剪，内联定位会被切掉。
// - 复用共享的 resolvePopoverPosition 做视口夹取，并按上下余量自动翻转。
// - Esc 就地拦下：外层是 modal 对话框，事件冒泡会连整个弹窗一起关掉。

import { CircleHelpIcon } from "lucide-react";
import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import {
  resolvePopoverPosition,
  type PopoverPosition,
} from "@/components/ui/popover-position";
import { cn } from "@/lib/utils";

/** 气泡宽度的上限，长句靠换行而不是撑宽。 */
const TIP_MAX_WIDTH = 272;
const TIP_MAX_HEIGHT = 240;

export type HelpTipProps = {
  className?: string;
  /** 无障碍名称，例如「时区 说明」。 */
  label: string;
  /** 说明正文；支持换行，用于把多条信息分成几行。 */
  text: string;
};

export function HelpTip({ className, label, text }: HelpTipProps) {
  const tipId = useId();
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<PopoverPosition | null>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const tipRef = useRef<HTMLDivElement>(null);

  // 定位跟随触发器：滚动/缩放时重新测量，避免气泡漂移。
  useLayoutEffect(() => {
    if (!open) {
      return;
    }
    const update = () => {
      const node = triggerRef.current;
      if (!node) {
        return;
      }
      const rect = node.getBoundingClientRect();
      const below = window.innerHeight - rect.bottom;
      const above = rect.top;
      // 气泡比问号按钮宽得多，若一律右对齐，按钮靠左时气泡会被推到左侧外面、
      // 与触发器脱节。按右侧剩余空间决定延展方向：够就往右铺，不够才右对齐。
      const spaceRight = window.innerWidth - rect.right;
      setPosition(
        resolvePopoverPosition(rect, {
          align: spaceRight >= TIP_MAX_WIDTH + 24 ? "start" : "end",
          gap: 6,
          maxHeight: TIP_MAX_HEIGHT,
          minWidth: 200,
          // 下方空间够就朝下，否则翻到上方，保证气泡整体可见。
          side: below >= 140 || below >= above ? "bottom" : "top",
        }),
      );
    };
    update();
    window.addEventListener("resize", update);
    window.addEventListener("scroll", update, true);
    return () => {
      window.removeEventListener("resize", update);
      window.removeEventListener("scroll", update, true);
    };
  }, [open]);

  // 点外部关闭。面板内的下拉候选是独立 portal，需排除，否则一点选项就收起。
  useEffect(() => {
    if (!open) {
      return;
    }
    const handlePointerDown = (event: PointerEvent) => {
      const target = event.target as Node | null;
      if (!target) {
        return;
      }
      if (
        tipRef.current?.contains(target) ||
        triggerRef.current?.contains(target)
      ) {
        return;
      }
      if (target instanceof Element && target.closest('[role="listbox"]')) {
        return;
      }
      setOpen(false);
    };
    document.addEventListener("pointerdown", handlePointerDown);
    return () => {
      document.removeEventListener("pointerdown", handlePointerDown);
    };
  }, [open]);

  const tip =
    open && position ? (
      <div
        className={cn(
          "z-[155] overflow-y-auto rounded-card-lg border border-border bg-surface-overlay px-3 py-2",
          "app-text-11 font-normal leading-[1.55] text-muted-foreground",
          "whitespace-pre-line shadow-[0_10px_24px_rgba(0,0,0,0.26)]",
        )}
        id={tipId}
        ref={tipRef}
        role="tooltip"
        style={{
          position: "fixed",
          width: "max-content",
          maxWidth: `min(${TIP_MAX_WIDTH}px, calc(100vw - 1.5rem))`,
          ...position,
        }}
      >
        {text}
      </div>
    ) : null;

  return (
    <>
      <button
        aria-describedby={open ? tipId : undefined}
        aria-expanded={open}
        aria-label={label}
        className={cn(
          "grid size-4 shrink-0 place-items-center rounded-full text-muted-foreground/60",
          "transition-colors hover:text-foreground",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          open && "text-foreground",
          className,
        )}
        data-testid="schedule-field-help"
        // 点击一律置为打开：鼠标点击会先触发 focus（已打开），
        // 若此处取反就会立刻关掉，表现为「点不开」。
        onClick={() => setOpen(true)}
        onBlur={() => setOpen(false)}
        onFocus={() => setOpen(true)}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            // 就地消费：外层 modal 也在听 Esc，冒泡会把整个弹窗关掉。
            event.preventDefault();
            event.stopPropagation();
            setOpen(false);
          }
        }}
        onMouseEnter={() => setOpen(true)}
        onMouseLeave={(event) => {
          // 已聚焦说明用户是点开的，指针移开不应立刻收起。
          if (document.activeElement !== event.currentTarget) {
            setOpen(false);
          }
        }}
        ref={triggerRef}
        type="button"
      >
        <CircleHelpIcon aria-hidden="true" size={13} />
      </button>
      {tip && typeof document !== "undefined" && document.body
        ? createPortal(tip, document.body)
        : tip}
    </>
  );
}
