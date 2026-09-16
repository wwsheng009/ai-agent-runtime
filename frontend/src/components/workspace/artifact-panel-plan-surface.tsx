import {
  CheckIcon,
  FileTextIcon,
  LoaderCircleIcon,
  PencilLineIcon,
  ScrollTextIcon,
  XIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";

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
  const { t } = useTranslation("workspace");
  const { t: tCommon } = useTranslation("common");
  const writeAllowPaths = plan?.write_allow_paths ?? [];
  const showDecisionActions = canSubmitDecision;

  return (
    <div className="grid min-h-0 flex-1 gap-2.5 overflow-auto p-2.5">
      <section className="flex min-h-0 flex-col overflow-hidden rounded-panel-lg border border-white/8 bg-white/[0.035]">
        <div className="flex items-start justify-between gap-3 border-b border-white/8 px-3 py-2.5">
          <div className="min-w-0 space-y-1">
            <div className="inline-flex items-center gap-2 app-text-10 uppercase tracking-[0.16em] text-muted-foreground">
              <ScrollTextIcon size={14} />
              {t("panels.artifacts.plan.title")}
            </div>
            <div className="truncate text-sm text-foreground">
              {plan?.plan_path?.trim() || "plan.md"}
            </div>
          </div>
          <div className="flex flex-wrap items-center justify-end gap-1.5">
            <Badge
              className={cn(
                plan?.active
                  ? "border-accent-teal/30 bg-accent-teal/10 text-accent-teal"
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
              {tCommon("actions.refresh")}
            </Button>
          </div>
        </div>

        <div className="min-h-0 flex-1 overflow-auto px-2.5 py-2.5">
          {planLoading ? (
            <div className="mb-2 inline-flex items-center gap-2 app-text-10 uppercase tracking-[0.16em] text-muted-foreground">
              <LoaderCircleIcon size={14} className="animate-spin" />
              {t("panels.artifacts.plan.loading")}
            </div>
          ) : null}

          {!sessionId ? (
            <div className="flex h-full items-center justify-center rounded-card border border-dashed border-white/10 px-3 py-5 text-center text-sm leading-6 text-muted-foreground">
              {t("panels.artifacts.plan.noSession")}
            </div>
          ) : planError ? (
            <div className="rounded-card-lg border border-accent-orange/18 bg-accent-orange/8 px-3.5 py-3 text-sm leading-6 text-muted-foreground">
              {planError}
            </div>
          ) : (
            <div className="space-y-3">
              <div className="grid gap-2 rounded-card-lg border border-white/8 bg-black/10 px-3 py-2.5 text-sm leading-6 text-muted-foreground">
                <div className="flex flex-wrap gap-x-4 gap-y-1">
                  <span>
                    {t("panels.artifacts.plan.permission")}{" "}
                    <span className="text-foreground">
                      {formatModeLabel(plan?.permission_mode)}
                    </span>
                  </span>
                  <span>
                    {t("panels.artifacts.plan.previous")}{" "}
                    <span className="text-foreground">
                      {formatModeLabel(plan?.previous_mode)}
                    </span>
                  </span>
                  {plan?.exit_decision ? (
                    <span>
                      {t("panels.artifacts.plan.lastDecision")}{" "}
                      <span className="text-foreground">
                        {plan.exit_decision}
                      </span>
                    </span>
                  ) : null}
                </div>
                <div className="flex flex-wrap gap-x-4 gap-y-1">
                  {plan?.entered_at ? (
                    <span>
                      {t("panels.artifacts.plan.entered", {
                        time: formatRelativeTimestamp(plan.entered_at),
                      })}
                    </span>
                  ) : null}
                  {plan?.exited_at ? (
                    <span>
                      {t("panels.artifacts.plan.exited", {
                        time: formatRelativeTimestamp(plan.exited_at),
                      })}
                    </span>
                  ) : null}
                  {plan?.workspace_path ? (
                    <span className="truncate" title={plan.workspace_path}>
                      {t("panels.artifacts.plan.workspacePath", {
                        path: plan.workspace_path,
                      })}
                    </span>
                  ) : null}
                </div>
                {writeAllowPaths.length > 0 ? (
                  <div className="flex flex-wrap items-center gap-1.5">
                    <span>{t("panels.artifacts.plan.writeAllow")}</span>
                    {writeAllowPaths.map((path) => (
                      <Badge key={path}>{path}</Badge>
                    ))}
                  </div>
                ) : null}
                {plan?.notes ? (
                  <div>
                    {t("panels.artifacts.plan.notes")}{" "}
                    <span className="text-foreground">{plan.notes}</span>
                  </div>
                ) : null}
              </div>

              {plan?.plan_content_error ? (
                <div className="rounded-card-lg border border-accent-orange/18 bg-accent-orange/8 px-3.5 py-3 text-sm leading-6 text-muted-foreground">
                  {plan.plan_content_error}
                </div>
              ) : null}

              {plan?.plan_content_available ? (
                <div className="overflow-hidden rounded-card-lg border border-white/8 bg-black/15">
                  <div className="flex items-center justify-between gap-2 border-b border-white/8 px-3 py-2 app-text-10 uppercase tracking-[0.16em] text-muted-foreground">
                    <span className="inline-flex items-center gap-1.5">
                      <FileTextIcon size={13} />
                      {t("panels.artifacts.plan.contentTitle")}
                    </span>
                    {plan.plan_content_truncated ? (
                      <Badge>{t("panels.artifacts.plan.truncated")}</Badge>
                    ) : null}
                  </div>
                  <div className="max-h-[28rem] overflow-auto px-3 py-3">
                    <MessageMarkdown content={plan.plan_content} />
                  </div>
                </div>
              ) : (
                <div className="rounded-card-lg border border-dashed border-white/10 px-3.5 py-5 text-center text-sm leading-6 text-muted-foreground">
                  {plan?.active
                    ? "Plan mode is active, but the plan file is not available yet."
                    : "No plan content is available for this session."}
                </div>
              )}

              <div className="space-y-2 rounded-card-lg border border-white/8 bg-black/10 px-3 py-2.5">
                <label
                  className="block app-text-10 uppercase tracking-[0.16em] text-muted-foreground"
                  htmlFor="artifact-panel-plan-notes"
                >
                  {t("panels.artifacts.plan.reviewNotes")}
                </label>
                <textarea
                  className="min-h-[4.5rem] w-full resize-y rounded-field border border-white/10 bg-black/20 px-3 py-2 text-sm leading-6 text-foreground outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring"
                  disabled={!sessionId || planActionPending}
                  id="artifact-panel-plan-notes"
                  onChange={(event) => onNotesDraftChange(event.target.value)}
                  placeholder={t("panels.artifacts.plan.notesPlaceholder")}
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
                    {t("panels.artifacts.plan.approve")}
                  </Button>
                  <Button
                    disabled={!showDecisionActions || planActionPending}
                    onClick={() => onSubmitDecision("request_changes")}
                    size="sm"
                    type="button"
                    variant="secondary"
                  >
                    <PencilLineIcon size={14} />
                    {t("panels.artifacts.plan.requestChanges")}
                  </Button>
                  <Button
                    disabled={!showDecisionActions || planActionPending}
                    onClick={() => onSubmitDecision("quit")}
                    size="sm"
                    type="button"
                    variant="ghost"
                  >
                    <XIcon size={14} />
                    {t("panels.artifacts.plan.quit")}
                  </Button>
                </div>
                {!showDecisionActions ? (
                  <div className="text-xs leading-5 text-muted-foreground">
                    {t("panels.artifacts.plan.decisionLocked")}
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
