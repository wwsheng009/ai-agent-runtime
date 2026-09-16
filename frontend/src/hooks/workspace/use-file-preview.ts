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
//
// 路径解析（相对路径必须是相对**会话工作目录**，不是运行时进程 cwd）：
//   * 工具行路径来自 agent，可能是相对写法（如 `frontend/src/app.tsx`）；而 `POST /fs/read-file`
//     的既有契约是「相对路径按运行时进程工作目录解析」（本机实测进程 cwd 落在 `backend/`），
//     直接透传会解析成 `<repo>/backend/frontend/...` 读错文件；
//   * 因此相对路径先按 `/fs/roots?session_id=` 给出的会话根解析成绝对路径（与文件浏览器同一份根），
//     绝对路径原样透传；
//   * 根不可用（端点未注入 / 探测失败 / 无会话且无 cwd 根）时不猜测，退回原样路径，
//     由读取端点按既有契约解析并如实报错；失败不写缓存，下次打开会重新询问。

import { useCallback, useEffect, useRef, useState } from "react";

import { FILE_PREVIEW_MAX_BYTES, isFileReadUnavailable, readRuntimeFile } from "@/api/runtime/files";
import { fetchFsRoots, pickPreviewRoot } from "@/api/runtime/fs-roots";
import { decodeFilePreview, type FilePreviewBody } from "@/lib/file-preview/decode";
import { isAbsoluteFilePath, toAbsolutePathFromRoot } from "@/lib/file-browser/path-utils";
import type { RuntimeFileReadResult } from "@/types/runtime";

export type FilePreviewStatus = "closed" | "loading" | "ready" | "error";

export type FilePreviewSnapshot = {
  status: FilePreviewStatus;
  /** 触发预览的原始路径（工具行给出的路径，原样保留供展示与重试；请求用的解析结果见 `result.path`）。 */
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

export type UseFilePreviewOptions = {
  /**
   * 会话 id：用于把「相对会话工作目录」的工具行路径解析成绝对路径
   * （`GET /fs/roots?session_id=`，与文件浏览器共用同一份作用域根）。
   * 缺省 = 不做解析，相对路径原样交给读取端点（后端按运行时进程工作目录解析）。
   */
  sessionId?: string | null;
};

export function useFilePreview(options: UseFilePreviewOptions = {}): UseFilePreviewResult {
  const sessionId = options.sessionId?.trim() ?? "";
  const [snapshot, setSnapshot] = useState<FilePreviewSnapshot>(initialState);
  const requestSeq = useRef(0);
  const activeController = useRef<AbortController | null>(null);
  /**
   * 解析基准（会话工作目录绝对路径）。同一次会话内作用域根不变，取到就缓存；
   * 空串 = 服务端明确没有可用基准（既无会话根也无 cwd 根），同样缓存，避免每次打开都白问。
   * 请求失败不写缓存：端点未注入属临时降级，下次打开重新询问，服务端恢复后自愈。
   */
  const rootPathRef = useRef<{ sessionId: string; path: string } | null>(null);

  const abortActive = useCallback(() => {
    activeController.current?.abort();
    activeController.current = null;
  }, []);

  useEffect(() => abortActive, [abortActive]);

  /** 把工具行路径解析成读取请求路径：绝对路径原样、相对路径按会话根拼接、根未知则原样。 */
  const resolveRequestPath = useCallback(
    async (path: string, signal: AbortSignal): Promise<string> => {
      const trimmed = path.trim();
      if (!trimmed || isAbsoluteFilePath(trimmed)) {
        return trimmed;
      }
      if (rootPathRef.current?.sessionId !== sessionId) {
        try {
          const { roots } = await fetchFsRoots({ sessionId: sessionId || null, signal });
          rootPathRef.current = { sessionId, path: pickPreviewRoot(roots)?.path ?? "" };
        } catch (caught) {
          // 取消必须上抛（调用方按「非错误」处理，不得落错误态）；其余失败（503/探测失败）
          // 不缓存、不猜测，退回原样路径。
          if (isAbortError(caught)) {
            throw caught;
          }
        }
      }
      const basePath = rootPathRef.current?.path ?? "";
      return basePath ? toAbsolutePathFromRoot(basePath, trimmed) : trimmed;
    },
    [sessionId],
  );

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
          const requestPath = await resolveRequestPath(path, controller.signal);
          if (requestSeq.current !== seq) {
            return;
          }
          const result = await readRuntimeFile(requestPath, { signal: controller.signal });
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
    [abortActive, resolveRequestPath],
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
