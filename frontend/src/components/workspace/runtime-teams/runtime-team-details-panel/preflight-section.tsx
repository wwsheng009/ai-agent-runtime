import { Badge } from "@/components/ui/badge";
import {
  type RuntimeTeamPreflightFinding,
  type RuntimeTeamPreflightReport,
} from "@/types/runtime";
import type { TeamDetailsState } from "@/components/workspace/runtime-teams/shared";
import { useTranslation } from "react-i18next";

import { detailCardClass, detailStatusPillClass } from "./format";
import { TeamDetailsSection } from "./primitives";

type RuntimeTeamPreflightSectionProps = {
  details: TeamDetailsState;
  onToggle: () => void;
  open: boolean;
};

type RoutePreviewField = {
  label: string;
  value: string;
};

type FindingSubject = {
  id: string;
  kind:
    | "dependsOn"
    | "existingMode"
    | "existingOwner"
    | "path"
    | "relatedPath"
    | "relatedTask"
    | "relatedTeammate"
    | "task"
    | "team"
    | "teammate";
};

function findingSubject(
  finding: RuntimeTeamPreflightFinding,
): FindingSubject[] {
  return [
    finding.team_id ? { kind: "team", id: finding.team_id } : null,
    finding.teammate_id ? { kind: "teammate", id: finding.teammate_id } : null,
    finding.related_teammate_id
      ? { kind: "relatedTeammate", id: finding.related_teammate_id }
      : null,
    finding.task_id ? { kind: "task", id: finding.task_id } : null,
    finding.related_task_id
      ? { kind: "relatedTask", id: finding.related_task_id }
      : null,
    finding.depends_on_id
      ? { kind: "dependsOn", id: finding.depends_on_id }
      : null,
    finding.path ? { kind: "path", id: finding.path } : null,
    finding.related_path
      ? { kind: "relatedPath", id: finding.related_path }
      : null,
    finding.existing_owner
      ? { kind: "existingOwner", id: finding.existing_owner }
      : null,
    finding.existing_mode
      ? { kind: "existingMode", id: finding.existing_mode }
      : null,
  ].filter((subject): subject is FindingSubject => subject !== null);
}

function routeString(
  route: Record<string, unknown> | undefined,
  field: string,
) {
  const value = route?.[field];
  return typeof value === "string" && value.trim() ? value : null;
}

function FindingList({
  findings,
  label,
}: {
  findings: RuntimeTeamPreflightFinding[];
  label: string;
}) {
  const { t } = useTranslation("workspace");
  if (findings.length === 0) {
    return null;
  }

  return (
    <div className="space-y-1.5">
      <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
        {label}
      </div>
      <ol className="space-y-1.5">
        {findings.map((finding, index) => {
          const subjects = findingSubject(finding);
          return (
            <li
              key={`${finding.severity}-${finding.code}-${finding.task_id ?? ""}-${index}`}
              className={detailCardClass}
            >
              <div className="flex flex-wrap items-center gap-2">
                <code className="text-xs font-semibold text-foreground">
                  {finding.code}
                </code>
                <span className={detailStatusPillClass}>
                  {finding.severity}
                </span>
                {subjects.map((subject) => (
                  <span
                    key={`${subject.kind}-${subject.id}`}
                    className="rounded-control border border-border bg-surface-solid px-2 py-0.5 text-[0.68rem] text-muted-foreground"
                  >
                    {t(`panels.teamsPanels.details.preflight.${subject.kind}`, {
                      id: subject.id,
                    })}
                  </span>
                ))}
              </div>
              <p className="mt-1.5 text-sm leading-6 text-foreground">
                {finding.message}
              </p>
              {finding.remediation ? (
                <p className="mt-1 text-xs leading-5 text-muted-foreground">
                  {finding.remediation}
                </p>
              ) : null}
            </li>
          );
        })}
      </ol>
    </div>
  );
}

