// 由 pages/logs-page.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { CheckIcon, CopyIcon } from "lucide-react";
import type { ComponentProps } from "react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export function LogHeaderBadge({ className, children }: ComponentProps<"span">) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-control border border-border bg-surface-soft px-2 py-0.5 app-text-9 font-semibold uppercase tracking-[0.12em] text-muted-foreground",
        className,
      )}
    >
      {children}
    </span>
  );
}

type CopyActionButtonProps = {
  copied: boolean;
  copiedLabel: string;
  label: string;
  onClick: () => void;
};

export function CopyActionButton({
  copied,
  copiedLabel,
  label,
  onClick,
}: CopyActionButtonProps) {
  return (
    <Button
      variant="ghost"
      size="sm"
      className="h-7 rounded-control border border-border bg-black/10 px-2.5 text-muted-foreground hover:bg-black/20 hover:text-foreground"
      onClick={onClick}
    >
      {copied ? <CheckIcon size={13} /> : <CopyIcon size={13} />}
      {copied ? copiedLabel : label}
    </Button>
  );
}

export function LogsPageDetailPanelFallback({ message }: { message: string }) {
  return (
    <div className="flex flex-1 items-center justify-center px-6 text-center text-sm text-muted-foreground">
      {message}
    </div>
  );
}
