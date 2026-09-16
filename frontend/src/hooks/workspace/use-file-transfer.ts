// 文件上传状态机 + 未完成元数据（规划文档 §4.4 / 阶段 P1-2）。
//
// 后端契约（api/runtime/fs-transfer.ts）：init → PUT 分片（Content-Range，偏移不符返回 409 +
//   expected_offset）→ complete，中止用 DELETE；进度只认服务端 `offset`，不用本地字节数估算。
//   下载三层不在本模块——它属于传输托盘 UI（file-browser/transfer-tray.tsx）的职责。
//
// 归一化纪律：分片在单文件内**顺序**推进（服务端按 offset 校验），所以「并发 3」落在文件之间；
//   重试只覆盖网络错误与 5xx（指数退避 3 次）；FsUploadOffsetMismatchError → 先 GET 状态对齐 offset
//   再继续，绝不盲目重放；暂停/取消走 AbortController，主动取消落 canceled；localStorage["aicli.uploads"]
//   只存元数据（upload_id/scope/dir/name/size/lastModified/offset），绝不存文件内容，解析失败一律丢弃；
//   冲突策略显式：默认 fail，命中冲突（target.existed 或 409）→ conflict 等 UI 询问 overwrite/rename，
//   不默默覆盖。
//
// 降级判据：无 scope（根未就绪）时不入队；localStorage 不可用（隐私模式/配额）→ 降级为「不记忆」，
//   传输继续；单文件失败只影响自己，其它任务照常排队推进。

import { useCallback, useRef, useState } from "react";

import {
  FsUploadOffsetMismatchError,
  abortUpload,
  completeUpload,
  fetchUploadStatus,
  initUpload,
  putUploadChunk,
} from "@/api/runtime/fs-transfer";
import { RuntimeApiError } from "@/api/runtime/shared";
import type { FsConflictPolicy } from "@/types/runtime/fs-browser";

/** 分片默认 4 MiB（与后端 chunk_size 默认一致）；同时上传的文件数上限 3。 */
export const DEFAULT_UPLOAD_CHUNK_SIZE = 4 * 1024 * 1024;
export const MAX_CONCURRENT_UPLOADS = 3;
export const MAX_UPLOAD_RETRIES = 3;
export const UPLOAD_STORAGE_KEY = "aicli.uploads";

export type UploadState = "queued" | "uploading" | "paused" | "conflict" | "completed" | "failed" | "canceled";

export type UploadTask = {
  id: string; uploadId: string; dir: string; name: string; size: number; lastModified: number;
  /** 服务端确认的写入偏移（暂停继续与 409 对齐后都以它为准），同时就是进度。 */
  offset: number;
  policy: FsConflictPolicy; state: UploadState; error: unknown; targetPath: string; file: File;
};

export type RetainedUploadMetadata = {
  uploadId: string; scope: string; dir: string; name: string;
  size: number; lastModified: number; offset: number;
};

const newId = () => `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 10)}`;

export function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError";
}

/** 只重试网络错误与 5xx；4xx（409/416/404）交给各自具名分支，不在这里重放。 */
export function isRetryableUploadError(error: unknown): boolean {
  return error instanceof RuntimeApiError ? error.status >= 500 : error instanceof TypeError;
}

/** 指数退避 200ms → 400ms → 800ms（乘随机因子，避免并发重试撞车）。 */
export function backoffDelayMs(attempt: number): number {
  return Math.round(200 * 2 ** attempt * (0.5 + Math.random() * 0.5));
}

