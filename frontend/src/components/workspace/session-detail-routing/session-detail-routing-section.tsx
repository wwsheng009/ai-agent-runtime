// 「会话详情」面内的「路由」区块（方案 §7.2/§7.3/§7.4，P3）。
//
// 硬约束：
//   * 非全屏、不新增底部状态条（§5.3.1/§7.2）——所有交互都落在侧栏内的这张卡片里；
//   * 字段值直接取后端投影（§6.1 / I-6），前端不重算 provider/model/effort/source，
//     也不把 warnings 翻译成「结论」；
//   * 写入层可写性只认 `panel.writable_layers`：后端没开放的层置灰并标注，
//     不给出「点了必然失败」的入口。
//
// 交互流：改草稿 → 保存（无改动则不发请求）→ config 层先弹行内二次确认 →
// PATCH → 以服务端返回的新投影刷新草稿；重置 = `clear: true` 清该层覆盖。

import { RefreshCwIcon, RouteIcon } from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import {
  SESSION_DETAIL_CARD_CLASS,
  SESSION_DETAIL_CHIP_CLASS,
  SESSION_DETAIL_SECTION_LABEL_CLASS,
  SESSION_DETAIL_SUBCARD_CLASS,
} from "@/components/workspace/session-detail-panel-shared";
import { useSessionRouting } from "@/hooks/workspace/use-session-routing";
import { cn } from "@/lib/utils";
import type {
  SessionRoutingEditableField,
  SessionRoutingResponse,
  SessionRoutingTargetLayer,
} from "@/types/runtime";

import {
  type SessionDetailRoutingPendingWrite,
  SessionDetailRoutingActions,
} from "./session-detail-routing-actions";
import { SessionDetailRoutingLayers } from "./session-detail-routing-layers";
import { SessionDetailRoutingLevels } from "./session-detail-routing-levels";
import { SessionDetailRoutingStatus } from "./session-detail-routing-status";
import {
  type RoutingLevelDraftMap,
  buildRoutingLevelDrafts,
  buildSessionRoutingWrite,
  isRoutingLayerWritable,
  resolveRoutingErrorEcho,
  resolveRoutingTargetPath,
} from "./session-detail-routing-shared";

const LAYER_LABEL_KEY = {
  session: "panels.sessionDetail.routing.layers.session",
  workspace: "panels.sessionDetail.routing.layers.workspace",
  config: "panels.sessionDetail.routing.layers.config",
} as const;

export type SessionDetailRoutingSectionProps = {
  sessionId: string;
};

