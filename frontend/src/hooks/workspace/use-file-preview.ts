// P2-1A：文件预览状态机（closed → loading → ready / error）。
//
// 竞态与生命周期（与 use-session-search 同口径）：
//   * 每次 open / retry 递增请求序号，只有最新一次的结果能写回 state；
//   * 新请求与关闭弹层都会中止在途请求（AbortController）；
//   * 主动取消（AbortError）不落错误态；超时与真实失败照常呈现。
//
// 降级与收口：
//   * 404/405/501/503 → unavailable=true（如实提示端点不可用）；
//   * 超过预览上限 → tooLarge=true，不渲染内容（只呈现真实字节数）；
//   * 二进制/空文件由解码层给出结论，UI 不伪造文本。

import { useCallback, useEffect, useRef, useState } from "react";

import { FILE_PREVIEW_MAX_BYTES, isFileReadUnavailable, readRuntimeFile } from "@/api/runtime/files";
import { decodeFilePreview, type FilePreviewBody } from "@/lib/file-preview/decode";
import type { RuntimeFileReadResult } from "@/types/runtime";

export type FilePreviewStatus = "closed" | "loading" | "ready" | "error";

export type FilePreviewSnapshot = {
  status: FilePreviewStatus;
  /** 触发预览的原始路径（工具行给出的路径；后端解析后的绝对路径见 result.path）。 */
  requestedPath: string;
  result: RuntimeFileReadResult | null;
  body: FilePreviewBody | null;
  error: unknown;
  unavailable: boolean;
  tooLarge: boolean;
};

const initialState: FilePreviewSnapshot = {
  status: "closed",
  requestedPath: "",
  result: null,
  body: null,
  error: null,
  unavailable: false,
  tooLarge: false,
};

export type UseFilePreviewResult = FilePreviewSnapshot & {
  open: (path: string) => void;
  close: () => void;
  retry: () => void;
};

export function useFilePreview(): UseFilePreviewResult {
  const [snapshot, setSnapshot] = useState<FilePreviewSnapshot>(initialState);
  const requestSeq = useRef(0);
  const activeController = useRef<AbortController | null>(null);

  const abortActive = useCallback(() => {
    activeController.current?.abort();
    activeController.current = null;
  }, []);

  useEffect(() => abortActive, [abortActive]);

  const load = useCallback(
    (path: string) => {
      abortActive();
      const seq = requestSeq.current + 1;
      requestSeq.current = seq;

      const controller = new AbortController();
      activeController.current = controller;
      setSnapshot({
        status: "loading",
        requestedPath: path,
        result: null,
        body: null,
        error: null,
        unavailable: false,
        tooLarge: false,
      });

      void (async () => {
        try {
          const result = await readRuntimeFile(path, { signal: controller.signal });
          if (requestSeq.current !== seq) {
            return;
          }
          if (result.byteCount > FILE_PREVIEW_MAX_BYTES) {
            setSnapshot({
              status: "ready",
              requestedPath: path,
              result,
              body: null,
              error: null,
              unavailable: false,
              tooLarge: true,
            });
            return;
          }
          setSnapshot({
            status: "ready",
            requestedPath: path,
            result,
            body: decodeFilePreview(result.dataBase64),
            error: null,
            unavailable: false,
            tooLarge: false,
          });
        } catch (caught) {
          if (requestSeq.current !== seq || isAbortError(caught)) {
            return;
          }
          setSnapshot({
            status: "error",
            requestedPath: path,
            result: null,
            body: null,
            error: caught,
            unavailable: isFileReadUnavailable(caught),
            tooLarge: false,
          });
        } finally {
          if (activeController.current === controller) {
            activeController.current = null;
          }
        }
      })();
    },
    [abortActive],
  );

  const close = useCallback(() => {
    abortActive();
    requestSeq.current += 1;
    setSnapshot(initialState);
  }, [abortActive]);

  const retry = useCallback(() => {
    if (!snapshot.requestedPath) {
      return;
    }
    load(snapshot.requestedPath);
  }, [load, snapshot.requestedPath]);

  return { ...snapshot, open: load, close, retry };
}

/** 主动取消（AbortController.abort）不落错误态；超时（TimeoutError）照常报错。 */
function isAbortError(error: unknown): boolean {
  if (typeof DOMException !== "undefined" && error instanceof DOMException) {
    return error.name === "AbortError";
  }
  return error instanceof Error && error.name === "AbortError";
}
