// 前端「当前任务」面板（composer 上沿浮动列表）的纯派生层。
//
// 数据来源与方案对齐（docs/plan/frontend-composer-floating-task-panel-plan.md §5.2/§5.3）：
// - 通道 A（实时）：`tool.completed` 的 `payload.protocol_result.metadata.todo_snapshot`
//   全量小视图（items 只含 content/status/active_form）；
// - 通道 B1（刷新 / 重开）：已落盘会话历史 `metadata.todos` 原文（会话历史产物逐字保留）。
//
// 本模块不发请求、不持有状态：只做解析与「取最新一次」。坏 JSON / 缺字段 / 未知状态一律
// 降级为 null（不臆造、不跨会话合并、不解析文本摘要）。

import { type SessionHistoryMessage, type SessionRuntimeEvent } from "@/types/runtime";

import { getRuntimeEventSeq } from "./sessions";

export type TodoStatus = "pending" | "in_progress" | "completed";

export type TodoItem = {
  content: string;
  status: TodoStatus;
  /** 执行态文案（后端契约 required，但历史原文可能缺省；展示层回退 content）。 */
  activeForm: string;
};

/** runtime = 实时事件通道；history = 会话历史回放（冷启动兜底）。 */
export type TodoSnapshotSource = "runtime" | "history";

export type TodoSnapshot = {
  items: TodoItem[];
  source: TodoSnapshotSource;
  sessionId: string;
  /** 归属 goal（子代理/目标级列表与主会话列表不同源）。 */
  goalId: string;
  /** 事件序号（runtime 通道取 `payload.seq`）；history 无法比较时记 0。 */
  seq: number;
};

export type TodoCounts = {
  total: number;
  pending: number;
  inProgress: number;
  completed: number;
};

const TODO_STATUSES: readonly TodoStatus[] = [
  "pending",
  "in_progress",
  "completed",
];

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function readText(value: unknown) {
  return typeof value === "string" ? value.trim() : "";
}

export function normalizeTodoStatus(value: unknown): TodoStatus | null {
  const status = readText(value);
  return (TODO_STATUSES as readonly string[]).includes(status)
    ? (status as TodoStatus)
    : null;
}

/**
 * 逐项校验 `content/status/active_form`：坏项丢弃、缺 active_form 记空串。
 * 返回 null 表示「载荷本身不可用」（不是数组，或整组没有一条合法项）——
 * 此时调用方必须保留原快照，而不是用空列表覆盖。
 */
export function parseTodoItems(value: unknown): TodoItem[] | null {
  if (!Array.isArray(value)) {
    return null;
  }
  const items: TodoItem[] = [];
  for (const raw of value) {
    if (!isRecord(raw)) {
      continue;
    }
    const content = readText(raw.content);
    const status = normalizeTodoStatus(raw.status);
    if (!content || !status) {
      continue;
    }
    items.push({ content, status, activeForm: readText(raw.active_form) });
  }
  if (items.length === 0 && value.length > 0) {
    return null;
  }
  return items;
}

function parseTodoSnapshotValue(
  value: unknown,
  origin: Omit<TodoSnapshot, "items">,
): TodoSnapshot | null {
  if (!isRecord(value)) {
    return null;
  }
  const items = parseTodoItems(value.items);
  if (!items) {
    return null;
  }
  return {
    items,
    source: origin.source,
    sessionId: readText(value.session_id) || origin.sessionId,
    goalId: readText(value.goal_id) || origin.goalId,
    seq: origin.seq,
  };
}

/**
 * 通道 A：从 `tool.completed` 事件取结构化快照。
 *
 * 只认 `protocol_result.metadata.todo_snapshot`（后端按工具作用域放行的裁剪视图）；
 * 不做工具名特判——键存在即数据，缺失即与 todo 无关的事件。
 */
export function deriveTodoSnapshotFromRuntimeEvent(
  event: SessionRuntimeEvent | null | undefined,
): TodoSnapshot | null {
  if (!event) {
    return null;
  }
  const payload = isRecord(event.payload) ? event.payload : null;
  const protocol = payload && isRecord(payload.protocol_result)
    ? payload.protocol_result
    : null;
  const metadata = protocol && isRecord(protocol.metadata)
    ? protocol.metadata
    : null;
  if (!metadata) {
    return null;
  }
  return parseTodoSnapshotValue(metadata.todo_snapshot, {
    source: "runtime",
    sessionId: readText(event.session_id),
    goalId: "",
    seq: getRuntimeEventSeq(event),
  });
}

/** 事件缓冲内取 seq 最大者（无 seq 时按到达顺序后者胜）。 */
export function deriveLatestTodosFromRuntimeEvents(
  events: readonly SessionRuntimeEvent[] | null | undefined,
): TodoSnapshot | null {
  if (!Array.isArray(events) || events.length === 0) {
    return null;
  }
  let latest: TodoSnapshot | null = null;
  for (const event of events) {
    const snapshot = deriveTodoSnapshotFromRuntimeEvent(event);
    if (!snapshot) {
      continue;
    }
    if (!latest || snapshot.seq >= latest.seq) {
      latest = snapshot;
    }
  }
  return latest;
}

/**
 * 通道 B1：从会话历史消息里取最近一次工具结果里的 todos 原文。
 *
 * 生产侧（`GET /api/runtime/sessions/{id}/history` 与 `session-history-{id}` 产物同源）
 * 把工具结果元数据整体嵌在 `metadata.tool_metadata` 下，todos 数组是该包的 `todos` 键，
 * 与实时通道 `protocol_result.metadata.todo_snapshot` 同源同形；因此以嵌套包为准，
 * 仅在它缺席时回落到平铺形状 `metadata.todos`（旧写入方/旁路载荷）。
 *
 * 历史页是「尾部优先」的一页，倒序扫描即最近一次；坏条目跳过并继续向前找，
 * 而不是整体降级（历史里旧快照仍可能可用）。
 */
