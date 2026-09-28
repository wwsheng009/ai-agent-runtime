// P2-2 预算门禁：对探针报告跑红线断言（先校准后收紧的「收紧」执行端）。
//
// 阈值来源：docs/plan/frontend-deepseek-harness-streaming-render-performance-plan.md
// §11 P2-2 预算草案（2026-09-28 基线：40 轮 / 10s 流式窗口——帧 p95=17ms、
// >100ms 共 13 帧；长任务 19 次 / max 189ms；Script 2380.6ms / Recalc 2462.8ms /
// Layout 625.8ms / Layout 257 次；mutations 11409（/frame p95=53）；removals 3977）。
// 红灯线 = 基线 ×~2；每轮优化后复测并下调。
//
// 报告命名：zz-perf-probe → `.artifacts/perf/${PERF_TAG}.json`（结构见其落盘代码）；
// zz-reconnect-probe → `.artifacts/perf/reconnect-${PERF_TAG}.json`。
// 只校验 `baseline-*` 与 `reconnect-*` 前缀（CI 传 PERF_TAG=baseline-ci / ci）；
// 其余报告（typewriter 等旧探针）跳过，避免跨探针结构误判。
//
// 用法：node e2e/budget-check.mjs [报告目录]（默认 .artifacts/perf）
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

const DIR = join(process.cwd(), process.argv[2] ?? ".artifacts/perf");

const RECOGNIZED = ["baseline-", "reconnect-"];

const BUDGET = {
  // 长会话流式（zz-perf-probe，40 轮场景）
  baseline: {
    "frames.p95": 20,
    "frames.over100ms": 30,
    "longTasks.count": 40,
    "longTasks.maxMs": 250,
    "cdpMetricsDelta.ScriptDuration": 4000,
    "cdpMetricsDelta.RecalcStyleDuration": 4000,
    "cdpMetricsDelta.LayoutDuration": 4000,
    "cdpMetricsDelta.LayoutCount": 400,
    "domMutations.total": 20000,
    "domMutations.perFrame.p95": 80,
    "domMutations.removals.count": 8000,
  },
  // 重连窗口（zz-reconnect-probe，8 轮场景；窗口短，帧/长任务上限更严）
  reconnect: {
    "frames.p95": 20,
    "frames.over100": 10,
    "phases.pre.count": 20,
    "phases.pre.maxMs": 250,
    "phases.recovery.count": 20,
    "phases.recovery.maxMs": 250,
    "phases.post.count": 20,
    "phases.post.maxMs": 250,
  },
};

function pick(report, path) {
  let cur = report;
  for (const part of path.split(".")) {
    if (cur == null) return undefined;
    cur = cur[part];
  }
  return cur;
}

const files = readdirSync(DIR).filter(
  (name) => RECOGNIZED.some((prefix) => name.startsWith(prefix)) && name.endsWith(".json"),
);
const skipped = readdirSync(DIR).filter(
  (name) =>
    name.endsWith(".json") &&
    !name.startsWith(".last-run") &&
    !RECOGNIZED.some((prefix) => name.startsWith(prefix)),
);
const failures = [];
const evaluated = [];

for (const file of files) {
  const report = JSON.parse(readFileSync(join(DIR, file), "utf8"));
  const kind = file.startsWith("reconnect-") ? "reconnect" : "baseline";
  const budget = BUDGET[kind];
  for (const [path, limit] of Object.entries(budget)) {
    const actual = pick(report, path);
    if (typeof actual !== "number") {
      failures.push(`${file} [${kind}] ${path}: 报告缺字段（got ${actual}）`);
      continue;
    }
    evaluated.push(`${file} ${path}=${actual}${actual > limit ? " ⚠️超线" : ""}`);
    if (actual > limit) {
      failures.push(`${file} [${kind}] ${path} ${actual} > 预算 ${limit}`);
    }
  }
}

for (const line of evaluated) console.log(`[budget] ${line}`);
for (const name of skipped) console.log(`[budget] skip（非基准报告）: ${name}`);
if (files.length === 0) {
  console.error("[budget] FAIL 未找到任何基准报告（需要 baseline-* / reconnect-* 前缀）");
  process.exit(2);
}
if (failures.length > 0) {
  console.error(`[budget] FAIL ${failures.length} 项超预算：`);
  for (const f of failures) console.error(`  - ${f}`);
  process.exit(1);
}
console.log(`[budget] ok（${files.length} 份报告全部在预算内）`);