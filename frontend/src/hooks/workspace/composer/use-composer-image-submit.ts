/**
 * S5：composer 的「带图发送」接线（`submit_prompt.images`）。
 *
 * 为什么独立成 hook：`use-workspace-agent-chat-turn.ts` 已到 P0-2 的 500 非空行上限，
 * 而这段逻辑自洽——它只做三件事：把回合脚手架（prepareTurn）转成本地回合身份、
 * 投递运行时命令、把回执写回 UI。上传/发送的数据真值仍由调用方持有。
 *
 * 语义（与 `image-prompt-turn` 一致，绝无乐观伪造）：
 * - 只投递**已上传成功**的服务端路径（调用方传进来的就是这些）；
 * - 会话忙（202 pending）：回滚乐观消息并保留草稿轨，给出可见原因；
 * - 无会话（新线程）：不发送，给出可见原因（不静默丢附件）。
 */

import { useCallback, useState } from "react";
import { type Dispatch, type SetStateAction } from "react";

import { type Thread } from "@/data/mock";
import type { AgentChatTurnBootstrap } from "@/hooks/workspace/agent-chat-turn/turn-bootstrap";
import { type SessionTurnRegistry } from "@/hooks/workspace/agent-chat-turn/session-turn-registry";
import {
  startImagePromptTurn,
  type ComposerImageSubmitNotice,
} from "@/hooks/workspace/agent-chat-turn/image-prompt-turn";
import { normalizeSessionId } from "@/lib/session-id";
import { normalizeRuntimePromptImages } from "@/lib/runtime-api";
import {
  createStreamingAssistantMessage,
  type RuntimeDeltaCoordinator,
  upsertArtifact,
} from "@/lib/workspace-thread-state";

export type UseComposerImageSubmitOptions = {
  /** 回合脚手架工厂（由宿主注入 `prepareAgentChatTurn` 的实参；会话由本 hook 校验后传入）。 */
  prepareTurn: (prompt: string, thread: Thread) => AgentChatTurnBootstrap;
  selectedThread: Thread | undefined;
  selectedTurnKey: string;
  setDraft: (value: string) => void;
  setSelectedArtifactId: (artifactId: string | null) => void;
  setThreads: Dispatch<SetStateAction<Thread[]>>;
  turnRegistry: SessionTurnRegistry;
  deltaCoordinator?: RuntimeDeltaCoordinator;
  clearAttachments: () => void;
  onSessionTouched?: () => void;
};

export type ComposerImageSubmitController = {
  notice: ComposerImageSubmitNotice | null;
  dismissNotice: () => void;
  /** 返回 false = 未启动（无会话 / 会话忙 / 无图）。 */
  start: (prompt: string, images: readonly string[]) => boolean;
};

export function useComposerImageSubmit({
  prepareTurn,
  selectedThread,
  selectedTurnKey,
  setDraft,
  setSelectedArtifactId,
  setThreads,
  turnRegistry,
  deltaCoordinator,
  clearAttachments,
  onSessionTouched,
}: UseComposerImageSubmitOptions): ComposerImageSubmitController {
  const [notice, setNotice] = useState<ComposerImageSubmitNotice | null>(null);

  const dismissNotice = useCallback(() => {
    setNotice(null);
  }, []);

  const start = useCallback(
    (prompt: string, images: readonly string[]): boolean => {
      const normalized = normalizeRuntimePromptImages(images);
      if (!selectedThread || normalized.length === 0) {
        return false;
      }
      const sessionId = normalizeSessionId(selectedThread.sessionId ?? "");
      if (!sessionId) {
        // 附件只挂在已落库会话上：新线程先发一条文字消息创建会话（不静默丢附件）。
        setNotice({
          tone: "error",
          messageKey: "composer.attachments.needsSession",
        });
        return false;
      }
      // 提交闸门按会话（同会话单飞）：注册表条目在收尾前一直挡住第二次提交。
      if (turnRegistry.isBusy(selectedTurnKey)) {
        return false;
      }
      const prepared = prepareTurn(prompt, selectedThread);
      turnRegistry.beginTurn({
        key: prepared.turnKey,
        threadId: prepared.threadId,
        turnId: prepared.turnId,
        controller: prepared.controller,
        turnState: prepared.turnState,
      });
      turnRegistry.setStalled(prepared.turnKey, false);
      setDraft("");
      setNotice(null);
      prepared.turnTrajectoryStore.reset();

      const updateCurrentThread = (updater: (thread: Thread) => Thread) => {
        setThreads((current) => {
          const existingIndex = current.findIndex(
            (thread) => thread.id === prepared.threadId,
          );
          if (existingIndex === -1) {
            return [updater(prepared.threadSnapshot), ...current];
          }
          return current.map((thread) =>
            thread.id === prepared.threadId ? updater(thread) : thread,
          );
        });
      };
      updateCurrentThread((thread) => ({
        ...thread,
        updatedAt: new Date().toISOString(),
        transport: thread.transport === "live" ? "live" : "mock",
        lastError: null,
        artifacts: upsertArtifact(thread.artifacts, prepared.requestArtifact),
        messages: [
          ...thread.messages,
          prepared.userMessage,
          createStreamingAssistantMessage(
            prepared.assistantMessageId,
            [prepared.requestArtifact.id],
            prepared.turnId,
          ),
        ],
      }));
      setSelectedArtifactId(prepared.requestArtifact.id);

      startImagePromptTurn({
        sessionId,
        prompt,
        images: normalized,
        threadId: prepared.threadId,
        turnId: prepared.turnId,
        assistantMessageId: prepared.assistantMessageId,
        userMessage: prepared.userMessage,
        controller: prepared.controller,
        updateCurrentThread,
        setNotice,
        clearAttachments,
        onSessionTouched: () => {
          onSessionTouched?.();
        },
        finishTurn: () => {
          deltaCoordinator?.endTurn(prepared.turnId);
          turnRegistry.finishTurn(prepared.turnId);
        },
      });
      return true;
    },
    [
      clearAttachments,
      deltaCoordinator,
      onSessionTouched,
      prepareTurn,
      selectedThread,
      selectedTurnKey,
      setDraft,
      setSelectedArtifactId,
      setThreads,
      turnRegistry,
    ],
  );

  return { notice, dismissNotice, start };
}
