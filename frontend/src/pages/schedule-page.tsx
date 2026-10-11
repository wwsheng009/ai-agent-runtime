// P3 余项：自动化任务独立页面（/runtime/schedule）。
//
// 与右侧栏 schedule-panel 共用同一数据面（useScheduleTasks + REST 客户端），
// 但作为全局页渲染：无会话上下文，跨会话目录即权威视图。布局纪律与
// /runtime/skills 一致（surface-panel 头部 + 网格主体 + 主题 token）。

import { ArrowLeftIcon, CalendarClockIcon, SettingsIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { buttonVariants } from "@/components/ui/button-variants";
import { SchedulePanel } from "@/components/workspace/schedule-panel";
import { cn } from "@/lib/utils";

export function SchedulePage() {
  const { t } = useTranslation("workspace");

  return (
    <div className="min-h-screen [background:var(--workspace-shell-bg)] text-foreground lg:h-dvh lg:overflow-hidden">
      <div className="mx-auto flex min-h-screen w-full max-w-[1760px] flex-col gap-2 px-2.5 py-2.5 sm:px-3 lg:h-full lg:min-h-0">
        <header className="surface-panel relative overflow-hidden rounded-panel-lg px-3.5 py-3">
          <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_top_left,rgba(240,199,123,0.14),transparent_28%),radial-gradient(circle_at_right,rgba(103,215,230,0.10),transparent_22%)]" />
          <div className="relative flex flex-col gap-2.5 lg:flex-row lg:items-center lg:justify-between">
            <div className="space-y-1.5">
              <div className="flex flex-wrap items-center gap-2">
                <Badge className="border-accent-primary-border bg-accent-primary-soft text-accent-primary">
                  <CalendarClockIcon size={13} />
                  {t("panels.schedule.title")}
                </Badge>
                <Badge>{t("panels.schedule.independentPage")}</Badge>
              </div>
              <div>
                <h1 className="text-base font-semibold tracking-[-0.03em] sm:text-[1.1rem]">
                  {t("panels.schedule.title")}
                </h1>
                <p className="mt-1 max-w-3xl text-sm leading-6 text-muted-foreground">
                  {t("panels.schedule.description")}
                </p>
              </div>
            </div>

            <nav className="flex flex-wrap items-center gap-2" aria-label={t("panels.schedule.title")}>
              <Link
                to="/runtime/config"
                className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
              >
                <SettingsIcon size={14} />
                {t("panels.schedule.backToRuntimeConfig")}
              </Link>
              <Link to="/workspace/chats/new" className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}>
                <ArrowLeftIcon size={14} />
                {t("panels.schedule.backToWorkspace")}
              </Link>
            </nav>
          </div>
        </header>

        <main className="min-h-0 flex-1 overflow-y-auto rounded-panel-lg border border-border/60 bg-surface/30">
          {/* 自包含面板（内部自取数据）；全局页即无会话上下文的目录视图。 */}
          <SchedulePanel lastRuntimeEventType={undefined} runtimeEventCount={undefined} sessionId="" />
        </main>
      </div>
    </div>
  );
}
