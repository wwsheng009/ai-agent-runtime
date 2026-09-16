import { useCallback, useSyncExternalStore } from "react";

import { type RuntimeLiveDelta } from "@/lib/thread-state/events-live";

/**
 * 流式文本 live 通道（外部 store）。
 *
 * 背景（CDP CPU profile 实测，2026-09-15）：文本增量此前每次都写页面级 thread
 * state，`WorkspacePage` → `WorkspaceShell` → topbar / 侧栏 / composer / 消息列
 * 整棵树都要重渲染一次。把**页面级提交**压到 1 次/秒的对照实验里 ScriptDur
 * 从 3.167s 掉到 1.049s（−67%），说明剩余开销里约 2/3 与流式文本本身无关，
 * 纯粹是"每次增量都惊动整页"。
 *
 * 因此把"正在揭示的文本"从 thread store 里解耦：增量文本进本模块（只在
 * `StreamingMarkdown` 处订阅，命中即整条消息外的树都不动），thread store 只按
 * 低频写"结构快照"（段落 / 工具行 / 定稿文本）。
 *
 * 不变式（两条通道共享，务必保持）：
 * 1. **每个增量必须且只能被追加一次**。`/api/agent/chat` 与 runtime/stream 两条
 *    通道通过 `RuntimeDeltaCoordinator` 共享去重 key（先判后 claim），因此
 *    "谁 claim 谁追加"——两条通道各自在事件到达时同步追加，顺序即到达顺序。
 * 2. **live 文本永远不短于 thread store 里的副本**。store 副本只在结构提交时用
 *    当时的完整文本重写（替换而非追加），所以它天然是 live 文本的前缀。
 *
 * 写入方（append/set）与读取方（useLiveStreamText）都按 messageId 寻址；没有
 * live 记录的会话（历史回放 / reload / 已定稿消息）走 store 文本，行为不变。
 */
export type LiveStreamEntry = {
  /** 正在揭示的正文（对应最后一段 text segment）。 */
  text: string;
  /**
   * 正在揭示的推理文本（对应**最后一段** reasoning segment）。
   *
   * 一个回合里推理与工具交替出现（推理 → 工具 → 推理 → …），每条消息因此可以有多
   * 段推理。本字段永远只装**当前这一块**：新块的首个增量必须用
   * `setLiveStreamReasoning` 覆盖（写方按 `RuntimeLiveDelta.blockStart` 判定），更早
   * 的块已经定稿在 thread store 的独立推理段里。以前它累加整轮推理，尾行于是把
   * 所有块拼成一段。
   */
  reasoningText: string;
};

const entries = new Map<string, LiveStreamEntry>();
const listeners = new Set<() => void>();

function emit() {
  // 复制一份再遍历：监听器可能在回调里退订（unmount / 消息切换）。
  for (const listener of Array.from(listeners)) {
    listener();
  }
}

function writeEntry(
  messageId: string,
  updater: (current: LiveStreamEntry) => LiveStreamEntry | null,
) {
  const id = messageId?.trim();
  if (!id) {
    return;
  }
  const current = entries.get(id) ?? { reasoningText: "", text: "" };
  const next = updater(current);
  if (!next || next === current) {
    return;
  }
  if (!next.text && !next.reasoningText) {
    entries.delete(id);
  } else {
    entries.set(id, next);
  }
  emit();
}

/** 追加一段正文增量（增量语义：保留原始空白，不 trim）。 */
export function appendLiveStreamText(messageId: string, delta: string) {
  if (!delta) {
    return;
  }
  writeEntry(messageId, (current) => ({
    ...current,
    text: current.text + delta,
  }));
}

/** 追加一段推理增量（增量语义：保留原始空白，不 trim）。 */
export function appendLiveStreamReasoning(messageId: string, delta: string) {
  if (!delta) {
    return;
  }
  writeEntry(messageId, (current) => ({
    ...current,
    reasoningText: current.reasoningText + delta,
  }));
}

/**
 * 以完整文本覆盖正文（chat 通道按合帧节奏把 `turnState.streamedText` 同步进来）。
 * 与 append 等价的家法：调用方持有权威累加器，这里只是它的投影。
 */
export function setLiveStreamText(messageId: string, text: string) {
  writeEntry(messageId, (current) =>
    current.text === text ? current : { ...current, text },
  );
}

/** 以完整文本覆盖推理（同上）。 */
export function setLiveStreamReasoning(messageId: string, text: string) {
  writeEntry(messageId, (current) =>
    current.reasoningText === text ? current : { ...current, reasoningText: text },
  );
}

/**
 * live 增量分派：把一帧 `RuntimeLiveDelta` 按模块头「不变式 1」写入本 store。
 * - text：追加正文；
 * - reasoning + blockStart：**覆盖**当前推理块——新块首帧若走追加，尾行会把
 *   「推理 → 工具 → 推理」整轮拼成一段（更早的块已定稿在 thread store 里）；
 * - reasoning：追加推理。
 * runtime/stream 通道在事件到达时同步调用（写完由各自的提交节奏兑现到 store）。
 */
export function applyLiveStreamDelta(delta: RuntimeLiveDelta) {
  if (delta.kind === "text") {
    appendLiveStreamText(delta.messageId, delta.text);
    return;
  }
  if (delta.blockStart) {
    setLiveStreamReasoning(delta.messageId, delta.text);
    return;
  }
  appendLiveStreamReasoning(delta.messageId, delta.text);
}

/**
 * 定稿 / 出错 / 中止：丢弃 live 记录，渲染回落到 thread store 的正式文本。
 * 必须在把最终文本写进 store **之后**调用，否则会出现"store 还是旧的、live
 * 又没了"的空窗。
 */
export function clearLiveStreamText(messageId: string) {
  const id = messageId?.trim();
  if (!id || !entries.has(id)) {
    return;
  }
  entries.delete(id);
  emit();
}

export function getLiveStreamEntry(messageId: string): LiveStreamEntry | null {
  return entries.get(messageId?.trim() ?? "") ?? null;
}

export function subscribeLiveStreamText(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** 测试钩子：清空整张表（避免用例间串味）。 */
export function resetLiveStreamTextStore() {
  if (entries.size === 0) {
    return;
  }
  entries.clear();
  emit();
}

// 无 live 记录时的快照：模块级常量，保证 `useSyncExternalStore` 拿到稳定引用
// （每次返回新对象会让 React 判定"变了"，触发死循环式重渲染）。
const NO_ENTRY: LiveStreamEntry | null = null;

const getNoEntrySnapshot = () => NO_ENTRY;

/**
 * 订阅某条消息的 live 文本。返回 null 表示"没有 live 记录"——调用方应回落到
 * thread store 里的文本。返回值只在真正变化时换引用，订阅组件的 memo 不会被打破。
 */
export function useLiveStreamEntry(
  messageId: string | null | undefined,
): LiveStreamEntry | null {
  const key = messageId ?? "";
  const getSnapshot = useCallback(() => entries.get(key) ?? NO_ENTRY, [key]);
  return useSyncExternalStore(
    subscribeLiveStreamText,
    getSnapshot,
    getNoEntrySnapshot,
  );
}
