// 传输队列的「下载降级模型」纯逻辑层（P2-4：三层下载的选择与能力说明）。
//
// 与 transfer-tray.tsx 的分工：这一层只放**常量 / 类型 / 纯函数**（可单测，见 transfer-tray.test.tsx），
//   组件文件只导出组件 —— 混导出会被 `react-refresh/only-export-components` 拦下。
//
// 降级纪律：只用浏览器真实能力（showSaveFilePicker / ReadableStream）与探测结论（Accept-Ranges / ETag）判断，
//   缺失的能力必须留一条说明（`layerNotes`），不假装支持。

import type { FsDownloadProbe } from "@/types/runtime/fs-browser";

/** < 8 MiB 直接交给浏览器；> 256 MiB 禁用浏览器内下载（流式层要在内存里拼 Blob）。 */
export const NATIVE_FIRST_LIMIT_BYTES = 8 * 1024 * 1024;
export const STREAM_DOWNLOAD_LIMIT_BYTES = 256 * 1024 * 1024;
export const PICKER_CHUNK_BYTES = 4 * 1024 * 1024;

export type DownloadLayer = "native" | "picker" | "stream" | "none";
export type DownloadCapabilities = { hasSaveFilePicker: boolean; hasStreamReadable: boolean };
export type DownloadNote = { key: string; params?: Record<string, string | number> };
export type DownloadTask = {
  id: string; path: string; name: string; size: number; layer: DownloadLayer;
  state: "probing" | "downloading" | "saving" | "completed" | "failed" | "canceled";
  transferred: number; error?: unknown; notes?: DownloadNote[];
};

/** 层 2 专用：下载途中 ETag 与探测值不一致（文件被改过）→ 中止，不写出半新半旧的内容。 */
export class DownloadEtagChangedError extends Error {
  readonly expected: string;
  readonly actual: string;
  constructor(expected: string, actual: string) {
    super(`download etag changed: expected ${expected}, got ${actual}`);
    this.name = "DownloadEtagChangedError";
    this.expected = expected;
    this.actual = actual;
  }
}

export type SaveFilePickerWindow = Window & {
  showSaveFilePicker?: (options?: { suggestedName?: string }) => Promise<{
    createWritable: () => Promise<{ write: (data: Blob) => Promise<void>; close: () => Promise<void>; abort?: () => Promise<void> }>;
  }>;
};

export function detectDownloadCapabilities(): DownloadCapabilities {
  return {
    hasSaveFilePicker: typeof (globalThis as unknown as SaveFilePickerWindow).showSaveFilePicker === "function",
    hasStreamReadable: typeof ReadableStream !== "undefined" && typeof Response !== "undefined",
  };
}

export type DownloadLayerRequest = {
  size: number;
  capabilities: DownloadCapabilities;
  /** HEAD 探测结果；缺失即「未探测」，不假设服务端支持 Range。 */
  probe?: Pick<FsDownloadProbe, "acceptRanges" | "etag"> | null;
  /** 用户显式点「下载」→ 直接走原生下载。 */
  preferNative?: boolean;
};

/** 三层选择（纯函数，可单测）：只用真实能力与探测结果，不做乐观假设。 */
export function selectDownloadLayer(request: DownloadLayerRequest): DownloadLayer {
  const { capabilities, preferNative, probe, size } = request;
  if (preferNative || (size >= 0 && size < NATIVE_FIRST_LIMIT_BYTES)) return "native";
  if (size < 0) return capabilities.hasStreamReadable ? "stream" : "native";
  if (size > STREAM_DOWNLOAD_LIMIT_BYTES) return "native";
  if (capabilities.hasSaveFilePicker && probe?.acceptRanges === true && (probe.etag ?? "") !== "") return "picker";
  return capabilities.hasStreamReadable ? "stream" : "native";
}

/** 层能力说明：不可用的能力必须留话（返回 i18n key，渲染时翻译）。 */
export function layerNotes(layer: DownloadLayer, capabilities: DownloadCapabilities, size: number): DownloadNote[] {
  const notes: DownloadNote[] = [];
  if (layer === "native" && size > STREAM_DOWNLOAD_LIMIT_BYTES) notes.push({ key: "panels.fileBrowser.transfer.layer.nativeTooLarge" });
  if (!capabilities.hasStreamReadable) notes.push({ key: "panels.fileBrowser.transfer.layer.noStream" });
  if (layer === "stream") {
    if (!capabilities.hasSaveFilePicker) notes.push({ key: "panels.fileBrowser.transfer.layer.noPicker" });
    notes.push({ key: "panels.fileBrowser.transfer.layer.noResume" });
  }
  return notes;
}
