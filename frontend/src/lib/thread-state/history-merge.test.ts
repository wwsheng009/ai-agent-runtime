// absorbTrailingOrphanTurnTailIntoHistory 的回归测试。
//
// 现场根因（2026-10-14 session_20261013175416_O9Ughlac）：回合收尾后，runtime
// 通道迟到桥接帧（tool_started/tool_finished）补建的占位消息
// （`turn-<本地chat UUID>-assistant`，id 空间与服务端 `turn_…` 不同）无法被
// mapSessionHistoryToMessages 按 turn_id 命中，作为 live-only 消息追加在权威
// 历史之后——19 张工具卡整块落在结论正文（durable msg_ec5749a…）后面。
// 吸收判据刻意不依赖跨空间 turn_id，只看时序 + 内容形态 + 权威性。

import { describe, expect, it } from "vitest";

import { type ChatMessage, type MessageSegment } from "@/data/mock";

import { absorbTrailingOrphanTurnTailIntoHistory } from "./history-merge";

function textMessage(id: string, content: string): ChatMessage {
  return {
    id,
    role: "assistant",
    author: "Runtime assistant",
    label: "assistant",
    segments: [{ type: "text", content }],
  };
}

function toolSegment(toolCallId: string): MessageSegment {
  return {
    type: "tool",
    toolCallId,
    name: "shell",
    status: "finished",
  };
}

function reasoningSegment(content: string): MessageSegment {
  return { type: "reasoning", content };
}

/** 迟到桥接帧补建的孤儿占位：streaming 已被终态帧摘掉、无正文、只带过程段。 */
function orphanPlaceholder(id: string, segments: MessageSegment[]): ChatMessage {
  return {
    id,
    role: "assistant",
    author: "Runtime stream",
    label: "runtime",
    runtimeTurnId: "8707136f-8690-48c3-ad18-2e60b664f514",
    streaming: false,
    segments,
  };
}

describe("absorbTrailingOrphanTurnTailIntoHistory", () => {
  const projection = textMessage("msg_ec5749a", "提交 174f9632（结论正文）");
  const orphan = orphanPlaceholder(
    "turn-8707136f-8690-48c3-ad18-2e60b664f514-assistant",
    [toolSegment("call-1"), toolSegment("call-2")],
  );

  it("吸收权威投影之后紧贴的无正文占位：工具行并入投影，占位行消失", () => {
    const messages = [textMessage("msg_user", "继续"), projection, orphan];
    const merged = absorbTrailingOrphanTurnTailIntoHistory(messages, [projection]);
    expect(merged).toHaveLength(2);
    const tail = merged[1];
    expect(tail.id).toBe("msg_ec5749a");
    expect(tail.segments.filter((segment) => segment.type === "tool")).toHaveLength(2);
    expect(tail.segments.at(-1)).toEqual({ type: "text", content: "提交 174f9632（结论正文）" });
  });

  it("幂等：重复同步不产生第二份工具行/正文", () => {
    const messages = [projection, orphan];
    const once = absorbTrailingOrphanTurnTailIntoHistory(messages, [projection]);
    expect(once).toHaveLength(1);
    const twice = absorbTrailingOrphanTurnTailIntoHistory(once, [once[0]]);
    expect(twice).toHaveLength(1);
    expect(twice[0].segments.filter((segment) => segment.type === "tool")).toHaveLength(2);
    expect(twice[0].segments.filter((segment) => segment.type === "text")).toHaveLength(1);
  });

  it("占位携带的推理块一并并入（过程区在前）", () => {
    const messages = [
      projection,
      orphanPlaceholder(orphan.id, [reasoningSegment("补记：检查 vet 输出"), toolSegment("call-1")]),
    ];
    const merged = absorbTrailingOrphanTurnTailIntoHistory(messages, [projection]);
    expect(merged).toHaveLength(1);
    expect(merged[0].segments[0]).toEqual(reasoningSegment("补记：检查 vet 输出"));
    expect(merged[0].segments.at(-1)).toEqual({ type: "text", content: "提交 174f9632（结论正文）" });
  });

  it("占位与投影之间隔着 user 消息（新一轮已开始）时不吸收", () => {
    const nextTurn = textMessage("msg_user_next", "下一轮");
    const messages = [projection, nextTurn, orphan];
    expect(absorbTrailingOrphanTurnTailIntoHistory(messages, [projection])).toHaveLength(3);
  });

  it("孤儿仍在流式（真正在途的直连回合）时不吸收", () => {
    const messages = [
      projection,
      { ...orphan, streaming: true },
    ];
    expect(absorbTrailingOrphanTurnTailIntoHistory(messages, [projection])).toHaveLength(2);
  });

  it("占位带可见正文（直连通道正常回答）时不吸收——live-only 语义优先", () => {
    const answered = orphanPlaceholder(orphan.id, [{ type: "text", content: "这是正在生成的回答" }]);
    const messages = [projection, answered];
    expect(absorbTrailingOrphanTurnTailIntoHistory(messages, [projection])).toHaveLength(2);
  });

  it("紧前一条不是权威历史确认的投影（历史里没有它的 id）时不吸收", () => {
    const unknownTail = textMessage("msg_local_only", "本地才有的消息");
    const messages = [unknownTail, orphan];
    expect(absorbTrailingOrphanTurnTailIntoHistory(messages, [projection])).toHaveLength(2);
  });

  it("紧前一条仍在 streaming（活跃回合尾部）时不吸收", () => {
    const streamingTail = { ...projection, streaming: true };
    const messages = [streamingTail, orphan];
    expect(absorbTrailingOrphanTurnTailIntoHistory(messages, [projection])).toHaveLength(2);
  });

  it("次级变体：streaming 占位(S) + 已收尾孤儿，吸收进更早的权威投影，S 原地保留", () => {
    const streamingPlaceholder = orphanPlaceholder(
      "turn-stalled-assistant",
      [toolSegment("call-stale")],
    );
    streamingPlaceholder.streaming = true;
    const messages = [projection, streamingPlaceholder, orphan];
    const merged = absorbTrailingOrphanTurnTailIntoHistory(messages, [projection]);
    expect(merged).toHaveLength(2);
    // 位置保持：投影仍在原位吸收孤儿；streaming 占位原地保留（有自己的收尾通道）。
    expect(merged[0].id).toBe("msg_ec5749a");
    expect(merged[1].id).toBe("turn-stalled-assistant");
    expect(merged[1].segments.filter((segment) => segment.type === "tool")).toHaveLength(1);
    expect(merged[0].segments.filter((segment) => segment.type === "tool")).toHaveLength(2);
  });

  it("次级变体歧义态：S 已被历史命中却仍 streaming——放弃吸收，不张冠李戴", () => {
    const ambiguousStreamingTail = { ...projection, streaming: true };
    const messages = [projection, ambiguousStreamingTail, orphan];
    // mappedHistory 同时含 projection 与 ambiguousStreamingTail（同一 id）——
    // S 是「历史确认身份但本地未收尾」的歧义态，整体放弃。
    expect(
      absorbTrailingOrphanTurnTailIntoHistory(messages, [projection, ambiguousStreamingTail]),
    ).toHaveLength(3);
  });
});
