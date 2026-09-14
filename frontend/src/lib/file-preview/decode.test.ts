// P2-1A：文件预览解码层单测（base64 / 文本 / 空文件 / 二进制判定 / 字节格式化）。

import { describe, expect, it } from "vitest";

import {
  countPreviewLines,
  decodeBase64Bytes,
  decodeFilePreview,
  formatByteSize,
} from "@/lib/file-preview/decode";

function toBase64(bytes: number[]): string {
  return btoa(String.fromCharCode(...bytes));
}

function toBase64Text(text: string): string {
  return toBase64([...new TextEncoder().encode(text)]);
}

describe("decodeBase64Bytes", () => {
  it("解码标准 base64 并容忍空白", () => {
    expect([...decodeBase64Bytes(toBase64Text("hello"))]).toEqual([104, 101, 108, 108, 111]);
    expect([...decodeBase64Bytes("aGVs\nbG8=")]).toEqual([104, 101, 108, 108, 111]);
    expect(decodeBase64Bytes("")).toHaveLength(0);
  });

  it("长度或字符不合法即抛错", () => {
    expect(() => decodeBase64Bytes("aGVsbG8")).toThrow(/not valid base64/);
    expect(() => decodeBase64Bytes("****")).toThrow(/not valid base64/);
  });
});

describe("countPreviewLines", () => {
  it("末行换行不计入新行，空文本为 0 行", () => {
    expect(countPreviewLines("")).toBe(0);
    expect(countPreviewLines("\n")).toBe(0);
    expect(countPreviewLines("a")).toBe(1);
    expect(countPreviewLines("a\nb")).toBe(2);
    expect(countPreviewLines("a\r\nb\n")).toBe(2);
  });
});

describe("decodeFilePreview", () => {
  it("UTF-8 文本按行数呈现，BOM 被剥离", () => {
    expect(decodeFilePreview(toBase64Text("第一行\nsecond\n"))).toEqual({
      kind: "text",
      text: "第一行\nsecond\n",
      lineCount: 2,
    });
    expect(decodeFilePreview(toBase64Text("\uFEFFhello"))).toEqual({
      kind: "text",
      text: "hello",
      lineCount: 1,
    });
  });

  it("空文件如实标记 empty", () => {
    expect(decodeFilePreview("")).toEqual({ kind: "empty" });
  });

  it("含 NUL 字节判定二进制", () => {
    expect(decodeFilePreview(toBase64([0x50, 0x4b, 0x00, 0x01]))).toEqual({
      kind: "binary",
      reason: "nul-byte",
    });
  });

  it("非 UTF-8 序列判定二进制，不硬解成乱码文本", () => {
    expect(decodeFilePreview(toBase64([0xff, 0xfe, 0x41]))).toEqual({
      kind: "binary",
      reason: "invalid-utf8",
    });
  });
});

describe("formatByteSize", () => {
  it("按 B / KB / MB 展示；非法输入返回空串", () => {
    expect(formatByteSize(0)).toBe("0 B");
    expect(formatByteSize(999)).toBe("999 B");
    expect(formatByteSize(1024)).toBe("1.0 KB");
    expect(formatByteSize(1_000_000)).toBe("977 KB");
    expect(formatByteSize(2 * 1024 * 1024)).toBe("2.0 MB");
    expect(formatByteSize(Number.NaN)).toBe("");
    expect(formatByteSize(-1)).toBe("");
  });
});
