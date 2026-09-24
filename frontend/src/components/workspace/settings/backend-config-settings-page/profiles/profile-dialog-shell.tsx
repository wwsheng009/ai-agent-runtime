// Profiles 对话框外壳：创建向导 / 生命周期操作 / 引用清单共用。
//
// 与设置域既有约定一致：DialogOverlay + DialogPanel + useDialogLifecycle
// （body 滚动锁定 + Esc 关闭 + 遮罩点击关闭），关闭按钮走 aria-label 便于测试。

import { XIcon } from "lucide-react";
import { type ReactNode } from "react";

import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import { cn } from "@/lib/utils";

type ProfileDialogShellProps = {
  children: ReactNode;
  closeLabel: string;
  description?: ReactNode;
  footer?: ReactNode;
  onClose: () => void;
  title: ReactNode;
  /** 面板宽度档位：向导与引用清单需要更宽。 */
  size?: "sm" | "md";
};

export function ProfileDialogShell({
  children,
  closeLabel,
  description,
  footer,
  onClose,
  title,
  size = "sm",
}: ProfileDialogShellProps) {
  useDialogLifecycle(true, onClose);

  return (
    <DialogOverlay className="z-[130]" onDismiss={onClose}>
      <DialogPanel
        className={cn("w-full", size === "sm" ? "max-w-xl" : "max-w-2xl")}
        role="dialog"
        aria-modal="true"
      >
        <div className="flex items-start justify-between gap-3 border-b border-border px-3.5 py-3">
          <div className="min-w-0">
            <h2 className="text-base font-semibold tracking-[-0.02em] text-foreground">
              {title}
            </h2>
            {description ? (
              <p className="mt-1 max-w-[42rem] text-xs leading-5 text-muted-foreground">
                {description}
              </p>
            ) : null}
          </div>
          <Button
            aria-label={closeLabel}
            size="icon"
            type="button"
            variant="ghost"
            onClick={onClose}
          >
            <XIcon size={16} />
          </Button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto px-3.5 py-3">{children}</div>

        {footer ? (
          <div className="border-t border-border px-3.5 py-3">{footer}</div>
        ) : null}
      </DialogPanel>
    </DialogOverlay>
  );
}
