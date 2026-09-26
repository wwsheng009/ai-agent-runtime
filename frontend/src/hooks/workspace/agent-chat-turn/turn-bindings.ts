/**
 * 回合线程绑定（从 `use-workspace-agent-chat-turn.ts` 机械抽出，仅搬迁不改语义）。
 *
 * 为什么单独成文件：该 hook 已到 P0-2 的 500 非空行上限，而这一组闭包是自洽的
 * 「回合 → 线程存储」写入器（S5 的带图发送回合也复用同一份 `updateCurrentThread`，
 * 避免两套写入路径漂移）。
 */

import { type Dispatch, type SetStateAction } from "react";

import { type Artifact, type Thread } from "@/data/mock";
import { type ChatTurnRuntimeState } from "@/hooks/workspace/agent-chat-turn/turn-state";
import { type TrajectoryStore } from "@/hooks/workspace/use-trajectory-snapshot";
import {
  setLiveStreamReasoning,
  setLiveStreamText,
} from "@/lib/live-stream-text";
import type { TrajectoryEventKind } from "@/lib/trajectory/types";
import {
  appendArtifactToMessage,
  buildAssistantMessageSegments,
  updateThreadMessage,
} from "@/lib/workspace-thread-state";

export type TurnThreadBindingsInput = {
  assistantMessageId: string;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
  threadId: string;
  threadSnapshot: Thread;
  turnState: ChatTurnRuntimeState;
  turnTrajectoryStore: TrajectoryStore;
};

export type TurnThreadBindings = {
  updateCurrentThread: (updater: (thread: Thread) => Thread) => void;
  attachTurnArtifact: (artifact: Artifact) => void;
  pushTrajectory: (
    kind: TrajectoryEventKind,
    payload: Record<string, unknown> | null | undefined,
  ) => void;
  setStreamingMessage: (label: string, author: string, content: string) => void;
};

export function createTurnThreadBindings({
  assistantMessageId,
  setThreads,
  threadId,
  threadSnapshot,
  turnState,
  turnTrajectoryStore,
}: TurnThreadBindingsInput): TurnThreadBindings {
  const updateCurrentThread = (updater: (thread: Thread) => Thread) => {
    setThreads((current) => {
      const existingIndex = current.findIndex((thread) => thread.id === threadId);
      if (existingIndex === -1) {
        return [updater(threadSnapshot), ...current];
      }

      return current.map((thread) =>
        thread.id === threadId ? updater(thread) : thread,
      );
    });
  };

  const attachTurnArtifact = (artifact: Artifact) => {
    updateCurrentThread((thread) =>
      appendArtifactToMessage(thread, assistantMessageId, artifact),
    );
  };

  /** 轨迹事件入口：SSE 回调 → 轨迹快照（reducer 幂等，记录失败不阻断转发）。 */
  const pushTrajectory = (
    kind: TrajectoryEventKind,
    payload: Record<string, unknown> | null | undefined,
  ) => {
    turnTrajectoryStore.push(kind, payload);
  };

  const setStreamingMessage = (label: string, author: string, content: string) => {
    // 结构快照落 store 的同时把 live 记录对齐到同一份正文/推理。live 记录是
    // 「比 store 更新的那一份」，但并非所有文本来源都走 append 增量：`onResult`
    // 的 output/reasoning 是**整体快照**（`reconcileRuntimeText` 取更长者），
    // 只更新 canonical。渲染层优先用 live，若不在这里对齐，气泡会一直显示旧
    // live 文本直到下一次快照或定稿。文本不变时 set* 是幂等的（不通知订阅方）。
    setLiveStreamText(assistantMessageId, content);
    // live 记录按块寻址：只对齐**当前块**（整轮拼接会让尾行把前面几块的内容也
    // 显示出来——观感就是「所有推理并成一段」）。更早的块已定稿在各自的推理段里。
    setLiveStreamReasoning(
      assistantMessageId,
      turnState.reasoningBlocks[turnState.reasoningBlocks.length - 1] ?? "",
    );
    updateCurrentThread((thread) =>
      updateThreadMessage(thread, assistantMessageId, (message) => ({
        ...message,
        author,
        label,
        segments: buildAssistantMessageSegments(
          content,
          turnState.currentSource,
          turnState.reasoningText,
          {
            reasoningRunning: turnState.reasoningRunning,
            existingSegments: message.segments,
            reasoningBlocks: turnState.reasoningBlocks,
            reasoningBlockToolCounts: turnState.reasoningBlockToolCounts,
          },
        ),
      })),
    );
  };

  return { attachTurnArtifact, pushTrajectory, setStreamingMessage, updateCurrentThread };
}
