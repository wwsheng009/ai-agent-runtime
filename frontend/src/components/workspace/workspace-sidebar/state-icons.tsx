// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { cn } from "@/lib/utils";
import { type SidebarStateIconSpec } from "./types";

export function SidebarStateIcon({ spec }: { spec: SidebarStateIconSpec }) {
  const Icon = spec.icon;

  return (
    <span
      title={spec.label}
      aria-label={spec.label}
      className={cn(
        "inline-flex size-[1.375rem] items-center justify-center rounded-[0.65rem] border",
        spec.toneClassName,
      )}
    >
      <Icon size={11} />
    </span>
  );
}