export function SessionDetailRoutingSection({
  sessionId,
}: SessionDetailRoutingSectionProps) {
  const { t } = useTranslation("workspace");
  const { snapshot, loading, saving, error, reload, write } = useSessionRouting(sessionId);
  const [layer, setLayer] = useState<SessionRoutingTargetLayer>("session");
  const [layerSourceSid, setLayerSourceSid] = useState(sessionId);
  const [drafts, setDrafts] = useState<RoutingLevelDraftMap>({});
  const [enabledDraft, setEnabledDraft] = useState<boolean | null>(null);
  const [saveError, setSaveError] = useState("");
  const [notice, setNotice] = useState("");
  const [pending, setPending] = useState<SessionDetailRoutingPendingWrite | null>(
    null,
  );
  const [draftSource, setDraftSource] = useState<SessionRoutingResponse | null>(
    null,
  );

  // 投影换代（首次加载 / 保存后）→ 草稿回到服务端事实；不保留本地未提交编辑。
  // 用 React 官方「渲染期按 prop 调整 state」写法：以 prev 引用作守卫，收敛且不产生级联副作用。
  if (snapshot !== draftSource) {
    setDraftSource(snapshot);
    setDrafts(buildRoutingLevelDrafts(snapshot?.panel.levels ?? []));
    setEnabledDraft(null);
    setPending(null);
    setSaveError("");
  }

  // 换会话 → 回到默认层：旧会话选中的层（例如 config）在新会话里可能不可写，
  // 留着会让「目标路径 / 置灰状态」看起来属于新会话。
  if (layerSourceSid !== sessionId) {
    setLayerSourceSid(sessionId);
    setLayer("session");
  }

  const levels = snapshot?.panel.levels ?? [];
  const panel = snapshot?.panel ?? null;
  const layerWritable = isRoutingLayerWritable(panel, layer);
  const targetPath = resolveRoutingTargetPath({
    panel,
    layer,
    serverTargetLayer: snapshot?.targetLayer ?? "",
    serverTargetPath: snapshot?.targetPath ?? "",
  });
  const errorEcho = resolveRoutingErrorEcho(
    saveError,
    levels.map((level) => level.level),
  );
  const warnings = useMemo(() => {
    if (!snapshot) {
      return [];
    }
    return Array.from(
      new Set([...snapshot.routing.warnings, ...snapshot.warnings]),
    );
  }, [snapshot]);

  const plan = useMemo(
    () =>
      snapshot
        ? buildSessionRoutingWrite({
            levels: snapshot.panel.levels,
            drafts,
            enabledDraft,
            effectiveEnabled: snapshot.routing.enabled,
            layer,
          })
        : null,
    [drafts, enabledDraft, layer, snapshot],
  );

  const handleDraftChange = useCallback(
    (level: string, field: SessionRoutingEditableField, value: string) => {
      setDrafts((prev) => ({
        ...prev,
        [level]: {
          provider: prev[level]?.provider ?? "",
          model: prev[level]?.model ?? "",
          reasoning_effort: prev[level]?.reasoning_effort ?? "",
          [field]: value,
        },
      }));
    },
    [],
  );

  const runWrite = useCallback(
    async (kind: SessionDetailRoutingPendingWrite["kind"], confirm: boolean) => {
      setPending(null);
      setSaveError("");
      setNotice("");
      // 二次确认期间草稿仍可编辑，确认时必须复核补丁非空：空补丁 + confirm
      // 会被后端按 §4.5「空补丁 = 清除该层覆盖」处理，config 层会清掉全局
      // routing 节（数据破坏路径）。
      if (kind === "save" && (!plan || !plan.hasChanges)) {
        setNotice(t("panels.sessionDetail.routing.notice.unchanged"));
        return;
      }
      const outcome =
        kind === "reset"
          ? await write({
              targetLayer: layer,
              clear: true,
              ...(confirm ? { confirm: true } : {}),
            })
          : await write({
              targetLayer: layer,
              ...(confirm ? { confirm: true } : {}),
              ...(plan?.mainAgent ? { mainAgent: plan.mainAgent } : {}),
              ...(plan && plan.clearFields.length > 0
                ? { clearFields: plan.clearFields }
                : {}),
            });
      if (!outcome.ok) {
        if (outcome.stale) {
          // 会话已切走（或被更新的写入取代）：旧结果不落到新会话界面上。
          return;
        }
        setSaveError(outcome.error || t("panels.sessionDetail.routing.errors.save"));
        return;
      }
      const layerLabel = t(LAYER_LABEL_KEY[layer]);
      const base =
        kind === "reset"
          ? t("panels.sessionDetail.routing.notice.reset", { layer: layerLabel })
          : t("panels.sessionDetail.routing.notice.saved", { layer: layerLabel });
      setNotice(
        outcome.snapshot.actorInvalidated
          ? `${base} ${t("panels.sessionDetail.routing.notice.actorInvalidated")}`
          : base,
      );
    },
    [layer, plan, t, write],
  );

  const handleSave = useCallback(() => {
    if (!snapshot) {
      return;
    }
    if (!plan || !plan.hasChanges) {
      setSaveError("");
      setNotice(t("panels.sessionDetail.routing.notice.unchanged"));
      return;
    }
    if (layer === "config") {
      setPending({ kind: "save" });
      return;
    }
    void runWrite("save", false);
  }, [layer, plan, runWrite, snapshot, t]);

  const handleReset = useCallback(() => {
    if (layer === "config") {
      setPending({ kind: "reset" });
      return;
    }
    void runWrite("reset", false);
  }, [layer, runWrite]);

  // 换层时丢弃未确认的二次确认（避免把 A 层的确认弹窗带到 B 层）。
  const handleLayerChange = useCallback((next: SessionRoutingTargetLayer) => {
    setLayer(next);
    setPending(null);
  }, []);

  if (!sessionId.trim()) {
    return null;
  }

  const routing = snapshot?.routing;
  const enabled = enabledDraft ?? routing?.enabled ?? false;
  const scopeLabel =
    panel?.scope === "sub"
      ? t("panels.sessionDetail.routing.scope.sub")
      : t("panels.sessionDetail.routing.scope.main");

  return (
    <section
      aria-label={t("panels.sessionDetail.routing.title")}
      className={cn(SESSION_DETAIL_CARD_CLASS, "grid gap-2")}
      data-testid="session-detail-routing"
    >
      <header className="flex items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-1.5">
          <RouteIcon className="shrink-0 text-muted-foreground" size={13} />
          <span className={SESSION_DETAIL_SECTION_LABEL_CLASS}>
            {t("panels.sessionDetail.routing.title")}
          </span>
          <span
            className={cn(
              SESSION_DETAIL_CHIP_CLASS,
              "border-border bg-surface-soft text-muted-foreground",
            )}
            data-testid="routing-scope"
          >
            {scopeLabel}
          </span>
        </div>
        <Button
          aria-label={t("panels.sessionDetail.routing.actions.reload")}
          className="h-6 shrink-0 px-1.5"
          data-testid="routing-reload"
          disabled={loading}
          onClick={() => void reload()}
          size="sm"
          type="button"
          variant="ghost"
        >
          <RefreshCwIcon className={loading ? "animate-spin" : undefined} size={12} />
        </Button>
      </header>

      {error && !snapshot ? (
        <div className={cn(SESSION_DETAIL_SUBCARD_CLASS, "grid gap-1")}>
          <div className="app-text-11 font-medium text-analytics-danger">
            {t("panels.sessionDetail.routing.errors.load")}
          </div>
          <p className="app-text-10 leading-4 break-words text-muted-foreground">
            {error}
          </p>
          <Button
            className="h-6 w-fit px-2 app-text-11"
            data-testid="routing-retry"
            onClick={() => void reload()}
            size="sm"
            type="button"
            variant="ghost"
          >
            {t("panels.sessionDetail.routing.actions.reload")}
          </Button>
        </div>
      ) : null}

      {loading && !snapshot ? (
        <p className="app-text-11 text-muted-foreground">
          {t("panels.sessionDetail.routing.loading")}
        </p>
      ) : null}

      {snapshot && routing ? (
        <>
          <SessionDetailRoutingStatus routing={routing} />

          <SessionDetailRoutingLayers
            layer={layer}
            onLayerChange={handleLayerChange}
            panel={panel}
            saving={saving}
            targetPath={targetPath}
          />

          <SessionDetailRoutingLevels
            disabled={!layerWritable || saving}
            drafts={drafts}
            errorEcho={errorEcho}
            errorMessage={saveError}
            layer={layer}
            levels={levels}
            onDraftChange={handleDraftChange}
          />

          {panel?.scope === "main" ? (
            <label className="flex items-start gap-1.5">
              <input
                checked={enabled}
                className="mt-0.5 size-3 shrink-0"
                data-testid="routing-enabled-toggle"
                disabled={!layerWritable || saving}
                onChange={(event) => setEnabledDraft(event.target.checked)}
                type="checkbox"
              />
              <span className="grid min-w-0 gap-0.5">
                <span className="app-text-11 text-foreground">
                  {t("panels.sessionDetail.routing.enableToggle.label")}
                </span>
                <span className="app-text-10 leading-4 text-muted-foreground">
                  {t("panels.sessionDetail.routing.enableToggle.hint")}
                </span>
              </span>
            </label>
          ) : null}

          {warnings.length > 0 ? (
            <div
              className={cn(SESSION_DETAIL_SUBCARD_CLASS, "grid gap-0.5")}
              data-testid="routing-warnings"
            >
              <span className={SESSION_DETAIL_SECTION_LABEL_CLASS}>
                {t("panels.sessionDetail.routing.warnings.title")}
              </span>
              <ul className="grid gap-0.5">
                {warnings.map((warning) => (
                  <li
                    className="app-text-10 leading-4 break-words text-analytics-warning"
                    key={warning}
                  >
                    {warning}
                  </li>
                ))}
              </ul>
            </div>
          ) : null}

          <SessionDetailRoutingActions
            layerWritable={layerWritable}
            notice={notice}
            onCancelConfirm={() => setPending(null)}
            onConfirm={() => void runWrite(pending?.kind ?? "save", true)}
            onReset={handleReset}
            onSave={handleSave}
            pending={pending}
            saveError={saveError}
            saving={saving}
            targetPath={targetPath}
          />
        </>
      ) : null}
    </section>
  );
}
