// 下载管理器（P2-4 三层下载的执行侧）：探测 → 选层 → 落盘，失败按可解释结论收敛。
//
// 与 transfer-model.ts 的分工：那一层只做纯选择/说明（可单测），这里做副作用（fetch / 文件写入 / 进度）。
// 与 transfer-tray.tsx 的分工：组件只渲染这里给出的状态，不自行发请求。
//
// 纪律：
//   * 进度只用服务端确认值（Range 分片写入字节数 / 流式读取字节数），不预测；
//   * 层 2 途中 ETag 变化立即中止（不写出半新半旧的内容）；
//   * 416（Range 不被接受）与「探测无 ETag」都退回原生下载，不硬凑续传。

import { useCallback, useEffect, useRef, useState } from "react";

import { RuntimeApiError } from "@/api/runtime/shared";
import { buildNativeDownloadHref, downloadRange, FsRangeNotSatisfiableError, probeDownload } from "@/api/runtime/fs-transfer";
import { isAbortError } from "@/hooks/workspace/use-file-browser";
import { describeError } from "@/lib/errors";
import {
  detectDownloadCapabilities,
  DownloadEtagChangedError,
  layerNotes,
  NATIVE_FIRST_LIMIT_BYTES,
  PICKER_CHUNK_BYTES,
  selectDownloadLayer,
  STREAM_DOWNLOAD_LIMIT_BYTES,
  type DownloadTask,
  type SaveFilePickerWindow,
} from "@/components/workspace/file-browser/transfer-model";
import type { FsDownloadProbe, FsEntry } from "@/types/runtime/fs-browser";

function triggerNativeDownload(scope: string, path: string, name: string) {
  const anchor = document.createElement("a");
  anchor.href = buildNativeDownloadHref(scope, path);
  anchor.download = name;
  anchor.rel = "noreferrer";
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
}

export type DownloadManager = {
  downloads: DownloadTask[];
  start: (entry: FsEntry, preferNative?: boolean) => void;
  cancel: (id: string) => void;
  dismiss: (id: string) => void;
};

type Patch = (id: string, next: Partial<DownloadTask>) => void;
type WriteContext = { controller: AbortController; entry: FsEntry; scope: string; size: number; probe: FsDownloadProbe | null; patch: Patch };

