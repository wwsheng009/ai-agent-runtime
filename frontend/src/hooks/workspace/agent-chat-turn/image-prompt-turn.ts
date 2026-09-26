/**
 * S5：composer 的「带图发送」回合（`submit_prompt.images`）。
 *
 * 为什么单独走这条通道：React 文本回合走 `/api/agent/chat`（SSE），而后端只在
 * `POST /api/runtime/sessions/{id}/runtime/commands` 的 `submit_prompt` 上接受
 * `images[]` 与附件目录边界校验（§6.3 P2-1C 后端前置，未改 Go）。
 *
 * 语义边界（如实呈现，不伪造）：
 * - 只投递**已上传成功**的服务端路径；空数组不发该字段（保持旧响应契约）；
 * - 202 `{pending:true}` = 会话忙、本轮未执行：**回滚乐观消息并保留草稿轨**，
 *   由调用方给出可见原因（既不假装已发送，也不静默丢弃附件）；
 * - 200 `{result, attached_images?, image_notes?}`：把服务端 result 文本落到助手
 *   消息（文本以服务端为准），`image_notes` 原样交给 UI 呈现；
 * - 失败：保留错误原文与分类，交给 composer 通知条。
 */

import {
  isSessionPromptUnavailable,
  submitRuntimeSessionPrompt,
} from "@/api/runtime/session-prompt";
import { type ChatMessage, type Thread } from "@/data/mock";
import { updateThreadMessage } from "@/lib/workspace-thread-state";

/** composer 通知条数据（宿主本地化：`messageKey` + 插值）。 */
export type ComposerImageSubmitNotice = {
  tone: "success" | "error";
  messageKey: string;
  values?: Record<string, unknown>;
};

export type ComposerImageSubmitBanner = { tone: "success" | "error"; text: string };

/**
 * 把回执本地化成通知条文案（与命令回执同一口径：键与插值按宿主数据收口，
 * 见 `use-composer-command-surface` 的 `commandResult`）。无回执返回 null。
 */
export function imageSubmitNoticeBanner(
  notice: ComposerImageSubmitNotice | null | undefined,
  t: (key: never, values: never) => unknown,
): ComposerImageSubmitBanner | null {
  if (!notice) {
    return null;
  }
  return {
    tone: notice.tone,
    text: t(notice.messageKey as never, (notice.values ?? {}) as never) as string,
  };
}

export type ImagePromptTurnInput = {
  sessionId: string;
  prompt: string;
  images: readonly string[];
  threadId: string;
  turnId: string;
  assistantMessageId: string;
  userMessage: ChatMessage;
  controller: AbortController;
  updateCurrentThread: (updater: (thread: Thread) => Thread) => void;
  setNotice: (notice: ComposerImageSubmitNotice | null) => void;
  /** 发送成功后清空附件草稿轨（失败/忙时保留，用户可重试）。 */
  clearAttachments: () => void;
  onSessionTouched: () => void;
  /** 回合收尾（注册表条目 / 轨迹账目 / 在途标记）。 */
  finishTurn: () => void;
};

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

/**
 * 从 `submit_prompt` 的 `result` 里取助手正文。
 * 只认明确的文本字段（string / output / content / text），取不到就返回空串——
 * 宁可让助手消息停在流式内容上，也不伪造正文。
 */
export function extractImagePromptResultText(result: unknown): string {
  if (typeof result === "string") {
    return result.trim();
  }
  const record = asRecord(result);
  if (!record) {
    return "";
  }
  for (const key of ["output", "content", "text", "message"]) {
    const value = record[key];
    if (typeof value === "string" && value.trim()) {
      return value.trim();
    }
  }
  return "";
}

function hasTextSegment(segments: ChatMessage["segments"]): boolean {
  return segments.some(
    (segment) => segment.type === "text" && segment.content.trim() !== "",
  );
}

function removeMessages(thread: Thread, messageIds: readonly string[]): Thread {
  const ids = new Set(messageIds);
  return {
    ...thread,
    messages: thread.messages.filter((message) => !ids.has(message.id)),
  };
}

function describePromptFailure(error: unknown): string {
  if (error instanceof Error && error.message.trim()) {
    return error.message.trim();
  }
  return String(error);
}

/** 主动停止（Stop / 会话切换 abort）：收掉占位消息的流式态，不写任何伪造正文。 */
function markPromptStopped(
  input: ImagePromptTurnInput,
  label: "stopped" | "failed",
): void {
  input.updateCurrentThread((thread) =>
    updateThreadMessage(thread, input.assistantMessageId, (message) => ({
      ...message,
      streaming: false,
      label,
    })),
  );
}

export function startImagePromptTurn(input: ImagePromptTurnInput): void {
  void (async () => {
    try {
      const response = await submitRuntimeSessionPrompt(input.sessionId, input.prompt, {
        images: input.images,
        signal: input.controller.signal,
      });
      if (input.controller.signal.aborted) {
        return;
      }
      if (response.pending) {
        // 会话忙：本轮未执行 → 回滚乐观消息，附件保留在草稿轨。
        input.updateCurrentThread((thread) =>
          removeMessages(thread, [input.userMessage.id, input.assistantMessageId]),
        );
        input.setNotice({
          tone: "error",
          messageKey: "composer.attachments.busyPending",
        });
        return;
      }
      const text = extractImagePromptResultText(response.result);
      input.updateCurrentThread((thread) =>
        updateThreadMessage(thread, input.assistantMessageId, (message) => ({
          ...message,
          streaming: false,
          label: "runtime",
          ...(text && !hasTextSegment(message.segments)
            ? { segments: [{ type: "text" as const, content: text }] }
            : {}),
        })),
      );
      input.clearAttachments();
      const notes = response.imageNotes;
      input.setNotice({
        tone: "success",
        messageKey:
          notes.length > 0
            ? "composer.attachments.sentWithNotes"
            : "composer.attachments.sent",
        values: {
          count: response.attachedImages ?? input.images.length,
          notes: notes.join("; "),
        },
      });
      input.onSessionTouched();
    } catch (error) {
      if (input.controller.signal.aborted) {
        markPromptStopped(input, "stopped");
        return;
      }
      // 失败：保留乐观消息（服务端可能已受理），把原因写进助手消息与通知条。
      const reason = describePromptFailure(error);
      input.updateCurrentThread((thread) =>
        updateThreadMessage(thread, input.assistantMessageId, (message) => ({
          ...message,
          streaming: false,
          label: "failed",
          ...(hasTextSegment(message.segments) ? {} : {
            segments: [{ type: "text" as const, content: reason }],
          }),
        })),
      );
      input.setNotice({
        tone: "error",
        messageKey: isSessionPromptUnavailable(error)
          ? "composer.attachments.sendUnavailable"
          : "composer.attachments.sendFailed",
        values: { reason },
      });
    } finally {
      input.finishTurn();
    }
  })();
}
