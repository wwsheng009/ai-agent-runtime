// 行为验证：js/event-sequence.js —— SSE 帧序号守卫（顺序 / 重复 / 丢帧 / 重连基线）。
// 运行：node scripts/verify-micro-web-event-sequence.mjs（纯逻辑，无需 DOM / 浏览器）
//
// 该模块是 sse.js 事件管理的唯一判据来源：
//   accept    正常递进（首帧建立基线）
//   duplicate seq <= 已见（重连重放 / 重复投递）→ 调用方丢弃，绝不重复渲染
//   gap       跳号（服务端队列满丢帧）→ 调用方重拉权威快照对账
//   reset     connected 帧：新连接序号从 1 重新开始，不复位会把整条新流误判成重复
import assert from "node:assert";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), "..");
const WEB_DIR = path.join(REPO_ROOT, "backend", "cmd", "aicli", "commands", "web", "js");
const mod = await import(pathToFileURL(path.join(WEB_DIR, "event-sequence.js")).href);

// ---- 1. 首帧建立基线，随后按 +1 递进 ----
var guard = mod.createEventSequenceGuard();
assert.strictEqual(guard.accept(1), "accept", "首帧应建立基线");
assert.strictEqual(guard.lastSeen(), 1);
assert.strictEqual(guard.accept(2), "accept");
assert.strictEqual(guard.accept(3), "accept");
assert.strictEqual(guard.lastSeen(), 3);

// ---- 2. 重复序号：判 duplicate 且不推进基线 ----
assert.strictEqual(guard.accept(3), "duplicate", "同一序号再次投递应判重复");
assert.strictEqual(guard.accept(2), "duplicate", "旧序号回放应判重复");
assert.strictEqual(guard.accept(1), "duplicate");
assert.strictEqual(guard.lastSeen(), 3, "重复帧不得推进基线");

// ---- 3. 跳号：判 gap 并推进基线，后续帧继续正常判定 ----
assert.strictEqual(guard.accept(9), "gap", "跳号应判丢帧");
assert.strictEqual(guard.lastSeen(), 9, "丢帧后基线应推进到本帧");
assert.strictEqual(guard.accept(10), "accept", "丢帧后的下一帧应恢复递进");
assert.strictEqual(guard.accept(10), "duplicate");

// ---- 4. 缺序号 / 非法序号：放行且不影响基线 ----
assert.strictEqual(guard.accept(undefined), "accept");
assert.strictEqual(guard.accept(0), "accept");
assert.strictEqual(guard.accept(""), "accept");
assert.strictEqual(guard.accept("abc"), "accept");
assert.strictEqual(guard.lastSeen(), 10, "无序号帧不得改变基线");

// ---- 5. 重连复位：新连接序号从 1 重新开始 ----
guard.reset(1);
assert.strictEqual(guard.lastSeen(), 1);
assert.strictEqual(guard.accept(2), "accept", "重连后序号 2 必须被接受（否则整条新流被丢弃）");
assert.strictEqual(guard.accept(2), "duplicate");

// ---- 6. 复位时缺序号（connected 无 _event）→ 下一帧重建基线 ----
guard.reset(0);
assert.strictEqual(guard.lastSeen(), 0);
assert.strictEqual(guard.accept(42), "accept", "无基线的首帧应重新建立基线");
assert.strictEqual(guard.lastSeen(), 42);

// ---- 7. 两个守卫互不干扰（多标签页/多连接各自计数）----
var a = mod.createEventSequenceGuard();
var b = mod.createEventSequenceGuard();
assert.strictEqual(a.accept(5), "accept");
assert.strictEqual(b.accept(1), "accept");
assert.strictEqual(a.accept(6), "accept");
assert.strictEqual(b.accept(2), "accept");

console.log("verify-micro-web-event-sequence: 全部断言通过");
console.log("  - 首帧基线 / 连续递进 / 重复序号丢弃 / 跳号判 gap");
console.log("  - 无序号帧放行 / connected 复位 / 守卫实例相互独立");
