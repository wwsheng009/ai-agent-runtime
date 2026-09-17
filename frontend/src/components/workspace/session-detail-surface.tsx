// 工作台右侧栏「会话详情」面：展示当前运行时会话的状态与关键信息。
//
// 契约：自包含面（panel-registry 注册 `load`，见 panel-registry.ts），只接收
// `sessionId` / `workspacePath`，自行通过 `useSessionDetail` 读取
// `GET /api/runtime/sessions/{id}` 快照——与侧栏「已关闭会话」图标的口径一致：
// 状态改为用文字徽标表达，不再依赖列表图标。
//
// 只读展示：不在此面提供归档 / 关闭 / 删除入口，避免侧栏行菜单与本面板两套写入口。
//
// 布局（自上而下，全部落在 288px 侧栏内，长文本一律断行不撑宽）：
//   * 头部：面标题 + 会话标题 + 状态徽标 + 刷新；sticky 常驻，滚动时仍可刷新与辨认；
//   * 关联状态卡：本地线程 ↔ 运行时会话（关联四态 + 传输三态）；
//   * 状态卡：加载中 / 空态 / 读取失败（互斥，且只在没有字段可看时出现）；
//   * 字段卡：会话元数据按「基本信息 / 时间 / 运行 / 内容」分段的标签-值两列；
//   * 网络详情卡：SSE 观测块（默认收起，摘要行常驻判读徽标，见 session-detail-network）。
// 顺序刻意把「这是个什么会话」放在「链路为什么不动」之前：先看事实，再看诊断。

