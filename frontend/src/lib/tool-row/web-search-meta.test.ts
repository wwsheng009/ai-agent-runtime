import { describe, expect, it } from "vitest";

import { parseWebSearchMetaFromCandidates, webSearchSourceDomain } from "./web-search-meta";

describe("parseWebSearchMetaFromCandidates", () => {
  it("解析 sources / has_answer / truncated / answer_queries", () => {
    const meta = parseWebSearchMetaFromCandidates([
      {
        sources: [
          { url: "https://a.example/1", title: "A", snippet: "s", published_at: "2026-10-01" },
          { url: "https://b.example/2", publishedAt: "2026-10-02" },
        ],
        has_answer: true,
        truncated: true,
        answer_queries: ["q1", " q2 ", 3, ""],
      },
    ]);
    expect(meta).not.toBeNull();
    expect(meta?.sources).toEqual([
      { url: "https://a.example/1", title: "A", snippet: "s", publishedAt: "2026-10-01" },
      { url: "https://b.example/2", title: "", snippet: "", publishedAt: "2026-10-02" },
    ]);
    expect(meta?.hasAnswer).toBe(true);
    expect(meta?.truncated).toBe(true);
    expect(meta?.answerQueries).toEqual(["q1", "q2"]);
  });

  it("解析 quality_signals / quality / cancelled_queries（P3-2/P3-3 展示面）", () => {
    const meta = parseWebSearchMetaFromCandidates([
      {
        sources: [{ url: "https://a.example/1" }],
        quality: "low",
        quality_signals: { coverage: 0.4, domain_diversity: 0.6, bad: "x", nan: Number.NaN },
        cancelled_queries: ["c1", " c2 ", 7, ""],
      },
    ]);
    expect(meta?.quality).toBe("low");
    expect(meta?.qualitySignals).toEqual({ coverage: 0.4, domain_diversity: 0.6 });
    expect(meta?.cancelledQueries).toEqual(["c1", "c2"]);
  });

  it("仅 cancelled_queries 也可成立（失败结果没有 sources）", () => {
    const meta = parseWebSearchMetaFromCandidates([{ cancelled_queries: ["q1"] }]);
    expect(meta?.cancelledQueries).toEqual(["q1"]);
    expect(meta?.sources).toEqual([]);
    expect(meta?.qualitySignals).toBeNull();
  });

  it("quality 非字符串 / 信号类型不符时不进入视图", () => {
    const meta = parseWebSearchMetaFromCandidates([
      { sources: [{ url: "https://a.example/1" }], quality: 3, quality_signals: "nope" },
    ]);
    expect(meta?.quality).toBeNull();
    expect(meta?.qualitySignals).toBeNull();
  });

  it("畸形矩阵：非对象候选 / 字段类型不符 / URL 缺失 / 重复 URL 全部收窄", () => {
    expect(parseWebSearchMetaFromCandidates([null, "x", 42, []])).toBeNull();

    // 字段类型不符（sources 非数组、布尔非 true）→ 无可用信息。
    expect(
      parseWebSearchMetaFromCandidates([
        { sources: "nope", has_answer: "yes", truncated: "yes", answer_queries: "q" },
      ]),
    ).toBeNull();

    const filtered = parseWebSearchMetaFromCandidates([
      {
        sources: [
          { title: "无 URL" },
          null,
          { url: "  " },
          { url: "https://a.example/1" },
          { url: "https://a.example/1", title: "Dup" },
        ],
      },
    ]);
    expect(filtered?.sources).toHaveLength(1);
    expect(filtered?.sources[0].title).toBe("");
  });

  it("嵌套 payload 形态（tool.metadata）可命中", () => {
    const meta = parseWebSearchMetaFromCandidates([
      { tool: { metadata: { sources: [{ url: "https://nested.example/1" }] } } },
    ]);
    expect(meta?.sources[0].url).toBe("https://nested.example/1");
  });

  it("历史回放形状（message.metadata.tool_metadata）可命中（实测缺口回归）", () => {
    const meta = parseWebSearchMetaFromCandidates([
      {
        arguments_digest: "abc",
        turn_id: "turn_x",
        tool_metadata: {
          sources: [
            { url: "https://hist.example/1", title: "历史", snippet: "s" },
          ],
          quality_signals: { coverage: 0.25, domain_diversity: 1, snippet_presence: 1 },
          quality: "low",
          trust: "untrusted",
        },
      },
    ]);
    expect(meta?.sources[0].url).toBe("https://hist.example/1");
    expect(meta?.quality).toBe("low");
    expect(meta?.qualitySignals?.coverage).toBe(0.25);
  });

  it("answer 非空字符串兼容为 hasAnswer（无 sources 也成立）", () => {
    const meta = parseWebSearchMetaFromCandidates([{ answer: "摘要" }]);
    expect(meta?.hasAnswer).toBe(true);
    expect(meta?.sources).toEqual([]);
  });

  it("仅弱信号（truncated）不足以生成卡片：返回 null 回退文本", () => {
    expect(parseWebSearchMetaFromCandidates([{ truncated: true }])).toBeNull();
    expect(parseWebSearchMetaFromCandidates([{ sources: [] }])).toBeNull();
  });
});

describe("webSearchSourceDomain", () => {
  it("取主机名并去 www；非法 URL 回退原始文本", () => {
    expect(webSearchSourceDomain("https://www.example.com/a")).toBe("example.com");
    expect(webSearchSourceDomain("https://sub.example.com/a")).toBe("sub.example.com");
    expect(webSearchSourceDomain("not a url")).toBe("not a url");
    expect(webSearchSourceDomain("")).toBe("");
  });
});