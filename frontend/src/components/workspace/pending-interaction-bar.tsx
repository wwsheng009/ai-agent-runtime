import {
  AlertTriangleIcon,
  CheckIcon,
  CircleHelpIcon,
  LoaderCircleIcon,
  ScrollTextIcon,
  XIcon,
} from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type {
  PendingInteraction,
  PendingQuestionInteraction,
} from "@/lib/pending-interaction";
import type { RuntimeSessionPlanModeExitDecision } from "@/lib/runtime-api";
import { cn } from "@/lib/utils";

type PendingInteractionBarProps = {
  interaction: PendingInteraction | null;
  onResolveApproval: (requestId: string, allow: boolean) => void;
  onAnswerQuestion: (questionId: string, answer: string) => void;
  /** 计划评审决策（与 artifact 面板 plan surface 共用同一提交入口）。 */
  onPlanDecision?: (
    decision: Exclude<RuntimeSessionPlanModeExitDecision, "">,
  ) => void;
  onPlanNotesChange?: (value: string) => void;
  planNotesDraft?: string;
  planActionPending?: boolean;
  className?: string;
};

/**
 * P1-7：审批 / 提问 / 计划评审的统一呈现位（composer 上沿的单卡片）。
 *
 * 只消费 `PendingInteraction` 快照并回抛用户决定；生命周期（注册 / resolving /
 * 回填 / 收敛）全部由 `usePendingInteractions` 持有，组件不自行判断 pending。
 */
function QuestionForm({
  interaction,
  disabled,
  onAnswer,
}: {
  interaction: PendingQuestionInteraction;
  disabled: boolean;
  onAnswer: (questionId: string, answer: string) => void;
}) {
  const { t } = useTranslation("workspace");
  // 由父级以 `key={interaction.id}` 挂载：身份切换即重建，草稿不会串到下一条。
  const [answerDraft, setAnswerDraft] = useState("");

  return (
    <form
      className="mt-2 flex flex-wrap items-center gap-2"
      onSubmit={(event) => {
        event.preventDefault();
        if (!disabled) {
          onAnswer(interaction.id, answerDraft);
        }
      }}
    >
      {interaction.suggestions.length > 0 ? (
        <div className="flex w-full flex-wrap gap-1.5">
          {interaction.suggestions.map((suggestion) => (
            <Button
              key={suggestion}
              disabled={disabled}
              size="sm"
              variant="secondary"
              onClick={() => onAnswer(interaction.id, suggestion)}
            >
              {suggestion}
            </Button>
          ))}
        </div>
      ) : null}
      <input
        aria-label={t("panels.interactions.question.placeholder")}
        className="h-8 min-w-0 flex-1 rounded-field border border-border bg-surface-solid px-2.5 text-sm text-foreground outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring"
        disabled={disabled}
        placeholder={t("panels.interactions.question.placeholder")}
        value={answerDraft}
        onChange={(event) => setAnswerDraft(event.target.value)}
      />
      <Button disabled={disabled} size="sm" type="submit" variant="primary">
        {t("panels.interactions.question.submit")}
      </Button>
    </form>
  );
}

