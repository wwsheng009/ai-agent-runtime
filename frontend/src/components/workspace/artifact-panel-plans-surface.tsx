// 右侧栏「计划归档」面（报告 §4.5 的 `/plans` 浏览器，Web 阅读面）。
//
// 契约：自包含面（见 panel-registry 注释），只从 PanelHost 收上下文 props；数据由
// `useRuntimePlans` 经 `GET /api/runtime/plans` / `GET /api/runtime/plans/{id}` 读取。
//
// 只读阅读面：这里**不做裁决**（批准/请求修改/退出仍由「计划」面的当前会话评审入口负责），
// 只回看归档记录 + 评审轮次 + 最新快照正文，避免出现第二套写入口。
//
// 布局（单列，宽面 416–672px）：
//   * 头部：面标题 + （详情态）返回列表 + 刷新；
//   * 列表态：每条 = 状态徽标 + 标题/计划路径 + `vN` + 更新时间 + 会话/项目；
//   * 详情态：记录元数据 → 评审轮次（决策 + 备注摘要）→ 最新快照正文（MessageMarkdown）；
//   * 三态：加载中 / 失败（含重试）/ 空列表互斥，不让空列表掩盖真实错误。

import {
  ChevronLeftIcon,
  FileTextIcon,
  LoaderCircleIcon,
  RefreshCwIcon,
  ScrollTextIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  formatStoredPlanProject,
  formatStoredPlanTitle,
  storedPlanDecisionLabel,
  storedPlanStatusClass,
  storedPlanStatusLabel,
  summarizeRoundNotes,
} from "@/components/workspace/artifact-panel-plans-shared";
import { MessageMarkdown } from "@/components/workspace/message-markdown";
import { type WorkspacePanelSurfaceProps } from "@/components/workspace/panel-registry";
import { useRuntimePlans } from "@/hooks/workspace/use-runtime-plans";
import { cn, formatRelativeTimestamp } from "@/lib/utils";
import type { RuntimeStoredPlanRound } from "@/types/runtime";

function PlanMetaRow({ label, value }: { label: string; value: string }) {
  if (!value) {
    return null;
  }

  return (
    <div className="flex min-w-0 gap-2">
      <span className="shrink-0 text-muted-foreground">{label}</span>
      <span className="min-w-0 break-words text-foreground" title={value}>
        {value}
      </span>
    </div>
  );
}

function PlanRoundRow({
  round,
  t,
}: {
  round: RuntimeStoredPlanRound;
  t: (key: string) => string;
}) {
  const notes = summarizeRoundNotes(round.notes);

  return (
    <li className="space-y-1 rounded-card border border-white/8 bg-black/10 px-2.5 py-2">
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge>{`v${round.version}`}</Badge>
        <Badge>{t(storedPlanDecisionLabel(round.decision))}</Badge>
        {round.source ? (
          <span className="text-muted-foreground">
            {t("panels.artifacts.plans.detail.roundSource")}: {round.source}
          </span>
        ) : null}
        {round.created_at ? (
          <span className="text-muted-foreground">
            {formatRelativeTimestamp(round.created_at)}
          </span>
        ) : null}
      </div>
      {notes ? (
        <div className="break-words text-foreground" title={round.notes?.trim()}>
          {notes}
        </div>
      ) : null}
    </li>
  );
}

