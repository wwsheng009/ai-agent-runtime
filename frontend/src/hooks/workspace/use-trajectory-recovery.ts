/**
 * 轨迹恢复（P3-1 事件重放 + P4 历史兜底）：会话选中/页面刷新后
 * 从 EventStore 增量拉取事件并按 seq 重放进轨迹 reducer。
 *
 * - 触发：sessionId 变化（挂载/线程切换，workspace-page 已同步 reset）；
 * - 分页：after = 已收最大 seq（后端 ListEvents 注入 payload.seq）；
 * - 幂等：reducer 乱序缓冲 + 稳定 ID upsert 保证与实时流并发安全；
 * - 失败可见：下拉失败时通过 onError 上报 UI（thread lastError banner），
 *   不再静默——实时流 / history sync 仍兜底内容，但连接问题必须可感知。
 *
 * 尾部优先（tail-first）：首屏不再从 seq=0 全量重放，而是只回放**最近一页**
 * 事件（`tail=1&limit=TRAJECTORY_TAIL_WINDOW_EVENTS`），窗口之前的 seq 用
 * 基准游标跳过；用户上滚 / 点「加载更早」时按 `before_seq` 逐页向前前插
 * （store 保留动作日志并重建投影）。大会话实测 2288 条 / 2.25MB：首屏回放
 * 量与解析字节数降为约 1/3，且窗口之外不再产生任何渲染项。
 *
 * P4 历史兜底：一部分会话（由 aicli 进程内 chat 运行时执行、只落生命周期
 * 事件与诊断事件的会话）EventStore 里没有任何 `chat.sse.*` 内容帧，轨迹于是
 * 只剩 system 行。这类会话的消息本体在持久化会话历史里，因此恢复链路在此
 * 回退到「会话历史 → 轨迹帧」投影（lib/trajectory/session-history.ts）并按
 * turn 插回事件序列，保证轨迹能看到所有消息、且重启（内存态清空）后仍可查看。
 */
import { useCallback, useEffect, useRef, useState } from "react";

import {
  fetchSessionRuntimeEvents,
} from "@/api/runtime/sessions";
import type { SessionRuntimeEvent } from "@/types/runtime";
import type {
  TrajectoryRawAction,
  TrajectoryStore,
} from "@/hooks/workspace/use-trajectory-snapshot";
import {
  fetchSessionHistoryMessages,
  hasTrajectoryContentFrames,
  mergeTrajectoryHistoryFallback,
  type TrajectoryReplayStep,
  trajectoryReplaySteps,
} from "@/lib/trajectory/history-fallback";
import {
  chatSseEventSeq,
  nextRecoveryAfter,
  TRAJECTORY_EARLIER_PAGE_EVENTS,
  TRAJECTORY_RECOVERY_PAGE_SIZE,
  TRAJECTORY_TAIL_WINDOW_EVENTS,
  TRAJECTORY_WINDOW_REPAIR_MAX_PAGES,
} from "@/lib/trajectory/recovery";

type TrajectoryRecoveryOptions = {
  store: TrajectoryStore;
  sessionId: string | undefined;
  /** 页面可见时才恢复（后台 tab 不抢带宽；默认 true）。 */
  enabled?: boolean;
  /** 增量拉取失败回调（HTTP 错误 / 网络错误）；参数为发起恢复的会话与错误文案。 */
  onError?: (sessionId: string, message: string) => void;
};

/** 已回放窗口（尾部优先）：驱动轨迹视图顶部的「加载更早」行。 */
export type TrajectoryReplayWindow = {
  /** 已回放事件的最早 seq（0 = 无窗口语义，例如从游标续拉或空会话）。 */
  firstSeq: number;
  /** 服务端确认还有更早的一页。 */
  hasMore: boolean;
};

export type TrajectoryRecoveryResult = {
  /**
   * 首屏窗口是否已就绪（尾部优先回放完成，或已判定无需回放）。
   *
   * 实时流据此 gate 首次建连：若在窗口就绪前用 `after=0` 建连，后端会把整份
   * 事件日志重放一遍，尾部优先就失效了。失败同样置为 true（fail-open：宁可
   * 退回全量回放，也不能让会话永远不连实时流）。
   */
  ready: boolean;
  window: TrajectoryReplayWindow;
  /** 加载更早一页（幂等：在途时重复调用直接返回）。 */
  loadEarlier: () => Promise<void>;
  /** 「加载更早」在途标记（视图禁用按钮 + 文案切换）。 */
  loadingEarlier: boolean;
};

