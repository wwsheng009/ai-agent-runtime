import type { SessionRuntimeEvent } from "@/lib/runtime-api";
import {
  getRuntimeDeltaKind,
  getRuntimeEventTurnId,
} from "@/lib/workspace-thread-state";

/**
 * L3 传输层「帧级至多一次」闸门（同 session 单写者 + seq 单调）。
 *
 * 背景：同一会话有两条 SSE 通道会投递同一批内容帧——
 *   A) `/api/agent/chat`（消费层：agent-chat-turn/stream-handlers.ts）；
 *   B) `/runtime/stream`（消费层：use-session-runtime-stream.ts）。
 * 服务端的 provider 增量既随 chat 流下发，又被持久化（带会话级 seq）在 runtime
 * 流上重放；runtime 流自身还可能因为「尾部窗口回放 + after 游标 dump + live
 * 订阅」重叠而重复投递同一帧。此前两路只能靠共享的 deltaCoordinator 按 provider
 * key（stream_id + sequence）兜底，命中不了 key 的帧（缺 stream_id / sequence）
 * 会被两条通道各应用一次。
 *
 * 本模块提供**两条通道共享**的帧入口闸门（module-level Map，按会话隔离）：
 * - 只对带持久化 seq 的内容帧（seq > 0）生效：同一会话 + 同一帧类别（text /
 *   reasoning / image）维护「已放行最大 seq」水位，`seq <= 水位` 视为重复/回退
 *   帧，整帧丢弃（并累计丢弃计数，便于诊断「是不是闸门吃掉了内容」）。
 * - 无身份帧（seq <= 0，例如 chat 流的 wire-only `chunk`/`reasoning`：后端明确
 *   不落盘、不带 `_event.sequence`）一律放行，保持既有 deltaCoordinator 按
 *   provider key 去重的行为——缺身份不等于重复，不能误丢。
 * - 水位按「会话 × 帧类别」存储：seq 是**会话级**游标，不同类别的帧可能共享同一
 *   seq（如 `assistant.reasoning` 与其 `chat.sse.reasoning` 孪生帧相差 1），按
 *   类别隔离可避免某一类的空洞帧把另一类的真实内容帧一起丢掉。
 * - 判定是纯同步调用，必须留在 React updater/渲染路径之外（与
 *   deltaCoordinator.claim 同口径：updater 可能被 StrictMode / 并发渲染重放）。
 *   被拒绝的帧**不得**消费 delta key。
 *
 * 本模块不替代 L2（lib/thread-state/**）的投影合并兜底，也不改变其行为：L2 仍
 * 负责历史回放/快照收敛，这里只保证「同一个 token 帧在传输入口只被放行一次」。
 */

/** 帧类别：与 lib/thread-state/deltas.ts 的 RuntimeDeltaKind 同名同义。 */
export type FrameIntakeKind = "text" | "reasoning" | "image";

/**
 * runtime 通道（/runtime/stream）的入口判定：只有带持久化 seq 的内容增量帧
 * 参与闸门（这些帧与 chat 流的孪生帧共享同一 provider 内容）；meta/tool/
 * lifecycle 帧以及 seq=0 的 live 帧一律放行，保持既有行为。
 */
export function admitRuntimeFrame(input: {
  sessionId?: string | null;
  event: SessionRuntimeEvent;
  seq: number;
  /** 单写者作用域：两条通道共享的 deltaCoordinator 实例（见 admitTransportFrame）。 */
  scope?: object | null;
}): boolean {
  const kind = getRuntimeDeltaKind(input.event.type);
  if (!kind) {
    return true;
  }
  return admitTransportFrame({
    sessionId: input.sessionId,
    turnId: getRuntimeEventTurnId(input.event),
    kind,
    seq: input.seq,
    scope: input.scope,
  });
}

export type FrameIntakeFrame = {
  sessionId?: string | null;
  /** 无 session 身份时的退化身份（新建会话可能只有 turn）。 */
  turnId?: string | null;
  kind: FrameIntakeKind;
  /** 持久化 seq；<= 0 / 非有限数 = 无身份，不参与闸门。 */
  seq: number;
  /**
   * 单写者作用域：本会话两条 SSE 通道共享的 deltaCoordinator 实例
   * （workspace-page 每页 useMemo 一个，两路都收到同一引用；见其
   * `deltaCoordinator` 注释「与直连 chat 共享的增量认领协调器」）。
   *
   * 传入时水位按「作用域 × 会话」隔离：同一页面的 chat/runtime 两入口共用一份；
   * 页面重建（整页刷新、测试用例各自新建 coordinator）天然从零开始，不会把
   * 上一份页面状态的水位带进新页面。
   *
   * 缺省（调用方不持有协调器）时退化为模块级 Map<sessionId, state>：两条通道
   * 都缺省时仍共享同一份，跨通道语义与传入作用域时一致。
   */
  scope?: object | null;
};

