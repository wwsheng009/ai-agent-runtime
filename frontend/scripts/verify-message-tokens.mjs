#!/usr/bin/env node
// 批次 E2 门禁（方案 §9）：消息渲染组件内不得出现字面色值 / 内联渐变。
//
// 背景：§8.2 约定「颜色一律走既有语义 token（--foreground / --muted-foreground /
// --border / --surface-* / --accent-*）」，缺少语义 token 时先在
// `src/styles/globals/tokens.css` 增补语义别名，禁止在组件里写字面色值或
// `rgba(...)` / `oklab(...)` 渐变。现状（改造前）命中点：assistant-message-card /
// user-message-bubble / history-context-message-card 的内联 rgba 渐变。
//
// 口径：
// - 扫描范围 = 消息渲染面（见 SCOPE），递归 .ts/.tsx，排除 *.test.ts(x)（用例注释
//   与断言字符串允许出现参考站色值）；
// - 命中模式 = `rgb(` / `rgba(` / `oklab(` / `oklch(` / 6 位或 8 位 hex 字面量；
//   3/4 位缩写的 hex 本仓无存量，暂不作为判定口径（如需收口再收紧）；
// - 主题/规范文件（styles/globals/*）不在此门禁内：那里正是 token 的定义处。

import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, "..");
const workspaceComponents = path.join(root, "src", "components", "workspace");

/** 消息渲染面：目录递归 + 顶层 message-*.tsx / chat-process-row.tsx。 */
const SCOPE_DIRS = [
  path.join(workspaceComponents, "message-list"),
  path.join(workspaceComponents, "message-markdown"),
  path.join(workspaceComponents, "message-markdown-streaming"),
  path.join(workspaceComponents, "tool-row"),
];

const PATTERN = /rgba?\(|oklab\(|oklch\(|#[0-9a-fA-F]{6}(?![0-9a-fA-F])|#[0-9a-fA-F]{8}(?![0-9a-fA-F])/;

const offenders = [];
let scanned = 0;

function isTestFile(name) {
  return /\.test\.tsx?$/.test(name);
}

function scanFile(full) {
  scanned += 1;
  const lines = readFileSync(full, "utf8").split(/\r?\n/);
  lines.forEach((line, index) => {
    if (PATTERN.test(line)) {
      offenders.push({
        file: path.relative(root, full),
        line: index + 1,
        text: line.trim(),
      });
    }
  });
}

function walk(dir) {
  let entries;
  try {
    entries = readdirSync(dir, { withFileTypes: true });
  } catch {
    return;
  }
  for (const entry of entries) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (entry.name === "node_modules" || entry.name === ".backups") {
        continue;
      }
      walk(full);
      continue;
    }
    if (!/\.tsx?$/.test(entry.name) || isTestFile(entry.name)) {
      continue;
    }
    scanFile(full);
  }
}

for (const dir of SCOPE_DIRS) {
  walk(dir);
}
for (const entry of readdirSync(workspaceComponents, { withFileTypes: true })) {
  if (!entry.isFile() || !/\.tsx?$/.test(entry.name) || isTestFile(entry.name)) {
    continue;
  }
  if (entry.name.startsWith("message-") || entry.name === "chat-process-row.tsx") {
    scanFile(path.join(workspaceComponents, entry.name));
  }
}

if (offenders.length > 0) {
  console.error(
    `[verify-message-tokens] 发现 ${offenders.length} 处字面色值/内联渐变（§9 E2：消息渲染面必须走语义 token）：`,
  );
  for (const item of offenders) {
    console.error(`  - ${item.file}:${item.line}  ${item.text}`);
  }
  console.error(
    "[verify-message-tokens] 请改用语义 token；缺语义别名时先在 src/styles/globals/tokens.css 增补。",
  );
  process.exit(1);
}

console.log(
  `[verify-message-tokens] OK（扫描 ${scanned} 个文件，0 处字面色值/内联渐变）`,
);
