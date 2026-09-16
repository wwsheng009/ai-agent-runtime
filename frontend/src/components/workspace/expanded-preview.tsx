// 放大预览：右侧栏小窗口（Git diff 浏览 / 文件预览）与信息流 apply_patch 差异面板共用的
// 「扩展图标 + 放大面板」形制。
//
// 为什么抽成共享件：三处小窗口的正文渲染各自已有唯一实现（GitDiffView / PreviewPane /
// 工具行差异正文），放大面板若各写一份正文就把「同一份 diff/预览渲染两份」引进来；本件只提供
// **外壳**（入口按钮 + 遮罩面板 + 关闭路径），正文由调用方把同一份渲染再挂一次。
//
// 关闭路径三条必须齐备（缺一条就有「关不掉」的死角）：面板右上角关闭按钮、遮罩点击、Esc。
// 焦点纪律：打开时把焦点移进面板（触发元素在工具行里会吞 keydown，焦点留在外面 Esc 失效），
// 关闭后由 useFocusRestore 交还触发元素。

import { Maximize2Icon, XIcon } from "lucide-react";
import { type ReactNode, useEffect, useRef } from "react";
import { createPortal } from "react-dom";

import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import { useFocusRestore } from "@/hooks/workspace/use-focus-restore";
import { cn } from "@/lib/utils";

export type ExpandPreviewButtonProps = {
  /** 无障碍与悬浮文案（i18n 由调用方取词：同一个按钮在三个面里的说法不同）。 */
  label: string;
  onClick: () => void;
  className?: string;
  testId?: string;
};

/** 小窗口右上角的「放大」入口：图标按钮，不抢宿主面板的既有控件位。 */
export function ExpandPreviewButton({
  className,
  label,
  onClick,
  testId,
}: ExpandPreviewButtonProps) {
  return (
    <Button
      aria-label={label}
      className={cn("size-6 shrink-0 text-muted-foreground hover:text-foreground", className)}
      data-testid={testId}
      onClick={onClick}
      size="icon"
      title={label}
      variant="ghost"
    >
      <Maximize2Icon aria-hidden className="size-3.5" />
    </Button>
  );
}

export type ExpandedPreviewDialogProps = {
  /** 面板 aria-label（读屏入口，通常是「放大视图 + 当前文件」）。 */
  ariaLabel: string;
  /** 正文：调用方复用小窗口那套渲染（同一份实现，放大只换容器尺寸）。 */
  children: ReactNode;
  closeLabel: string;
  /** 顶部小标题（如「放大视图」）。 */
  eyebrow: string;
  /** 底部操作口径提示（如 Esc / 遮罩关闭）。 */
  hint?: string;
  onClose: () => void;
  open: boolean;
  /** 副标题：路径之外的次要信息（大小、对比目标、增删统计）。 */
  subtitle?: string;
  testId?: string;
  /** 主标题：通常是文件路径（等宽字体，超长截断但保留 title）。 */
  title: string;
};

export function ExpandedPreviewDialog({
  ariaLabel,
  children,
  closeLabel,
  eyebrow,
  hint,
  onClose,
  open,
  subtitle,
  testId,
  title,
}: ExpandedPreviewDialogProps) {
  const panelRef = useRef<HTMLDivElement | null>(null);

  useDialogLifecycle(open, onClose);
  useFocusRestore(open);

  useEffect(() => {
    if (open) {
      panelRef.current?.focus();
    }
  }, [open]);

  if (!open) {
    return null;
  }

  return createPortal(
    // z 序：高于文件预览弹层（z-120）与右栏抽屉（z-96），保证任何入口进来都在最上层。
    <DialogOverlay className="z-[130] backdrop-blur-sm" onDismiss={onClose}>
      <DialogPanel
        aria-label={ariaLabel}
        aria-modal="true"
        className="h-[calc(100vh-1.5rem)] max-w-[1600px]"
        data-testid={testId}
        ref={panelRef}
        role="dialog"
        tabIndex={-1}
      >
        <div className="flex items-start justify-between gap-3 border-b border-border px-3.5 py-2.5">
          <div className="min-w-0">
            <div className="app-text-10 uppercase tracking-[0.16em] text-accent-secondary">
              {eyebrow}
            </div>
            <h2
              className="mt-0.5 truncate font-mono text-sm text-foreground"
              title={title}
            >
              {title}
            </h2>
            {subtitle ? (
              <p className="mt-0.5 truncate app-text-11 text-muted-foreground">{subtitle}</p>
            ) : null}
          </div>
          <Button
            aria-label={closeLabel}
            data-testid={testId ? `${testId}-close` : undefined}
            onClick={onClose}
            size="icon"
            title={closeLabel}
            variant="ghost"
          >
            <XIcon size={16} />
          </Button>
        </div>
        <div className="min-h-0 flex-1 overflow-hidden">{children}</div>
        {hint ? (
          <p className="border-t border-border px-3.5 py-1.5 app-text-10 text-muted-foreground/70">
            {hint}
          </p>
        ) : null}
      </DialogPanel>
    </DialogOverlay>,
    document.body,
  );
}
