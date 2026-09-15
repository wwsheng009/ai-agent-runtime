// zz-profile-report：zz-profile-audit.mjs 的配套分析器（一次性诊断脚本）。
//
// 输入：CDP 采集的 .cpuprofile（.tmp/profile-<mode>.cpuprofile）
// 输出：
//   * 按「应用源码文件」聚合的**包含时间**（total time = self + 全部后代），
//     用来回答「哪棵组件子树在买单」——纯 self 聚合会被大量细碎组件函数摊平；
//   * Top 函数的总耗时（含后代），并标注它是应用代码还是 node_modules；
//   * 元素创建（jsxDEV / createElement）的**调用者链条**，用来定位「谁在每次提交里
//     重新创建整棵树的元素」。
//
// 跑法（cwd = frontend）：node e2e/zz-profile-report.mjs .tmp/profile-plain.cpuprofile

import fs from "node:fs";

const file = process.argv[2];
if (!file) {
  process.stdout.write("usage: node e2e/zz-profile-report.mjs <profile.cpuprofile>\n");
  process.exit(2);
}

const profile = JSON.parse(fs.readFileSync(file, "utf8"));
const nodes = profile.nodes ?? [];
const byId = new Map(nodes.map((node) => [node.id, node]));
const parentOf = new Map();
for (const node of nodes) {
  for (const childId of node.children ?? []) {
    parentOf.set(childId, node.id);
  }
}

const samples = profile.samples ?? [];
const deltas = profile.timeDeltas ?? [];
const selfUs = new Map();
for (let i = 0; i < samples.length; i += 1) {
  const id = samples[i];
  selfUs.set(id, (selfUs.get(id) ?? 0) + (deltas[i] ?? 0));
}

const totalUs = [...selfUs.values()].reduce((sum, value) => sum + value, 0);

function frameOf(id) {
  const node = byId.get(id);
  if (!node) return { fn: "(unknown)", url: "", line: 0 };
  const frame = node.callFrame ?? {};
  return {
    fn: frame.functionName || "(anonymous)",
    url: frame.url ?? "",
    line: (frame.lineNumber ?? 0) + 1,
  };
}

function classify(url) {
  if (!url) return "native";
  if (url.includes("/src/")) return "app";
  if (url.includes("node_modules")) return "vendor";
  if (url.startsWith("http")) return "vendor";
  return "other";
}

function fileLabel(url) {
  if (!url) return "(native)";
  try {
    const parsed = new URL(url);
    const clean = decodeURIComponent(parsed.pathname);
    const marker = "/src/";
    const index = clean.indexOf(marker);
    if (index >= 0) return `src/${clean.slice(index + marker.length)}`;
    return clean.split("/").pop() ?? clean;
  } catch {
    return url;
  }
}

// --- 包含时间：自底向上把 self 累加到每个祖先（含自身） -----------------------

const inclusiveUs = new Map();
for (const [id, value] of selfUs) {
  let current = id;
  const guard = new Set();
  while (current !== undefined && !guard.has(current)) {
    guard.add(current);
    inclusiveUs.set(current, (inclusiveUs.get(current) ?? 0) + value);
    current = parentOf.get(current);
  }
}

// 同一函数的多份调用点合并（按 函数名@文件:行）。
const inclusiveByFn = new Map();
for (const [id, value] of inclusiveUs) {
  const frame = frameOf(id);
  const key = `${frame.fn} @ ${fileLabel(frame.url)}:${frame.line}`;
  const entry = inclusiveByFn.get(key) ?? { us: 0, kind: classify(frame.url), url: frame.url };
  entry.us += value;
  inclusiveByFn.set(key, entry);
}

const appTotalByFile = new Map();
for (const [, entry] of inclusiveByFn) {
  if (entry.kind !== "app") continue;
  const label = fileLabel(entry.url);
  appTotalByFile.set(label, (appTotalByFile.get(label) ?? 0) + entry.us);
}

function printSection(title) {
  process.stdout.write(`\n================ ${title} ================\n`);
}

process.stdout.write(
  `profile=${file}\nsamples=${samples.length}  window=${((profile.endTime - profile.startTime) / 1000).toFixed(0)}ms  self_total=${(totalUs / 1000).toFixed(0)}ms\n`,
);

// 先按同一 key 预算 self，避免在 Top-N 排序里对每个 key 重扫全部采样。
const selfByKey = new Map();
for (const [id, value] of selfUs) {
  const frame = frameOf(id);
  const key = `${frame.fn} @ ${fileLabel(frame.url)}:${frame.line}`;
  selfByKey.set(key, (selfByKey.get(key) ?? 0) + value);
}

printSection("应用源码：包含时间 Top 25（total = self + 后代）");
process.stdout.write("total_ms\tself_ms\tkind\tfn @ file:line\n");
const topInclusive = [...inclusiveByFn.entries()]
  .map(([key, entry]) => ({
    key,
    us: entry.us,
    selfUs: selfByKey.get(key) ?? 0,
    kind: entry.kind,
  }))
  .sort((left, right) => right.us - left.us);
for (const row of topInclusive.filter((row) => row.kind === "app").slice(0, 25)) {
  process.stdout.write(
    `${(row.us / 1000).toFixed(1)}\t${(row.selfUs / 1000).toFixed(1)}\t${row.kind}\t${row.key}\n`,
  );
}

printSection("按文件聚合：应用源码包含时间 Top 25");
process.stdout.write("total_ms\tpct_of_self_total\tfile\n");
for (const [label, us] of [...appTotalByFile.entries()]
  .sort((left, right) => right[1] - left[1])
  .slice(0, 25)) {
  process.stdout.write(`${(us / 1000).toFixed(1)}\t${((us / totalUs) * 100).toFixed(1)}\t${label}\n`);
}

printSection("按文件聚合：vendor / native 包含时间 Top 15");
const vendorByFile = new Map();
for (const [, entry] of inclusiveByFn) {
  if (entry.kind === "app") continue;
  const label = entry.kind === "native" ? "(native)" : fileLabel(entry.url);
  vendorByFile.set(label, (vendorByFile.get(label) ?? 0) + entry.us);
}
process.stdout.write("total_ms\tpct_of_self_total\tfile\n");
for (const [label, us] of [...vendorByFile.entries()]
  .sort((left, right) => right[1] - left[1])
  .slice(0, 15)) {
  process.stdout.write(`${(us / 1000).toFixed(1)}\t${((us / totalUs) * 100).toFixed(1)}\t${label}\n`);
}

// --- 元素创建的调用者链条 ---------------------------------------------------

for (const target of ["jsxDEV", "jsxDEVImpl", "createElement", "ReactElement"]) {
  const chains = new Map();
  for (const [id, value] of selfUs) {
    if (frameOf(id).fn !== target) continue;
    const chain = [];
    let current = parentOf.get(id);
    let depth = 0;
    while (current !== undefined && depth < 6) {
      const frame = frameOf(current);
      chain.push(`${frame.fn}@${fileLabel(frame.url)}:${frame.line}`);
      current = parentOf.get(current);
      depth += 1;
    }
    const key = chain.join(" <- ");
    chains.set(key, (chains.get(key) ?? 0) + value);
  }
  if (chains.size === 0) continue;
  printSection(`${target}：调用者链条 Top 12`);
  process.stdout.write("self_ms\tpct\tchain (caller <- ...)\n");
  for (const [chain, us] of [...chains.entries()]
    .sort((left, right) => right[1] - left[1])
    .slice(0, 12)) {
    process.stdout.write(`${(us / 1000).toFixed(1)}\t${((us / totalUs) * 100).toFixed(1)}\t${chain}\n`);
  }
}
