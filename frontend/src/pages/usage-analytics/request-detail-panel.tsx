import { XIcon } from "lucide-react";
import { useEffect, useId, useRef, type KeyboardEvent } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DialogOverlay, DialogPanel } from "@/components/ui/dialog-shell";
import { useDialogLifecycle } from "@/components/ui/use-dialog-lifecycle";
import type { CacheRequestRecord } from "@/types/runtime";

import {
  formatDuration,
  formatFirstToken,
  formatNumber,
  formatPercent,
  formatTimestamp,
} from "./format";
import { requestHasReportedCache, requestRecordSuccess, requestSessionID } from "./request-analysis-data";
import { cacheStatusLabel } from "./request-labels";

type DetailField = {
  label: string;
  value?: string | number;
  wide?: boolean;
};

type DetailGroup = {
  title: string;
  fields: DetailField[];
};

export function RequestDetailPanel({
  record,
  onClose,
}: {
  record: CacheRequestRecord;
  onClose: () => void;
}) {
  const { t } = useTranslation("usageAnalytics");
  const titleId = useId();
  const panelRef = useRef<HTMLDivElement>(null);
  const sessionID = requestSessionID(record);
  const success = requestRecordSuccess(record);
  const notRecorded = t("requestAnalysis.details.notRecorded");
  const yes = t("requestAnalysis.details.yes");
  const no = t("requestAnalysis.details.no");
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

  const usage = record.usage;
  const groups: DetailGroup[] = [
    {
      title: t("requestAnalysis.details.sections.request"),
      fields: [
        { label: t("requestAnalysis.details.fields.requestId"), value: record.llm_request_id, wide: true },
        { label: t("requestAnalysis.details.fields.status"), value: record.status },
        { label: t("requestAnalysis.details.fields.errorCategory"), value: record.error_category },
        { label: t("requestAnalysis.details.fields.startedAt"), value: formatTimestamp(record.started_at), wide: true },
        {
          label: t("requestAnalysis.details.fields.finishedAt"),
          value: record.finished_at ? formatTimestamp(record.finished_at) : undefined,
          wide: true,
        },
        {
          label: t("requestAnalysis.details.fields.duration"),
          value: typeof record.duration_ms === "number" ? formatDuration(record.duration_ms) : undefined,
        },
        {
          label: t("requestAnalysis.details.fields.firstToken"),
          value: formatFirstToken(record.first_token_ms) ?? undefined,
        },
        { label: t("requestAnalysis.details.fields.step"), value: record.step },
        { label: t("requestAnalysis.details.fields.attempt"), value: record.attempt },
        {
          label: t("requestAnalysis.details.fields.stream"),
          value: typeof record.stream === "boolean" ? (record.stream ? yes : no) : undefined,
        },
      ],
    },
    {
      title: t("requestAnalysis.details.sections.route"),
      fields: [
        { label: t("requestAnalysis.details.fields.provider"), value: record.provider },
        { label: t("requestAnalysis.details.fields.model"), value: record.model },
        { label: t("requestAnalysis.details.fields.providerRequestId"), value: record.provider_request_id, wide: true },
      ],
    },
    {
      title: t("requestAnalysis.details.sections.usage"),
      fields: [
        { label: t("requestAnalysis.details.fields.promptTokens"), value: usage?.prompt_tokens },
        { label: t("requestAnalysis.details.fields.completionTokens"), value: usage?.completion_tokens },
        { label: t("requestAnalysis.details.fields.totalTokens"), value: usage?.total_tokens },
        { label: t("requestAnalysis.details.fields.reasoningTokens"), value: usage?.reasoning_tokens },
        { label: t("requestAnalysis.details.fields.cacheReadTokens"), value: usage?.cache_read_tokens },
        { label: t("requestAnalysis.details.fields.cacheCreationTokens"), value: usage?.cache_creation_tokens },
        { label: t("requestAnalysis.details.fields.uncachedInputTokens"), value: usage?.uncached_input_tokens },
        { label: t("requestAnalysis.details.fields.inputTotalTokens"), value: usage?.input_total_tokens },
        { label: t("requestAnalysis.details.fields.usageSource"), value: usage?.usage_source, wide: true },
        { label: t("requestAnalysis.details.fields.usageUnavailableReason"), value: record.usage_unavailable_reason, wide: true },
      ],
    },
    {
      title: t("requestAnalysis.details.sections.cache"),
      fields: [
        { label: t("requestAnalysis.details.fields.cacheStatus"), value: cacheStatusLabel(t, record.cache_status) },
        {
          label: t("requestAnalysis.details.fields.cacheHitRatio"),
          value: requestHasReportedCache(record) && typeof record.cache_hit_ratio === "number"
            ? formatPercent(record.cache_hit_ratio)
            : undefined,
        },
        {
          label: t("requestAnalysis.details.fields.cacheWriteRatio"),
          value: typeof record.cache_write_ratio === "number"
            ? formatPercent(record.cache_write_ratio)
            : undefined,
        },
        { label: t("requestAnalysis.details.fields.cacheEpoch"), value: record.cache_epoch },
        { label: t("requestAnalysis.details.fields.cacheLineageMode"), value: record.cache_lineage_mode },
        { label: t("requestAnalysis.details.fields.promptCacheKey"), value: record.prompt_cache_key, wide: true },
        { label: t("requestAnalysis.details.fields.promptFingerprint"), value: record.prompt_fingerprint, wide: true },
        { label: t("requestAnalysis.details.fields.parentPromptCacheKey"), value: record.parent_prompt_cache_key, wide: true },
      ],
    },
    {
      title: t("requestAnalysis.details.sections.session"),
      fields: [
        { label: t("requestAnalysis.details.fields.sessionId"), value: record.session_id, wide: true },
        { label: t("requestAnalysis.details.fields.rootSessionId"), value: record.root_session_id, wide: true },
        { label: t("requestAnalysis.details.fields.parentSessionId"), value: record.parent_session_id, wide: true },
        { label: t("requestAnalysis.details.fields.subagentId"), value: record.subagent_id, wide: true },
        { label: t("requestAnalysis.details.fields.traceId"), value: record.trace_id, wide: true },
        { label: t("requestAnalysis.details.fields.turnId"), value: record.turn_id, wide: true },
        { label: t("requestAnalysis.details.fields.userMessageId"), value: record.user_message_id, wide: true },
        { label: t("requestAnalysis.details.fields.assistantMessageId"), value: record.assistant_message_id, wide: true },
      ],
    },
    {
      title: t("requestAnalysis.details.sections.diagnostics"),
      fields: [
        { label: t("requestAnalysis.details.fields.contextPromptTokens"), value: record.context_prompt_tokens },
        { label: t("requestAnalysis.details.fields.contextWindowTokens"), value: record.context_window_tokens },
        { label: t("requestAnalysis.details.fields.promptBudget"), value: record.prompt_budget },
        { label: t("requestAnalysis.details.fields.contextMode"), value: record.context_mode },
        { label: t("requestAnalysis.details.fields.correlationSource"), value: record.correlation_source },
        { label: t("requestAnalysis.details.fields.taskAttempt"), value: record.task_attempt },
        {
          label: t("requestAnalysis.details.fields.terminalSeen"),
          value: typeof record.terminal_seen === "boolean" ? (record.terminal_seen ? yes : no) : undefined,
        },
        { label: t("requestAnalysis.details.fields.argErrorClass"), value: record.arg_error_class },
        { label: t("requestAnalysis.details.fields.cancelSource"), value: record.cancel_source },
        { label: t("requestAnalysis.details.fields.cancelCause"), value: record.cancel_cause },
        { label: t("requestAnalysis.details.fields.cancelReason"), value: record.cancel_reason, wide: true },
        { label: t("requestAnalysis.details.fields.forkSnapshotHash"), value: record.fork_snapshot_hash, wide: true },
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
        className="h-dvh max-h-dvh max-w-3xl rounded-none border-y-0 border-r-0"
        onKeyDown={keepFocusInPanel}
      >
        <div className="flex shrink-0 items-start justify-between gap-3 border-b border-border px-4 py-3">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h2 id={titleId} className="text-sm font-semibold">
                {t("requestAnalysis.details.title")}
              </h2>
              <Badge className={success
                ? "border-analytics-success-border bg-analytics-success-soft text-analytics-success"
                : "border-analytics-danger-border bg-analytics-danger-soft text-analytics-danger"}
              >
                {success
                  ? t("requestAnalysis.table.success")
                  : t("requestAnalysis.table.failure")}
              </Badge>
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              {formatTimestamp(record.started_at)}
            </p>
          </div>
          <Button
            variant="ghost"
            size="icon"
            className="shrink-0"
            onClick={onClose}
            aria-label={t("requestAnalysis.details.close")}
          >
            <XIcon size={16} aria-hidden="true" />
          </Button>
        </div>

        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto overscroll-contain p-4" tabIndex={0}>
          {sessionID ? (
            <Link
              to={`/usage/sessions/${encodeURIComponent(sessionID)}`}
              className="inline-flex rounded-field border border-accent-primary-border bg-accent-primary-soft px-3 py-2 text-xs font-medium text-accent-primary"
            >
              {t("requestAnalysis.details.openSession")}
            </Link>
          ) : null}

          {groups.map((group) => (
            <section key={group.title} className="min-w-0 rounded-card border border-border p-3">
              <h3 className="text-xs font-semibold">{group.title}</h3>
              <dl className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2">
                {group.fields.map((field) => (
                  <div key={field.label} className={field.wide ? "min-w-0 sm:col-span-2" : "min-w-0"}>
                    <dt className="text-xs text-muted-foreground">{field.label}</dt>
                    <dd className="mt-1 whitespace-pre-wrap font-mono text-xs leading-5 [overflow-wrap:anywhere]">
                      {formatDetailValue(field.value, notRecorded)}
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

function formatDetailValue(value: string | number | undefined, notRecorded: string) {
  if (typeof value === "number") return formatNumber(value);
  return value?.trim() ? value : notRecorded;
}