function delay(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(resolve, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(new DOMException("aborted", "AbortError"));
    };
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

/** 写一个分片：409 → 先对齐服务端 offset 再继续；网络/5xx → 退避重试。 */
async function putChunkAligned(
  uploadId: string,
  chunk: Blob,
  offset: number,
  total: number,
  signal: AbortSignal,
): Promise<number> {
  for (let attempt = 0; ; attempt += 1) {
    try {
      const result = await putUploadChunk(uploadId, chunk, offset, total, { signal });
      return result.offset >= 0 ? result.offset : offset + chunk.size;
    } catch (caught) {
      if (caught instanceof FsUploadOffsetMismatchError) {
        const status = await fetchUploadStatus(uploadId, { signal });
        return status.offset;
      }
      if (isAbortError(caught) || attempt >= MAX_UPLOAD_RETRIES || !isRetryableUploadError(caught)) {
        throw caught;
      }
      await delay(backoffDelayMs(attempt), signal);
    }
  }
}

/** 读取未完成上传元数据；任何解析异常都按「没有记录」处理。 */
export function readRetainedUploads(storage?: Storage | null): RetainedUploadMetadata[] {
  try {
    const raw = (storage ?? globalThis.localStorage)?.getItem(UPLOAD_STORAGE_KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed)
      ? (parsed.filter((item) => typeof (item as RetainedUploadMetadata | null)?.uploadId === "string") as RetainedUploadMetadata[])
      : [];
  } catch {
    return [];
  }
}

export function writeRetainedUploads(list: RetainedUploadMetadata[], storage?: Storage | null): void {
  try {
    (storage ?? globalThis.localStorage)?.setItem(UPLOAD_STORAGE_KEY, JSON.stringify(list));
  } catch {
    // 隐私模式 / 配额不足：降级为「不记忆」，不阻断传输。
  }
}

export type UseFileTransferOptions = { scope: string };

export type UseFileTransferResult = {
  uploads: UploadTask[];
  enqueueUpload: (files: FileList | File[], dir: string, policy?: FsConflictPolicy) => void;
  pauseUpload: (id: string) => void; resumeUpload: (id: string) => void; cancelUpload: (id: string) => void;
  resolveConflict: (id: string, policy: Exclude<FsConflictPolicy, "fail">) => void; dismissUpload: (id: string) => void;
};

export function useFileTransfer({ scope }: UseFileTransferOptions): UseFileTransferResult {
  const [uploads, setUploads] = useState<UploadTask[]>([]);
  const tasksRef = useRef(new Map<string, UploadTask>());
  const controllersRef = useRef(new Map<string, AbortController>());
  const runningRef = useRef(0);
  const scopeRef = useRef(scope);
  scopeRef.current = scope;

  const patch = useCallback((id: string, next: Partial<UploadTask>) => {
    const current = tasksRef.current.get(id);
    if (!current) {
      return;
    }
    const merged = { ...current, ...next };
    tasksRef.current.set(id, merged);
    setUploads((list) => list.map((item) => (item.id === id ? merged : item)));
  }, []);

  const remember = useCallback((task: UploadTask, uploadId: string, offset: number) => {
    const entry: RetainedUploadMetadata = {
      uploadId, scope: scopeRef.current, dir: task.dir, name: task.name,
      size: task.size, lastModified: task.lastModified, offset,
    };
    writeRetainedUploads([...readRetainedUploads().filter((item) => item.uploadId !== uploadId), entry]);
  }, []);

  const forget = useCallback((uploadId: string) => {
    writeRetainedUploads(readRetainedUploads().filter((item) => item.uploadId !== uploadId));
  }, []);

  const runUpload = useCallback(
    async (id: string) => {
      const task = tasksRef.current.get(id);
      if (!task) {
        return;
      }
      const controller = new AbortController();
      controllersRef.current.set(id, controller);
      const signal = controller.signal;
      const local = (next: Partial<UploadTask>) => patch(id, next);
      local({ state: "uploading", error: null });
      try {
        let { uploadId, offset } = task;
        let chunkSize = DEFAULT_UPLOAD_CHUNK_SIZE;
        if (!uploadId) {
          const init = await initUpload(
            {
              scope: scopeRef.current, dir: task.dir, name: task.name, size: task.size,
              chunkSize: DEFAULT_UPLOAD_CHUNK_SIZE, conflictPolicy: task.policy,
            },
            { signal },
          );
          ({ uploadId, offset } = init);
          chunkSize = init.chunkSize > 0 ? init.chunkSize : chunkSize;
          local({ uploadId, offset, targetPath: init.targetPath });
          if (init.completed) {
            local({ state: "completed", offset: task.size });
            return;
          }
          if (task.policy === "fail" && init.targetExists) {
            // 冲突策略显式：交给 UI 询问 overwrite/rename，不默默覆盖。
            local({ state: "conflict" });
            return;
          }
          remember(task, uploadId, offset);
        } else {
          // 继续：以服务端状态为准对齐 offset（本地进度可能落后于已写入字节）。
          const status = await fetchUploadStatus(uploadId, { signal });
          offset = status.offset;
          chunkSize = status.chunkSize > 0 ? status.chunkSize : chunkSize;
          local({ offset });
        }
        while (offset < task.size && uploadId) {
          const blob = task.file.slice(offset, Math.min(offset + chunkSize, task.size));
          offset = await putChunkAligned(uploadId, blob, offset, task.size, signal);
          local({ offset });
          remember(task, uploadId, offset);
        }
        const done = await completeUpload(uploadId, undefined, { signal });
        local({ state: "completed", offset: task.size, targetPath: done.path || task.targetPath });
        forget(uploadId);
      } catch (caught) {
        if (isAbortError(caught)) {
          return; // 暂停/取消：状态由触发方决定，主动取消不落 error 态。
        }
        local(caught instanceof RuntimeApiError && caught.status === 409 ? { state: "conflict" } : { state: "failed", error: caught });
      } finally {
        controllersRef.current.delete(id);
      }
    },
    [forget, patch, remember],
  );

  const pumpRef = useRef<() => void>(() => {});
  pumpRef.current = () => {
    while (runningRef.current < MAX_CONCURRENT_UPLOADS) {
      const next = [...tasksRef.current.values()].find((task) => task.state === "queued");
      if (!next) {
        return;
      }
      runningRef.current += 1;
      void runUpload(next.id).finally(() => {
        runningRef.current -= 1;
        pumpRef.current();
      });
    }
  };

  const enqueueUpload = useCallback((files: FileList | File[], dir: string, policy: FsConflictPolicy = "fail") => {
    const list = Array.from(files);
    if (list.length === 0 || !scopeRef.current) {
      return;
    }
    const created = list.map<UploadTask>((file) => ({
      id: newId(), uploadId: "", dir, name: file.name, size: file.size, lastModified: file.lastModified,
      offset: 0, policy, state: "queued", error: null, targetPath: "", file,
    }));
    for (const task of created) {
      tasksRef.current.set(task.id, task);
    }
    setUploads((previous) => [...previous, ...created]);
    pumpRef.current();
  }, []);

  const pauseUpload = useCallback(
    (id: string) => {
      if (!tasksRef.current.has(id)) {
        return;
      }
      patch(id, { state: "paused" });
      controllersRef.current.get(id)?.abort();
    },
    [patch],
  );

  const resumeUpload = useCallback(
    (id: string) => {
      const task = tasksRef.current.get(id);
      if (!task || task.state === "uploading" || task.state === "completed") {
        return;
      }
      patch(id, { state: "queued", error: null });
      pumpRef.current();
    },
    [patch],
  );

  const cancelUpload = useCallback(
    (id: string) => {
      const task = tasksRef.current.get(id);
      controllersRef.current.get(id)?.abort();
      if (task?.uploadId) {
        void abortUpload(task.uploadId).catch(() => undefined);
        forget(task.uploadId);
      }
      patch(id, { state: "canceled" });
    },
    [forget, patch],
  );

  const resolveConflict = useCallback(
    (id: string, policy: Exclude<FsConflictPolicy, "fail">) => {
      // 换策略必须重新 init：服务端只认 init 时的 conflict_policy，分片阶段改不了语义。
      patch(id, { policy, uploadId: "", offset: 0, state: "queued", error: null });
      pumpRef.current();
    },
    [patch],
  );

  const dismissUpload = useCallback((id: string) => {
    tasksRef.current.delete(id);
    setUploads((list) => list.filter((item) => item.id !== id));
  }, []);

  return {
    uploads,
    enqueueUpload,
    pauseUpload,
    resumeUpload,
    cancelUpload,
    resolveConflict,
    dismissUpload,
  };
}