export function useDownloadManager({ scope }: { scope: string }): DownloadManager {
  const [downloads, setDownloads] = useState<DownloadTask[]>([]);
  const controllersRef = useRef(new Map<string, AbortController>());
  const patch = useCallback<Patch>((id, next) => {
    setDownloads((list) => list.map((item) => (item.id === id ? { ...item, ...next } : item)));
  }, []);

  useEffect(() => () => {
    for (const controller of controllersRef.current.values()) controller.abort();
    controllersRef.current.clear();
  }, []);

  const start = useCallback(
    (entry: FsEntry, preferNative = false) => {
      const id = `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`;
      const controller = new AbortController();
      controllersRef.current.set(id, controller);
      const capabilities = detectDownloadCapabilities();
      setDownloads((list) => [{ id, path: entry.path, name: entry.name, size: entry.size, layer: "none", state: "probing", transferred: 0 }, ...list]);
      void (async () => {
        const writeContext: WriteContext = { controller, entry, scope, size: entry.size, probe: null, patch };
        try {
          const wantsProbe = entry.size < 0 || (capabilities.hasSaveFilePicker && entry.size >= NATIVE_FIRST_LIMIT_BYTES && entry.size <= STREAM_DOWNLOAD_LIMIT_BYTES);
          if (!preferNative && wantsProbe) {
            try {
              writeContext.probe = await probeDownload(scope, entry.path, { signal: controller.signal });
              if (entry.size < 0) writeContext.size = writeContext.probe.size;
            } catch (probeError) {
              if (isAbortError(probeError)) throw probeError;
              // 探测失败 ≠ 文件不可下载：退回最稳的原生下载，并留一条诚实说明（没有进度与校验）。
              triggerNativeDownload(scope, entry.path, entry.name);
              patch(id, {
                state: "completed",
                size: entry.size,
                transferred: entry.size,
                layer: "native",
                notes: [{ key: "panels.fileBrowser.transfer.layer.probeFailed", params: { message: describeError(probeError) } }],
              });
              return;
            }
          }
          const layer = selectDownloadLayer({ size: writeContext.size, capabilities, probe: writeContext.probe, preferNative });
          const notes = layerNotes(layer, capabilities, writeContext.size);
          if (layer === "none") {
            patch(id, { state: "failed", size: writeContext.size, layer, notes: [...notes, { key: "panels.fileBrowser.transfer.layer.unavailable" }] });
            return;
          }
          if (layer === "native") {
            triggerNativeDownload(scope, entry.path, entry.name);
            patch(id, { state: "completed", size: writeContext.size, transferred: writeContext.size, layer, notes });
            return;
          }
          patch(id, { state: layer === "picker" ? "saving" : "downloading", size: writeContext.size, layer, notes });
          if (layer === "picker") await writeRanges(id, writeContext);
          else await streamToBlob(id, writeContext);
          patch(id, { state: "completed" });
        } catch (error) {
          if (isAbortError(error)) {
            patch(id, { state: "canceled" });
          } else if (error instanceof FsRangeNotSatisfiableError) {
            triggerNativeDownload(scope, entry.path, entry.name);
            patch(id, { state: "completed", layer: "native", notes: [{ key: "panels.fileBrowser.transfer.layer.rangeUnsupported" }] });
          } else if (error instanceof DownloadEtagChangedError) {
            patch(id, { state: "failed", error, notes: [{ key: "panels.fileBrowser.transfer.layer.etagChanged" }] });
          } else {
            patch(id, { state: "failed", error });
          }
        } finally {
          controllersRef.current.delete(id);
        }
      })();
    },
    [patch, scope],
  );

  const cancel = useCallback(
    (id: string) => {
      controllersRef.current.get(id)?.abort();
      patch(id, { state: "canceled" });
    },
    [patch],
  );
  const dismiss = useCallback((id: string) => setDownloads((list) => list.filter((item) => item.id !== id)), []);
  return { cancel, dismiss, downloads, start };
}

/** 层 2：Range 顺序写入 + ETag 校验（探测无 ETag 时不会走到这一层）。 */
async function writeRanges(id: string, ctx: WriteContext) {
  const picker = (globalThis as unknown as SaveFilePickerWindow).showSaveFilePicker;
  if (!picker) throw new Error("showSaveFilePicker is unavailable");
  const handle = await picker({ suggestedName: ctx.entry.name });
  const writable = await handle.createWritable();
  let offset = 0;
  try {
    while (offset < ctx.size) {
      const end = Math.min(ctx.size, offset + PICKER_CHUNK_BYTES) - 1;
      const chunk = await downloadRange(ctx.scope, ctx.entry.path, offset, end, { signal: ctx.controller.signal });
      if (ctx.probe?.etag && chunk.etag && chunk.etag !== ctx.probe.etag) throw new DownloadEtagChangedError(ctx.probe.etag, chunk.etag);
      if (chunk.blob.size === 0) break;
      await writable.write(chunk.blob);
      offset += chunk.blob.size;
      ctx.patch(id, { transferred: offset });
    }
    await writable.close();
  } catch (error) {
    await writable.abort?.().catch(() => undefined);
    throw error;
  }
}

/** 层 3：fetch + ReadableStream 进度；该层没有续传能力（说明见 layerNotes 的 noResume）。 */
async function streamToBlob(id: string, ctx: WriteContext) {
  const response = await fetch(buildNativeDownloadHref(ctx.scope, ctx.entry.path), { signal: ctx.controller.signal });
  if (!response.ok || !response.body) throw new RuntimeApiError(response.status, null);
  const reader = response.body.getReader();
  const chunks: BlobPart[] = [];
  let received = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    if (value) {
      chunks.push(value);
      received += value.byteLength;
      ctx.patch(id, { transferred: received });
    }
  }
  const url = URL.createObjectURL(new Blob(chunks));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = ctx.entry.name;
  document.body.append(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
  ctx.patch(id, { size: received, transferred: received });
}
