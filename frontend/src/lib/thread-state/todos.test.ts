// 「当前任务」面板派生层单测（方案 §5.2 / §5.3 / §8.2）。
//
// 断言只依赖后端原始字段（status / seq / metadata 键名），不绑定任何 UI 文案，
// 这样文案调整不会误伤数据通道的回归覆盖。

import { describe, expect, it } from "vitest";

import type { SessionHistoryMessage, SessionRuntimeEvent } from "@/types/runtime";

import {
  countTodos,
  currentTodoItem,
  deriveLatestTodos,
  deriveLatestTodosFromHistoryMessages,
  deriveLatestTodosFromRuntimeEvents,
  deriveTodoSnapshotFromRuntimeEvent,
  isTodoPanelVisible,
  mergeTodoSnapshot,
  normalizeTodoStatus,
  parseSessionHistoryContent,
  parseTodoItems,
  todoItemLabel,
  type TodoSnapshot,
} from "./todos";

function runtimeEvent(
  seq: number,
  items: unknown,
  overrides: Partial<SessionRuntimeEvent> = {},
): SessionRuntimeEvent {
  return {
    type: "tool.completed",
    session_id: "session-1",
    timestamp: "2026-09-15T10:00:00Z",
    payload: {
      seq,
      protocol_result: {
        metadata: {
          todo_snapshot: { session_id: "session-1", items },
        },
      },
    },
    ...overrides,
  };
}

function historyMessage(
  todos: unknown,
  overrides: Partial<SessionHistoryMessage> = {},
): SessionHistoryMessage {
  return {
    role: "tool",
    content: "{}",
    // 生产形状：工具结果元数据整体嵌在 `metadata.tool_metadata` 下
    // （实测 `GET /api/runtime/sessions/{id}/history` 的 tool 消息）。
    metadata: {
      tool_name: "todos",
      tool_metadata: { todos, session_id: "session-1", goal_id: "" },
    },
    ...overrides,
  };
}

describe("todos 解析层", () => {
  it("parseTodoItems 逐项校验：坏项丢弃、缺 active_form 记空串", () => {
    const items = parseTodoItems([
      { content: "  写实现  ", status: "in_progress", active_form: "正在写实现" },
      { content: "缺状态" },
      { content: "", status: "pending" },
      null,
      "字符串不是项",
      { content: "待补文案", status: "completed" },
    ]);

    expect(items).toEqual([
      { content: "写实现", status: "in_progress", activeForm: "正在写实现" },
      { content: "待补文案", status: "completed", activeForm: "" },
    ]);
  });

  it("非数组 → null；空数组 → 空列表（清空是合法语义）", () => {
    expect(parseTodoItems(undefined)).toBeNull();
    expect(parseTodoItems({ content: "x" })).toBeNull();
    expect(parseTodoItems([])).toEqual([]);
  });

  it("数组非空但全部非法 → null，调用方据此保留旧快照", () => {
    expect(parseTodoItems([{ content: "没有状态" }, { status: "pending" }])).toBeNull();
  });

  it("normalizeTodoStatus 只认三个契约值", () => {
    expect(normalizeTodoStatus("pending")).toBe("pending");
    expect(normalizeTodoStatus(" in_progress ")).toBe("in_progress");
    expect(normalizeTodoStatus("done")).toBeNull();
    expect(normalizeTodoStatus(undefined)).toBeNull();
  });
});

describe("todos 通道 A（runtime 事件）", () => {
  it("从 protocol_result.metadata.todo_snapshot 取快照，seq 来自 payload", () => {
    const snapshot = deriveTodoSnapshotFromRuntimeEvent(
      runtimeEvent(7, [{ content: "任务", status: "pending" }]),
    );

    expect(snapshot).toEqual({
      items: [{ content: "任务", status: "pending", activeForm: "" }],
      source: "runtime",
      sessionId: "session-1",
      goalId: "",
      seq: 7,
    });
  });

  it("与 todo 无关的事件一律 null（不做工具名特判）", () => {
    expect(deriveTodoSnapshotFromRuntimeEvent(null)).toBeNull();
    expect(
      deriveTodoSnapshotFromRuntimeEvent({
        type: "assistant.delta",
        session_id: "session-1",
        timestamp: "2026-09-15T10:00:00Z",
        payload: { seq: 1, delta: "hi" },
      }),
    ).toBeNull();
  });

  it("取 seq 最大的一次，乱序到达不倒退", () => {
    const snapshot = deriveLatestTodosFromRuntimeEvents([
      runtimeEvent(9, [{ content: "新", status: "in_progress" }]),
      runtimeEvent(3, [{ content: "旧", status: "pending" }]),
      runtimeEvent(5, [{ content: "中", status: "pending" }]),
    ]);

    expect(snapshot?.seq).toBe(9);
    expect(snapshot?.items[0]?.content).toBe("新");
  });
});

