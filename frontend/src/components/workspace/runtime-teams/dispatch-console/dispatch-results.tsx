// 由 components/workspace/runtime-teams/dispatch-console.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { cn } from "@/lib/utils";

import { truncateIdentifier } from "../shared";

import { consolePillClass } from "./console-styles";
import type { DispatchConsoleProps } from "./types";

type DispatchResultsProps = Pick<
  DispatchConsoleProps,
  | "dispatchTaskResults"
>;

export function DispatchResults({
  dispatchTaskResults,
}: DispatchResultsProps) {
  return (
    <>
      {dispatchTaskResults.length > 0 ? (
        <div className="space-y-1.5">
          {dispatchTaskResults.map((result) => (
            <div
              key={`dispatch-result-${result.teamId}`}
              className="rounded-card border border-white/8 bg-white/4 px-3 py-2.5"
            >
              <div className="flex items-center justify-between gap-3">
                <div className="text-sm font-semibold text-foreground">
                  {truncateIdentifier(result.teamId, 18)}
                </div>
                <span
                  className={cn(
                    consolePillClass,
                    result.status === "created"
                      ? "border-accent-teal/24 bg-accent-teal/10 text-accent-teal"
                      : "border-accent-orange/24 bg-accent-orange/10 text-accent-orange",
                  )}
                >
                  {result.status}
                </span>
              </div>
              <div className="mt-2 text-sm text-muted-foreground">
                {result.status === "created"
                  ? `task ${truncateIdentifier(result.taskId, 18)} created`
                  : result.error || "dispatch failed"}
              </div>
            </div>
          ))}
        </div>
      ) : null}
    </>
  );
}
