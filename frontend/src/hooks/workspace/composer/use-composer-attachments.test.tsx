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

import {
  useComposerAttachments,
  type ComposerAttachmentsController,
  type UseComposerAttachmentsOptions,
} from "./use-composer-attachments";

type ReactActEnvironmentGlobal = typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean;
};

function makeFile(name: string, size = 4, type = "text/plain"): File {
  return new File([new Uint8Array(size)], name, { type });
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
  let options: UseComposerAttachmentsOptions;

  // 稳定的探针组件：换 prop 重渲染时保留 hook 内部状态（会话切换用例依赖此点）。
  function Probe() {
    const next = useComposerAttachments({
      ...options,
      createObjectUrl,
      revokeObjectUrl,
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

  it("keeps insertion order and marks every local draft as pending", () => {
    const attachments = renderAttachments({ threadKey: "s-1" });

    act(() =>
      attachments.addFiles([makeFile("b.txt"), makeFile("a.txt", 8, "image/png")]),
    );

    expect(controller?.attachments.map((item) => item.name)).toEqual([
      "b.txt",
      "a.txt",
    ]);
    expect(controller?.attachments.map((item) => item.status)).toEqual([
      "pending",
      "pending",
    ]);
    expect(controller?.attachments.map((item) => item.kind)).toEqual([
      "file",
      "image",
    ]);
  });

  it("previews images through the injected object URL factory only", () => {
    const attachments = renderAttachments({ threadKey: "s-1" });

    act(() => attachments.addFiles([makeFile("shot.png", 8, "image/png")]));

    expect(createObjectUrl).toHaveBeenCalledTimes(1);
    expect(controller?.attachments[0].previewUrl).toBe("blob:preview-1");
  });

  it("rejects duplicates, oversized files and over-limit files by reason", () => {
    const attachments = renderAttachments({
      threadKey: "s-1",
      maxCount: 2,
      maxBytes: 16,
    });

    act(() => attachments.addFiles([makeFile("one.txt", 4), makeFile("one.txt", 4)]));
    expect(controller?.attachments).toHaveLength(1);
    expect(controller?.rejectedCount).toBe(1);

    act(() => attachments.addFiles([makeFile("big.txt", 64)]));
    expect(controller?.attachments).toHaveLength(1);
    expect(controller?.rejectedCount).toBe(2);

    act(() =>
      attachments.addFiles([
        makeFile("two.txt", 4),
        makeFile("three.txt", 4),
        makeFile("four.txt", 4),
      ]),
    );
    expect(controller?.attachments.map((item) => item.name)).toEqual([
      "one.txt",
      "two.txt",
    ]);
    expect(controller?.rejectedCount).toBe(4);

    act(() => controller?.acknowledgeRejections());
    expect(controller?.rejectedCount).toBe(0);
  });

  it("revokes the preview URL when an attachment is removed or cleared", () => {
    const attachments = renderAttachments({ threadKey: "s-1" });
    act(() =>
      attachments.addFiles([
        makeFile("one.png", 8, "image/png"),
        makeFile("two.png", 8, "image/png"),
      ]),
    );
    const [first, second] = controller?.attachments ?? [];

    act(() => controller?.removeAttachment(first.id));
    expect(revokeObjectUrl).toHaveBeenCalledWith(first.previewUrl);
    expect(controller?.attachments.map((item) => item.name)).toEqual(["two.png"]);

    act(() => controller?.clearAttachments());
    expect(revokeObjectUrl).toHaveBeenCalledWith(second.previewUrl);
    expect(controller?.attachments).toHaveLength(0);
  });

  it("keeps attachment rails isolated per session key", () => {
    const attachments = renderAttachments({ threadKey: "s-1" });
    act(() => attachments.addFiles([makeFile("for-s1.txt")]));

    renderAttachments({ threadKey: "s-2" });
    expect(controller?.attachments).toHaveLength(0);
    act(() => controller?.addFiles([makeFile("for-s2.txt")]));

    renderAttachments({ threadKey: "s-1" });
    expect(controller?.attachments.map((item) => item.name)).toEqual([
      "for-s1.txt",
    ]);
  });

  it("toggles the drop invitation by drag depth and accepts dropped files", () => {
    renderAttachments({ threadKey: "s-1" });

    act(() => {
      window.dispatchEvent(dragEvent("dragenter", [makeFile("dropped.txt")]));
    });
    expect(controller?.isDragOver).toBe(true);

    act(() => {
      window.dispatchEvent(dragEvent("drop", [makeFile("dropped.txt")]));
    });
    expect(controller?.isDragOver).toBe(false);
    expect(controller?.attachments.map((item) => item.name)).toEqual([
      "dropped.txt",
    ]);

    // 非文件拖拽不触发邀请（文本/链接落点不受影响）。
    act(() => {
      window.dispatchEvent(new Event("dragenter", { bubbles: true }));
    });
    expect(controller?.isDragOver).toBe(false);
  });

  it("ignores dragleave bookkeeping for non-file payloads", () => {
    const attachments = renderAttachments({ threadKey: "s-1" });

    act(() => {
      window.dispatchEvent(dragEvent("dragleave", []));
    });
    expect(controller?.isDragOver).toBe(false);
    expect(attachments.attachments).toHaveLength(0);
  });

  it("recycles every preview URL on unmount", () => {
    const attachments = renderAttachments({ threadKey: "s-1" });
    act(() => attachments.addFiles([makeFile("one.png", 8, "image/png")]));
    const preview = controller?.attachments[0].previewUrl;

    act(() => root?.unmount());
    root = null;

    expect(revokeObjectUrl).toHaveBeenCalledWith(preview);
  });
});
