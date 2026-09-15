import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import type { Artifact, ChatMessage } from "@/data/mock";

import { MessageList } from "./message-list";

describe("MessageList", () => {
  it("renders the message timeline as an accessible log while streaming", () => {
    const messages: ChatMessage[] = [
      {
        id: "user-1",
        role: "user",
        author: "You",
        label: "prompt",
        segments: [
          {
            type: "text",
            content: "Summarize the latest checkpoint.",
          },
        ],
      },
      {
        id: "assistant-1",
        role: "assistant",
        author: "Runtime stream",
        label: "streaming",
        segments: [
          {
            type: "text",
            content: "Checkpoint summary is still streaming",
          },
        ],
      },
    ];
    const artifacts: Artifact[] = [];

    const markup = renderToStaticMarkup(
      <MessageList
        artifacts={artifacts}
        isResponding
        messages={messages}
        onSelectArtifact={() => {}}
      />,
    );

    expect(markup).toContain('role="log"');
    expect(markup).toContain('aria-relevant="additions text"');
    expect(markup).toContain('aria-busy="true"');
    expect(markup).toContain('aria-label="Workspace conversation timeline"');
    expect(markup).toContain('aria-labelledby="assistant-1-label"');
    expect(markup).toContain('aria-describedby="assistant-1-meta assistant-1-status"');
    expect(markup).toContain('aria-posinset="2"');
    expect(markup).toContain('id="assistant-1-label"');
    expect(markup).toContain('id="assistant-1-meta"');
    expect(markup).toContain('id="assistant-1-status"');
    expect(markup).toContain("响应流式输出中");
    expect(markup).toContain('role="status"');
    expect(markup).toContain("Runtime stream active");
  });

  it("renders backtrack and inline edit actions on user messages when enabled", () => {
    const messages: ChatMessage[] = [
      {
        id: "user-1",
        role: "user",
        author: "You",
        label: "prompt",
        segments: [
          {
            type: "text",
            content: "Rewrite from here",
          },
        ],
      },
    ];

    const markup = renderToStaticMarkup(
      <MessageList
        artifacts={[]}
        canBacktrack
        isResponding={false}
        messages={messages}
        onBacktrackToMessage={() => {}}
        onSelectArtifact={() => {}}
      />,
    );

    expect(markup).toContain("回溯");
    expect(markup).toContain("编辑");
    expect(markup).toContain('aria-label="回溯到该用户轮次"');
    expect(markup).toContain('aria-label="在回溯前编辑该用户轮次"');
  });

  it("highlights the selected user turn during keyboard backtrack navigation", () => {
    const messages: ChatMessage[] = [
      {
        id: "user-1",
        role: "user",
        author: "You",
        label: "prompt",
        segments: [{ type: "text", content: "older" }],
      },
      {
        id: "user-2",
        role: "user",
        author: "You",
        label: "prompt",
        segments: [{ type: "text", content: "newer" }],
      },
    ];

    const markup = renderToStaticMarkup(
      <MessageList
        artifacts={[]}
        backtrackNavigationActive
        backtrackSelectedMessageId="user-2"
        canBacktrack
        isResponding={false}
        messages={messages}
        onBacktrackToMessage={() => {}}
        onSelectBacktrackNavigationMessage={() => {}}
        onSelectArtifact={() => {}}
      />,
    );

    expect(markup).toContain("回溯导航已激活");
    expect(markup).toContain('aria-current="true"');
    expect(markup).toContain('data-backtrack-selected="true"');
    // A3：元数据不上屏（§5.2），选中态改由 sr-only 标记 + aria-current 表达。
    expect(markup).toContain("已选中这条用户轮次");
  });

  it("renders history tool receipts as tool rows instead of context injection", () => {
    const messages: ChatMessage[] = [
      {
        id: "tool-1",
        role: "assistant",
        author: "Tool receipt",
        label: "tool",
        segments: [
          {
            type: "tool",
            toolCallId: "call-1",
            name: "read_file",
            status: "finished",
            resultSummary: "42 行",
            details: { filePath: "frontend/src/app.tsx" },
          },
        ],
      },
    ];

    const markup = renderToStaticMarkup(
      <MessageList
        artifacts={[]}
        isResponding={false}
        messages={messages}
        onSelectArtifact={() => {}}
      />,
    );

    // 标题是工具名而不是「上下文注入」，摘要带出具体路径（§5.5 / §8.5）。
    expect(markup).toContain("read_file");
    expect(markup).toContain("frontend/src/app.tsx");
    expect(markup).toContain('data-tool-row-status="finished"');
    expect(markup).toContain('data-chat-flow-kind="tool-call"');
    expect(markup).not.toContain("上下文注入");
  });

  it("§12.1.4：无可见内容的助手消息不产出 article（不占 gap 空行）", () => {
    const messages: ChatMessage[] = [
      {
        id: "user-1",
        role: "user",
        author: "You",
        label: "prompt",
        segments: [{ type: "text", content: "跑一下测试" }],
      },
      {
        // 流式首块到达前的空壳：没有段落，也没有用量 / 关联产物。
        id: "assistant-1",
        role: "assistant",
        author: "Runtime stream",
        label: "streaming",
        segments: [],
      },
    ];

    const markup = renderToStaticMarkup(
      <MessageList
        artifacts={[]}
        isResponding
        messages={messages}
        onSelectArtifact={() => {}}
      />,
    );

    expect(markup).toContain('data-message-id="user-1"');
    expect(markup).not.toContain('data-message-id="assistant-1"');
    // 行序号按实际渲染出的消息重新计数（跳过空壳后只剩 1 条）。
    expect(markup).toContain('aria-posinset="1"');
    expect(markup).toContain('aria-setsize="1"');
  });

  it("falls back to the context row for legacy tool receipts without tool segments", () => {
    const messages: ChatMessage[] = [
      {
        id: "tool-legacy",
        role: "assistant",
        author: "Tool receipt",
        label: "tool",
        segments: [{ type: "text", content: "# AGENTS.md\n项目约定…" }],
      },
    ];

    const markup = renderToStaticMarkup(
      <MessageList
        artifacts={[]}
        isResponding={false}
        messages={messages}
        onSelectArtifact={() => {}}
      />,
    );

    expect(markup).toContain("上下文注入");
    expect(markup).not.toContain("read_file");
  });

  it("announces rich-segment and related-artifact fallbacks as status updates", () => {
    const messages: ChatMessage[] = [
      {
        id: "assistant-2",
        role: "assistant",
        author: "Runtime stream",
        label: "artifacts",
        relatedArtifactIds: ["artifact-1"],
        segments: [
          {
            type: "code",
            language: "ts",
            title: "Streaming code",
            code: "export const runtime = true;",
          },
        ],
      },
    ];
    const artifacts: Artifact[] = [
      {
        id: "artifact-1",
        name: "runtime-summary.md",
        path: "runtime/runtime-summary.md",
        summary: "Streaming summary snapshot",
        kind: "code",
        language: "ts",
        content: "export const runtime = true;",
      },
    ];

    const markup = renderToStaticMarkup(
      <MessageList
        artifacts={artifacts}
        isResponding={false}
        messages={messages}
        onSelectArtifact={() => {}}
      />,
    );

    expect(markup).toContain('role="status"');
    expect(markup).toContain("正在加载 代码块");
    expect(markup).toContain("正在加载 1 条相关证据");
  });

  it("surfaces a non-online connection status at the stream tail with manual retry", () => {
    const markup = renderToStaticMarkup(
      <MessageList
        artifacts={[]}
        connectionStatus="offline"
        isResponding={false}
        messages={[]}
        onRetryConnection={() => {}}
        onSelectArtifact={() => {}}
      />,
    );

    expect(markup).toContain('data-connection-status="offline"');
    expect(markup).toContain("连接中断");
    expect(markup).toContain("重试");
  });

  it("keeps the online and idle connection states out of the stream tail", () => {
    for (const status of ["online", "idle"] as const) {
      const markup = renderToStaticMarkup(
        <MessageList
          artifacts={[]}
          connectionStatus={status}
          isResponding={false}
          messages={[]}
          onRetryConnection={() => {}}
          onSelectArtifact={() => {}}
        />,
      );

      expect(markup).not.toContain("data-connection-status");
      expect(markup).not.toContain("重试");
    }
  });

  it("renders branch entries on every completed turn tail, not only the transcript tail", () => {
    const messages: ChatMessage[] = [
      {
        id: "user-1",
        role: "user",
        author: "You",
        label: "prompt",
        segments: [{ type: "text", content: "first question" }],
      },
      {
        id: "answer-1",
        role: "assistant",
        author: "Runtime",
        label: "answer",
        segments: [{ type: "text", content: "first answer" }],
      },
      {
        id: "user-2",
        role: "user",
        author: "You",
        label: "prompt",
        segments: [{ type: "text", content: "second question" }],
      },
      {
        id: "answer-2",
        role: "assistant",
        author: "Runtime",
        label: "answer",
        segments: [{ type: "text", content: "second answer" }],
      },
    ];

    const markup = renderToStaticMarkup(
      <MessageList
        artifacts={[]}
        isResponding={false}
        messages={messages}
        onBranchFromMessage={() => {}}
        onSelectArtifact={() => {}}
      />,
    );

    // 两个已完成轮次各一个锚点；不存在「可见但不可用」的常驻按钮。
    expect(markup.split('data-branch-state="available"').length - 1).toBe(2);
    expect(markup).not.toContain('data-branch-state="unavailable"');
  });

  it("drops branch entries while a pending interaction keeps the turn open", () => {
    const messages: ChatMessage[] = [
      {
        id: "user-1",
        role: "user",
        author: "You",
        label: "prompt",
        segments: [{ type: "text", content: "run the plan" }],
      },
      {
        id: "answer-1",
        role: "assistant",
        author: "Runtime",
        label: "answer",
        segments: [{ type: "text", content: "waiting for approval" }],
      },
    ];

    const markup = renderToStaticMarkup(
      <MessageList
        artifacts={[]}
        hasPendingApproval
        isResponding={false}
        messages={messages}
        onBranchFromMessage={() => {}}
        onSelectArtifact={() => {}}
      />,
    );

    expect(markup).not.toContain("data-branch-state");
  });

  it("never renders a branch entry on a reasoning-only turn tail", () => {
    const messages: ChatMessage[] = [
      {
        id: "user-1",
        role: "user",
        author: "You",
        label: "prompt",
        segments: [{ type: "text", content: "think first" }],
      },
      {
        id: "reasoning-1",
        role: "assistant",
        author: "Runtime",
        label: "reasoning",
        segments: [{ type: "reasoning", content: "先盘点入口文件" }],
      },
    ];

    const markup = renderToStaticMarkup(
      <MessageList
        artifacts={[]}
        isResponding={false}
        messages={messages}
        onBranchFromMessage={() => {}}
        onSelectArtifact={() => {}}
      />,
    );

    expect(markup).not.toContain("data-branch-state");
  });
});
