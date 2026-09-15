// P0-2 随源拆分：由 workspace-thread-state.test.ts 按关注点切分，断言未改动。
import { describe, expect, it } from "vitest";

import {
  applySessionHistoryToThread,
} from "@/lib/workspace-thread-state";
import type { SessionHistoryResponse } from "@/types/runtime";
import { createThread } from "./test-fixtures";

describe("applySessionHistoryToThread", () => {
  it("preserves existing code segments when authoritative history matches a message", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 1,
      history: [
        {
          role: "assistant",
          content: "Merged answer",
        },
      ],
    };

    const nextThread = applySessionHistoryToThread(createThread(), response);

    expect(nextThread.sessionId).toBe("session-1");
    expect(nextThread.messages).toHaveLength(1);
    expect(nextThread.messages[0].segments).toEqual([
      {
        type: "text",
        content: "Merged answer",
      },
      {
        type: "code",
        language: "json",
        title: "Reasoning snapshot",
        code: '{"ok":true}',
      },
    ]);
    expect(nextThread.artifacts[0]?.id).toBe("session-history-session-1");
  });

  it("uses durable message_id from history metadata as ChatMessage.id", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 2,
      history: [
        {
          role: "user",
          content: "rewrite this",
          metadata: {
            message_id: "msg_user_1",
            turn_id: "turn_1",
          },
        },
        {
          role: "assistant",
          content: "done",
          metadata: {
            message_id: "msg_assistant_1",
            turn_id: "turn_1",
          },
        },
      ],
    };

    const nextThread = applySessionHistoryToThread(createThread(), response);

    expect(nextThread.messages).toHaveLength(2);
    expect(nextThread.messages[0].id).toBe("msg_user_1");
    expect(nextThread.messages[0].role).toBe("user");
    expect(nextThread.messages[1].id).toBe("msg_assistant_1");
  });

  it("keeps existing streaming messages when session history is null", () => {
    const response = {
      session_id: "session-1",
      count: 0,
      history: null,
    } as unknown as SessionHistoryResponse;

    const nextThread = applySessionHistoryToThread(createThread(), response);

    expect(nextThread.sessionId).toBe("session-1");
    // null history = 无权威历史：保留当前流式消息（与 history 匹配时的
    // 合并语义一致，避免恢复流程清掉正在渲染的内容）。
    expect(nextThread.messages).toHaveLength(1);
    expect(nextThread.messages[0]?.id).toBe("assistant-existing");
    expect(nextThread.artifacts[0]?.id).toBe("session-history-session-1");
  });

  it("clears stale messages when authoritative history is an empty array (full backtrack)", () => {
    // 回溯到首轮（或截断到 0 条）时后端返回 `{"count":0,"history":[]}`：
    // 这是「服务端确认没有消息」，不是「本次没有权威历史」，消息列表必须清空，
    // 否则界面停留在回滚前的内容，表现为「点了确认回溯没反应」。
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 0,
      history: [],
    };

    const nextThread = applySessionHistoryToThread(createThread(), response);

    expect(nextThread.sessionId).toBe("session-1");
    expect(nextThread.messages).toHaveLength(0);
    expect(nextThread.artifacts[0]?.id).toBe("session-history-session-1");
  });

  it("keeps live-only tool segments when authoritative history matches the message", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 1,
      history: [{ role: "assistant", content: "Merged answer" }],
    };
    const thread = createThread();
    thread.messages[0].segments = [
      ...thread.messages[0].segments,
      {
        type: "tool",
        toolCallId: "call-1",
        name: "read_file",
        status: "finished",
        resultSummary: "src",
      },
    ];

    const nextThread = applySessionHistoryToThread(thread, response);

    // History has no expression for tool evidence; the rendered card must survive
    // the authoritative projection instead of vanishing from the timeline.
    expect(nextThread.messages).toHaveLength(1);
    expect(
      nextThread.messages[0].segments.filter((segment) => segment.type === "tool"),
    ).toEqual([
      {
        type: "tool",
        toolCallId: "call-1",
        name: "read_file",
        status: "finished",
        resultSummary: "src",
      },
    ]);
  });

  it("keeps interrupted live messages that history has not persisted", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 1,
      history: [{ role: "user", content: "interrupt this stream" }],
    };
    const thread = createThread();
    thread.messages = [
      {
        id: "user-1",
        role: "user",
        author: "You",
        label: "draft",
        segments: [{ type: "text", content: "interrupt this stream" }],
      },
      {
        id: "assistant-stopped",
        role: "assistant",
        author: "Runtime stream",
        label: "runtime",
        interrupted: true,
        streaming: false,
        segments: [{ type: "text", content: "Interruptible chunk 1." }],
      },
    ];

    const nextThread = applySessionHistoryToThread(thread, response);

    // The aborted partial answer is not persisted yet; dropping it would make the
    // "Stopped" marker disappear right after the user hits Ctrl+Enter.
    expect(nextThread.messages.map((message) => message.id)).toEqual([
      "user-1",
      "assistant-stopped",
    ]);
    expect(nextThread.messages[1].interrupted).toBe(true);
  });

  it("hides fact ledger and other internal prompt-context messages", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 4,
      history: [
        {
          role: "user",
          content: "continue",
        },
        {
          role: "developer",
          content:
            "Verified fact ledger (authoritative over compacted prose):\n- [execution] shell succeeded",
          metadata: {
            context_stage: "fact_ledger",
            context_snapshot: true,
          },
        },
        {
          role: "assistant",
          content:
            "Verified fact ledger (authoritative over compacted prose):\n- legacy assistant leak",
        },
        {
          role: "assistant",
          content: "real answer",
        },
      ],
    };

    const nextThread = applySessionHistoryToThread(createThread(), response);

    expect(nextThread.messages).toHaveLength(2);
    expect(nextThread.messages[0].role).toBe("user");
    expect(nextThread.messages[0].segments).toEqual([
      { type: "text", content: "continue" },
    ]);
    expect(nextThread.messages[1].role).toBe("assistant");
    expect(nextThread.messages[1].segments).toEqual([
      { type: "text", content: "real answer" },
    ]);
  });

  it("restores persisted related evidence artifacts from session history metadata", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 1,
      history: [
        {
          role: "assistant",
          content: "Recovered answer",
          metadata: {
            workspace_related_artifacts: [
              {
                id: "persisted-agent-chat-response",
                name: "agent-chat-response-agent-route.json",
                path: "runtime/agent-chat-response-agent-route.json",
                summary: "Final response payload persisted with the assistant history.",
                kind: "json",
                language: "json",
                content: {
                  source: "agent_route",
                  kind: "agent",
                  status: "completed",
                },
              },
            ],
          },
        },
      ],
    };

    const nextThread = applySessionHistoryToThread(
      {
        ...createThread(),
        messages: [],
        artifacts: [],
      },
      response,
    );

    expect(nextThread.messages).toHaveLength(1);
    expect(nextThread.messages[0].relatedArtifactIds).toEqual([
      "persisted-history:session-1:0:0:agent-chat-response-agent-route-json",
    ]);
    expect(nextThread.artifacts).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          id: "persisted-history:session-1:0:0:agent-chat-response-agent-route-json",
          name: "agent-chat-response-agent-route.json",
          path: "runtime/agent-chat-response-agent-route.json",
          summary: "Final response payload persisted with the assistant history.",
          kind: "json",
          language: "json",
          content: JSON.stringify(
            {
              source: "agent_route",
              kind: "agent",
              status: "completed",
            },
            null,
            2,
          ),
        }),
      ]),
    );
  });

  it("restores generated images from assistant metadata into inline segments and artifacts", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 1,
      history: [
        {
          role: "assistant",
          content: "Generated image",
          metadata: {
            generated_images: [
              {
                id: "image:1",
                status: "completed",
                revised_prompt: "a tiny robot",
                mime_type: "image/png",
                saved_path: "C:/temp/image_1.png",
                sha256: "abc123",
                byte_count: 42,
              },
            ],
            generated_images_error: "image save warning",
          },
        },
      ],
    };

    const nextThread = applySessionHistoryToThread(
      {
        ...createThread(),
        messages: [],
        artifacts: [],
      },
      response,
    );

    expect(nextThread.messages).toHaveLength(1);
    expect(nextThread.messages[0].relatedArtifactIds).toEqual([
      "generated-image:session-1:image_1",
    ]);
    expect(nextThread.messages[0].segments).toEqual([
      {
        type: "text",
        content: "Generated image",
      },
      {
        type: "image",
        src: expect.stringContaining(
          "/api/runtime/sessions/session-1/generated-images/image_1",
        ),
        alt: "a tiny robot",
        caption: "a tiny robot",
        artifactId: "generated-image:session-1:image_1",
        imageId: "image_1",
      },
      {
        type: "callout",
        title: "图片保存失败",
        tone: "warning",
        content: "image save warning",
      },
    ]);

    expect(nextThread.artifacts).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          id: "generated-image:session-1:image_1",
          kind: "image",
          name: "image_1.png",
          path: "runtime/generated-images/image_1.png",
          content: expect.stringContaining(
            "/api/runtime/sessions/session-1/generated-images/image_1",
          ),
          mimeType: "image/png",
          sha256: "abc123",
          byteCount: 42,
          revisedPrompt: "a tiny robot",
        }),
      ]),
    );
  });

  it("restores reasoning from assistant metadata into the history segment", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 1,
      history: [
        {
          role: "assistant",
          content: "Final answer",
          metadata: {
            reasoning_details: {
              provider: "deepseek",
              visibility: "summary",
              summary: "Because the flag was unset",
            },
          },
        },
      ],
    };

    const nextThread = applySessionHistoryToThread(
      {
        ...createThread(),
        messages: [],
        artifacts: [],
      },
      response,
    );

    expect(nextThread.messages).toHaveLength(1);
    // 推理段在正文段之前：思考过程在上、正式回答在下（页面按段落顺序渲染）。
    expect(nextThread.messages[0].segments).toEqual([
      {
        type: "reasoning",
        content: "Because the flag was unset",
      },
      {
        type: "text",
        content: "Final answer",
      },
    ]);
  });

  it("prefers summary over full content when both exist (backend DisplayText parity)", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 1,
      history: [
        {
          role: "assistant",
          content: "Final answer",
          metadata: {
            reasoning_details: {
              provider: "openai",
              visibility: "full",
              summary: "Short summary",
              content: "Longer detailed reasoning body",
            },
          },
        },
      ],
    };

    const nextThread = applySessionHistoryToThread(
      {
        ...createThread(),
        messages: [],
        artifacts: [],
      },
      response,
    );

    const segments = nextThread.messages[0].segments;
    expect(segments).toHaveLength(2);
    expect(segments[0]).toEqual({
      type: "reasoning",
      content: "Short summary",
    });
    expect(segments[1]).toEqual({
      type: "text",
      content: "Final answer",
    });
  });

  it("does not restore reasoning when visibility is none or opaque", () => {
    const response: SessionHistoryResponse = {
      session_id: "session-1",
      count: 1,
      history: [
        {
          role: "assistant",
          content: "Final answer",
          metadata: {
            reasoning_details: {
              provider: "openai",
              visibility: "none",
              content: "hidden reasoning",
            },
          },
        },
      ],
    };

    const nextThread = applySessionHistoryToThread(
      {
        ...createThread(),
        messages: [],
        artifacts: [],
      },
      response,
    );

    expect(nextThread.messages[0].segments).toEqual([
      {
        type: "text",
        content: "Final answer",
      },
    ]);
  });

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
