// 由 hooks/workspace/use-workspace-agent-chat-turn.ts 机械拆分而来（P0-2），仅搬迁不改语义。

/**
 * rAF 节流的流式渲染调度器：把高频 SSE delta 合并为每帧一次渲染；后台标签页不触发
 * rAF，用 setTimeout 兜底（G3），标签页恢复可见时立即冲刷。
 *
 * 2026-09-15 追加**最小提交间隔**（`MIN_COMMIT_INTERVAL_MS`）：文本 / 推理 delta 的每次
 * 提交都会改写页面级 thread state，因此 `WorkspacePage` → `WorkspaceShell` → 侧栏 /
 * 面板 / 消息列整棵树都要重渲染一次。CDP CPU profile 实测（`e2e/zz-perf-probe.manual.ts`）：
 * 按 rAF（≈60 次/秒）提交时，流式期间主线程约 330ms/s 花在这条链上——i18next 每帧几十次
 * `t()`、React 全页协调、样式重算（另有 ~80ms/s RecalcStyle + ~30ms/s Layout）；长会话下
 * 还会打出 150-200ms 的长任务，即用户看到的「整页卡住」。
 *
 * 因此把**页面级提交**压到最小间隔（默认 ≈8 次/秒）。平滑度不受影响：
 * - 行内打字机（`use-typewriter`）仍按 rAF 60fps 逐字揭示，它消费的是目标文本，
 *   目标文本按最小间隔跳跃、揭示过程连续；
 * - 贴底跟随由滚动宿主的 `ResizeObserver` 持续钉底线，不依赖提交频率；
 * - 工具结果、可见性恢复、回合收尾走 `flush()`，立即提交（不受最小间隔约束）。
 */
const MIN_COMMIT_INTERVAL_MS = 120;

/**
 * **三帧门**（P1-2 发布分级）：`animation-frame` 档的增量提交必须**连跨三次绘制
 * 机会**（60fps 下 ≈20Hz 上限）后才上屏。
 *
 * 参考取证 `ui-conversation/.../assembly.ts:130-147`：高频流更新先挂起一个 rAF，
 * 回调里再连排两次，第三次回调才 flush——挂起帧存在时再次调度直接去重
 * （`if (this.frame !== undefined) return`）。我们不照抄其数字（§9.3.3），
 * 3 帧 = 50ms 是有界节奏：P1-1 切片后 live 通道提交只打消息列，可放宽回帧对齐
 * （计划书 P1-1 预期），同时突发发布频率 ≤ 帧门上限（计划书 P1-2 预期）。
 *
 * 与 `MIN_COMMIT_INTERVAL_MS` 的关系：**合并而非叠加**。
 * - `animation-frame` 档：**120ms 下界 + 三帧门**——120ms 维持提交频率预算
 *   （验收「提交次数:事件数 远小于 1:1」，P0-2 的 −67% 实测不回归）；三帧门保证
 *   帧节奏（两次提交至少隔 3 个绘制机会，突发多事件合帧、上屏平稳不抖动）；
 * - `structural` 档（默认 / 持久信号）：既有 120ms 最小间隔 + 一帧 rAF 对齐，
 *   逐字节不变（chat 通道与历史验证语义不动）；
 * - `immediate`（`flush({force:true})`）取消挂起帧立即提交，两档共用。
 */
export const PUBLICATION_FRAME_GATE_FRAMES = 3;

/** 提交节奏档：`animation-frame` = 三帧门（内容增量）；`structural` = 既有最小间隔（含持久信号）。 */
export type StreamingPace = "animation-frame" | "structural";

