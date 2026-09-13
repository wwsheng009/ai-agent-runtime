import { describe, expect, it } from "vitest";

import {
  classifyComposerAttachment,
  composerAttachmentIdentity,
  createComposerAttachment,
  formatComposerAttachmentSize,
  hasComposerFilePayload,
  planComposerAttachmentAdd,
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
    status: "pending",
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
  it("marks local drafts as pending and never fabricates an upload", () => {
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
      status: "pending",
    });
    expect(created.file).toBe(file);
  });

  it("leaves non-image previews empty", () => {
    const file = new File([new Uint8Array(4)], "notes.txt", { type: "text/plain" });
    expect(createComposerAttachment(file, { id: "a-2" }).previewUrl).toBeNull();
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
