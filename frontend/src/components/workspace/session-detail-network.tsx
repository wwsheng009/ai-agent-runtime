// 「会话详情 → 网络详情」：SSE live 与渲染闸门的实时观测块。
//
// 它回答一个具体问题：页面不动的时候，是**没有事件**（SSE / 服务端 / 代理链路），
// 还是**事件到了却没渲染**（前端闸门 / 提交 / DOM）。判据来自三处独立事实：
//   1. 传输层插桩（api/runtime/sse.ts → lib/live-diagnostics/store）：字节、帧、
//      keepalive、静默看门狗命中；
//   2. 接线层插桩（use-workspace-live → 同一 store）：闸门开合、被拦增量、
//      未认领回合与快照刷新；
//   3. DOM 侧独立观测（MutationObserver，见 use-live-diagnostics）：消息列到底
//      有没有发生变更——这一条不依赖任何插桩，是「渲染有没有发生」的诚实证据。
// 三者交叉即可把断点定位到具体一段，不必再靠「刷新页面再看」来猜。
//
// 观测本身必须廉价：store 侧合并通知（≤4 次/秒），本组件只按 1s tick 重渲染，
// 不会给正在排查的流式链路增加负载。

import { ActivityIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import {
  formatLiveAge,
  formatLiveBytes,
  formatLiveClock,
  formatLiveFrameLabel,
  resolveFrameGapStats,
  resolveLiveNetworkVerdict,
} from "@/lib/live-diagnostics/derive";
import { LIVE_FRAME_VISIBLE } from "@/lib/live-diagnostics/types";
import { type LiveChannelDiagnostics } from "@/lib/live-diagnostics/types";
import {
  useLiveDiagnostics,
  useMessageListDomObserver,
} from "@/hooks/workspace/use-live-diagnostics";
import { cn } from "@/lib/utils";

/** 面板自身的刷新节拍：观测的是秒级链路，1s 足够，也不与流式渲染抢主线程。 */
const NETWORK_TICK_MS = 1_000;

type Translate = (key: string) => string;

function StatGrid({
  stats,
  testId,
}: {
  stats: Array<{ key: string; label: string; value: string; hint?: string }>;
  testId: string;
}) {
  return (
    <dl className="grid grid-cols-2 gap-x-2 gap-y-1" data-testid={testId}>
      {stats.map((stat) => (
        <div className="min-w-0" key={stat.key}>
          <dt className="app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
            {stat.label}
          </dt>
          <dd
            className="truncate font-mono text-xs text-foreground"
            title={stat.hint ?? stat.value}
          >
            {stat.value}
          </dd>
        </div>
      ))}
    </dl>
  );
}

/** 一条通道的计数行：最近字节 / 事件 / 保活 / 静默超时 / 错误 / 游标。 */
function buildChannelStats(
  channel: LiveChannelDiagnostics,
  translate: Translate,
  now: number,
): Array<{ key: string; label: string; value: string; hint?: string }> {
  const field = (key: string) =>
    translate(`panels.sessionDetail.network.fields.${key}`);
  const never = field("never");
  const age = (at: number | null) =>
    formatLiveAge(now, at) ?? never;
  const stallsValue =
    channel.stalls > 0 && channel.lastStallMs !== null
      ? `${channel.stalls} · ${Math.round(channel.lastStallMs / 1000)}s`
      : String(channel.stalls);
  return [
    { key: "lastFrame", label: field("lastFrame"), value: age(channel.lastFrameAt) },
    { key: "lastEvent", label: field("lastEvent"), value: age(channel.lastEventAt) },
    {
      key: "bytes",
      label: field("bytes"),
      value: formatLiveBytes(channel.bytes),
    },
    { key: "events", label: field("events"), value: String(channel.events) },
    {
      key: "keepalives",
      label: field("keepalives"),
      value: String(channel.keepalives),
    },
    {
      key: "stalls",
      label: field("stalls"),
      value: stallsValue,
      hint:
        channel.lastStallMs === null
          ? undefined
          : `${field("stalls")}: ${channel.lastStallMs}ms`,
    },
    {
      key: "errors",
      label: field("errors"),
      value: String(channel.errors),
      hint: channel.lastError ?? undefined,
    },
    {
      key: "opens",
      label: field("opens"),
      value: `${channel.opens}/${channel.closes}`,
    },
  ];
}

/** 单条通道区块：标题 + 连接状态徽标 + 计数网格。 */
function ChannelBlock({
  channel,
  title,
  translate,
  now,
  testId,
}: {
  channel: LiveChannelDiagnostics;
  title: string;
  translate: Translate;
  now: number;
  testId: string;
}) {
  const stateKey = channel.active ? "active" : "inactive";
  return (
    <div
      className="grid gap-1 rounded-card border border-border bg-surface-soft px-2 py-1.5"
      data-active={channel.active ? "true" : "false"}
      data-testid={testId}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
          {title}
        </span>
        <span
          className={cn(
            "shrink-0 rounded-full border px-1.5 py-0.5 app-text-10 tracking-[0.08em]",
            channel.active
              ? "border-connection-online-border bg-connection-online-soft text-connection-online"
              : "border-border bg-surface-softer text-muted-foreground",
          )}
        >
          {translate(`panels.sessionDetail.network.state.${stateKey}`)}
        </span>
      </div>
      <StatGrid
        stats={buildChannelStats(channel, translate, now)}
        testId={`${testId}-stats`}
      />
    </div>
  );
}

export function SessionDetailNetworkSection({ sessionId }: { sessionId: string }) {
  const { t } = useTranslation("workspace");
  const translate = (key: string) => t(key as never) as string;
  const snapshot = useLiveDiagnostics(sessionId);
  useMessageListDomObserver();
  const dom = snapshot.dom;
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), NETWORK_TICK_MS);
    return () => {
      clearInterval(timer);
    };
  }, []);

  const verdict = resolveLiveNetworkVerdict({
    runtime: snapshot.runtime,
    blockedDeltas: snapshot.counters.blockedDeltas,
    lastBlockedAt: snapshot.counters.lastBlockedAt,
    now,
  });
  const field = (key: string) =>
    translate(`panels.sessionDetail.network.fields.${key}`);
  const gap = resolveFrameGapStats(snapshot.runtime.frames);
  const gapValue =
    gap.p50 === null || gap.max === null
      ? field("none")
      : `${Math.round(gap.p50)}ms / ${Math.round(gap.max)}ms`;
  const gateStats = [
    {
      key: "gate",
      label: translate("panels.sessionDetail.network.gate.title"),
      value: translate(
        snapshot.gate.rendering
          ? "panels.sessionDetail.network.gate.open"
          : "panels.sessionDetail.network.gate.closed",
      ),
    },
    {
      key: "turn",
      label: translate("panels.sessionDetail.network.gate.turn"),
      value:
        snapshot.gate.liveTurnId ??
        translate("panels.sessionDetail.network.gate.none"),
    },
    {
      key: "blocked",
      label: translate("panels.sessionDetail.network.gate.blockedDeltas"),
      value: String(snapshot.counters.blockedDeltas),
    },
    {
      key: "unowned",
      label: translate("panels.sessionDetail.network.gate.unownedTurns"),
      value: String(snapshot.counters.unownedTurns),
      hint: snapshot.counters.lastUnownedTurnId ?? undefined,
    },
    {
      key: "refreshes",
      label: translate("panels.sessionDetail.network.gate.snapshotRefreshes"),
      value: String(snapshot.counters.snapshotRefreshes),
    },
    {
      key: "gap",
      label: field("gap"),
      value: gapValue,
    },
  ];
  const domStats = [
    {
      key: "changes",
      label: translate("panels.sessionDetail.network.dom.changes"),
      value: String(dom.changes),
    },
    {
      key: "lastChange",
      label: translate("panels.sessionDetail.network.dom.lastChange"),
      value: formatLiveAge(now, dom.lastChangeAt) ?? field("never"),
    },
  ];
  const frames = snapshot.runtime.frames.slice(0, LIVE_FRAME_VISIBLE);

  return (
    <div
      className="grid gap-1.5 rounded-card border border-border bg-surface-softer px-2.5 py-2"
      data-testid="session-detail-network"
    >
      <div className="flex items-center justify-between gap-2">
        <span className="flex items-center gap-1 app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
          <ActivityIcon aria-hidden="true" size={11} />
          {translate("panels.sessionDetail.network.title")}
        </span>
        <span
          className={cn(
            "shrink-0 rounded-full border px-1.5 py-0.5 app-text-10 tracking-[0.08em]",
            verdict.className,
          )}
          data-testid="session-detail-network-verdict"
          title={translate(verdict.hintKey)}
        >
          {translate(verdict.labelKey)}
        </span>
      </div>

      <p className="text-xs leading-5 text-muted-foreground">
        {translate(verdict.hintKey)}
      </p>

      <ChannelBlock
        channel={snapshot.runtime}
        now={now}
        testId="session-detail-network-runtime"
        title={translate("panels.sessionDetail.network.channels.runtime")}
        translate={translate}
      />
      <ChannelBlock
        channel={snapshot.chat}
        now={now}
        testId="session-detail-network-chat"
        title={translate("panels.sessionDetail.network.channels.chat")}
        translate={translate}
      />

      <StatGrid stats={gateStats} testId="session-detail-network-gate" />

      <div className="grid gap-0.5">
        <span className="app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
          {translate("panels.sessionDetail.network.dom.title")}
        </span>
        <div className="flex items-center justify-between gap-2">
          <StatGrid stats={domStats} testId="session-detail-network-dom" />
          <span
            className="shrink-0 self-start app-text-10 text-muted-foreground"
            data-testid="session-detail-network-dom-status"
          >
            {translate(
              dom.observing
                ? "panels.sessionDetail.network.dom.observing"
                : "panels.sessionDetail.network.dom.detached",
            )}
          </span>
        </div>
      </div>

      <div className="grid gap-0.5" data-testid="session-detail-network-frames">
        <span className="app-text-10 uppercase tracking-[0.12em] text-muted-foreground">
          {translate("panels.sessionDetail.network.frames.title")}
        </span>
        {frames.length === 0 ? (
          <span className="text-xs text-muted-foreground">
            {translate("panels.sessionDetail.network.frames.empty")}
          </span>
        ) : (
          frames.map((frame, index) => (
            <div
              className="flex items-baseline gap-2 app-text-10 leading-4"
              key={`${frame.at}-${index}`}
            >
              <span className="shrink-0 font-mono text-muted-foreground">
                {formatLiveClock(frame.at)}
              </span>
              <span className="min-w-0 flex-1 truncate font-mono text-foreground">
                {formatLiveFrameLabel(frame)}
              </span>
              {frame.kind === "keepalive" ? (
                <span className="shrink-0 text-muted-foreground">
                  {translate("panels.sessionDetail.network.frames.keepalive")}
                </span>
              ) : null}
            </div>
          ))
        )}
      </div>
    </div>
  );
}
