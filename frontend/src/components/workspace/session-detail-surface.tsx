// 工作台右侧栏「会话详情」面：展示当前运行时会话的状态与关键信息。
//
// 契约：自包含面（panel-registry 注册 `load`，见 panel-registry.ts），只接收
// `sessionId` / `workspacePath`，自行通过 `useSessionDetail` 读取
// `GET /api/runtime/sessions/{id}` 快照——与侧栏「已关闭会话」图标的口径一致：
// 状态改为用文字徽标表达，不再依赖列表图标。
//
// 只读展示：不在此面提供归档 / 关闭 / 删除入口，避免侧栏行菜单与本面板两套写入口。

import { AlertTriangleIcon, IdCardIcon, RefreshCwIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import {
  formatSessionDetailTimestamp,
  normalizeSessionDetailTags,
  resolveSessionDetailState,
} from "@/components/workspace/session-detail-panel-shared";
import { useSessionDetail } from "@/hooks/workspace/use-session-detail";
import { cn } from "@/lib/utils";
import { type RuntimeSessionRecord } from "@/types/runtime";

export type SessionDetailSurfaceProps = {
  sessionId: string;
  workspacePath?: string;
};

type SessionDetailRow = {
  key: string;
  label: string;
  /** 长文本（会话 id / 目录 / 摘要）用等宽或断行呈现，避免撑破 288px 侧栏。 */
  mono?: boolean;
  value: string;
};

/** 字段行按「有值才渲染」组装：缺失字段保持空白，不写「未知」以免噪声。 */
function buildSessionDetailRows(
  session: RuntimeSessionRecord,
  workspacePath: string | undefined,
  t: (key: string) => string,
): SessionDetailRow[] {
  const rows: SessionDetailRow[] = [];
  const pushRow = (
    key: string,
    labelKey: string,
    value: string | null | undefined,
    options?: { mono?: boolean },
  ) => {
    const text = value?.toString().trim();
    if (!text) {
      return;
    }
    rows.push({ key, label: t(labelKey), mono: options?.mono, value: text });
  };

  const metadata = session.metadata;
  const tags = normalizeSessionDetailTags(metadata?.tags);

  pushRow("id", "panels.sessionDetail.fields.id", session.id, { mono: true });
  pushRow("userId", "panels.sessionDetail.fields.userId", session.userId);
  pushRow("createdBy", "panels.sessionDetail.fields.createdBy", metadata?.createdBy);
  pushRow(
    "workspace",
    "panels.sessionDetail.fields.workspace",
    workspacePath?.trim() || t("panels.sessionDetail.fields.workspaceUnbound"),
    { mono: true },
  );
  pushRow(
    "createdAt",
    "panels.sessionDetail.fields.createdAt",
    formatSessionDetailTimestamp(session.createdAt),
  );
  pushRow(
    "updatedAt",
    "panels.sessionDetail.fields.updatedAt",
    formatSessionDetailTimestamp(session.updatedAt),
  );
  pushRow(
    "expiresAt",
    "panels.sessionDetail.fields.expiresAt",
    formatSessionDetailTimestamp(session.expiresAt),
  );
  pushRow(
    "totalTurns",
    "panels.sessionDetail.fields.totalTurns",
    metadata?.totalTurns === undefined ? null : String(metadata.totalTurns),
  );
  pushRow("lastAgent", "panels.sessionDetail.fields.lastAgent", metadata?.lastAgent);
  pushRow("lastSkill", "panels.sessionDetail.fields.lastSkill", metadata?.lastSkill);
  pushRow("lastModel", "panels.sessionDetail.fields.lastModel", metadata?.lastModel);
  pushRow(
    "titleSource",
    "panels.sessionDetail.fields.titleSource",
    metadata?.titleSource,
  );
  pushRow("tags", "panels.sessionDetail.fields.tags", tags.join(", "));
  pushRow("summary", "panels.sessionDetail.fields.summary", metadata?.summary);

  return rows;
}

export function SessionDetailSurface({
  sessionId,
  workspacePath,
}: SessionDetailSurfaceProps) {
  const { t } = useTranslation("workspace");
  const translate = (key: string) => t(key as never) as string;
  const { error, reload, session, status } = useSessionDetail(sessionId);
  const stateDisplay = resolveSessionDetailState(session?.state);
  const rows = session
    ? buildSessionDetailRows(session, workspacePath, translate)
    : [];
  const title = session?.metadata?.title?.trim() || sessionId.trim();
  const loading = status === "loading";

  return (
    <section
      aria-label={t("panels.sessionDetail.ariaLabel")}
      className="flex min-h-0 flex-col gap-2.5 overflow-y-auto px-3 py-3"
      data-testid="session-detail-panel"
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="flex items-center gap-1.5 text-xs font-semibold text-foreground">
            <IdCardIcon className="text-accent-primary" size={14} />
            {t("panels.sessionDetail.title")}
          </h2>
          <p
            className="mt-1 truncate text-xs text-muted-foreground"
            title={title || undefined}
          >
            {title || t("panels.sessionDetail.untitled")}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {session ? (
            <span
              className={cn(
                "rounded-full border px-1.5 py-0.5 app-text-10 tracking-[0.08em]",
                stateDisplay.className,
              )}
              data-testid="session-detail-state"
            >
              {translate(stateDisplay.labelKey)}
            </span>
          ) : null}
          <Button
            aria-label={t("panels.sessionDetail.refresh")}
            className="h-7 w-7 px-0"
            disabled={loading || !sessionId.trim()}
            onClick={reload}
            size="sm"
            title={t("panels.sessionDetail.refresh")}
            variant="ghost"
          >
            <RefreshCwIcon
              className={cn(loading && "animate-spin")}
              size={14}
            />
          </Button>
        </div>
      </div>

      {error ? (
        <div className="flex items-start gap-2 rounded-card border border-border bg-surface-softer px-2.5 py-2 text-xs text-analytics-danger">
          <AlertTriangleIcon className="mt-0.5 shrink-0" size={14} />
          <div className="min-w-0">
            <div className="font-medium">
              {t("panels.sessionDetail.errorTitle")}
            </div>
            <div className="mt-0.5 break-words text-muted-foreground">
              {error}
            </div>
            <Button
              className="mt-1.5 h-6 px-2 text-xs"
              onClick={reload}
              size="sm"
              type="button"
              variant="ghost"
            >
              {t("panels.sessionDetail.retry")}
            </Button>
          </div>
        </div>
      ) : null}

      {!error && loading && !session ? (
        <div className="flex items-center gap-2 rounded-card border border-border bg-surface-softer px-2.5 py-2 text-xs text-muted-foreground">
          <RefreshCwIcon className="animate-spin" size={13} />
          {t("panels.sessionDetail.loading")}
        </div>
      ) : null}

      {!error && !loading && !session ? (
        <div className="rounded-card border border-border bg-surface-softer px-2.5 py-2 text-xs text-muted-foreground">
          <div className="font-medium text-foreground">
            {t("panels.sessionDetail.empty")}
          </div>
          <p className="mt-1 leading-5">{t("panels.sessionDetail.emptyHint")}</p>
        </div>
      ) : null}

      {session && rows.length > 0 ? (
        <dl className="grid gap-2" data-testid="session-detail-fields">
          {rows.map((row) => (
            <div className="min-w-0" key={row.key}>
              <dt className="app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
                {row.label}
              </dt>
              <dd
                className={cn(
                  "mt-0.5 break-all text-sm text-foreground",
                  row.mono && "font-mono text-xs",
                )}
                data-testid={`session-detail-field-${row.key}`}
                title={row.value}
              >
                {row.value}
              </dd>
            </div>
          ))}
        </dl>
      ) : null}
    </section>
  );
}
