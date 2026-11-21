import { describe, expect, it } from "vitest";

import { applySessionHistoryToThread } from "@/lib/workspace-thread-state";
import type { SessionHistoryResponse } from "@/types/runtime";
import { createThread } from "./test-fixtures";

describe("applySessionHistoryToThread todo metadata", () => {
  it("通道 B1：生产形状历史（metadata.tool_metadata.todos）折叠出 thread.todoSnapshot", () => {
    // 复刻实测 /history：工具结果元数据嵌在 tool_metadata 下（不是平铺 metadata.todos），
    // 这条链路一旦错位，刷新页面后任务面板就不显示。
    const response: SessionHistoryResponse = {
      session_id: "session_20260915175131_N2pzqRjJ",
      count: 1,
      history: [
        {
          role: "tool",
          content: "任务列表已更新: 3 待处理, 1 进行中, 1 已完成",
          tool_call_id: "call_00_5NoxfS1mFtbZvAjIds2u4332",
          metadata: {
            tool_name: "todos",
            tool_source: "toolkit",
            tool_metadata: {
              session_id: "session_20260915175131_N2pzqRjJ",
              goal_id: "",
              todos: [
                {
                  content: "初始化测试环境并检查依赖",
                  status: "completed",
                  active_form: "初始化测试环境并检查依赖",
                },
                {
                  content: "编写测试用例骨架",
                  status: "in_progress",
                  active_form: "编写测试用例骨架",
                },
              ],
            },
          },
        },
      ],
    };

    const nextThread = applySessionHistoryToThread(createThread(), response);

    expect(nextThread.todoSnapshot?.source).toBe("history");
    expect(nextThread.todoSnapshot?.sessionId).toBe(
      "session_20260915175131_N2pzqRjJ",
    );
    expect(nextThread.todoSnapshot?.items).toHaveLength(2);
    expect(nextThread.todoSnapshot?.items[1]?.status).toBe("in_progress");
  });
});
