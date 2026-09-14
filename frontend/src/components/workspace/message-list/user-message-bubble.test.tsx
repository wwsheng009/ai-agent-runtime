// §12.1.4 验收：用户气泡的复制图标只在确有可复制正文时显示。
// 空正文 / 纯附件消息不再常驻一颗禁用图标。

import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { type ChatMessage, type MessageSegment } from "@/data/mock";

import { UserMessageBubble } from "./user-message-bubble";

function userMessage(segments: MessageSegment[]): ChatMessage {
  return {
    id: "user-1",
    role: "user",
    author: "You",
    label: "prompt",
    segments,
  };
}

function renderBubble(message: ChatMessage) {
  return renderToStaticMarkup(
    <UserMessageBubble
      actionsDisabled={false}
      backtrackNavigationActive={false}
      backtrackPending={false}
      inlineEditDraft=""
      isEditing={false}
      isNavigationSelected={false}
      labelId={`${message.id}-label`}
      message={message}
      metaId={`${message.id}-meta`}
      onSelectArtifact={() => {}}
      setEditingMessageId={() => {}}
      setInlineEditDraft={() => {}}
      showBacktrack={false}
      statusId={`${message.id}-status`}
    />,
  );
}

describe("UserMessageBubble 复制入口", () => {
  it("有正文：渲染复制按钮", () => {
    const markup = renderBubble(userMessage([{ type: "text", content: "看一下入口文件" }]));

    expect(markup).toContain("复制这条用户消息");
  });

  it("空正文：不渲染复制按钮", () => {
    const markup = renderBubble(userMessage([]));

    expect(markup).not.toContain("复制这条用户消息");
    expect(markup).toContain('data-chat-flow-kind="user"');
  });

  it("纯图片附件（无正文）：不渲染复制按钮", () => {
    const markup = renderBubble(
      userMessage([{ type: "image", src: "blob:mock-image", alt: "附件截图" }]),
    );

    expect(markup).not.toContain("复制这条用户消息");
  });

  it("空文本段不产出行节点（不靠空 div 占位）", () => {
    const markup = renderBubble(
      userMessage([
        { type: "text", content: "   " },
        { type: "text", content: "看一下入口文件" },
      ]),
    );

    // 只有有正文的那一段产出 assistant-step 行锚点。
    expect(markup.match(/data-chat-flow-kind="assistant-step"/g)).toHaveLength(1);
    expect(markup).toContain("看一下入口文件");
  });

  it("无正文且无回溯入口：动作行整行不渲染（不留 28px 空行）", () => {
    const markup = renderBubble(userMessage([]));

    expect(markup).not.toContain("app-hover-reveal");
  });
});
