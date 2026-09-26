// @vitest-environment jsdom

import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  vi,
  type Mock,
} from "vitest";

import { RuntimeApiError } from "@/api/runtime/shared";
import {
  useComposerAttachments,
  type ComposerAttachmentsController,
  type UseComposerAttachmentsOptions,
} from "./use-composer-attachments";
import type { RuntimeUploadResult } from "@/types/runtime";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function makeFile(name: string, size = 4, type = "image/png"): File {
  return new File([new Uint8Array(size)], name, { type });
}

function uploadOk(
  name: string,
  path = `/srv/uploads/${name}`,
): RuntimeUploadResult {
  return {
    ok: true,
    accepted: 1,
    attachments: [
      { name, path, bytes: 4, width: 2, height: 2, note: "", skipped: false },
    ],
  };
}

async function flush() {
  await act(async () => {
    await Promise.resolve();
  });
}

/** jsdom 无 DataTransfer：手工挂载拖放负载所需的最小字段。 */
function dragEvent(type: string, files: File[]): Event {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(event, "dataTransfer", {
    value: {
      types: files.length > 0 ? ["Files"] : [],
      files,
    },
  });
  return event;
}

describe("useComposerAttachments", () => {
  let container: HTMLDivElement;
  let root: Root | null;
  let controller: ComposerAttachmentsController | null;
  let createObjectUrl: Mock<(file: File) => string | null>;
  let revokeObjectUrl: Mock<(url: string) => void>;
  let uploadFile: Mock<
    (file: File, options: { signal: AbortSignal }) => Promise<RuntimeUploadResult>
  >;
  let options: UseComposerAttachmentsOptions;

  // 稳定的探针组件：换 prop 重渲染时保留 hook 内部状态（会话切换用例依赖此点）。
  function Probe() {
    const next = useComposerAttachments({
      ...options,
      createObjectUrl,
      revokeObjectUrl,
      uploadFile,
    });
    useEffect(() => {
      controller = next;
    });
    return null;
  }

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    controller = null;
    let seq = 0;
    createObjectUrl = vi.fn<(file: File) => string | null>(
      () => `blob:preview-${(seq += 1)}`,
    );
    revokeObjectUrl = vi.fn<(url: string) => void>();
    uploadFile = vi.fn(async (file: File) => uploadOk(file.name));
    (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    if (root) {
      act(() => root?.unmount());
    }
    container.remove();
    document.body.innerHTML = "";
    delete (globalThis as ReactActEnvironmentGlobal).IS_REACT_ACT_ENVIRONMENT;
  });

  function renderAttachments(next: UseComposerAttachmentsOptions) {
    options = next;
    act(() => root?.render(<Probe />));
    return controller as ComposerAttachmentsController;
  }

  it("keeps insertion order, uploads on add and carries the server path", async () => {
    const attachments = renderAttachments({ threadKey: "s-1" });

    act(() =>
      attachments.addFiles([makeFile("b.png"), makeFile("a.png", 8, "image/jpeg")]),
    );

    // 入轨瞬间：全部是 uploading（不伪造已上传）。
    expect(controller?.attachments.map((item) => item.name)).toEqual([
      "b.png",
      "a.png",
    ]);
    expect(controller?.attachments.map((item) => item.status)).toEqual([
      "uploading",
      "uploading",
    ]);
    expect(controller?.attachments.every((item) => item.remotePath === null)).toBe(
      true,
    );

    await flush();

    expect(uploadFile).toHaveBeenCalledTimes(2);
    expect(controller?.attachments.map((item) => item.status)).toEqual([
      "uploaded",
      "uploaded",
    ]);
    expect(controller?.attachments.map((item) => item.remotePath)).toEqual([
      "/srv/uploads/b.png",
      "/srv/uploads/a.png",
    ]);
    expect(controller?.uploadedPaths).toEqual([
      "/srv/uploads/b.png",
      "/srv/uploads/a.png",
    ]);
    expect(controller?.uploadedCount).toBe(2);
    expect(controller?.unsettledCount).toBe(0);
  });

  it("keeps the local preview and a retryable error when the upload fails", async () => {
    uploadFile.mockRejectedValueOnce(
      new RuntimeApiError(
        503,
        {
          error: "uploads_unavailable",
          reason: "本服务未配置附件上传目录",
        } as never,
      ),
    );
    const attachments = renderAttachments({ threadKey: "s-1" });

    act(() => attachments.addFiles([makeFile("shot.png")]));
    await flush();

    const failed = controller?.attachments[0];
    expect(failed?.status).toBe("error");
    expect(failed?.errorKind).toBe("unavailable");
    expect(failed?.error).toBe("本服务未配置附件上传目录");
    expect(failed?.remotePath).toBeNull();
    // 失败保留本地预览（可继续重试/移除），且不计入可发送路径。
    expect(failed?.previewUrl).toBe("blob:preview-1");
    expect(controller?.uploadedPaths).toEqual([]);
    expect(controller?.unsettledCount).toBe(1);

    uploadFile.mockResolvedValueOnce(uploadOk("shot.png"));
    act(() => controller?.retryAttachment(failed?.id ?? ""));
    expect(controller?.attachments[0].status).toBe("uploading");
    await flush();

    expect(controller?.attachments[0].status).toBe("uploaded");
    expect(controller?.attachments[0].remotePath).toBe("/srv/uploads/shot.png");
    expect(controller?.attachments[0].error).toBeNull();
  });

  it("turns backend-skipped files into actionable errors (no fake upload)", async () => {
    uploadFile.mockResolvedValueOnce({
      ok: true,
      accepted: 0,
      attachments: [
        {
          name: "notes.txt",
          path: "",
          bytes: 0,
          width: 0,
          height: 0,
          note: "不是可识别的图片",
          skipped: true,
        },
      ],
    });
    const attachments = renderAttachments({ threadKey: "s-1" });

    act(() => attachments.addFiles([makeFile("notes.txt", 4, "text/plain")]));
    await flush();

    expect(controller?.attachments[0].status).toBe("error");
    expect(controller?.attachments[0].errorKind).toBe("rejected");
    expect(controller?.attachments[0].error).toBe("不是可识别的图片");
    expect(controller?.uploadedPaths).toEqual([]);
  });

  it("never resurrects a removed attachment when a late upload response lands", async () => {
    let resolveLate: ((value: RuntimeUploadResult) => void) | null = null;
    uploadFile.mockImplementationOnce(
      () =>
        new Promise<RuntimeUploadResult>((resolve) => {
          resolveLate = resolve;
        }),
    );
    const attachments = renderAttachments({ threadKey: "s-1" });

    act(() => attachments.addFiles([makeFile("slow.png")]));
    const id = controller?.attachments[0].id ?? "";
    act(() => controller?.removeAttachment(id));
    expect(controller?.attachments).toHaveLength(0);

    // 迟到的成功响应必须被丢弃：条目已移除，不能复活成「已上传」。
    act(() => resolveLate?.(uploadOk("slow.png", "/srv/uploads/late.png")));
    await flush();

    expect(controller?.attachments).toHaveLength(0);
    expect(controller?.uploadedPaths).toEqual([]);
  });

  it("rejects duplicates, oversized files and over-limit files by reason", async () => {
    const attachments = renderAttachments({
      threadKey: "s-1",
      maxCount: 2,
      maxBytes: 16,
    });

    act(() => attachments.addFiles([makeFile("one.png", 4), makeFile("one.png", 4)]));
    expect(controller?.attachments).toHaveLength(1);
    expect(controller?.rejectedCount).toBe(1);

    act(() => attachments.addFiles([makeFile("big.png", 64)]));
    expect(controller?.attachments).toHaveLength(1);
    expect(controller?.rejectedCount).toBe(2);

    act(() =>
      attachments.addFiles([
        makeFile("two.png", 4),
        makeFile("three.png", 4),
        makeFile("four.png", 4),
      ]),
    );
    expect(controller?.attachments.map((item) => item.name)).toEqual([
      "one.png",
      "two.png",
    ]);
    expect(controller?.rejectedCount).toBe(4);
    // 重复文件不会重复上传。
    await flush();
    expect(uploadFile).toHaveBeenCalledTimes(2);

    act(() => controller?.acknowledgeRejections());
    expect(controller?.rejectedCount).toBe(0);
  });

  it("revokes preview URLs and aborts in-flight uploads on remove / clear", async () => {
    uploadFile.mockImplementation(
      (_file, options) =>
        new Promise<RuntimeUploadResult>((_resolve, reject) => {
          options.signal.addEventListener("abort", () =>
            reject(new DOMException("aborted", "AbortError")),
          );
        }),
    );
    const attachments = renderAttachments({ threadKey: "s-1" });
    act(() =>
      attachments.addFiles([makeFile("one.png", 8), makeFile("two.png", 8)]),
    );
    const [first, second] = controller?.attachments ?? [];

    act(() => controller?.removeAttachment(first.id));
    expect(revokeObjectUrl).toHaveBeenCalledWith(first.previewUrl);
    expect(controller?.attachments.map((item) => item.name)).toEqual(["two.png"]);

    act(() => controller?.clearAttachments());
    expect(revokeObjectUrl).toHaveBeenCalledWith(second.previewUrl);
    expect(controller?.attachments).toHaveLength(0);
    // 主动取消不落错误态：轨道已经空了，也不会再冒出一条失败条目。
    await flush();
    expect(controller?.attachments).toHaveLength(0);
  });

  it("keeps attachment rails isolated per session key", async () => {
    const attachments = renderAttachments({ threadKey: "s-1" });
    act(() => attachments.addFiles([makeFile("for-s1.png")]));
    await flush();

    renderAttachments({ threadKey: "s-2" });
    expect(controller?.attachments).toHaveLength(0);
    act(() => controller?.addFiles([makeFile("for-s2.png")]));
    await flush();

    renderAttachments({ threadKey: "s-1" });
    expect(controller?.attachments.map((item) => item.name)).toEqual([
      "for-s1.png",
    ]);
    expect(controller?.uploadedPaths).toEqual(["/srv/uploads/for-s1.png"]);
  });

  it("toggles the drop invitation by drag depth and accepts dropped files", async () => {
    renderAttachments({ threadKey: "s-1" });

    act(() => {
      window.dispatchEvent(dragEvent("dragenter", [makeFile("dropped.png")]));
    });
    expect(controller?.isDragOver).toBe(true);

    act(() => {
      window.dispatchEvent(dragEvent("drop", [makeFile("dropped.png")]));
    });
    expect(controller?.isDragOver).toBe(false);
    expect(controller?.attachments.map((item) => item.name)).toEqual([
      "dropped.png",
    ]);

    // 非文件拖拽不触发邀请（文本/链接落点不受影响）。
    act(() => {
      window.dispatchEvent(new Event("dragenter", { bubbles: true }));
    });
    expect(controller?.isDragOver).toBe(false);
    // 落点触发的上传在微任务里收尾：等它落定再结束用例（避免 act 之外的更新）。
    await flush();
  });

  it("ignores dragleave bookkeeping for non-file payloads", () => {
    const attachments = renderAttachments({ threadKey: "s-1" });

    act(() => {
      window.dispatchEvent(dragEvent("dragleave", []));
    });
    expect(controller?.isDragOver).toBe(false);
    expect(attachments.attachments).toHaveLength(0);
  });

  it("recycles every preview URL on unmount", async () => {
    const attachments = renderAttachments({ threadKey: "s-1" });
    act(() => attachments.addFiles([makeFile("one.png", 8)]));
    await flush();
    const preview = controller?.attachments[0].previewUrl;

    act(() => root?.unmount());
    root = null;

    expect(revokeObjectUrl).toHaveBeenCalledWith(preview);
  });
});
