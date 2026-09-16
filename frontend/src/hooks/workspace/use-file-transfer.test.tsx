// @vitest-environment jsdom

// P2-3：上传状态机与本地元数据单测（分片 4 MiB / 并发与重试口径 / 409 对齐 / 冲突显式询问）。
//
// 断言纪律：进度只认服务端返回的 `offset`（不拿「已发送」充数）；409 必须走 GET 状态重新对齐而不是原地重放
// 同一偏移；冲突策略默认 `fail`，overwrite/rename 只有 UI 显式选择后才下发。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { FsUploadInitResult, FsUploadStatus } from "@/types/runtime/fs-browser";

const { abortUploadMock, completeUploadMock, fetchUploadStatusMock, initUploadMock, putUploadChunkMock } = vi.hoisted(() => ({
  abortUploadMock: vi.fn(),
  completeUploadMock: vi.fn(),
  fetchUploadStatusMock: vi.fn(),
  initUploadMock: vi.fn(),
  putUploadChunkMock: vi.fn(),
}));

vi.mock("@/api/runtime/fs-transfer", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/runtime/fs-transfer")>();
  return {
    ...actual,
    abortUpload: abortUploadMock,
    completeUpload: completeUploadMock,
    fetchUploadStatus: fetchUploadStatusMock,
    initUpload: initUploadMock,
    putUploadChunk: putUploadChunkMock,
  };
});

import { FsUploadOffsetMismatchError } from "@/api/runtime/fs-transfer";
import { RuntimeApiError } from "@/api/runtime/shared";

import {
  backoffDelayMs,
  DEFAULT_UPLOAD_CHUNK_SIZE,
  isRetryableUploadError,
  readRetainedUploads,
  useFileTransfer,
  writeRetainedUploads,
  type RetainedUploadMetadata,
  type UseFileTransferResult,
} from "./use-file-transfer";

type ReactActEnvironmentGlobal = typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean };

function flush() {
  return Promise.resolve().then(() => Promise.resolve()).then(() => Promise.resolve());
}

function memoryStorage(): Storage {
  const map = new Map<string, string>();
  return {
    get length() {
      return map.size;
    },
    clear: () => map.clear(),
    getItem: (key: string) => map.get(key) ?? null,
    key: (index: number) => [...map.keys()][index] ?? null,
    removeItem: (key: string) => {
      map.delete(key);
    },
    setItem: (key: string, value: string) => {
      map.set(key, value);
    },
  } as Storage;
}

function initResult(overrides: Partial<FsUploadInitResult> = {}): FsUploadInitResult {
  return {
    uploadId: "upload-1",
    offset: 0,
    received: 0,
    chunkSize: DEFAULT_UPLOAD_CHUNK_SIZE,
    expiresAt: 0,
    completed: false,
    deduplicated: false,
    targetPath: "a.txt",
    targetExists: false,
    ...overrides,
  };
}

function uploadStatus(overrides: Partial<FsUploadStatus> = {}): FsUploadStatus {
  return {
    uploadId: "upload-1",
    received: 0,
    offset: 0,
    chunkSize: DEFAULT_UPLOAD_CHUNK_SIZE,
    size: 100,
    expiresAt: 0,
    targetPath: "a.txt",
    conflictPolicy: "fail",
    ...overrides,
  };
}

function Harness({ scope, onSnapshot }: { scope: string; onSnapshot: (result: UseFileTransferResult) => void }) {
  onSnapshot(useFileTransfer({ scope }));
  return null;
}

describe("use-file-transfer 纯函数", () => {
  it("backoffDelayMs 落在指数区间内（200/400/800ms 抖动，不超过上界）", () => {
    for (const [attempt, base] of [0, 1, 2].map((value) => [value, 200 * 2 ** value] as const)) {
      const delay = backoffDelayMs(attempt);
      expect(delay).toBeGreaterThanOrEqual(base * 0.5);
      expect(delay).toBeLessThanOrEqual(base);
    }
  });

  it("isRetryableUploadError：只重试网络错误与 5xx，4xx（409/416）交给具名分支", () => {
    expect(isRetryableUploadError(new RuntimeApiError(503, null))).toBe(true);
    expect(isRetryableUploadError(new RuntimeApiError(409, null))).toBe(false);
    expect(isRetryableUploadError(new TypeError("Failed to fetch"))).toBe(true);
    expect(isRetryableUploadError(new Error("boom"))).toBe(false);
  });

  it("元数据只存 upload_id/scope/dir/name/size/offset：坏数据按「没有记录」处理", () => {
    const storage = memoryStorage();
    const entry: RetainedUploadMetadata = {
      uploadId: "upload-1",
      scope: "workspace:1",
      dir: "docs",
      name: "a.txt",
      size: 100,
      lastModified: 42,
      offset: 60,
    };
    writeRetainedUploads([entry], storage);
    expect(readRetainedUploads(storage)).toEqual([entry]);
    const raw = storage.getItem("aicli.uploads") ?? "";
    expect(raw).not.toContain("lastModified\n");
    expect(JSON.parse(raw)[0]).not.toHaveProperty("file");
    expect(JSON.parse(raw)[0]).not.toHaveProperty("content");

    storage.setItem("aicli.uploads", "{not json");
    expect(readRetainedUploads(storage)).toEqual([]);
    expect(readRetainedUploads(null)).toEqual([]);
  });
});

