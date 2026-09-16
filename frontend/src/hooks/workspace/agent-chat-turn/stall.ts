import { useState } from "react";

import type { Thread } from "@/data/mock";

import { CHAT_STREAM_IDLE_TIMEOUT_MS } from "./shared";

/**
 * 读侧静默看门狗命中标记：本页 chat 流已死（但服务端回合可能仍在跑）。
 * 与 isResponding 分开——看门狗命中后 finally 会把 isResponding 置 false，
 * 而「服务端还在跑」这件事必须继续对用户可见（状态行 + 重试入口）。
 */
export function useChatStreamStall() {
  const [streamStalled, setStreamStalled] = useState(false);
  return {
    clear: () => setStreamStalled(false),
    mark: () => setStreamStalled(true),
    streamStalled,
  };
}

/**
 * 命中静默看门狗：标记降级 + 明确告知，但**不** finalizeTurn——尾巴消息保持
 * streaming，runtime 流的续传通道才能把它当作可续写的 live 目标。
 */
export function applyChatStreamStall(effects: {
  notifyFailure: (message: string) => void;
  updateCurrentThread: (updater: (thread: Thread) => Thread) => void;
  updateStreamingError: (message: string) => void;
}): string {
  const seconds = Math.round(CHAT_STREAM_IDLE_TIMEOUT_MS / 1000);
  const message =
    `连接中断：${seconds} 秒未收到任何数据（本页 chat 流已停止接收）。` +
    "服务端回合可能仍在运行——会话流会自动续传，也可手动重试。";
  effects.updateStreamingError(message);
  effects.updateCurrentThread((thread) => ({
    ...thread,
    updatedAt: new Date().toISOString(),
    transport: "error",
    lastError: message,
  }));
  effects.notifyFailure(message);
  return message;
}