type FrameIntakeState = {
  /** 已放行的最大持久化 seq（按帧类别隔离）。 */
  applied: Map<FrameIntakeKind, number>;
  /** 被拒绝的重复/回退帧数（诊断与测试用）。 */
  dropped: number;
};

type FrameIntakeNamespace = Map<string, FrameIntakeState>;

/**
 * 无单写者作用域时的退化命名空间：模块级 Map<sessionId, state>。生产路径总是
 * 传入共享 deltaCoordinator，这里只服务不持有协调器的调用方。
 */
const FALLBACK_INTAKE: FrameIntakeNamespace = new Map();

/**
 * 单写者作用域 → 命名空间。WeakMap 保证作用域（页面级 coordinator）生命周期
 * 结束后水位随之释放；同一作用域内 chat/runtime 两入口读到同一份 Map。
 */
const SCOPED_INTAKE = new WeakMap<object, FrameIntakeNamespace>();

/** 被闸门拒绝的帧总数（诊断用；不分会话/作用域）。 */
let droppedTotal = 0;

function isScope(value: object | null | undefined): value is object {
  return (
    (typeof value === "object" && value !== null) || typeof value === "function"
  );
}

function intakeNamespace(scope: object | null | undefined): FrameIntakeNamespace {
  if (!isScope(scope)) {
    return FALLBACK_INTAKE;
  }
  let namespace = SCOPED_INTAKE.get(scope);
  if (!namespace) {
    namespace = new Map();
    SCOPED_INTAKE.set(scope, namespace);
  }
  return namespace;
}

function intakeState(
  scope: object | null | undefined,
  key: string,
): FrameIntakeState {
  const namespace = intakeNamespace(scope);
  let state = namespace.get(key);
  if (!state) {
    state = { applied: new Map(), dropped: 0 };
    namespace.set(key, state);
  }
  return state;
}

function normalizeIdentity(value: string | null | undefined): string {
  return typeof value === "string" ? value.trim() : "";
}

/** 帧身份：`session_id` 优先，缺会话时退化为 `turn_id`（都没有则不参与闸门）。 */
export function frameIntakeKey(
  frame: Pick<FrameIntakeFrame, "sessionId" | "turnId">,
): string {
  const session = normalizeIdentity(frame.sessionId);
  if (session) {
    return `session:${session}`;
  }
  const turn = normalizeIdentity(frame.turnId);
  return turn ? `turn:${turn}` : "";
}

// 与 trajectory-reducer/event-readers.ts 的 eventSeqOf 同取法：chat wire 的持久化
// seq 在 `_event.sequence`（服务端写失败时不写该字段，见 sseEmitter 注释）。
function readPositiveInt(raw: unknown): number {
  if (typeof raw === "number" && Number.isFinite(raw)) {
    return raw;
  }
  if (typeof raw === "string" && raw.trim() !== "") {
    const parsed = Number(raw);
    if (Number.isFinite(parsed)) {
      return parsed;
    }
  }
  return 0;
}

/** chat 帧的持久化 seq：`_event.sequence`（缺省 = 0，走既有按 key 去重）。 */
export function readChatFrameSeq(payload: unknown): number {
  if (!payload || typeof payload !== "object") {
    return 0;
  }
  const envelope = (payload as Record<string, unknown>)._event;
  if (!envelope || typeof envelope !== "object") {
    return 0;
  }
  return readPositiveInt((envelope as Record<string, unknown>).sequence);
}

/**
 * 入口放行判定：true = 继续既有 willApply→claim 流程；false = 整帧丢弃。
 *
 * 只有「确定带持久化 seq 且已被应用过」的帧才会被拒；丢帧前不消费任何 delta key。
 */
export function admitTransportFrame(frame: FrameIntakeFrame): boolean {
  const seq = Number.isFinite(frame.seq) ? frame.seq : 0;
  if (seq <= 0) {
    return true;
  }
  const key = frameIntakeKey(frame);
  if (!key) {
    return true;
  }
  const state = intakeState(frame.scope, key);
  const applied = state.applied.get(frame.kind) ?? 0;
  if (seq <= applied) {
    state.dropped += 1;
    droppedTotal += 1;
    return false;
  }
  state.applied.set(frame.kind, seq);
  return true;
}

/** 被闸门拒绝的帧总数（诊断用；不分会话）。 */
export function getFrameIntakeDroppedTotal(): number {
  return droppedTotal;
}

/**
 * 测试专用：清空**无作用域**命名空间与丢弃计数。带作用域的水位挂在 WeakMap 上
 * （无法枚举），测试应每个用例新建 scope/coordinator 以获得隔离；生产代码不应
 * 调用（会重新打开重复帧窗口）。
 */
export function resetFrameIntake(): void {
  FALLBACK_INTAKE.clear();
  droppedTotal = 0;
}