function RoutePreviewSummary({
  report,
  label,
}: {
  report: RuntimeTeamPreflightReport;
  label: string;
}) {
  const { t } = useTranslation("workspace");
  const routePreviews = report.route_previews ?? [];
  const capabilityLabels: Record<string, string> = {
    not_requested: t(
      "panels.teamsPanels.details.preflight.routeCapability.notRequested",
    ),
    resolved: t(
      "panels.teamsPanels.details.preflight.routeCapability.resolved",
    ),
    disabled: t(
      "panels.teamsPanels.details.preflight.routeCapability.disabled",
    ),
    unavailable: t(
      "panels.teamsPanels.details.preflight.routeCapability.unavailable",
    ),
    failed: t("panels.teamsPanels.details.preflight.routeCapability.failed"),
    partial: t("panels.teamsPanels.details.preflight.routeCapability.partial"),
  };
  if (routePreviews.length === 0) {
    return null;
  }

  return (
    <div className="space-y-1.5">
      <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
        {label}
      </div>
      <div className="space-y-1.5">
        {routePreviews.map((preview, index) => {
          const provider = routeString(preview.route, "provider");
          const model = routeString(preview.route, "model");
          const reasoningEffort = routeString(
            preview.route,
            "reasoning_effort",
          );
          const source = routeString(preview.route, "source");
          const fallbackReason = routeString(preview.route, "fallback_reason");
          const permissionMode = routeString(preview.route, "permission_mode");
          const toolProfile = routeString(preview.route, "tool_profile");
          const routingReasoningEffort = routeString(
            preview.route,
            "route_reasoning_effort",
          );
          const fallbackUsed =
            preview.route?.fallback_used === true ||
            routeString(preview.route, "fallback_used") === "true";
          const warnings = Array.isArray(preview.route?.warnings)
            ? preview.route.warnings.filter(
                (warning): warning is string =>
                  typeof warning === "string" && warning.trim().length > 0,
              )
            : [];
          const fields: RoutePreviewField[] = [
            provider
              ? {
                  label: t("panels.teamsPanels.details.preflight.provider"),
                  value: provider,
                }
              : null,
            model
              ? {
                  label: t("panels.teamsPanels.details.preflight.model"),
                  value: model,
                }
              : null,
            reasoningEffort
              ? {
                  label: t(
                    "panels.teamsPanels.details.preflight.reasoningEffort",
                  ),
                  value: reasoningEffort,
                }
              : null,
            source
              ? {
                  label: t("panels.teamsPanels.details.preflight.routeSource"),
                  value: source,
                }
              : null,
            permissionMode
              ? {
                  label: t(
                    "panels.teamsPanels.details.preflight.permissionMode",
                  ),
                  value: permissionMode,
                }
              : null,
            toolProfile
              ? {
                  label: t("panels.teamsPanels.details.preflight.toolProfile"),
                  value: toolProfile,
                }
              : null,
            routingReasoningEffort
              ? {
                  label: t(
                    "panels.teamsPanels.details.preflight.routeReasoningEffort",
                  ),
                  value: routingReasoningEffort,
                }
              : null,
          ].filter((field) => field !== null);
          const routeError =
            preview.error ?? routeString(preview.route, "error");

          return (
            <div
              key={`${preview.task_id ?? "task"}-${preview.teammate_id ?? "mate"}-${index}`}
              className={detailCardClass}
            >
              <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                {preview.task_id ? (
                  <span>
                    {t("panels.teamsPanels.details.preflight.task", {
                      id: preview.task_id,
                    })}
                  </span>
                ) : null}
                {preview.teammate_id ? (
                  <span>
                    {t("panels.teamsPanels.details.preflight.teammate", {
                      id: preview.teammate_id,
                    })}
                  </span>
                ) : null}
                <span
                  className={`${detailStatusPillClass} ${
                    routeError || preview.disabled
                      ? "border-accent-gold/30 text-accent-gold"
                      : "border-border text-muted-foreground"
                  }`}
                >
                  {capabilityLabels[preview.capability] ?? preview.capability}
                </span>
                {preview.disabled ? (
                  <span className={detailStatusPillClass}>
                    {t("panels.teamsPanels.details.preflight.disabled")}
                  </span>
                ) : null}
                {preview.strict ? (
                  <span className={detailStatusPillClass}>
                    {t("panels.teamsPanels.details.preflight.strict")}
                  </span>
                ) : null}
              </div>
              {fields.length > 0 ? (
                <dl className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs">
                  {fields.map((field) => (
                    <div key={field.label} className="flex gap-1">
                      <dt className="text-muted-foreground">{field.label}:</dt>
                      <dd className="break-all text-foreground">
                        {field.value}
                      </dd>
                    </div>
                  ))}
                </dl>
              ) : null}
              {fallbackUsed ? (
                <p className="mt-1.5 text-xs leading-5 text-muted-foreground">
                  {t("panels.teamsPanels.details.preflight.fallbackUsed")}
                  {fallbackReason ? ` — ${fallbackReason}` : null}
                </p>
              ) : null}
              {routeError ? (
                <p className="mt-1.5 text-xs leading-5 text-muted-foreground">
                  {routeError}
                </p>
              ) : null}
              {warnings.length > 0 ? (
                <ul className="mt-1.5 list-disc space-y-1 pl-4 text-xs leading-5 text-muted-foreground">
                  {warnings.map((warning, warningIndex) => (
                    <li key={`${warning}-${warningIndex}`}>{warning}</li>
                  ))}
                </ul>
              ) : null}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function renderFindings(report: RuntimeTeamPreflightReport) {
  if (report.findings?.length) {
    return report.findings;
  }
  return [
    ...(report.blockers ?? []),
    ...(report.warnings ?? []),
    ...(report.infos ?? []),
  ];
}

export function RuntimeTeamPreflightSection({
  details,
  onToggle,
  open,
}: RuntimeTeamPreflightSectionProps) {
  const { t } = useTranslation("workspace");
  const report = details.preflight?.preflight;
  const findings = report ? renderFindings(report) : [];
  const otherFindings = findings.filter(
    (finding) =>
      finding.severity !== "blocker" &&
      finding.severity !== "warning" &&
      finding.severity !== "info",
  );
  const blockerCount = report?.blocker_count ?? 0;

  return (
    <TeamDetailsSection
      title={t("panels.teamsPanels.details.preflight.title")}
      subtitle={t("panels.teamsPanels.details.preflight.subtitle")}
      badge={<Badge>{blockerCount}</Badge>}
      open={open}
      onToggle={onToggle}
    >
      {!report ? (
        <p className="text-sm text-muted-foreground">
          {t("panels.teamsPanels.details.preflight.notLoaded")}
        </p>
      ) : (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <span
              className={`${detailStatusPillClass} ${
                report.ready
                  ? "border-accent-secondary-border text-accent-secondary"
                  : "border-accent-gold/30 text-accent-gold"
              }`}
              role="status"
            >
              {report.ready
                ? t("panels.teamsPanels.details.preflight.ready")
                : t("panels.teamsPanels.details.preflight.blocked")}
            </span>
            <span className="text-xs text-muted-foreground">
              {t("panels.teamsPanels.details.preflight.counts", {
                blockers: report.blocker_count,
                warnings: report.warning_count,
                infos: report.info_count,
              })}
            </span>
          </div>
          <p className="text-xs leading-5 text-muted-foreground">
            {t(
              "panels.teamsPanels.details.preflight.workerSessionLivenessBoundary",
              {
                capability:
                  details.preflight?.capabilities?.worker_session_liveness ??
                  "unknown",
              },
            )}
          </p>
          {findings.length > 0 ? (
            <div className="space-y-3">
              <FindingList
                findings={findings.filter(
                  (finding) => finding.severity === "blocker",
                )}
                label={t("panels.teamsPanels.details.preflight.blockers")}
              />
              <FindingList
                findings={findings.filter(
                  (finding) => finding.severity === "warning",
                )}
                label={t("panels.teamsPanels.details.preflight.warnings")}
              />
              <FindingList
                findings={findings.filter(
                  (finding) => finding.severity === "info",
                )}
                label={t("panels.teamsPanels.details.preflight.infos")}
              />
              <FindingList
                findings={otherFindings}
                label={t("panels.teamsPanels.details.preflight.otherFindings")}
              />
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">
              {t("panels.teamsPanels.details.preflight.noFindings")}
            </p>
          )}
          <RoutePreviewSummary
            report={report}
            label={t("panels.teamsPanels.details.preflight.routePreviews")}
          />
        </div>
      )}
    </TeamDetailsSection>
  );
}
