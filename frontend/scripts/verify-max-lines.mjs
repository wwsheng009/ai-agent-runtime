#!/usr/bin/env node
// P0-2 门禁：frontend/src 下单文件非空行数上限（默认 500），防止「> 500 行文件 = 0」
// 验收再次回退。
//
// 背景（2026-09-13 复检）：P0-2 验收在 P2-1A 接线后回退——`pages/usage-analytics/quota.tsx`
// 559、`pages/workspace-page.tsx` 512（非空行口径），而该口径此前无门禁看护，回归无人拦截；
// 本脚本把口径变成可执行门禁：`npm run verify:lines`，并已并入 `npm run lint`。
//
// 口径：
// - 统计 frontend/src 下 .ts / .tsx（含测试）的**非空行**数（与 P0-2 复核口径一致）；
// - 阈值 500：≤ 500 通过，> 500 失败；
// - i18n 词典（src/i18n/resources/**）是纯数据文件，豁免判定（仍参与扫描计数）；
// - 只读门禁，不改写任何文件；超限时请拆分（目录 barrel / 提取 hook、共享模块或原子组件）。

import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, "..");
const srcRoot = path.join(root, "src");

const MAX_LINES = 500;
const SKIP_DIRS = new Set(["node_modules"]);
// 词典类数据文件：按 i18n 资源目录整树豁免（内容为翻译键值，不参与行数预算）。
const EXEMPT_DIRS = [path.join(srcRoot, "i18n", "resources")];

const offenders = [];
let scanned = 0;
let exempt = 0;
let largest = { file: "", lines: 0 };

function countNonBlankLines(text) {
  return text.split(/\r?\n/).filter((line) => line.trim() !== "").length;
}

function walk(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);

    if (entry.isDirectory()) {
      if (!SKIP_DIRS.has(entry.name)) {
        walk(full);
      }
      continue;
    }

    if (!/\.(ts|tsx)$/.test(entry.name)) {
      continue;
    }

    scanned += 1;

    if (EXEMPT_DIRS.some((prefix) => full.startsWith(prefix + path.sep))) {
      exempt += 1;
      continue;
    }

    const lines = countNonBlankLines(readFileSync(full, "utf8"));
    const relative = path.relative(root, full).split(path.sep).join("/");
    if (lines > largest.lines) {
      largest = { file: relative, lines };
    }
    if (lines > MAX_LINES) {
      offenders.push({ file: relative, lines });
    }
  }
}

walk(srcRoot);
offenders.sort((a, b) => b.lines - a.lines);

if (offenders.length > 0) {
  console.error(
    `[verify-max-lines] 发现 ${offenders.length} 个文件超过 ${MAX_LINES} 非空行（P0-2 约束：frontend/src 内 > ${MAX_LINES} 行文件 = 0）：`,
  );
  for (const item of offenders) {
    console.error(`  - ${item.file} = ${item.lines}`);
  }
  console.error(
    "[verify-max-lines] 请拆分超长文件（提取 hook / 共享模块 / 原子组件，或改为目录 barrel）后重试。",
  );
  process.exit(1);
}

console.log(
  `[verify-max-lines] OK（扫描 ${scanned} 个 .ts/.tsx，其中 ${exempt} 个 i18n 词典豁免；0 个 > ${MAX_LINES} 非空行，最大 ${largest.file} = ${largest.lines}）`,
);
