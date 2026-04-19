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
    expect(markup).toContain("Streaming response in progress");
    expect(markup).toContain('role="status"');
    expect(markup).toContain("Runtime stream active");
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
});