describe("todos 通道 B1（会话历史原文）", () => {
  it("倒序取最近一条 metadata.tool_metadata.todos，坏条目继续向前找", () => {
    const snapshot = deriveLatestTodosFromHistoryMessages([
      historyMessage([{ content: "较早", status: "pending" }]),
      historyMessage([{ content: "坏的", status: "unknown" }]),
      { role: "assistant", content: "无 metadata" },
    ]);

    expect(snapshot?.source).toBe("history");
    expect(snapshot?.items[0]?.content).toBe("较早");
  });

  it("回归：真实历史载荷（metadata.tool_metadata.todos）能被识别", () => {
    // 复刻 2026-09-15 `session_20260915175131_N2pzqRjJ` 实测 /history 的 tool 消息：
    // 工具结果元数据嵌在 tool_metadata 下，平铺读取会永远取不到（面板不显示）。
    const snapshot = deriveLatestTodosFromHistoryMessages([
      {
        role: "tool",
        content: "任务列表已更新: 3 待处理, 1 进行中, 1 已完成",
        tool_call_id: "call_00_5NoxfS1mFtbZvAjIds2u4332",
        metadata: {
          tool_name: "todos",
          tool_source: "toolkit",
          ok: true,
          tool_metadata: {
            total: 5,
            pending: 3,
            in_progress: 1,
            completed: 1,
            storage_mode: "file",
            session_id: "session_20260915175131_N2pzqRjJ",
            goal_id: "",
            todos: [
              {
                active_form: "初始化测试环境并检查依赖",
                content: "初始化测试环境并检查依赖",
                created_at: 1789465916,
                status: "completed",
                updated_at: 1789465916,
              },
              {
                active_form: "编写测试用例骨架",
                content: "编写测试用例骨架",
                created_at: 1789465916,
                status: "in_progress",
                updated_at: 1789465916,
              },
              {
                active_form: "运行单元测试并收集结果",
                content: "运行单元测试并收集结果",
                created_at: 1789465916,
                status: "pending",
                updated_at: 1789465916,
              },
              {
                active_form: "修复失败用例并复测",
                content: "修复失败用例并复测",
                created_at: 1789465916,
                status: "pending",
                updated_at: 1789465916,
              },
              {
                active_form: "产出测试报告",
                content: "产出测试报告",
                created_at: 1789465916,
                status: "pending",
                updated_at: 1789465916,
              },
            ],
          },
        },
      },
    ]);

    expect(snapshot?.source).toBe("history");
    expect(snapshot?.sessionId).toBe("session_20260915175131_N2pzqRjJ");
    expect(snapshot?.items).toHaveLength(5);
    expect(snapshot?.items.map((item) => item.status)).toEqual([
      "completed",
      "in_progress",
      "pending",
      "pending",
      "pending",
    ]);
    expect(isTodoPanelVisible(snapshot)).toBe(true);
  });

  it("兼容平铺形状 metadata.todos（旧写入方/旁路载荷）", () => {
    const snapshot = deriveLatestTodosFromHistoryMessages([
      historyMessage([], {
        metadata: {
          todos: [{ content: "平铺", status: "pending" }],
          goal_id: "goal-1",
        },
      }),
    ]);

    expect(snapshot?.items[0]?.content).toBe("平铺");
    expect(snapshot?.goalId).toBe("goal-1");
  });

  it("tool_metadata 存在但无 todos 键 → 跳过该条，不误读其它工具元数据", () => {
    expect(
      deriveLatestTodosFromHistoryMessages([
        historyMessage([], {
          metadata: {
            tool_name: "bash",
            todos: [{ content: "不该被读到", status: "pending" }],
            tool_metadata: { exit_code: 0 },
          },
        }),
      ]),
    ).toBeNull();
  });

  it("没有 todos 载荷 → null", () => {
    expect(deriveLatestTodosFromHistoryMessages([])).toBeNull();
    expect(deriveLatestTodosFromHistoryMessages([{ role: "user", content: "hi" }])).toBeNull();
  });

  it("parseSessionHistoryContent 只接受 { history: [...] }，坏 JSON 降级 null", () => {
    const history = [{ role: "tool", content: "{}" }];
    expect(parseSessionHistoryContent(JSON.stringify({ history }))).toEqual(history);
    expect(parseSessionHistoryContent("{ 坏 JSON")).toBeNull();
    expect(parseSessionHistoryContent(JSON.stringify({ other: [] }))).toBeNull();
    expect(parseSessionHistoryContent("")).toBeNull();
  });

  it("deriveLatestTodos：live 通道优先，缺失时回落历史原文", () => {
    const live = deriveLatestTodos(
      [runtimeEvent(2, [{ content: "实时", status: "in_progress" }])],
      JSON.stringify({ history: [historyMessage([{ content: "历史", status: "pending" }])] }),
    );
    expect(live?.source).toBe("runtime");

    const fallback = deriveLatestTodos(
      [],
      JSON.stringify({ history: [historyMessage([{ content: "历史", status: "pending" }])] }),
    );
    expect(fallback?.source).toBe("history");
    expect(deriveLatestTodos([], null)).toBeNull();
  });
});

