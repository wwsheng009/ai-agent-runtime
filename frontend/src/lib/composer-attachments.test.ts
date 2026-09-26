import { describe, expect, it } from "vitest";

import {
  classifyComposerAttachment,
  composerAttachmentIdentity,
  composerAttachmentUploadedPaths,
  countUnsettledComposerAttachments,
  createComposerAttachment,
  formatComposerAttachmentSize,
  hasComposerFilePayload,
  hasUnsettledComposerAttachments,
  planComposerAttachmentAdd,
  resolveComposerAttachmentUploadOutcome,
  type ComposerAttachment,
  type ComposerAttachmentCandidate,
} from "./composer-attachments";

function attachment(name: string, size: number, type: string): ComposerAttachment {
  return {
    id: `id-${name}`,
    kind: classifyComposerAttachment(type),
    name,
    size,
    mimeType: type,
    previewUrl: null,
    status: "uploaded",
    remotePath: `/srv/${name}`,
    remoteNote: "",
    error: null,
    errorKind: null,
    file: new File([new Uint8Array(size)], name, { type }),
  };
}

function candidate(
  name: string,
  size = 10,
  type = "text/plain",
): ComposerAttachmentCandidate {
  return { name, size, type };
}

describe("classifyComposerAttachment", () => {
  it("treats image/* as image and everything else as file", () => {
    expect(classifyComposerAttachment("image/png")).toBe("image");
    expect(classifyComposerAttachment("image/svg+xml")).toBe("image");
    expect(classifyComposerAttachment("application/pdf")).toBe("file");
    expect(classifyComposerAttachment("")).toBe("file");
  });
});

describe("composerAttachmentIdentity", () => {
  it("keys by name+size+type so identical names with different bodies stay distinct", () => {
    expect(composerAttachmentIdentity(candidate("a.txt", 1, "text/plain"))).toBe(
      composerAttachmentIdentity(candidate("a.txt", 1, "text/plain")),
    );
    expect(composerAttachmentIdentity(candidate("a.txt", 1, "text/plain"))).not.toBe(
      composerAttachmentIdentity(candidate("a.txt", 2, "text/plain")),
    );
  });

  it("matches a stored attachment (mimeType) against a raw candidate (type)", () => {
    expect(
      composerAttachmentIdentity(attachment("a.txt", 10, "text/plain")),
    ).toBe(composerAttachmentIdentity(candidate("a.txt", 10, "text/plain")));
  });
});

describe("hasComposerFilePayload", () => {
  it("detects the Files drag type only", () => {
    expect(hasComposerFilePayload(["Files"])).toBe(true);
    expect(hasComposerFilePayload(["text/plain", "Files"])).toBe(true);
    expect(hasComposerFilePayload(["text/plain"])).toBe(false);
    expect(hasComposerFilePayload(undefined)).toBe(false);
  });
});

describe("planComposerAttachmentAdd", () => {
  it("keeps incoming order and appends after current attachments", () => {
    const current = [attachment("old.txt", 10, "text/plain")];
    const plan = planComposerAttachmentAdd(current, [
      candidate("b.txt"),
      candidate("a.txt"),
    ]);

    expect(plan.rejections).toEqual([]);
    expect(plan.accepted.map((item) => item.name)).toEqual(["b.txt", "a.txt"]);
  });

  it("rejects duplicates against both current and already accepted files", () => {
    const current = [attachment("same.txt", 10, "text/plain")];
    const plan = planComposerAttachmentAdd(current, [
      candidate("same.txt"),
      candidate("fresh.txt"),
      candidate("fresh.txt"),
    ]);

    expect(plan.accepted.map((item) => item.name)).toEqual(["fresh.txt"]);
    expect(plan.rejections).toEqual([
      { reason: "duplicate", name: "same.txt" },
      { reason: "duplicate", name: "fresh.txt" },
    ]);
  });

  it("rejects oversized files before counting them against the limit", () => {
    const plan = planComposerAttachmentAdd([], [candidate("big.bin", 2048)], {
      maxBytes: 1024,
    });

    expect(plan.accepted).toEqual([]);
    expect(plan.rejections).toEqual([{ reason: "too-large", name: "big.bin" }]);
  });

  it("rejects everything past maxCount and reports the rest as over-limit", () => {
    const current = [attachment("one.txt", 10, "text/plain")];
    const plan = planComposerAttachmentAdd(
      current,
      [candidate("two.txt"), candidate("three.txt"), candidate("four.txt")],
      { maxCount: 2 },
    );

    expect(plan.accepted.map((item) => item.name)).toEqual(["two.txt"]);
    expect(plan.rejections).toEqual([
      { reason: "over-limit", name: "three.txt" },
      { reason: "over-limit", name: "four.txt" },
    ]);
  });
});