export function deriveLatestTodosFromHistoryMessages(
  history: readonly SessionHistoryMessage[] | null | undefined,
): TodoSnapshot | null {
  if (!Array.isArray(history) || history.length === 0) {
    return null;
  }
  for (let index = history.length - 1; index >= 0; index -= 1) {
    const message = history[index];
    const metadata = message && isRecord(message.metadata) ? message.metadata : null;
    if (!metadata) {
      continue;
    }
    const bag = isRecord(metadata.tool_metadata) ? metadata.tool_metadata : metadata;
    if (!("todos" in bag)) {
      continue;
    }
    const items = parseTodoItems(bag.todos);
    if (!items) {
      continue;
    }
    return {
      items,
      source: "history",
      sessionId: readText(bag.session_id),
      goalId: readText(bag.goal_id),
      seq: 0,
    };
  }
  return null;
}

/** 会话历史产物（`session-history-{id}`）原文 → 历史消息数组；坏 JSON 降级 null。 */
export function parseSessionHistoryContent(
  content: string | null | undefined,
): SessionHistoryMessage[] | null {
  if (typeof content !== "string" || !content.trim()) {
    return null;
  }
  try {
    const parsed: unknown = JSON.parse(content);
    if (!isRecord(parsed) || !Array.isArray(parsed.history)) {
      return null;
    }
    return parsed.history as SessionHistoryMessage[];
  } catch {
    return null;
  }
}

/**
 * 单入口派生：live 通道优先，缺省时回落到已保留的历史原文。
 * 两者都没有 → null（面板不渲染，不显示过期数据）。
 */
export function deriveLatestTodos(
  events: readonly SessionRuntimeEvent[] | null | undefined,
  historyContent?: string | null,
): TodoSnapshot | null {
  const live = deriveLatestTodosFromRuntimeEvents(events);
  if (live) {
    return live;
  }
  return deriveLatestTodosFromHistoryMessages(
    parseSessionHistoryContent(historyContent),
  );
}

/**
 * 快照合并：runtime 按 seq 单调推进；history 只作冷启动兜底。
 *
 * - 已有 runtime 快照时，history 不覆盖（历史同步是「最近一页」，不一定比 live 新）；
 * - history 之间允许后写胜（新的历史页替换旧的兜底值）；
 * - incoming 为 null（本轮没取到）时保持原值，不清空已知快照。
 */
export function mergeTodoSnapshot(
  current: TodoSnapshot | null | undefined,
  incoming: TodoSnapshot | null,
): TodoSnapshot | null {
  const existing = current ?? null;
  if (!incoming) {
    return existing;
  }
  if (!existing) {
    return incoming;
  }
  if (incoming.source === "runtime") {
    if (existing.source === "runtime" && incoming.seq < existing.seq) {
      return existing;
    }
    return incoming;
  }
  return existing.source === "runtime" ? existing : incoming;
}

/**
 * 线程投影里承载快照的最小结构约束。
 * 用结构类型而不是 `import type { Thread }`：`data/mock` 反向依赖本模块的类型，
 * 直接引用会形成循环。
 */
type TodoSnapshotCarrier = { todoSnapshot?: TodoSnapshot | null };

/**
 * 事件 → 线程投影的通道 A 折叠（调用点见 `lib/thread-state/events.ts`）。
 *
 * 与 todo 无关的事件**原样返回入参引用**：这个函数跑在每条事件的公共路径上，
 * 不产生新对象才不会让下游的 memo 全部失效。
 */
export function applyTodoSnapshotToThread<T extends TodoSnapshotCarrier>(
  thread: T,
  event: SessionRuntimeEvent | null | undefined,
): T {
  const incoming = deriveTodoSnapshotFromRuntimeEvent(event);
  if (!incoming) {
    return thread;
  }
  const todoSnapshot = mergeTodoSnapshot(thread.todoSnapshot, incoming);
  return todoSnapshot === thread.todoSnapshot ? thread : { ...thread, todoSnapshot };
}

export function countTodos(items: readonly TodoItem[]): TodoCounts {
  const counts: TodoCounts = {
    total: items.length,
    pending: 0,
    inProgress: 0,
    completed: 0,
  };
  for (const item of items) {
    if (item.status === "completed") {
      counts.completed += 1;
    } else if (item.status === "in_progress") {
      counts.inProgress += 1;
    } else {
      counts.pending += 1;
    }
  }
  return counts;
}

/** 当前进行项（本项目同一时刻最多一条 in_progress，见 todos.go 的软修复契约）。 */
export function currentTodoItem(items: readonly TodoItem[]): TodoItem | null {
  return items.find((item) => item.status === "in_progress") ?? null;
}

/** 单项展示文案：优先执行态文案，缺省回退任务描述。 */
export function todoItemLabel(item: TodoItem): string {
  return item.activeForm || item.content;
}

/**
 * 可见性策略（方案 §6.3 / §8.2 第 3 条，Q1 默认档）：
 * - 无快照 / 空列表 → 不渲染（不占位）；
 * - 全部完成 → 立即隐藏（retention = 0），避免「已完成」噪声常驻。
 */
export function isTodoPanelVisible(snapshot: TodoSnapshot | null | undefined) {
  if (!snapshot || snapshot.items.length === 0) {
    return false;
  }
  return snapshot.items.some((item) => item.status !== "completed");
}