describe("todos 快照合并", () => {
  const runtimeSnapshot = (seq: number, content: string): TodoSnapshot => ({
    items: [{ content, status: "in_progress", activeForm: "" }],
    source: "runtime",
    sessionId: "session-1",
    goalId: "",
    seq,
  });

  const historySnapshot = (content: string): TodoSnapshot => ({
    items: [{ content, status: "pending", activeForm: "" }],
    source: "history",
    sessionId: "",
    goalId: "",
    seq: 0,
  });

  it("runtime 按 seq 单调推进，旧事件不覆盖新快照", () => {
    const current = runtimeSnapshot(9, "新");
    expect(mergeTodoSnapshot(current, runtimeSnapshot(4, "旧"))).toBe(current);
    expect(mergeTodoSnapshot(current, runtimeSnapshot(11, "更新"))?.items[0]?.content).toBe("更新");
  });

  it("history 不覆盖 runtime；history 之间后写胜", () => {
    const current = runtimeSnapshot(1, "实时");
    expect(mergeTodoSnapshot(current, historySnapshot("历史"))).toBe(current);
    expect(
      mergeTodoSnapshot(historySnapshot("旧历史"), historySnapshot("新历史"))?.items[0]?.content,
    ).toBe("新历史");
  });

  it("incoming 为 null 时保持原值（不清空已知快照）", () => {
    const current = runtimeSnapshot(1, "实时");
    expect(mergeTodoSnapshot(current, null)).toBe(current);
    expect(mergeTodoSnapshot(null, null)).toBeNull();
  });
});

describe("todos 视图派生", () => {
  const items = parseTodoItems([
    { content: "甲", status: "completed", active_form: "完成了甲" },
    { content: "乙", status: "in_progress", active_form: "正在做乙" },
    { content: "丙", status: "pending" },
  ])!;

  it("countTodos 统计四类计数", () => {
    expect(countTodos(items)).toEqual({ total: 3, pending: 1, inProgress: 1, completed: 1 });
  });

  it("currentTodoItem / todoItemLabel 取进行项并优先执行态文案", () => {
    const current = currentTodoItem(items);
    expect(current?.content).toBe("乙");
    expect(current ? todoItemLabel(current) : "").toBe("正在做乙");
    // 缺 active_form 时回退任务描述，避免折叠条只显示空白。
    expect(todoItemLabel(items[2]!)).toBe("丙");
    expect(currentTodoItem([])).toBeNull();
  });

  it("可见性：无快照 / 空列表 / 全部完成 → 不渲染", () => {
    const emptySnapshot: TodoSnapshot = {
      items: [],
      source: "history",
      sessionId: "",
      goalId: "",
      seq: 0,
    };
    expect(isTodoPanelVisible(null)).toBe(false);
    expect(isTodoPanelVisible(undefined)).toBe(false);
    expect(isTodoPanelVisible(emptySnapshot)).toBe(false);
    expect(
      isTodoPanelVisible({
        ...emptySnapshot,
        items: parseTodoItems([{ content: "完成", status: "completed" }])!,
      }),
    ).toBe(false);
    expect(
      isTodoPanelVisible({
        ...emptySnapshot,
        items: parseTodoItems([
          { content: "完成", status: "completed" },
          { content: "待办", status: "pending" },
        ])!,
      }),
    ).toBe(true);
  });
});
