// 「网络详情」的纯派生口径：诊断结论、时间/体积格式化、帧间隔抖动。
// 与组件分离的原因同 session-detail-panel-shared：可单测、无 IO、无 React。

import { type LiveChannelDiagnostics, type LiveFrameSample } from "./types";

/** 帧新鲜窗口：窗口内收到过任何字节即视为「通道在推数据」。 */
export const LIVE_FRAME_FRESH_MS = 5_000;
/** 超过该时长无任何字节：按断流呈现（低于运行时 45s 看门狗，让用户提前看到）。 */
export const LIVE_FRAME_STALL_MS = 20_000;
/** 闸门拦截的「近期」窗口：超出后不再作为当前诊断结论。 */
export const LIVE_BLOCKED_RECENT_MS = 60_000;

export type LiveNetworkVerdictCode =
  | "healthy"
  | "render-blocked"
  | "no-events"
  | "channel-error"
  | "idle";

export type LiveNetworkVerdict = {
  code: LiveNetworkVerdictCode;
  /** 徽标配色（与「会话详情」其它徽标同口径：中性底 + 只染文字/描边）。 */
  className: string;
  /** i18n key：`panels.sessionDetail.network.verdict.states.*`。 */
  labelKey: string;
  /** i18n key：`panels.sessionDetail.network.verdict.hints.*`。 */
  hintKey: string;
};

const VERDICT_DISPLAY: Record<LiveNetworkVerdictCode, Omit<LiveNetworkVerdict, "code">> = {
  healthy: {
    className: "border-connection-online-border bg-connection-online-soft text-connection-online",
    labelKey: "panels.sessionDetail.network.verdict.states.healthy",
    hintKey: "panels.sessionDetail.network.verdict.hints.healthy",
  },
  "render-blocked": {
    className: "border-accent-orange/24 bg-accent-orange/10 text-accent-orange",
    labelKey: "panels.sessionDetail.network.verdict.states.renderBlocked",
    hintKey: "panels.sessionDetail.network.verdict.hints.renderBlocked",
  },
  "no-events": {
    className: "border-connection-offline-border bg-connection-offline-soft text-connection-offline",
    labelKey: "panels.sessionDetail.network.verdict.states.noEvents",
    hintKey: "panels.sessionDetail.network.verdict.hints.noEvents",
  },
  "channel-error": {
    className: "border-connection-offline-border bg-connection-offline-soft text-connection-offline",
    labelKey: "panels.sessionDetail.network.verdict.states.channelError",
    hintKey: "panels.sessionDetail.network.verdict.hints.channelError",
  },
  idle: {
    className: "border-border bg-surface-soft text-muted-foreground",
    labelKey: "panels.sessionDetail.network.verdict.states.idle",
    hintKey: "panels.sessionDetail.network.verdict.hints.idle",
  },
};

export function resolveLiveNetworkVerdict(input: {
  runtime: LiveChannelDiagnostics;
  blockedDeltas: number;
  lastBlockedAt: number | null;
  now: number;
}): LiveNetworkVerdict {
  const { runtime, blockedDeltas, lastBlockedAt, now } = input;
  const code = pickVerdictCode(runtime, blockedDeltas, lastBlockedAt, now);
  return { code, ...VERDICT_DISPLAY[code] };
}

function pickVerdictCode(
  runtime: LiveChannelDiagnostics,
  blockedDeltas: number,
  lastBlockedAt: number | null,
  now: number,
): LiveNetworkVerdictCode {
  // 1) 连接层自身报错：error 帧或连续失败降级（最近 30s 内）——先修连接再看别的。
  if (
    runtime.errors > 0 &&
    runtime.lastErrorAt !== null &&
    now - runtime.lastErrorAt < 30_000
  ) {
    return "channel-error";
  }
  // 2) 事件在到达、闸门却关着：这就是「刷新才可见」那一类故障的现场证据。
  if (
    blockedDeltas > 0 &&
    lastBlockedAt !== null &&
    now - lastBlockedAt < LIVE_BLOCKED_RECENT_MS
  ) {
    return "render-blocked";
  }
  const lastFrameAt = runtime.lastFrameAt;
  // 3) 通道建立过但一帧未收：SSE 无事件（服务端/代理/网络层）。
  if (lastFrameAt === null) {
    return runtime.opens > 0 ? "no-events" : "idle";
  }
  const age = now - lastFrameAt;
  if (age > LIVE_FRAME_STALL_MS) {
    return "no-events";
  }
  if (age <= LIVE_FRAME_FRESH_MS) {
    return "healthy";
  }
  return "idle";
}

/** 距今时长：`0.4s` / `12s` / `3m05s`；无值时返回 null（由调用方决定占位文案）。 */
export function formatLiveAge(now: number, at: number | null): string | null {
  if (at === null) {
    return null;
  }
  const delta = Math.max(0, now - at);
  if (delta < 10_000) {
    return `${(delta / 1000).toFixed(1)}s`;
  }
  const seconds = Math.round(delta / 1000);
  if (seconds < 60) {
    return `${seconds}s`;
  }
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  return `${minutes}m${String(rest).padStart(2, "0")}s`;
}

/** 体积：`0 B` / `842 B` / `128.4 KB` / `2.31 MB`（K=1024，保留可辨精度）。 */
export function formatLiveBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) {
    return "0 B";
  }
  if (bytes < 1024) {
    return `${Math.round(bytes)} B`;
  }
  const kb = bytes / 1024;
  if (kb < 1024) {
    return `${kb < 100 ? kb.toFixed(1) : Math.round(kb)} KB`;
  }
  const mb = kb / 1024;
  return `${mb < 100 ? mb.toFixed(2) : Math.round(mb)} MB`;
}

/** 本地时钟 `HH:MM:SS.mmm`：手写补零，避免 locale 差异让测试与截图不稳定。 */
export function formatLiveClock(at: number): string {
  const date = new Date(at);
  const pad = (value: number, width = 2) => String(value).padStart(width, "0");
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}.${pad(
    date.getMilliseconds(),
    3,
  )}`;
}

export type LiveFrameGapStats = {
  /** 相邻帧到达间隔的中位数（毫秒）；样本不足时 null。 */
  p50: number | null;
  /** 最大间隔（毫秒）；样本不足时 null。 */
  max: number | null;
};

/**
 * 帧间隔抖动：直接用最近帧缓冲（新→旧）的相邻时间差。
 * 业务事件与 keepalive 都算流量——「只剩 keepalive」本身就是最值得看到的形态。
 */
export function resolveFrameGapStats(
  frames: readonly LiveFrameSample[],
): LiveFrameGapStats {
  if (frames.length < 2) {
    return { p50: null, max: null };
  }
  const gaps: number[] = [];
  for (let index = 0; index < frames.length - 1; index += 1) {
    gaps.push(Math.abs(frames[index].at - frames[index + 1].at));
  }
  const sorted = [...gaps].sort((a, b) => a - b);
  const middle = Math.floor(sorted.length / 2);
  const p50 =
    sorted.length % 2 === 0
      ? (sorted[middle - 1] + sorted[middle]) / 2
      : sorted[middle];
  return { p50, max: sorted[sorted.length - 1] };
}

/** 帧的短标签：事件名 + 可选 seq；面板用它做「最近帧」列表。 */
export function formatLiveFrameLabel(frame: LiveFrameSample): string {
  return frame.seq === null ? frame.name : `${frame.name} #${frame.seq}`;
}