/**
 * **结构快照**（thread store 里的正文副本）的最小间隔。
 *
 * live 通道（`lib/live-stream-text.ts`）接管了"正在揭示的文本"之后，store 里的正文
 * 只承担两件事：① 段落骨架（工具行 / 代码块闭合 / 推理收尾位）需要文本参与；
 * ② 定稿前给 store 一份完整副本（定稿本身会用 terminal 文本重写，见 finalize-turn）。
 * 两者都不需要跟 SSE 节奏对齐，因此压到 1 次/秒——文本量按秒只有几百字符，
 * 而每次写 store 都要整棵工作区树重渲染一次（Probe A：把提交间隔从 120ms 放到
 * 1000ms，ScriptDur 3.167s → 1.049s，−67%）。
 *
 * 两条豁免（见 renderStreamingMessage 调用点）：
 * - store 里还没有可见正文时立即提交：否则流式消息在首块期间没有可挂的行节点，
 *   live 文本无行可渲染；
 * - `flush({force:true})` 一律提交：可见性恢复 / result 收口 / 里程碑。
 */
export const STRUCTURAL_COMMIT_INTERVAL_MS = 1000;

/** 隐藏标签页判定（P0-2）：无 document 环境（SSR / 单测）视为可见。 */
function isDocumentHidden(): boolean {
  return typeof document !== "undefined" && document.visibilityState === "hidden";
}