export function PendingInteractionBar({
  interaction,
  onResolveApproval,
  onAnswerQuestion,
  onPlanDecision,
  onPlanNotesChange,
  planNotesDraft = "",
  planActionPending = false,
  className,
}: PendingInteractionBarProps) {
  const { t } = useTranslation("workspace");
  const interactionId = interaction?.id ?? "";
  const interactionKind = interaction?.kind ?? "";
  const interactionStatus = interaction?.status ?? "";

  if (!interaction) {
    return null;
  }

  const isResolving = interaction.status === "resolving";
  const isPlanReview = interaction.kind === "plan_review";
  const TitleIcon = isPlanReview
    ? ScrollTextIcon
    : interaction.kind === "question"
      ? CircleHelpIcon
      : AlertTriangleIcon;

  return (
    <section
      aria-live="polite"
      className={cn(
        "mb-2 rounded-panel border border-border bg-surface-soft/95 px-3 py-2.5 shadow-[0_10px_30px_var(--surface-shadow)] backdrop-blur",
        className,
      )}
      data-interaction-id={interactionId}
      data-kind={interactionKind}
      data-status={interactionStatus}
      data-testid="pending-interaction"
    >
      <div className="flex items-start gap-2">
        <TitleIcon
          aria-hidden="true"
          className={cn(
            "mt-0.5 size-4 shrink-0",
            interaction.kind === "approval"
              ? "text-accent-orange"
              : "text-accent-secondary",
          )}
        />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2 text-sm font-semibold text-foreground">
            {interaction.kind === "approval"
              ? t("panels.interactions.approval.title")
              : interaction.kind === "question"
                ? t("panels.interactions.question.title")
                : t("panels.interactions.planReview.title")}
            {interaction.kind === "approval" ? (
              <Badge className="font-mono text-xs">
                {interaction.toolName || t("panels.interactions.approval.unknownTool")}
              </Badge>
            ) : null}
            {interaction.kind === "question" && interaction.required ? (
              <Badge>{t("panels.interactions.question.required")}</Badge>
            ) : null}
            {isResolving ? (
              <span className="inline-flex items-center gap-1 text-xs font-medium text-muted-foreground">
                <LoaderCircleIcon className="size-3 animate-spin" />
                {t("panels.interactions.submitting")}
              </span>
            ) : null}
          </div>

          {interaction.kind === "approval" && interaction.reason ? (
            <p className="mt-1 text-xs leading-5 text-muted-foreground">
              {interaction.reason}
            </p>
          ) : null}
          {interaction.kind === "question" ? (
            <p className="mt-1 text-xs leading-5 text-muted-foreground">
              {interaction.prompt}
            </p>
          ) : null}
          {interaction.kind === "plan_review" ? (
            <p className="mt-1 text-xs leading-5 text-muted-foreground">
              {t("panels.interactions.planReview.hint")}
            </p>
          ) : null}
          {interaction.error ? (
            <p className="mt-1 text-xs leading-5 text-accent-orange" role="alert">
              {interaction.error}
            </p>
          ) : null}
        </div>
      </div>

      {interaction.kind === "approval" ? (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <Button
            disabled={isResolving}
            size="sm"
            variant="primary"
            onClick={() => onResolveApproval(interaction.id, true)}
          >
            <CheckIcon className="size-3.5" />
            {t("panels.interactions.approval.approve")}
          </Button>
          <Button
            disabled={isResolving}
            size="sm"
            variant="destructive"
            onClick={() => onResolveApproval(interaction.id, false)}
          >
            <XIcon className="size-3.5" />
            {t("panels.interactions.approval.deny")}
          </Button>
        </div>
      ) : null}

      {interaction.kind === "question" ? (
        <QuestionForm
          key={interaction.id}
          disabled={isResolving}
          interaction={interaction}
          onAnswer={onAnswerQuestion}
        />
      ) : null}

      {interaction.kind === "plan_review" ? (
        <div className="mt-2 space-y-2">
          <textarea
            aria-label={t("panels.artifacts.plan.reviewNotes")}
            className="min-h-16 w-full resize-y rounded-field border border-border bg-surface-solid px-2.5 py-2 text-sm text-foreground outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring"
            disabled={planActionPending}
            placeholder={t("panels.artifacts.plan.notesPlaceholder")}
            value={planNotesDraft}
            onChange={(event) => onPlanNotesChange?.(event.target.value)}
          />
          <div className="flex flex-wrap items-center gap-2">
            <Button
              disabled={planActionPending}
              size="sm"
              variant="primary"
              onClick={() => onPlanDecision?.("approve")}
            >
              <CheckIcon className="size-3.5" />
              {t("panels.artifacts.plan.approve")}
            </Button>
            <Button
              disabled={planActionPending}
              size="sm"
              variant="secondary"
              onClick={() => onPlanDecision?.("request_changes")}
            >
              {t("panels.artifacts.plan.requestChanges")}
            </Button>
            <Button
              disabled={planActionPending}
              size="sm"
              variant="ghost"
              onClick={() => onPlanDecision?.("quit")}
            >
              {t("panels.artifacts.plan.quit")}
            </Button>
          </div>
        </div>
      ) : null}
    </section>
  );
}
