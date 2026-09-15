// §12.1.4 验收：turn-tail 的可见性由「该回合是否有可呈现内容」决定。
// - 有可见回答文本 → 28px 动作行（复制图标默认 hover 显现，不常驻上屏）；
// - 纯工具 / 仅推理且无用量、无重试 → 整行不渲染（不留空行，也不留禁用图标）；
// - 有完整用量 / 重试入口 → 行保留（锚点契约不变），但无正文时仍不出复制图标。

import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { type ChatMessage, type MessageSegment } from "@/data/mock";
import { type TurnUsage } from "@/lib/turn-usage";

import { TurnTailRow } from "./turn-tail-row";

function assistantMessage(segments: MessageSegment[]): ChatMessage {
  return {
    id: "assistant-tail",
    role: "assistant",
    author: "Runtime",
    label: "response",
    segments,
  };
}

function renderTail(
  message: ChatMessage,
  options: {
    onBranch?: () => void;
    branchPending?: boolean;
    onRetry?: () => void;
    usage?: TurnUsage | null;
  } = {},
) {
  return renderToStaticMarkup(
    <TurnTailRow
      anchorKey={`anchor:${message.id}`}
      branchPending={options.branchPending}
      flowKey={`turn-tail:${message.id}`}
      message={message}
      onBranch={options.onBranch}
      onRetry={options.onRetry}
      usage={options.usage ?? null}
    />,
  );
}

const COMPLETE_USAGE: TurnUsage = {
  promptTokens: 1200,
  completionTokens: 300,
  totalTokens: 1500,
};

describe("TurnTailRow 可见性", () => {
  it("有可见回答文本：渲染复制按钮，且默认 hover 显现（不常驻上屏）", () => {
    const markup = renderTail(
      assistantMessage([{ type: "text", content: "结论：入口文件共 42 行。" }]),
    );

    expect(markup).toContain("复制这条回复");
    expect(markup).toContain('data-chat-flow-kind="turn-tail"');
    // 未命中锚点（宿主不下发 onBranch）时不渲染分支入口。
    expect(markup).not.toContain("在新对话中分支");
    // §12.1.4：动作图标走 hover 显现轴，不默认常驻。
    expect(markup).toContain("app-hover-reveal");
  });

  it("可分支锚点：渲染分支按钮，且不带禁用态（可用即可点）", () => {
    const markup = renderTail(
      assistantMessage([{ type: "text", content: "结论：入口文件共 42 行。" }]),
      { onBranch: () => {} },
    );

    expect(markup).toContain('data-branch-state="available"');
    expect(markup).toContain('aria-label="在新对话中分支"');
    expect(markup).not.toContain("aria-disabled");
    expect(markup).not.toContain("aria-describedby");
  });

  it("分支在途：按钮进入 pending（spinner + aria-busy），不再是不可用态", () => {
    const markup = renderTail(
      assistantMessage([{ type: "text", content: "结论：入口文件共 42 行。" }]),
      { branchPending: true, onBranch: () => {} },
    );

    expect(markup).toContain('data-branch-state="pending"');
    expect(markup).toContain('aria-busy="true"');
    expect(markup).toContain("正在创建分支会话…");
  });

  it("仅空白文本：整行不渲染（空格不构成可复制内容）", () => {
    const markup = renderTail(
      assistantMessage([{ type: "text", content: "   \n\t " }]),
    );

    expect(markup).toBe("");
  });

  it("纯工具回合（无正文 / 无用量 / 无重试）：整行不渲染，不留空行", () => {
    const markup = renderTail(
      assistantMessage([
        {
          type: "tool",
          toolCallId: "call-1",
          name: "read_file",
          status: "finished",
          argsSummary: "src/index.ts",
          resultSummary: "42 行",
        },
      ]),
    );

    expect(markup).toBe("");
  });

  it("仅推理回合（无正文）：整行不渲染", () => {
    const markup = renderTail(
      assistantMessage([{ type: "reasoning", content: "先盘点入口文件" }]),
    );

    expect(markup).toBe("");
  });

  it("仅推理回合（无正文）：即使宿主误传 onBranch 也不渲染分支按钮", () => {
    const markup = renderTail(
      assistantMessage([{ type: "reasoning", content: "先盘点入口文件" }]),
      { onBranch: () => {} },
    );

    expect(markup).toBe("");
  });

  it("有完整用量但无正文：行保留锚点，但不渲染复制图标", () => {
    const markup = renderTail(
      assistantMessage([
        {
          type: "tool",
          toolCallId: "call-2",
          name: "read_file",
          status: "finished",
          resultSummary: "42 行",
        },
      ]),
      { usage: COMPLETE_USAGE },
    );

    expect(markup).toContain('data-chat-flow-kind="turn-tail"');
    expect(markup).toContain("data-chat-anchor-key");
    expect(markup).not.toContain("复制这条回复");
  });
});
