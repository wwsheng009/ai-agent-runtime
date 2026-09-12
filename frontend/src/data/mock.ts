// 由 data/mock.ts 机械拆分而来（P0-2），仅搬迁不改语义。
// 对外导出面保持不变，消费方 import 路径零改动；实现见 ./mock/ 各模块。

export type { Artifact, ChatMessage, MessageSegment, Thread } from "./mock/types";

export { initialThreads } from "./mock/initial-threads";
