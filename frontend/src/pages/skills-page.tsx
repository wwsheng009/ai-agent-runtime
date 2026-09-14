// P2-1A：技能市场 / 热重载独立页面（/runtime/skills）。
//
// 纯前端接线：目录、统计、热重载三条数据流分别来自
//   GET  /api/runtime/skills
//   GET  /api/runtime/skills/stats
//   GET  /api/runtime/skills/hot-reload/stats
//   POST /api/runtime/skills/hot-reload/{start,stop,reload}
// 任一条失败只影响自身面板，不遮蔽其余能力（与 /usage、/runtime/config 的呈现纪律一致）。

import { ArrowLeftIcon, BarChart3Icon, SparklesIcon, TerminalSquareIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { buttonVariants } from "@/components/ui/button-variants";
import { useSkillsMarket } from "@/hooks/use-skills-market";
import { cn } from "@/lib/utils";

import { SkillCatalogPanel } from "./skills/catalog";
import { HotReloadPanel } from "./skills/hot-reload";
import { SkillsStatsPanel } from "./skills/stats";

export function SkillsPage() {
  const { t } = useTranslation("skills");
  const market = useSkillsMarket();

  return (
    <div className="min-h-screen [background:var(--workspace-shell-bg)] text-foreground lg:h-dvh lg:overflow-hidden">
      <div className="mx-auto flex min-h-screen w-full max-w-[1760px] flex-col gap-2 px-2.5 py-2.5 sm:px-3 lg:h-full lg:min-h-0">
        <header className="surface-panel relative overflow-hidden rounded-panel-lg px-3.5 py-3">
          <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_top_left,rgba(240,199,123,0.18),transparent_28%),radial-gradient(circle_at_right,rgba(103,215,230,0.12),transparent_22%)]" />
          <div className="relative flex flex-col gap-2.5 lg:flex-row lg:items-center lg:justify-between">
            <div className="space-y-1.5">
              <div className="flex flex-wrap items-center gap-2">
                <Badge className="border-accent-primary-border bg-accent-primary-soft text-accent-primary">
                  <SparklesIcon size={13} />
                  {t("badge")}
                </Badge>
                <Badge>{t("independentPage")}</Badge>
              </div>
              <div>
                <h1 className="text-base font-semibold tracking-[-0.03em] sm:text-[1.1rem]">
                  {t("title")}
                </h1>
                <p className="mt-1 max-w-3xl text-sm leading-6 text-muted-foreground">
                  {t("description")}
                </p>
              </div>
            </div>

            <nav className="flex flex-wrap items-center gap-2" aria-label={t("title")}>
              <Link
                to="/runtime/config"
                className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
              >
                <ArrowLeftIcon size={14} />
                {t("backToRuntimeConfig")}
              </Link>
              <Link to="/usage" className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}>
                <BarChart3Icon size={14} />
                {t("nav.usage")}
              </Link>
              <Link to="/logs" className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}>
                <TerminalSquareIcon size={14} />
                {t("nav.logs")}
              </Link>
            </nav>
          </div>
        </header>

        <div className="grid min-h-0 gap-2 lg:flex-1 lg:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)] lg:overflow-hidden">
          <div className="min-h-0 lg:overflow-y-auto">
            <SkillCatalogPanel
              catalog={market.catalog}
              catalogCount={market.catalogCount}
              catalogStatus={market.catalogStatus}
              catalogError={market.catalogError}
              searchQuery={market.searchQuery}
              searchCategory={market.searchCategory}
              searchMode={market.searchMode}
              searchStatus={market.searchStatus}
              searchResult={market.searchResult}
              searchError={market.searchError}
              onSearch={market.runSearch}
              onClearSearch={market.clearSearch}
              onRefresh={market.refreshCatalog}
            />
          </div>
          <div className="min-h-0 space-y-2 lg:overflow-y-auto">
            <SkillsStatsPanel
              stats={market.stats}
              status={market.statsStatus}
              error={market.statsError}
              onRefresh={market.refreshStats}
            />
            <HotReloadPanel
              stats={market.hotReload}
              status={market.hotReloadStatus}
              error={market.hotReloadError}
              action={market.hotReloadAction}
              actionError={market.hotReloadActionError}
              policyDisabled={market.stats?.policy?.disableHotReload ?? null}
              suggestedDirs={market.stats?.skillDirs ?? []}
              onRefresh={market.refreshHotReload}
              onStart={market.startHotReloadDirs}
              onStop={market.stopHotReloading}
              onReload={market.reloadHotReloadNow}
            />
          </div>
        </div>
      </div>
    </div>
  );
}
