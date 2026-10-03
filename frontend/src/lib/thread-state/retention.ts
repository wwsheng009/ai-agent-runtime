// 会话载荷（messages / artifacts）驻留治理：多会话切换的**有界卸载**。
//
// 背景：线程 store 对「访问过的每个会话」长期保留整份 `messages` 与 `artifacts`
// （后者还包含每回合 JSON 产物与会话历史快照）。浏览器长时间开着、在大量会话间
// 来回切换时，这些载荷只增不减——内存随「访问过的会话数 × 每会话历史长度」线性
// 增长，是前端内存占用最大的结构性来源之一。
//
// 治理口径（不改动任何「当前会话」行为）：
// - 选中会话（含草稿）永不卸载；
// - 在途回合 / 有服务端活动回合的会话（调用方传入 protectedKeys）永不卸载；
// - transport === "error" 的会话保留载荷，便于人工排查与重试，不静默丢现场；
// - 其余空闲会话按最近访问 LRU 保留最近 N 个（默认
//   {@link THREAD_PAYLOAD_LRU_SIZE}）的完整载荷；更久未访问的会话只保留元数据
//   （标题 / 时间 / 标签 / todo 快照），messages 与 artifacts 置空。
//
// 卸载后的回填依赖既有链路：重新选中会话时 `useSessionHistorySync` 会按
// `sessionId` 变化重拉「最新一页」权威历史并写回线程；轨迹 store 池
// （LRU=3）则独立负责轨迹窗口的保留与重建。因此卸载只影响「冷会话」的即开即显，
// 不影响权威数据与恢复路径。
//
// 回滚开关：置 false 退回「全部会话载荷常驻」的旧行为（见
// {@link THREAD_PAYLOAD_RETENTION_ENABLED}）。

import { type Thread } from "@/data/mock";
import { normalizeSessionId } from "@/lib/session-id";

/** 除选中会话外，按最近访问保留完整载荷的会话数（与轨迹池口径同阶）。 */
export const THREAD_PAYLOAD_LRU_SIZE = 4;

/**
 * 回滚开关：会话载荷 LRU 卸载。置 false = 旧行为（访问过的会话载荷全部常驻）。
 * 与 `MULTI_SESSION_REGISTRY_ENABLED` 同形态的常量开关，不引入部署分支。
 */
export const THREAD_PAYLOAD_RETENTION_ENABLED = true;

/** 卸载判定用：会话的归一化键（id 与 sessionId 两种形态；空值剔除）。 */
export function threadRetentionKeys(thread: Thread): string[] {
  const keys = new Set<string>();
  const id = normalizeSessionId(thread.id) || thread.id.trim();
  const sessionId = normalizeSessionId(thread.sessionId) || "";
  if (id) {
    keys.add(id);
  }
  if (sessionId) {
    keys.add(sessionId);
  }
  return [...keys];
}

function hasHeavyPayload(thread: Thread) {
  return thread.messages.length > 0 || thread.artifacts.length > 0;
}

/** 在途回合的消息（流式占位）：该线程即便未被显式保护也不能卸载。 */
function hasStreamingMessage(thread: Thread) {
  return thread.messages.some((message) => message.streaming === true);
}

/** 会话是否不可卸载（选中 / 草稿 / 显式保护 / 错误现场 / 流式在途）。 */
export function isRetainedThread(
  thread: Thread,
  input: {
    protectedKeys: ReadonlySet<string>;
    keepKeys: ReadonlySet<string>;
  },
): boolean {
  const keys = threadRetentionKeys(thread);
  if (keys.some((key) => input.keepKeys.has(key))) {
    return true;
  }
  if (keys.some((key) => input.protectedKeys.has(key))) {
    return true;
  }
  if (thread.status === "draft" || thread.id === "new") {
    return true;
  }
  if (thread.transport === "error") {
    return true;
  }
  return hasStreamingMessage(thread);
}

/**
 * 计算「已卸载的消息/产物」的线程版本：对命中 LRU 之外的冷会话把
 * `messages` / `artifacts` 置空，`todoSnapshot` 等轻量元数据原样保留。
 *
 * 纯函数：无变化时逐项返回原引用；有任何线程被卸载时返回新数组（仅改动命中项）。
 */
export function trimIdleThreadPayloads(input: {
  threads: Thread[];
  selectedThreadId?: string | null;
  /** 最近访问的线程键（新→旧），由调用方按选中变化维护。 */
  recentKeys: readonly string[];
  /** 调用方声明必须保留载荷的键（在途回合 / 服务端活动回合等）。 */
  protectedKeys?: ReadonlySet<string>;
  /** 保留完整载荷的最近会话数（不含选中）。 */
  lruSize?: number;
}): Thread[] {
  const lruSize = Math.max(
    0,
    Math.floor(input.lruSize ?? THREAD_PAYLOAD_LRU_SIZE),
  );
  const selectedKey = input.selectedThreadId
    ? normalizeSessionId(input.selectedThreadId) || input.selectedThreadId.trim()
    : "";
  // recentKeys 含选中项（调用方按「新→旧」维护）；保留窗口按「除选中外的最近
  // lruSize 个」计算，选中项单独加入，避免把窗口名额耗在选中项上。
  const keepKeys = new Set(
    input.recentKeys.filter((key) => key !== selectedKey).slice(0, lruSize),
  );
  if (selectedKey) {
    keepKeys.add(selectedKey);
  }
  const protectedKeys = input.protectedKeys ?? new Set<string>();

  let changed = false;
  const next = input.threads.map((thread) => {
    if (
      isRetainedThread(thread, {
        protectedKeys,
        keepKeys,
      }) ||
      !hasHeavyPayload(thread)
    ) {
      return thread;
    }
    changed = true;
    return {
      ...thread,
      messages: [],
      artifacts: [],
    };
  });

  return changed ? next : input.threads;
}
