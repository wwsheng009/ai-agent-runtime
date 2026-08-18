/**
 * Trajectory → chat 投影映射器 + 双跑一致性校验
 *
 * - chat 投影：Item[] → MessageSegment[]（复用现有渲染组件，message-list 零改动）；
 * - G7 事件（planning/orchestration/route/observation/subagent）在 chat 投影中
 *   不渲染（Phase 2 轨迹视图消费）；
 * - 双跑校验：turn 收尾时对比「轨迹快照摘要」与「现有 segments 摘要」，DEV 下
 *   不一致告警（过渡策略：双跑校验快照一致性，确认无回归后再切换唯一路径）。
 */
import type { MessageSegment } from "@/data/mock";

import type { TrajectoryItem, TrajectorySnapshot } from "./types";

/** 工具阶段 → MessageSegment.tool.status。 */
export function toolPhaseToSegmentStatus(
  phase: "started" | "running" | "finished" | "error",
): "started" | "running" | "finished" | "error" {
  return phase;
}

/** chat 投影：轨迹 Items → MessageSegment[]。 */
export function trajectoryItemsToMessageSegments(
  items: TrajectoryItem[],
): MessageSegment[] {
  const segments: MessageSegment[] = [];
  for (const item of items) {
    const head = item.head;
    switch (head.kind) {
      case "text":
        if (head.content) {
          segments.push({ type: "text", content: head.content });
        }
        break;
      case "reasoning":
        if (head.content) {
          segments.push({
            type: "reasoning",
            content: head.content,
            running: item.status === "running",
          });
        }
        break;
      case "tool":
        segments.push({
          type: "tool",
          toolCallId: head.toolCallId,
          name: head.name,
          status: toolPhaseToSegmentStatus(head.phase),
          argsSummary: head.argsSummary,
          resultSummary: head.resultSummary,
          errorMessage: head.errorMessage,
        });
        break;
      case "system":
        segments.push({
          type: "callout",
          title: item.status === "failed" ? "Runtime failed" : "Runtime",
          content: head.note,
          tone: item.status === "failed" ? "warning" : "info",
        });
        break;
      case "structured":
        break; // G7 事件：Phase 2 轨迹视图消费
    }
  }
  return segments;
}

/** 轨迹侧摘要（双跑校验输入）。 */
export interface TrajectoryTurnSummary {
  text: string;
  reasoning: string;
  tools: Array<{ name: string; phase: string }>;
}

/** 从轨迹快照提取 turn 摘要（text / reasoning / 工具序列）。 */
export function summarizeTrajectorySnapshot(
  snapshot: TrajectorySnapshot,
): TrajectoryTurnSummary {
  const summary: TrajectoryTurnSummary = {
    text: "",
    reasoning: "",
    tools: [],
  };
  for (const item of snapshot.items) {
    const head = item.head;
    switch (head.kind) {
      case "text":
        summary.text += head.content;
        break;
      case "reasoning":
        summary.reasoning += head.content;
        break;
      case "tool":
        summary.tools.push({ name: head.name, phase: head.phase });
        break;
      default:
        break;
    }
  }
  return summary;
}

/** 从现有 segments 提取同一摘要（双跑校验另一侧）。 */
export function summarizeMessageSegments(
  segments: MessageSegment[],
): TrajectoryTurnSummary {
  const summary: TrajectoryTurnSummary = {
    text: "",
    reasoning: "",
    tools: [],
  };
  for (const segment of segments) {
    switch (segment.type) {
      case "text":
        summary.text += segment.content;
        break;
      case "reasoning":
        summary.reasoning += segment.content;
        break;
      case "tool":
        summary.tools.push({
          name: segment.name,
          phase: segment.status,
        });
        break;
      default:
        break;
    }
  }
  return summary;
}

/**
 * 双跑校验：轨迹快照 vs 现有 segments 是否一致。
 * 返回不一致的维度列表（空数组 = 一致）。
 */
export function compareTrajectoryVsSegments(
  snapshot: TrajectorySnapshot,
  segments: MessageSegment[],
): string[] {
  const trajectory = summarizeTrajectorySnapshot(snapshot);
  const current = summarizeMessageSegments(segments);
  const differences: string[] = [];
  if (trajectory.text !== current.text) {
    differences.push(
      `text mismatch: trajectory=${JSON.stringify(trajectory.text)} segments=${JSON.stringify(current.text)}`,
    );
  }
  if (trajectory.reasoning !== current.reasoning) {
    differences.push(
      `reasoning mismatch: trajectory=${JSON.stringify(trajectory.reasoning)} segments=${JSON.stringify(current.reasoning)}`,
    );
  }
  if (JSON.stringify(trajectory.tools) !== JSON.stringify(current.tools)) {
    differences.push(
      `tools mismatch: trajectory=${JSON.stringify(trajectory.tools)} segments=${JSON.stringify(current.tools)}`,
    );
  }
  return differences;
}

/** DEV 双跑校验：不一致时 console.warn（幂等、无返回值）。 */
export function debugTrajectoryConsistency(
  snapshot: TrajectorySnapshot,
  segments: MessageSegment[],
) {
  if (typeof console === "undefined") {
    return;
  }
  const differences = compareTrajectoryVsSegments(snapshot, segments);
  if (differences.length > 0) {
    // eslint-disable-next-line no-console
    console.warn(
      "[trajectory] consistency check failed:\n" + differences.join("\n"),
    );
  }
}
