import { useCallback, useEffect, useRef, useState } from "react";

import { type ConnectionStatus } from "@/lib/connection-status";
import { type SessionRuntimeEvent } from "@/lib/runtime-api";
import { getPublicationLevel } from "@/lib/thread-state/publication";
import { runSessionRuntimeSubscription } from "./subscription";
import {
  type RuntimeStreamStatusState,
  type SessionRuntimeStreamOptions,
} from "./types";

export function useSessionRuntimeStream({
  applyRuntimeEventToThread,
  applyRuntimeDeltaToThread,
  getErrorMessage,
  getRuntimeEventSeq,
  mergeRuntimeEvent,
  onTrajectoryEvent,
  onRuntimeEvent,
  renderLiveDeltas = false,
  activeTurnId,
  deltaCoordinator,
  selectedThread,
  setThreads,
  setThreadsLive,
  enabled = true,
  getReplayCursor,
}: SessionRuntimeStreamOptions) {
  // P1-8：对外暴露连接状态与手动重试入口；手动重试复用同一重连循环
  // （retryNonce 触发 effect 重跑，旧连接先 abort，不新增第二套退避）。
  // 状态按「连接键（thread:session:retryNonce）」存储：无会话时在渲染期派生为
  // idle，重试时自然回落 connecting；effect 同步段不写状态
  // （react-hooks/set-state-in-effect），状态推进只来自流回调与重连循环（异步）。
  const [statusState, setStatusState] = useState<RuntimeStreamStatusState>(null);
  const [retryNonce, setRetryNonce] = useState(0);
  const retryConnection = useCallback(() => {
    setRetryNonce((current) => current + 1);
  }, []);

  const runtimeEventsRef = useRef<Record<string, SessionRuntimeEvent[]>>({});
  const runtimeSeqRef = useRef<Record<string, number>>({});
  const activeTurnIdRef = useRef(activeTurnId);
  useEffect(() => {
    activeTurnIdRef.current = activeTurnId;
  }, [activeTurnId]);
  const deltaCoordinatorRef = useRef(deltaCoordinator);
  useEffect(() => {
    deltaCoordinatorRef.current = deltaCoordinator;
  }, [deltaCoordinator]);
  // Q4：轨迹投递回调经 ref 转发——调用方每次 render 产生新引用时
  // 不触发 effect 重跑（否则 onEvent 内 setThreads → render → 新 arrow →
  // abort 重连 → 无限重连循环）。ref 写入放在 effect（react-hooks/refs）。
  const onTrajectoryEventRef = useRef(onTrajectoryEvent);
  useEffect(() => {
    onTrajectoryEventRef.current = onTrajectoryEvent;
  }, [onTrajectoryEvent]);
  const onRuntimeEventRef = useRef(onRuntimeEvent);
  useEffect(() => {
    onRuntimeEventRef.current = onRuntimeEvent;
  }, [onRuntimeEvent]);
  const renderLiveDeltasRef = useRef(renderLiveDeltas);
  useEffect(() => {
    renderLiveDeltasRef.current = renderLiveDeltas;
  }, [renderLiveDeltas]);
  // 订阅 effect 的依赖里**只允许留「会话身份」**（sessionId/sessionKey/retryNonce
  // 与 hasThreadId 布尔位）。回调与 setter 一律经 ref 读取：
  // 这些引用由父级 render 决定，父级因任何原因（首屏线程归并、事件到达后
  // setThreads 触发的新 render、父级 useCallback 依赖变化）产生新引用时，
  // 若留在依赖里就会 abort 在途 SSE 再重连——实测大会话首屏 1s 内 6 次建连、
  // 5 次被 cleanup 掐断（浏览器侧表现为 canceled/失败），且每次重连都从游标
  // 重拉一份事件日志。长连接的生命周期必须只由会话切换与手动重试决定。
  const applyRuntimeEventToThreadRef = useRef(applyRuntimeEventToThread);
  useEffect(() => {
    applyRuntimeEventToThreadRef.current = applyRuntimeEventToThread;
  }, [applyRuntimeEventToThread]);
  const applyRuntimeDeltaToThreadRef = useRef(applyRuntimeDeltaToThread);
  useEffect(() => {
    applyRuntimeDeltaToThreadRef.current = applyRuntimeDeltaToThread;
  }, [applyRuntimeDeltaToThread]);
  const getErrorMessageRef = useRef(getErrorMessage);
  useEffect(() => {
    getErrorMessageRef.current = getErrorMessage;
  }, [getErrorMessage]);
  const getRuntimeEventSeqRef = useRef(getRuntimeEventSeq);
  useEffect(() => {
    getRuntimeEventSeqRef.current = getRuntimeEventSeq;
  }, [getRuntimeEventSeq]);
  const mergeRuntimeEventRef = useRef(mergeRuntimeEvent);
  useEffect(() => {
    mergeRuntimeEventRef.current = mergeRuntimeEvent;
  }, [mergeRuntimeEvent]);
  const setThreadsRef = useRef(setThreads);
  useEffect(() => {
    setThreadsRef.current = setThreads;
  }, [setThreads]);
  const setThreadsLiveRef = useRef(setThreadsLive);
  useEffect(() => {
    setThreadsLiveRef.current = setThreadsLive;
  }, [setThreadsLive]);
  const getPublicationLevelRef = useRef(getPublicationLevel);
  useEffect(() => {
    getPublicationLevelRef.current = getPublicationLevel;
  }, []);
  // 目标线程最新值：判断「这条增量能否落到消息上」必须读线程状态（纯判定，
  // 不写状态）。经 ref 读取，避免把 selectedThread 放进订阅 effect 依赖——
  // 线程对象每次归并都是新引用，进依赖会掐断在途 SSE（见下方 sessionKey 注释）。
  const selectedThreadRef = useRef(selectedThread);
  useEffect(() => {
    selectedThreadRef.current = selectedThread;
  }, [selectedThread]);
  // 尾部优先：建连游标经 ref 读取（调用方每次 render 都会产出新箭头函数，
  // 不能进依赖，否则会掐断在途 SSE——与本文件其它回调同口径）。
  const getReplayCursorRef = useRef(getReplayCursor);
  useEffect(() => {
    getReplayCursorRef.current = getReplayCursor;
  }, [getReplayCursor]);
  const threadId = selectedThread?.id;
  const sessionId = selectedThread?.sessionId;
  // 连接键只由 sessionId + retryNonce 决定，**刻意不含 threadId**：
  // 启动期 `mergeRuntimeSessionsIntoThreads`（lib/thread-state/sessions.ts）会丢弃
  // 占位线程、按 sessionId 重新归并，threadId 在首屏一秒内会多次变化。把它放进
  // effect 依赖会让订阅反复卸载重建——实测大会话首屏 0.75s 内开了 6 条 SSE 连接、
  // 5 条被 cleanup abort（DevTools 里显示为 canceled/失败），并且可能在 dump 传输
  // 中途掐断连接，导致同一份事件日志被重拉。
  const sessionKey = sessionId ? `${sessionId}:${retryNonce}` : null;
  // 事件回填仍需最新 threadId：经 ref 读取，不进依赖。
  const threadIdRef = useRef(threadId);
  useEffect(() => {
    threadIdRef.current = threadId;
  }, [threadId]);
  // 只关心「有没有 threadId」这个布尔位：undefined → 有值 时补跑一次订阅，
  // 值本身变化（重键）不再重订阅。
  const hasThreadId = Boolean(threadId);
  const connectionStatus: ConnectionStatus =
    sessionKey === null
      ? "idle"
      : statusState?.key === sessionKey
        ? statusState.status
        : "connecting";

  useEffect(() => {
    if (!enabled || !hasThreadId || !sessionId || !sessionKey) {
      return;
    }

    return runSessionRuntimeSubscription({
      sessionId,
      sessionKey,
      setStatusState,
      runtimeEventsRef,
      runtimeSeqRef,
      activeTurnIdRef,
      deltaCoordinatorRef,
      onTrajectoryEventRef,
      onRuntimeEventRef,
      renderLiveDeltasRef,
      applyRuntimeEventToThreadRef,
      applyRuntimeDeltaToThreadRef,
      getErrorMessageRef,
      getRuntimeEventSeqRef,
      mergeRuntimeEventRef,
      setThreadsRef,
      setThreadsLiveRef,
      getPublicationLevelRef,
      selectedThreadRef,
      getReplayCursorRef,
      threadIdRef,
    });
    // 依赖里只留会话身份：回调/setter 经上面的 ref 读取（保持最新引用但不参与
    // 依赖），否则任何父级 render 造成的引用抖动都会掐断在途 SSE。
  }, [retryNonce, sessionId, sessionKey, hasThreadId, enabled]);

  return { connectionStatus, retryConnection };
}
