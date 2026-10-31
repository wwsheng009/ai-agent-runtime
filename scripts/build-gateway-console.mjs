#!/usr/bin/env node
// 把 console-src/ 下按功能域拆分的片段，按 manifest.json 的顺序拼回
// backend/cmd/gateway/console/app.js。
//
// 为什么还是单文件产物：app.js 是 go:embed 的服务入口，index.html 的
// `./app.js?v=N`、sw.js 的 CACHE/SHELL、以及 console_assets_test.go 的版本
// 一致性断言都绑定这个 URL；拆成多个运行时文件会同时改动缓存与版本契约。
// 所以这里做的是「源码按功能拆分 + 产物确定性拼装」：拼装结果与原单文件逐字节
// 相同（本脚本 --check 与 TestConsoleAppMatchesSplitSources 双向守住漂移）。
//
// 用法：
//   node scripts/build-gateway-console.mjs           # 重新生成 app.js
//   node scripts/build-gateway-console.mjs --check   # 只校验（CI/提交前）
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const SRC_DIR = path.join(REPO_ROOT, "backend", "cmd", "gateway", "console-src");
const OUT = path.join(REPO_ROOT, "backend", "cmd", "gateway", "console", "app.js");
const MANIFEST = path.join(SRC_DIR, "manifest.json");

// 单文件 IIFE 的固定头尾；片段内容夹在中间，拼接后与拆分的源文件完全一致。
const PROLOGUE = "(function () {\n";
const EPILOGUE = "})();\n";

function build() {
  const manifest = JSON.parse(fs.readFileSync(MANIFEST, "utf8"));
  const files = manifest && manifest.files;
  if (!Array.isArray(files) || files.length === 0) {
    throw new Error("manifest.json 缺少 files 列表");
  }
  let out = PROLOGUE;
  for (const name of files) {
    const full = path.join(SRC_DIR, name);
    if (!fs.existsSync(full)) {
      throw new Error("缺少片段: " + name);
    }
    const body = fs.readFileSync(full, "utf8");
    if (body.trim() === "") {
      throw new Error("片段为空: " + name);
    }
    out += body;
  }
  return out + EPILOGUE;
}

const checkOnly = process.argv.includes("--check");
const rebuilt = build();
const current = fs.existsSync(OUT) ? fs.readFileSync(OUT, "utf8") : null;

if (checkOnly) {
  if (current !== rebuilt) {
    console.error("console/app.js 与 console-src/ 不一致：请运行 node scripts/build-gateway-console.mjs");
    process.exit(1);
  }
  const manifest = JSON.parse(fs.readFileSync(MANIFEST, "utf8"));
  console.log("ok: console/app.js 与 " + manifest.files.length + " 个片段一致");
  process.exit(0);
}

if (current === rebuilt) {
  const manifest = JSON.parse(fs.readFileSync(MANIFEST, "utf8"));
  console.log("no changes: " + manifest.files.length + " 个片段 -> console/app.js");
  process.exit(0);
}
fs.writeFileSync(OUT, rebuilt, "utf8");
const manifest = JSON.parse(fs.readFileSync(MANIFEST, "utf8"));
console.log("rebuilt console/app.js from " + manifest.files.length + " fragments (" + Buffer.byteLength(rebuilt) + " bytes)");