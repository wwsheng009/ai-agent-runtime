// P0-2 拆分：recovery 测试共用的事件构造器（原 recovery.test.ts L26-L36、L342-L348）。

import type { SessionRuntimeEvent } from "@/types/runtime";

export function chatSseEvent(
  kind: string,
  seq: number,
  extra: Record<string, unknown> = {},
): SessionRuntimeEvent {
  return {
    type: `chat.sse.${kind}`,
    timestamp: "2026-08-16T00:00:00Z",
    payload: { ...extra, seq },
  };
}

export function runtimeEvent(
  type: string,
  seq: number,
  extra: Record<string, unknown> = {},
): SessionRuntimeEvent {
  return {
    type,
    timestamp: "2026-08-16T00:00:00Z",
    payload: { ...extra, seq },
  } as SessionRuntimeEvent;
}
