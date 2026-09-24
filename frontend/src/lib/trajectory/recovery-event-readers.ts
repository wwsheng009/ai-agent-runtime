// P0-2 拆分（原 recovery.ts L64-L77、L152-L165）：事件字段读取工具。
// 拆出原因：recovery.ts 超 500 非空行门禁；这些读取器被映射层多个模块共享
// （recovery.ts 与 recovery-runtime-tools.ts），下沉后避免循环依赖。

import type { SessionRuntimeEvent } from "@/types/runtime";

export function readTrimmedString(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  const trimmed = value.trim();
  return trimmed === "" ? undefined : trimmed;
}

export function readFiniteNumber(value: unknown): number | undefined {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  return undefined;
}

/** 读取事件持久化 seq（后端 ListEvents 注入 payload.seq）。 */
export function chatSseEventSeq(event: SessionRuntimeEvent): number {
  const rawSeq = event.payload?.seq;
  if (typeof rawSeq === "number" && Number.isFinite(rawSeq) && rawSeq > 0) {
    return Math.floor(rawSeq);
  }
  if (typeof rawSeq === "string") {
    const parsed = Number(rawSeq.trim());
    if (Number.isFinite(parsed) && parsed > 0) {
      return Math.floor(parsed);
    }
  }
  return 0;
}
