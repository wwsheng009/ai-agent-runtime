// SSE live 观测（网络详情）的数据形状：传输层与渲染闸门的**唯一**事实来源。
//
// 为什么独立成模块：两条流（`/runtime/stream` 与 `/api/agent/chat`）共用
// `api/runtime/sse.ts` 的读取循环，而「有没有事件」与「有没有渲染」是两个不同
// 的问题，需要同一份快照交叉判定。类型放这里，store 与 UI 都只依赖它。

/** 观测通道：运行时流（长连接订阅）与本地回合流（chat POST 流式响应）。 */
export type LiveDiagnosticsChannelId = "runtime" | "chat";

/** 一次读取循环里落地的一帧。keepalive 是注释帧（无业务数据）但**是存活证据**。 */
export type LiveFrameKind = "event" | "keepalive" | "error";

export type LiveFrameSample = {
  /** 到达时刻（epoch ms；便于与面板的 1s tick 求差）。 */
  at: number;
  kind: LiveFrameKind;
  /** 事件名或 payload.type（运行时事件用它区分 delta/reasoning/工具事件）。 */
  name: string;
  /** 事件游标；live-only 帧无 seq（0/undefined）时为 null。 */
  seq: number | null;
  /** 该次读取分片的字节数（粗粒度，用于判断「只剩 keepalive」）。 */
  bytes: number;
};

export type LiveChannelDiagnostics = {
  /** 读取循环是否在场（open→true / finally close→false）。 */
  active: boolean;
  opens: number;
  closes: number;
  events: number;
  keepalives: number;
  errors: number;
  bytes: number;
  /** 静默看门狗命中次数（无字节超时）与最近一次阈值。 */
  stalls: number;
  lastStallMs: number | null;
  lastOpenAt: number | null;
  lastCloseAt: number | null;
  /** 任何字节（含 keepalive）的最近到达时刻——「SSE 有没有消息」看它。 */
  lastFrameAt: number | null;
  /** 业务事件（非 keepalive）的最近到达时刻。 */
  lastEventAt: number | null;
  lastKeepaliveAt: number | null;
  lastErrorAt: number | null;
  lastError: string | null;
  lastEventName: string | null;
  /** 已消费到的最大事件 seq（游标）。 */
  cursor: number | null;
  /** 最近帧（新→旧，长度上限见 LIVE_FRAME_BUFFER）。 */
  frames: readonly LiveFrameSample[];
};

/** 渲染闸门：运行时流只负责传输，「这条增量能不能落到消息上」的判定在接线层。 */
export type LiveRenderGateDiagnostics = {
  liveTurnId: string | null;
  resumedTurnId: string | null;
  localResponding: boolean;
  /** 与 use-workspace-live 的 renderLiveDeltas 同一口径。 */
  rendering: boolean;
  lastGateChangeAt: number | null;
};

/** 闸门侧的累计计数：判断「事件在到达但被前端拦下」的关键证据。 */
export type LiveRenderCounters = {
  /** 闸门关闭时到达的增量帧数（这些帧不会渲染）。 */
  blockedDeltas: number;
  /** 「未认领回合」发现次数与最近一次触发的快照刷新次数。 */
  unownedTurns: number;
  snapshotRefreshes: number;
  lastBlockedAt: number | null;
  lastUnownedTurnId: string | null;
  lastUnownedAt: number | null;
};

/**
 * 消息列的 DOM 观测（全局一份，不区分会话）：
 * 插桩只能说「事件到了」，这里直接说「DOM 真的变了没有」——判定归属的最后一环。
 */
export type LiveDomActivity = {
  /** 自观测以来消息列的 DOM 变更条数（含流式追加、工具行更新、折叠等）。 */
  changes: number;
  /** 最近一次变更时刻；没有变更时为 null。 */
  lastChangeAt: number | null;
  /** 观测起始时刻（首次盯上消息列时落笔；面板先于消息列挂载则为 0）。 */
  startedAt: number;
  /** 是否找到了消息列节点（找不到时数据无意义，UI 必须如实说明）。 */
  observing: boolean;
};

export type LiveDiagnosticsSnapshot = {
  sessionId: string;
  runtime: LiveChannelDiagnostics;
  chat: LiveChannelDiagnostics;
  gate: LiveRenderGateDiagnostics;
  counters: LiveRenderCounters;
  /** 消息列 DOM 活跃度（全局观测，随任一会话的快照一起下发）。 */
  dom: LiveDomActivity;
  /** 每次写入自增：UI 可据此判断快照新鲜度。 */
  revision: number;
};

/** 传输层写入句柄：方法必须是**廉价且无副作用**的（每帧都会调用）。 */
export type LiveChannelSink = {
  open: () => void;
  close: () => void;
  bytes: (count: number) => void;
  keepalive: () => void;
  event: (name: string, payload: Record<string, unknown>) => void;
  stall: (idleTimeoutMs: number) => void;
};

/** 最近帧环形缓冲长度：够看清「帧有没有在流」，又不至于让面板自己变成负担。 */
export const LIVE_FRAME_BUFFER = 40;

/** 面板展示的最近帧条数。 */
export const LIVE_FRAME_VISIBLE = 6;