export function ArtifactPanelPlansSurface({
  lastRuntimeEventType,
  runtimeEventCount,
}: WorkspacePanelSurfaceProps) {
  const { t } = useTranslation("workspace");
  const { t: tCommon } = useTranslation("common");
  // 动态键（状态 / 决策）必须走宽松包装：键类型由 zh-CN 字典静态约束，运行期才收敛。
  const translate = (key: string) => t(key as never) as string;
  const {
    detailError,
    detailLoading,
    loadedOnce,
    plans,
    plansError,
    plansLoading,
    refresh,
    select,
    selectedPlan,
    selectedPlanId,
  } = useRuntimePlans({ lastRuntimeEventType, runtimeEventCount });

  const detailOpen = selectedPlanId !== null;
  const busy = plansLoading || detailLoading;

  return (
    <div
      className="grid min-h-0 flex-1 content-start gap-2.5 overflow-auto p-2.5"
      data-testid="plans-surface"
    >
      <section className="flex min-h-0 flex-col overflow-hidden rounded-panel-lg border border-white/8 bg-white/[0.035]">
        <div className="flex items-start justify-between gap-3 border-b border-white/8 px-3 py-2.5">
          <div className="min-w-0 space-y-1">
            <div className="inline-flex items-center gap-2 app-text-10 uppercase tracking-[0.16em] text-muted-foreground">
              <ScrollTextIcon size={14} />
              {t("panels.artifacts.plans.title")}
            </div>
            <div className="truncate text-sm text-foreground">
              {detailOpen
                ? selectedPlan
                  ? formatStoredPlanTitle(selectedPlan)
                  : selectedPlanId
                : t("panels.artifacts.plans.count", { count: plans.length })}
            </div>
          </div>
          <div className="flex flex-wrap items-center justify-end gap-1.5">
            {detailOpen ? (
              <Button
                onClick={() => select(null)}
                size="sm"
                type="button"
                variant="ghost"
              >
                <ChevronLeftIcon size={14} />
                {t("panels.artifacts.plans.back")}
              </Button>
            ) : null}
            <Button
              disabled={busy}
              onClick={() => {
                void refresh();
              }}
              size="sm"
              type="button"
              variant="ghost"
            >
              <RefreshCwIcon size={14} className={busy ? "animate-spin" : undefined} />
              {tCommon("actions.refresh")}
            </Button>
          </div>
        </div>

        <div className="min-h-0 flex-1 overflow-auto px-2.5 py-2.5">
          {detailOpen ? (
            <div className="space-y-3" data-testid="plans-detail">
              {detailLoading ? (
                <div className="inline-flex items-center gap-2 app-text-10 uppercase tracking-[0.16em] text-muted-foreground">
                  <LoaderCircleIcon size={14} className="animate-spin" />
                  {t("panels.artifacts.plans.detail.loading")}
                </div>
              ) : null}

              {detailError ? (
                <div className="space-y-2 rounded-card-lg border border-accent-orange/18 bg-accent-orange/8 px-3 py-2.5 text-sm leading-6 text-muted-foreground">
                  <div>{detailError}</div>
                  <Button
                    onClick={() => {
                      if (selectedPlanId) {
                        select(selectedPlanId);
                      }
                    }}
                    size="sm"
                    type="button"
                    variant="secondary"
                  >
                    {t("panels.artifacts.plans.retry")}
                  </Button>
                </div>
              ) : null}

              {selectedPlan ? (
                <>
                  <div className="space-y-1 rounded-card-lg border border-white/8 bg-black/10 px-3 py-2.5 text-sm leading-6">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <Badge
                        className={cn(storedPlanStatusClass(selectedPlan.status))}
                      >
                        {translate(storedPlanStatusLabel(selectedPlan.status))}
                      </Badge>
                      <Badge>{`v${selectedPlan.version}`}</Badge>
                      {selectedPlan.updated_at ? (
                        <span className="text-muted-foreground">
                          {t("panels.artifacts.plans.updatedAt", {
                            time: formatRelativeTimestamp(selectedPlan.updated_at),
                          })}
                        </span>
                      ) : null}
                    </div>
                    <PlanMetaRow
                      label={t("panels.artifacts.plans.planPath")}
                      value={selectedPlan.plan_path ?? ""}
                    />
                    <PlanMetaRow
                      label={t("panels.artifacts.plans.session")}
                      value={selectedPlan.session_id ?? ""}
                    />
                    <PlanMetaRow
                      label={t("panels.artifacts.plans.project")}
                      value={formatStoredPlanProject(selectedPlan)}
                    />
                  </div>

                  <div className="space-y-2 rounded-card-lg border border-white/8 bg-black/10 px-3 py-2.5 text-sm leading-6">
                    <div className="app-text-10 uppercase tracking-[0.16em] text-muted-foreground">
                      {t("panels.artifacts.plans.detail.rounds")}
                    </div>
                    {selectedPlan.rounds && selectedPlan.rounds.length > 0 ? (
                      <ul className="space-y-1.5">
                        {selectedPlan.rounds.map((round, index) => (
                          <PlanRoundRow
                            key={`${round.version}-${index}`}
                            round={round}
                            t={translate}
                          />
                        ))}
                      </ul>
                    ) : (
                      <div className="rounded-card border border-dashed border-white/10 px-3 py-3 text-center text-muted-foreground">
                        {t("panels.artifacts.plans.detail.roundsEmpty")}
                      </div>
                    )}
                  </div>

                  <div className="overflow-hidden rounded-card-lg border border-white/8 bg-black/15">
                    <div className="flex items-center justify-between gap-2 border-b border-white/8 px-3 py-2 app-text-10 uppercase tracking-[0.16em] text-muted-foreground">
                      <span className="inline-flex items-center gap-1.5">
                        <FileTextIcon size={13} />
                        {t("panels.artifacts.plans.detail.snapshot")}
                      </span>
                      {selectedPlan.content_truncated ? (
                        <Badge>
                          {t("panels.artifacts.plans.detail.snapshotTruncated")}
                        </Badge>
                      ) : null}
                    </div>
                    {selectedPlan.content_available && selectedPlan.content ? (
                      <div className="max-h-[28rem] overflow-auto px-3 py-3">
                        <MessageMarkdown content={selectedPlan.content} />
                      </div>
                    ) : (
                      <div className="px-3 py-3 text-sm leading-6 text-muted-foreground">
                        {selectedPlan.content_error?.trim() ||
                          t("panels.artifacts.plans.detail.snapshotEmpty")}
                      </div>
                    )}
                  </div>
                </>
              ) : null}
            </div>
          ) : plansError ? (
            <div className="space-y-2 rounded-card-lg border border-accent-orange/18 bg-accent-orange/8 px-3 py-2.5 text-sm leading-6 text-muted-foreground">
              <div>{plansError}</div>
              <Button
                onClick={() => {
                  void refresh();
                }}
                size="sm"
                type="button"
                variant="secondary"
              >
                {t("panels.artifacts.plans.retry")}
              </Button>
            </div>
          ) : !loadedOnce || (plansLoading && plans.length === 0) ? (
            <div className="inline-flex items-center gap-2 app-text-10 uppercase tracking-[0.16em] text-muted-foreground">
              <LoaderCircleIcon size={14} className="animate-spin" />
              {t("panels.artifacts.plans.loading")}
            </div>
          ) : plans.length === 0 ? (
            <div className="rounded-card-lg border border-dashed border-white/10 px-3 py-5 text-center text-sm leading-6 text-muted-foreground">
              {t("panels.artifacts.plans.empty")}
            </div>
          ) : (
            <ul className="space-y-1.5" data-testid="plans-list">
              {plans.map((plan) => (
                <li key={plan.id}>
                  <button
                    className="w-full space-y-1 rounded-card-lg border border-white/8 bg-black/10 px-3 py-2.5 text-left transition-colors hover:border-white/15 hover:bg-white/[0.05] focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
                    data-testid={`plans-list-item-${plan.id}`}
                    onClick={() => select(plan.id)}
                    type="button"
                  >
                    <div className="flex flex-wrap items-center gap-1.5">
                      <Badge className={cn(storedPlanStatusClass(plan.status))}>
                        {translate(storedPlanStatusLabel(plan.status))}
                      </Badge>
                      <Badge>{`v${plan.version}`}</Badge>
                      <span className="min-w-0 truncate text-sm text-foreground">
                        {formatStoredPlanTitle(plan)}
                      </span>
                    </div>
                    <div className="truncate text-muted-foreground" title={plan.plan_path}>
                      {plan.plan_path?.trim() || plan.id}
                    </div>
                    <div className="flex flex-wrap gap-x-3 gap-y-0.5 text-muted-foreground">
                      {plan.updated_at ? (
                        <span>
                          {t("panels.artifacts.plans.updatedAt", {
                            time: formatRelativeTimestamp(plan.updated_at),
                          })}
                        </span>
                      ) : null}
                      {plan.session_id?.trim() ? (
                        <span className="truncate" title={plan.session_id}>
                          {t("panels.artifacts.plans.session")}: {plan.session_id}
                        </span>
                      ) : null}
                      {formatStoredPlanProject(plan) ? (
                        <span className="truncate" title={formatStoredPlanProject(plan)}>
                          {t("panels.artifacts.plans.project")}:{" "}
                          {formatStoredPlanProject(plan)}
                        </span>
                      ) : null}
                    </div>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </section>
    </div>
  );
}
