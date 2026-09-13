#!/usr/bin/env node
// P0-1 卫生门禁：禁止在 frontend 源码树内留下 *.bak 文件或 .backups/ 目录。
//
// 背景（2026-09-13 复核）：P0-1 的「src 下 .bak=0」验收在工作区被回退——
// 复核时 src 下有 52 个 .bak / 17 个 .backups/（frontend 全域 72 / 19），
// 且因 .gitignore 屏蔽，`git status` 完全看不到。本脚本把该约束变成
// 可执行门禁：`npm run verify:clean`，并已并入 `npm run lint`。
//
// 口径：只扫描 frontend 源码树；node_modules / dist / .artifacts / .tmp /
// test-results / playwright-report 属于设计上的忽略目录，不参与判定。

import { readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, "..");

const SKIP_DIRS = new Set([
  "node_modules",
  "dist",
  ".artifacts",
  ".tmp",
  "test-results",
  "playwright-report",
]);

const offenders = [];
let scanned = 0;

function walk(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);

    if (entry.isDirectory()) {
      if (SKIP_DIRS.has(entry.name)) {
        continue;
      }
      if (entry.name === ".backups") {
        offenders.push(`${path.relative(root, full)}/`);
        continue;
      }
      walk(full);
      continue;
    }

    scanned += 1;
    if (entry.name.endsWith(".bak")) {
      offenders.push(path.relative(root, full));
    }
  }
}

walk(root);

if (offenders.length > 0) {
  console.error(
    `[verify-no-backups] 发现 ${offenders.length} 处本地备份残留（P0-1 约束：源码树内不得有 *.bak / .backups/）：`,
  );
  for (const item of offenders.sort()) {
    console.error(`  - ${item}`);
  }
  console.error(
    "[verify-no-backups] 请删除这些本地备份（源文件缺失时先从 git 恢复）后重试。",
  );
  process.exit(1);
}

console.log(
  `[verify-no-backups] OK（扫描 ${scanned} 个文件，0 处 *.bak / .backups 残留）`,
);
