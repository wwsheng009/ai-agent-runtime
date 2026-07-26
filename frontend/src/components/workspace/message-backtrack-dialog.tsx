import { HistoryIcon, LoaderCircleIcon, XIcon } from "lucide-react";
import { useEffect, useId, useRef } from "react";
import { createPortal } from "react-dom";

import { Button } from "@/components/ui/button";
import type { SessionBacktrackDialogState } from "@/hooks/workspace/use-session-backtrack";
import type { RuntimeSessionBacktrackMode } from "@/lib/runtime-api";
import { cn } from "@/lib/utils";

type MessageBacktrackDialogProps = {
  onApply: () => void;
  onClose: () => void;
  onEditPromptChange: (value: string) => void;
  onModeChange: (mode: RuntimeSessionBacktrackMode) => void;
  onPrefillChange: (prefill: boolean) => void;
  state: SessionBacktrackDialogState;
};

export function MessageBacktrackDialog({
  onApply,
  onClose,
  onEditPromptChange,
  onModeChange,
  onPrefillChange,
  state,
}: MessageBacktrackDialogProps) {
  const titleId = useId();
  const closeRef = useRef<HTMLButtonElement | null>(null);

  useEffect(() => {
    if (!state.open) {
      return;
    }
    closeRef.current?.focus();
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !state.busy) {
        onClose();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onClose, state.busy, state.open]);

  if (!state.open || typeof document === "undefined") {
    return null;
  }

  const preview = state.preview;
  const target = state.target;
  const removedMessages = preview?.removed_message_count ?? null;
  const removedTurns = preview?.removed_user_turns ?? null;
  const canApply = Boolean(preview) && !state.busy && !state.error?.includes("busy");

  return createPortal(
    <div
      aria-labelledby={titleId}
      aria-modal="true"
      className="fixed inset-0 z-[80] flex items-center justify-center bg-black/55 px-4 py-6 backdrop-blur-[2px]"
      role="dialog"
    >
      <div className="w-full max-w-[32rem] overflow-hidden rounded-[1rem] border border-white/10 bg-[var(--dialog-bg,var(--background))] shadow-[0_24px_80px_rgba(0,0,0,0.45)]">
        <div className="flex items-start justify-between gap-3 border-b border-white/8 px-4 py-3.5">
          <div className="flex items-start gap-2.5">
            <div className="mt-0.5 inline-flex size-8 items-center justify-center rounded-[0.75rem] border border-[#f0c77b]/20 bg-[#f0c77b]/10 text-[#f0c77b]">
              <HistoryIcon size={16} />
            </div>
            <div>
              <h2
                className="text-sm font-semibold tracking-[-0.01em] text-[var(--foreground)]"
                id={titleId}
              >
                Backtrack to this user message
              </h2>
              <p className="mt-1 text-sm leading-6 text-[var(--muted-foreground)]">
                Truncate the conversation after this turn and optionally restore
                later file mutations.
              </p>
            </div>
          </div>
          <button
            ref={closeRef}
            aria-label="Close backtrack dialog"
            className="inline-flex size-8 items-center justify-center rounded-[0.7rem] border border-white/10 text-[var(--muted-foreground)] transition hover:bg-white/6 hover:text-[var(--foreground)]"
            disabled={state.busy}
            onClick={onClose}
            type="button"
          >
            <XIcon size={16} />
          </button>
        </div>

        <div className="space-y-4 px-4 py-4">
          <div className="rounded-[0.85rem] border border-white/8 bg-white/[0.03] px-3.5 py-3">
            <div className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              Anchor
              {target ? ` · turn ${target.userTurnIndex}` : null}
            </div>
            <p className="mt-2 text-sm leading-6 text-[var(--foreground)]">
              {target?.preview?.trim() || preview?.anchor_preview || "(empty)"}
            </p>
          </div>

          {state.busy && !preview ? (
            <div className="inline-flex items-center gap-2 text-sm text-[var(--muted-foreground)]">
              <LoaderCircleIcon className="animate-spin" size={14} />
              Planning backtrack…
            </div>
          ) : null}

          {preview ? (
            <div className="grid gap-2 rounded-[0.85rem] border border-white/8 bg-white/[0.03] px-3.5 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
              <div>
                Will remove{" "}
                <span className="text-[var(--foreground)]">
                  {removedMessages ?? "?"} messages
                </span>
                {" / "}
                <span className="text-[var(--foreground)]">
                  {removedTurns ?? "?"} later user turns
                </span>
                .
              </div>
              <div>
                History keeps the first{" "}
                <span className="text-[var(--foreground)]">
                  {preview.truncated_to_message_count}
                </span>{" "}
                messages.
              </div>
              {preview.base_checkpoint_id ? (
                <div>
                  Code restore can use base checkpoint{" "}
                  <span className="text-[var(--foreground)]">
                    {preview.base_checkpoint_id.slice(0, 12)}
                  </span>
                  {preview.later_checkpoint_ids?.length
                    ? ` (+${preview.later_checkpoint_ids.length} later)`
                    : null}
                  .
                </div>
              ) : (
                <div>No mutation checkpoint is mapped to this turn yet.</div>
              )}
              {preview.warnings?.length
                ? preview.warnings.map((warning) => (
                    <div key={warning} className="text-[#f0c77b]">
                      {warning}
                    </div>
                  ))
                : null}
            </div>
          ) : null}

          <fieldset className="space-y-2">
            <legend className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              Restore mode
            </legend>
            {(
              [
                ["conversation", "Conversation only"],
                ["both", "Conversation + files"],
                ["code", "Files only (advanced)"],
              ] as const
            ).map(([value, label]) => (
              <label
                key={value}
                className={cn(
                  "flex cursor-pointer items-center gap-2 rounded-[0.75rem] border px-3 py-2 text-sm transition",
                  state.mode === value
                    ? "border-[#f0c77b]/30 bg-[#f0c77b]/8 text-[var(--foreground)]"
                    : "border-white/8 bg-white/[0.02] text-[var(--muted-foreground)] hover:border-white/14",
                )}
              >
                <input
                  checked={state.mode === value}
                  className="accent-[#f0c77b]"
                  disabled={state.busy}
                  name="backtrack-mode"
                  onChange={() => onModeChange(value)}
                  type="radio"
                  value={value}
                />
                <span>{label}</span>
              </label>
            ))}
          </fieldset>

          <label className="grid gap-2">
            <span className="app-text-10 uppercase tracking-[0.14em] text-[var(--muted-foreground)]">
              Edit prompt before prefill
            </span>
            <textarea
              aria-label="Edit backtrack prompt"
              className="min-h-[7.5rem] w-full resize-y rounded-[0.85rem] border border-white/10 bg-white/[0.03] px-3 py-2.5 text-sm leading-6 text-[var(--foreground)] outline-none transition placeholder:text-[var(--muted-foreground)] focus:border-[#f0c77b]/35 focus:bg-white/[0.05]"
              disabled={state.busy}
              onChange={(event) => onEditPromptChange(event.target.value)}
              placeholder="Edit the original user prompt…"
              value={state.editPrompt}
            />
            <span className="text-xs leading-5 text-[var(--muted-foreground)]">
              Leave unchanged to keep the original text. Edits are sent as
              edit_prompt and prefilled into the composer after apply.
            </span>
          </label>

          <label className="flex items-center gap-2 text-sm text-[var(--muted-foreground)]">
            <input
              checked={state.prefillComposer}
              className="accent-[#f0c77b]"
              disabled={state.busy}
              onChange={(event) => onPrefillChange(event.target.checked)}
              type="checkbox"
            />
            Prefill composer with the original (or edited) prompt
          </label>

          {state.error ? (
            <div className="rounded-[0.85rem] border border-[#f59e7d]/20 bg-[#f59e7d]/10 px-3 py-2.5 text-sm leading-6 text-[var(--foreground)]">
              {state.error}
            </div>
          ) : null}
        </div>

        <div className="flex items-center justify-end gap-2 border-t border-white/8 px-4 py-3">
          <Button disabled={state.busy} onClick={onClose} variant="ghost">
            Cancel
          </Button>
          <Button disabled={!canApply} onClick={onApply}>
            {state.busy ? (
              <span className="inline-flex items-center gap-2">
                <LoaderCircleIcon className="animate-spin" size={14} />
                Working…
              </span>
            ) : (
              "Confirm backtrack"
            )}
          </Button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