import {
  AlertTriangleIcon,
  FlaskConicalIcon,
  HistoryIcon,
  IdCardIcon,
  LinkIcon,
  RadioTowerIcon,
  RefreshCwIcon,
  UnlinkIcon,
  WifiOffIcon,
  type LucideIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import {
  SESSION_DETAIL_CARD_CLASS,
  SESSION_DETAIL_CHIP_CLASS,
  SESSION_DETAIL_SECTION_LABEL_CLASS,
  formatSessionDetailTimestamp,
  normalizeSessionDetailTags,
  resolveSessionDetailRelation,
  resolveSessionDetailState,
  resolveSessionDetailTransport,
} from "@/components/workspace/session-detail-panel-shared";
import { type WorkspacePanelThreadRelation } from "@/components/workspace/panel-registry";
import { SessionDetailNetworkSection } from "@/components/workspace/session-detail-network";
import {
  type WorkspaceThreadRelationKind,
  type WorkspaceThreadTransportKind,
} from "@/components/workspace/workspace-shell-shared";
import { useSessionDetail } from "@/hooks/workspace/use-session-detail";
import { cn } from "@/lib/utils";
import { type RuntimeSessionRecord } from "@/types/runtime";

export type SessionDetailSurfaceProps = {
  sessionId: string;
  workspacePath?: string;
  /** 当前线程 ↔ 运行时会话的关联快照；缺省时不渲染关联状态区块（独立挂载/测试）。 */
  threadRelation?: WorkspacePanelThreadRelation;
};

/** 关联四态图标：与侧栏行状态同源（金=已附着 / 青=已恢复 / 橙=同步异常 / 灰=未附着）。 */
const RELATION_ICONS: Record<WorkspaceThreadRelationKind, LucideIcon> = {
  attached: LinkIcon,
  restored: HistoryIcon,
  error: AlertTriangleIcon,
  pending: UnlinkIcon,
};

/** 传输三态图标：与顶栏传输状态图标保持同一套图形。 */
const TRANSPORT_ICONS: Record<WorkspaceThreadTransportKind, LucideIcon> = {
  live: RadioTowerIcon,
  error: WifiOffIcon,
  seeded: FlaskConicalIcon,
};

/** 字段分段：会话元数据按语义分成四段，界面顺序即本数组顺序。 */
const FIELD_GROUP_ORDER = ["basic", "timing", "runtime", "content"] as const;

type SessionDetailFieldGroup = (typeof FIELD_GROUP_ORDER)[number];

type SessionDetailRow = {
  key: string;
  group: SessionDetailFieldGroup;
  label: string;
  /** 长文本（会话 id / 目录）用等宽呈现，避免撑破 288px 侧栏。 */
  mono?: boolean;
  /** 摘要这类整段文字整行铺开（标签在上、正文在下），不挤进定宽值列。 */
  stacked?: boolean;
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
    group: SessionDetailFieldGroup,
    labelKey: string,
    value: string | null | undefined,
    options?: { mono?: boolean; stacked?: boolean },
  ) => {
    const text = value?.toString().trim();
    if (!text) {
      return;
    }
    rows.push({
      key,
      group,
      label: t(labelKey),
      mono: options?.mono,
      stacked: options?.stacked,
      value: text,
    });
  };

  const metadata = session.metadata;
  const tags = normalizeSessionDetailTags(metadata?.tags);

  pushRow("id", "basic", "panels.sessionDetail.fields.id", session.id, {
    mono: true,
  });
  pushRow("userId", "basic", "panels.sessionDetail.fields.userId", session.userId);
  pushRow(
    "createdBy",
    "basic",
    "panels.sessionDetail.fields.createdBy",
    metadata?.createdBy,
  );
  pushRow(
    "workspace",
    "basic",
    "panels.sessionDetail.fields.workspace",
    workspacePath?.trim() || t("panels.sessionDetail.fields.workspaceUnbound"),
    { mono: true },
  );
  pushRow(
    "createdAt",
    "timing",
    "panels.sessionDetail.fields.createdAt",
    formatSessionDetailTimestamp(session.createdAt),
  );
  pushRow(
    "updatedAt",
    "timing",
    "panels.sessionDetail.fields.updatedAt",
    formatSessionDetailTimestamp(session.updatedAt),
  );
  pushRow(
    "expiresAt",
    "timing",
    "panels.sessionDetail.fields.expiresAt",
    formatSessionDetailTimestamp(session.expiresAt),
  );
  pushRow(
    "totalTurns",
    "runtime",
    "panels.sessionDetail.fields.totalTurns",
    metadata?.totalTurns === undefined ? null : String(metadata.totalTurns),
  );
  pushRow("lastAgent", "runtime", "panels.sessionDetail.fields.lastAgent", metadata?.lastAgent);
  pushRow("lastSkill", "runtime", "panels.sessionDetail.fields.lastSkill", metadata?.lastSkill);
  pushRow("lastModel", "runtime", "panels.sessionDetail.fields.lastModel", metadata?.lastModel);
  pushRow(
    "titleSource",
    "runtime",
    "panels.sessionDetail.fields.titleSource",
    metadata?.titleSource,
  );
  pushRow("tags", "content", "panels.sessionDetail.fields.tags", tags.join(", "));
  pushRow("summary", "content", "panels.sessionDetail.fields.summary", metadata?.summary, {
    stacked: true,
  });

  return rows;
}

/**
 * 关联状态区块：本地线程 ↔ 运行时会话（关联四态 + 传输三态）。
 * 判据由 shell 侧传入（与侧栏行状态、顶栏状态图标同一口径），本组件只负责展示，
 * 状态名与解释同时作为 tooltip（`title`），便于在 288px 侧栏内不撑宽。
 */
