/**
 * 目录 barrel：实现按职责拆分到 ./use-session-runtime-stream/ 下
 * （types.ts 契约与状态类型、subscription.ts 订阅主体、hook.ts 组装），
 * 保持既有导入路径与具名导出 `useSessionRuntimeStream` 不变。
 */
export { useSessionRuntimeStream } from "./use-session-runtime-stream/hook";
