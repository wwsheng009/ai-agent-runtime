// 「网络详情」的 React 绑定层：订阅观测 store + 观测消息列的 DOM 活跃度。
//
// 为什么需要 DOM 活跃度：观测 store 只能回答「SSE 有没有事件、闸门开没开」，
// 但「事件到了、闸门也开着、页面却不动」还可能是 React 提交/浏览器渲染的问题。
// 消息列（`[role="log"]`）的实际 DOM 变更是这一环唯一诚实的证据——它不依赖
// 任何插桩，直接测「渲染有没有发生」。
//
// 观测值一律上报给 store（快照里带 `dom` 字段），本层不持有可变状态：渲染期
// 不读 ref、不读时钟，也就没有「观测值要等下一次渲染才更新」的时序陷阱。

import { useCallback, useEffect, useSyncExternalStore } from "react";

import {
  getLiveDiagnosticsSnapshot,
  reportDomActivity,
  reportDomObserver,
  subscribeLiveDiagnostics,
} from "@/lib/live-diagnostics/store";
import { type LiveDiagnosticsSnapshot } from "@/lib/live-diagnostics/types";

/** 订阅某会话的观测快照（store 侧已合并通知，这里不会按帧重渲染）。 */
export function useLiveDiagnostics(sessionId: string): LiveDiagnosticsSnapshot {
  const subscribe = useCallback(
    (listener: () => void) => subscribeLiveDiagnostics(listener),
    [],
  );
  const getSnapshot = useCallback(
    () => getLiveDiagnosticsSnapshot(sessionId),
    [sessionId],
  );
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}

/** 消息列选择器：与 message-list 的 `role="log"` 契约一致。 */
const MESSAGE_LIST_SELECTOR = '[role="log"]';
/** 重新寻找消息列的周期：处理列表节点被替换（切会话/重挂载）的情况。 */
const REATTACH_INTERVAL_MS = 2_000;

/**
 * 观测消息列的 DOM 变更并上报 store。
 *
 * 无返回值：观测值随快照下发、按 store 的合并窗口刷新，观测本身不额外驱动
 * 渲染（避免「观测面板自己给自己加负载」）。找不到消息列时如实上报
 * `observing=false`——沉默会把「没观测到」伪装成「没有变更」。
 */
export function useMessageListDomObserver(): void {
  useEffect(() => {
    if (typeof document === "undefined" || typeof MutationObserver !== "function") {
      return;
    }
    let observer: MutationObserver | null = null;
    let observed: Element | null = null;

    const attach = () => {
      const target = document.querySelector(MESSAGE_LIST_SELECTOR);
      if (target === observed) {
        return;
      }
      observer?.disconnect();
      observer = null;
      observed = target;
      reportDomObserver(Boolean(target));
      if (!target) {
        return;
      }
      observer = new MutationObserver((records) => {
        reportDomActivity(records.length);
      });
      observer.observe(target, {
        characterData: true,
        childList: true,
        subtree: true,
      });
    };

    attach();
    const timer = setInterval(attach, REATTACH_INTERVAL_MS);
    return () => {
      clearInterval(timer);
      observer?.disconnect();
      observed = null;
      reportDomObserver(false);
    };
  }, []);
}
