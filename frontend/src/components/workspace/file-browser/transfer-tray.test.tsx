// @vitest-environment jsdom

// P2-4：下载三层选择 + 传输托盘渲染单测（能力声明必须诚实：能力缺失要留话，不谎称可续传）。
//
// 三层口径（与实现同源）：
//   native → 原生 <a download>（< 8 MiB、显式点下载、> 256 MiB 或能力不足时兜底）；
//   picker → Chromium 的 showSaveFilePicker + 服务端确认支持 Range/ETag 顺序写入；
//   stream → fetch + ReadableStream 进度（无断点续传能力，必须显式说明）。

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { UploadTask } from "@/hooks/workspace/use-file-transfer";

import {
  detectDownloadCapabilities,
  layerNotes,
  NATIVE_FIRST_LIMIT_BYTES,
  selectDownloadLayer,
  STREAM_DOWNLOAD_LIMIT_BYTES,
  type DownloadCapabilities,
  type DownloadTask,
} from "./transfer-model";
import { TransferTray, type TransferTrayProps } from "./transfer-tray";

type ReactActEnvironmentGlobal = typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean };

const FULL_CAPS: DownloadCapabilities = { hasSaveFilePicker: true, hasStreamReadable: true };
const STREAM_ONLY_CAPS: DownloadCapabilities = { hasSaveFilePicker: false, hasStreamReadable: true };
const BARE_CAPS: DownloadCapabilities = { hasSaveFilePicker: false, hasStreamReadable: false };

function uploadTask(overrides: Partial<UploadTask> = {}): UploadTask {
  return {
    id: "u1",
    uploadId: "upload-1",
    dir: "docs",
    name: "a.txt",
    size: 100,
    lastModified: 0,
    offset: 0,
    policy: "fail",
    state: "uploading",
    error: null,
    targetPath: "",
    file: new File([new Uint8Array(0)], "a.txt"),
    ...overrides,
  };
}

function downloadTask(overrides: Partial<DownloadTask> = {}): DownloadTask {
  return { id: "d1", path: "docs/a.bin", name: "a.bin", size: 100, layer: "native", state: "completed", transferred: 100, ...overrides };
}

describe("下载三层选择（纯函数）", () => {
  it("小文件与显式点「下载」一律走原生下载，不占用分片写入", () => {
    expect(selectDownloadLayer({ size: NATIVE_FIRST_LIMIT_BYTES - 1, capabilities: FULL_CAPS, probe: { acceptRanges: true, etag: "e" } })).toBe("native");
    expect(selectDownloadLayer({ size: 64 * 1024 * 1024, capabilities: FULL_CAPS, probe: { acceptRanges: true, etag: "e" }, preferNative: true })).toBe("native");
  });

  it("Chromium + 服务端确认 Range/ETag 才用分片落盘；缺证据时退回流式", () => {
    const size = 32 * 1024 * 1024;
    expect(selectDownloadLayer({ size, capabilities: FULL_CAPS, probe: { acceptRanges: true, etag: "e" } })).toBe("picker");
    expect(selectDownloadLayer({ size, capabilities: FULL_CAPS, probe: { acceptRanges: false, etag: "e" } })).toBe("stream");
    expect(selectDownloadLayer({ size, capabilities: FULL_CAPS, probe: { acceptRanges: true, etag: "" } })).toBe("stream");
    expect(selectDownloadLayer({ size, capabilities: FULL_CAPS, probe: null })).toBe("stream");
  });

  it("超过 256 MiB 或缺少流式能力时禁用该层，回落到原生下载", () => {
    expect(selectDownloadLayer({ size: STREAM_DOWNLOAD_LIMIT_BYTES + 1, capabilities: FULL_CAPS, probe: { acceptRanges: true, etag: "e" } })).toBe("native");
    expect(selectDownloadLayer({ size: 32 * 1024 * 1024, capabilities: BARE_CAPS, probe: null })).toBe("native");
    expect(selectDownloadLayer({ size: -1, capabilities: STREAM_ONLY_CAPS, probe: null })).toBe("stream");
    expect(selectDownloadLayer({ size: -1, capabilities: BARE_CAPS, probe: null })).toBe("native");
  });

  it("能力说明必须诚实：流式层显式说明「无断点续传」，超大文件说明为何禁用流式", () => {
    const streamNotes = layerNotes("stream", STREAM_ONLY_CAPS, 32 * 1024 * 1024).map((note) => note.key);
    expect(streamNotes).toContain("panels.fileBrowser.transfer.layer.noResume");
    expect(streamNotes).toContain("panels.fileBrowser.transfer.layer.noPicker");
    const nativeNotes = layerNotes("native", BARE_CAPS, STREAM_DOWNLOAD_LIMIT_BYTES + 1).map((note) => note.key);
    expect(nativeNotes).toContain("panels.fileBrowser.transfer.layer.nativeTooLarge");
    expect(nativeNotes).toContain("panels.fileBrowser.transfer.layer.noStream");
    expect(layerNotes("picker", FULL_CAPS, 32 * 1024 * 1024)).toEqual([]);
  });

  it("detectDownloadCapabilities 只反映真实全局能力", () => {
    const target = globalThis as typeof globalThis & { showSaveFilePicker?: unknown };
    const original = target.showSaveFilePicker;
    delete target.showSaveFilePicker;
    expect(detectDownloadCapabilities().hasSaveFilePicker).toBe(false);
    expect(detectDownloadCapabilities().hasStreamReadable).toBe(true);
    target.showSaveFilePicker = () => undefined;
    expect(detectDownloadCapabilities().hasSaveFilePicker).toBe(true);
    if (original === undefined) {
      delete target.showSaveFilePicker;
    } else {
      target.showSaveFilePicker = original;
    }
  });
});

