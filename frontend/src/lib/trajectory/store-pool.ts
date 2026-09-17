/**
 * TrajectoryStore 池（工作区多会话并发 Batch 1）：按会话键持有多个轨迹快照
 * store，带 LRU 上限与 dispose。
 *
 * 动机（单实例 store 的两个痛点，见 plan §4.1「TrajectoryStore 池」）：
 * 1. 切换会话必须 `reset({ hard: true })` 才能避免新旧会话的游标/快照互相污染，
 *    代价是**切回**原会话要整段重放（尾部优先也至少重新拉一页 + 重建渲染项）；
 * 2. 后台会话如果在跑，其增量仍会写进同一个 store——前台切走后写进去的内容
 *    既渲染不了，也会在 reset 时被整段丢弃（切回只能靠重放找回）。
 *
 * 池化后：
 * - 切换会话 = 取该会话自己的 store（游标、快照、回放日志原样保留）；
 * - 切回未被驱逐的会话 = 直接复用快照，恢复链路走「游标增量补齐」；
 * - 超过容量（默认 {@link DEFAULT_TRAJECTORY_STORE_POOL_SIZE}）的最久未用会话被
 *   dispose（取消挂起帧、清空订阅者），内存占用有界；被驱逐会话再次选中时
 *   按冷启动重建（与旧的「切换即 reset」路径等价）；
 * - `maxStores = 1` 时退化为旧的单实例行为（回滚开关口径）。
 */
import {
  createTrajectoryStore,
  type TrajectoryStore,
} from "@/hooks/workspace/use-trajectory-snapshot";

/** 默认容量：选中会话 + 近两次访问过的会话（方案 §4.6「轨迹 store LRU 默认 3」）。 */
export const DEFAULT_TRAJECTORY_STORE_POOL_SIZE = 3;

/** 无身份键的兜底桶（selectedThread 尚未就绪时的调用口径）。 */
export const ANONYMOUS_TRAJECTORY_STORE_KEY = "__anonymous__";

export type TrajectoryStoreFactory = () => TrajectoryStore;

export interface TrajectoryStorePool {
  /** 取（或新建）键对应的 store，并标记为最近使用；超出容量时驱逐最久未用者。 */
  acquire(key: string): TrajectoryStore;
  /**
   * 按线程身份取 store：同一 `threadId` 的键升级（草稿线程 id → 落库
   * sessionId）在解析时先 `adopt` 旧 store、再 `acquire` 新键。
   *
   * 顺序不能反：先 `acquire` 会在新键上落一个空 store，随后 adopt 因「目标键
   * 已占用」被拒，正在收 SSE 的回合中途切到空快照。`threadId` 为空时退化为
   * `acquire(key)`（匿名桶）。
   */
  acquireForThread(threadId: string, key: string): TrajectoryStore;
  /** 只读查询：不存在返回 undefined（不创建、不改变 LRU 顺序）。 */
  peek(key: string): TrajectoryStore | undefined;
  /**
   * 键迁移（同一逻辑会话的身份升级：草稿线程 id → 落库 sessionId）。
   *
   * 回合进行中会话 id 才由服务端落库，若此时按新键取 store，会把同一次生成
   * 切到另一个空快照上（旧 store 仍在收 SSE 增量，新 store 只能靠重放补齐，
   * 表现为中途闪断/重放）。迁移把旧键下的 store 原样搬到新键，保持身份不变。
   *
   * 返回 false（新键已被占用 / 键相同）时不搬移，旧键保留给调用方处理。
   */
  adopt(fromKey: string, toKey: string): boolean;
  /**
   * 显式重置某键的 store（会话回溯 / 归档 / 分支跳转等破坏性操作）；
   * 键不存在时不创建新 store（返回 false）。
   */
  reset(key: string, options?: { hard?: boolean }): boolean;
  /** 当前驻留的键（least → most recently used 顺序）。 */
  residentKeys(): string[];
  disposeAll(): void;
}

export function createTrajectoryStorePool(options?: {
  maxStores?: number;
  /** 工厂注入（测试用）；默认 `createTrajectoryStore()`。 */
  createStore?: TrajectoryStoreFactory;
}): TrajectoryStorePool {
  const maxStores = Math.max(
    1,
    Math.floor(options?.maxStores ?? DEFAULT_TRAJECTORY_STORE_POOL_SIZE),
  );
  const createStore = options?.createStore ?? (() => createTrajectoryStore());
  // Map 插入顺序 = LRU 顺序（least → most recently used）；acquire 先删后插完成 touch。
  const stores = new Map<string, TrajectoryStore>();
  // 线程身份 → 最近一次解析出的键：识别「同一线程的键升级」用（见 acquireForThread）。
  const threadKeys = new Map<string, string>();

  const normalizeKey = (key: string) => {
    const trimmed = typeof key === "string" ? key.trim() : "";
    return trimmed || ANONYMOUS_TRAJECTORY_STORE_KEY;
  };

  const evictOverflow = () => {
    while (stores.size > maxStores) {
      const oldestKey = stores.keys().next().value as string | undefined;
      if (oldestKey === undefined) {
        return;
      }
      const victim = stores.get(oldestKey);
      stores.delete(oldestKey);
      victim?.dispose();
    }
  };

  const adoptKeys = (fromKey: string, toKey: string): boolean => {
    const from = normalizeKey(fromKey);
    const to = normalizeKey(toKey);
    if (from === to) {
      return true;
    }
    const source = stores.get(from);
    if (!source || stores.has(to)) {
      return false;
    }
    stores.delete(from);
    stores.set(to, source);
    evictOverflow();
    return true;
  };

  const acquireStore = (key: string): TrajectoryStore => {
    const normalizedKey = normalizeKey(key);
    const existing = stores.get(normalizedKey);
    if (existing) {
      // touch：先删后插，把它移到 LRU 尾部。
      stores.delete(normalizedKey);
      stores.set(normalizedKey, existing);
      return existing;
    }
    const created = createStore();
    stores.set(normalizedKey, created);
    evictOverflow();
    return created;
  };

  return {
    acquire: acquireStore,
    acquireForThread: (threadId, key) => {
      const normalizedThreadId = (threadId ?? "").trim();
      const normalizedKey = normalizeKey(key);
      if (normalizedThreadId) {
        const previousKey = threadKeys.get(normalizedThreadId);
        if (previousKey && previousKey !== normalizedKey) {
          // 目标键已被占用（真切换/重放）时 adopt 返回 false，旧键保留。
          adoptKeys(previousKey, normalizedKey);
        }
        threadKeys.set(normalizedThreadId, normalizedKey);
      }
      return acquireStore(normalizedKey);
    },
    peek: (key) => stores.get(normalizeKey(key)),
    adopt: adoptKeys,
    reset: (key, resetOptions) => {
      const store = stores.get(normalizeKey(key));
      if (!store) {
        return false;
      }
      store.reset(resetOptions);
      return true;
    },
    residentKeys: () => [...stores.keys()],
    disposeAll: () => {
      for (const store of stores.values()) {
        store.dispose();
      }
      stores.clear();
      threadKeys.clear();
    },
  };
}
