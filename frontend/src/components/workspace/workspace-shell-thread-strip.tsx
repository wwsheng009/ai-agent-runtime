import {
  Clock3Icon,
  CommandIcon,
  FolderKanbanIcon,
  OrbitIcon,
  RadioTowerIcon,
  SparklesIcon,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { type Thread } from "@/data/mock";
import { cn } from "@/lib/utils";

const stripPillClass =
  "inline-flex items-center gap-2 rounded-[0.65rem] border px-2.5 py-1 app-text-11 uppercase tracking-[0.14em]";

type WorkspaceShellThreadStripProps = {
  commandStateLabel: string;
  selectedThread: Thread;
  transportLabel: string;
};

export function WorkspaceShellThreadStrip({
  commandStateLabel,
  selectedThread,
  transportLabel,
}: WorkspaceShellThreadStripProps) {
  return (
    <div className="shrink-0 px-3.5 pb-2.5 pt-1.5 sm:px-4 lg:px-6">
      <div className="mx-auto flex max-w-[42rem] flex-wrap items-center gap-2 app-text-11 uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
        <span className={cn(stripPillClass, "border-white/10 bg-white/5 text-[#8fd0c6]")}>
          <SparklesIcon size={14} />
          Active thread
        </span>
        <span className={cn(stripPillClass, "border-white/10 bg-black/18")}>
          <RadioTowerIcon size={14} className="text-[#8fd0c6]" />
          {transportLabel}
        </span>
        <span className={cn(stripPillClass, "border-white/10 bg-black/18")}>
          <CommandIcon size={14} className="text-[#8fd0c6]" />
          {commandStateLabel}
        </span>
        <span className={cn(stripPillClass, "border-white/10 bg-black/18")}>
          <Clock3Icon size={14} />
          {selectedThread.updatedAt.slice(11, 16)} updated
        </span>
        {selectedThread.lastRuntimeEventType ? (
          <Badge>{selectedThread.lastRuntimeEventType}</Badge>
        ) : null}
      </div>

      <p className="mx-auto mt-2.5 max-w-[42rem] text-sm leading-6 text-[var(--muted-foreground)]">
        {selectedThread.summary}
      </p>

      <div className="mx-auto mt-2 flex max-w-[42rem] flex-wrap items-center gap-x-4 gap-y-1 app-text-11 uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
        <span className="inline-flex items-center gap-2">
          <OrbitIcon size={13} className="text-[#f0c77b]" />
          {selectedThread.messages.length} entries
        </span>
        <span className="inline-flex items-center gap-2">
          <FolderKanbanIcon size={13} className="text-[#8fd0c6]" />
          {selectedThread.artifacts.length} artifacts
        </span>
        {selectedThread.tags.slice(0, 3).map((tag) => (
          <span key={tag}>{tag}</span>
        ))}
      </div>

      {selectedThread.lastError ? (
        <div className="mx-auto mt-2.5 max-w-[42rem] rounded-[0.8rem] border border-[#f0c77b]/22 bg-[#f0c77b]/8 px-3 py-2.5 text-sm leading-6 text-[var(--foreground)]">
          Runtime sync failed.
          <span className="ml-2 text-[var(--muted-foreground)]">
            {selectedThread.lastError}
          </span>
        </div>
      ) : null}
    </div>
  );
}
