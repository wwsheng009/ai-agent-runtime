// SSE live 观测 store（模块级、无 React）：
//   * 写入端：传输层（api/runtime/sse.ts）与接线层（use-workspace-live）；
//   * 读取端：「会话详情 → 网络详情」面用 useSyncExternalStore 订阅。
//
// 三条硬约束：
//   1. 写入必须廉价：流式回合实测可达 ~1000 帧，逐帧 JSON 深拷贝会把观测本身
//      变成性能问题（这正是要排查的那类问题）。因此只累加数字 + 环形缓冲。
//   2. 通知必须合并：逐帧通知会让面板以帧率重渲染。250ms 窗口把面板刷新压到
//      ≤4 次/秒——观测的是「网络与渲染链路」，秒级足够。
//   3. 快照必须引用稳定：useSyncExternalStore 要求同一份数据返回同一引用，
//      否则会触发无限重渲染。快照按会话缓存，写入时失效、读取时重建。

import {
  LIVE_FRAME_BUFFER,
  LIVE_TRAFFIC_BUFFER,
  type LiveChannelDiagnostics,
  type LiveChannelSink,
  type LiveDomActivity,
  type LiveDiagnosticsChannelId,
  type LiveDiagnosticsSnapshot,
  type LiveFrameKind,
  type LiveFrameSample,
  type LiveRenderCounters,
  type LiveRenderGateDiagnostics,
} from "./types";

/** 订阅通知合并窗口（毫秒）：见文件头约束 2。 */
export const LIVE_DIAGNOSTICS_NOTIFY_MS = 250;

/** 无会话上下文时的兜底键（chat 流可能拿不到 session_id）。 */
const UNKNOWN_SESSION_KEY = "*";

type SessionState = {
  runtime: LiveChannelDiagnostics;
  chat: LiveChannelDiagnostics;
  gate: LiveRenderGateDiagnostics;
  counters: LiveRenderCounters;
  revision: number;
};

const states = new Map<string, SessionState>();
const snapshots = new Map<string, LiveDiagnosticsSnapshot>();
const listeners = new Set<() => void>();
let notifyTimer: ReturnType<typeof setTimeout> | null = null;

/** 消息列 DOM 观测：全局一份（消息列表在全应用内唯一），因此不挂在 SessionState 上。 */
let domActivity: LiveDomActivity = emptyDomActivity();

function emptyChannel(): LiveChannelDiagnostics {
  return {
    active: false,
    opens: 0,
    closes: 0,
    events: 0,
    keepalives: 0,
    errors: 0,
    bytes: 0,
    stalls: 0,
    lastStallMs: null,
    lastOpenAt: null,
    lastCloseAt: null,
    lastFrameAt: null,
    lastEventAt: null,
    lastKeepaliveAt: null,
    lastErrorAt: null,
    lastError: null,
    lastEventName: null,
    cursor: null,
    frames: [],
    traffic: [],
  };
}

function emptyDomActivity(): LiveDomActivity {
  return { changes: 0, lastChangeAt: null, startedAt: 0, observing: false };
}

function emptyState(): SessionState {
  return {
    runtime: emptyChannel(),
    chat: emptyChannel(),
    gate: {
      liveTurnId: null,
      resumedTurnId: null,
      localResponding: false,
      rendering: false,
      lastGateChangeAt: null,
    },
    counters: {
      blockedDeltas: 0,
      unownedTurns: 0,
      snapshotRefreshes: 0,
      lastBlockedAt: null,
      lastUnownedTurnId: null,
      lastUnownedAt: null,
    },
    revision: 0,
  };
}

function sessionKey(sessionId?: string | null): string {
  const trimmed = sessionId?.trim();
  return trimmed ? trimmed : UNKNOWN_SESSION_KEY;
}

function stateFor(sessionId?: string | null): { key: string; state: SessionState } {
  const key = sessionKey(sessionId);
  let state = states.get(key);
  if (!state) {
    state = emptyState();
    states.set(key, state);
  }
  return { key, state };
}

function scheduleNotify() {
  // 合并窗口内已排过通知就不再排：首个写入开启窗口，窗口结束统一通知一次。
  if (notifyTimer !== null || listeners.size === 0) {
    return;
  }
  notifyTimer = setTimeout(() => {
    notifyTimer = null;
    for (const listener of Array.from(listeners)) {
      listener();
    }
  }, LIVE_DIAGNOSTICS_NOTIFY_MS);
}

/**
 * 立即通知（不合并）。只给低频事件用——观测挂载/脱离必须当场生效，否则面板
 * 会在 250ms 窗口里显示过期的「未找到消息列」。高频写入（帧、DOM 变更）仍走
 * `scheduleNotify` 的合并窗口。
 */