function SessionDetailRelationBlock({
  relation,
}: {
  relation: WorkspacePanelThreadRelation;
}) {
  const { t } = useTranslation("workspace");
  const relationDisplay = resolveSessionDetailRelation(relation.kind);
  const transportDisplay = resolveSessionDetailTransport(relation.transport);
  const RelationIcon = RELATION_ICONS[relation.kind];
  const TransportIcon = TRANSPORT_ICONS[relation.transport];
  const relationLabel = t(relationDisplay.labelKey as never) as string;
  const relationDetail = t(relationDisplay.detailKey as never) as string;
  const relationTooltip = `${relationLabel} · ${relationDetail}`;
  const transportLabel = t(transportDisplay.labelKey as never) as string;
  const transportHint = t(transportDisplay.hintKey as never) as string;
  const transportTooltip = `${transportLabel} · ${transportHint}`;

  return (
    <div
      className={cn(SESSION_DETAIL_CARD_CLASS, "grid gap-2")}
      data-testid="session-detail-relation"
    >
      <div className="flex items-center justify-between gap-2">
        <span className={SESSION_DETAIL_SECTION_LABEL_CLASS}>
          {t("panels.sessionDetail.relation.label")}
        </span>
        <span
          className={cn(SESSION_DETAIL_CHIP_CLASS, relationDisplay.className)}
          data-relation-kind={relation.kind}
          data-testid="session-detail-relation-state"
          title={relationTooltip}
        >
          <RelationIcon aria-hidden="true" size={11} />
          {relationLabel}
        </span>
      </div>
      <p className="text-xs leading-5 text-muted-foreground">{relationDetail}</p>
      <div className="flex items-center justify-between gap-2 border-t border-border/60 pt-2">
        <span className={SESSION_DETAIL_SECTION_LABEL_CLASS}>
          {t("panels.sessionDetail.relation.transportLabel")}
        </span>
        <span
          className={cn(
            SESSION_DETAIL_CHIP_CLASS,
            "border-border bg-surface-soft",
            transportDisplay.toneClassName,
          )}
          data-testid="session-detail-relation-transport"
          data-transport-kind={relation.transport}
          title={transportTooltip}
        >
          <TransportIcon aria-hidden="true" size={11} />
          {transportLabel}
        </span>
      </div>
    </div>
  );
}

/**
 * 值列排版：摘要整段铺开；会话 id / 目录等长文本等宽呈现；其余按常规值排版。
 *
 * 断行口径用 `break-words`（必要时才断）而不是 `break-all`（逐字符断）：值列只有
 * ~153px，模型名 / 会话 id 都是带连字符的长 token，逐字符断会把它从中间劈开，
 * 反而比多占一行更难扫读。
 */
function sessionDetailValueClass(row: SessionDetailRow): string {
  if (row.stacked) {
    return "app-text-12 leading-5 break-words";
  }
  return row.mono ? "font-mono app-text-11 break-words" : "app-text-12 break-words";
}

/**
 * 字段卡：会话元数据。分组小标题 + 标签定宽的两列行，组内用细分隔线切行——
 * 十几行元数据平铺在 288px 侧栏里既难扫读也难定位，按语义分段后可就近查找。
 */
function SessionDetailFieldsCard({
  rows,
  translate,
}: {
  rows: SessionDetailRow[];
  translate: (key: string) => string;
}) {
  return (
    <div
      className={cn(SESSION_DETAIL_CARD_CLASS, "grid gap-2")}
      data-testid="session-detail-fields"
    >
      {FIELD_GROUP_ORDER.map((group) => {
        const groupRows = rows.filter((row) => row.group === group);
        if (groupRows.length === 0) {
          return null;
        }

        return (
          <section className="grid gap-1" key={group}>
            <h3 className={SESSION_DETAIL_SECTION_LABEL_CLASS}>
              {translate(`panels.sessionDetail.sections.${group}`)}
            </h3>
            <dl className="grid">
              {groupRows.map((row, index) => (
                <div
                  className={cn(
                    "grid gap-x-2 py-1",
                    index > 0 && "border-t border-border/60",
                    row.stacked
                      ? "grid-cols-1 gap-y-1"
                      : "grid-cols-[80px_minmax(0,1fr)] items-baseline",
                  )}
                  key={row.key}
                >
                  <dt
                    className={cn(
                      SESSION_DETAIL_SECTION_LABEL_CLASS,
                      "break-words leading-4",
                    )}
                  >
                    {row.label}
                  </dt>
                  <dd
                    className={cn(
                      "min-w-0 text-foreground",
                      sessionDetailValueClass(row),
                    )}
                    data-testid={`session-detail-field-${row.key}`}
                    title={row.value}
                  >
                    {row.value}
                  </dd>
                </div>
              ))}
            </dl>
          </section>
        );
      })}
    </div>
  );
}

