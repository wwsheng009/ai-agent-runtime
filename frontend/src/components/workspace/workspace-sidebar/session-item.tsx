// 由 components/workspace/workspace-sidebar.tsx 机械拆分而来（P0-2），仅搬迁不改语义。

import { PencilIcon } from "lucide-react";
import { useRef, useState } from "react";
import { type RuntimeSessionRecord } from "@/lib/runtime-api";
import { cn } from "@/lib/utils";

import { SidebarStateIcon } from "./state-icons";
import { type SidebarStateIconSpec } from "./types";

export type InlineRenameInputProps = {
  ariaLabel: string;
  initial: string;
  onCancel: () => void;
  onSubmit: (value: string) => void;
  placeholder: string;
};

export function InlineRenameInput({
  ariaLabel,
  initial,
  onCancel,
  onSubmit,
  placeholder,
}: InlineRenameInputProps) {
  const [value, setValue] = useState(initial);
  const settledRef = useRef(false);

  return (
    <input
      autoFocus
      value={value}
      aria-label={ariaLabel}
      onChange={(event) => setValue(event.target.value)}
      onClick={(event) => event.stopPropagation()}
      onKeyDown={(event) => {
        if (event.key === "Enter") {
          event.preventDefault();
          const trimmed = value.trim();
          if (!trimmed) {
            settledRef.current = true;
            onCancel();
            return;
          }
          settledRef.current = true;
          onSubmit(trimmed);
        } else if (event.key === "Escape") {
          event.preventDefault();
          settledRef.current = true;
          onCancel();
        }
      }}
      onBlur={() => {
        if (!settledRef.current) {
          onCancel();
        }
      }}
      placeholder={placeholder}
      spellCheck={false}
      className="w-full min-w-0 rounded-[0.55rem] border border-[var(--accent-primary-border)] bg-[var(--surface-solid)] px-2 py-1 text-sm text-[var(--foreground)] outline-none"
    />
  );
}

export type SidebarSessionItemProps = {
  isActive: boolean;
  onCancelRename: () => void;
  onRenameSubmit: (sessionId: string, title: string) => void;
  onSelect: () => void;
  onStartRename: (sessionId: string, currentTitle: string) => void;
  renameLabels: {
    placeholder: string;
    rename: string;
  };
  renaming: boolean;
  session: RuntimeSessionRecord;
  statusIcon: SidebarStateIconSpec;
  title: string;
};

export function SidebarSessionItem({
  isActive,
  onCancelRename,
  onRenameSubmit,
  onSelect,
  onStartRename,
  renameLabels,
  renaming,
  session,
  statusIcon,
  title,
}: SidebarSessionItemProps) {
  if (renaming) {
    return (
      <div
        className={cn(
          "flex w-full items-center gap-2 rounded-[0.72rem] border px-2 py-1 text-left transition",
          isActive
            ? "border-[var(--accent-secondary-border)] bg-[var(--accent-secondary-soft)]"
            : "border-[var(--border)] bg-[var(--surface-softer)]",
        )}
      >
        <InlineRenameInput
          ariaLabel={renameLabels.rename}
          initial={title}
          placeholder={renameLabels.placeholder}
          onCancel={onCancelRename}
          onSubmit={(value) => onRenameSubmit(session.id, value)}
        />
      </div>
    );
  }

  return (
    <div className="group/session relative">
      <button
        type="button"
        title={`${title} · ${statusIcon.label}`}
        onClick={onSelect}
        className={cn(
          "flex w-full items-center gap-2 rounded-[0.72rem] border py-1.5 pl-2 pr-7 text-left transition",
          isActive
            ? "border-[var(--accent-secondary-border)] bg-[var(--accent-secondary-soft)]"
            : "border-[var(--border)] bg-[var(--surface-softer)] hover:border-[var(--border-strong)] hover:bg-[var(--surface-soft)]",
        )}
      >
        <div className="min-w-0 flex-1 truncate text-sm font-medium text-[var(--foreground)]">
          {title}
        </div>
        <SidebarStateIcon spec={statusIcon} />
      </button>
      <button
        type="button"
        aria-label={renameLabels.rename}
        title={renameLabels.rename}
        onClick={(event) => {
          event.stopPropagation();
          onStartRename(session.id, title);
        }}
        className="absolute right-1 top-1/2 -translate-y-1/2 rounded-[0.5rem] p-1 text-[var(--muted-foreground)] opacity-0 transition hover:bg-[var(--surface-soft)] hover:text-[var(--foreground)] focus-visible:opacity-100 group-hover/session:opacity-100"
      >
        <PencilIcon size={12} />
      </button>
    </div>
  );
}
