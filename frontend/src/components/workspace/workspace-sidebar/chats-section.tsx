// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { MessagesSquareIcon } from "lucide-react";
import { describeThreadSession } from "@/components/workspace/workspace-sidebar-shared";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import { type TFunction } from "i18next";

import { buildSidebarIconLabels, buildThreadSessionDetails } from "./labels";
import { SidebarSection } from "./section-shell";
import { SidebarStateIcon } from "./state-icons";
import { getSessionStatusIcon, getThreadWorkflowIcon } from "./state-icon-utils";
import {
  type SidebarSectionId,
  type SidebarSectionState,
  type SidebarThread,
} from "./types";

type WorkspaceSidebarChatsSectionProps = {
  chatThreads: SidebarThread[];
  deferredQuery: string;
  onSelectThread: (threadId: string) => void;
  openSections: SidebarSectionState;
  selectedThreadId: string;
  showChatsSection: boolean;
  t: TFunction<"workspace">;
  toggleSection: (section: SidebarSectionId) => void;
};

export function WorkspaceSidebarChatsSection({
  chatThreads,
  deferredQuery,
  onSelectThread,
  openSections,
  selectedThreadId,
  showChatsSection,
  t,
  toggleSection,
}: WorkspaceSidebarChatsSectionProps) {
  const sidebarLabels = buildSidebarIconLabels(t);
  const threadSessionDetails = buildThreadSessionDetails(t);

  return (
    showChatsSection ? (
    <SidebarSection
        id="chats"
        icon={MessagesSquareIcon}
        iconClassName="text-[var(--accent-primary)]"
        title={t("sidebar.sections.chats")}
        count={<Badge>{chatThreads.length}</Badge>}
        isOpen={openSections.chats}
        onToggle={toggleSection}
      >
          <div className="space-y-1">
            {chatThreads.length > 0 ? (
              chatThreads.map((thread) => {
                const isActive = thread.id === selectedThreadId;
                const sessionDescriptor = describeThreadSession(
                  thread,
                  threadSessionDetails,
                );
                const itemStatusIcons = [
                  getThreadWorkflowIcon(thread, sidebarLabels),
                  getSessionStatusIcon(sessionDescriptor.label, sidebarLabels),
                ];
                const title = [
              thread.title,
              ...itemStatusIcons.map((item) => item.label),
            ].join(" · ");

            return (
              <button
                key={thread.id}
                type="button"
                title={title}
                onClick={() => onSelectThread(thread.id)}
                className={cn(
                  "flex w-full items-center gap-2.5 rounded-[0.8rem] border px-2.5 py-2 text-left transition",
                  isActive
                    ? "border-[var(--accent-primary-border)] bg-[var(--accent-primary-soft)]"
                    : "border-[var(--border)] bg-[var(--surface-softer)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]",
                )}
              >
                <div className="min-w-0 flex-1 truncate text-base font-medium text-[var(--foreground)]">
                  {thread.title}
                </div>
                <div className="flex shrink-0 items-center gap-1.5">
                  {itemStatusIcons.map((item) => (
                    <SidebarStateIcon
                      key={`${thread.id}-${item.label}`}
                      spec={item}
                    />
                  ))}
                </div>
              </button>
            );
          })
        ) : (
          <div className="rounded-[0.8rem] border border-dashed border-[var(--border)] px-3 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
            {deferredQuery
              ? t("sidebar.emptyChats.search")
              : t("sidebar.emptyChats.default")}
          </div>
        )}
      </div>
      </SidebarSection>
    ) : null
  );
}
