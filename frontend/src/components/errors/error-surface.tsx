// P1-10：错误边界统一可见错误面。只负责呈现（标题/说明/细节/恢复动作），
// 文案由调用方经 i18n 注入，保证 zh-CN / en-US 双份对齐。
// 面板级（scope="panel"）内联在原位置，全局/路由级占满视口，避免白屏。

import { TriangleAlertIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export type ErrorSurfaceScope = "global" | "route" | "panel";

export interface ErrorSurfaceAction {
  key: string;
  label: string;
  onClick: () => void;
  variant?: "primary" | "secondary";
  autoFocus?: boolean;
}

export interface ErrorSurfaceProps {
  scope: ErrorSurfaceScope;
  title: string;
  description: string;
  detail?: string;
  hint?: string;
  actions: readonly ErrorSurfaceAction[];
}

export function ErrorSurface({
  scope,
  title,
  description,
  detail,
  hint,
  actions,
}: ErrorSurfaceProps) {
  const panel = scope === "panel";

  return (
    <div
      role="alert"
      aria-live="assertive"
      data-error-scope={scope}
      className={cn(
        "flex w-full text-foreground",
        panel
          ? "min-h-0 flex-1 flex-col overflow-hidden px-3 py-3"
          : "min-h-screen items-center justify-center [background:var(--workspace-shell-bg)] px-4",
      )}
    >
      <div
        className={cn(
          "flex flex-col gap-3 rounded-panel border border-border bg-surface-softer",
          panel ? "w-full p-3" : "w-full max-w-xl p-5",
        )}
      >
        <div className="flex items-center gap-2">
          <TriangleAlertIcon
            aria-hidden
            size={16}
            className="shrink-0 text-accent-orange"
          />
          <h2 className="text-base font-semibold">{title}</h2>
        </div>
        <p className="text-sm text-muted-foreground">{description}</p>
        {detail ? (
          <p className="break-words text-xs text-muted-foreground/80">{detail}</p>
        ) : null}
        {hint ? <p className="text-xs text-muted-foreground">{hint}</p> : null}
        <div className="flex flex-wrap items-center gap-2">
          {actions.map((action) => (
            <Button
              key={action.key}
              autoFocus={action.autoFocus}
              size="sm"
              variant={action.variant ?? "secondary"}
              onClick={action.onClick}
            >
              {action.label}
            </Button>
          ))}
        </div>
      </div>
    </div>
  );
}
