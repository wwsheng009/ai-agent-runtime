import {
  type ComponentProps,
  type CSSProperties,
  type MouseEvent,
  type ReactNode,
} from "react";

import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

// P0-3：设置域对话框的唯一外壳实现（遮罩 + 面板）。生命周期见 use-dialog-lifecycle。
type DialogOverlayProps = {
  children: ReactNode;
  className?: string;
  onDismiss: () => void;
  /** 实例级内衬：如文件预览按 composer 实测高度让出底部空间（不改变其他弹层默认形制）。 */
  style?: CSSProperties;
};

export function DialogOverlay({
  children,
  className,
  onDismiss,
  style,
}: DialogOverlayProps) {
  const handleMouseDown = (event: MouseEvent<HTMLDivElement>) => {
    if (event.target === event.currentTarget) {
      onDismiss();
    }
  };

  return (
    <div
      className={cn(
        "fixed inset-0 flex items-center justify-center bg-dialog-backdrop px-3 py-4",
        className,
      )}
      onMouseDown={handleMouseDown}
      style={style}
    >
      {children}
    </div>
  );
}

// eslint-disable-next-line react-refresh/only-export-components
export const dialogPanelVariants = cva(
  "flex max-h-[calc(100vh-1.5rem)] w-full flex-col overflow-hidden rounded-panel border border-border [background:var(--dialog-bg)]",
  {
    variants: {
      elevation: {
        md: "shadow-[0_12px_36px_rgba(0,0,0,0.22)]",
        lg: "shadow-[0_14px_36px_rgba(0,0,0,0.2)]",
      },
    },
    defaultVariants: {
      elevation: "md",
    },
  },
);

type DialogPanelProps = ComponentProps<"div"> &
  VariantProps<typeof dialogPanelVariants>;

export function DialogPanel({
  className,
  elevation,
  ...props
}: DialogPanelProps) {
  return (
    <div
      className={cn(dialogPanelVariants({ elevation }), className)}
      {...props}
    />
  );
}