export function SessionDetailSurface({
  sessionId,
  threadRelation,
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
      className="flex min-h-0 flex-col gap-2.5 overflow-y-auto px-3 pb-4"
      data-testid="session-detail-panel"
    >
      {/* 头部常驻：合上网络块后内容仍可能超一屏，标题 / 状态 / 刷新不能滚走。
          sticky 配方沿用消息列与变更列表的既有做法（半透明底 + 毛玻璃 + 底部细线），
          负外边距让吸附时横贯整栏，不留 12px 的透明缝。 */}
      <header className="sticky top-0 z-10 -mx-3 flex items-start justify-between gap-2 border-b border-border/60 bg-surface-softer/90 px-3 pt-3 pb-2.5 backdrop-blur">
        <div className="flex min-w-0 items-start gap-2">
          <span
            aria-hidden="true"
            className="mt-0.5 grid h-6 w-6 shrink-0 place-items-center rounded-chip border border-border bg-surface-soft text-accent-primary"
          >
            <IdCardIcon size={13} />
          </span>
          <div className="min-w-0">
            <h2 className="truncate text-xs font-semibold text-foreground">
              {t("panels.sessionDetail.title")}
            </h2>
            <p
              className="mt-0.5 truncate app-text-11 text-muted-foreground"
              title={title || undefined}
            >
              {title || t("panels.sessionDetail.untitled")}
            </p>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {session ? (
            <span
              className={cn(SESSION_DETAIL_CHIP_CLASS, stateDisplay.className)}
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
      </header>

      {threadRelation ? (
        <SessionDetailRelationBlock relation={threadRelation} />
      ) : null}

      {error ? (
        <div
          className={cn(
            SESSION_DETAIL_CARD_CLASS,
            "grid grid-cols-[auto_minmax(0,1fr)] items-start gap-2",
          )}
        >
          <AlertTriangleIcon
            className="mt-0.5 text-analytics-danger"
            size={14}
          />
          <div className="min-w-0">
            <div className="app-text-12 font-medium text-analytics-danger">
              {t("panels.sessionDetail.errorTitle")}
            </div>
            <p className="mt-1 app-text-11 leading-5 break-words text-muted-foreground">
              {error}
            </p>
            <Button
              className="mt-1.5 h-6 px-2 app-text-11"
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
        <div
          className={cn(
            SESSION_DETAIL_CARD_CLASS,
            "flex items-center gap-2 app-text-11 text-muted-foreground",
          )}
        >
          <RefreshCwIcon className="animate-spin" size={13} />
          {t("panels.sessionDetail.loading")}
        </div>
      ) : null}

      {!error && !loading && !session ? (
        <div className={cn(SESSION_DETAIL_CARD_CLASS, "grid gap-1")}>
          <div className="app-text-12 font-medium text-foreground">
            {t("panels.sessionDetail.empty")}
          </div>
          <p className="app-text-11 leading-5 text-muted-foreground">
            {t("panels.sessionDetail.emptyHint")}
          </p>
        </div>
      ) : null}

      {rows.length > 0 ? (
        <SessionDetailFieldsCard rows={rows} translate={translate} />
      ) : null}

      {/* 观测块常驻（不依赖会话快照加载状态）：它要回答的正是「快照/渲染没动静」时的归因问题。 */}
      <SessionDetailNetworkSection sessionId={sessionId} />
    </section>
  );
}