describe("createComposerAttachment", () => {
  it("starts in the uploading state and never fabricates an upload", () => {
    const file = new File([new Uint8Array(4)], "shot.png", { type: "image/png" });
    const created = createComposerAttachment(file, {
      id: "a-1",
      previewUrl: "blob:preview",
    });

    expect(created).toMatchObject({
      id: "a-1",
      kind: "image",
      name: "shot.png",
      size: 4,
      mimeType: "image/png",
      previewUrl: "blob:preview",
      status: "uploading",
      remotePath: null,
      remoteNote: "",
      error: null,
      errorKind: null,
    });
    expect(created.file).toBe(file);
  });

  it("leaves non-image previews empty", () => {
    const file = new File([new Uint8Array(4)], "notes.txt", { type: "text/plain" });
    expect(createComposerAttachment(file, { id: "a-2" }).previewUrl).toBeNull();
  });
});

describe("upload outcome resolution", () => {
  function uploaded(
    overrides: Partial<ComposerAttachment> = {},
  ): ComposerAttachment {
    return {
      ...createComposerAttachment(
        new File([new Uint8Array(4)], "shot.png", { type: "image/png" }),
        { id: "a-1" },
      ),
      ...overrides,
    };
  }

  function result(entries: Array<Partial<{ name: string; path: string; note: string; skipped: boolean }>>) {
    return {
      attachments: entries.map((entry) => ({
        name: entry.name ?? "shot.png",
        path: entry.path ?? "",
        note: entry.note ?? "",
        skipped: entry.skipped ?? false,
      })),
    };
  }

  it("only counts server-confirmed paths as uploaded", () => {
    expect(
      resolveComposerAttachmentUploadOutcome(
        result([{ name: "shot.png", path: "/srv/a.png", note: "已压缩" }]),
        "shot.png",
      ),
    ).toEqual({ status: "uploaded", remotePath: "/srv/a.png", remoteNote: "已压缩" });

    expect(
      resolveComposerAttachmentUploadOutcome(
        result([{ name: "shot.png", skipped: true, note: "不是可识别的图片" }]),
        "shot.png",
      ),
    ).toEqual({ status: "error", errorKind: "rejected", error: "不是可识别的图片" });
  });

  it("falls back to the single entry and reports missing entries honestly", () => {
    expect(
      resolveComposerAttachmentUploadOutcome(
        result([{ name: "renamed.png", path: "/srv/b.png" }]),
        "shot.png",
      ),
    ).toMatchObject({ status: "uploaded", remotePath: "/srv/b.png" });
    expect(resolveComposerAttachmentUploadOutcome(result([]), "shot.png")).toEqual({
      status: "error",
      errorKind: "rejected",
      error: "",
    });
  });

  it("derives sendable paths and unsettled counts from status", () => {
    const list = [
      uploaded({ id: "a", status: "uploaded", remotePath: "/srv/a.png" }),
      uploaded({ id: "b", status: "uploading" }),
      uploaded({ id: "c", status: "error", error: "boom", errorKind: "failed" }),
      uploaded({ id: "d", status: "uploaded", remotePath: "" }),
    ];

    expect(composerAttachmentUploadedPaths(list)).toEqual(["/srv/a.png"]);
    expect(countUnsettledComposerAttachments(list)).toBe(2);
    expect(hasUnsettledComposerAttachments(list)).toBe(true);
    expect(hasUnsettledComposerAttachments([list[0]])).toBe(false);
  });
});

describe("formatComposerAttachmentSize", () => {
  it("formats bytes, KB and MB without locale dependence", () => {
    expect(formatComposerAttachmentSize(0)).toBe("0 B");
    expect(formatComposerAttachmentSize(512)).toBe("512 B");
    expect(formatComposerAttachmentSize(2048)).toBe("2 KB");
    expect(formatComposerAttachmentSize(1024 * 1024 * 1.25)).toBe("1.3 MB");
  });
});
