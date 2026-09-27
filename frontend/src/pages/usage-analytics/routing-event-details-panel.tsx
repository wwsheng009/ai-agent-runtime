import { XIcon } from "lucide-react";
import { useEffect, useId, useRef, type KeyboardEvent } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import type { AnalyticsRouteEvent } from "@/types/runtime";

import { formatNumber, formatTimestamp } from "./format";
import {
  difficultySourceLabel,
  kindLabel,
  reasonLabel,
  routeFlagKeys,
  scopeLabel,
  sourceLabel,
  taskTypeLabel,
  triStateLabel,
  warningLabel,
} from "./routing-labels";

type DetailGroup = {
  title: string;
  fields: { label: string; value?: string | number; wide?: boolean }[];
};

export function RoutingEventDetailsPanel({
  event,
  onClose,
}: {
  event: AnalyticsRouteEvent;
  onClose: () => void;
}) {
  const { t } = useTranslation("usageAnalytics");
  const titleId = useId();
  const panelRef = useRef<HTMLDivElement>(null);
  const notRecorded = t("observability.routing.flags.notRecorded");
  const warnings = event.warnings ?? [];
  useDialogLifecycle(true, onClose);

  useEffect(() => {
    const previousFocus = document.activeElement;
    panelRef.current?.querySelector("button")?.focus();
    return () => {
      if (previousFocus instanceof HTMLElement && previousFocus.isConnected) {
        previousFocus.focus({ preventScroll: true });
      }
    };
  }, []);

  const keepFocusInPanel = (keyEvent: KeyboardEvent<HTMLDivElement>) => {
    if (keyEvent.key !== "Tab") return;
    const controls = keyEvent.currentTarget.querySelectorAll<HTMLElement>(
      'button, [href], input, select, textarea, [tabindex="0"]',
    );
    const first = controls[0];
    const last = controls[controls.length - 1];
    if (keyEvent.shiftKey && document.activeElement === first) {
      keyEvent.preventDefault();
      last?.focus();
    } else if (!keyEvent.shiftKey && document.activeElement === last) {
      keyEvent.preventDefault();
      first?.focus();
    }
  };

  const groups: DetailGroup[] = [
    {
      title: t("observability.routing.details.taskTitle"),
      fields: [
        { label: t("observability.routing.details.agentId"), value: event.agent_id },
        { label: t("observability.routing.distributions.role"), value: event.role },
        { label: t("observability.routing.columns.taskType"), value: taskTypeLabel(t, event.task_type) },
        { label: t("observability.routing.details.taskSubject"), value: event.task_subject, wide: true },
        { label: t("observability.routing.columns.goal"), value: event.goal, wide: true },
      ],
    },
    {
      title: t("observability.routing.details.routeTitle"),
      fields: [
        { label: t("observability.routing.distributions.provider"), value: event.provider },
        { label: t("observability.routing.distributions.model"), value: event.model },
        { label: t("observability.routing.columns.difficulty"), value: event.difficulty },
        { label: t("observability.routing.distributions.difficultySource"), value: event.difficulty_source ? difficultySourceLabel(t, event.difficulty_source) : undefined },
        { label: t("observability.routing.details.reasoningEffort"), value: event.reasoning_effort },
        { label: t("observability.routing.distributions.source"), value: event.source ? sourceLabel(t, event.source) : undefined },
        { label: t("observability.routing.columns.reason"), value: event.reason ? reasonLabel(t, event.reason) : undefined, wide: true },
        { label: t("observability.routing.metrics.routeChanged"), value: triStateLabel(t, event.route_changed, routeFlagKeys.routeChanged, routeFlagKeys.routeUnchanged) },
        { label: t("observability.routing.metrics.fallbackUsed"), value: triStateLabel(t, event.fallback_used, routeFlagKeys.fallbackUsed, routeFlagKeys.fallbackUnused) },
        { label: t("observability.routing.details.candidateCount"), value: event.candidate_count },
        { label: t("observability.routing.details.fallbackReason"), value: event.fallback_reason, wide: true },
      ],
    },
    {
      title: t("observability.routing.details.contextTitle"),
      fields: [
        { label: t("observability.routing.columns.time"), value: event.recorded_at, wide: true },
        { label: t("observability.routing.columns.scope"), value: scopeLabel(t, event.scope) },
        { label: t("observability.routing.columns.kind"), value: kindLabel(t, event.kind) },
        { label: t("observability.routing.details.step"), value: event.step },
        { label: t("observability.routing.columns.attempt"), value: event.attempt },
        { label: t("observability.routing.details.maxAttempts"), value: event.max_attempts },
        { label: t("observability.routing.details.sessionId"), value: event.session_id, wide: true },
        { label: t("observability.routing.details.parentSessionId"), value: event.parent_session_id, wide: true },
        { label: t("observability.routing.details.childSessionId"), value: event.child_session_id, wide: true },
        { label: t("observability.routing.details.traceId"), value: event.trace_id, wide: true },
        { label: t("observability.routing.details.batchId"), value: event.batch_id, wide: true },
      ],
    },
  ];

  if (typeof document === "undefined") return null;

  return createPortal(
    <DialogOverlay className="z-[100] items-stretch justify-end p-0" onDismiss={onClose}>
      <DialogPanel
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className="h-dvh max-h-dvh max-w-2xl rounded-none border-y-0 border-r-0"
        onKeyDown={keepFocusInPanel}
      >
        <div className="flex shrink-0 items-start justify-between gap-3 border-b border-border px-4 py-3">
          <div className="min-w-0">
            <h2 id={titleId} className="text-sm font-semibold">{t("observability.routing.details.title")}</h2>
            <p className="mt-1 truncate text-xs text-muted-foreground">{formatTimestamp(event.recorded_at)}</p>
          </div>
          <Button variant="ghost" size="icon" className="shrink-0" onClick={onClose} aria-label={t("observability.routing.details.close")}>
            <XIcon size={16} />
          </Button>
        </div>

        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto overscroll-contain p-4" tabIndex={0}>
          <section className="min-w-0 rounded-card border border-border p-3">
            <h3 className="flex items-center gap-2 text-xs font-semibold">
              {t("observability.routing.metrics.warnings")}
              {warnings.length > 0 ? (
                <Badge className="border-analytics-warning-border bg-analytics-warning-soft text-analytics-warning">
                  {t("observability.routing.warningCount", { count: warnings.length })}
                </Badge>
              ) : null}
            </h3>
            {warnings.length === 0 ? (
              <p className="mt-2 text-xs text-muted-foreground">{t("observability.routing.details.noWarnings")}</p>
            ) : (
              <ul className="mt-3 space-y-2">
                {warnings.map((warning, index) => {
                  const label = warningLabel(t, warning);
                  return (
                    <li key={`${index}-${warning}`} className="rounded-field border border-analytics-warning-border bg-analytics-warning-soft p-2.5">
                      <p className="whitespace-pre-wrap text-sm leading-6 text-analytics-warning [overflow-wrap:anywhere]">{label}</p>
                      {label !== warning ? (
                        <p className="mt-1 whitespace-pre-wrap break-all font-mono text-xs text-muted-foreground">{warning}</p>
                      ) : null}
                    </li>
                  );
                })}
              </ul>
            )}
          </section>

          {groups.map((group) => (
            <section key={group.title} className="min-w-0 rounded-card border border-border p-3">
              <h3 className="text-xs font-semibold">{group.title}</h3>
              <dl className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
                {group.fields.map((field) => (
                  <div key={field.label} className={field.wide ? "min-w-0 sm:col-span-2" : "min-w-0"}>
                    <dt className="text-xs text-muted-foreground">{field.label}</dt>
                    <dd className="mt-1 whitespace-pre-wrap text-sm leading-6 [overflow-wrap:anywhere]">
                      {typeof field.value === "number" ? formatNumber(field.value) : field.value?.trim() ? field.value : notRecorded}
                    </dd>
                  </div>
                ))}
              </dl>
            </section>
          ))}
        </div>
      </DialogPanel>
    </DialogOverlay>,
    document.body,
  );
}
