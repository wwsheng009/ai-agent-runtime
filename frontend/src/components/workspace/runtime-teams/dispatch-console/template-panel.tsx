// 由 components/workspace/runtime-teams/dispatch-console.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { cn } from "@/lib/utils";

import { consoleModeButtonClass, consolePanelClass } from "./console-styles";
import type { DispatchConsoleProps } from "./types";

type DispatchTemplatePanelProps = Pick<
  DispatchConsoleProps,
  | "dispatchTemplateMode"
  | "onDispatchTemplateModeChange"
>;

export function DispatchTemplatePanel({
  dispatchTemplateMode,
  onDispatchTemplateModeChange,
}: DispatchTemplatePanelProps) {
  return (
    <div className={cn("mt-3", consolePanelClass)}>
      <div className="app-text-11 uppercase tracking-[0.14em] text-muted-foreground">
        Fan-out template
      </div>
      <div className="mt-3 flex flex-wrap gap-2">
        <button
          type="button"
          onClick={() => onDispatchTemplateModeChange("review_implement_verify")}
          className={cn(
            consoleModeButtonClass,
            dispatchTemplateMode === "review_implement_verify"
              ? "border-[#f0c77b]/24 bg-[#f0c77b]/10 text-[#f0c77b]"
              : "border-white/10 bg-white/4 text-muted-foreground hover:border-white/14 hover:bg-white/7 hover:text-foreground",
          )}
        >
          Review / Implement / Verify
        </button>
        <button
          type="button"
          onClick={() => onDispatchTemplateModeChange("mirror")}
          className={cn(
            consoleModeButtonClass,
            dispatchTemplateMode === "mirror"
              ? "border-[#f0c77b]/24 bg-[#f0c77b]/10 text-[#f0c77b]"
              : "border-white/10 bg-white/4 text-muted-foreground hover:border-white/14 hover:bg-white/7 hover:text-foreground",
          )}
        >
          Mirror Same Task
        </button>
      </div>
      <div className="mt-3 text-sm leading-6 text-muted-foreground">
        {dispatchTemplateMode === "mirror"
          ? "Every selected team receives the same task payload."
          : "Teams receive role-specific variants of the same next task so they execute from different angles."}
      </div>
    </div>
  );
}