/** 窗口起点 seq：优先用服务端 `first_seq`，缺失时回落到首条事件的持久化 seq。 */
function resolveWindowFirstSeq(
  events: SessionRuntimeEvent[],
  serverFirstSeq: number | undefined,
): number {
  if (typeof serverFirstSeq === "number" && Number.isFinite(serverFirstSeq)) {
    const normalized = Math.floor(serverFirstSeq);
    if (normalized > 0) {
      return normalized;
    }
  }
  for (const event of events) {
    const seq = chatSseEventSeq(event);
    if (seq > 0) {
      return seq;
    }
  }
  return 0;
}

/** 恢复计划的 push/skip 步 → store 的前插动作（同一形状，便于统一重建）。 */
function replayStepToRawAction(step: TrajectoryReplayStep): TrajectoryRawAction {
  return step.type === "push"
    ? { type: "push", kind: step.push.kind, payload: step.push.payload }
    : { type: "skip", seq: step.seq };
}

/** 按计划推进 store：可渲染帧 push，被过滤事件的 seq 空洞 advanceCursor。 */
function applyReplaySteps(
  store: Pick<TrajectoryStore, "push" | "advanceCursor">,
  plan: TrajectoryReplayStep[],
): void {
  for (const step of plan) {
    if (step.type === "push") {
      store.push(step.push.kind, step.push.payload);
    } else {
      store.advanceCursor(step.seq);
    }
  }
}

