/**
 * 选中会话的回合视图（工作区多会话并发 Batch 1）。
 *
 * 从 `useWorkspaceAgentChatTurn` 机械拆出的注册表接线：注册表实例自持、按选中
 * 会话解析键、同一线程的键升级做 adopt、`useSyncExternalStore` 订阅该键的
 * 快照（isResponding / activeTurnId / phase / stalled）。Map 尺寸为 1 时与
 * 旧的单值 hook 状态逐字节等价（回滚口径）。
 */
import { useCallback, useState, useSyncExternalStore } from "react";

import { resolveTrajectoryStoreKey } from "@/hooks/workspace/use-trajectory-store-pool";
import type { Thread } from "@/data/mock";
import type { ChatStreamPhase } from "@/types/runtime";

import {
  createSessionTurnRegistry,
  type SessionTurnEntry,
  type SessionTurnRegistry,
} from "./session-turn-registry";

export type SessionTurnView = {
  turnRegistry: SessionTurnRegistry;
  /** 选中会话的 threadKey（sessionId 优先、线程 id 兜底）。 */
  selectedTurnKey: string;
  selectedTurnEntry: SessionTurnEntry | null;
  isResponding: boolean;
  activeTurnId: string | null;
  phase: ChatStreamPhase | null;
  streamStalled: boolean;
  /**
   * 全部在途回合的 threadKey（Batch 3 §4.7：跨会话「运行中」投影）。
   * 切走之后 A 的回合仍在表里，侧栏据此把 A 显示为运行中。
   */
  activeSessionKeys: string[];
  /** 清掉选中会话的静默看门狗标记（手动重试/续传成功后调用）。 */
  clearStreamStall: () => void;
};

export function useSessionTurnView(thread: Thread | undefined): SessionTurnView {
  // 注册表实例惰性自持：`useState` 的初始化器只在首次渲染执行一次，且不在
  // 渲染期读写 ref（react-hooks/refs）；实例在组件生命周期内保持稳定。
  const [turnRegistry] = useState(createSessionTurnRegistry);
  // 同一线程的键升级（草稿线程 id → 服务端 sessionId）由注册表在解析时迁移
  // 条目：与轨迹 store 池的 adopt 口径一致；跨线程切换不迁移（A 的回合不会
  // 被搬到 B 的键上）。迁移先于快照读取，避免「升级那一帧」读到空条目。
  const selectedTurnKey = turnRegistry.resolveThreadKey(
    thread?.id ?? "",
    resolveTrajectoryStoreKey(thread?.sessionId, thread?.id),
  );

  const getTurnSnapshot = useCallback(
    () => turnRegistry.getSnapshot(selectedTurnKey),
    [turnRegistry, selectedTurnKey],
  );
  const { entry, stalled } = useSyncExternalStore(
    turnRegistry.subscribe,
    getTurnSnapshot,
    getTurnSnapshot,
  );
  const clearStreamStall = useCallback(() => {
    turnRegistry.setStalled(selectedTurnKey, false);
  }, [selectedTurnKey, turnRegistry]);

  // 在途回合键集合：注册表快照是"整体版本"，这里压成排序后的字符串，
  // 只有集合内容变化才产生新引用（useSyncExternalStore 的稳定性要求）。
  const getActiveKeysSnapshot = useCallback(
    () => turnRegistry.activeKeys().sort().join("\n"),
    [turnRegistry],
  );
  const activeKeysSnapshot = useSyncExternalStore(
    turnRegistry.subscribe,
    getActiveKeysSnapshot,
    getActiveKeysSnapshot,
  );

  return {
    turnRegistry,
    selectedTurnKey,
    selectedTurnEntry: entry,
    isResponding: entry !== null,
    activeTurnId: entry?.activeTurnId ?? null,
    phase: entry?.phase ?? null,
    streamStalled: stalled,
    activeSessionKeys: activeKeysSnapshot ? activeKeysSnapshot.split("\n") : [],
    clearStreamStall,
  };
}