function notifyNow() {
  if (notifyTimer !== null) {
    clearTimeout(notifyTimer);
    notifyTimer = null;
  }
  for (const listener of Array.from(listeners)) {
    listener();
  }
}

/** 写入后统一收口：作废快照缓存 + 合并通知。 */
function touch(key: string) {
  const state = states.get(key);
  if (state) {
    state.revision += 1;
  }
  snapshots.delete(key);
  scheduleNotify();
}

/** DOM 观测是全局的（不属于某个会话），只能整体作废快照缓存后合并通知。 */
function touchDom() {
  snapshots.clear();
  scheduleNotify();
}

/**
 * 消息列 DOM 变更上报（写入端在 use-live-diagnostics 的 MutationObserver）。
 *
 * 注意：这只在**消息列真的变了**时才被调用，因此不会形成「观测 → 渲染 →
 * 观测」的自激环；面板自身的渲染不在此 observer 的观察范围内。
 */
export function reportDomActivity(records: number) {
  if (records <= 0) {
    return;
  }
  domActivity.changes += records;
  domActivity.lastChangeAt = Date.now();
  touchDom();
}

/** 观测挂载状态上报：消息列找到/丢失（丢失时必须如实说明，沉默 = 谎报「无变更」）。 */
export function reportDomObserver(observing: boolean) {
  if (domActivity.observing === observing) {
    return;
  }
  domActivity.observing = observing;
  if (observing && domActivity.startedAt === 0) {
    // 观测起点 = 第一次真正盯上消息列的时刻（面板可能先于消息列挂载）。
    domActivity.startedAt = Date.now();
  }
  snapshots.clear();
  notifyNow();
}

function pushFrame(channel: LiveChannelDiagnostics, frame: LiveFrameSample) {
  channel.frames = [frame, ...channel.frames].slice(0, LIVE_FRAME_BUFFER);
}

function readEventName(
  channel: LiveDiagnosticsChannelId,
  name: string,
  payload: Record<string, unknown>,
): string {
  const type = payload.type;
  if (typeof type === "string" && type.trim()) {
    return type.trim();
  }
  return name || (channel === "runtime" ? "runtime_event" : "message");
}

function readSeq(payload: Record<string, unknown>): number | null {
  const seq = payload.seq;
  return typeof seq === "number" && Number.isFinite(seq) && seq > 0 ? seq : null;
}

function readErrorText(payload: Record<string, unknown>): string {
  const error = payload.error;
  if (typeof error === "string" && error.trim()) {
    return error.trim();
  }
  return "stream reported an error";
}

/**
 * 传输层写入句柄。`sessionId` 缺省时落到兜底键（chat 流拿不到会话时才可能发生），
 * 该通道的数据不会出现在任何会话详情的面板里——如实丢弃胜过把数据张冠李戴。
 */
export function beginLiveChannel(input: {
  channel: LiveDiagnosticsChannelId;
  sessionId?: string | null;
}): LiveChannelSink {
  const { key, state } = stateFor(input.sessionId);
  const channel = state[input.channel];
  const now = () => Date.now();

  return {
    open() {
      channel.active = true;
      channel.opens += 1;
      channel.lastOpenAt = now();
      touch(key);
    },
    close() {
      if (channel.active) {
        channel.active = false;
      }
      channel.closes += 1;
      channel.lastCloseAt = now();
      touch(key);
    },
    // 字节是**每帧**累加的热路径：只加数字、不通知（下一次事件到达时统一刷新）。
    // 同时按秒聚合成一个流量桶——面板的波动图要的是每秒吞吐，而读取分片密度极
    // 不均匀（见 LiveTrafficSample 的说明）。秒内只改桶里的数字（零分配），跨秒
    // 才换数组。当前秒的桶是这里唯一可变的对象：快照按引用共享它，面板下一次
    // 重渲染就自然读到最新值，不需要额外的通知来驱动。
    bytes(count: number) {
      if (count > 0) {
        channel.bytes += count;
        const second = Math.floor(Date.now() / 1000) * 1000;
        const head = channel.traffic[0];
        if (head !== undefined && head.at === second) {
          head.bytes += count;
        } else {
          channel.traffic = [
            { at: second, bytes: count },
            ...channel.traffic,
          ].slice(0, LIVE_TRAFFIC_BUFFER);
        }
      }
    },
    keepalive() {
      const at = now();
      channel.keepalives += 1;
      channel.lastKeepaliveAt = at;
      channel.lastFrameAt = at;
      pushFrame(channel, { at, bytes: 0, kind: "keepalive", name: ": keepalive", seq: null });
      touch(key);
    },
    event(name, payload) {
      const at = now();
      const kind: LiveFrameKind = name === "error" ? "error" : "event";
      const seq = readSeq(payload);
      channel.events += 1;
      channel.lastFrameAt = at;
      channel.lastEventAt = at;
      channel.lastEventName = readEventName(input.channel, name, payload);
      if (seq !== null) {
        channel.cursor = Math.max(channel.cursor ?? 0, seq);
      }
      if (kind === "error") {
        channel.errors += 1;
        channel.lastErrorAt = at;
        channel.lastError = readErrorText(payload);
      }
      pushFrame(channel, { at, bytes: 0, kind, name: channel.lastEventName, seq });
      touch(key);
    },
    stall(idleTimeoutMs) {
      channel.stalls += 1;
      channel.lastStallMs = idleTimeoutMs;
      touch(key);
    },
  };
}