describe("useFileTransfer 上传状态机", () => {
  let container: HTMLDivElement;
  let root: Root | null = null;
  let latest: UseFileTransferResult | null = null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
    latest = null;
    for (const mock of [abortUploadMock, completeUploadMock, fetchUploadStatusMock, initUploadMock, putUploadChunkMock]) {
      mock.mockReset();
    }
    // abortUpload 的线上实现是 Promise，mock 也必须保持「可 .catch」的形状。
    abortUploadMock.mockResolvedValue(undefined);
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    root = null;
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderHook() {
    act(() => {
      root?.render(<Harness scope="workspace:1" onSnapshot={(result) => { latest = result; }} />);
    });
  }

  function makeFile(bytes: number, name = "a.txt") {
    return new File([new Uint8Array(bytes)], name, { lastModified: 123 });
  }

  it("顺利路径：按 4 MiB 分片写入，进度用服务端 offset，完成后清理本地元数据", async () => {
    initUploadMock.mockResolvedValue(initResult({ offset: 0 }));
    putUploadChunkMock.mockResolvedValue({ received: 100, offset: 100 });
    completeUploadMock.mockResolvedValue({ path: "a.txt", size: 100, action: "create" });
    renderHook();
    await act(async () => {
      latest?.enqueueUpload([makeFile(100)], "docs");
      await flush();
    });
    expect(initUploadMock).toHaveBeenCalledWith(
      expect.objectContaining({ scope: "workspace:1", dir: "docs", name: "a.txt", size: 100, chunkSize: DEFAULT_UPLOAD_CHUNK_SIZE, conflictPolicy: "fail" }),
      expect.objectContaining({ signal: expect.anything() }),
    );
    expect(putUploadChunkMock).toHaveBeenCalledTimes(1);
    expect(putUploadChunkMock.mock.calls[0]?.[2]).toBe(0);
    const task = latest?.uploads[0];
    expect(task?.state).toBe("completed");
    expect(task?.offset).toBe(100);
    expect(task?.targetPath).toBe("a.txt");
    expect(readRetainedUploads(memoryStorage())).toEqual([]);
  });

  it("目标已存在且策略为 fail：进入 conflict 等待用户选择，不自动覆盖", async () => {
    initUploadMock.mockResolvedValueOnce(initResult({ targetExists: true }));
    initUploadMock.mockResolvedValueOnce(initResult({ uploadId: "upload-2", targetPath: "a (1).txt" }));
    putUploadChunkMock.mockResolvedValue({ received: 100, offset: 100 });
    completeUploadMock.mockResolvedValue({ path: "a (1).txt", size: 100, action: "rename" });
    renderHook();
    await act(async () => {
      latest?.enqueueUpload([makeFile(100)], "docs");
      await flush();
    });
    const id = latest?.uploads[0]?.id ?? "";
    expect(latest?.uploads[0]?.state).toBe("conflict");
    expect(putUploadChunkMock).not.toHaveBeenCalled();
    expect(initUploadMock.mock.calls[0]?.[0]).toMatchObject({ conflictPolicy: "fail" });

    await act(async () => {
      latest?.resolveConflict(id, "rename");
      await flush();
    });
    expect(initUploadMock.mock.calls[1]?.[0]).toMatchObject({ conflictPolicy: "rename" });
    expect(latest?.uploads[0]?.state).toBe("completed");
    expect(latest?.uploads[0]?.targetPath).toBe("a (1).txt");
  });

  it("409 offset 不匹配：先 GET 服务端状态对齐偏移再续传，不盲目重放", async () => {
    initUploadMock.mockResolvedValue(initResult({ offset: 0 }));
    putUploadChunkMock.mockRejectedValueOnce(new FsUploadOffsetMismatchError(0));
    fetchUploadStatusMock.mockResolvedValue(uploadStatus({ offset: 60, received: 60 }));
    putUploadChunkMock.mockResolvedValueOnce({ received: 100, offset: 100 });
    completeUploadMock.mockResolvedValue({ path: "a.txt", size: 100, action: "create" });
    renderHook();
    await act(async () => {
      latest?.enqueueUpload([makeFile(100)], "");
      await flush();
    });
    expect(fetchUploadStatusMock).toHaveBeenCalledWith("upload-1", expect.objectContaining({ signal: expect.anything() }));
    expect(putUploadChunkMock).toHaveBeenCalledTimes(2);
    expect(putUploadChunkMock.mock.calls[1]?.[2]).toBe(60);
    expect(latest?.uploads[0]?.state).toBe("completed");
    expect(latest?.uploads[0]?.offset).toBe(100);
  });

  it("暂停/继续：暂停保留元数据与偏移，继续时以服务端状态续传；取消不再写入", async () => {
    initUploadMock.mockResolvedValue(initResult({ offset: 0 }));
    putUploadChunkMock.mockImplementation(() => new Promise(() => {}));
    renderHook();
    await act(async () => {
      latest?.enqueueUpload([makeFile(100)], "");
      await flush();
    });
    const id = latest?.uploads[0]?.id ?? "";
    await act(async () => {
      latest?.pauseUpload(id);
      await flush();
    });
    expect(latest?.uploads[0]?.state).toBe("paused");
    expect(latest?.uploads[0]?.uploadId).toBe("upload-1");
    expect(latest?.uploads[0]?.offset).toBe(0);

    fetchUploadStatusMock.mockResolvedValue(uploadStatus({ offset: 60, received: 60 }));
    putUploadChunkMock.mockResolvedValue({ received: 100, offset: 100 });
    initUploadMock.mockClear();
    completeUploadMock.mockResolvedValue({ path: "a.txt", size: 100, action: "create" });
    await act(async () => {
      latest?.resumeUpload(id);
      await flush();
    });
    expect(initUploadMock).not.toHaveBeenCalled();
    expect(putUploadChunkMock.mock.calls.at(-1)?.[2]).toBe(60);
    expect(latest?.uploads[0]?.state).toBe("completed");

    await act(async () => {
      latest?.cancelUpload(id);
      await flush();
    });
    expect(latest?.uploads[0]?.state).toBe("canceled");
  });
});
