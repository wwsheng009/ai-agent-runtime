/**
 * analyze-trajectory.mjs 验证（P3-3 验收：样例 JSONL 输出稳定指标；空/损坏行容错）。
 * 运行：node --test scripts/analyze-trajectory.test.mjs
 */
import { readFileSync } from "node:fs";
import { test } from "node:test";
import assert from "node:assert/strict";

import {
  analyzeTrajectoryEntries,
  analyzeTrajectoryJsonl,
  parseTrajectoryJsonl,
} from "./analyze-trajectory.mjs";

const fixturePath = new URL("./fixtures/trajectory-sample.jsonl", import.meta.url);

test("样例 JSONL：TTFT / 工具耗时 / 重试 / token / reasoning 占比全部稳定", () => {
  const text = readFileSync(fixturePath, "utf8");
  const result = analyzeTrajectoryJsonl(text);

  // TTFT：meta 08:00:00.000 → 首个 text chunk 08:00:01.500 = 1500ms
  assert.equal(result.ttftMs, 1500);

  // 工具：两次 lookup（tool-1 500ms、tool-2 200ms）
  assert.equal(result.tools.callCount, 1); // 唯一工具名
  assert.equal(result.tools.completedCount, 2);
  assert.equal(result.tools.openCount, 0);
  assert.equal(result.tools.totalDurationMs, 700);
  assert.equal(result.tools.avgDurationMs, 350);

  // 重试：1 次 fallback、1 次 route_attempted、lookup 重复 2 次
  assert.equal(result.retries.fallbackCount, 1);
  assert.equal(result.retries.routeAttemptedCount, 1);
  assert.equal(result.retries.totalOrchestrations, 2);
  assert.deepEqual(result.retries.repeatedToolNames, [{ name: "lookup", count: 2 }]);

  // token 分布：两个 done 的 usage 汇总
  assert.equal(result.tokens.promptTokens, 160);
  assert.equal(result.tokens.completionTokens, 90);
  assert.equal(result.tokens.totalTokens, 250);
  assert.equal(result.tokens.cachedTokens, 10);
  assert.equal(result.tokens.reasoningTokens, 20);

  // reasoning 占比：30 / (30 + 25) = 0.5455
  assert.equal(result.reasoning.reasoningChars, 30);
  assert.equal(result.reasoning.textChars, 25);
  assert.ok(Math.abs(result.reasoning.ratio - 30 / 55) < 1e-9);

  // 事件计数与损坏行（12 条合法 JSON + 1 条损坏行）
  assert.equal(result.summary.eventCount, 12);
  assert.equal(result.skippedLines, 1);
  assert.equal(result.summary.doneCount, 2);
  assert.ok(result.summary.durationMs > 0);
});

test("空/损坏输入容错：不抛异常，事件为 0", () => {
  // 空行/损坏行跳过；{} 是合法对象（字段缺失按 0 处理）计入事件。
  const result = analyzeTrajectoryJsonl("\nnot json\n{}\n");
  assert.equal(result.summary.eventCount, 1);
  assert.equal(result.skippedLines, 1);
  assert.equal(result.ttftMs, 0);
  assert.equal(result.tools.completedCount, 0);
  assert.equal(result.retries.fallbackCount, 0);
  assert.equal(result.tokens.totalTokens, 0);
  assert.equal(result.reasoning.ratio, 0);
});

test("parseTrajectoryJsonl：只跳过损坏行，保留合法行", () => {
  const { entries, skipped } = parseTrajectoryJsonl(
    '{"seq":1,"kind":"meta"}\nbroken\n{"seq":2,"kind":"chunk"}\n',
  );
  assert.equal(skipped, 1);
  assert.equal(entries.length, 2);
  assert.equal(entries[0].kind, "meta");
});

test("analyzeTrajectoryEntries：无 done 时 orchestration 计数为 0", () => {
  const result = analyzeTrajectoryEntries([
    { seq: 1, ts: "2026-08-16T08:00:00.000Z", kind: "chunk", payload: { type: "text", content: "hi" } },
  ]);
  assert.equal(result.retries.totalOrchestrations, 0);
  assert.equal(result.tokens.totalTokens, 0);
  assert.equal(result.ttftMs, 0);
});
