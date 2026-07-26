import {
  CheckIcon,
  FileTextIcon,
  LoaderCircleIcon,
  PencilLineIcon,
  ScrollTextIcon,
  XIcon,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { MessageMarkdown } from "@/components/workspace/message-markdown";
import type { RuntimeSessionPlanMode } from "@/lib/runtime-api";
import { cn, formatRelativeTimestamp } from "@/lib/utils";

type ArtifactPanelPlanSurfaceProps = {
  canSubmitDecision: boolean;
  notesDraft: string;
  onNotesDraftChange: (value: string) => void;
  onReload: () => void;
  onSubmitDecision: (decision: "approve" | "request_changes" | "quit") => void;
  plan: RuntimeSessionPlanMode | null;
  planActionPending: boolean;
  planError: string | null;
  planLoading: boolean;
  planStatusLabel: string;
  sessionId?: string;
};

function formatModeLabel(value?: string) {
  const normalized = value?.trim();
  if (!normalized) {
    return "—";
  }
  return normalized;
}

export function ArtifactPanelPlanSurface({
  canSubmitDecision,
  notesDraft,
  onNotesDraftChange,
  onReload,
  onSubmitDecision,
  plan,
  planActionPending,
  planError,
  planLoading,
  planStatusLabel,
  sessionId,
}: ArtifactPanelPlanSurfaceProps) {
  const writeAllowPaths = plan?.write_allow_paths ?? [];
  const showDecisionActions = canSubmitDecision;

  return (
    <div className="grid min-h-0 flex-1 gap-2.5 overflow-auto p-2.5">
      <section className="flex min-h-0 flex-col overflow-hidden rounded-[0.95rem] border border-white/8 bg-white/[0.035]">
        <div className="flex items-start justify-between gap-3 border-b border-white/8 px-3 py-2.5">
          <div className="min-w-0 space-y-1">
            <div className="inline-flex items-center gap-2 text-[10px] uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
              <ScrollTextIcon size={14} />
              Plan preview
            </div>
            <div className="truncate text-sm text-[var(--foreground)]">
              {plan?.plan_path?.trim() || "plan.md"}
            </div>
          </div>
          <div className="flex flex-wrap items-center justify-end gap-1.5">
            <Badge
              className={cn(
                plan?.active
                  ? "border-[#8fd0c6]/30 bg-[#8fd0c6]/10 text-[#8fd0c6]"
                  : undefined,
              )}
            >
              {planStatusLabel}
            </Badge>
            <Button
              disabled={!sessionId || planLoading || planActionPending}
              onClick={() => onReload()}
              size="sm"
              type="button"
              variant="ghost"
            >
              Refresh
            </Button>
          </div>
        </div>

        <div className="min-h-0 flex-1 overflow-auto px-2.5 py-2.5">
          {planLoading ? (
            <div className="mb-2 inline-flex items-center gap-2 text-[10px] uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
              <LoaderCircleIcon size={14} className="animate-spin" />
              Loading
            </div>
          ) : null}

          {!sessionId ? (
            <div className="flex h-full items-center justify-center rounded-[0.8rem] border border-dashed border-white/10 px-3 py-5 text-center text-sm leading-6 text-[var(--muted-foreground)]">
              Plan preview becomes available after the thread attaches to a live
              session.
            </div>
          ) : planError ? (
            <div className="rounded-[0.85rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-3.5 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
              {planError}
            </div>
          ) : (
            <div className="space-y-3">
              <div className="grid gap-2 rounded-[0.85rem] border border-white/8 bg-black/10 px-3 py-2.5 text-sm leading-6 text-[var(--muted-foreground)]">
                <div className="flex flex-wrap gap-x-4 gap-y-1">
                  <span>
                    Permission:{" "}
                    <span className="text-[var(--foreground)]">
                      {formatModeLabel(plan?.permission_mode)}
                    </span>
                  </span>
                  <span>
                    Previous:{" "}
                    <span className="text-[var(--foreground)]">
                      {formatModeLabel(plan?.previous_mode)}
                    </span>
                  </span>
                  {plan?.exit_decision ? (
                    <span>
                      Last decision:{" "}
                      <span className="text-[var(--foreground)]">
                        {plan.exit_decision}
                      </span>
                    </span>
                  ) : null}
                </div>
                <div className="flex flex-wrap gap-x-4 gap-y-1">
                  {plan?.entered_at ? (
                    <span>Entered {formatRelativeTimestamp(plan.entered_at)}</span>
                  ) : null}
                  {plan?.exited_at ? (
                    <span>Exited {formatRelativeTimestamp(plan.exited_at)}</span>
                  ) : null}
                  {plan?.workspace_path ? (
                    <span className="truncate" title={plan.workspace_path}>
                      Workspace: {plan.workspace_path}
                    </span>
                  ) : null}
                </div>
                {writeAllowPaths.length > 0 ? (
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span>Write allow:</span>
                    {writeAllowPaths.map((path) => (
                      <Badge key={path}>{path}</Badge>
                    ))}
                  </div>
                ) : null}
                {plan?.notes ? (
                  <div>
                    Notes:{" "}
                    <span className="text-[var(--foreground)]">{plan.notes}</span>
                  </div>
                ) : null}
              </div>

              {plan?.plan_content_error ? (
                <div className="rounded-[0.85rem] border border-[#f59e7d]/18 bg-[#f59e7d]/8 px-3.5 py-3 text-sm leading-6 text-[var(--muted-foreground)]">
                  {plan.plan_content_error}
                </div>
              ) : null}

              {plan?.plan_content_available ? (
                <div className="overflow-hidden rounded-[0.85rem] border border-white/8 bg-black/15">
                  <div className="flex items-center justify-between gap-2 border-b border-white/8 px-3 py-2 text-[10px] uppercase tracking-[0.16em] text-[var(--muted-foreground)]">
                    <span className="inline-flex items-center gap-1.5">
                      <FileTextIcon size={13} />
                      Plan content
                    </span>
                    {plan.plan_content_truncated ? <Badge>Truncated</Badge> : null}
                  </div>
                  <div className="max-h-[28rem] overflow-auto px-3 py-3">
                    <MessageMarkdown content={plan.plan_content} />
                  </div>
                </div>
              ) : (
                <div className="rounded-[0.85rem] border border-dashed border-white/10 px-3.5 py-5 text-center text-sm leading-6 text-[var(--muted-foreground)]">
                  {plan?.active
                    ? "Plan mode is active, but the plan file is not available yet."
                    : "No plan content is available for this session."}
                </div>
              )}

              <div className="space-y-2 rounded-[0.85rem] border border-white/8 bg-black/10 px-3 py-2.5">
                <label
                  className="block text-[10px] uppercase tracking-[0.16em] text-[var(--muted-foreground)]"
                  htmlFor="artifact-panel-plan-notes"
                >
                  Review notes
                </label>
                <textarea
                  className="min-h-[4.5rem] w-full resize-y rounded-[0.7rem] border border-white/10 bg-black/20 px-3 py-2 text-sm leading-6 text-[var(--foreground)] outline-none placeholder:text-[var(--muted-foreground)] focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
                  disabled={!sessionId || planActionPending}
                  id="artifact-panel-plan-notes"
                  onChange={(event) => onNotesDraftChange(event.target.value)}
                  placeholder="Optional notes for approve / request changes / quit"
                  value={notesDraft}
                />
                <div className="flex flex-wrap gap-1.5">
                  <Button
                    disabled={!showDecisionActions || planActionPending}
                    onClick={() => onSubmitDecision("approve")}
                    size="sm"
                    type="button"
                    variant="primary"
                  >
                    <CheckIcon size={14} />
                    Approve
                  </Button>
                  <Button
                    disabled={!showDecisionActions || planActionPending}
                    onClick={() => onSubmitDecision("request_changes")}
                    size="sm"
                    type="button"
                    variant="secondary"
                  >
                    <PencilLineIcon size={14} />
                    Request changes
                  </Button>
                  <Button
                    disabled={!showDecisionActions || planActionPending}
                    onClick={() => onSubmitDecision("quit")}
                    size="sm"
                    type="button"
                    variant="ghost"
                  >
                    <XIcon size={14} />
                    Quit
                  </Button>
                </div>
                {!showDecisionActions ? (
                  <div className="text-xs leading-5 text-[var(--muted-foreground)]">
                    Decision actions unlock while plan mode is active.
                  </div>
                ) : null}
              </div>
            </div>
          )}
        </div>
      </section>
    </div>
  );
}