describe("TransferTray 渲染", () => {
  let container: HTMLDivElement;
  let root: Root | null = null;

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
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

  function renderTray(overrides: Partial<TransferTrayProps> = {}) {
    const props: TransferTrayProps = {
      uploads: [],
      downloads: [],
      retained: [],
      onPauseUpload: vi.fn(),
      onResumeUpload: vi.fn(),
      onCancelUpload: vi.fn(),
      onResolveConflict: vi.fn(),
      onDismissUpload: vi.fn(),
      onCancelDownload: vi.fn(),
      onDismissDownload: vi.fn(),
      onClose: vi.fn(),
      ...overrides,
    };
    act(() => {
      root?.render(<TransferTray {...props} />);
    });
    return props;
  }

  it("冲突态必须等用户选择：点击「覆盖」才回调 overwrite，点击「改名」回调 rename", () => {
    const onResolveConflict = vi.fn();
    renderTray({ uploads: [uploadTask({ state: "conflict" })], onResolveConflict });
    const conflict = document.body.querySelector('[data-testid="upload-conflict"]');
    expect(conflict).not.toBeNull();
    const buttons = [...(conflict?.querySelectorAll("button") ?? [])];
    expect(buttons).toHaveLength(2);
    act(() => {
      buttons[0]?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    act(() => {
      buttons[1]?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(onResolveConflict).toHaveBeenNthCalledWith(1, "u1", "overwrite");
    expect(onResolveConflict).toHaveBeenNthCalledWith(2, "u1", "rename");
  });

  it("暂停中的上传提供「继续」，进行中提供「暂停」，失败原因直接展示", () => {
    const onResumeUpload = vi.fn();
    renderTray({ uploads: [uploadTask({ state: "paused", offset: 40 }), uploadTask({ id: "u2", state: "failed", error: new Error("boom") })] });
    const paused = document.body.querySelector('[data-testid="upload-task-paused"]');
    const resume = [...(paused?.querySelectorAll("button") ?? [])].find((button) => button.textContent?.includes("继续") || button.getAttribute("aria-label")?.includes("resume"));
    expect(resume).not.toBeUndefined();
    const failed = document.body.querySelector('[data-testid="upload-task-failed"]');
    expect(failed?.textContent ?? "").toContain("boom");
    expect(document.body.querySelector('[data-testid="upload-task-uploading"]')).toBeNull();
    expect(onResumeUpload).not.toHaveBeenCalled();
  });

  it("下载任务把三层选择与「无断点续传」等说明渲染成可见文案（data-note-key 可核对）", () => {
    renderTray({
      downloads: [
        downloadTask({ id: "d2", name: "big.bin", layer: "stream", notes: layerNotes("stream", STREAM_ONLY_CAPS, 32 * 1024 * 1024) }),
      ],
    });
    const row = document.body.querySelector('[data-testid="download-task-completed"]');
    expect(row).not.toBeNull();
    const noteKeys = [...(row?.querySelectorAll("[data-note-key]") ?? [])].map((node) => node.getAttribute("data-note-key"));
    expect(noteKeys).toEqual([
      "panels.fileBrowser.transfer.layer.noPicker",
      "panels.fileBrowser.transfer.layer.noResume",
    ]);
    expect(row?.textContent ?? "").toMatch(/noResume|断点续传/);
  });

  it("残留元数据只做提示，不谎称正在传输", () => {
    renderTray({ retained: [{ uploadId: "upload-9", scope: "workspace:1", dir: "", name: "x.bin", size: 10, lastModified: 0, offset: 4 }] });
    expect(document.body.querySelector('[data-testid="retained-uploads"]')).not.toBeNull();
    expect(document.body.querySelector('[data-testid="upload-task-uploading"]')).toBeNull();
  });
});
