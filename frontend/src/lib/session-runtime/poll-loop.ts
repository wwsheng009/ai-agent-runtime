// Batch 4（多会话并发运行时 §4.6）：poll 订阅的循环、空闲自适应与隐藏暂停。
//
// 从 `entry.ts` 抽出（P0-2 单文件 ≤500 非空行）：entry 只保留状态机与 live 循环，
// 这里承载 poll 的传输节奏与降采样策略：
// - 空闲自适应：活动指纹不变时按 1.5 倍退避到 `pollIdleMaxMs`，指纹变化回起点；
// - 失败退避：与空闲退避相互独立，上限 `pollMaxMs`（原行为不变）；
// - 隐藏暂停：暂停期间挂起（不产生请求），恢复时立即拉取一次；
// - 轻量视图：轮询固定带 `view=light`（省略 stable_tool_surface 等大字段）。
//
// 纯逻辑、无 React：`isActive` / `isPaused` / 回调都由调用方注入，可脱离浏览器单测。

import type { getSessionRuntimeState } from "@/lib/runtime-api";
import type { RuntimeSessionSnapshot } from "@/types/runtime";

type FetchSnapshotFn = typeof getSessionRuntimeState;

export type SessionRuntimePollLoopConfig = {
  sessionId: string;
  signal: AbortSignal;
  fetchSnapshot: FetchSnapshotFn;
  pollInitialMs: number;
  pollMaxMs: number;
  pollIdleMaxMs: number;
  pollBackoffFactor: number;
  /** 循环是否仍属于当前订阅（disposed / 模式切换后返回 false）。 */
  isActive: () => boolean;
  isPaused: () => boolean;
  /** 暂停期间挂起；恢复（或 abort）时立即返回，循环随即拉取一次。 */
  waitWhilePaused: (signal: AbortSignal) => Promise<void>;
  /** 进入暂停等待时回调（状态置 idle）。 */
  onPaused: () => void;
  onSnapshot: (snapshot: RuntimeSessionSnapshot | null) => void;
  onError: (error: unknown) => void;
};

export function sleepWithSignal(ms: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted) {
    return Promise.resolve();
  }
  return new Promise<void>((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(timer);
        resolve();
      },
      { once: true },
    );
  });
}

/**
 * 快照活动指纹：`poll` 自适应退避的判据。指纹一致 = 这段时间没有新回合、
 * 没有新待交互、也没有事件推进（head_offset / status），空闲会话可安全退避。
 */
export function snapshotActivityKey(
  snapshot: RuntimeSessionSnapshot | null,
): string {
  const state = snapshot?.state ?? null;
  return [
    state?.status ?? "",
    state?.headOffset ?? 0,
    snapshot?.activeTurn?.turnId ?? "",
    state?.pendingApproval?.id ?? "",
    state?.pendingQuestion?.id ?? "",
  ].join("|");
}

export type SessionRuntimePauseGate = {
  isPaused(): boolean;
  setPaused(paused: boolean): void;
  waitWhilePaused(signal: AbortSignal): Promise<void>;
};

/**
 * 暂停闸门：页面隐藏时由注册表调用 `setPaused(true)`，轮询循环在下一次进入
 * 循环体时挂起；`setPaused(false)` 立即唤醒（恢复可见即刷新一次）。
 */
export function createSessionRuntimePauseGate(
  onChange: (paused: boolean) => void,
): SessionRuntimePauseGate {
  let paused = false;
  const waiters = new Set<() => void>();

  function waitWhilePaused(signal: AbortSignal): Promise<void> {
    if (!paused || signal.aborted) {
      return Promise.resolve();
    }
    return new Promise<void>((resolve) => {
      const finish = () => {
        waiters.delete(finish);
        signal.removeEventListener("abort", finish);
        resolve();
      };
      waiters.add(finish);
      signal.addEventListener("abort", finish, { once: true });
    });
  }

  return {
    isPaused: () => paused,
    setPaused(next: boolean) {
      if (paused === next) {
        return;
      }
      paused = next;
      onChange(next);
      if (!next) {
        for (const finish of [...waiters]) {
          finish();
        }
      }
    },
    waitWhilePaused,
  };
}

/** 常驻快照轮询循环（模式切到 idle / dispose 时由调用方 abort）。 */
export async function runSessionRuntimePollLoop(
  config: SessionRuntimePollLoopConfig,
): Promise<void> {
  const {
    sessionId,
    signal,
    fetchSnapshot,
    pollInitialMs,
    pollMaxMs,
    pollIdleMaxMs,
    pollBackoffFactor,
    isActive,
    isPaused,
    waitWhilePaused,
    onPaused,
    onSnapshot,
    onError,
  } = config;
  let interval = pollInitialMs;
  let lastActivityKey: string | null = null;

  while (isActive() && !signal.aborted) {
    if (isPaused()) {
      // 隐藏期间不轮询：状态置 idle，恢复可见时循环顶部立即拉取一次。
      onPaused();
      await waitWhilePaused(signal);
      if (!isActive() || signal.aborted) {
        return;
      }
    }
    try {
      const snapshot = await fetchSnapshot(sessionId, {
        signal,
        // 轻量视图：后台轮询不需要 stable_tool_surface 等大字段。
        view: "light",
      });
      if (!isActive()) {
        return;
      }
      // 活动指纹变化（新回合 / 新待交互 / 事件推进）→ 回到起点周期；
      // 连续无变化 → 按 1.5 倍退避到空闲上限（成功路径的降采样）。
      const activityKey = snapshotActivityKey(snapshot);
      interval =
        activityKey === lastActivityKey
          ? Math.min(Math.round(interval * pollBackoffFactor), pollIdleMaxMs)
          : pollInitialMs;
      lastActivityKey = activityKey;
      onSnapshot(snapshot);
    } catch (error) {
      if (signal.aborted || !isActive()) {
        return;
      }
      onError(error);
      interval = Math.min(Math.round(interval * pollBackoffFactor), pollMaxMs);
    }
    await sleepWithSignal(interval, signal);
  }
}