export function createStreamingFrameScheduler(
  renderStreamingMessage: (options?: { force?: boolean }) => void,
) {
  let pendingStreamingFrame: number | null = null;
  let pendingStreamingTimeout: number | null = null;
  let lastCommitAt = 0;
  /**
   * P1-2 三帧门剩余帧数：`animation-frame` 档排帧时置 3，rAF 回调里递减，
   * 减到 0 才提交（连跨三次绘制机会）；`structural` 档恒为 0/1（一帧即提交）。
   * `flush`（immediate）/ 兜底定时器跳过剩余门数直接兑现（有界延迟）。
   */
  let frameGateRemaining = 0;
  /**
   * P0-2：自上次提交以来是否存在未兑现的渲染请求。
   * - `schedule()` 置脏，提交清脏；
   * - 隐藏标签页不排任何回调（零工作），脏位保留，恢复可见时一次兑现；
   * - 无脏位的调度回调不再空提交（no-op commit 旁路）。
   */
  let dirty = false;

  const now = () =>
    typeof performance !== "undefined" && typeof performance.now === "function"
      ? performance.now()
      : Date.now();

  const clearPendingStreamingTimeout = () => {
    if (
      pendingStreamingTimeout !== null &&
      typeof window !== "undefined" &&
      typeof window.clearTimeout === "function"
    ) {
      window.clearTimeout(pendingStreamingTimeout);
    }
    pendingStreamingTimeout = null;
  };

  const cancelPending = () => {
    if (
      pendingStreamingFrame !== null &&
      typeof window !== "undefined" &&
      typeof window.cancelAnimationFrame === "function"
    ) {
      window.cancelAnimationFrame(pendingStreamingFrame);
    }
    pendingStreamingFrame = null;
    clearPendingStreamingTimeout();
    frameGateRemaining = 0;
    dirty = false;
  };

  const commitStreamingMessage = (force = false) => {
    cancelPending();
    dirty = false;
    lastCommitAt = now();
    renderStreamingMessage({ force });
  };

  /**
   * 立即冲刷（页面恢复可见 / 流结束时调用）：可见时不受最小间隔约束；
   * 隐藏时只记脏，恢复可见时一次兑现（P0-2：隐藏标签页零工作）。
   */
  const flushStreamingMessage = () => {
    if (isDocumentHidden()) {
      dirty = true;
      return;
    }
    commitStreamingMessage(true);
  };

  /** 排一次 rAF；回调里按三帧门剩余帧数决定再排或提交。 */
  const scheduleFrameOnce = () => {
    pendingStreamingFrame = window.requestAnimationFrame(() => {
      pendingStreamingFrame = null;
      clearPendingStreamingTimeout();
      if (isDocumentHidden()) {
        dirty = true; // 隐藏：保留脏位，恢复可见时兑现
        return;
      }
      if (!dirty) {
        return; // 无变化：不空提交
      }
      // P1-2 三帧门：剩余门数未耗尽则连排下一帧（跨绘制机会），不提交。
      frameGateRemaining -= 1;
      if (frameGateRemaining > 0) {
        scheduleFrameOnce();
        return;
      }
      commitStreamingMessage();
    });
    return pendingStreamingFrame;
  };

  /** 帧对齐提交（rAF + 后台标签页 setTimeout 兜底，G3）。`gateFrames` = 三帧门门数。 */
  const scheduleStreamingFrame = (gateFrames: number) => {
    if (pendingStreamingFrame !== null) {
      return;
    }

    frameGateRemaining = gateFrames;
    scheduleFrameOnce();

    // 后台兜底（G3）只在可见时保留：隐藏标签页零工作（P0-2），恢复可见由
    // visibilitychange 一次冲刷；挂起事件已累积在 turnState / 挂起批次里，不丢。
    if (isDocumentHidden()) {
      return;
    }
    pendingStreamingTimeout = window.setTimeout(() => {
      if (
        pendingStreamingFrame !== null &&
        typeof window.cancelAnimationFrame === "function"
      ) {
        window.cancelAnimationFrame(pendingStreamingFrame);
        pendingStreamingFrame = null;
      }
      pendingStreamingTimeout = null;
      if (isDocumentHidden()) {
        dirty = true;
        return;
      }
      if (!dirty) {
        return;
      }
      // 兜底不等满三帧门（100ms 已超过三帧 ≈50ms）：有界延迟直接兑现。
      frameGateRemaining = 0;
      commitStreamingMessage();
    }, 100);
  };

  const scheduleStreamingMessage = (options?: { pace?: StreamingPace }) => {
    dirty = true;
    // P0-2：隐藏标签页零工作——不排 rAF / 不排兜底定时器；恢复可见时
    // visibilitychange 看到脏位并一次冲刷。
    if (isDocumentHidden()) {
      return;
    }
    if (pendingStreamingFrame !== null || pendingStreamingTimeout !== null) {
      return;
    }

    if (
      typeof window === "undefined" ||
      typeof window.requestAnimationFrame !== "function"
    ) {
      flushStreamingMessage();
      return;
    }

    // P1-2 发布分级：animation-frame 档 = 120ms 下界 + 三帧门（帧节奏）；
    // structural 档 = 既有 120ms + 一帧 rAF（逐字节不变）。
    const gateFrames =
      (options?.pace ?? "structural") === "animation-frame"
        ? PUBLICATION_FRAME_GATE_FRAMES
        : 1;

    // 距上次页面级提交不足最小间隔：先等满，再按档位排帧；
    // 等待期间到达的 delta 已累积在 turnState 里，提交时一次性兑现。
    const waitMs = MIN_COMMIT_INTERVAL_MS - (now() - lastCommitAt);
    if (waitMs > 0) {
      pendingStreamingTimeout = window.setTimeout(() => {
        pendingStreamingTimeout = null;
        scheduleStreamingFrame(gateFrames);
      }, waitMs);
      return;
    }

    scheduleStreamingFrame(gateFrames);
  };

  const handleVisibilityChange = () => {
    if (document.visibilityState !== "visible") {
      return;
    }
    // 隐藏期间攒下的渲染请求（脏位 / 挂起句柄）在这里一次兑现；没有就不提交。
    if (
      !dirty &&
      pendingStreamingFrame === null &&
      pendingStreamingTimeout === null
    ) {
      return;
    }
    commitStreamingMessage(true);
  };

  const attachVisibilityListener = () => {
    if (typeof document === "undefined") {
      return;
    }
    document.addEventListener("visibilitychange", handleVisibilityChange);
  };

  const detachVisibilityListener = () => {
    if (typeof document === "undefined") {
      return;
    }
    document.removeEventListener("visibilitychange", handleVisibilityChange);
  };

  return {
    cancel: cancelPending,
    flush: flushStreamingMessage,
    schedule: scheduleStreamingMessage,
    attachVisibilityListener,
    detachVisibilityListener,
  };
}
