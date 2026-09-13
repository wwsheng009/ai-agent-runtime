import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

// P0-3：卡片头部的共享槽位。原先 settings-panel-card / settings-info-card /
// settings-field-card / toggle / choice 等各自手写同一组类名，这里集中为唯一实现。
export function SectionHeaderRow({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      className={cn("flex items-start justify-between gap-3", className)}
      {...props}
    />
  );
}

export function SectionHeaderMain({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("min-w-0", className)} {...props} />;
}

export function SectionAside({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("shrink-0", className)} {...props} />;
}

export function SectionTitleRow({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      className={cn(
        "flex items-center gap-3 text-sm font-semibold text-foreground",
        className,
      )}
      {...props}
    />
  );
}

export function SectionDescriptionText({
  gapClassName,
  className,
  ...props
}: ComponentProps<"div"> & { gapClassName?: string }) {
  return (
    <div
      className={cn(
        gapClassName,
        "text-sm leading-6 text-muted-foreground",
        className,
      )}
      {...props}
    />
  );
}

export function SectionBody({
  spaced = false,
  className,
  ...props
}: ComponentProps<"div"> & { spaced?: boolean }) {
  return <div className={cn(spaced ? "mt-3" : null, className)} {...props} />;
}