/** 渲染闸门状态上报：只在字段真变化时写入，避免每个 render 都作废快照。 */
export function reportRenderGate(
  sessionId: string | null | undefined,
  gate: {
    liveTurnId: string | null;
    resumedTurnId: string | null;
    localResponding: boolean;
    rendering: boolean;
  },
) {
  const { key, state } = stateFor(sessionId);
  const current = state.gate;
  const changed =
    current.liveTurnId !== gate.liveTurnId ||
    current.resumedTurnId !== gate.resumedTurnId ||
    current.localResponding !== gate.localResponding ||
    current.rendering !== gate.rendering;
  if (!changed) {
    return;
  }
  const now = Date.now();
  state.gate = {
    ...gate,
    // 闸门开合时刻单独记录：面板据此说明「闸门何时关上的」。
    lastGateChangeAt: current.rendering === gate.rendering ? current.lastGateChangeAt : now,
  };
  touch(key);
}

/** 闸门关闭期间到达的增量帧：**核心证据**——事件在到达、前端没渲染。 */
export function reportBlockedDelta(sessionId: string | null | undefined) {
  const { key, state } = stateFor(sessionId);
  state.counters.blockedDeltas += 1;
  state.counters.lastBlockedAt = Date.now();
  touch(key);
}

/** 「未认领回合」发现：本页没有任何回合身份，但服务端在产增量。 */
export function reportUnownedTurn(
  sessionId: string | null | undefined,
  turnId: string,
) {
  const { key, state } = stateFor(sessionId);
  state.counters.unownedTurns += 1;
  state.counters.lastUnownedTurnId = turnId;
  state.counters.lastUnownedAt = Date.now();
  touch(key);
}

/** 因未认领回合而触发的 `/runtime` 快照刷新（自愈动作）。 */
export function reportSnapshotRefresh(sessionId: string | null | undefined) {
  const { key, state } = stateFor(sessionId);
  state.counters.snapshotRefreshes += 1;
  touch(key);
}

function buildSnapshot(key: string, state: SessionState): LiveDiagnosticsSnapshot {
  return {
    sessionId: key === UNKNOWN_SESSION_KEY ? "" : key,
    runtime: {
      ...state.runtime,
      frames: [...state.runtime.frames],
      // 只冻结数组本身（长度与顺序），桶对象按引用共享：当前秒那一桶仍在
      // 原地累计，面板重渲染时读到的是最新值。
      traffic: [...state.runtime.traffic],
    },
    chat: {
      ...state.chat,
      frames: [...state.chat.frames],
      traffic: [...state.chat.traffic],
    },
    gate: { ...state.gate },
    counters: { ...state.counters },
    dom: { ...domActivity },
    revision: state.revision,
  };
}

/** 读取某会话的观测快照（引用稳定：同一份数据多次读取返回同一对象）。 */
export function getLiveDiagnosticsSnapshot(sessionId: string): LiveDiagnosticsSnapshot {
  const key = sessionKey(sessionId);
  const cached = snapshots.get(key);
  if (cached) {
    return cached;
  }
  const state = states.get(key) ?? emptyState();
  const snapshot = buildSnapshot(key, state);
  snapshots.set(key, snapshot);
  return snapshot;
}

export function subscribeLiveDiagnostics(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** 测试专用：清空全部状态与在途通知（生产代码不要调用）。 */
export function resetLiveDiagnostics() {
  states.clear();
  snapshots.clear();
  listeners.clear();
  domActivity = emptyDomActivity();
  if (notifyTimer !== null) {
    clearTimeout(notifyTimer);
    notifyTimer = null;
  }
}