export function useTrajectoryRecovery({
  store,
  sessionId,
  enabled = true,
  onError,
}: TrajectoryRecoveryOptions): TrajectoryRecoveryResult {
  const storeRef = useRef(store);
  storeRef.current = store;
  const onErrorRef = useRef(onError);
  onErrorRef.current = onError;
  const recoveredSessionRef = useRef<string | null>(null);
  const windowRef = useRef<{ sessionId: string; firstSeq: number; hasMore: boolean } | null>(
    null,
  );
  const loadingEarlierRef = useRef(false);
  const [windowState, setWindowState] = useState<TrajectoryReplayWindow>({
    firstSeq: 0,
    hasMore: false,
  });
  const [loadingEarlier, setLoadingEarlier] = useState(false);
  const [readySessionId, setReadySessionId] = useState<string | null>(null);

  useEffect(() => {
    if (!sessionId || !enabled) {
      return;
    }
    // 同一会话只恢复一次（成功完成后标记）；线程切换（sessionId 变化）
    // 重新恢复。取消/失败不标记——reload 后会话数据短暂清空再恢复时
    // （selectedThread 经 undefined 往返）允许重试，避免恢复永远丢失。
    if (recoveredSessionRef.current === sessionId) {
      return;
    }

    let cancelled = false;

    void (async () => {
      try {
        // 已应用游标：与在途实时流共享。
        //
        // `lastEventSeq > 0` 说明实时流已经（或正在）把事件喂进同一 reducer，
        // 此时沿用**增量续拉**语义：从游标向后补齐，不做窗口化——因为窗口只
        // 影响「历史首屏」，而这里已经处在直播推进的路径上（常见情况：请求数
        // 5 → 0/1）。
        //
        // `lastEventSeq === 0`（冷启动 / 流不可用）才是首屏：走**尾部优先**，
        // 只回放最近一页事件，窗口之前的 seq 用基准游标跳过。
        const startAfter = storeRef.current.getSnapshot().lastEventSeq ?? 0;

        if (startAfter > 0) {
          const events: SessionRuntimeEvent[] = [];
          let after = startAfter;
          for (;;) {
            // 每轮分页前重读共享游标：实时流正在**并发**把同一份日志喂进 store
            // （dump 逐页到达），游标会一路前移。若沿用第一轮捕获的 after，恢复
            // 链路会一路把流已经重放过的页再拉一遍；重读后每轮自动跳到「尚未覆盖」
            // 的位置，通常在 1~2 次请求内收敛。
            after = Math.max(
              after,
              storeRef.current.getSnapshot().lastEventSeq ?? 0,
            );
            const { events: batch } = await fetchSessionRuntimeEvents(sessionId, {
              after,
              limit: TRAJECTORY_RECOVERY_PAGE_SIZE,
            });
            if (cancelled) {
              return;
            }
            for (const event of batch) {
              events.push(event);
            }
            if (batch.length === 0 || batch.length < TRAJECTORY_RECOVERY_PAGE_SIZE) {
              break;
            }
            after = nextRecoveryAfter(batch, after);
          }

          let plan = trajectoryReplaySteps(events);
          if (!hasTrajectoryContentFrames(events)) {
            const history = await fetchSessionHistoryMessages(
              sessionId,
              () => cancelled,
            );
            if (cancelled) {
              return;
            }
            if (history.length > 0) {
              plan = mergeTrajectoryHistoryFallback(plan, history);
            }
          }

          for (const step of plan) {
            if (step.type === "push") {
              storeRef.current.push(step.push.kind, step.push.payload);
            } else {
              storeRef.current.advanceCursor(step.seq);
            }
          }
          storeRef.current.flush();
          windowRef.current = null;
          setWindowState({ firstSeq: 0, hasMore: false });
          recoveredSessionRef.current = sessionId;
          return;
        }

        // 尾部优先首屏：只取最近一页（tail=1），旧内容留给「加载更早」。
        // 与在途实时流共享游标：`/runtime/stream?after=0` 的批量重放会把同一批
        // 事件喂进**同一个**轨迹 reducer 并前移 `lastEventSeq`（见 workspace-page
        // 的 onTrajectoryEvent）。恢复链路若仍从 0 分页，就会把同一份事件日志
        // 再拉一遍：实测大会话首屏 5 页 × ~360KB = 1.78MB，与 SSE dump 的
        // 2.15MB 完全重叠，并且 2288 条事件被解析/重放两遍——首屏 8 个 long task
        // 的主要来源。这里从「已应用游标」续拉：
        // - 流已覆盖的部分不再请求、不再重放（常见情况：请求数 5 → 0/1）；
        // - 流尚未到达（慢/断连/被代理拦）时游标仍是 0，行为与原来完全一致，
        //   全量兜底语义不变。
        const tail = await fetchSessionRuntimeEvents(sessionId, {
          tail: true,
          limit: TRAJECTORY_TAIL_WINDOW_EVENTS,
        });
        if (cancelled) {
          return;
        }
        const events = tail.events;
        const firstSeq = resolveWindowFirstSeq(events, tail.first_seq);
        const hasMore = Boolean(tail.has_more);
        // 窗口之前的 seq 视为「已跳过」：否则 reducer 会把首个事件当成乱序待补，
        // 整个窗口卡在 pending、轨迹一行都不渲染。
        storeRef.current.setBaselineSeq(firstSeq > 1 ? firstSeq - 1 : 0);

        // 事件序列 → 计划（可渲染帧 push；被过滤事件的 seq 空洞 advanceCursor）。
        // 逐个处理保持既有语义：被过滤的 tool_started/context.profile.injected
        // 等事件占据同一 EventStore 全局 seq，必须前移游标否则后续事件永久 pending。
        let plan = trajectoryReplaySteps(events);

        if (!hasTrajectoryContentFrames(events)) {
          const history = await fetchSessionHistoryMessages(
            sessionId,
            () => cancelled,
          );
          if (cancelled) {
            return;
          }
          if (history.length > 0) {
            plan = mergeTrajectoryHistoryFallback(plan, history);
          }
        }

        applyReplaySteps(storeRef.current, plan);
        storeRef.current.flush();

        // 窗口落地校验：窗口只有「序列连续推进游标」才渲染得出来，但游标可能停在
        // 窗口末尾之前——同一段增量已被实时路径应用（`seenDeltaKeys` 去重后不再
        // 推进）、后端返回的一页被截断、或 EventStore 保留期丢了中间事件。此时
        // 窗口渲染不全，按 after 续拉补齐：游标已到窗口末尾则一次请求都不发，
        // 重复事件按 seq 幂等丢弃，无进展即退出（不空转）。
        const windowEndSeq = typeof tail.last_seq === "number" ? tail.last_seq : 0;
        for (
          let attempt = 0;
          attempt < TRAJECTORY_WINDOW_REPAIR_MAX_PAGES;
          attempt += 1
        ) {
          const cursor = storeRef.current.getSnapshot().lastEventSeq ?? 0;
          if (cursor >= windowEndSeq) {
            break;
          }
          const repaired = await fetchSessionRuntimeEvents(sessionId, {
            after: cursor,
            limit: TRAJECTORY_RECOVERY_PAGE_SIZE,
          });
          if (cancelled) {
            return;
          }
          if (repaired.events.length === 0) {
            break;
          }
          applyReplaySteps(storeRef.current, trajectoryReplaySteps(repaired.events));
          storeRef.current.flush();
          if ((storeRef.current.getSnapshot().lastEventSeq ?? 0) <= cursor) {
            break; // 兜底：无进展即退出，避免空转请求
          }
        }

        windowRef.current = { sessionId, firstSeq, hasMore };
        setWindowState({ firstSeq, hasMore });
        recoveredSessionRef.current = sessionId;
      } catch (error) {
        // 恢复失败不再静默：连接问题必须让用户可见（thread 标为降级）。
        // 已取消的恢复（会话已切换）不再上报，避免把迟到失败错标到
        // 当前（无关）线程上。
        if (cancelled) {
          return;
        }
        const message =
          error instanceof Error && error.message
            ? error.message
            : String(error);
        onErrorRef.current?.(sessionId, message);
      } finally {
        // 窗口就绪（含失败 fail-open）：实时流据此 gate 首次建连，避免在窗口
        // 之前用 after=0 建连把整份日志再重放一遍。
        if (!cancelled) {
          setReadySessionId(sessionId);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [enabled, sessionId]);

  /**
   * 向前加载更早一页（尾部优先窗口的「加载更早」）。
   *
   * 语义：`before_seq = 上一页的 next_before_seq ?? first_seq`（排他上界），
   * 拿到的页**前插**到已回放窗口之前——store 重建投影后与「一次性回放全部
   * 已加载事件」完全一致（见 `TrajectoryStore.prependEarlier`）。
   */
  const loadEarlier = useCallback(async () => {
    const targetSession = sessionId;
    const current = windowRef.current;
    if (!targetSession || !current || current.sessionId !== targetSession) {
      return;
    }
    if (!current.hasMore || current.firstSeq <= 1) {
      return;
    }
    // 回放日志已被上限裁剪（大会话反复前插）：更早页前插后会整体重建投影，
    // 而最早保留动作之前的行无法复现（见 `TrajectoryStore.isReplayLogTruncated`）。
    // 此时不再前移游标、不发请求，直接收起入口——否则「加载更早」会把已渲染的
    // 最早行静默吞掉，且游标已前移，用户再也拉不回来。
    if (storeRef.current.isReplayLogTruncated()) {
      windowRef.current = {
        sessionId: targetSession,
        firstSeq: current.firstSeq,
        hasMore: false,
      };
      setWindowState({ firstSeq: current.firstSeq, hasMore: false });
      return;
    }
    if (loadingEarlierRef.current) {
      return;
    }
    loadingEarlierRef.current = true;
    setLoadingEarlier(true);
    try {
      const page = await fetchSessionRuntimeEvents(targetSession, {
        beforeSeq: current.firstSeq,
        limit: TRAJECTORY_EARLIER_PAGE_EVENTS,
      });
      // 会话在请求期间被切走：丢弃这一页（store 已被新会话 reset）。
      if (windowRef.current?.sessionId !== targetSession) {
        return;
      }
      const events = page.events;
      // 空页（并发删除 / 服务端边界）时不前移游标，避免把窗口游标重置成 0
      // 后彻底失去向前翻页能力。
      const firstSeq =
        events.length > 0 ? resolveWindowFirstSeq(events, page.first_seq) : current.firstSeq;
      const hasMore = events.length > 0 && Boolean(page.has_more) && firstSeq > 1;
      if (events.length > 0) {
        storeRef.current.prependEarlier(
          trajectoryReplaySteps(events).map(replayStepToRawAction),
        );
        storeRef.current.flush();
      }
      windowRef.current = { sessionId: targetSession, firstSeq, hasMore };
      setWindowState({ firstSeq, hasMore });
    } catch (error) {
      // 与首屏恢复同口径：失败可见（thread 降级 banner），不静默。
      const message =
        error instanceof Error && error.message ? error.message : String(error);
      onErrorRef.current?.(targetSession, message);
    } finally {
      loadingEarlierRef.current = false;
      setLoadingEarlier(false);
    }
  }, [sessionId]);

  return {
    ready: !sessionId || readySessionId === sessionId,
    window: windowState,
    loadEarlier,
    loadingEarlier,
  };
}
