// P2-1A：技能热重载面板（/api/runtime/skills/hot-reload/*）。
//
// 写操作是后端策略约束端点，UI 必须如实分类：
//   * 403 → 鉴定/策略拒绝（提示补管理令牌 / 查看 mutation policy），绝不显示「已启动」；
//   * 503 → 服务端未配置热重载（getHotReloadStats 的既有语义）；
//   * 其他 → 原始错误，可重试。
// 统计只展示后端字段；`watching` 为准，不以本地状态臆测。

import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { Button } from "@/components/ui/button";
import type { RuntimeHotReloadStats } from "@/types/runtime";

import type { HotReloadAction, SkillsStreamStatus } from "@/hooks/use-skills-market";

import { classifySkillsError, hotReloadExtraEntries, normalizeSkillDirs } from "./shared";

type HotReloadPanelProps = {
  stats: RuntimeHotReloadStats | null;
  status: SkillsStreamStatus;
  error: unknown;
  action: HotReloadAction | null;
  actionError: unknown;
  /** 来自 /skills/stats 的 mutation policy；true 表示策略明确禁用热重载。 */
  policyDisabled: boolean | null;
  suggestedDirs: string[];
  onRefresh: () => void;
  onStart: (dirs: string[], debounceMs?: number) => Promise<void>;
  onStop: () => Promise<void>;
  onReload: () => Promise<void>;
};

function StatValue({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-panel border border-border bg-surface-softer px-3 py-2">
      <div className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">{label}</div>
      <div className="mt-0.5 break-all text-sm">{value}</div>
    </div>
  );
}

export function HotReloadPanel(props: HotReloadPanelProps) {
  const {
    stats,
    status,
    error,
    action,
    actionError,
    policyDisabled,
    suggestedDirs,
    onRefresh,
    onStart,
    onStop,
    onReload,
  } = props;
  const { t } = useTranslation("skills");

  const [dirsDraft, setDirsDraft] = useState(() => suggestedDirs.join("\n"));
  const [debounceDraft, setDebounceDraft] = useState("");
  const dirs = normalizeSkillDirs(dirsDraft);
  const busy = action !== null;
  const blocked = policyDisabled === true;

  const submitStart = () => {
    if (dirs.length === 0 || busy || blocked) {
      return;
    }
    const debounce = Number.parseInt(debounceDraft, 10);
    void onStart(dirs, Number.isFinite(debounce) && debounce > 0 ? debounce : undefined);
  };

  return (
    <section
      className="surface-panel min-w-0 rounded-panel border border-border px-3 py-3 sm:px-4"
      aria-label={t("hotReload.title")}
      data-testid="skills-hot-reload"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-sm font-semibold">{t("hotReload.title")}</h2>
        <button
          type="button"
          onClick={onRefresh}
          className="text-xs text-muted-foreground underline-offset-2 hover:underline"
        >
          {t("actions.refresh")}
        </button>
      </div>

      {blocked ? (
        <p
          className="mt-2 rounded-panel border border-analytics-warning-border bg-analytics-warning-soft px-3 py-2 text-xs text-analytics-warning"
          data-testid="skills-hot-reload-policy"
        >
          {t("hotReload.policyDisabled")}
        </p>
      ) : null}

      {status === "loading" ? (
        <p className="mt-2 text-xs text-muted-foreground" data-testid="skills-hot-reload-loading">
          {t("hotReload.loading")}
        </p>
      ) : null}

      {status === "error" ? (
        <div
          className="mt-2 rounded-panel border border-analytics-warning-border bg-analytics-warning-soft px-3 py-2 text-xs text-analytics-warning"
          data-testid="skills-hot-reload-error"
        >
          {t(`errors.${classifySkillsError(error)}`)}
        </div>
      ) : null}

      {status === "ready" && stats ? (
        <div className="mt-3 grid gap-2 sm:grid-cols-3">
          <StatValue
            label={t("hotReload.enabled")}
            value={
              stats.enabled === null
                ? t("stats.unknown")
                : stats.enabled
                  ? t("hotReload.yes")
                  : t("hotReload.no")
            }
          />
          <StatValue
            label={t("hotReload.watching")}
            value={
              stats.watching === null
                ? t("stats.unknown")
                : stats.watching
                  ? t("hotReload.yes")
                  : t("hotReload.no")
            }
          />
          <StatValue
            label={t("hotReload.skillCount")}
            value={stats.skillCount === null ? t("stats.unknown") : String(stats.skillCount)}
          />
          <StatValue
            label={t("hotReload.callbackCount")}
            value={stats.callbackCount === null ? t("stats.unknown") : String(stats.callbackCount)}
          />
          <StatValue
            label={t("hotReload.debounce")}
            value={stats.debounceTime || t("stats.unknown")}
          />
          <StatValue
            label={t("hotReload.dirs")}
            value={stats.skillDirs.length > 0 ? stats.skillDirs.join(", ") : t("stats.absent")}
          />
          {hotReloadExtraEntries(stats).map((entry) => (
            <StatValue key={entry.key} label={entry.key} value={entry.value} />
          ))}
        </div>
      ) : null}

      <div className="mt-3 space-y-2">
        <label className="block">
          <span className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
            {t("hotReload.startDirs")}
          </span>
          <textarea
            value={dirsDraft}
            onChange={(event) => setDirsDraft(event.target.value)}
            placeholder={t("hotReload.startDirsPlaceholder")}
            rows={2}
            data-testid="skills-hot-reload-dirs"
            className="mt-1 w-full rounded-panel border border-border bg-surface-softer px-2.5 py-1.5 text-sm outline-none placeholder:text-muted-foreground focus:border-accent-primary-border"
          />
        </label>
        <div className="flex flex-wrap items-end gap-2">
          <label className="block w-32">
            <span className="app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
              {t("hotReload.debounceMs")}
            </span>
            <input
              value={debounceDraft}
              onChange={(event) => setDebounceDraft(event.target.value)}
              inputMode="numeric"
              placeholder={t("hotReload.debouncePlaceholder")}
              data-testid="skills-hot-reload-debounce"
              className="mt-1 w-full rounded-panel border border-border bg-surface-softer px-2.5 py-1.5 text-sm outline-none placeholder:text-muted-foreground focus:border-accent-primary-border"
            />
          </label>
          <Button
            size="sm"
            onClick={submitStart}
            disabled={dirs.length === 0 || busy || blocked}
            data-testid="skills-hot-reload-start"
          >
            {action === "start" ? t("hotReload.starting") : t("hotReload.start")}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void onStop()}
            disabled={busy || blocked}
            data-testid="skills-hot-reload-stop"
          >
            {action === "stop" ? t("hotReload.stopping") : t("hotReload.stop")}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void onReload()}
            disabled={busy || blocked}
            data-testid="skills-hot-reload-reload"
          >
            {action === "reload" ? t("hotReload.reloading") : t("hotReload.reload")}
          </Button>
        </div>
      </div>

      {actionError ? (
        <div
          className="mt-2 rounded-panel border border-analytics-warning-border bg-analytics-warning-soft px-3 py-2 text-xs text-analytics-warning"
          data-testid="skills-hot-reload-action-error"
        >
          <p>{t(`errors.${classifySkillsError(actionError)}`)}</p>
          {classifySkillsError(actionError) === "forbidden" ? (
            <p className="mt-1">
              {t("hotReload.forbiddenHint")}{" "}
              <Link to="/logs" className="underline underline-offset-2">
                {t("hotReload.setTokenLink")}
              </Link>
            </p>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}
